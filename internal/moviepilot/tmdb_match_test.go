package moviepilot

import "testing"

func TestNormalizeForMatch(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"遮 天", "遮天"},
		{"遮.天", "遮天"},
		{"The Last House", "thelasthouse"},
		{"2-一斩苍穹", "2一斩苍穹"}, // 序号是调用方剥离，归一化保留
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeForMatch(tc.in); got != tc.want {
			t.Fatalf("normalizeForMatch(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestIsOrderedSubsequence(t *testing.T) {
	if !isOrderedSubsequence("遮天", "遮 天") {
		t.Fatal("「遮天」应为「遮 天」的有序子序列")
	}
	if !isOrderedSubsequence("遮天", "遮.天") {
		t.Fatal("「遮天」应为「遮.天」的有序子序列")
	}
	if isOrderedSubsequence("天遮", "遮天") {
		t.Fatal("「天遮」不是「遮天」的有序子序列")
	}
}

func TestTitleMatchScore(t *testing.T) {
	cases := []struct {
		query, cand string
		want        int
		min         bool // want 为下限断言
	}{
		{"遮天", "遮天", titleScoreExact, false},
		{"遮 天", "遮天", titleScoreExact, false},    // 归一化去分隔符后相等
		{"The Last House", "The Last House (2026)", titleScoreContains, false},
		{" unrelated", "另一个名字", 0, true}, // 完全无关 → 低分
	}
	for _, tc := range cases {
		got := titleMatchScore(tc.query, tc.cand)
		if tc.min {
			if got < tc.want {
				t.Fatalf("titleMatchScore(%q,%q) = %d，应 ≥ %d", tc.query, tc.cand, got, tc.want)
			}
		} else if got != tc.want {
			t.Fatalf("titleMatchScore(%q,%q) = %d，期望 %d", tc.query, tc.cand, got, tc.want)
		}
	}
}

func TestYearMatchScore(t *testing.T) {
	// 季画像精确命中
	if s, mismatch := yearMatchScore(2026, 2023, []int{2023, 2025, 2026}, true); s != yearBucketExact || mismatch {
		t.Fatalf("季画像精确命中应得 %d/否，实际 %d/%v", yearBucketExact, s, mismatch)
	}
	// 季画像明确不符 → 拒绝
	if s, mismatch := yearMatchScore(2030, 2023, []int{2023, 2025}, true); s != yearBucketMiss || !mismatch {
		t.Fatalf("季画像不符应拒绝，实际 %d/%v", s, mismatch)
	}
	// 相近 ±1
	if s, mismatch := yearMatchScore(2024, 2023, nil, false); s != yearBucketNear || mismatch {
		t.Fatalf("±1 相近应得 %d/否，实际 %d/%v", yearBucketNear, s, mismatch)
	}
	// 无年份信息
	if s, mismatch := yearMatchScore(0, 0, nil, false); s != yearBucketUnknown || mismatch {
		t.Fatalf("无年份应得 %d/否，实际 %d/%v", yearBucketUnknown, s, mismatch)
	}
}

// 关键场景断言（对应本次「2-遮.天 年番4」识别失败的回归）
func TestTitleMatchScoreFanScenario(t *testing.T) {
	// 目录归一化后标题「遮 天」与 TMDB 正名「遮天」：归一化去空格后相等 → 满分
	if got := titleMatchScore("遮 天", "遮天"); got != titleScoreExact {
		t.Fatalf("「遮 天」vs「遮天」应满分（归一化相等），实际 %d", got)
	}
}

func TestYearDistance(t *testing.T) {
	if d := yearDistance(2023, 2026); d != 3 {
		t.Fatalf("yearDistance(2023,2026) = %d，期望 3", d)
	}
	if d := yearDistance(2027, 2026); d != 1 {
		t.Fatalf("yearDistance(2027,2026) = %d，期望 1", d)
	}
	if d := yearDistance(0, 2026); d != 1<<30 {
		t.Fatalf("无年份候选应视为最远，实际 %d", d)
	}
}

// 用户规则「年份用作辅助识别」：多个候选标题同分且年份档相同时，年份就近者胜（不做硬过滤）
func TestSortCandidatesByScoreYearTieBreak(t *testing.T) {
	final := []scoredCandidate{
		{cand: tmdbCandidate{ID: 100, Name: "同名旧剧", Year: 2018}, score: titleScoreExact},
		{cand: tmdbCandidate{ID: 224839, Name: "遮天", Year: 2023}, score: titleScoreExact},
	}
	sortCandidatesByScore(final, 2026)
	if final[0].cand.ID != 224839 {
		t.Fatalf("年份就近决胜失败：首选 %d，期望 224839（2023 距 2026 更近）", final[0].cand.ID)
	}
	// 无目标年份：回退 ID 稳定序
	final2 := []scoredCandidate{
		{cand: tmdbCandidate{ID: 500, Year: 2018}, score: titleScoreExact},
		{cand: tmdbCandidate{ID: 200, Year: 2023}, score: titleScoreExact},
	}
	sortCandidatesByScore(final2, 0)
	if final2[0].cand.ID != 200 {
		t.Fatalf("无目标年份应按 ID 稳定排序：首选 %d，期望 200", final2[0].cand.ID)
	}
	// 分差优先于年份距离（高分类别仍胜出）
	final3 := []scoredCandidate{
		{cand: tmdbCandidate{ID: 1, Name: "近年份低标题分", Year: 2026}, score: titleScoreContains},
		{cand: tmdbCandidate{ID: 2, Name: "远年份满分", Year: 1990}, score: titleScoreExact},
	}
	sortCandidatesByScore(final3, 2026)
	if final3[0].cand.ID != 2 {
		t.Fatalf("标题满分应优先于年份距离：首选 %d，期望 2", final3[0].cand.ID)
	}
}
