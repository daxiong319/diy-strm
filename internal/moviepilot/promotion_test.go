package moviepilot

import (
	"testing"

	"litepan/internal/domain"
)

func TestNormalizePromotionOrder(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty falls back to default", "", domain.DefaultPromotionOrder},
		{"whitespace only", "   ", domain.DefaultPromotionOrder},
		{"trims and lowercases", " FREE , 2XFree , Normal ", "free,2xfree,normal"},
		{"drops unknown values", "free,bogus,normal", "free,normal"},
		{"dedupes keeping first", "free,normal,free", "free,normal"},
		{"all unknown falls back", "bogus,nope", domain.DefaultPromotionOrder},
		{"ignores empty segments", "free,,normal,", "free,normal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizePromotionOrder(tc.in); got != tc.want {
				t.Fatalf("NormalizePromotionOrder(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPromotionOrderList(t *testing.T) {
	got := PromotionOrderList("free,normal")
	if len(got) != 2 || got[0] != "free" || got[1] != "normal" {
		t.Fatalf("unexpected list: %#v", got)
	}
	if def := PromotionOrderList(""); len(def) != 5 {
		t.Fatalf("expected default of 5 tiers, got %d", len(def))
	}
}

func TestPromotionIncludeRegexSingleState(t *testing.T) {
	cases := map[string]string{
		"free":   `(?<![Xx])免费$`,
		"2xfree": `2X免费$`,
		"normal": `普通$`,
		"half":   `(?<!X )50%$`,
		"2xhalf": `2X 50%$`,
		"FREE":   `(?<![Xx])免费$`,
		"bogus":  "",
	}
	for in, want := range cases {
		if got := PromotionIncludeRegex(in); got != want {
			t.Fatalf("PromotionIncludeRegex(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPromotionTierIncludeRegex(t *testing.T) {
	order := PromotionOrderList(domain.DefaultPromotionOrder)
	cases := []struct {
		tier int
		want string
	}{
		{0, `(?<![Xx])免费$`},
		{1, `(?<![Xx])免费$|2X免费$`},
		{2, `(?<![Xx])免费$|2X免费$|普通$`},
		{4, `(?<![Xx])免费$|2X免费$|普通$|(?<!X )50%$|2X 50%$`},
	}
	for _, tc := range cases {
		if got := PromotionTierIncludeRegex(order, tc.tier); got != tc.want {
			t.Fatalf("tier %d: got %q want %q", tc.tier, got, tc.want)
		}
	}
	// 越界返回空串
	if got := PromotionTierIncludeRegex(order, -1); got != "" {
		t.Fatalf("tier -1 should be empty, got %q", got)
	}
	if got := PromotionTierIncludeRegex(order, len(order)); got != "" {
		t.Fatalf("tier out of range should be empty, got %q", got)
	}
	if got := PromotionTierIncludeRegex(nil, 0); got != "" {
		t.Fatalf("empty order should be empty, got %q", got)
	}
}

func TestAdvancePromotionLadderResetOnNewDownload(t *testing.T) {
	order := PromotionOrderList(domain.DefaultPromotionOrder)
	// 当前处于第 2 层，且从第 2 层开始后有新下载 → 重置回最高优先层
	d := AdvancePromotionLadder(order, 2, 1000, 5000, 3000, 12*3600)
	if !d.Reset || d.Tier != 0 || d.TierStartedAt != 5000 || !d.Changed {
		t.Fatalf("expected reset to tier 0, got %+v", d)
	}
	if d.Relaxed {
		t.Fatalf("reset must not also relax: %+v", d)
	}
}

func TestAdvancePromotionLadderNoResetAtTopTier(t *testing.T) {
	order := PromotionOrderList(domain.DefaultPromotionOrder)
	// 已在最高层：新下载不应触发重置
	d := AdvancePromotionLadder(order, 0, 1000, 5000, 3000, 12*3600)
	if d.Reset || d.Changed || d.Tier != 0 || d.TierStartedAt != 1000 {
		t.Fatalf("expected no change at top tier, got %+v", d)
	}
}

func TestAdvancePromotionLadderRelaxAfterPatience(t *testing.T) {
	order := PromotionOrderList(domain.DefaultPromotionOrder)
	// 第 0 层已等待超过 12 小时 → 放宽到第 1 层
	patience := int64(12 * 3600)
	d := AdvancePromotionLadder(order, 0, 1000, 1000+patience+1, 0, patience)
	if !d.Relaxed || d.Tier != 1 || d.TierStartedAt != 1000+patience+1 {
		t.Fatalf("expected relax to tier 1, got %+v", d)
	}
	if d.Reset || !d.Changed {
		t.Fatalf("relax should set Changed and not Reset: %+v", d)
	}
}

func TestAdvancePromotionLadderNoRelaxBeforePatience(t *testing.T) {
	order := PromotionOrderList(domain.DefaultPromotionOrder)
	patience := int64(12 * 3600)
	d := AdvancePromotionLadder(order, 0, 1000, 1000+patience-1, 0, patience)
	if d.Relaxed || d.Changed || d.Tier != 0 {
		t.Fatalf("expected no relax yet, got %+v", d)
	}
}

func TestAdvancePromotionLadderStopsAtLastTier(t *testing.T) {
	order := PromotionOrderList(domain.DefaultPromotionOrder)
	last := len(order) - 1
	patience := int64(3600)
	d := AdvancePromotionLadder(order, last, 1000, 1000+patience+1, 0, patience)
	if d.Relaxed || d.Tier != last {
		t.Fatalf("expected to stay at last tier %d, got %+v", last, d)
	}
}

func TestAdvancePromotionLadderClampsInvalidTier(t *testing.T) {
	order := PromotionOrderList(domain.DefaultPromotionOrder)
	// 负数层 → 夹到 0
	d := AdvancePromotionLadder(order, -3, 500, 900, 0, 0)
	if d.Tier != 0 || !d.Changed {
		t.Fatalf("expected clamp to 0, got %+v", d)
	}
	// 超出末层 → 夹到末层
	d = AdvancePromotionLadder(order, 99, 500, 900, 0, 0)
	if d.Tier != len(order)-1 {
		t.Fatalf("expected clamp to last tier, got %+v", d)
	}
	// TierStartedAt 为 0 时补当前时间
	d = AdvancePromotionLadder(order, 0, 0, 900, 0, 0)
	if d.TierStartedAt != 900 || !d.Changed {
		t.Fatalf("expected tierStartedAt filled with now, got %+v", d)
	}
}

func TestAdvancePromotionLadderPatienceZeroDisablesRelax(t *testing.T) {
	order := PromotionOrderList(domain.DefaultPromotionOrder)
	// patience<=0 表示不因超时放宽
	d := AdvancePromotionLadder(order, 0, 1000, 1000+99999, 0, 0)
	if d.Relaxed || d.Changed {
		t.Fatalf("patience 0 should disable relax, got %+v", d)
	}
}

func TestAdvancePromotionLadderEmptyOrder(t *testing.T) {
	d := AdvancePromotionLadder(nil, 3, 1000, 5000, 9000, 3600)
	if d.Changed || d.Tier != 3 || d.TierStartedAt != 1000 {
		t.Fatalf("empty order should be a no-op, got %+v", d)
	}
}

func TestAdvancePromotionLadderDownloadBeforeTierStartDoesNotReset(t *testing.T) {
	order := PromotionOrderList(domain.DefaultPromotionOrder)
	// 下载时间早于本层起始 → 不算新下载，但超过耐心仍应放宽
	patience := int64(3600)
	d := AdvancePromotionLadder(order, 1, 5000, 5000+patience+1, 4000, patience)
	if d.Reset {
		t.Fatalf("older download must not reset: %+v", d)
	}
	if !d.Relaxed || d.Tier != 2 {
		t.Fatalf("expected relax to tier 2, got %+v", d)
	}
}

func TestLadderTierLabel(t *testing.T) {
	order := PromotionOrderList(domain.DefaultPromotionOrder)
	if got := LadderTierLabel(order, 0); got != "free" {
		t.Fatalf("tier 0 label = %q", got)
	}
	if got := LadderTierLabel(order, 2); got != "free>2xfree>normal" {
		t.Fatalf("tier 2 label = %q", got)
	}
	if got := LadderTierLabel(order, 99); got != "free>2xfree>normal>half>2xhalf" {
		t.Fatalf("out-of-range label = %q", got)
	}
}

func TestPromotionStateName(t *testing.T) {
	if got := PromotionStateName("2xhalf"); got != "2X 50%" {
		t.Fatalf("unexpected name: %q", got)
	}
	if got := PromotionStateName("unknown"); got != "unknown" {
		t.Fatalf("unknown should pass through, got %q", got)
	}
}
