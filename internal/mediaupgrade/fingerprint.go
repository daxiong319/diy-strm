package mediaupgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"litepan/internal/moviepilot"
)

// FingerprintLength 规则指纹长度（SHA256 截前 N 位十六进制）。
//
// 16 位 = 64 bit：碰撞概率对本仓的用途（几十万条记录里标出「规则有没有变过」）
// 可以忽略，而短指纹能直接显示在界面上和日志里 ——
// 用户截图问「这条驳回是哪个规则版本」，16 位比 64 位可抄。
const FingerprintLength = 16

// Fingerprint 规则指纹：回答「这条驳回是哪个规则版本下的」。
//
// 解决的问题：规则一改，旧的驳回理由归属就变了。用户上周看到「因为分辨率被驳回」，
// 这周把 min_resolution 调低了，同一批记录的这句话的含义已经不同 ——
// 光看记录本身分不清。指纹让「同一批记录跨越一次规则变更」可查。
//
// 只哈希**有效判定字段**：名字、扫描源、媒体库根、候选目录、move_dir、
// 分类范围、每剧上限都不参与判定，改了它们不该让整批记录看起来「过期」。
// 哈希的是这六个字段：WashRules / GroupPriority / MinResolution /
// MinChannels / RequireSubtitle / LoserAction。
func (r *RuleSet) Fingerprint() string {
	if r == nil {
		return ""
	}
	// 规则行可能没填 wash_rules，判定时回落默认规则 —— 指纹必须按
	// **实际判定用的那一份**算，否则「空规则」与「显式默认规则」
	// 会得到两个不同指纹，而这两者的判定完全一样。
	wash := r.WashRules
	if len(wash) == 0 {
		wash = moviepilot.DefaultWashRules
	}
	// WashRules 是数组，顺序不该影响判定结果：用户在前端把「编码」从
	// 第 3 项拖到第 1 项，语义上一条规则没变。必须先排序再哈希，
	// 否则一次纯粹的界面调整会让整批记录的指纹失效、看起来像换过规则。
	rules := make([]string, 0, len(wash))
	for _, wr := range wash {
		// Higher 是「这个字段是不是越大越好」。它必须进哈希：
		// 同一组字段反转方向是彻底不同的判定口径。
		rules = append(rules, wr.Field+"\x00"+strconv.FormatBool(wr.Higher))
	}
	sort.Strings(rules)

	group := append([]string(nil), r.GroupPriority...)
	sort.Strings(group)

	var b strings.Builder
	b.WriteString("washrules=")
	b.WriteString(strings.Join(rules, ","))
	b.WriteString(";group_priority=")
	b.WriteString(strings.Join(group, ","))
	b.WriteString(";min_resolution=")
	b.WriteString(strconv.Itoa(r.MinResolution))
	b.WriteString(";min_channels=")
	b.WriteString(strconv.Itoa(r.MinChannels))
	b.WriteString(";require_subtitle=")
	b.WriteString(strconv.FormatBool(r.RequireSubtitle))
	b.WriteString(";loser_action=")
	b.WriteString(r.LoserAction)

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:FingerprintLength]
}
