package moviepilot

import (
	"strings"
	"testing"
)

func TestNormalizeAutoDirName(t *testing.T) {
	cases := []struct {
		in            string
		wantName      string
		wantFanSeason int
	}{
		// TG 分享批次序号 + 年番季标记
		{"2-遮.天 年番4 (2026)", "遮天 (2026)", 4},
		{"2-一斩苍穹 (2026)", "一斩苍穹 (2026)", 0},
		// 无序号无年番：原样
		{"炽夏.Never-Ending-Summer.2026.S01", "炽夏.Never-Ending-Summer.2026.S01", 0},
		// 纯序号前缀变体（点分隔）
		{"3.凡人修仙传 年番2", "凡人修仙传", 2},
		// 序号剥离防误伤：剥后以数字开头（2.5D 类标题本身）不剥
		{"2.5D (2026)", "2.5D (2026)", 0},
		// 序号剥离防误伤：全数字名不剥成空
		{"2026", "2026", 0},
	}
	for _, tc := range cases {
		name, fan := normalizeAutoDirName(tc.in)
		if name != tc.wantName || fan != tc.wantFanSeason {
			t.Fatalf("normalizeAutoDirName(%q) = (%q, %d)，期望 (%q, %d)", tc.in, name, fan, tc.wantName, tc.wantFanSeason)
		}
	}
}

// 用户规则：识别时把年份前所有文字用作片名，年份做辅助；独立文件（无目录级信息）同样适用
func TestBuildAutoMediaStandaloneFileName(t *testing.T) {
	media, err := buildAutoMedia("2-遮.天.年番4.第01集.2160p.mkv", nil)
	if err != nil {
		t.Fatalf("buildAutoMedia 失败：%v", err)
	}
	if media.Category != "tv" {
		t.Fatalf("Category = %s，期望 tv", media.Category)
	}
	if media.Season != 4 || media.Episode != 1 {
		t.Fatalf("季集 = S%dE%d，期望 S04E01（文件名「年番4」应提取为季号）", media.Season, media.Episode)
	}
	if !strings.Contains(media.Title, "遮天") {
		t.Fatalf("Title = %q，期望片名含「遮天」（年份/集号前文字）", media.Title)
	}
}

// 端到端：目录+文件 → buildAutoMedia（关键：年番季号覆盖文件 S01）
func TestBuildAutoMediaWithFanSeason(t *testing.T) {
	dirCtx := &autoDirMedia{Category: "movie", Title: "遮 天", Season: 4, Year: 2026, RawName: "2-遮.天 年番4 (2026)"}
	media, err := buildAutoMedia("S01E180.2026.2160p.WEB-DL.H265.AAC5.1.mp4", dirCtx)
	if err != nil {
		t.Fatalf("buildAutoMedia 失败：%v", err)
	}
	if media.Category != "tv" || media.Season != 4 || media.Episode != 180 {
		t.Fatalf("media = %+v，期望 tv S04E180", media)
	}
	if media.Title != "遮 天" {
		t.Fatalf("Title = %q，期望目录回填标题", media.Title)
	}
}
