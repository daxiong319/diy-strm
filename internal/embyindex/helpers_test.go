package embyindex

import (
	"errors"
	"testing"

	"litepan/internal/discover/embyclient"
)

// TestBuildMinDateLastSaved 覆盖游标回退重叠窗口的数学。
// 期望值与老版 internal/emby/incremental_test.go 保持一致。
func TestBuildMinDateLastSaved(t *testing.T) {
	cases := []struct {
		name          string
		cursor        int64
		overlapSecond int64
		want          string
	}{
		{
			name:          "正常回退十分钟重叠",
			cursor:        1782698400,
			overlapSecond: 600,
			want:          "2026-06-29T01:50:00Z",
		},
		{
			name:          "零游标退化为 Unix 零点",
			cursor:        0,
			overlapSecond: 600,
			want:          "1970-01-01T00:00:00Z",
		},
		{
			name:          "游标小于重叠窗口时不产生负时间",
			cursor:        300,
			overlapSecond: 600,
			want:          "1970-01-01T00:00:00Z",
		},
		{
			name:          "负重叠按零处理",
			cursor:        1782698400,
			overlapSecond: -100,
			want:          "2026-06-29T02:00:00Z",
		},
		{
			name:          "游标恰好等于重叠窗口时归零",
			cursor:        600,
			overlapSecond: 600,
			want:          "1970-01-01T00:00:00Z",
		},
		{
			name:          "游标比重叠窗口大一秒",
			cursor:        601,
			overlapSecond: 600,
			want:          "1970-01-01T00:00:01Z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildMinDateLastSaved(tc.cursor, tc.overlapSecond)
			if got != tc.want {
				t.Fatalf("BuildMinDateLastSaved(%d, %d) = %q，期望 %q",
					tc.cursor, tc.overlapSecond, got, tc.want)
			}
		})
	}
}

// TestOverlapConstantIsTenMinutes 固定住命名常量的值，
// 避免有人改动它而悄悄破坏「不遗漏条目」的保证。
func TestOverlapConstantIsTenMinutes(t *testing.T) {
	if EmbyIncrementalCursorOverlapSeconds != 600 {
		t.Fatalf("EmbyIncrementalCursorOverlapSeconds = %d，期望 600",
			EmbyIncrementalCursorOverlapSeconds)
	}
}

// TestExtractPickCodeFromPath 表驱动覆盖 PickCode 解析。
func TestExtractPickCodeFromPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "pickcode 查询参数",
			path: "https://cdn.example.com/video.mkv?pickcode=abc123&other=1",
			want: "abc123",
		},
		{
			name: "pick_code 下划线写法",
			path: "https://cdn.example.com/video.mkv?pick_code=under_score",
			want: "under_score",
		},
		{
			name: "pickcode 优先于 pick_code",
			path: "https://cdn.example.com/v.mkv?pick_code=second&pickcode=first",
			want: "first",
		},
		{
			name: "空串返回空串",
			path: "",
			want: "",
		},
		{
			name: "非 http 前缀返回空串",
			path: "/mnt/media/电影/影片.mkv",
			want: "",
		},
		{
			name: "STRM 相对路径返回空串",
			path: "电影/影片.strm",
			want: "",
		},
		{
			name: "ftp 协议不算直链",
			path: "ftp://example.com/video.mkv?pickcode=abc",
			want: "",
		},
		{
			name: "无查询参数时返回整条路径作为兜底",
			path: "https://cdn.example.com/video.mkv",
			want: "https://cdn.example.com/video.mkv",
		},
		{
			name: "查询参数值为空时回退整条路径",
			path: "https://cdn.example.com/video.mkv?pickcode=",
			want: "https://cdn.example.com/video.mkv?pickcode=",
		},
		{
			name: "http 前缀同样识别",
			path: "http://cdn.example.com/video.mkv?pickcode=plain_http",
			want: "plain_http",
		},
		{
			name: "首尾空白被裁剪",
			path: "  https://cdn.example.com/video.mkv?pickcode=trimmed  ",
			want: "trimmed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractPickCodeFromPath(tc.path); got != tc.want {
				t.Fatalf("ExtractPickCodeFromPath(%q) = %q，期望 %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestExtractPickCode 覆盖多媒体源的选择顺序。
func TestExtractPickCode(t *testing.T) {
	t.Run("取第一个可解析的媒体源", func(t *testing.T) {
		sources := []embyclient.MediaSource{
			{Path: "/local/path.mkv"},
			{Path: "https://cdn.example.com/a.mkv?pickcode=second"},
			{Path: "https://cdn.example.com/b.mkv?pickcode=third"},
		}
		code, path, err := ExtractPickCode(sources)
		if err != nil {
			t.Fatalf("意外错误：%v", err)
		}
		if code != "second" {
			t.Fatalf("PickCode = %q，期望 second", code)
		}
		if path != "https://cdn.example.com/a.mkv?pickcode=second" {
			t.Fatalf("路径 = %q", path)
		}
	})

	t.Run("空列表返回错误", func(t *testing.T) {
		_, _, err := ExtractPickCode(nil)
		if !errors.Is(err, ErrPickCodeNotFound) {
			t.Fatalf("错误 = %v，期望 ErrPickCodeNotFound", err)
		}
	})

	t.Run("全部不可解析时返回最后一条路径与错误", func(t *testing.T) {
		sources := []embyclient.MediaSource{
			{Path: "/local/a.mkv"},
			{Path: "/local/b.mkv"},
		}
		_, path, err := ExtractPickCode(sources)
		if !errors.Is(err, ErrPickCodeNotFound) {
			t.Fatalf("错误 = %v，期望 ErrPickCodeNotFound", err)
		}
		if path != "/local/b.mkv" {
			t.Fatalf("路径 = %q，期望最后一条 /local/b.mkv", path)
		}
	})
}

// TestParseItemIDInt 覆盖 ItemIdInt 的尽力解析语义。
func TestParseItemIDInt(t *testing.T) {
	cases := []struct {
		name   string
		itemID string
		want   int64
	}{
		{name: "十进制可解析", itemID: "12345", want: 12345},
		{name: "带空白可解析", itemID: "  42  ", want: 42},
		{name: "Emby 十六进制 Id 解析为 0", itemID: "a1b2c3d4e5f60718293a4b5c6d7e8f90", want: 0},
		{name: "空串为 0", itemID: "", want: 0},
		{name: "负数可解析", itemID: "-7", want: -7},
		{name: "非数字为 0", itemID: "abc", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseItemIDInt(tc.itemID); got != tc.want {
				t.Fatalf("parseItemIDInt(%q) = %d，期望 %d", tc.itemID, got, tc.want)
			}
		})
	}
}

// TestExtractSeasonNumberFromDirName 表驱动覆盖季目录判定。
func TestExtractSeasonNumberFromDirName(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		want int
	}{
		{name: "Season 01", dir: "Season 01", want: 1},
		{name: "season 2", dir: "season 2", want: 2},
		{name: "S01", dir: "S01", want: 1},
		{name: "小写 s03", dir: "s03", want: 3},
		{name: "S1 带前后缀", dir: "SO1", want: -1},
		{name: "空串", dir: "", want: -1},
		{name: "不以 s 开头", dir: "第一季", want: -1},
		{name: "剧名开头", dir: "Breaking Bad", want: -1},
		{name: "大季号", dir: "S123", want: 123},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractSeasonNumberFromDirName(tc.dir); got != tc.want {
				t.Fatalf("ExtractSeasonNumberFromDirName(%q) = %d，期望 %d", tc.dir, got, tc.want)
			}
		})
	}
}

// TestIsSafeDeleteTarget 覆盖删除目标的根保护。
func TestIsSafeDeleteTarget(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{name: "正常目录", target: "电影/影片名 (2024)", want: true},
		{name: "空串拒绝", target: "", want: false},
		{name: "点拒绝", target: ".", want: false},
		{name: "根拒绝", target: "/", want: false},
		{name: "上级目录拒绝", target: "..", want: false},
		{name: "包含上级目录段拒绝", target: "电影/../其它", want: false},
		{name: "多级正常路径", target: "剧名/Season 01", want: true},
		{name: "纯空白拒绝", target: "   ", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSafeDeleteTarget(tc.target); got != tc.want {
				t.Fatalf("isSafeDeleteTarget(%q) = %v，期望 %v", tc.target, got, tc.want)
			}
		})
	}
}

// TestIsScannedType 覆盖纳入索引的类型白名单。
func TestIsScannedType(t *testing.T) {
	cases := []struct {
		itemType string
		want     bool
	}{
		{itemType: "Movie", want: true},
		{itemType: "Video", want: true},
		{itemType: "Episode", want: true},
		{itemType: "Series", want: false},
		{itemType: "Folder", want: false},
		{itemType: "BoxSet", want: false},
		{itemType: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.itemType, func(t *testing.T) {
			if got := isScannedType(tc.itemType); got != tc.want {
				t.Fatalf("isScannedType(%q) = %v，期望 %v", tc.itemType, got, tc.want)
			}
		})
	}
}

// TestLibrarySelected 覆盖媒体库选中判定。
func TestLibrarySelected(t *testing.T) {
	cases := []struct {
		name      string
		selected  []string
		libraryID string
		want      bool
	}{
		{name: "未勾选任何库等同于全选", selected: nil, libraryID: "lib-a", want: true},
		{name: "命中已选库", selected: []string{"lib-a", "lib-b"}, libraryID: "lib-a", want: true},
		{name: "未命中已选库", selected: []string{"lib-a"}, libraryID: "lib-z", want: false},
		{name: "空库 ID 视为选中", selected: []string{"lib-a"}, libraryID: "", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := librarySelected(tc.selected, tc.libraryID); got != tc.want {
				t.Fatalf("librarySelected(%v, %q) = %v，期望 %v",
					tc.selected, tc.libraryID, got, tc.want)
			}
		})
	}
}

// TestDecodeSelectedLibraries 覆盖选中库列表解析。
func TestDecodeSelectedLibraries(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{name: "空串得到 nil", raw: "", want: 0},
		{name: "空数组得到 nil", raw: "[]", want: 0},
		{name: "null 得到 nil", raw: "null", want: 0},
		{name: "正常数组", raw: `["a","b"]`, want: 2},
		{name: "过滤空元素", raw: `["a","","b"]`, want: 2},
		{name: "非法 JSON 得到 nil", raw: "{not json", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(decodeSelectedLibraries(tc.raw)); got != tc.want {
				t.Fatalf("decodeSelectedLibraries(%q) 长度 = %d，期望 %d", tc.raw, got, tc.want)
			}
		})
	}
}

// TestBuildMediaItem 覆盖 Emby 条目到本地索引记录的投影。
func TestBuildMediaItem(t *testing.T) {
	item := embyclient.BaseItemDtoV2{
		Id:             "abc123",
		Name:           "测试影片",
		Type:           "Movie",
		Path:           "/emby/电影/测试影片.mkv",
		DateCreated:    "2026-06-29T02:00:00Z",
		DateModified:   "2026-06-29T03:00:00Z",
		IndexNumber:    3,
		ProductionYear: 2024,
		MediaSources: []embyclient.MediaSource{
			{Path: "https://cdn.example.com/a.mkv?pickcode=pick-1"},
		},
	}
	got := buildMediaItem(item, "lib-a", "full-100", 500)
	if got.ItemID != "abc123" {
		t.Fatalf("ItemID = %q", got.ItemID)
	}
	if got.PickCode != "pick-1" {
		t.Fatalf("PickCode = %q，期望 pick-1", got.PickCode)
	}
	if got.LibraryID != "lib-a" {
		t.Fatalf("LibraryID = %q", got.LibraryID)
	}
	if got.LastSeenSyncRun != "full-100" || got.LastSeenAt != 500 {
		t.Fatalf("批次字段 = %q / %d", got.LastSeenSyncRun, got.LastSeenAt)
	}
	if got.DateCreatedTime != 1782698400 {
		t.Fatalf("DateCreatedTime = %d，期望 1782698400", got.DateCreatedTime)
	}
	if got.DateModifiedTime != 1782702000 {
		t.Fatalf("DateModifiedTime = %d，期望 1782702000", got.DateModifiedTime)
	}
	if got.Path != "/emby/电影/测试影片.mkv" {
		t.Fatalf("Path = %q", got.Path)
	}
}

// TestBuildMediaItemFallsBackToMediaSourcePath 覆盖 Path 缺失时回落到媒体源路径。
func TestBuildMediaItemFallsBackToMediaSourcePath(t *testing.T) {
	item := embyclient.BaseItemDtoV2{
		Id:   "abc124",
		Type: "Movie",
		MediaSources: []embyclient.MediaSource{
			{Path: "https://cdn.example.com/b.mkv?pickcode=pick-2"},
		},
	}
	got := buildMediaItem(item, "", "", 0)
	if got.Path != "https://cdn.example.com/b.mkv?pickcode=pick-2" {
		t.Fatalf("Path = %q，期望回落到媒体源路径", got.Path)
	}
	if got.PickCode != "pick-2" {
		t.Fatalf("PickCode = %q", got.PickCode)
	}
}

// TestBaseNameWithoutExt 覆盖元数据同名匹配用的去扩展名。
func TestBaseNameWithoutExt(t *testing.T) {
	cases := []struct {
		name     string
		fileName string
		want     string
	}{
		{name: "普通扩展名", fileName: "影片.mkv", want: "影片"},
		{name: "多段扩展名只去最后一段", fileName: "影片.web.mkv", want: "影片.web"},
		{name: "无扩展名原样返回", fileName: "影片", want: "影片"},
		{name: "隐藏文件不去扩展名", fileName: ".nfo", want: ".nfo"},
		{name: "空串", fileName: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := baseNameWithoutExt(tc.fileName); got != tc.want {
				t.Fatalf("baseNameWithoutExt(%q) = %q，期望 %q", tc.fileName, got, tc.want)
			}
		})
	}
}
