// RSS 候选的投递入口（T16）。
//
// RSS 与 TMDB 实体订阅的**结构差异**（这是本文件存在的全部理由）：
//
//	RSS 源不产生 DiscoverySubscription 行 —— 它没有 TMDB ID、没有用户可编辑的
//	标题/类型/季集期望，条目的身份信息全在 RSS 标题里。于是下面这些
//	「拿订阅当参数」的既有函数用不了：
//
//	  - identityRequestForSubscription 要求 sub 才能组装期望身份；
//	  - logIdentityDecision / persistIdentityDecision 里 sub.ID / sub.TMDBID 原本是裸取；
//	  - probeMediaLibraryFromEmby 对 sub != nil 会用 sub.Title 覆盖候选标题。
//
//	这三处本轮都已改成 nil 安全（transfer_ledger.go 的 subIDOf/tmdbIDOf、
//	identity_gate.go 的两处落库字段与请求组装）。
//
//	但**必须复用的东西远多于此**，任务书验收④要求「RSS 候选走同一条订阅执行器」，
//	复用的价值全在这里：
//
//	1. 词表过滤（filter.RuleSkipReason）—— 用户配的包含/排除规则对 RSS 同样有意义；
//	2. 账本 CAS + 3 小时保护期（transfer_ledger.go）—— RSS 轮询每 30 分钟一轮，
//	   没有账本的话同一个 info_hash 会被反复提交离线下载；
//	3. 媒体库真值（media_library.go）—— 「网盘里已经有了就别再下」是 RSS 的刚需，
//	   番剧追新每一集都要在媒体库里查。
//
// 本文件**不是**另写一套去重，而是把「没有订阅实体」这一种情况补进既有链路。
package discovery

import (
	"context"
	"fmt"
	"log"
	"strings"

	"litepan/internal/discover/connector"
	"litepan/internal/domain"
)

// RSSCandidate 一条待投递的 RSS 候选。
//
// Key（去重键）与 Cand 绑成一个结构体，而不是两个平行数组：去重键由
// rss.Item.DedupKey 算出，要写进 rss_subscription_history.guid（数据库唯一约束）。
// 如果投递时把 key 丢掉、事后靠 cand 反算，最坏情况是算出一个与写历史时
// 不同的键 —— 那个错的键占住唯一约束后，真正的条目会被**永久吃掉**，
// 症状是「这个源只处理过一条」，且日志里完全看不出异常。
type RSSCandidate struct {
	// Key 去重键（rss.Item.DedupKey 的结果，永不空）。
	Key string
	// Cand 归一化后的候选。
	Cand resourceCandidate
	// Title 展示用标题。
	Title string
}

// RSSDeliveryPlan RSS 源的一次投递计划。
//
// 刻意**不带** DiscoverySubscription：RSS 源没有订阅实体。
type RSSDeliveryPlan struct {
	// SourceID RSS 源 ID（进日志与账本事件，不参与幂等键）。
	SourceID int64
	// SourceName 源名称（日志用）。
	SourceName string
	// MediaType 源上配置的媒体类型（movie / tv），空按 tv 兜底。
	MediaType string
	// Providers 源上配置的目标网盘（123 / guangya / pan139），可能有多个。
	//
	// 迁移 0044 里这一列叫 `storage`，语义与订阅的 target_provider 一致但允许多值
	// （逗号分隔），因为「先转存到 123、转存不了就下磁力」是常见配置。
	Providers []string
	// Action 落地通道：domain.RSSActionTransfer（分享链接走转存）/
	// domain.RSSActionOffline（磁力走离线下载）。
	Action string
	// Candidates 本轮 RSS 解析出的候选。
	Candidates []RSSCandidate
}

// RSSItemOutcome 一条候选的逐条结论。
//
// 按 Key 而不是标题回传：同一个 feed 里出现两条同名不同集的条目很常见
// （"[某番] 01" 与 "[某番] 02" 归一化后可能撞车），用标题回传会把结果张冠李戴。
type RSSItemOutcome struct {
	// Key 对应 RSSCandidate.Key。
	Key string
	// Title 展示用标题。
	Title string
	// Status 三值：rssOutcomeSuccess / Skipped / Failed。
	Status string
	// Reason 被跳过或失败的原因（放行时为空）。
	Reason string
	// Link 实际提交的链接。
	Link string
}

// RSSDeliveryResult 一次投递的结果。
type RSSDeliveryResult struct {
	// Outcomes 逐条结论，顺序与 plan.Candidates 一致。
	Outcomes []RSSItemOutcome
	// Counters 三计数，与 参考实现 同步接口同口径。
	Counters RSSDeliveryCounters
	// notes 口径提示与告警（不阻断其余条目）。
	notes []string
}

// RSSDeliveryCounters 三计数。
type RSSDeliveryCounters struct {
	Added   int
	Skipped int
	Failed  int
}

// Notices 口径提示与告警（去重后的非空串）。
//
// 走接口而不是让调用方直接读字段：这些文案是要渲染到用户面前的，
// 收口在这里才能保证「不重复」「不空」。
func (d RSSDeliveryResult) Notices() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(d.notes))
	for _, n := range d.notes {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// FirstFailure 第一条失败原因（供错误摘要用）。
func (d RSSDeliveryResult) FirstFailure() string {
	for _, o := range d.Outcomes {
		if o.Status == rssOutcomeFailed {
			return o.Reason
		}
	}
	return ""
}

// RSS 投递结果里的三值状态。
//
// 用常量而不是直接用 domain.RSSStatus*：这个包历史上不 import domain，
// 为三个字符串破例不值。service 层负责把两者对上（mapStatusOf 有单测钉着）。
const (
	rssOutcomeSuccess = "success"
	rssOutcomeSkipped = "skipped"
	rssOutcomeFailed  = "failed"
)

// rssCandidateFromItem 把连接器条目映射成订阅链路认得的 RSSCandidate。
//
// 复用 candidateFromConnectorItem 而不是手写字段映射：那条路径已经把
// 「磁力→ShareURL / ed2k→ShareURL / Kind→LinkType / 集号→Episode /
// PointsKnown 只在非 magnet-ed2k 时为真」这些规则固化在连接器契约里，
// 手写第二份必然与订阅侧漂移。
func rssCandidateFromItem(it connector.Item, key string) (RSSCandidate, bool) {
	cand, ok := candidateFromConnectorItem(it)
	if !ok {
		return RSSCandidate{}, false
	}
	cand.Source = firstNonEmptyStr(it.SourceKey, "rss")
	return RSSCandidate{Key: key, Cand: cand, Title: firstNonEmptyStr(it.Title, cand.Title)}, true
}

// DeliverRSSCandidates RSS 候选投递的唯一入口。
//
// 与 planAndTransferRuleCandidates 逐段对齐（顺序也刻意一致，便于对照审阅）：
//
//	词表过滤 → 账本 CAS/保护期 → 媒体库真值 → 提交 → 确认账本
//
// 与订阅链路的三处**有意**差异，都注出来了，不要当成漏写：
//
//	(a) 少了身份闸门。
//	    identity.Manifest 对 magnet/ed2k 一律返回 EvidenceNone
//	    （identity_gate.go 的不变式「拿不到证据就不转存」），RSS 条目 100% 被拦死。
//	    变更说明里点名为结构性冲突，当前解法是 RSS 不走这道闸门，
//	    幂等由「账本 info_hash + rss_subscription_history.guid 唯一约束」承担。
//	    ⚠️ 代价要说清：RSS 提交的磁力**没有经过**「这是不是用户订阅的那部作品」
//	    的校验。feed 贴错、标题解析错都可能下到不想要的东西。变更说明点名。
//	(b) 少了同轮 batchScopes 去重。
//	    交给上面两层，后者是数据库唯一约束，比内存 map 更强也更持久。
//	(c) 提交走离线下载而不是 tgto123 转存 —— 磁力/ed2k 没有 slug 可转存
//	    （transferSubscriptionCandidate 对空 slug 直接报错）。
func DeliverRSSCandidates(ctx context.Context, plan RSSDeliveryPlan) RSSDeliveryResult {
	var out RSSDeliveryResult
	if !RSSServicesReady() {
		out.notes = append(out.notes, "RSS 服务未就绪，本轮没有任何条目被处理")
		return out
	}
	provider := rssPrimaryProvider(plan.Providers)
	mediaType := strings.ToLower(strings.TrimSpace(plan.MediaType))
	if mediaType != "movie" && mediaType != "tv" {
		mediaType = "tv"
	}
	action := strings.ToLower(strings.TrimSpace(plan.Action))
	if action != domain.RSSActionOffline {
		action = domain.RSSActionTransfer
	}

	filter := RuleFilterFromMatch(nil)
	// 规则 ID 进账本事件的稳定标识。RSS 不写 discovery_subscription_items，
	// 但 discovery_transfer_items 照写 —— 那张表才是跨轮去重的权威。
	rule := DiscoverySubscriptionRule{
		ID:             uint(plan.SourceID),
		Name:           firstNonEmptyStr(plan.SourceName, "RSS"),
		Enabled:        true,
		TargetProvider: provider,
		MatchData:      map[string]any{},
	}

	for _, entry := range plan.Candidates {
		cand := entry.Cand
		title := firstNonEmptyStr(entry.Title, cand.Title, cand.ChannelTitle, "(无标题条目)")
		link := firstNonEmptyStr(cand.ShareURL, cand.Slug, cand.AccessCode)
		// 跳过时也要把 skip 计数放进 Outcome，让 service 层写历史时
		// 有据可依（用户要能看到「这条为什么没下」）。
		skip := func(reason string) {
			out.Counters.Skipped++
			out.Outcomes = append(out.Outcomes, RSSItemOutcome{
				Key: entry.Key, Title: title, Status: rssOutcomeSkipped, Reason: reason, Link: link,
			})
		}

		// ── 1. 词表过滤（与订阅链路同一套实现）────────────────────────────
		if reason, blocked := filter.RuleSkipReason(cand.Title, cand.Remark); blocked {
			skip(reason)
			continue
		}

		// ── 2. 落地通道：源上配的是什么就用什么 ───────────────────────────
		// 放在词表过滤之后、账本之前：通道都没接通的条目不该占掉账本名额，
		// 否则用户修好离线下载配置后，之前的失败条目会被 3 小时保护期挡掉。
		channel, cerr := rssChannelFor(cand, action, provider)
		if cerr != nil {
			skip(cerr.Error())
			continue
		}

		// ── 3. 账本 CAS + 保护期 + 媒体库真值（decideTransfer 唯一入口）────
		//    baselineScopes 传空 map：RSS 不用「历史转存 scope 永久兜底」那层，
		//    那层的存在理由是 TMDB 订阅的季集粒度去重，RSS 有 info_hash 键。
		//
		//    ⚠️ 下面所有判定都以 **sub = nil** 进入，这是刻意的，不是遗漏：
		//
		//   - ledgerMediaScope：sub==nil && TMDBID<=0 时走「标题归一化」分支，
		//     正是 RSS 想要的（RSS 没有 TMDB ID）；
		//   - probeMediaLibraryFromEmby：sub!=nil 会用 sub.Title **覆盖候选标题**，
		//     把「源名」当标题会让整个源的条目都被查成同一个作品 —— 所以必须 nil；
		//   - ClaimTransferSlot / decideTransfer 的日志：已换成 subIDOf/tmdbIDOf。
		//
		//    将来若有新代码加进这条链路，**先照 subIDOf 的写法加保护**，
		//    不要在 RSS 路径上直接裸取 sub.X。
		dedup := decideTransfer(nil, rule, cand, map[string]bool{})
		if !dedup.Transfer {
			out.Counters.Skipped++
			reason := firstNonEmptyStr(dedup.Message, dedup.Layer)
			out.Outcomes = append(out.Outcomes, RSSItemOutcome{
				Key: entry.Key, Title: title, Status: rssOutcomeSkipped, Reason: reason, Link: link,
			})
			continue
		}
		if !dedup.MediaLibraryHit.Available {
			// 不阻断：媒体库索引不可用时放行是既有语义（第三层基线顶替）。
			// 但要让用户看得到「这轮没有媒体库去重兜底」——
			// 否则表现是「Emby 里明明有却还是下了一遍」，而界面完全看不出原因。
			// 只提示一次，否则 30 条候选会刷出 30 条相同告警。
			if len(out.notes) == 0 {
				out.notes = append(out.notes, "媒体库索引不可用（Emby 未连接或索引表为空），"+
					"本轮少了「已存在」去重层：媒体库里已有的资源也可能被再次提交。")
			}
		}

		// ── 4. 提交 ────────────────────────────────────────────────────────
		// 不能靠「解析器只产出有链接的条目」这个前提：候选映射有 KindNone 分支，
		// 将来新增资源形态时漏填 ShareURL 就会静默提交空链接。
		if strings.TrimSpace(link) == "" {
			skip("条目没有可提交的下载链接")
			continue
		}
		if err := channel.submit(ctx, link, title); err != nil {
			// 失败必须把账本条目退回可重试状态，否则下轮保护期会把这个 info_hash
			// 挡掉整整 3 小时 —— 一个临时失败会变成 3 小时后才重试。
			FailLedgerEntry(dedup.Ledger, "RSS_SUBMIT_FAILED")
			out.Counters.Failed++
			out.Outcomes = append(out.Outcomes, RSSItemOutcome{
				Key: entry.Key, Title: title, Status: rssOutcomeFailed, Reason: err.Error(), Link: link,
			})
			log.Printf("[rss] 源=%d 提交失败 title=%q link=%q err=%v", plan.SourceID, title, link, err)
			continue
		}
		ConfirmLedgerEntry(dedup.Ledger)
		out.Counters.Added++
		out.Outcomes = append(out.Outcomes, RSSItemOutcome{
			Key: entry.Key, Title: title, Status: rssOutcomeSuccess, Link: link,
		})
		log.Printf("[rss] 源=%d 已提交落地 title=%q kind=%s channel=%s sha1=%s",
			plan.SourceID, title, cand.LinkType, channel.name, orEmpty(cand.Sha1, "-"))
	}
	return out
}

// rssChannel RSS 的一条落地通道。
type rssChannel struct {
	name   string
	submit func(ctx context.Context, link, title string) error
}

// rssChannelFor 按条目的资源形态与源上配的动作选通道。
//
// 关键口径：**资源形态优先于源上配的动作**。用户把源设成「转存」，
// 但 feed 里给的是 magnet，磁力没有网盘分享可转存 —— 这时候报一句
// 「这条是磁力，源配的转存通道用不上」远好过把磁力字符串发给转存接口。
func rssChannelFor(cand resourceCandidate, action, provider string) (rssChannel, error) {
	switch strings.ToLower(strings.TrimSpace(cand.LinkType)) {
	case "magnet", "ed2k", "direct_link", "torrent":
		// 全部走离线下载：只有链接，没有可转存的网盘分享。
		return rssChannel{name: "离线下载", submit: func(ctx context.Context, link, _ string) error {
			return SubmitOfflineLink(ctx, link, provider)
		}}, nil
	case "share_link":
		if action != domain.RSSActionTransfer {
			// 源配的是「只下磁力」，但这条是分享链接。
			return rssChannel{}, fmt.Errorf("源配置为只走离线下载，但这条是网盘分享链接，未处理")
		}
		provider, err := NormalizeTransferProvider(provider)
		if err != nil {
			return rssChannel{}, err
		}
		dir := TransferTargetDir(provider)
		if dir == "" {
			return rssChannel{}, fmt.Errorf("请先在「影视发现 - 基础配置」中配置%s保存目录", resourceProviderName(provider))
		}
		if TransferShareFn == nil {
			return rssChannel{}, fmt.Errorf("转存服务尚未就绪")
		}
		return rssChannel{name: "转存", submit: func(ctx context.Context, link, _ string) error {
			_, _, err := TransferShareLink(ctx, link, "", provider)
			return err
		}}, nil
	default:
		return rssChannel{}, fmt.Errorf("未知的资源类型 %q，未处理", cand.LinkType)
	}
}

// rssPrimaryProvider 取第一个可用的目标网盘。
//
// 迁移 0044 里这一列名 `storage`，允许逗号分隔多值。
// ⚠️ 未知网盘名会被 **丢掉**而不是报错：这个字段是用户手填的，
// 而「填错一个网盘名就让整个源不同步」比「回退到默认网盘」糟糕得多。
// 全部无效时回退到 123（既有转存链的默认目标）。
func rssPrimaryProvider(providers []string) string {
	for _, raw := range providers {
		for _, part := range strings.Split(raw, ",") {
			if p, err := NormalizeTransferProvider(part); err == nil {
				return p
			}
		}
	}
	return "123"
}
