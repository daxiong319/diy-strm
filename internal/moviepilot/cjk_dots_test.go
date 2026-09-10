package moviepilot

import (
	"testing"

	"diy-strm/internal/mediaparse"
)

// 回归：「2-遮.天 年番4 (2026)」识别失败/错误。
// 链路：normalizeAutoDirName（剥批次序号「2-」+「年番4」→ S4 + 中文夹点移除）
// → 「遮天 (2026)」→ ParseMedia 正确解析。此前 path.Ext 把「遮.天 (2026)」
// 的点当扩展名分隔符，stem 被截成「遮」导致标题/年份全错。
func TestNormalizeAutoDirNameZheTian(t *testing.T) {
	name, fan := normalizeAutoDirName("2-遮.天 年番4 (2026)")
	if name != "遮天 (2026)" {
		t.Fatalf("normalizeAutoDirName = %q，期望 遮天 (2026)", name)
	}
	if fan != 4 {
		t.Fatalf("年番季号 = %d，期望 4", fan)
	}
	cat, title, _, _, year := mediaparse.ParseMedia(name)
	if cat != "movie" || title != "遮天" || year != 2026 {
		t.Fatalf("ParseMedia(%q) = (%q, %q, %d)，期望 movie/遮天/2026", name, cat, title, year)
	}
}

func TestStripCjkDots(t *testing.T) {
	cases := []struct{ in, want string }{
		{"遮.天 (2026)", "遮天 (2026)"},
		{"遮.天.年番", "遮天年番"},
		{"The.Last.House.2026.mkv", "The.Last.House.2026.mkv"}, // 英文点不动
		{"H.265 编码", "H.265 编码"},                            // 英文数字间的点不动
		{"无点名称", "无点名称"},
	}
	for _, tc := range cases {
		if got := stripCjkDots(tc.in); got != tc.want {
			t.Fatalf("stripCjkDots(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}
