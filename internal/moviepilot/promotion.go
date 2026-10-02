package moviepilot

import (
	"strings"

	"litepan/internal/domain"
)

// NormalizePromotionOrder 规范化促销阶梯顺序：去空、转小写、丢弃未知值、按首次出现去重。
// 空串或全部非法时回退默认顺序。
func NormalizePromotionOrder(order string) string {
	raw := strings.Split(order, ",")
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		v := strings.ToLower(strings.TrimSpace(part))
		if v == "" || seen[v] {
			continue
		}
		if !isKnownPromotionState(v) {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return domain.DefaultPromotionOrder
	}
	return strings.Join(out, ",")
}

// PromotionOrderList 规范化后拆分为切片。
func PromotionOrderList(order string) []string {
	normalized := NormalizePromotionOrder(order)
	if normalized == "" {
		return nil
	}
	return strings.Split(normalized, ",")
}

func isKnownPromotionState(state string) bool {
	for _, s := range domain.PromotionStates {
		if s == state {
			return true
		}
	}
	return false
}

// PromotionStateName 促销状态的中文名，未知状态原样返回。
func PromotionStateName(state string) string {
	if name, ok := domain.PromotionStateNames[state]; ok {
		return name
	}
	return state
}

// LadderDecision 阶梯推进的判定结果。
type LadderDecision struct {
	// Tier 推进后的层号。
	Tier int
	// TierStartedAt 层级起始时间（unix 秒）。
	TierStartedAt int64
	// Reset 表示因为出现新下载而重置回最高优先层。
	Reset bool
	// Relaxed 表示因为超过耐心时长而放宽一层。
	Relaxed bool
	// Changed 表示层号或起始时间发生了变化，需要落库。
	Changed bool
}

// AdvancePromotionLadder 计算促销阶梯的下一步状态。纯函数，便于单测。
//
// 规则（与旧实现一致）：
//  1. 若该订阅在阶梯启动后出现了新下载（latest > tierStartedAt）且当前不在最高层，
//     说明资源已到手，重置回最高优先层；
//  2. 否则若当前层已等待超过 patience 且还有更宽的层可用，则放宽一层；
//  3. 否则保持不动。
//
// now 与 latest 均为 unix 秒；latest <= 0 表示没有下载记录。
func AdvancePromotionLadder(order []string, tier int, tierStartedAt, now, latest int64, patienceSeconds int64) LadderDecision {
	d := LadderDecision{Tier: tier, TierStartedAt: tierStartedAt}
	if len(order) == 0 {
		return d
	}
	if d.Tier < 0 {
		d.Tier = 0
		d.Changed = true
	}
	if maxTier := len(order) - 1; d.Tier > maxTier {
		d.Tier = maxTier
		d.Changed = true
	}
	if d.TierStartedAt <= 0 {
		d.TierStartedAt = now
		d.Changed = true
	}
	// 1. 新下载 → 重置回最高优先层
	if latest > 0 && latest > d.TierStartedAt && d.Tier > 0 {
		d.Tier = 0
		d.TierStartedAt = now
		d.Reset = true
		d.Changed = true
		return d
	}
	// 2. 超过耐心 → 放宽一层
	if patienceSeconds > 0 && now-d.TierStartedAt > patienceSeconds && d.Tier < len(order)-1 {
		d.Tier++
		d.TierStartedAt = now
		d.Relaxed = true
		d.Changed = true
	}
	return d
}

// LadderTierLabel 阶梯层的可读描述，例如 "free>2xfree"。
func LadderTierLabel(order []string, tier int) string {
	if len(order) == 0 || tier < 0 {
		return ""
	}
	if tier >= len(order) {
		tier = len(order) - 1
	}
	return strings.Join(order[:tier+1], ">")
}
