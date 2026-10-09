package discovery

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"litepan/internal/discover/ddb"
)

// T06 · 完结宽限的无状态推导。
//
// 参考实现 docs 的完结判据原文：「TMDB 非 Ended 的剧，本季转存齐后
// 最后一集播出满该天数才判完结」。
//
// 移植到 litepan 时的取舍：
//
//   - **不加列**。T06 任务书标注迁移号 = 无，而落一列 finished_at
//     就要写迁移。改成从已有数据推导：
//     本季总集数 ← 候选的 TotalEpisodeNum（标题「全N集」）/ EndEpisode / 最大 Episode；
//     已转存集号 ← discovery_subscription_items 中 status='transferred' 的行，
//     季集信息从 Candidate JSON 还原；
//     「转存齐」= 已转存集号集合 ⊇ [1..total]；
//     完结时刻 = 该季最后一条 transferred_at 的最大值（由数据推出，不落库）。
//
//   - **总集数未知就永不判完结**（fail-open）。标题没写「全N集」、
//     订阅里也没有 TMDB 总集数时，拿不到分母，硬猜会把还在更新的剧判死。
//     这个方向的错误代价明显更大：判死 = 订阅永久停止。
//
// 宽限期内不是停止，而是**降档继续搜**（见 runBudget.enforceGrace）。

// finishedState 一部剧的完结推导结果。
type finishedState struct {
	// Finished 本季已转存齐且宽限期已过 —— 应当停止检索。
	Finished bool
	// InGrace 本季已转存齐但还在宽限期内 —— 继续搜，但强制保守档。
	InGrace bool
	// Total 本季总集数；0 表示未知（此时不判完结）。
	Total int
	// TransferredCount 本季已转存集数。
	TransferredCount int
	// FinishedAt 本季最后一条转存时刻（完结时刻的推导依据）。
	FinishedAt time.Time
	// Reason 人类可读的判据说明，写进运行详情。
	Reason string
}

// seasonTransferRecord 一季的已转存记录。
type seasonTransferRecord struct {
	episodes   map[int]bool
	last       time.Time
	knownTotal int
}

// deriveFinishedState 判断一条订阅某季是否已完结/在宽限期内。
//
// candidates 是**本轮**检索到的候选，可以为空：
//   - 传 nil 是在检索**之前**判一次。分母只能从历史转存记录里拿，
//     所以这个口径会漏判（历史行里没写「全N集」就推不出分母），
//     但它能挡住绝大多数「早就完结」的剧 —— 那些剧的历史行里必然带过总集数，
//     不该再为它们白花一次检索请求。
//   - 检索之后再判一次，补上两个情况：本轮候选第一次给出总集数（此前未知），
//     以及本轮刚好把最后一集补齐。
func deriveFinishedState(sub *DiscoverySubscription, rule DiscoverySubscriptionRule, candidates []resourceCandidate) finishedState {
	season := seasonOfRule(rule)
	rec := transferredEpisodesOf(sub.ID, season)
	total := rec.knownTotal
	if fromCandidates := inferSeasonTotal(candidates, season); fromCandidates > total {
		total = fromCandidates
	}
	if total <= 0 {
		// 拿不到分母就不判 —— 见上方「fail-open」。
		return finishedState{Reason: "本季总集数未知，不判定完结"}
	}
	if len(rec.episodes) == 0 {
		return finishedState{Total: total, Reason: "本季尚无转存记录"}
	}
	for ep := 1; ep <= total; ep++ {
		if !rec.episodes[ep] {
			return finishedState{
				Total: total, TransferredCount: len(rec.episodes), FinishedAt: rec.last,
				Reason: fmt.Sprintf("本季尚未转存齐（%d/%d）", len(rec.episodes), total),
			}
		}
	}

	// 转存齐了。最后一集的转存时刻 + 宽限天数 = 完结判定线。
	//
	// 口径：**满** graceDays（即 elapsed >= graceDays）判完结，
	// 对应任务书验收项④的「宽限 7 天内仍搜索、第 8 天判定完结」——
	// 最后一集记在第 1 天，宽限覆盖第 1~7 天，第 8 天起不再搜。
	//
	// 方向必须是这一边：判完结不可逆（之后永远不再检索这条规则），
	// 而继续搜最多是多几次被去重挡掉的空转。写成 elapsed > graceDays
	// 会在边界那一秒提前把刚转存齐的剧永久停掉。
	deadline := rec.last.Add(time.Duration(finishedGraceDays()) * 24 * time.Hour)
	inGrace := time.Now().Before(deadline)
	reason := fmt.Sprintf("本季已转存齐 %d 集", total)
	if finishedGraceDays() > 0 {
		reason += fmt.Sprintf("，宽限至 %s", deadline.Format("2006-01-02 15:04"))
	}
	return finishedState{
		Finished:         !inGrace,
		InGrace:          inGrace,
		Total:            total,
		TransferredCount: len(rec.episodes),
		FinishedAt:       rec.last,
		Reason:           reason,
	}
}

// seasonOfRule 取规则所属季号。规则的 MatchData 里带 season 时优先用它。
func seasonOfRule(rule DiscoverySubscriptionRule) int {
	if s := seasonFromMatchData(rule.MatchData); s > 0 {
		return s
	}
	return 0
}

// seasonFromMatchData 从规则已解析的 MatchData 里取季号。
//
// MatchData 是 map[string]any（DiscoverySubscriptionRule.MatchData，subscriptions.go:93），
// 由 parseJSONObject(rules[i].Match) 填充，所以这里不再自己反序列化字符串。
func seasonFromMatchData(data map[string]any) int {
	if len(data) == 0 {
		return 0
	}
	for _, key := range []string{"season", "season_num", "season_number"} {
		v, ok := data[key]
		if !ok {
			continue
		}
		if n, ok := toFloat(v); ok && n > 0 {
			return int(n)
		}
	}
	return 0
}

// inferSeasonTotal 从候选里推本季总集数（分母）。
//
// **只认「显式总集数」**：标题里写明的「全N集 / 更新至N集 / 完结至N集」。
//
// 为什么不用 max(EpisodeNum) 或 EndEpisode 当分母 —— 这两个都不是「本季共几集」：
//   - 「某剧 第01-05集」是**打包的 5 集**，不是本季只有 5 集；
//   - 「某剧 S02E08」只说明站上更新到第 8 集，不说明一共 8 集。
//
// 拿它们当分母的后果很严重：一部 24 集的剧转存到 E01~E05 恰好连续，
// 分母被推成 5、集合又刚好覆盖 1..5，于是被判「本季已转存齐」→
// 宽限期一过就永久停订。用户看到的是「订了半年突然不转了」，日志里还写着
// 「本季已转存齐 5 集」，完全看不出是分母被算错了。
//
// 只统计 season 匹配（或不带季号）的候选：把其它季的「全24集」算进来
// 会让分母虚高，于是永远等不到「转存齐」，完结判定彻底失效（另一个方向的错）。
// 宁可漏判（继续搜）也不误判（永久停订）。
func inferSeasonTotal(candidates []resourceCandidate, season int) int {
	total := 0
	for _, cand := range candidates {
		if cand.MediaType != "tv" || cand.Episode == nil {
			continue
		}
		if season > 0 && cand.Episode.SeasonNum != nil && *cand.Episode.SeasonNum != season {
			continue
		}
		if n := cand.Episode.TotalEpisodeNum; n != nil && *n > total {
			total = *n
		}
	}
	return total
}

// transferredEpisodesOf 取出某订阅某季已转存的集号集合、最晚转存时刻，
// 以及历史行里见过的最大总集数（当分母用）。
//
// 季集信息存在 Candidate JSON 里（item_key 只保证唯一、不保证可解析出集号），
// 所以逐行反序列化。老行没有这列时 skip 掉，不猜。
//
// 注意不按 ruleID 过滤：item 行是订阅级的，一条规则转存的季集
// 对同订阅的其它规则同样是已转存（网盘里就是同一份文件）。
func transferredEpisodesOf(subID uint, season int) seasonTransferRecord {
	rec := seasonTransferRecord{episodes: map[int]bool{}}

	var rows []DiscoverySubscriptionItem
	if err := ddb.Db.Where("subscription_id = ? AND status = ?", subID, "transferred").Find(&rows).Error; err != nil {
		return rec
	}
	for _, row := range rows {
		if strings.TrimSpace(row.Candidate) == "" {
			continue
		}
		var cand resourceCandidate
		if err := json.Unmarshal([]byte(row.Candidate), &cand); err != nil {
			continue
		}
		if cand.MediaType != "tv" || cand.Episode == nil {
			continue
		}
		if season > 0 && cand.Episode.SeasonNum != nil && *cand.Episode.SeasonNum != season {
			continue
		}
		if n := inferSeasonTotal([]resourceCandidate{cand}, season); n > rec.knownTotal {
			rec.knownTotal = n
		}
		if cand.Episode.EpisodeNum == nil {
			continue
		}
		rec.episodes[*cand.Episode.EpisodeNum] = true
		if row.TransferredAt != nil && row.TransferredAt.After(rec.last) {
			rec.last = *row.TransferredAt
		}
	}
	return rec
}
