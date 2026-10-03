package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"litepan/internal/discover/dmodels"
	"litepan/internal/discover/tgchannel"
)

// TG 频道增量订阅的「停机追赶限制」（对齐参考实现 _limit_catchup 语义）。
//
// 问题：DiscoveryChannel.LastPostID 是增量游标，但增量抓取没有时间上限。
// 服务停机或频道长期拉取失败后重启，解析器会按 ?before= 一路向后翻页回看全部积压帖子
// （单频道 maxPages 100、每页 20 帖，即 2000 帖），把这段历史一次性补转存 ——
// 网盘短时高频转存会触发限流/风控，且用户早已不需要这段陈年资源。
//
// 处理（参考实现原文：「重启后接着上次处理到的位置继续；停机超过 12 小时则从最新消息开始」）：
//  1. 停机时长 > 阈值（默认 12 小时，可在「影视发现设置」配置，0=不限）时，不再从旧游标深翻，
//     直接把游标推进到「最新一页的最新帖」——即丢弃该窗口之外的全部积压，只从最新一页开始处理；
//  2. 若该频道有订阅处于回溯模式（backfill），把被跳过的旧游标写入 CatchupCheckpoints
//     冻结保存，之后每轮抽检时向前补一窗，直到追上它，才真正丢弃这段历史。
//     起点必须与 LastPostID 分开保存：追赶期间 LastPostID 会持续推进（转存失败回退更深的帖
//     靠它下次重扫），若借用 LastPostID 记起点，回退的帖会被推进到已处理区而永久丢失。

// channelCatchupState 一个频道的追赶判定结果
type channelCatchupState struct {
	// Jump 是否需要直接跳到最新（停机过久）
	Jump bool
	// Since 停机时长（Jump 时用于日志）
	Since time.Duration
	// Checkpoints 需要冻结保存的旧游标（有回溯订阅且将被跳过时非空）
	Checkpoints []string
}

// parseChannelCheckpoints 解析频道冻结的追赶起点快照
func parseChannelCheckpoints(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// 脏数据（历史手工改动等）：按无快照处理，不阻断订阅轮询
		log.Printf("[discovery] TG 频道订阅：追赶起点快照解析失败（按无快照继续）：%v", err)
		return nil
	}
	res := make([]string, 0, len(out))
	for _, v := range out {
		if v = strings.TrimSpace(v); v != "" {
			res = append(res, v)
		}
	}
	return res
}

// formatChannelCheckpoints 序列化追赶起点快照（全部清空时返回空串）
func formatChannelCheckpoints(cps []string) string {
	if len(cps) == 0 {
		return ""
	}
	b, err := json.Marshal(cps)
	if err != nil {
		log.Printf("[discovery] TG 频道订阅：追赶起点快照序列化失败：%v", err)
		return ""
	}
	return string(b)
}

// planChannelCatchup 判断该频道是否需要跳过积压、以及是否需要冻结旧游标供后续回补。
//
// subs 用于判断「被跳过的积压是否有订阅还想要」：只有回溯（backfill）订阅会深翻历史，
// 纯增量订阅按语义本就该丢弃旧窗口，故仅在存在回溯订阅时才冻结起点。
func planChannelCatchup(ch *DiscoveryChannel, subs []DiscoverySubscription, _ int) channelCatchupState {
	var st channelCatchupState
	stopID := strings.TrimSpace(ch.LastPostID)
	if stopID == "" {
		// 首启（无游标）：ParseChannelPageRange 天然只抓最新一页，无需追赶判定
		return st
	}
	hours := dmodels.GetChannelCatchupHours()
	if hours <= 0 {
		return st // 0 = 不限：保持原行为，停机多久都从头补
	}
	if ch.LastRunAt.IsZero() {
		// 历史数据 / 新建频道尚未跑过一轮：LastRunAt 零值不是「停机」，不判跳
		return st
	}
	since := time.Since(ch.LastRunAt)
	if since <= time.Duration(hours)*time.Hour {
		return st
	}

	st.Jump = true
	st.Since = since
	for i := range subs {
		if subPrefBool(subs[i], "backfill", false) {
			st.Checkpoints = []string{stopID}
			break
		}
	}
	return st
}

// applyChannelCatchup 执行追赶：把游标提升到频道最新一页，并以「最新一页的最旧帖」为本轮边界。
//
// 关键取舍：游标此时必须先持久化并冻结旧值，否则进程若在推进后崩溃，被跳过的积压段
// 将永久丢失（旧游标已不在库里，无从回补）。
//   - 无回溯订阅：直接丢弃积压，游标推进到最新帖，正常增量继续（等价参考实现的「从最新消息开始」）。
//   - 有回溯订阅：额外冻结旧游标，之后每轮按窗口向前回补，直到追上。
func applyChannelCatchup(ch *DiscoveryChannel, ctx *context.Context, stopID *string, st channelCatchupState) {
	if !st.Jump {
		return
	}
	channel := ch.ChannelName()
	ch.CatchupCheckpoints = formatChannelCheckpoints(mergeCheckpoints(parseChannelCheckpoints(ch.CatchupCheckpoints), catchupStartID(st.Checkpoints), ""))
	ch.NextCatchupAt = time.Now()
	// 推进游标前先落盘：崩溃也不丢被跳过的起点
	if err := SaveChannel(ch); err != nil {
		log.Printf("[discovery] TG 频道订阅：频道 %s 追赶起点保存失败，跳过积压：%v", channel, err)
		return
	}

	// 只抓最新一页，取其中最新帖作为新游标（丢弃其下全部积压）
	latest, _, err := tgchannel.ParseChannelPageRange(*ctx, channel, "", 1)
	if err != nil || len(latest) == 0 {
		// 抓不到就保守放弃本轮（不推进游标，下次重试），避免误推进造成永久跳过
		log.Printf("[discovery] TG 频道订阅：频道 %s 追赶取最新页失败，本轮不跳过积压：%v", channel, err)
		return
	}
	newest := strings.TrimSpace(latest[0].PostID)
	for _, p := range latest {
		if postIDGreater(p.PostID, newest) {
			newest = strings.TrimSpace(p.PostID)
		}
	}
	if newest == "" || !postIDGreater(newest, ch.LastPostID) {
		return // 最新帖不比当前游标更新：无积压可跳
	}
	log.Printf("[discovery] TG 频道订阅：频道 %s %s（游标 %s → %s，积压起点 %q）",
		channel, catchupSummary(st), ch.LastPostID, newest, ch.CatchupCheckpoints)
	ch.LastPostID = newest
	// 本轮以最新一页为界重新抓取：既有最新帖正常进订阅，中间积压待后续回补
	boundary, _ := catchupStopID(ch, parseChannelCheckpoints(ch.CatchupCheckpoints), postIDs(latest))
	*stopID = boundary
}

// postIDs 提取帖子 ID 列表
func postIDs(posts []tgchannel.ChannelPost) []string {
	out := make([]string, 0, len(posts))
	for i := range posts {
		out = append(out, posts[i].PostID)
	}
	return out
}

// syncChannelCheckpoints 无新帖/追赶完成时同步追赶状态（追平则清空快照与抽检时间）
func syncChannelCheckpoints(ch *DiscoveryChannel, posts []tgchannel.ChannelPost) {
	if strings.TrimSpace(ch.CatchupCheckpoints) == "" {
		return
	}
	_, done := catchupStopID(ch, parseChannelCheckpoints(ch.CatchupCheckpoints), postIDs(posts))
	if !done {
		return
	}
	ch.CatchupCheckpoints = ""
	ch.NextCatchupAt = time.Time{}
}

// advanceChannelCatchup 追赶态下按本轮实际扫过的窗口推进冻结起点，并在追平后清空。
//
// 每轮只回补一个窗口（catchupStopID 把抓取边界钉在最新一页，故窗口天然等于一页的帖子数），
// 把积压分摊到多轮，避免一次性补转存打爆网盘；
// 窗口以本轮抓到的帖子数为准，追平（起点已进入最新一页）即结束追赶。
func advanceChannelCatchup(ch *DiscoveryChannel, posts []tgchannel.ChannelPost) {
	cps := parseChannelCheckpoints(ch.CatchupCheckpoints)
	if len(cps) == 0 {
		return
	}
	ids := postIDs(posts)
	boundary, done := catchupStopID(ch, cps, ids)
	if done {
		ch.CatchupCheckpoints = ""
		ch.NextCatchupAt = time.Time{}
		log.Printf("[discovery] TG 频道订阅：频道 %s 追赶完成，积压已回补至最新", ch.ChannelName())
		return
	}
	// 本轮扫过的窗口整体视为已处理：起点整体替换为「本轮最旧帖」。
	// 必须替换而非合并：旧起点（如 1000）本身不比当前游标旧，mergeCheckpoints 裁不掉它，
	// 保留会让下一轮又从 1000 重新回补，追赶永远推进不了。
	if boundary != "" {
		ch.CatchupCheckpoints = formatChannelCheckpoints([]string{boundary})
	}
}

// mergeCheckpoints 合并追赶起点快照（去重 + 新到旧排序）。
// lastPostID 非空时裁掉不比它旧的起点（已被游标越过 = 该段已处理，无需再补）。
func mergeCheckpoints(existing []string, add string, lastPostID string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(existing)+1)
	for _, v := range append(append([]string{}, existing...), add) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		if lastPostID != "" && !postIDGreater(lastPostID, v) {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return postIDGreater(out[i], out[j]) })
	return out
}

// catchupStartID 取最旧的追赶起点（回补应从最旧处一路向前）
func catchupStartID(cps []string) string {
	oldest := ""
	for _, v := range cps {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if oldest == "" || postIDGreater(oldest, v) {
			oldest = v
		}
	}
	return oldest
}

// catchupStopID 返回本轮应使用的抓取游标与是否已追平。
//
//	无快照（空闲态）：游标就是 ch.LastPostID，正常增量。
//	有快照（追赶态）：以「最新一页的最旧帖」为界，使抓取天然只覆盖最新一页，
//	  中间积压不会被一次性补完；起点若已落在该页内即视为追平。
func catchupStopID(ch *DiscoveryChannel, cps []string, newestPage []string) (stopID string, done bool) {
	if len(cps) == 0 {
		return strings.TrimSpace(ch.LastPostID), false
	}
	start := catchupStartID(cps)
	oldestOfPage := ""
	for _, id := range newestPage {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if oldestOfPage == "" || postIDGreater(oldestOfPage, id) {
			oldestOfPage = id
		}
	}
	if start == "" {
		return strings.TrimSpace(ch.LastPostID), true
	}
	if oldestOfPage == "" {
		// 未抓到任何帖（频道无更新）：沿用起点作为边界，等价于继续回补
		return start, false
	}
	// 起点已不旧于本页最旧帖 = 积压已补进最新一页范围
	if !postIDGreater(oldestOfPage, start) {
		return strings.TrimSpace(ch.LastPostID), true
	}
	return oldestOfPage, false
}

// catchupSummary 追赶状态的可读摘要（日志用）
func catchupSummary(st channelCatchupState) string {
	if !st.Jump {
		return ""
	}
	return fmt.Sprintf("停机 %s 超过追赶窗口 %d 小时，跳过积压直达最新",
		st.Since.Round(time.Minute), dmodels.GetChannelCatchupHours())
}
