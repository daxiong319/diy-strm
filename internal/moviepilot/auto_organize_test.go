package moviepilot

import (
	"errors"
	"testing"

	"diy-strm/internal/mediaparse"
)

func TestExtractQualityTags(t *testing.T) {
	cases := []struct {
		fileName string
		title    string
		want     string
	}{
		{"花开锦绣.S01E01.第1集.2160p.WEB-DL.H.265.60fps-Ocat.mp4", "花开锦绣", "2160p.WEB-DL.H.265.60fps-Ocat"},
		{"花开锦绣.S01E01.第1集.mp4", "花开锦绣", ""},
		{"Some.Movie.2024.2160p.WEB-DL.DDP5.1.mkv", "Some Movie", "2160p.WEB-DL.DDP5.1"},
		{"The.Last.Of.Us.S01E01.1080p.WEB-DL.x264.mkv", "The Last Of Us", "1080p.WEB-DL.x264"},
		{"第1集.1080p.mp4", "花开锦绣", "1080p"},
		{"浩哥爱情故事.2026.S01E01.第1集.2160p.mkv", "浩哥爱情故事", "2160p"},
		{"S01E02.2160p.WEB-DL.mkv", "", "2160p.WEB-DL"},
	}
	for _, c := range cases {
		got := extractQualityTags(c.fileName, c.title)
		if got != c.want {
			t.Errorf("extractQualityTags(%q, %q) = %q, want %q", c.fileName, c.title, got, c.want)
		}
	}
}

func TestBuildAutoOrganizeNewName(t *testing.T) {
	cases := []struct {
		category string
		title    string
		season   int
		episode  int
		year     int
		orig     string
		want     string
	}{
		// 用户需求原文例子
		{"tv", "花开锦绣", 1, 1, 2026, "花开锦绣.S01E01.第1集.2160p.WEB-DL.H.265.60fps-Ocat.mp4",
			"花开锦绣.2026.S01E01.第1集.2160p.WEB-DL.H.265.60fps-Ocat.mp4"},
		// 电影保留质量标签
		{"movie", "拯救大兵瑞恩", 0, 0, 2024, "拯救大兵瑞恩.2024.2160p.WEB-DL.H.265.mkv",
			"拯救大兵瑞恩 (2024).2160p.WEB-DL.H.265.mkv"},
		// 无质量标签
		{"tv", "花开锦绣", 2, 5, 2026, "花开锦绣.S02E05.mp4",
			"花开锦绣.2026.S02E05.第5集.mp4"},
		// 无年份
		{"tv", "某某剧", 1, 3, 0, "某某剧.第3集.mkv",
			"某某剧.S01E03.第3集.mkv"},
		// 中文集号+质量
		{"tv", "繁花", 1, 1, 2024, "繁花.第1集.4K.WEB-DL.mkv",
			"繁花.2024.S01E01.第1集.4K.WEB-DL.mkv"},
	}
	for _, c := range cases {
		got := buildAutoOrganizeNewName(c.category, c.title, c.season, c.episode, c.year, c.orig)
		if got != c.want {
			t.Errorf("buildAutoOrganizeNewName(%q,%q,%d,%d,%d,%q) = %q, want %q",
				c.category, c.title, c.season, c.episode, c.year, c.orig, got, c.want)
		}
	}
}

func TestBuildAutoMedia(t *testing.T) {
	// 剧集：目录级标题 + 文件级集号
	dirCtx := &autoDirMedia{Category: "tv", Title: "花开锦绣", Season: 1, Year: 2026}
	media, err := buildAutoMedia("第3集.1080p.mp4", dirCtx)
	if err != nil {
		t.Fatalf("buildAutoMedia err: %v", err)
	}
	if media.Category != "tv" || media.Title != "花开锦绣" || media.Season != 1 || media.Episode != 3 || media.Year != 2026 {
		t.Errorf("unexpected media: %+v", media)
	}

	// 目录级为 movie 的整季分享树（目录名是剧名但无季集、文件无季集）→ 应判为剧集
	dirCtx2 := &autoDirMedia{Category: "unknown", Title: "花开锦绣", Season: 0, Year: 2026}
	media2, err := buildAutoMedia("第1集.mkv", dirCtx2)
	if err != nil {
		t.Fatalf("buildAutoMedia err: %v", err)
	}
	if media2.Category != "tv" || media2.Episode != 1 || media2.Season != 1 {
		t.Errorf("unexpected media: %+v", media2)
	}

	// 单文件电影：标题.年份.质量
	media3, err := buildAutoMedia("Some.Movie.2024.2160p.WEB-DL.mkv", nil)
	if err != nil {
		t.Fatalf("buildAutoMedia err: %v", err)
	}
	if media3.Category != "movie" || media3.Title != "Some Movie" || media3.Year != 2024 {
		t.Errorf("unexpected media: %+v", media3)
	}

	// 电影目录（仅年份）内多文件：判定为电影
	dirCtx3 := &autoDirMedia{Category: "movie", Title: "拯救大兵瑞恩", Year: 2024}
	media4, err := buildAutoMedia("Saving.Private.Ryan.1998.mkv", dirCtx3)
	if err != nil {
		t.Fatalf("buildAutoMedia err: %v", err)
	}
	if media4.Category != "movie" {
		t.Errorf("unexpected media: %+v", media4)
	}

	// 电视类无集号：返回 errMediaUnrecognized（上层转 AI 或失败目录）
	if _, err := buildAutoMedia("某剧.1080p.mkv", nil); err == nil || !errors.Is(err, errMediaUnrecognized) {
		t.Errorf("want errMediaUnrecognized, got %v", err)
	}
}

func TestBuildOrganizeRelDirAutoFormat(t *testing.T) {
	// 用户需求原文：目录 花开锦绣 (2026) {tmdb=287496}/Season 01
	rel, ok := buildOrganizeRelDir("tv", "花开锦绣", 2026, 1, 287496, "国产剧集")
	if !ok {
		t.Fatal("buildOrganizeRelDir failed")
	}
	if rel != "国产剧集/花开锦绣 (2026) {tmdb=287496}/Season 01" {
		t.Errorf("rel = %q", rel)
	}
}

func TestBuildAutoMediaSeasonFromDir(t *testing.T) {
	// Season 目录级信息传入后，无集号文件归入正确季
	dirCtx := &autoDirMedia{Category: "tv", Title: "某某剧", Season: 2, Year: 2025}
	media, err := buildAutoMedia("第5集.mkv", dirCtx)
	if err != nil {
		t.Fatalf("buildAutoMedia err: %v", err)
	}
	if media.Season != 2 || media.Episode != 5 {
		t.Errorf("unexpected media: %+v", media)
	}
}

func TestParseEpisodeCompatible(t *testing.T) {
	// 确保 mediaparse 对本功能依赖的集号解析路径可用
	ep, ok := mediaparse.ParseEpisode("第3集.1080p.mp4")
	if !ok || ep.Episode != 3 {
		t.Errorf("ParseEpisode = %+v, %v", ep, ok)
	}
	ep2, ok := mediaparse.ParseEpisode("花开锦绣.S01E01.第1集.2160p.mkv")
	if !ok || ep2.Season != 1 || ep2.Episode != 1 || ep2.Title != "花开锦绣" {
		t.Errorf("ParseEpisode = %+v, %v", ep2, ok)
	}
}