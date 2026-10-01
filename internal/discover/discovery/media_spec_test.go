package discovery

import "testing"

func TestParseMediaSpec(t *testing.T) {
	tests := []struct {
		name string
		text string
		want MediaSpec
	}{
		{
			name: "2160p WEB-DL H265 HDR",
			text: "2160p WEB-DL H265 HDR 20GB",
			want: MediaSpec{Resolution: 3, Source: 3, Codec: 2, Effect: 2, SizeGB: 20},
		},
		{
			name: "1080p BluRay x264",
			text: "1080p BluRay x264 5.5GB",
			want: MediaSpec{Resolution: 2, Source: 4, Codec: 1, Effect: 1, SizeGB: 5.5},
		},
		{
			name: "4K REMUX DV",
			text: "4K REMUX DV 60GB",
			want: MediaSpec{Resolution: 3, Source: 5, Codec: 0, Effect: 3, SizeGB: 60},
		},
		{
			// 体积封顶：SizeGB 仍原样保留 200，Score 的 size 部分封顶 100
			name: "体积封顶 200GB",
			text: "2160p WEB-DL H265 HDR 200GB",
			want: MediaSpec{Resolution: 3, Source: 3, Codec: 2, Effect: 2, SizeGB: 200},
		},
		{
			// 数字紧贴字母（HDR10.1TB）不算体积
			name: "HDR10.1TB 不被当成体积",
			text: "2160p HDR10.1TB",
			want: MediaSpec{Resolution: 3, Source: 0, Codec: 0, Effect: 2, SizeGB: 0},
		},
		{
			name: "720p HDTV",
			text: "720p HDTV",
			want: MediaSpec{Resolution: 1, Source: 1, Codec: 0, Effect: 1, SizeGB: 0},
		},
		{
			// 空文本：Effect 兜底为 1（SDR），Score = 10
			name: "空文本",
			text: "",
			want: MediaSpec{Resolution: 0, Source: 0, Codec: 0, Effect: 1, SizeGB: 0},
		},
		{
			name: "TB 单位换算为 GB",
			text: "2160p BluRay 1.5TB",
			want: MediaSpec{Resolution: 3, Source: 4, Codec: 0, Effect: 1, SizeGB: 1536},
		},
		{
			name: "WEBRip 与 HEVC",
			text: "1080p WEBRip HEVC 3GB",
			want: MediaSpec{Resolution: 2, Source: 2, Codec: 2, Effect: 1, SizeGB: 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseMediaSpec(tt.text); got != tt.want {
				t.Fatalf("ParseMediaSpec(%q) = %+v, want %+v", tt.text, got, tt.want)
			}
		})
	}
}

func TestMediaSpecScore(t *testing.T) {
	tests := []struct {
		name string
		spec MediaSpec
		want int
	}{
		{
			name: "2160p WEB-DL H265 HDR 20GB",
			spec: MediaSpec{Resolution: 3, Source: 3, Codec: 2, Effect: 2, SizeGB: 20},
			want: 3*10000 + 3*1000 + 2*100 + 2*10 + 20,
		},
		{
			name: "1080p BluRay H264 SDR 5.5GB",
			spec: MediaSpec{Resolution: 2, Source: 4, Codec: 1, Effect: 1, SizeGB: 5.5},
			want: 2*10000 + 4*1000 + 1*100 + 1*10 + 5,
		},
		{
			// 体积封顶：200GB 的 size 部分按 100 计
			name: "体积封顶 200GB",
			spec: MediaSpec{Resolution: 3, Source: 3, Codec: 2, Effect: 2, SizeGB: 200},
			want: 3*10000 + 3*1000 + 2*100 + 2*10 + 100,
		},
		{
			name: "体积正好 100 不封顶",
			spec: MediaSpec{Resolution: 2, Source: 3, Codec: 2, Effect: 2, SizeGB: 100},
			want: 2*10000 + 3*1000 + 2*100 + 2*10 + 100,
		},
		{
			name: "空规格仅 Effect 兜底",
			spec: MediaSpec{Effect: 1},
			want: 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.Score(); got != tt.want {
				t.Fatalf("%+v.Score() = %d, want %d", tt.spec, got, tt.want)
			}
		})
	}
}

func TestMediaSpecBetterThan(t *testing.T) {
	tests := []struct {
		name string
		s, o MediaSpec
		want bool
	}{
		{
			name: "2160p 优于 1080p",
			s:    MediaSpec{Resolution: 3, Source: 3, Codec: 2, Effect: 2, SizeGB: 20},
			o:    MediaSpec{Resolution: 2, Source: 4, Codec: 2, Effect: 2, SizeGB: 20},
			want: true,
		},
		{
			name: "1080p 不优于 2160p",
			s:    MediaSpec{Resolution: 2, Source: 4, Codec: 2, Effect: 2, SizeGB: 20},
			o:    MediaSpec{Resolution: 3, Source: 3, Codec: 2, Effect: 2, SizeGB: 20},
			want: false,
		},
		{
			name: "完全相同不更优",
			s:    MediaSpec{Resolution: 2, Source: 4, Codec: 1, Effect: 1, SizeGB: 5.5},
			o:    MediaSpec{Resolution: 2, Source: 4, Codec: 1, Effect: 1, SizeGB: 5.5},
			want: false,
		},
		{
			name: "同分辨率下来源更优（BluRay > WEB-DL）",
			s:    MediaSpec{Resolution: 2, Source: 4, Codec: 2, Effect: 2, SizeGB: 5},
			o:    MediaSpec{Resolution: 2, Source: 3, Codec: 2, Effect: 2, SizeGB: 20},
			want: true,
		},
		{
			name: "仅体积更大也算更优（未封顶范围）",
			s:    MediaSpec{Resolution: 2, Source: 3, Codec: 2, Effect: 2, SizeGB: 30},
			o:    MediaSpec{Resolution: 2, Source: 3, Codec: 2, Effect: 2, SizeGB: 20},
			want: true,
		},
		{
			name: "体积封顶后不再更优",
			s:    MediaSpec{Resolution: 2, Source: 3, Codec: 2, Effect: 2, SizeGB: 200},
			o:    MediaSpec{Resolution: 2, Source: 3, Codec: 2, Effect: 2, SizeGB: 100},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.BetterThan(tt.o); got != tt.want {
				t.Fatalf("%+v.BetterThan(%+v) = %v, want %v", tt.s, tt.o, got, tt.want)
			}
		})
	}
}

func TestWashTargetScore(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   int
	}{
		{"空字符串", "", 0},
		{"1080p", "1080p", 2 * 10000},
		{"1080p 大小写与空白", "  1080P  ", 2 * 10000},
		{"4k", "4k", 3 * 10000},
		{"4k 大小写与空白", " 4K ", 3 * 10000},
		{"4k_remux", "4k_remux", 3*10000 + 5*1000},
		{"4k_remux 大小写与空白", " 4K_REMUX ", 3*10000 + 5*1000},
		{"非法值拼写错误", "4k-remux", 0},
		{"非法值 2160p", "2160p", 0},
		{"非法值乱码", "不知道", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WashTargetScore(tt.target); got != tt.want {
				t.Fatalf("WashTargetScore(%q) = %d, want %d", tt.target, got, tt.want)
			}
		})
	}
}

func TestWashTargetReached(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		oldScore int
		want     bool
	}{
		{
			// 空 target = 无限制，视为未达标，允许继续升级
			name:     "空 target 允许继续升级",
			target:   "",
			oldScore: 999999,
			want:     false,
		},
		{
			name:     "纯空白 target 视为空",
			target:   "   ",
			oldScore: 999999,
			want:     false,
		},
		{
			name:     "1080p 恰好达标",
			target:   "1080p",
			oldScore: 2 * 10000,
			want:     true,
		},
		{
			name:     "1080p 超过达标",
			target:   "1080p",
			oldScore: 3 * 10000,
			want:     true,
		},
		{
			name:     "1080p 未达标（720p 分数）",
			target:   "1080p",
			oldScore: 1 * 10000,
			want:     false,
		},
		{
			name:     "1080p 大小写与空白容错",
			target:   " 1080P ",
			oldScore: 2 * 10000,
			want:     true,
		},
		{
			name:     "4k 达标",
			target:   "4k",
			oldScore: 3 * 10000,
			want:     true,
		},
		{
			name:     "4k 未达标（1080p 分数）",
			target:   "4k",
			oldScore: 2 * 10000,
			want:     false,
		},
		{
			name:     "4k_remux 达到阈值",
			target:   "4k_remux",
			oldScore: 3*10000 + 5*1000,
			want:     true,
		},
		{
			name:     "4k_remux 低于阈值（纯 4k 分数）",
			target:   "4k_remux",
			oldScore: 3 * 10000,
			want:     false,
		},
		{
			// 非法 target 必须按未达标处理，防止拼写错误导致洗版静默失效
			name:     "非法 target 按未达标处理",
			target:   "4k-remux",
			oldScore: 999999,
			want:     false,
		},
		{
			name:     "非法 target 2160p 按未达标处理",
			target:   "2160p",
			oldScore: 999999,
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WashTargetReached(tt.target, tt.oldScore); got != tt.want {
				t.Fatalf("WashTargetReached(%q, %d) = %v, want %v", tt.target, tt.oldScore, got, tt.want)
			}
		})
	}
}

func TestNameMatchesTitle(t *testing.T) {
	tests := []struct {
		name  string
		fname string
		title string
		want  bool
	}{
		{"完全相等", "流浪地球", "流浪地球", true},
		{"大小写与空白归一后相等", "  The Movie  ", "the movie", true},
		{"文件名带规格后缀（title 前缀）", "The Movie.2160p.WEB-DL.mkv", "The Movie", true},
		{"标题带更多后缀（name 前缀）", "The Movie", "The Movie.2160p", true},
		{"无点分隔不算前缀", "The MovieX", "The Movie", false},
		{"不同标题", "流浪地球", "流浪地球2", false},
		{"空文件名", "", "流浪地球", false},
		{"空标题", "流浪地球", "", false},
		{"两者都空", "  ", "  ", false},
		{"大小写不同前缀匹配", "MOVIE.1080p.mkv", "movie", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NameMatchesTitle(tt.fname, tt.title); got != tt.want {
				t.Fatalf("NameMatchesTitle(%q, %q) = %v, want %v", tt.fname, tt.title, got, tt.want)
			}
		})
	}
}

func TestDiscoveryTransferRecordToMediaSpec(t *testing.T) {
	r := &DiscoveryTransferRecord{
		Resolution: 3,
		Source:     5,
		Codec:      2,
		Effect:     3,
		SizeGB:     60,
	}
	want := MediaSpec{Resolution: 3, Source: 5, Codec: 2, Effect: 3, SizeGB: 60}
	if got := r.ToMediaSpec(); got != want {
		t.Fatalf("ToMediaSpec() = %+v, want %+v", got, want)
	}
	// 零值记录应还原为零值规格
	var zero DiscoveryTransferRecord
	if got := zero.ToMediaSpec(); got != (MediaSpec{}) {
		t.Fatalf("zero ToMediaSpec() = %+v, want zero MediaSpec", got)
	}
}
