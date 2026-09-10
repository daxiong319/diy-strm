package moviepilot

import "testing"

func TestNormalizeAutoDirName(t *testing.T) {
	cases := []struct {
		in            string
		wantName      string
		wantFanSeason int
	}{
		// TG 分享批次序号 + 年番季标记
		{"2-遮.天 年番4 (2026)", "遮.天 (2026)", 4},
		{"2-一斩苍穹 (2026)", "一斩苍穹 (2026)", 0},
		// 无序号无年番：原样
		{"炽夏.Never-Ending-Summer.2026.S01", "炽夏.Never-Ending-Summer.2026.S01", 0},
		// 纯序号前缀变体（点分隔）
		{"3.凡人修仙传 年番2", "凡人修仙传", 2},
	}
	for _, tc := range cases {
		name, fan := normalizeAutoDirName(tc.in)
		if name != tc.wantName || fan != tc.wantFanSeason {
			t.Fatalf("normalizeAutoDirName(%q) = (%q, %d)，期望 (%q, %d)", tc.in, name, fan, tc.wantName, tc.wantFanSeason)
		}
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
