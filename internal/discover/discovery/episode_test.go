package discovery

import (
	"reflect"
	"testing"
)

func TestParseEpisodeKeys(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		fallback int
		want     []string
	}{
		{
			name:     "带季号标准格式",
			text:     "S01 E13",
			fallback: 1,
			want:     []string{"S01E13"},
		},
		{
			name:     "带季号无 fallback",
			text:     "S01 E13",
			fallback: 0,
			want:     []string{"S01E13"},
		},
		{
			// 区间展开必须出 5 个键（S01E24..S01E28）
			name:     "区间展开",
			text:     "S01E24-E28",
			fallback: 1,
			want:     []string{"S01E24", "S01E25", "S01E26", "S01E27", "S01E28"},
		},
		{
			// 跨季撞键回归用例：帖内显式中文季号优先于 fallbackSeason
			name:     "中文季号优先于 fallback",
			text:     "第2季第5集",
			fallback: 1,
			want:     []string{"S02E05"},
		},
		{
			name:     "中文集号加 fallback 季",
			text:     "第10集",
			fallback: 1,
			want:     []string{"S01E10"},
		},
		{
			// 季号未知时发裸 E%02d 键保持历史兼容
			name:     "无季号无 fallback 发裸键",
			text:     "第10集",
			fallback: 0,
			want:     []string{"E10"},
		},
		{
			name:     "裸 Eyy 无 fallback",
			text:     "E10",
			fallback: 0,
			want:     []string{"E10"},
		},
		{
			name:     "裸 Eyy 加 fallback 季",
			text:     "E10",
			fallback: 1,
			want:     []string{"S01E10"},
		},
		{
			// e2-e1 > 300 时退化单集
			name:     "区间跨度过大退化为单集",
			text:     "S01E24-E400",
			fallback: 1,
			want:     []string{"S01E24"},
		},
		{
			// 去重：同帖重复出现同一集只出一个键
			name:     "重复集去重",
			text:     "S01E05 S01E05 S01E06",
			fallback: 1,
			want:     []string{"S01E05", "S01E06"},
		},
		{
			// 区间展开与单集重复也应去重
			name:     "区间展开与重复单集去重",
			text:     "S01E24-E28 S01E24",
			fallback: 1,
			want:     []string{"S01E24", "S01E25", "S01E26", "S01E27", "S01E28"},
		},
		{
			name:     "空文本返回空",
			text:     "",
			fallback: 1,
			want:     []string{},
		},
		{
			name:     "无集号文本返回空",
			text:     "这是一部 2160p 的电影，没有剧集号",
			fallback: 1,
			want:     []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseEpisodeKeys(tt.text, tt.fallback)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseEpisodeKeys(%q, %d) = %#v, want %#v", tt.text, tt.fallback, got, tt.want)
			}
		})
	}
}

// TestParseEpisodeKeysStableOrder 验证 keysOf 排序后输出稳定（同一输入多次调用结果一致）。
func TestParseEpisodeKeysStableOrder(t *testing.T) {
	text := "S01E07 S01E03 S01E11 S01E01"
	first := ParseEpisodeKeys(text, 1)
	want := []string{"S01E01", "S01E03", "S01E07", "S01E11"}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("got %#v, want sorted %#v", first, want)
	}
	for i := 0; i < 20; i++ {
		got := ParseEpisodeKeys(text, 1)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: got %#v, want stable %#v", i, got, want)
		}
	}
}

func TestJoinEpisodeKeys(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want string
	}{
		{"正常连接", []string{"S01E01", "S01E02"}, "S01E01,S01E02"},
		{"单个", []string{"S01E13"}, "S01E13"},
		{"空切片", []string{}, ""},
		{"nil 切片", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := JoinEpisodeKeys(tt.keys); got != tt.want {
				t.Fatalf("JoinEpisodeKeys(%#v) = %q, want %q", tt.keys, got, tt.want)
			}
		})
	}
}
