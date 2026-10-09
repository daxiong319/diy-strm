// T16 · RSS 订阅的同步服务（单源同步 + 轮询调度）。
//
// 它坐落在三个既有模块之间，边界是刻意划的：
//
//	解析/抓取    internal/discover/rss   —— 纯函数，无 DB、无设置、无时钟
//	落地/去重    internal/discover/discovery（DeliverRSSCandidates + SubmitOfflineLink）
//	存取         internal/store          —— RSSSourceRepository / RSSHistoryRepository
//
// 为什么不把这些全塞进 internal/discover/rss：那会让解析包依赖 gorm、settings、
// 账本模型，单测要拖上整个发现栈。解析器是最该保持纯的部分。
//
// 五条与 参考实现 不同的既定口径（变更说明里逐条对照）：
//
//  1. **停机追赶**：参考实现 侧 RSS 没有位点，去重全靠 history.guid 唯一约束，
//     抓到什么处理什么。停机窗口是本仓新增的，照 internal/discover/dmodels/scrape.go
//     的 DefaultChannelCatchupHours = 12 同值。
//  2. **失败即上报**：一个坏 URL / 超时源只影响它自己，错误写进该源 last_message
//     并继续同步其余源（验收 ⑦）。参考实现 的轮询实现全在 .so 里，这条按域惯例定。
//  3. **失败不写历史**：提交失败的条目不写 history，下一轮还会重来。
//     写了下轮就被 guid 唯一约束永久吃掉，用户的重试机会凭空消失。
//  4. **过期条目不写历史**：被 stale_after 过滤掉的条目不占 guid，
//     否则用户把阈值调小再调大，那些条目永远回不来了。
//  5. **两级幂等**：history.guid（数据库唯一约束，跨源）+ 账本 info_hash
//     （3 小时保护期）。前者管「同一个 feed 条目只处理一次」，
//     后者管「同一个内容资源不要每轮重提」。删掉任一层都还剩一层兜底。
package discovery

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"litepan/internal/discover/connector"
	"litepan/internal/discover/rss"
	"litepan/internal/domain"
)

// ---------------------------------------------------------------------------
// 依赖注入
// ---------------------------------------------------------------------------

// RSSSourceStore / RSSHistoryStore 由 internal/app/wire_http.go 注入。
// internal/store 依赖 domain、不依赖 discover，方向干净。
var (
	RSSSourceStore  domain.RSSSourceRepository
	RSSHistoryStore domain.RSSHistoryRepository
)

// RSSSettingsKeyPrefix 便于把设置键集中在一处，避免裸字符串散落。
const (
	rssKeyEnabled    = "mo_rss_enabled"
	rssKeyPollMinute = "mo_rss_poll_interval_minutes"
	rssKeyCatchupHrs = "mo_rss_catchup_gap_hours"
	rssKeyStaleMins  = "mo_rss_stale_after_minutes"
	rssKeyTimeoutSec = "mo_rss_http_timeout_seconds"
	rssKeyMaxBytes   = "mo_rss_max_feed_bytes"
)

// RSSServicesReady 依赖是否齐备。
//
// ⚠️ 这里**不**检查 OfflineLinkFn：RSS 源的动作可能是转存（分享链接）也可能是
// 离线（磁力），而两条链路的就绪状态不同。缺哪条在真正走到时给明确错误，
// 提前整体报「未就绪」会让「只配了转存、没配 qBittorrent」的用户连分享链接的
// RSS 源都用不了。
func RSSServicesReady() bool { return RSSSourceStore != nil && RSSHistoryStore != nil }

// ---------------------------------------------------------------------------
// 同步结果
// ---------------------------------------------------------------------------

// RSSSyncResult 一轮同步的完整结论（直接作为 API 响应体）。
type RSSSyncResult struct {
	// Source 同步的源（回显）。
	Source domain.RSSSource `json:"source"`
	// Counters 三计数，与 参考实现 同步接口同口径，前端 toast 直接用。
	Counters domain.RSSSyncCounters `json:"counters"`
	// Status 五值状态之一（domain.RSSStatus*）。
	Status string `json:"status"`
	// Message 一句话结论，同时写进 source.last_message。
	Message string `json:"message"`
	// Catchup 本轮是否走了「只取最新一页」的追赶模式。
	Catchup bool `json:"catchup"`
	// Total 解析出的条目总数（含被过滤的）。
	Total int `json:"total"`
	// FetchedAt 抓取完成时刻（RFC3339）。
	FetchedAt time.Time `json:"fetched_at"`
	// Detail 逐条明细，最多 20 条（防止 300 条的 feed 把响应撑爆）。
	Detail []RSSItemResult `json:"detail,omitempty"`
	// Notices 口径提示，由服务端下发、前端原样渲染，不在前端复述改写。
	Notices []string `json:"notices,omitempty"`
}

// RSSItemResult 一条条目的处理结论。
type RSSItemResult struct {
	Title  string `json:"title"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Link   string `json:"link,omitempty"`
}

// rssDetailLimit 逐条明细的条数上限。
const rssDetailLimit = 20

// ---------------------------------------------------------------------------
// 单源同步
// ---------------------------------------------------------------------------

// SyncRSSSource 同步一个源一轮。
//
// 错误语义：**只有「这个源这一轮没成」才记 failed**。一个源失败不影响别的源
// （验收 ⑦），所以轮询 worker 与手动同步都要逐个调用。
func SyncRSSSource(ctx context.Context, src domain.RSSSource) RSSSyncResult {
	res := RSSSyncResult{Source: src, Counters: domain.RSSSyncCounters{}, FetchedAt: time.Now()}
	if !RSSServicesReady() {
		res.Status = domain.RSSStatusFailed
		res.Message = "RSS 服务未就绪"
		return res
	}

	// ── 抓取 ────────────────────────────────────────────────────────────
	fetched, err := newRSSFetcher().Fetch(ctx, src.RssURL)
	if err != nil {
		return rssFailRound(res, src, err)
	}
	items := fetched.Feed.Items
	res.Total = len(items)
	if len(items) > 0 {
		res.Detail = append(res.Detail, RSSItemResult{Title: rssItemTitle(items[0]), Status: domain.RSSStatusSkipped,
			Reason: "feed 里最新的一条（用于对照下方逐条结果）"})
	}

	// ── 位点与追赶 ──────────────────────────────────────────────────────
	lastAt, hasCursor, err := RSSHistoryStore.LatestProcessedAt(ctx, src.ID)
	if err != nil {
		return rssFailRound(res, src, fmt.Errorf("读取同步位点失败：%w", err))
	}
	if rssShouldCatchUp(lastAt, hasCursor, rssCatchupGapHours()) {
		res.Catchup = true
		res.Notices = append(res.Notices, fmt.Sprintf(
			"距离上次成功同步已超过 %d 小时，本轮只取 feed 的最新一条，没有逐条补齐停机期间的历史。",
			rssCatchupGapHours()))
		// 只留最新一条。注意是 items[:1] 而不是过滤出「发布时间在窗口内的」——
		// 后者会漏掉「最新一条也早于窗口」这个真实情形，那样用户看到的是空结果
		// 而不是「我确实追到最新了」。
		if len(items) > 1 {
			items = items[:1]
		}
	}

	// ── 过滤 ────────────────────────────────────────────────────────────
	// 顺序有讲究：正则先砍（纯字符串运算），再砍过期条目，最后才逐条查库。
	// 倒过来的话一个 300 条的源每轮要发 300 次 SQL。
	include, err := compileRSSRegex(src.IncludeRegex, "包含")
	if err != nil {
		return rssFailRound(res, src, err)
	}
	exclude, err := compileRSSRegex(src.ExcludeRegex, "排除")
	if err != nil {
		return rssFailRound(res, src, err)
	}
	staleAfter := time.Duration(rssStaleAfterMinutes()) * time.Minute
	kept := make([]rss.Item, 0, len(items))
	for _, it := range items {
		if include != nil && !include.MatchString(it.Title) {
			continue
		}
		if exclude != nil && exclude.MatchString(it.Title) {
			continue
		}
		// 只在「发布时间非零」时判过期。feed 不给 pubDate 时 entry 是零值，
		// now.Sub(零值) 是 2000 多年，会把整个 feed 全部误判为过期。
		if staleAfter > 0 && !it.Published.IsZero() && res.FetchedAt.Sub(it.Published) > staleAfter {
			continue
		}
		kept = append(kept, it)
	}

	// ── 候选化 ──────────────────────────────────────────────────────────
	// 去重键在这里算好一次，随后**跟着候选一路传到写历史那一步**。
	// 分两处算的话，标题归一化规则的任何改动都会让「历史里写过的 guid」与
	// 「这轮算出的 guid」错位，于是同一条目被反复处理 —— 而这个错误在日志里
	// 完全看不出来（guid 不为空、唯一约束也不冲突，只是换了个值）。
	candidates := make([]RSSCandidate, 0, len(kept))
	var noResource, alreadyDone int
	for _, it := range kept {
		key := it.DedupKey(src.ID)
		entry, ok := rssCandidateFromItem(rssItemToConnectorItem(it, src), key)
		if !ok {
			// 解析器已滤掉无资源条目，正常走不到这里。真到了也不能算失败：
			// 它是「这条没有可下的东西」，且**不占 guid**（否则用户后来
			// 修好 feed 再抓同一条会以为已经处理过）。
			noResource++
			continue
		}
		// ⭐ 这一步是**永久去重**，不是优化。
		//
		// 不先查一遍的后果：历史是「提交成功之后」才写的，所以唯一约束
		// 只能挡住重复的历史行，挡不住重复的下载。同一份资源出现在两个源
		// 里（或者用户复制了一个源）时会真的下两遍，而且账本那层也救不了
		// —— ClaimTransferSlot 命中的是 (storage_slug, content_key) 唯一键，
		// 但它插入失败时会**降级为「按无账本处理」并放行**（见 transfer_ledger.go
		// 的「插入失败，降级为按无账本处理」日志），所以最终只有这一个闸门。
		//
		// 提前在候选化阶段查完还有个副作用收益：一轮几百条的 feed 只发几百次
		// 单行查询，而不是把它们全推进投递层再一个个被账本/媒体库挡下。
		seen, err := RSSHistoryStore.HasGuid(ctx, key)
		if err != nil {
			// 查不到不等于处理过。宁可多下一次，也不要在查库失败时静默跳过
			// ——后者会让用户以为「同步完成：新增 0」，实际是整轮没干活。
			log.Printf("[rss] 源=%d 查去重失败 guid=%q err=%v（本轮按未处理继续）", src.ID, key, err)
		} else if seen {
			alreadyDone++
			continue
		}
		candidates = append(candidates, entry)
	}
	res.Counters.Skipped += noResource + alreadyDone
	if alreadyDone > 0 {
		rssAddDetail(&res, RSSItemResult{
			Title:  fmt.Sprintf("%d 条此前已处理过（去重键命中历史）", alreadyDone),
			Status: domain.RSSStatusSkipped,
			Reason: "不重复提交下载",
		})
	}

	// ── 落地 ────────────────────────────────────────────────────────────
	delivery := DeliverRSSCandidates(ctx, RSSDeliveryPlan{
		SourceID:   src.ID,
		SourceName: src.Name,
		MediaType:  src.MediaType,
		Providers:  rssProvidersOf(src),
		Action:     src.Action,
		Candidates: candidates,
	})

	// ── 写历史 ──────────────────────────────────────────────────────────
	// 只给「落地成功」的条目写 success / 「明确被闸门挡住」的写 skipped。
	// 前者是位点的实体（下一轮 LatestProcessedAt 从这张表取），
	// 后者是为了让 UI 能显示「这条为什么没下」。
	for _, outcome := range delivery.Outcomes {
		switch outcome.Status {
		case rssOutcomeSuccess:
			_, ok, err := RSSHistoryStore.InsertOnce(ctx, domain.RSSHistory{
				SourceID: src.ID, SourceName: src.Name, Guid: outcome.Key,
				Title: outcome.Title, Link: outcome.Link, DownloadURL: outcome.Link,
				TargetPath: src.TargetPath, Status: domain.RSSStatusSuccess,
			})
			if err != nil {
				// 写历史失败不改变本轮结论：条目已经提交出去了，账本也确认了。
				// 但必须打日志 —— 否则下一轮会重复提交同一个条目。
				log.Printf("[rss] 写历史失败 source=%d guid=%q err=%v（下一轮会重复提交这个条目）", src.ID, outcome.Key, err)
				continue
			}
			if ok {
				res.Counters.Added++
			} else {
				// guid 已存在 = 之前处理过。计入 skipped 而不是静默丢弃：
				// 用户点「立即同步」时需要看到「命中 N 条重复」。
				res.Counters.Skipped++
			}
		case rssOutcomeFailed:
			// 失败**不写历史**（口径 3）：下轮还能重试。
			res.Counters.Failed++
			rssAddDetail(&res, RSSItemResult{Title: outcome.Title, Status: domain.RSSStatusFailed,
				Reason: outcome.Reason, Link: outcome.Link})
		case rssOutcomeSkipped:
			res.Counters.Skipped++
			rssAddDetail(&res, RSSItemResult{Title: outcome.Title, Status: domain.RSSStatusSkipped,
				Reason: outcome.Reason, Link: outcome.Link})
		}
	}
	res.Detail = res.Detail[:0] // 丢掉开头的「最新一条」提示行，逐条明细重新填
	rssAddDetail(&res, RSSItemResult{
		Title:  fmt.Sprintf("解析到 %d 条，解析器留下可下资源的有 %d 条", res.Total, len(candidates)+noResource),
		Status: domain.RSSStatusSkipped,
		Reason: fmt.Sprintf("其中 %d 条没有可下载链接（更新通知之类），未处理", noResource),
	})

	// ── 结论与回写 ──────────────────────────────────────────────────────
	res.Status = rssStatusOf(res.Counters)
	res.Message = rssSummaryMessage(res.Total, res.Counters, delivery)
	res.Notices = append(res.Notices, delivery.Notices()...)
	if err := RSSSourceStore.MarkSyncResult(ctx, src.ID, res.Status, res.Message, res.FetchedAt); err != nil {
		log.Printf("[rss] 回写同步结果失败 source=%d err=%v", src.ID, err)
	}
	return res
}

// rssFailRound 标记本轮失败并回写源状态。
//
// 回写用 Background 上下文：ctx 被取消正是最常见的失败原因之一，
// 那种情况下恰恰**最需要**把「失败」落到 last_message 上。
func rssFailRound(res RSSSyncResult, src domain.RSSSource, cause error) RSSSyncResult {
	res.Status = domain.RSSStatusFailed
	res.Message = cause.Error()
	if RSSSourceStore != nil {
		if err := RSSSourceStore.MarkSyncResult(context.Background(), src.ID,
			domain.RSSStatusFailed, res.Message, res.FetchedAt); err != nil {
			log.Printf("[rss] 回写失败状态出错 source=%d err=%v", src.ID, err)
		}
	}
	return res
}

func rssStatusOf(c domain.RSSSyncCounters) string {
	switch {
	case c.Added > 0 && c.Failed > 0:
		return domain.RSSStatusPartial
	case c.Added > 0:
		return domain.RSSStatusSuccess
	case c.Failed > 0:
		return domain.RSSStatusFailed
	default:
		return domain.RSSStatusSkipped
	}
}

func rssSummaryMessage(total int, c domain.RSSSyncCounters, delivery RSSDeliveryResult) string {
	switch rssStatusOf(c) {
	case domain.RSSStatusSuccess:
		return fmt.Sprintf("同步完成：新增 %d，已提交落地；跳过 %d", c.Added, c.Skipped)
	case domain.RSSStatusPartial:
		return fmt.Sprintf("同步完成：新增 %d，跳过 %d，失败 %d（失败的条目下一轮会重试）", c.Added, c.Skipped, c.Failed)
	case domain.RSSStatusFailed:
		return fmt.Sprintf("本轮 %d 条全部失败：%s", c.Failed, firstNonEmptyStr(delivery.FirstFailure(), "未提供原因"))
	default:
		return fmt.Sprintf("同步完成：没有新条目（共 %d 条，%d 条已处理过）", total, c.Skipped)
	}
}

func rssAddDetail(res *RSSSyncResult, item RSSItemResult) {
	if len(res.Detail) >= rssDetailLimit {
		return
	}
	res.Detail = append(res.Detail, item)
}

func rssItemTitle(it rss.Item) string { return firstNonEmptyStr(it.Title, "(无标题条目)") }

// compileRSSRegex 编译用户填的正则，失败时**报错**而不是忽略。
//
// 静默忽略的后果：一个打错的正则（比如漏了左括号）会让过滤完全不生效，
// 用户以为自己在筛 1080p，实际每条都下 —— 而 UI 上那个输入框看起来完全正常。
func compileRSSRegex(pattern, label string) (*regexp.Regexp, error) {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return nil, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("%s过滤正则无效：%w", label, err)
	}
	return re, nil
}

// ---------------------------------------------------------------------------
// 设置读取
// ---------------------------------------------------------------------------

func rssEnabled() bool { return guardrailInt(rssKeyEnabled, 0) != 0 }

// rssCatchupGapHours 停机追赶窗口（小时）。0 = 不追赶。
func rssCatchupGapHours() int { return guardrailInt(rssKeyCatchupHrs, 12) }

// rssStaleAfterMinutes 过期条目阈值（分钟）。0 = 不按发布时间过滤。
func rssStaleAfterMinutes() int { return guardrailInt(rssKeyStaleMins, 60) }

// rssPollInterval 轮询间隔。
func rssPollInterval() time.Duration {
	mins := guardrailInt(rssKeyPollMinute, 30)
	if mins < 1 {
		mins = 1
	}
	return time.Duration(mins) * time.Minute
}

// newRSSFetcher 按设置构造抓取器。
//
// 每次新建而不复用：超时是设置项，用户改完设置要立刻生效，
// 而 http.Client 的 Timeout 一旦设死就没法改。
func newRSSFetcher() *rss.Fetcher {
	timeout := time.Duration(guardrailInt(rssKeyTimeoutSec, 20)) * time.Second
	if timeout < time.Second {
		timeout = time.Second
	}
	maxBytes := int64(guardrailInt(rssKeyMaxBytes, 4<<20))
	return rss.NewFetcherWithOptions(&http.Client{Timeout: timeout}, rss.FetchOptions{MaxBytes: maxBytes})
}

// rssShouldCatchUp 判断这一轮是否只取最新一条。
//
// hasCursor=false（这个源从未处理过任何条目）**不算**停机 —— 首次同步就该
// 把 feed 现有的条目都过一遍，否则用户刚贴上 URL 会看到「同步完成：新增 0」。
func rssShouldCatchUp(lastAt time.Time, hasCursor bool, gapHours int) bool {
	if !hasCursor || gapHours <= 0 {
		return false
	}
	return time.Since(lastAt) > time.Duration(gapHours)*time.Hour
}

// ---------------------------------------------------------------------------
// 轮询调度
// ---------------------------------------------------------------------------

// rssWatcherOnce 保证轮询 worker 只起一次。
var rssWatcherOnce sync.Once

// StartRSSWatcher 启动 RSS 轮询 worker。
//
// 由 StartDiscoveryWorkers 调用（那里有 workerOnce 保证整个发现栈只起一次）。
// 开关关着时**根本不启动**：这不是「同步会失败」的功能开关，而是
// 「这条链路要不要占用网络与离线下载额度」的开关（见 registry.go 的说明）。
func StartRSSWatcher(ctx context.Context) {
	rssWatcherOnce.Do(func() {
		if !rssEnabled() {
			log.Println("[rss] 轮询未启动：mo_rss_enabled 关闭")
			return
		}
		go rssWatchLoop(ctx)
	})
}

// rssWatchLoop 轮询主循环。
//
// 节奏照 internal/discover/tgchannel 的 StartChannelWatcher：先睡一个间隔
// 再首轮，避免与同一进程里其它 worker 抢启动瞬间的网络与数据库。
// 关着开关重启时不会在启动瞬间打一轮（用户刚改完设置还在浏览器里看页面）。
func rssWatchLoop(ctx context.Context) {
	interval := rssPollInterval()
	log.Printf("[rss] 轮询已启动，间隔 %s", interval)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("[rss] 轮询已停止")
			return
		case <-timer.C:
			if rssEnabled() {
				SyncAllRSSSources(ctx)
			} else {
				log.Println("[rss] 轮询已暂停：mo_rss_enabled 已关闭")
			}
			timer.Reset(rssPollInterval())
		}
	}
}

// SyncAllRSSSources 同步全部启用的源。
//
// **一个源失败不影响其它源**（验收 ⑦）：逐个调用、逐个记状态，
// 源多的时候并发跑，单个源的超时不会把整轮拖成 N 倍。
func SyncAllRSSSources(ctx context.Context) []RSSSyncResult {
	if !RSSServicesReady() {
		return nil
	}
	sources, err := RSSSourceStore.ListEnabled(ctx)
	if err != nil {
		log.Printf("[rss] 读启用源失败: %v", err)
		return nil
	}
	if len(sources) == 0 {
		return nil
	}
	log.Printf("[rss] 开始同步 %d 个源", len(sources))

	// 源多的时候并发跑：每个源已经各自带超时，这里的并发只是不让 10 个源
	// 串成 10 倍时长。上限 4，再多对下游站点也不礼貌。
	results := make([]RSSSyncResult, len(sources))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, src domain.RSSSource) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = SyncRSSSource(ctx, src)
		}(i, src)
	}
	wg.Wait()

	var added, failed, skipped int
	for _, r := range results {
		added += r.Counters.Added
		skipped += r.Counters.Skipped
		failed += r.Counters.Failed
		if r.Status == domain.RSSStatusFailed {
			log.Printf("[rss] 源 %q(#%d) 失败：%s", r.Source.Name, r.Source.ID, r.Message)
		}
	}
	log.Printf("[rss] 同步完成：新增 %d，跳过 %d，失败 %d", added, skipped, failed)
	return results
}

// ---------------------------------------------------------------------------
// 条目 → 连接器条目
// ---------------------------------------------------------------------------

// rssItemToConnectorItem 把 rss.Item 映射成 connector.Item。
//
// 复用 candidateFromConnectorItem 之后，这条映射的义务就只剩「把资源形态填进
// 正确的字段」，不能自己写 ShareURL —— 候选映射是连接器契约的一部分，
// 手写第二份必然与订阅侧漂移（Kind→LinkType、季集→Episode 都靠它）。
func rssItemToConnectorItem(it rss.Item, src domain.RSSSource) connector.Item {
	base := connector.Item{
		Title:     it.Title,
		SourceKey: rssSourceKeyOf(src),
		// 磁力/ed2k 条目的 Provider 刻意**留空**：
		// candidateBlockReason 只在 `Provider != "" && LinkType 非 magnet/ed2k`
		// 时才查网盘类型一致性，而这两类条目根本不走转存通道。
		// 填了反而会拿 feed 里随手写的 provider 去和源上配的目标网盘比对，
		// 然后报一个用户完全看不懂的「资源网盘类型不一致」。
		Provider:  "",
		SizeBytes: it.Length,
		// ⭐ IsUnlocked=true 是硬要求，不是可选优化。
		// candidateBlockReason 里有 `if !cand.IsUnlocked && !cand.PointsKnown`
		// 就直接返回「资源积分未知，已跳过自动解锁」—— RSS 条目拿不到任何积分
		// 信息（那是 RE0/HDHive 站点的概念），不填 true 的话 100% 被拦。
		// 语义上也站得住：RSS 源是用户自己贴的，「直接下」正是用户签的字。
		IsUnlocked: true,
		// Remark 参与词表匹配（RuleSkipReason 匹配 title + "\n" + remark），
		// 填描述让用户配的规则能按描述里的画质信息过滤。
		Remark:    it.Description,
		MediaType: rssMediaTypeOf(src, it),
	}
	if mediaType, ep := extractEpisodeEvidence(it.Title); ep != nil && mediaType == "tv" {
		_ = mediaType
		base.Season = derefInt(ep.SeasonNum, 0)
		base.Episode = derefInt(ep.EpisodeNum, 0)
		base.EndEpisode = derefInt(ep.EndEpisodeNum, 0)
	}
	switch it.Kind {
	case rss.KindMagnet:
		base.Kind = connector.ItemMagnet
		base.MagnetURI = it.ResourceURL
		base.InfoHash = it.ContentIdentity()
	case rss.KindEd2k:
		base.Kind = connector.ItemEd2k
		base.Ed2kLink = it.ResourceURL
		base.InfoHash = it.ContentIdentity()
	case rss.KindTorrent:
		base.Kind = connector.ItemTorrent
		base.DirectURL = it.ResourceURL
		base.InfoHash = it.ContentIdentity()
	case rss.KindDirect:
		base.Kind = connector.ItemDirectLink
		base.DirectURL = it.ResourceURL
	case rss.KindShare:
		base.Kind = connector.ItemShareLink
		base.Slug = it.ResourceURL
		base.ShareCode = it.ResourceURL
	case rss.KindNone:
		// 解析层已滤掉。返回 kind 为空的条目让 candidateFromConnectorItem
		// 返回 false —— 不能返回 share_link，否则空条目指到一个空 URL 上，
		// 会被当成「有条目但链接是空的」而白占一次落地尝试。
		return connector.Item{Title: it.Title, SourceKey: base.SourceKey, IsUnlocked: true}
	}
	return base
}

// rssSourceKeyOf 源的稳定标识，进候选的 Source/ChannelTitle 与事件日志。
// 刻意用 id 而不是 name：用户改源名不该让所有历史记录的归属变样。
func rssSourceKeyOf(src domain.RSSSource) string { return fmt.Sprintf("rss:%d", src.ID) }

// rssProvidersOf 拆源上配的目标网盘。
//
// 迁移 0044 里这一列名 `storage`，允许逗号分隔多值
// （「先转存到 123、转存不了就下磁力」是常见配置）。
// 空串与空项都在这里滤掉，避免把 `""` 当成一个网盘名传给 NormalizeTransferProvider。
func rssProvidersOf(src domain.RSSSource) []string {
	var out []string
	for _, part := range strings.Split(src.Storage, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// rssMediaTypeOf 条目的媒体类型。
//
// 源上配了就用它；没配（存量数据）时从标题猜，猜不出按 tv。
// ⚠️ 兜底必须是 tv 不是 movie：BT RSS 绝大多数是番剧源，兜底 movie 会让
// 每一条 tv 资源都撞「资源类型与订阅类型不一致」被挡下。
func rssMediaTypeOf(src domain.RSSSource, it rss.Item) string {
	if t := strings.ToLower(strings.TrimSpace(src.MediaType)); t == "movie" || t == "tv" {
		return t
	}
	if t, _ := extractEpisodeEvidence(it.Title); t == "tv" {
		return "tv"
	}
	return "tv"
}
