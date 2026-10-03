package discovery

// TG 频道订阅 watcher 引擎（移植自旧仓库 internal/controllers/tg_channel_watcher.go）。
//
// 与旧仓库的差异（本次迁移的关键设计决策）：
//   - 旧仓库有独立的 CloudSubscription 模型（带 Keywords/TargetDir/Season/Wash/... 字段），
//     新仓库不新建模型，直接复用 DiscoverySubscription：TargetProvider 就是转发目标网盘
//     （123/guangya/pan139），与频道表 SourceType 一一对应；扩展字段（关键词、洗版、
//     回溯等）放在 sub.Pref（Preferences JSON）里，由 subPref 系列辅助函数读取。
//   - 目标目录不再由订阅指定，统一交给 TransferShareLink 内部用 TransferTargetDir(provider)
//     解析（即「影视发现-基础配置」里配的保存目录）。因此本文件不出现任何目录拼接逻辑。
//   - 通知：discovery 包内没有通知服务入口，且直接 import notifychannel 会造成循环依赖，
//     故实现为可注入的函数变量（TransferSuccessNotifyFn / TransferFailedNotifyFn），
//     由 wire 层绑定；未绑定时不发通知（只留审计记录），不影响转存主流程。
//   - 旧仓库依赖 123 驱动 ListShareDir 做「聚合帖防误转」的分享标题复核；新仓库驱动层
//     未暴露该能力，故改为可注入的 ShareTitleProbeFn（由 wire 层绑定 123 驱动能力）。
//     未绑定时按降级策略一律放行（详见 verifyShareTitleMatches）。

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"litepan/internal/discover/ddb"
	"litepan/internal/discover/tgchannel"
)

// channelWatchInterval TG 频道订阅引擎的增量轮询间隔。
const channelWatchInterval = 5 * time.Minute

// ChannelWatcherInterval 对外暴露轮询间隔（API 状态接口展示用）。
const ChannelWatcherInterval = channelWatchInterval

// 单频道抓取超时：常规增量 3 分钟；回溯要翻很多页，放宽到 15 分钟。
const (
	channelFetchTimeout        = 3 * time.Minute
	channelBackfillTimeout     = 15 * time.Minute
	channelBatchTimeout        = 10 * time.Minute
	channelDefaultBackfillPage = 50
)

// ---- 通知注入点（由 wire 层绑定；未绑定时静默跳过） ----

// TransferSuccessNotifyFn 转存成功通知注入点，由 wire 层绑定。
// 参数：订阅 ID、订阅标题、转存标题、目标网盘。
var TransferSuccessNotifyFn func(subscriptionID uint, title, shareTitle, provider string)

// TransferFailedNotifyFn 转存失败通知注入点，由 wire 层绑定。
var TransferFailedNotifyFn func(subscriptionID uint, title, shareURL, provider, reason string)

// ShareTitleProbeFn 「聚合帖防误转」用的分享顶级目录名探测注入点，由 wire 层绑定。
// 参数：分享 URL（含提取码）、提取码题面之外无需额外参数。返回分享内第一个文件名。
// 返回错误、空名称或未绑定本函数时，调用方一律放行（详见 verifyShareTitleMatches）。
var ShareTitleProbeFn func(ctx context.Context, shareURL, pwd string) (string, error)

// ---- 运行状态 ----

var (
	channelWorkerOnce sync.Once

	channelStatusMu      sync.Mutex
	channelLastRunAt     time.Time
	channelRunning       bool
	channelLastSummaries []string
)

// StartChannelWatcher 启动 TG 频道订阅引擎（幂等，可重复调用）。
// 首轮延迟 10s，让 discovery 其余初始化（目录预抓/订阅调度等）先完成，
// 避免启动瞬间多个 worker 抢同一把 SQLite 写锁。
func StartChannelWatcher(ctx context.Context) {
	channelWorkerOnce.Do(func() {
		go channelWatcherWorker(ctx)
	})
}

// channelWatcherWorker 引擎主循环：sleep 10s → 首轮 → 每 5 分钟一轮。
func channelWatcherWorker(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[discovery] TG 频道订阅引擎 panic：%v", r)
		}
	}()
	log.Printf("[discovery] TG 频道订阅引擎已启动，轮询间隔 %v", channelWatchInterval)

	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
	}

	runChannelWatchRound("")

	ticker := time.NewTicker(channelWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[discovery] TG 频道订阅引擎已停止")
			return
		case <-ticker.C:
			runChannelWatchRound("")
		}
	}
}

// runChannelWatchRound 执行一轮全量频道扫描，并把出错的 panic 兜在本轮内，
// 避免一轮异常把整个 worker goroutine 打死。
func runChannelWatchRound(sourceType string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[discovery] TG 频道订阅引擎本轮 panic：%v", r)
		}
	}()
	markChannelRoundStart()
	summaries, err := runAllSubscriptions(sourceType)
	markChannelRoundEnd(summaries)
	if err != nil {
		log.Printf("[discovery] TG 频道订阅引擎本轮执行失败：%v", err)
	}
}

func markChannelRoundStart() {
	channelStatusMu.Lock()
	channelRunning = true
	channelStatusMu.Unlock()
}

func markChannelRoundEnd(summaries []string) {
	channelStatusMu.Lock()
	channelRunning = false
	channelLastRunAt = time.Now()
	channelLastSummaries = summaries
	channelStatusMu.Unlock()
}

// ChannelWatchStatus 报告引擎运行状态（API 状态接口用）。
func ChannelWatchStatus() map[string]any {
	channelStatusMu.Lock()
	defer channelStatusMu.Unlock()
	out := map[string]any{
		"running":          channelRunning,
		"interval_seconds": int(channelWatchInterval.Seconds()),
		"last_run_at":      nil,
		"last_summaries":   append([]string{}, channelLastSummaries...),
	}
	if !channelLastRunAt.IsZero() {
		out["last_run_at"] = channelLastRunAt.Format(time.RFC3339)
	}
	return out
}

// RunAllChannelSubscriptions 立即执行一轮频道抓取（供 API「立即运行」按钮），
// sourceType 为空表示跑全部网盘。返回可直接展示给用户的汇总文案。
func RunAllChannelSubscriptions(sourceType string) (string, error) {
	sourceType = strings.ToLower(strings.TrimSpace(sourceType))
	markChannelRoundStart()
	summaries, err := runAllSubscriptions(sourceType)
	markChannelRoundEnd(summaries)
	if err != nil {
		return "", err
	}
	if len(summaries) == 0 {
		return "本轮无启用中的订阅或资源频道", nil
	}
	return strings.Join(summaries, "；"), nil
}

// runAllSubscriptions 按目标网盘分组执行：回溯订阅走单订阅路径，其余走「一扫多配」增量路径。
func runAllSubscriptions(sourceType string) ([]string, error) {
	subs, err := ListSubscriptions(boolPtr(true))
	if err != nil {
		return nil, fmt.Errorf("读取订阅失败：%w", err)
	}
	pending := make([]DiscoverySubscription, 0, len(subs))
	for _, sub := range subs {
		if sourceType != "" && normalizeProviderKey(sub.TargetProvider) != normalizeProviderKey(sourceType) {
			continue
		}
		pending = append(pending, sub)
	}
	if len(pending) == 0 {
		log.Printf("[discovery] TG 频道订阅：本轮无启用中的订阅")
		return nil, nil
	}

	// 按目标网盘分组：同一网盘的频道只需要抓一次，再分发给该网盘下的所有订阅（一扫多配）。
	groups := map[string][]DiscoverySubscription{}
	for _, sub := range pending {
		provider := normalizeProviderKey(sub.TargetProvider)
		if provider == "" {
			continue
		}
		groups[provider] = append(groups[provider], sub)
	}

	providers := make([]string, 0, len(groups))
	for provider := range groups {
		providers = append(providers, provider)
	}
	sortStrings(providers)

	summaries := make([]string, 0, len(providers))
	for _, provider := range providers {
		group := groups[provider]
		incremental := make([]DiscoverySubscription, 0, len(group))
		for _, sub := range group {
			// 回溯订阅语义与增量完全不同（忽略游标、限制页数、不推进游标），
			// 不能混进批量分发，否则会把整包历史帖当成新帖重复处理。
			if subPrefBool(sub, "backfill", false) {
				summary, _ := RunSubscriptionOnce(sub)
				summaries = append(summaries, summary)
				continue
			}
			incremental = append(incremental, sub)
		}
		if len(incremental) == 0 {
			continue
		}
		channels, err := ListEnabledChannels(provider)
		if err != nil {
			log.Printf("[discovery] TG 频道订阅：读取 %s 频道失败：%v", resourceProviderName(provider), err)
			continue
		}
		if len(channels) == 0 {
			log.Printf("[discovery] TG 频道订阅：%s 没有启用中的资源频道，请先「订阅频道」添加", resourceProviderName(provider))
			continue
		}
		for ci := range channels {
			if summary := runChannelBatchForSubs(incremental, &channels[ci]); summary != "" {
				summaries = append(summaries, summary)
			}
		}
		// 批量分发结束后统一结算每个订阅（更新运行时间、判断自动完结）。
		for si := range incremental {
			if finalizeSubscriptionRun(&incremental[si]) {
				summaries = append(summaries, fmt.Sprintf("订阅 #%d（%s）已自动完结并停用", incremental[si].ID, incremental[si].Title))
			}
		}
	}
	return summaries, nil
}

// runChannelBatchForSubs 抓一次频道帖，分发给同一网盘下的所有增量订阅。
func runChannelBatchForSubs(subs []DiscoverySubscription, ch *DiscoveryChannel) string {
	channelName := ch.ChannelName()
	stopID := strings.TrimSpace(ch.LastPostID)

	ctx, cancel := context.WithTimeout(context.Background(), channelBatchTimeout)
	defer cancel()

	// ★ 停机追赶：停机超过追赶窗口时不再从旧游标深翻全部积压（会一次性补转存打爆网盘），
	// 而是把游标直接提升到频道最新一页。被跳过的旧游标在存在回溯订阅时冻结保存，
	// 之后每轮回补一小段直到追上。必须在抓取之前判定，因为它可能改写 stopID 与游标。
	catchup := planChannelCatchup(ch, subs, 0)
	applyChannelCatchup(ch, &ctx, &stopID, catchup)

	posts, pages, err := tgchannel.ParseChannelPageRange(ctx, channelName, stopID, 100)
	if err != nil {
		// 部分失败：已抓到的帖照常分发（丢弃会让这些帖永远等不到下一轮），
		// 但本轮窗口不完整，下面的游标推进必须跳过。
		if len(posts) == 0 {
			log.Printf("[discovery] TG 频道订阅：频道 %s 抓取失败：%v", channelName, err)
			return fmt.Sprintf("频道 %s 抓取失败", channelName)
		}
		log.Printf("[discovery] TG 频道订阅：频道 %s 翻页中断（已得 %d 帖，翻 %d 页），本轮按部分结果处理且不推进游标：%v", channelName, len(posts), pages, err)
	}
	now := time.Now()
	if len(posts) == 0 {
		// 无新帖也要落 LastRunAt，前端才能显示「上次运行时间」。
		ch.LastRunAt = now
		// 追赶态下无新帖即已追平：清空冻结起点，避免下一轮继续按追赶窗口回补。
		syncChannelCheckpoints(ch, posts)
		if err := SaveChannel(ch); err != nil {
			log.Printf("[discovery] TG 频道订阅：频道 %s 保存运行时间失败：%v", channelName, err)
		}
		log.Printf("[discovery] TG 频道订阅：频道 %s 无新帖（翻 %d 页，分发给 %d 条订阅）", channelName, pages, len(subs))
		return fmt.Sprintf("频道 %s 无新帖（翻 %d 页）", channelName, pages)
	}

	batchCursor := ""
	allOK := true
	parts := make([]string, 0, len(subs))
	for si := range subs {
		sub := &subs[si]
		newMaxID, summary, ok := processChannelPostsForSub(sub, ch, ctx, posts, pages, stopID)
		if !ok {
			allOK = false
		}
		if summary != "" {
			parts = append(parts, summary)
		}
		// ★ 批次游标取各订阅新游标的「最小值」。
		// 原因：某个订阅转存失败时会把游标回退到自己最后一条失败帖，
		// 若这里取最大值，等于把失败帖永久越过，该订阅再也不会重试这批帖。
		// 取最小值可保证「最慢的那个订阅」也不会漏帖（共享游标只会因它而保守后退）。
		if newMaxID != "" && (batchCursor == "" || postIDGreater(batchCursor, newMaxID)) {
			batchCursor = newMaxID
		}
	}

	// 翻页中断（err != nil）时本轮窗口不完整：若把游标推到本轮最旧帖，中间没扫到的积压
	// 会被永久越过，故只在完整窗口（err == nil）时才推进。
	// 追赶态下同理：applyChannelCatchup 已把游标钉在最新帖，此处再推进只会前进游标而非越过积压。
	if err == nil && batchCursor != "" && postIDGreater(batchCursor, ch.LastPostID) {
		ch.LastPostID = batchCursor
	}
	// 追赶态下按本轮实际扫过的窗口推进冻结起点（追平则清空）。必须在游标推进之后调用：
	// advanceChannelCatchup 会与本轮游标比较以判断是否已进入最新一页范围。
	if err == nil {
		advanceChannelCatchup(ch, posts)
	}
	ch.LastRunAt = now
	if err := SaveChannel(ch); err != nil {
		log.Printf("[discovery] TG 频道订阅：频道 %s 游标保存失败：%v", channelName, err)
	}

	suffix := ""
	if !allOK {
		suffix = "，部分订阅存在失败"
	}
	log.Printf("[discovery] TG 频道订阅：频道 %s 本轮分发给 %d 条订阅完成（翻 %d 页，游标推进至 %s%s）",
		channelName, len(subs), pages, ch.LastPostID, suffix)
	return fmt.Sprintf("频道 %s：翻 %d 页，游标 %s%s", channelName, pages, ch.LastPostID, suffix)
}

// RunSubscriptionOnce 立即执行单个订阅（回溯订阅专用路径，也供 API「立即运行」）。
// 返回 (汇总文案, 是否全部成功)。
func RunSubscriptionOnce(sub DiscoverySubscription) (string, bool) {
	provider := normalizeProviderKey(sub.TargetProvider)
	if provider == "" {
		return fmt.Sprintf("订阅 #%d 未配置目标网盘", sub.ID), false
	}
	channels, err := ListEnabledChannels(provider)
	if err != nil {
		return fmt.Sprintf("订阅 #%d（%s）：读取频道失败：%v", sub.ID, sub.Title, err), false
	}
	if len(channels) == 0 {
		return fmt.Sprintf("订阅 #%d（%s）没有启用中的资源频道，请先「订阅频道」添加", sub.ID, sub.Title), false
	}

	parts := make([]string, 0, len(channels))
	allOK := true
	for ci := range channels {
		summary, ok := runChannelSubscriptionOnce(&sub, &channels[ci])
		if !ok {
			allOK = false
		}
		if summary != "" {
			parts = append(parts, summary)
		}
	}
	finished := finalizeSubscriptionRun(&sub)

	summary := fmt.Sprintf("订阅 #%d（%s）：%s", sub.ID, sub.Title, strings.Join(parts, "；"))
	if finished {
		summary += "；已自动完结（影片已收录完毕，订阅已停用）"
	}
	return summary, allOK
}

// finalizeSubscriptionRun 结算订阅：更新运行时间、兼容旧游标、判断自动完结。
// 返回 true 表示本次判定为完结并已停用订阅。
func finalizeSubscriptionRun(sub *DiscoverySubscription) bool {
	now := time.Now()
	sub.LastCheckedAt = &now

	// 兼容旧数据：早期订阅没有频道游标概念，这里把下次检查时间推到本刻，
	// 避免订阅调度 worker 与频道引擎同时抢跑同一个订阅。
	if sub.NextCheckAt == nil {
		sub.NextCheckAt = &now
	}
	if err := touchChannelSubscription(sub); err != nil {
		log.Printf("[discovery] TG 频道订阅：订阅 #%d 保存运行时间失败：%v", sub.ID, err)
	}

	if !subPrefBool(*sub, "auto_finish", false) {
		return false
	}
	if !subscriptionFinished(*sub) {
		return false
	}
	// 停用走直接更新：SaveSubscription 是面向前端的 upsert（要整包 payload），
	// worker 里只改 enabled 一列，直接更新可避免覆盖用户在 UI 的其它修改。
	sub.Enabled = false
	if err := ddb.Db.Model(&DiscoverySubscription{}).Where("id = ?", sub.ID).
		Updates(map[string]any{"enabled": false, "updated_at": time.Now()}).Error; err != nil {
		log.Printf("[discovery] TG 频道订阅：订阅 #%d 停用失败：%v", sub.ID, err)
		return false
	}
	log.Printf("[discovery] TG 频道订阅：订阅 #%d（%s）已自动完结并停用", sub.ID, sub.Title)
	return true
}

// touchChannelSubscription 只更新订阅的运行时间字段，不动用户在 UI 里的其它配置。
func touchChannelSubscription(sub *DiscoverySubscription) error {
	if ddb.Db == nil {
		return fmt.Errorf("发现板块数据库未初始化")
	}
	return ddb.Db.Model(&DiscoverySubscription{}).Where("id = ?", sub.ID).
		Updates(map[string]any{
			"last_checked_at": sub.LastCheckedAt,
			"next_check_at":   sub.NextCheckAt,
			"updated_at":      time.Now(),
		}).Error
}

// subscriptionFinished 自动完结判定：
// 洗版订阅以「达标」为准；movie 看是否已收录；tv 依次按总集数 / 指定季 / 总季数收敛。
func subscriptionFinished(sub DiscoverySubscription) bool {
	mediaType := strings.ToLower(strings.TrimSpace(sub.MediaType))
	season := subPrefInt(sub, "season", 0)

	if mediaType != "" && subPrefBool(sub, "wash", false) {
		return washTargetMet(sub)
	}
	if sub.TMDBID <= 0 {
		return false
	}
	switch mediaType {
	case "movie":
		return HasSubscriptionRecord(sub.ID, sub.TMDBID, 0)
	case "tv":
		if total := subPrefInt(sub, "total_episodes", 0); total > 0 {
			return countDistinctEpisodes(sub.ID, sub.TMDBID, season) >= total
		}
		if season > 0 {
			return HasSubscriptionRecord(sub.ID, sub.TMDBID, season)
		}
		if totalSeasons := subPrefInt(sub, "total_seasons", 0); totalSeasons > 0 {
			return countSubscriptionRecords(sub.ID) >= totalSeasons
		}
	}
	return false
}

// washTargetMet 洗版订阅是否已达目标规格：该订阅所有记录里最高分达到目标分即算达标。
func washTargetMet(sub DiscoverySubscription) bool {
	target := strings.TrimSpace(subPrefString(sub, "wash_target", ""))
	if target == "" || sub.TMDBID <= 0 {
		return false
	}
	records, err := ListTransferRecordsBySubscription(sub.ID, sub.TMDBID)
	if err != nil {
		return false
	}
	best := 0
	for i := range records {
		if score := records[i].ToMediaSpec().Score(); score > best {
			best = score
		}
	}
	return WashTargetReached(target, best)
}

// countDistinctEpisodes 统计某影片已收录的去重集数（Episode 字段是逗号分隔的集键集合）。
func countDistinctEpisodes(subID uint, tmdbID int64, season int) int {
	records, err := ListTransferRecordsBySubscription(subID, tmdbID)
	if err != nil {
		return 0
	}
	seen := map[string]struct{}{}
	for i := range records {
		if season > 0 && records[i].Season != season {
			continue
		}
		for _, key := range strings.Split(records[i].Episode, ",") {
			if key = strings.TrimSpace(key); key != "" {
				seen[key] = struct{}{}
			}
		}
	}
	return len(seen)
}

// countSubscriptionRecords 统计订阅下的转存记录条数（按总季数完结时用）。
func countSubscriptionRecords(subID uint) int {
	var count int64
	if ddb.Db == nil {
		return 0
	}
	if err := ddb.Db.Model(&DiscoveryTransferRecord{}).
		Where("subscription_id = ? AND status <> ?", subID, "superseded").
		Count(&count).Error; err != nil {
		return 0
	}
	return int(count)
}

// ListTransferRecordsBySubscription 读取订阅下某影片的全部有效转存记录（排除已被洗版替换的）。
func ListTransferRecordsBySubscription(subID uint, tmdbID int64) ([]DiscoveryTransferRecord, error) {
	if ddb.Db == nil {
		return nil, fmt.Errorf("发现板块数据库未初始化")
	}
	var records []DiscoveryTransferRecord
	err := ddb.Db.Where("subscription_id = ? AND tmdb_id = ? AND status <> ?", subID, tmdbID, "superseded").
		Order("created_at asc").Find(&records).Error
	if err != nil {
		return nil, err
	}
	return records, nil
}

// runChannelSubscriptionOnce 回溯/单频道抓取：忽略游标翻指定页数，抓到后交 processChannelPostsForSub。
func runChannelSubscriptionOnce(sub *DiscoverySubscription, ch *DiscoveryChannel) (string, bool) {
	channelName := ch.ChannelName()
	backfill := subPrefBool(*sub, "backfill", false)

	timeout := channelFetchTimeout
	stopID := strings.TrimSpace(ch.LastPostID)
	maxPages := 100
	if backfill {
		// 回溯模式要翻历史，忽略游标从最新一页往前推到指定页数；游标本轮不推进（安全）。
		timeout = channelBackfillTimeout
		stopID = ""
		maxPages = subPrefInt(*sub, "backfill_pages", channelDefaultBackfillPage)
		if maxPages <= 0 {
			maxPages = channelDefaultBackfillPage
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// ★ 停机追赶（仅非回溯路径）。
	// 回溯模式本来就有意忽略游标、翻固定页数去补历史，且本轮不推进游标，
	// 故追赶判定对它没有意义，跳过以免干扰其语义。
	if !backfill {
		catchup := planChannelCatchup(ch, []DiscoverySubscription{*sub}, 0)
		applyChannelCatchup(ch, &ctx, &stopID, catchup)
	}

	posts, pages, err := tgchannel.ParseChannelPageRange(ctx, channelName, stopID, maxPages)
	if err != nil {
		// 部分失败：已抓到的帖照常处理，但本轮窗口不完整，游标不推进（见下方 err == nil 判定）。
		if len(posts) == 0 {
			log.Printf("[discovery] TG 频道订阅：频道 %s 抓取失败：%v", channelName, err)
			return fmt.Sprintf("频道 %s 抓取失败：%v", channelName, err), false
		}
		log.Printf("[discovery] TG 频道订阅：频道 %s 翻页中断（已得 %d 帖，翻 %d 页），本轮按部分结果处理且不推进游标：%v", channelName, len(posts), pages, err)
	}
	now := time.Now()
	if len(posts) == 0 {
		ch.LastRunAt = now
		if !backfill {
			// 追赶态下无新帖即已追平：清空冻结起点。
			syncChannelCheckpoints(ch, posts)
		}
		if err := SaveChannel(ch); err != nil {
			log.Printf("[discovery] TG 频道订阅：频道 %s 保存运行时间失败：%v", channelName, err)
		}
		if backfill {
			return fmt.Sprintf("频道 %s：回溯翻 %d 页未命中", channelName, pages), true
		}
		return fmt.Sprintf("频道 %s 无新帖（翻 %d 页）", channelName, pages), true
	}

	newMaxID, summary, ok := processChannelPostsForSub(sub, ch, ctx, posts, pages, stopID)
	if backfill {
		// 回溯只补齐历史，游标保持在增量位置不动。
		return summary, ok
	}
	if newMaxID != "" && postIDGreater(newMaxID, ch.LastPostID) {
		// 注意：这里只在完整窗口下才推进（err == nil）。
		if err == nil {
			ch.LastPostID = newMaxID
		}
	}
	// 追赶态下按本轮实际扫过的窗口推进冻结起点（追平则清空）。理由同 runChannelBatchForSubs。
	if err == nil {
		advanceChannelCatchup(ch, posts)
	}
	ch.LastRunAt = now
	if err := SaveChannel(ch); err != nil {
		return fmt.Sprintf("频道 %s 游标保存失败：%v", channelName, err), false
	}
	return summary, ok
}

// processChannelPostsForSub 把一批帖子按单个订阅的筛选规则处理并转存。
// 返回 (该订阅本轮可推进到的新游标, 汇总文案, 是否全部成功)。
//
// 关键语义（逐条对齐旧仓库，改动前请先理解原因）：
//   - 隐式关键词：订阅没配关键词时用影片标题兜底，否则 MatchKeywords 空关键词等价「全命中」，
//     聚合频道会把整包无关剧集都转走（旧仓库「无上神帝误转事故」的根因）。
//   - 聚合帖防误转：转存前复核分享内第一个文件名是否匹配关键词，不匹配则跳过。
//   - 失败回退游标：本轮有转存失败时，把游标退到最早失败帖的前一帖，下轮重试。
func processChannelPostsForSub(sub *DiscoverySubscription, ch *DiscoveryChannel, ctx context.Context, posts []tgchannel.ChannelPost, pages int, stopID string) (string, string, bool) {
	provider := normalizeProviderKey(sub.TargetProvider)
	kws := subPrefStrings(*sub, "keywords")

	// ★ 隐式关键词兜底。
	// MatchKeywords 在 keywords 为空时恒返回 true（等价全命中），而聚合频道里同时挂着
	// 几十部剧的帖子，一旦订阅没配关键词就会把无关剧集整包转走 —— 旧仓库
	// 「无上神帝误转事故」正是这样发生的。因此这里用订阅标题（及原始标题）兜底，
	// 让「没配关键词」退化为「只转本订阅影片」，而不是「什么都转」。
	if sub.MediaType != "" && len(kws) == 0 && strings.TrimSpace(sub.Title) != "" {
		kws = []string{strings.TrimSpace(sub.Title)}
		if orig := strings.TrimSpace(sub.OriginalTitle); orig != "" && orig != strings.TrimSpace(sub.Title) {
			kws = append(kws, orig)
		}
	}

	backfill := subPrefBool(*sub, "backfill", false)
	lastID := stopID
	if backfill {
		// 回溯模式忽略游标，整批历史帖都要过一遍筛选。
		lastID = ""
	}

	season := subPrefInt(*sub, "season", 0)
	wash := subPrefBool(*sub, "wash", false)
	washTarget := strings.TrimSpace(subPrefString(*sub, "wash_target", ""))
	replaceOld := subPrefBool(*sub, "replace_old", false)
	useWashPath := sub.MediaType != "" && wash && sub.TMDBID > 0

	msgURLFor := func(postID string) string { return buildTGMessageURL(ch.ChannelName(), postID) }
	// ★ 只在 replace_old 为真时才走洗版分支。
	// 原因：洗版分支用「规格比较」决定是否转存，而不是用「是否已收录」去重。若 wash=true
	// 但 replace_old=false，洗版分支既不标记 superseded、也不会被常规去重拦住，同一批集号
	// 每轮轮询都会被反复转存（无限循环刷爆目标目录与监控历史）。replace_old=false 的语义是
	// 「保留旧版不删」，那就该退化成普通增量订阅——由常规路径的 HasEpisodeRecord 去重。
	processingWash := useWashPath && replaceOld

	newMaxID := ""
	hitPosts := 0
	linkCount := 0
	transferred := 0
	skipped := 0
	failedIDs := make([]string, 0, 4)

	for pi := range posts {
		p := posts[pi]
		if lastID != "" && !postIDGreater(p.PostID, lastID) {
			break
		}
		if newMaxID == "" || postIDGreater(p.PostID, newMaxID) {
			newMaxID = p.PostID
		}

		// 只认与本订阅目标网盘匹配的链接；probeURL 用于回溯留痕时附带帖子里的资源链接。
		probeURL := ""
		for _, l := range p.Links {
			if linkTypeMatchesProvider(l.Type, provider) {
				if probeURL == "" {
					probeURL = l.FullURL()
				}
			}
		}

		if !tgchannel.MatchKeywords(p.Text, kws) {
			if backfill && probeURL != "" {
				recordMonitorSkipped(sub, ch, p, msgURLFor(p.PostID), probeURL, "关键词未命中(帖含资源链接)")
			}
			continue
		}
		if len(p.Links) == 0 {
			if backfill && tgchannel.HasFastShareMarker(p.Text) {
				// 123 秒传暗号（123FSLink/FLCP）需要额外解析协议，当前不支持自动转存，
				// 回溯时留痕便于人工处理，增量时静默跳过避免刷屏。
				recordMonitorSkipped(sub, ch, p, msgURLFor(p.PostID), "", "帖子含 123 秒传暗号(123FSLink/FLCP)，暂不支持自动转存")
			}
			continue
		}

		hitPosts++
		for li := range p.Links {
			l := p.Links[li]
			if !linkTypeMatchesProvider(l.Type, provider) {
				continue
			}
			linkCount++
			msgURL := msgURLFor(p.PostID)

			epKeys := ParseEpisodeKeys(p.Text, season)
			spec := ParseMediaSpec(p.Text)

			// ---- 洗版分支：只转「规格更高」的版本，并把旧版本标记为 superseded ----
			var superseded []DiscoveryTransferRecord
			if processingWash {
				worth := false
				upgrades := make([]DiscoveryTransferRecord, 0, 4)
				if len(epKeys) > 0 {
					for _, epKey := range epKeys {
						old := LatestEpisodeRecord(sub.ID, sub.TMDBID, season, epKey)
						if old == nil {
							worth = true
							continue
						}
						oldSpec := old.ToMediaSpec()
						if WashTargetReached(washTarget, oldSpec.Score()) {
							continue
						}
						if spec.BetterThan(oldSpec) {
							worth = true
							upgrades = append(upgrades, *old)
						}
					}
					if !worth {
						recordMonitorSkipped(sub, ch, p, msgURL, l.FullURL(), "洗版跳过：全部集已达标或新资源规格不优于现有版本")
						continue
					}
					// 多集升级时把牵涉到的旧记录全部标记替换，避免残留半新半旧。
					superseded = upgrades
				} else {
					old := LatestSubscriptionRecord(sub.ID, sub.TMDBID, season)
					if old != nil {
						oldSpec := old.ToMediaSpec()
						if WashTargetReached(washTarget, oldSpec.Score()) {
							recordMonitorSkipped(sub, ch, p, msgURL, l.FullURL(), "洗版跳过：现有版本已达标")
							continue
						}
						if !spec.BetterThan(oldSpec) {
							recordMonitorSkipped(sub, ch, p, msgURL, l.FullURL(), "洗版跳过：新资源规格不优于现有版本")
							continue
						}
						superseded = []DiscoveryTransferRecord{*old}
						worth = true
					} else {
						worth = true
					}
				}
				if !worth {
					skipped++
					continue
				}
				// 聚合帖防误转：洗版路径转存前复核分享标题。
				if !verifyShareTitleMatches(ctx, l.FullURL(), l.Pwd, kws) {
					recordMonitorSkipped(sub, ch, p, msgURL, l.FullURL(), "跳过：分享标题与订阅影片不匹配（聚合帖防误转）")
					skipped++
					continue
				}
			} else {
				// ---- 常规路径：影片级/剧集级去重 ----
				if sub.TMDBID > 0 {
					if HasEpisodeRecord(sub.ID, sub.TMDBID, season, epKeys) {
						recordMonitorSkipped(sub, ch, p, msgURL, l.FullURL(), "去重跳过：该影片/剧集已收录")
						skipped++
						continue
					}
				} else if HasLinkRecord(l.URL) {
					recordMonitorSkipped(sub, ch, p, msgURL, l.FullURL(), "去重跳过：该分享链接已转存过")
					skipped++
					continue
				}
				// 聚合帖防误转：常规路径同样复核分享标题。
				if !verifyShareTitleMatches(ctx, l.FullURL(), l.Pwd, kws) {
					recordMonitorSkipped(sub, ch, p, msgURL, l.FullURL(), "跳过：分享标题与订阅影片不匹配（聚合帖防误转）")
					skipped++
					continue
				}
			}

			// ---- 转存（限流自动重试 + 槽位节流）----
			if err := awaitTransferSlot(ctx, providerSlotKey(provider), transferSlotInterval(provider)); err != nil {
				return newMaxID, buildChannelSummary(ch.ChannelName(), pages, hitPosts, linkCount, transferred, skipped, newMaxID, backfill), false
			}
			title, total, err := retryTransferOnRateLimit(ctx, func() (string, int, error) {
				return TransferShareLink(ctx, l.URL, l.Pwd, provider)
			})
			if err != nil {
				failedIDs = append(failedIDs, p.PostID)
				recordMonitorFailed(sub, ch, p, msgURL, l.FullURL(), err.Error())
				sendTransferFailedNotification(*sub, ch, l.FullURL(), err.Error())
				log.Printf("[discovery] TG 频道订阅 #%d：帖 %s 转存失败：%v", sub.ID, p.PostID, err)
				continue
			}

			transferred++
			rec := &DiscoveryTransferRecord{
				SourceType:     provider,
				SubscriptionID: sub.ID,
				MediaType:      sub.MediaType,
				TMDBID:         sub.TMDBID,
				Season:         season,
				Title:          firstNonEmptyStr(recTitle(sub), firstNonEmptyStr(title, sub.Title)),
				PostID:         p.PostID,
				LinkURL:        l.URL,
				Episode:        JoinEpisodeKeys(epKeys),
			}
			if processingWash {
				rec.Resolution = spec.Resolution
				rec.Source = spec.Source
				rec.Codec = spec.Codec
				rec.Effect = spec.Effect
				rec.SizeGB = spec.SizeGB
			}
			if err := CreateTransferRecord(rec); err != nil {
				log.Printf("[discovery] TG 频道订阅 #%d：写转存记录失败：%v", sub.ID, err)
			}

			// 洗版成功后把被替换的旧记录标记为 superseded（新仓库的收录判定已排除该状态，
			// 因此新版本记录不会互相顶掉，达标集数统计也不会重复计）。
			if processingWash && len(superseded) > 0 {
				keepIDs := make([]uint, 0, len(superseded)+1)
				keepIDs = append(keepIDs, rec.ID)
				for i := range superseded {
					keepIDs = append(keepIDs, superseded[i].ID)
				}
				if _, err := SupersedeTransferRecords(sub.ID, sub.TMDBID, season, keepIDs); err != nil {
					log.Printf("[discovery] TG 频道订阅 #%d：标记洗版替换失败：%v", sub.ID, err)
				}
			}

			if processingWash {
				// 洗版成功单独留「洗版替换」痕迹，便于监控页筛出被替换的版本。
				recordMonitorWash(sub, ch, p, msgURL, l.FullURL(), title, total)
			} else {
				recordMonitorSuccess(sub, ch, p, msgURL, l.FullURL(), title, total)
			}
			sendTransferSuccessNotification(*sub, ch, l.FullURL(), firstNonEmptyStr(title, sub.Title))
			log.Printf("[discovery] TG 频道订阅 #%d：命中帖 %s，已转存「%s」共 %d 项到 %s",
				sub.ID, p.PostID, firstNonEmptyStr(title, l.URL), total, resourceProviderName(provider))
		}
	}

	// ★ 失败回退游标：batchCursor 取的是各订阅的最小值，但单个订阅若在最后一条失败帖之后
	// 还有成功的帖，仍会把失败帖越过。这里把游标退到「最早失败帖」的前一帖，保证下轮重试，
	// 且不会重复转存已成功的帖（去重会拦住）。
	if len(failedIDs) > 0 {
		oldestFailed := failedIDs[len(failedIDs)-1]
		if n, err := strconv.ParseInt(strings.TrimSpace(oldestFailed), 10, 64); err == nil && n > 0 {
			newMaxID = strconv.FormatInt(n-1, 10)
		}
	}

	return newMaxID, buildChannelSummary(ch.ChannelName(), pages, hitPosts, linkCount, transferred, skipped, newMaxID, backfill), len(failedIDs) == 0
}

// buildChannelSummary 生成单个频道的处理汇总文案。
func buildChannelSummary(channel string, pages, hitPosts, linkCount, transferred, skipped int, cursor string, backfill bool) string {
	if backfill {
		return fmt.Sprintf("频道 %s：翻 %d 页，命中 %d 帖，链接 %d 个，转存成功 %d 次，跳过 %d 次（回溯模式，未推进游标）",
			channel, pages, hitPosts, linkCount, transferred, skipped)
	}
	return fmt.Sprintf("频道 %s：翻 %d 页，命中 %d 帖，链接 %d 个，转存成功 %d 次，跳过 %d 次，游标推进至 %s",
		channel, pages, hitPosts, linkCount, transferred, skipped, cursor)
}

// recTitle 转存记录/通知的展示标题，优先订阅标题。
func recTitle(sub *DiscoverySubscription) string {
	return strings.TrimSpace(sub.Title)
}

// ---------------------------------------------------------------------------
// 审计留痕
// ---------------------------------------------------------------------------

// 回溯模式同一帖会被反复翻到，若每次都写审计记录会把监控页刷爆，
// 因此回溯模式下先查 MonitorRecordExists 去重；增量模式每帖只会处理一次，无需查库。
func monitorRecordAlreadyLogged(sub *DiscoverySubscription, backfill bool, messageURL string) bool {
	if !backfill || messageURL == "" {
		return false
	}
	return MonitorRecordExists(sub.ID, messageURL)
}

func newMonitorRecord(sub *DiscoverySubscription, ch *DiscoveryChannel, p tgchannel.ChannelPost, messageURL, targetURL, status, result string) *DiscoveryMonitorRecord {
	rec := &DiscoveryMonitorRecord{
		SourceType:     ch.SourceType,
		Channel:        ch.ChannelName(),
		MessageID:      p.PostID,
		MessageURL:     messageURL,
		TargetURL:      targetURL,
		TransferStatus: status,
		TransferResult: result,
		Title:          strings.TrimSpace(sub.Title),
		SubscriptionID: sub.ID,
		TMDBID:         sub.TMDBID,
		MediaType:      sub.MediaType,
		Season:         strconv.Itoa(subPrefInt(*sub, "season", 0)),
	}
	if recordURL := targetURL; recordURL != "" {
		rec.TargetURL = recordURL
	}
	return rec
}

func recordMonitorSkipped(sub *DiscoverySubscription, ch *DiscoveryChannel, p tgchannel.ChannelPost, messageURL, targetURL, reason string) {
	if monitorRecordAlreadyLogged(sub, subPrefBool(*sub, "backfill", false), messageURL) {
		return
	}
	if err := CreateMonitorTransferRecord(newMonitorRecord(sub, ch, p, messageURL, targetURL, MonitorStatusSkipped, reason)); err != nil {
		log.Printf("[discovery] TG 频道订阅：写跳过留痕失败：%v", err)
	}
}

func recordMonitorFailed(sub *DiscoverySubscription, ch *DiscoveryChannel, p tgchannel.ChannelPost, messageURL, targetURL, reason string) {
	if err := CreateMonitorTransferRecord(newMonitorRecord(sub, ch, p, messageURL, targetURL, MonitorStatusFailed, reason)); err != nil {
		log.Printf("[discovery] TG 频道订阅：写失败留痕失败：%v", err)
	}
}

func recordMonitorSuccess(sub *DiscoverySubscription, ch *DiscoveryChannel, p tgchannel.ChannelPost, messageURL, targetURL, title string, total int) {
	rec := newMonitorRecord(sub, ch, p, messageURL, targetURL, MonitorStatusSuccess, firstNonEmptyStr(title, "转存成功"))
	rec.Title = firstNonEmptyStr(title, rec.Title)
	rec.Total = total
	if err := CreateMonitorTransferRecord(rec); err != nil {
		log.Printf("[discovery] TG 频道订阅：写成功留痕失败：%v", err)
	}
}

// recordMonitorWash 洗版替换留痕（成功路径由 recordMonitorSuccess 记，这里补充洗版语义）。
func recordMonitorWash(sub *DiscoverySubscription, ch *DiscoveryChannel, p tgchannel.ChannelPost, messageURL, targetURL, title string, total int) {
	rec := newMonitorRecord(sub, ch, p, messageURL, targetURL, MonitorStatusWash, firstNonEmptyStr(title, "洗版替换"))
	rec.Title = firstNonEmptyStr(title, rec.Title)
	rec.Total = total
	if err := CreateMonitorTransferRecord(rec); err != nil {
		log.Printf("[discovery] TG 频道订阅：写洗版留痕失败：%v", err)
	}
}

func sendTransferSuccessNotification(sub DiscoverySubscription, ch *DiscoveryChannel, shareURL, title string) {
	if TransferSuccessNotifyFn == nil {
		return
	}
	TransferSuccessNotifyFn(sub.ID, sub.Title, firstNonEmptyStr(title, shareURL), normalizeProviderKey(sub.TargetProvider))
}

func sendTransferFailedNotification(sub DiscoverySubscription, ch *DiscoveryChannel, shareURL, reason string) {
	if TransferFailedNotifyFn == nil {
		return
	}
	TransferFailedNotifyFn(sub.ID, sub.Title, shareURL, normalizeProviderKey(sub.TargetProvider), reason)
}

// ---------------------------------------------------------------------------
// 聚合帖防误转
// ---------------------------------------------------------------------------

var reChannel123Share = regexp.MustCompile(`(?i)(?:123pan\.(?:com|cn)|123684\.com|123865\.com)/(?:s|123pan|share)/([A-Za-z0-9\-_]{6,})`)

// verifyShareTitleMatches 复核分享内的顶级文件名是否匹配订阅关键词（聚合帖防误转）。
//
// ★ 降级策略：只要无法「确定地」拿到名称，一律放行（返回 true）——
// 关键词为空、非 123 分享、探测函数未绑定、账号缺失、查询失败、名称为空。
// 原因：这个检查只是「额外的安全网」，误放行的代价是可能多转一点无关内容，
// 而误拦截的代价是该转的被永久跳过（用户很难察觉）。所以宁放勿拦。
func verifyShareTitleMatches(ctx context.Context, shareURL, pwd string, kws []string) bool {
	if len(kws) == 0 || ShareTitleProbeFn == nil {
		return true
	}
	shareKey := extract123ShareKey(shareURL)
	if shareKey == "" {
		return true
	}
	name, err := ShareTitleProbeFn(ctx, shareURL, pwd)
	if err != nil || strings.TrimSpace(name) == "" {
		return true
	}
	return tgchannel.MatchKeywords(name, kws)
}

// extract123ShareKey 从 123 分享链接提取 shareKey（新仓库未导出该正则，故在此自建）。
func extract123ShareKey(rawURL string) string {
	m := reChannel123Share.FindStringSubmatch(strings.TrimSpace(rawURL))
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ---------------------------------------------------------------------------
// 限流与节流
// ---------------------------------------------------------------------------

// transferRetryBackoffs 限流重试退避序列（比 123 官方建议更保守，避免触发风控）。
var transferRetryBackoffs = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}

// isRateLimitErr 判断是否属于「可重试」的限流类错误。
// 各家网盘（尤其 123）文案不统一，故用关键词宽松匹配；非限流错误不重试，避免把真错误拖成超时。
func isRateLimitErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{"rate limit", "rate_limit", "ratelimit", "429", "too many", "quota", "频率", "频繁", "太快", "限流", "exceeded", "too frequent", "slow down"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// retryTransferOnRateLimit 仅对限流类错误重试转存。
func retryTransferOnRateLimit(ctx context.Context, fn func() (string, int, error)) (string, int, error) {
	title, total, err := fn()
	if err == nil || !isRateLimitErr(err) {
		return title, total, err
	}
	for attempt, backoff := range transferRetryBackoffs {
		log.Printf("[discovery] TG 频道订阅：转存遇限流，第 %d 次重试（等待 %v）：%v", attempt+1, backoff, err)
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-time.After(backoff):
		}
		title, total, err = fn()
		if err == nil || !isRateLimitErr(err) {
			return title, total, err
		}
	}
	return title, total, err
}

// transferSlotInterval 各网盘的转存节流间隔：123 风控最严，放宽到 20s。
func transferSlotInterval(provider string) time.Duration {
	switch normalizeProviderKey(provider) {
	case "123":
		return 20 * time.Second
	case "guangya", "pan139":
		return 15 * time.Second
	}
	return 15 * time.Second
}

// providerSlotKey 节流槽位键。同一网盘的所有转存共享一个槽位，串行排队，
// 避免并发转存触发风控。
func providerSlotKey(provider string) string {
	return "channel:" + normalizeProviderKey(provider)
}

// awaitTransferSlot 按槽位节流：同一 key 的两次转存至少间隔 interval。
// 用 lockForKey 保证同槽位串行，用 sleep 补齐间隔。
func awaitTransferSlot(ctx context.Context, key string, interval time.Duration) error {
	if interval <= 0 {
		return nil
	}
	mu := lockForKey("transfer-slot:" + key)
	mu.Lock()
	defer mu.Unlock()

	slot := transferSlotGet(key)
	wait := interval - time.Since(slot)
	if wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	transferSlotSet(key, time.Now())
	return nil
}

var (
	transferSlotMu sync.Mutex
	transferSlots  = map[string]time.Time{}
)

func transferSlotGet(key string) time.Time {
	transferSlotMu.Lock()
	defer transferSlotMu.Unlock()
	return transferSlots[key]
}

func transferSlotSet(key string, at time.Time) {
	transferSlotMu.Lock()
	transferSlots[key] = at
	transferSlotMu.Unlock()
}

// ---------------------------------------------------------------------------
// 通用小件
// ---------------------------------------------------------------------------

// PreviewChannel 预览频道最新帖（不改游标、不转存），供 API 添加频道前确认内容质量。
func PreviewChannel(channel string, limit int) ([]tgchannel.ChannelPost, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return nil, fmt.Errorf("频道名不能为空")
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	posts, err := tgchannel.ParseChannelPage(ctx, channel)
	if err != nil {
		return nil, err
	}
	if len(posts) > limit {
		posts = posts[:limit]
	}
	return posts, nil
}

// postIDGreater 比较 TG 帖子 ID：长度不同比长度（TG post id 是递增整数，
// 但可能超过 int64 安全拼接范围，故按字符串长度+字典序比较），长度相同比字典序。
func postIDGreater(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

// buildTGMessageURL 拼帖子永久链接。
func buildTGMessageURL(channel, postID string) string {
	channel = strings.TrimPrefix(strings.TrimSpace(channel), "@")
	postID = strings.TrimSpace(postID)
	if channel == "" || postID == "" {
		return ""
	}
	return fmt.Sprintf("https://t.me/%s/%s", channel, postID)
}

// linkTypeMatchesProvider 频道帖里的链接类型（123/guangyapan/pan139）与订阅目标网盘是否匹配。
func linkTypeMatchesProvider(linkType, provider string) bool {
	return normalizeProviderKey(linkType) == normalizeProviderKey(provider)
}

// ---------------------------------------------------------------------------
// 订阅扩展字段读取（sub.Pref 是 Preferences JSON 解出的 map，可能为 nil）
// ---------------------------------------------------------------------------

func subPrefBool(sub DiscoverySubscription, key string, def bool) bool {
	v, ok := subPrefValue(sub, key)
	if !ok {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return def
}

func subPrefInt(sub DiscoverySubscription, key string, def int) int {
	v, ok := subPrefValue(sub, key)
	if !ok {
		return def
	}
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	}
	return def
}

func subPrefString(sub DiscoverySubscription, key, def string) string {
	v, ok := subPrefValue(sub, key)
	if !ok {
		return def
	}
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return def
}

func subPrefStrings(sub DiscoverySubscription, key string) []string {
	v, ok := subPrefValue(sub, key)
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return normalizeStringList(t)
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return normalizeStringList(out)
	case string:
		// 兼容用户在 UI 里以逗号/分号分隔的输入。
		fields := strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == ';' || r == '，' || r == '；' })
		return normalizeStringList(fields)
	}
	return nil
}

// subPrefValue 从 sub.Pref 取字段，做 nil 兜底。
func subPrefValue(sub DiscoverySubscription, key string) (any, bool) {
	if sub.Pref == nil {
		return nil, false
	}
	v, ok := sub.Pref[key]
	return v, ok
}

// boolPtr 取 bool 指针（ListSubscriptions 只收指针）。
func boolPtr(v bool) *bool { return &v }

// sortStrings 插入排序：providers 分组数量很小（最多 3 个网盘），
// 不值得为它引入 sort 包与闭包开销。
func sortStrings(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j] < list[j-1]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
