package controllers

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseEpisodeKeys(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		season int
		want   []string
	}{
		{name: "单集 S 前缀", text: "🎬 花开锦绣 (2026) 已更新 S01 E13 4K", season: 0, want: []string{"S01E13"}},
		{name: "区间展开", text: "S01 E24-E28 4K", season: 0, want: []string{"S01E24", "S01E25", "S01E26", "S01E27", "S01E28"}},
		{name: "区间带 E", text: "S02 E01 - E03", season: 0, want: []string{"S02E01", "S02E02", "S02E03"}},
		{name: "紧凑写法", text: "S01E13 4K", season: 0, want: []string{"S01E13"}},
		{name: "S 优先于 fallback", text: "S01 E13 4K", season: 2, want: []string{"S01E13"}},
		{name: "裸 E 用 fallback 季", text: "E13 4K", season: 2, want: []string{"S02E13"}},
		{name: "裸 E 无季", text: "E13 4K", season: 0, want: []string{"E13"}},
		{name: "中文第 N 集", text: "第10集 4K", season: 1, want: []string{"S01E10"}},
		{name: "中文第 N 集无季", text: "第10集 4K", season: 0, want: []string{"E10"}},
		{name: "无剧集", text: "花开锦绣 全季", season: 0, want: nil},
		{name: "WEB-DL 不误匹配", text: "S01 E13 WEB-DL H.265", season: 0, want: []string{"S01E13"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseEpisodeKeys(c.text, c.season)
			sort.Strings(got)
			want := append([]string{}, c.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ParseEpisodeKeys(%q, %d) = %v, want %v", c.text, c.season, got, c.want)
			}
		})
	}
}

func TestJoinEpisodeKeys(t *testing.T) {
	if got := JoinEpisodeKeys([]string{"S01E13", "S01E14"}); got != "S01E13,S01E14" {
		t.Errorf("JoinEpisodeKeys = %q", got)
	}
	if got := JoinEpisodeKeys(nil); got != "" {
		t.Errorf("JoinEpisodeKeys(nil) = %q", got)
	}
}