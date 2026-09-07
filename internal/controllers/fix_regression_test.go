package controllers

import (
	"sort"
	"testing"
)

// 回归：三批修复 —— 季号碰撞（ParseEpisodeKeys 中文季号入键）。
// 背景：S02 资源帖只写「第2季第5集」时，原实现把集号归到 fallbackSeason（未知时发裸 E 键），
// S02E05 与 S01E05 撞键，S02 资源被误判"已收录/已达标"。

func TestParseEpisodeKeysCnSeasonOverridesFallback(t *testing.T) {
	// 全季订阅（fallbackSeason=0）：帖内中文季号必须入键
	got := ParseEpisodeKeys("第2季第5集 4K", 0)
	want := "S02E05"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("第2季第5集 (fallback=0) = %v, want [%s]", got, want)
	}
}

func TestParseEpisodeKeysCnSeasonBeatsWrongFallback(t *testing.T) {
	// fallbackSeason=1 但帖内显式「第2季」：中文季号优先，防 S02 撞 S01
	got := ParseEpisodeKeys("第2季 第10集 更新", 1)
	if len(got) != 1 || got[0] != "S02E10" {
		t.Fatalf("第2季第10集 (fallback=1) = %v, want [S02E10]（中文季号应优先于订阅季）", got)
	}
}

func TestParseEpisodeKeysNoSeasonStillBareKey(t *testing.T) {
	// 无任何季号（历史兼容）：仍发裸 E 键
	got := ParseEpisodeKeys("第10集 1080p", 0)
	if len(got) != 1 || got[0] != "E10" {
		t.Fatalf("第10集 (fallback=0) = %v, want [E10]", got)
	}
	// 订阅指定单季时归属该季（原行为保持）
	got = ParseEpisodeKeys("更新第10集", 3)
	if len(got) != 1 || got[0] != "S03E10" {
		t.Fatalf("第10集 (fallback=3) = %v, want [S03E10]", got)
	}
}

func TestParseEpisodeKeysSxxEyyUnaffected(t *testing.T) {
	// SxxEyy 与区间展开不受本次改动影响（回归保护）。
	// keysOf 从 map 收集返回、顺序不确定（调用方均按集合语义使用），排序后断言集合内容
	got := ParseEpisodeKeys("S01 E24-E26 4K", 2)
	if len(got) != 3 {
		t.Fatalf("S01 E24-E26 应展开 3 集，实际 %v", got)
	}
	sort.Strings(got)
	for i, want := range []string{"S01E24", "S01E25", "S01E26"} {
		if got[i] != want {
			t.Fatalf("区间展开[%d] = %s, want %s（全集=%v）", i, got[i], want, got)
		}
	}
}

func TestParseEpisodeKeysCnSeasonOutOfRange(t *testing.T) {
	// 非法季号（0/超两位）不采纳，回落 fallback
	got := ParseEpisodeKeys("第0季第5集", 2)
	if len(got) != 1 || got[0] != "S02E05" {
		t.Fatalf("第0季第5集 (fallback=2) = %v, want [S02E05]", got)
	}
	if got := ParseEpisodeKeys("没有集号", 0); len(got) != 0 {
		t.Fatalf("无集号文本不应产生键，实际 %v", got)
	}
}

// 回归：三批修复 —— washTargetReached 统一守卫（空目标=可继续升级、非法值告警不静默失效）。
func TestWashTargetReached(t *testing.T) {
	cases := []struct {
		name     string
		target   string
		oldScore int
		want     bool
	}{
		{"空目标=未达标可升级", "", 99999, false},
		{"空目标+零分", "", 0, false},
		{"非法值不静默失效", "8k_ultra", 0, false},
		{"非法值+高分也不算达标", "4kk", 99999, false},
		{"1080p 目标+1080p 旧版达标", "1080p", 2*10000 + 3*1000 + 2*100 + 1, true},
		{"1080p 目标+720p 旧版未达标", "1080p", 1*10000 + 999, false},
		{"4k 目标+1080p 旧版未达标", "4k", 2*10000 + 9999, false},
		{"大小写与空白归一", "  4K  ", 3*10000 + 5*1000 + 9999, true},
		{"4k_remux 达标", "4k_remux", 3*10000 + 5*1000, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := washTargetReached(c.target, c.oldScore); got != c.want {
				t.Fatalf("washTargetReached(%q, %d) = %v, want %v", c.target, c.oldScore, got, c.want)
			}
		})
	}
}

// 回归：P0-3 一扫多配 —— 批量游标取各订阅最小新游标的关键不变量（postIDGreater 语义）。
func TestPostIDGreaterForBatchCursor(t *testing.T) {
	// 长度不同：更长更新（两位帖 ID 时代 vs 一位）
	if !postIDGreater("100", "99") {
		t.Fatal("100 应新于 99")
	}
	// 等长字典序
	if !postIDGreater("17260", "17259") {
		t.Fatal("17260 应新于 17259")
	}
	if postIDGreater("17259", "17260") {
		t.Fatal("17259 不应新于 17260")
	}
	// 批量游标场景：订阅 A 推进到 17260、订阅 B 失败回退到 17250，
	// 最小新游标=17250（B 的失败帖下轮重扫）；反向断言防止取最大值
	cursor := ""
	for _, id := range []string{"17260", "17250", "17255"} {
		if cursor == "" || postIDGreater(cursor, id) {
			cursor = id
		}
	}
	if cursor != "17250" {
		t.Fatalf("批量游标应取最小新游标 17250，实际 %s", cursor)
	}
}
