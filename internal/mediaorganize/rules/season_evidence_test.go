package rules

import "testing"

// T06 · 季集证据解析的表格驱动用例。
//
// 这个文件锁三件事，重要性依次递减：
//
//  1. **中文数字与全角**（任务书验收项⑤⑥）：这是新增能力，改坏了不会有人立刻发现，
//     因为它错的时候是把剧集当成电影 —— 静默地少转存、不报错。
//  2. **「全N集」与「更新中」**：给 season_evidence.go 补 TotalEpisodeNum /
//     IsComplete / IsUpdated，这三个字段支撑 T06 的完结宽限判定。
//  3. **存量行为钉死**：改动前就能匹配的写法必须逐字不变。
//     解析器在这条链路上同时喂给整理侧命名和订阅转存，改动前能出的结果不能丢。

// seasonEpisodeCase 一行一个用例。
type seasonEpisodeCase struct {
	name  string
	parts []string
	want  SeasonEpisode
}

// ptrInt 取 int 指针，表格里写起来比辅助函数短。
func ptrInt(v int) *int { return &v }

func TestSeasonEpisodeFromText(t *testing.T) {
	cases := []seasonEpisodeCase{
		// ---- 一、存量行为：这六例改动前就能匹配，逐字钉死 ----
		{
			name:  "存量/标准 S01E02",
			parts: []string{"Breaking.Bad.S01E02.1080p.WEB-DL.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(1), Episode: ptrInt(2)},
		},
		{
			name:  "存量/带站点备注",
			parts: []string{"The.Wire.S01E02.1080p.WEB.H264-GROUP", "某站备注"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(1), Episode: ptrInt(2)},
		},
		{
			name:  "存量/S02E05",
			parts: []string{"Severance.S02E05.2160p.DV.HDR.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(2), Episode: ptrInt(5)},
		},
		{
			name:  "存量/集数区间 S01E01-E05",
			parts: []string{"The.Office.S01E01-E05.1080p.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(1), Episode: ptrInt(1), EndEpisode: ptrInt(5)},
		},
		{
			name:  "存量/单数字季 S1E2",
			parts: []string{"Some.Show.S1E2.720p.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(1), Episode: ptrInt(2)},
		},
		{
			name:  "存量/只有季号 第2季",
			parts: []string{"三体 第2季"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(2)},
		},

		// ---- 二、中文数字（验收项⑤）----
		{
			name:  "中文数字/第二季 第十二集",
			parts: []string{"流浪地球 第二季 第十二集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(2), Episode: ptrInt(12)},
		},
		{
			name:  "中文数字/第一季 第一集",
			parts: []string{"漫长的季节 第一季 第一集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(1), Episode: ptrInt(1)},
		},
		{
			name:  "中文数字/两字集号 第十二季",
			parts: []string{"某剧 第十二季.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(12)},
		},
		{
			name:  "中文数字/只有集号 第二十四集",
			parts: []string{"某剧 第二十四集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Episode: ptrInt(24)},
		},
		{
			name:  "中文数字/百位 第三季 第九十九集",
			parts: []string{"某剧 第三季 第九十九集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(3), Episode: ptrInt(99)},
		},
		{
			name:  "中文数字/阿拉伯季号配中文集号",
			parts: []string{"三体 第2季 第八集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(2), Episode: ptrInt(8)},
		},
		{
			name:  "中文数字/剧名.第十季.E03",
			parts: []string{"剧名.第十季.E03.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(10), Episode: ptrInt(3)},
		},
		{
			name:  "中文数字/剧名-第二季-第七集",
			parts: []string{"剧名-第二季-第七集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(2), Episode: ptrInt(7)},
		},
		{
			name:  "中文数字/「部」当季号",
			parts: []string{"某剧 第二部 第五集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(2), Episode: ptrInt(5)},
		},

		// ---- 三、全角（验收项⑥）----
		{
			name:  "全角/Ｓ０１Ｅ０１",
			parts: []string{"Ｓ０１Ｅ０１.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(1), Episode: ptrInt(1)},
		},
		{
			name:  "全角/全角大写 S02E05",
			parts: []string{"剧名．Ｓ０２Ｅ０５．１０８０Ｐ．mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(2), Episode: ptrInt(5)},
		},
		{
			name:  "全角/全角小写 s03e11",
			parts: []string{"ｓ０３ｅ１１.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(3), Episode: ptrInt(11)},
		},
		{
			name:  "全角/全角空格分隔",
			parts: []string{"剧名　Ｓ０１Ｅ０３.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(1), Episode: ptrInt(3)},
		},
		{
			name:  "全角/全角区间 Ｓ０１Ｅ０１－Ｅ０５",
			parts: []string{"剧名.Ｓ０１Ｅ０１－Ｅ０５.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(1), Episode: ptrInt(1), EndEpisode: ptrInt(5)},
		},

		// ---- 四、总集数与更新状态（完结宽限的输入）----
		{
			name:  "总集数/全24集 + 季号",
			parts: []string{"三体 第三季 全24集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(3), TotalEpisodes: ptrInt(24), Complete: true},
		},
		{
			name:  "总集数/更新至24集",
			parts: []string{"某剧 第2季 更新至24集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(2), TotalEpisodes: ptrInt(24), Complete: true},
		},
		{
			name:  "总集数/完结至12集",
			parts: []string{"某剧 第2季 完结至12集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(2), TotalEpisodes: ptrInt(12), Complete: true},
		},
		{
			name:  "总集数/中文数字 全二十四集",
			parts: []string{"某剧 第三季 全二十四集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(3), TotalEpisodes: ptrInt(24), Complete: true},
		},
		{
			name:  "总集数/单集 + 全N集 同时出现",
			parts: []string{"某剧 第三季 第5集 全24集.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(3), Episode: ptrInt(5), TotalEpisodes: ptrInt(24), Complete: true},
		},
		{
			name:  "更新状态/更新中",
			parts: []string{"某剧 更新中.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Updated: true},
		},
		{
			name:  "更新状态/连载中",
			parts: []string{"某剧 第二季 连载中.mkv"},
			want:  SeasonEpisode{MediaType: "tv", Season: ptrInt(2), Updated: true},
		},

		// ---- 五、认不出来就不猜 ----
		{
			name:  "电影/纯片名不产生季集",
			parts: []string{"流浪地球 (2019).1080p.BluRay.mkv"},
			want:  SeasonEpisode{MediaType: "movie"},
		},
		{
			name:  "电影/英文名不产生季集",
			parts: []string{"The.Matrix.1999.1080p.BluRay.mkv"},
			want:  SeasonEpisode{MediaType: "movie"},
		},
		// 四位年份绝不能当集号：seTotalRe 的 \d{1,4} 只在带「全/更新至/完结至」
		// 前缀时才生效，而 cnNumber/集号分支都封顶 3 位。
		{
			name:  "电影/四位年份不当集号",
			parts: []string{"某剧 2024.mkv"},
			want:  SeasonEpisode{MediaType: "movie"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SeasonEpisodeFromText(tc.parts...)
			if got.MediaType != tc.want.MediaType {
				t.Fatalf("MediaType = %q, 期望 %q", got.MediaType, tc.want.MediaType)
			}
			assertIntPtr(t, "Season", got.Season, tc.want.Season)
			assertIntPtr(t, "Episode", got.Episode, tc.want.Episode)
			assertIntPtr(t, "EndEpisode", got.EndEpisode, tc.want.EndEpisode)
			assertIntPtr(t, "TotalEpisodes", got.TotalEpisodes, tc.want.TotalEpisodes)
			if got.Complete != tc.want.Complete {
				t.Fatalf("Complete = %v, 期望 %v", got.Complete, tc.want.Complete)
			}
			if got.Updated != tc.want.Updated {
				t.Fatalf("Updated = %v, 期望 %v", got.Updated, tc.want.Updated)
			}
		})
	}
}

func assertIntPtr(t *testing.T, field string, got, want *int) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil:
		t.Fatalf("%s = nil, 期望 %d", field, *want)
	case want == nil:
		t.Fatalf("%s = %d, 期望 nil", field, *got)
	case *got != *want:
		t.Fatalf("%s = %d, 期望 %d", field, *got, *want)
	}
}

// TestNormalizeFullWidth 单独钉住全角归一的边界。
//
// 关键：**全角标点必须原样保留**。中文片名里的「！」是标题的一部分，
// 换成半角会改掉整理侧的片名输出与分组键 —— 那是 00-master 明令本系列不许动的。
// 这个坑我第一版真踩了：映射整个 U+FF01–FF5E 立刻打挂
// parse_test.go 的「孤独摇滚！」「吹响吧！上低音号」两个既有用例。
func TestNormalizeFullWidth(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Ｓ０１Ｅ０１", "S01E01"},
		{"ｓ０３ｅ１１", "s03e11"},
		{"１０８０Ｐ", "1080P"},
		{"剧名　Ｓ０１Ｅ０３", "剧名 S01E03"},
		{"剧名．Ｓ０２Ｅ０５", "剧名．S02E05"},
		{"Ｓ０１Ｅ０１－Ｅ０５", "S01E01－E05"},
		{"Ｓ０１Ｅ０１～Ｅ０５", "S01E01～E05"},
		// 以下是标题的一部分，一律不动。
		{"孤独摇滚！", "孤独摇滚！"},
		{"吹响吧！上低音号", "吹响吧！上低音号"},
		{"特别篇 吹响吧！上低音号～合奏比赛～", "特别篇 吹响吧！上低音号～合奏比赛～"},
		{"某剧（第二季）", "某剧（第二季）"},
		{"某剧，第二季", "某剧，第二季"},
		{"100% 的可能", "100% 的可能"},
		{"无全角字符时原样返回", "无全角字符时原样返回"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := NormalizeFullWidth(tc.in); got != tc.want {
				t.Fatalf("NormalizeFullWidth(%q) = %q, 期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseFilenameStrictKeepsFullWidthTitle 确认全角归一没有渗进片名输出：
// 「吹响吧！上低音号」的标题里必须还留着全角感叹号。
func TestParseFilenameStrictKeepsFullWidthTitle(t *testing.T) {
	got := ParseFilenameStrict("特别篇 吹响吧！上低音号～合奏比赛～ (2023){tmdb-1108306}.mkv")
	if got.Title == "" {
		t.Fatalf("片名解析为空")
	}
	if !containsRune(got.Title, '！') {
		t.Fatalf("片名 %q 丢了全角感叹号 —— 全角归一渗进了标题", got.Title)
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
