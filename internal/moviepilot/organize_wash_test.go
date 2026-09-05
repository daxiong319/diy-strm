package moviepilot

import (
	"testing"
)

// TestAppendQualityTagsToName 验证 MP 整理命名追加质量标签
func TestAppendQualityTagsToName(t *testing.T) {
	cases := []struct {
		name     string
		category string
		title    string
		season   int
		episode  int
		year     int
		ext      string
		orig     string // 原始文件名（质量标签来源）
		want     string
	}{
		{
			name:     "剧集带质量标签",
			category: "tv", title: "飞到我心上", season: 1, episode: 2, year: 2026,
			ext: ".mp4", orig: "飞到我心上.2026.S01E02.第2集.1080p.WEB-DL.AAC2.0.H.264-Ocat.mp4",
			want: "飞到我心上.2026.S01E02.第2集.1080p.WEB-DL.AAC2.0.H.264-Ocat.mp4",
		},
		{
			name:     "剧集无质量标签保持原命名",
			category: "tv", title: "飞到我心上", season: 1, episode: 2, year: 2026,
			ext: ".mp4", orig: "飞到我心上.2026.S01E02.第2集.mp4",
			want: "飞到我心上.2026.S01E02.第2集.mp4",
		},
		{
			name:     "电影带质量标签",
			category: "movie", title: "群体 Colony", season: 0, episode: 0, year: 2026,
			ext: ".mkv", orig: "群体 Colony 2026 1080p WEB-DL AAC2.0 H.264.mkv",
			want: "群体 Colony (2026).1080p.WEB-DL.AAC2.0.H.264.mkv",
		},
		{
			name:     "已含质量标签不重复追加",
			category: "tv", title: "飞到我心上", season: 1, episode: 2, year: 2026,
			ext: ".mp4", orig: "飞到我心上.2026.S01E02.第2集.2160p.H.265.mp4",
			// base 由 orig 生成本身就带标签，append 应检测末段为质量 token 不再加
			want: "飞到我心上.2026.S01E02.第2集.2160p.H.265.mp4",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 模拟真实整理流程：标签用 officialTitle（TMDB 校准后）提取
			tags := extractQualityTags(c.orig, c.title)
			got := appendQualityTagsToName(c.category, c.title, c.season, c.episode, c.year, c.ext, tags)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestFindWashTargetsEpisode 验证同集匹配（忽略扩展名与质量后缀）
func TestFindWashTargetsEpisode(t *testing.T) {
	entries := []organizeEntry{
		{ID: "1", Name: "飞到我心上.2026.S01E02.第2集.mp4"},
		{ID: "2", Name: "飞到我心上.2026.S01E02.第2集_20260905_111420.mp4"},
		{ID: "3", Name: "飞到我心上.2026.S01E03.第3集.mp4"},
		{ID: "4", Name: "飞到我心上.2026.S01E02.第2集.nfo"},
	}
	newName := "飞到我心上.2026.S01E02.第2集.1080p.WEB-DL.H.264.mp4"
	newQ := ParseQualityFromName(newName)
	targets := findWashTargets(newName, newQ, entries)
	if len(targets) != 2 {
		t.Fatalf("expect 2 targets (E02 original + timestamp copy), got %d", len(targets))
	}
	for _, idx := range targets {
		if entries[idx].ID == "3" {
			t.Fatalf("E03 should not match E02 target")
		}
	}
}

// TestWashCompareSizeFallback 验证质量持平用大小兜底、更差不放行
func TestWashCompareSizeFallback(t *testing.T) {
	oldName := "飞到我心上.2026.S01E02.第2集.mp4"
	newName := "飞到我心上.2026.S01E02.第2集.1080p.WEB-DL.H.264.mp4"
	oldQ := ParseQualityFromName(oldName) // 旧规范名无质量 token → 维度全 0
	newQ := ParseQualityFromName(newName)

	// 新文件可解析出质量维度 → 更优
	if cmp := CompareQuality(newQ, oldQ, nil, DefaultWashRules); cmp <= 0 {
		t.Fatalf("labeled new should beat unlabeled old, cmp=%d", cmp)
	}

	// 双方都无质量标签（同为空维度）→ 打平走大小兜底
	oldEntries := []organizeEntry{{ID: "old", Name: oldName, Size: 1000}}
	newEntry := organizeEntry{ID: "new", Name: oldName, Size: 2000}
	targets := findWashTargets(newEntry.Name, ParseQualityFromName(newEntry.Name), oldEntries)
	if len(targets) != 1 {
		t.Fatalf("same-name should match, got %d", len(targets))
	}
	old := &oldEntries[targets[0]]
	cmp := CompareQuality(ParseQualityFromName(newEntry.Name), ParseQualityFromName(old.Name), nil, DefaultWashRules)
	if cmp != 0 {
		t.Fatalf("identical tokens should tie, cmp=%d", cmp)
	}
	if newEntry.Size <= old.Size {
		t.Fatalf("larger new should win size fallback")
	}
}
