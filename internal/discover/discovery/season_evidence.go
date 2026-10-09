package discovery

import (
	"strings"

	"litepan/internal/mediaorganize/rules"
)

// T06 · 订阅侧的季集证据适配层。
//
// 正则与中文数字解析全在 rules 包（公共函数），这里只做类型映射：
// rules.SeasonEpisode → discovery 内部结构体 candidateEpisode。
// 之所以还要这一层，而不是让 candidateEpisode 直接用 rules.SeasonEpisode，
// 是因为 candidateEpisode 会被序列化进转存账本（json tag 是历史契约，
// T04 的 discovery_transfer_items 里存的就是这份 JSON），换结构会毁掉已落库的数据。

// episodeEvidenceFromText 把公共解析结果映射成订阅内部的季集证据。
//
// 认不出来时返回 (movie, nil) —— 与改动前的行为一致：调用方
// candidateScopeKey 收到空 episode 时回落到 movie scope。
func episodeEvidenceFromText(parts ...string) (string, *candidateEpisode) {
	se := rules.SeasonEpisodeFromText(parts...)
	if !se.IsTV() {
		return "movie", nil
	}
	return "tv", &candidateEpisode{
		SeasonNum:       se.Season,
		EpisodeNum:      se.Episode,
		EndEpisodeNum:   se.EndEpisode,
		TotalEpisodeNum: se.TotalEpisodes,
		IsComplete:      se.Complete,
		IsUpdated:       se.Updated,
	}
}

// nonEmpty 过滤掉空白片段后拼接。与 rules 包的 nonEmptyParts 同义，
// 保留是因为 identity_gate.go 也在用（改动前就定义在这里）。
func nonEmpty(list []string) []string {
	out := []string{}
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
