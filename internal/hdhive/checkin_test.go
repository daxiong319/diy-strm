package hdhive

import (
	"encoding/json"
	"testing"
)

// TestFindCheckinAwardPoints 验证借鉴 NanShare 的递归积分解析策略
func TestFindCheckinAwardPoints(t *testing.T) {
	cases := []struct {
		name string
		json string
		want *int
	}{
		{"专用字段 earned_points", `{"success":true,"data":{"earned_points":5}}`, intPtr(5)},
		{"专用字段 reward_points 优先于 points", `{"points":999,"reward_points":8}`, intPtr(8)},
		{"嵌套 data.result", `{"data":{"result":{"gain_points":3}}}`, intPtr(3)},
		{"通用 points 兜底", `{"message":"ok","data":{"points":7}}`, intPtr(7)},
		{"账号资料上下文不误判", `{"user":{"nickname":"a","level":"vip","points":500}}`, nil},
		{"跳过 user 键取外层奖励", `{"user":{"points":500},"checkin_points":2}`, intPtr(2)},
		{"负数积分（赌狗）", `{"delta_points":-3}`, intPtr(-3)},
		{"字符串数字", `{"sign_points":"12"}`, intPtr(12)},
		{"数组内查找", `{"items":[{"award_points":4}]}`, intPtr(4)},
		{"布尔与浮点忽略", `{"ok":true,"x":1.5}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(tc.json), &v); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := findCheckinAwardPoints(v)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("want nil, got %d", *got)
				}
				return
			}
			if got == nil || *got != *tc.want {
				t.Fatalf("want %v, got %v", *tc.want, got)
			}
		})
	}
}

func TestExtractCheckinStats(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"earned_points":5,"total_points":123,"continuous_days":4}`), &v)
	stats := ExtractCheckinStats(v)
	if stats.AwardPoints == nil || *stats.AwardPoints != 5 {
		t.Fatalf("award = %v", stats.AwardPoints)
	}
	if stats.BalancePoints == nil || *stats.BalancePoints != 123 {
		t.Fatalf("balance = %v", stats.BalancePoints)
	}
	if stats.StreakDays == nil || *stats.StreakDays != 4 {
		t.Fatalf("streak = %v", stats.StreakDays)
	}
	suffix := stats.FormatStatsSuffix()
	want := "，获得 5 积分，余额 123，已连签 4 天"
	if suffix != want {
		t.Fatalf("suffix = %q, want %q", suffix, want)
	}
}

func TestRandomCheckinMinute(t *testing.T) {
	// 同一账号同一天稳定
	a1 := RandomCheckinMinute(42, "2026-08-24")
	a2 := RandomCheckinMinute(42, "2026-08-24")
	if a1 != a2 {
		t.Fatalf("same day should be stable: %d vs %d", a1, a2)
	}
	// 不同日期大概率不同
	diff := false
	for d := 1; d <= 10; d++ {
		if RandomCheckinMinute(42, "2026-09-01") != RandomCheckinMinute(42, "2026-09-0"+string(rune('0'+d%10))) {
			diff = true
			break
		}
	}
	if !diff {
		// 换个方式再验证不同账号分布
		seen := map[int]bool{}
		for id := uint(1); id <= 50; id++ {
			seen[RandomCheckinMinute(id, "2026-08-24")] = true
		}
		if len(seen) < 10 {
			t.Fatalf("distribution too narrow: %d distinct minutes across 50 ids", len(seen))
		}
	}
	// 范围 0-29
	for id := uint(1); id <= 200; id++ {
		if m := RandomCheckinMinute(id, "2026-08-24"); m < 0 || m > 29 {
			t.Fatalf("minute out of range: %d", m)
		}
	}
}

func TestIsAlreadyCheckedExtended(t *testing.T) {
	for _, msg := range []string{"已经签到过啦", "您今天已签到", "签到过了哦", "明天再来吧", "already checked in"} {
		if !IsAlreadyChecked(msg) {
			t.Errorf("IsAlreadyChecked(%q) = false, want true", msg)
		}
	}
	if IsAlreadyChecked("签到成功") {
		t.Error("签到成功 should not be treated as already checked")
	}
}

func intPtr(n int) *int { return &n }
