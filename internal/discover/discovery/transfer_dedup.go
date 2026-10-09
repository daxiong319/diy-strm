// 转存前的三层去重裁决（把「永久去重」换成「以媒体库为真值 + 3 小时保护期」）。
//
// 原来的做法是 transferredScopesFor(sub.ID, rule.ID) —— 把订阅历史上所有
// status='transferred' 的 scope 收成集合，**永不过期**。后果是用户自己在网盘删掉一部剧，
// 订阅永远不会再拉回来；洗版换了个版本也永远转不进来。这不是用户想要的语义。
//
// 参考实现 的三层去重，各自的职责（不要混用，也不要指望一层替代另一层）：
//
//	第一层  同轮候选去重  planAndTransferRuleCandidates 里的 batchScopes。
//	                       同一轮内同一个季集范围只转一次 —— 一轮搜索常常命中
//	                       5 个帖都是同一季，没有这层会重复转存 5 次。
//	第二层  账本 + 媒体库真值 + 保护期  本文件 + transfer_ledger.go + media_library.go。
//	                       **唯一的跨轮权威判据**。为什么必须要它：定时器一轮与用户
//	                       点「立即搜索」可能同时跑到同一条目，batchScopes 只在
//	                       单轮内存里挡不住，必须有一个原子占位（账本的 CAS）。
//	第三层  transferredScopesFor 的位点基线。**只在媒体库索引不可用时兜底** ——
//	                       没开 Emby 时媒体库恒为空，第一层永远认为「不在」，
//	                       没有这层兜底就会每轮都转一遍。
//
// 裁决顺序（先便宜后昂贵，先可靠后兜底）：
//
//	账本 CAS 抢占用例  →  媒体库真值  →  第三层基线（仅当媒体库索引不可用）
//
// 账本放在最前面，因为它既是最便宜的一步（一次索引查表），也是唯一能提供
// **原子占用**的一步 —— 「能不能发起转存」这个问题必须由一个原子操作回答，
// 不能是「查一下 → 判断 → 再写入」这三步。
package discovery

import (
	"fmt"
	"log"
	"strings"
)

// DuplicateDecision 一条候选的去重裁决。
type DuplicateDecision struct {
	// Transfer 是否允许发起转存
	Transfer bool
	// Layer 判定来自哪一层，用于日志与账本 reason_code
	Layer string
	// ReasonCode 机器可读的判定码
	ReasonCode string
	// Message 给人看的跳过原因（写进订阅项的 skip_reason）
	Message string
	// Ledger 抢到的账本条目；转存成功/失败时用它推进状态
	Ledger *DiscoveryTransferItem
	// Scope 本条候选的范围键，用于第三层基线比对
	Scope string
	// MediaLibraryHit 媒体库查询结果（供日志与调参）
	MediaLibraryHit MediaLibraryHit
}

// 去重层级标识（写进日志，排查时一眼看出是谁挡下的）。
const (
	dedupLayerSameRound      = "same_round"      // 第一层
	dedupLayerLedger         = "ledger"          // 第二层·保护期
	dedupLayerLedgerCAS      = "ledger_cas"      // 第二层·并发抢占
	dedupLayerLibrary        = "library"         // 第二层·媒体库真值
	dedupLayerLibraryUnknown = "library_unknown" // 第二层·媒体库索引不可用
	dedupLayerBaseline       = "baseline"        // 第三层·位点基线
	dedupLayerPass           = "pass"            // 放行
)

// decideTransfer 去重裁决的唯一入口，由 planAndTransferRuleCandidates 在
// 身份校验通过之后、转存调用之前调用。
//
// 参数 baselineScopes 就是第三层的位点基线（原来 transferredScopesFor 那套）。
// 它**不是**无条件生效的 —— 只有媒体库索引说不出真相时才顶上来，见 baselineFallback。
func decideTransfer(sub *DiscoverySubscription, rule DiscoverySubscriptionRule, cand resourceCandidate, baselineScopes map[string]bool) DuplicateDecision {
	scope := candidateScopeKey(cand)

	// ── 第一层：同轮去重 ───────────────────────────────────────────────────
	// 调用方负责 batchScopes（它在内存里、要跟着整轮走）；这里只处理后面两层。
	_ = scope

	// ── 第二层之一：账本 CAS 抢占 ──────────────────────────────────────────
	// 这一步同时回答两个问题：「这个内容最近转过吗（保护期）」和
	// 「这一轮有别人正在转它吗（并发占用）」。两者由唯一索引原子裁决。
	claim := ClaimTransferSlot(sub, rule, cand)
	contentKey, sha1 := ledgerContentKey(cand)
	entry := &DiscoveryTransferItem{
		ID:          claim.ID,
		Idempotency: LedgerIdempotencyKey(firstNonEmptyStr(rule.TargetProvider, cand.Provider), contentKey, ledgerMediaScope(sub, cand)),
		ContentKey:  contentKey,
		Sha1:        sha1,
		MediaScope:  ledgerMediaScope(sub, cand),
		StorageSlug: firstNonEmptyStr(rule.TargetProvider, cand.Provider),
		Title:       cand.Title,
		State:       LedgerStateTransferRequested,
	}

	if !claim.Claimed {
		log.Printf("[discovery] transfer_dedup skipped=1 layer=%s reason=%s sub_id=%d tmdb_id=%d scope=%s item=%s protected_until=%s",
			dedupLayerLedger, claim.ReasonCode, subIDOf(sub), tmdbIDOf(sub), scope,
			firstNonEmptyStr(cand.Slug, cand.Title), claim.ProtectedUntil.Format("2006-01-02 15:04:05"))
		return DuplicateDecision{
			Layer: dedupLayerLedger, ReasonCode: claim.ReasonCode,
			Message: fmt.Sprintf("刚转存过（保护期内，最早 %s 可再次转存）",
				claim.ProtectedUntil.Format("01-02 15:04")),
			Ledger: entry,
			Scope:  scope,
		}
	}

	// ── 第二层之二：媒体库真值 ──────────────────────────────────────────────
	// 抢到账本不等于该转 —— 媒体库里已经有了照样不用转。
	// 「用户在网盘删了」才会重新拉取，这正是「以媒体库为真值」的语义。
	hit := MediaLibraryPresent(sub, cand)
	if hit.Present {
		// 媒体库里有 ⇒ 用户没删 ⇒ 不转。把条目推进为 confirmed 让它进入保护期，
		// 免得下一轮媒体库还没刷新时又去查一遍。
		ConfirmLedgerEntry(entry)
		log.Printf("[discovery] transfer_dedup skipped=1 layer=%s reason=%s sub_id=%d tmdb_id=%d scope=%s item=%s",
			dedupLayerLibrary, ReasonCodePresentInLibrary, subIDOf(sub), tmdbIDOf(sub), scope,
			firstNonEmptyStr(cand.Slug, cand.Title))
		return DuplicateDecision{
			Layer: dedupLayerLibrary, ReasonCode: ReasonCodePresentInLibrary,
			Message:         fmt.Sprintf("媒体库里已经有「%s」，不重复转存（用户删除后会重新拉取）", firstNonEmptyStr(cand.Title, "该作品")),
			Ledger:          entry,
			Scope:           scope,
			MediaLibraryHit: hit,
		}
	}

	// ── 第三层：位点基线兜底（仅当媒体库索引不可用）─────────────────────────
	if dup := baselineFallback(sub, cand, scope, baselineScopes, hit); dup != nil {
		// 账本占位要退掉：这一轮并没有真的转，条目应当回到可重试状态，
		// 否则用户把 Emby 索引补上之后，反而还要等一个保护期才能重转。
		FailLedgerEntry(entry, ReasonCodeBaselineHit)
		return *dup
	}

	return DuplicateDecision{
		Transfer: true, Layer: dedupLayerPass, Ledger: entry, Scope: scope,
		MediaLibraryHit: hit,
	}
}

// baselineFallback 第三层兜底：**仅在媒体库索引不可用时**才查位点基线。
//
// 为什么要有这层：没开 Emby（或者索引表是空的）时，媒体库真值恒为「不在」，
// 于是每轮订阅都会重新转存一遍同样的东西 —— 对 NAS 用户是实打实的流量与积分浪费。
// 位点基线（历史上转存过的 scope）能兜住这个场景。
//
// 为什么不能无条件用：位点基线是永久的，会把「用户删掉的文件」也一并挡住 ——
// 那正是这次要修的缺陷。所以只在媒体库说不出真相时才用它顶替。
func baselineFallback(sub *DiscoverySubscription, cand resourceCandidate, scope string, baselineScopes map[string]bool, hit MediaLibraryHit) *DuplicateDecision {
	if hit.Available || len(baselineScopes) == 0 || scope == "" || scope == "unknown" {
		return nil
	}
	if !baselineScopes[scope] {
		return nil
	}
	log.Printf("[discovery] transfer_dedup skipped=1 layer=%s reason=%s sub_id=%d scope=%s item=%s media_library=unavailable",
		dedupLayerBaseline, ReasonCodeBaselineHit, subIDOf(sub), scope, firstNonEmptyStr(cand.Slug, cand.Title))
	return &DuplicateDecision{
		Layer: dedupLayerBaseline, ReasonCode: ReasonCodeBaselineHit,
		Message:         "媒体库索引不可用，按历史转存记录跳过（开启 Emby 索引后删除的文件即可重新拉取）",
		Scope:           scope,
		MediaLibraryHit: hit,
	}
}

// describeDedupLayer 把层级标识转成人能读的名字，用作订阅项 skip_reason 的前缀。
// 用户在界面上看到「媒体库真值：媒体库里已经有…」比看到一串 reason_code 有用得多，
// 而 reason_code 仍然完整地留在日志里供 grep。
func describeDedupLayer(layer string) string {
	switch layer {
	case dedupLayerSameRound:
		return "同轮去重"
	case dedupLayerLedger:
		return "转存账本/保护期"
	case dedupLayerLibrary:
		return "媒体库真值"
	case dedupLayerBaseline:
		return "历史基线兜底"
	default:
		return strings.TrimSpace(layer)
	}
}
