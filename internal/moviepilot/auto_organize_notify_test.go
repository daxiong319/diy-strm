package moviepilot

import "testing"

func TestFormatEpisodeRanges(t *testing.T) {
	cases := []struct {
		in   []int
		want string
	}{
		{[]int{1}, "E01"},
		{[]int{1, 2, 3}, "E01-E03"},
		{[]int{1, 2, 3, 7}, "E01-E03、E07"},
		{[]int{24, 1, 2}, "E01-E02、E24"}, // 乱序输入也应正确排序
		{[]int{}, ""},
	}
	for _, tc := range cases {
		if got := formatEpisodeRanges(tc.in); got != tc.want {
			t.Fatalf("formatEpisodeRanges(%v) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestExtractReleaseGroup(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"赌金.2026.S01E01.第1集.2160p.WEB-DL.DoVi.H.265.DDP5.1-Ocat.mkv", "Ocat"},
		{"Movie.2026.2160p.WEB-DL-HHWEB.mkv", "HHWEB"},
		{"NoGroup.2026.1080p.mkv", ""}, // 尾段含数字/点，过滤
		{"plainname.mkv", ""},          // 无 - 段
	}
	for _, tc := range cases {
		if got := extractReleaseGroup(tc.in); got != tc.want {
			t.Fatalf("extractReleaseGroup(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}
