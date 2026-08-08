package controllers

import "testing"

func TestParseNameAlignEpisode(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		wantOK   bool
		want     nameAlignParsed
	}{
		{"SxxExx 标准格式", "Breaking.Bad.S01E01.1080p.mkv", true, nameAlignParsed{Title: "Breaking Bad", Season: 1, Episode: 1}},
		{"SxxExx 小写", "game.of.thrones s08e03.mkv", true, nameAlignParsed{Title: "game of thrones", Season: 8, Episode: 3}},
		{"SxxExx 无补零", "friends.s1e10.mkv", true, nameAlignParsed{Title: "friends", Season: 1, Episode: 10}},
		{"NxN 格式", "Fringe.2x15.mp4", true, nameAlignParsed{Title: "Fringe", Season: 2, Episode: 15}},
		{"EP 格式", "THE.SIMPSONS.EP21.mp4", true, nameAlignParsed{Title: "THE SIMPSONS", Episode: 21}},
		{"EP 带点", "house md ep.05.avi", true, nameAlignParsed{Title: "house md", Episode: 5}},
		{"中文集数", "名侦探柯南第12集.mkv", true, nameAlignParsed{Title: "名侦探柯南", Episode: 12}},
		{"中文话数", "海贼王 第3话.mp4", true, nameAlignParsed{Title: "海贼王", Episode: 3}},
		{"标题含年份", "Rick.and.Morty.2023.S07E01.mkv", true, nameAlignParsed{Title: "Rick and Morty 2023", Season: 7, Episode: 1}},
		{"方括号季集", "MyShow [S02E04].mkv", true, nameAlignParsed{Title: "MyShow", Season: 2, Episode: 4}},
		{"无法解析", "random_movie_2024.mkv", false, nameAlignParsed{}},
		{"无法解析-纯标题", "纪录片合集.mp4", false, nameAlignParsed{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseNameAlignEpisode(tt.fileName)
			if ok != tt.wantOK {
				t.Fatalf("parseNameAlignEpisode(%q) ok = %v, want %v", tt.fileName, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Title != tt.want.Title || got.Season != tt.want.Season || got.Episode != tt.want.Episode {
				t.Fatalf("parseNameAlignEpisode(%q) = %+v, want %+v", tt.fileName, got, tt.want)
			}
		})
	}
}

func TestBuildNameAlignNewName(t *testing.T) {
	tests := []struct {
		name       string
		parsed     nameAlignParsed
		mediaTitle string
		mediaType  string
		year       int
		ext        string
		want       string
		wantOK     bool
	}{
		{"剧集-使用解析标题", nameAlignParsed{Title: "Breaking Bad", Season: 1, Episode: 1}, "", "tvshow", 0, ".mkv", "Breaking Bad S01E01.mkv", true},
		{"剧集-使用指定标题", nameAlignParsed{Title: "bad", Season: 2, Episode: 3}, "Better Call Saul", "tvshow", 0, ".mkv", "Better Call Saul S02E03.mkv", true},
		{"剧集-无季默认第一季", nameAlignParsed{Title: "The Simpsons", Episode: 21}, "", "tvshow", 0, ".mp4", "The Simpsons S01E21.mp4", true},
		{"剧集-无集数失败", nameAlignParsed{Title: "The Simpsons"}, "", "tvshow", 0, ".mp4", "", false},
		{"剧集-无标题失败", nameAlignParsed{Title: "", Season: 1, Episode: 1}, "", "tvshow", 0, ".mkv", "", false},
		{"电影-带年份", nameAlignParsed{Title: "Inception"}, "", "movie", 2010, ".mkv", "Inception (2010).mkv", true},
		{"电影-无年份", nameAlignParsed{Title: "Inception"}, "盗梦空间", "movie", 0, ".mkv", "盗梦空间.mkv", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := buildNameAlignNewName(tt.parsed, tt.mediaTitle, tt.mediaType, tt.year, tt.ext)
			if ok != tt.wantOK {
				t.Fatalf("buildNameAlignNewName(%+v) ok = %v, want %v", tt.parsed, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Fatalf("buildNameAlignNewName(%+v) = %q, want %q", tt.parsed, got, tt.want)
			}
		})
	}
}
