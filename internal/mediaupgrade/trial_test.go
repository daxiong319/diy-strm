package mediaupgrade

import (
	"strings"
	"testing"

	"litepan/internal/moviepilot"
)

// trialRule 造一份只影响判定的规则集（与真实扫描同口径的有效字段）。
func trialRule() *RuleSet {
	return &RuleSet{
		Source:         SourceLocal,
		LibraryRoot:    "/media",
		CandidateRoots: []string{"/downloads"},
		WashRules:      moviepilot.DefaultWashRules,
		LoserAction:    LoserActionDelete,
		Enabled:        true,
	}
}

// ---- 理由与 Trace 同源 ----

// TestRejectReasonFieldsMatchTheTraceFragment 把「Reasons 与 Trace 同源」钉死。
//
// 验收 ①：compareOne 的 Reasons 非空，且 Field/New/Old 与 Trace 里那一段一致。
//
// 为什么值得单独测：Trace 和 Reasons 是两份输出，派生自同一份维度明细，
// 但只要有人某天把其中一条改成独立计算（比如 Reasons 改用
// moviepilot.qualityFieldDisplay 而 Trace 继续用 qualityField），
// 界面上就会出现「理由说 1080p、Trace 说 1080P」或者「理由说 codec、
// Trace 说分辨率」这种自相矛盾，而且**不会报任何错**。
func TestRejectReasonFieldsMatchTheTraceFragment(t *testing.T) {
	cases := []struct {
		name     string
		newName  string
		oldName  string
		wantRel  string
		wantCode string
		// wantField 期望命中的维度；空表示不校验维度。
		wantField string
	}{
		{
			// ⚠️ 同槽位才能进 compareOne，而槽位里含分辨率/编码/制作组/声道/字幕/容器
			// 六个维度 —— 所以「同槽位却分辨率不同」根本构造不出来。
			// 真实扫描里跨分辨率是两个槽位 → no_slot，两个都保留（宁可不洗不误删）。
			// 同理，下面所有「同槽位更劣」的用例都只能在**槽位没覆盖的维度**上发生：
			// 这里能用的是色深与制作组（槽位里存的是 lower 后的组名，
			// 而 group 维度比较的是带优先级的完整名）。
			name:      "色深更差被驳回",
			newName:   "剧名.S01E01.2160p.x265.8bit.mkv",
			oldName:   "剧名.S01E01.2160p.x265.10bit.mkv",
			wantRel:   RelationNewLoses,
			wantCode:  RejectInferiorDimension,
			wantField: "bitdepth",
		},
		{
			name:     "无可比维度被兜底",
			newName:  "剧名.S01E01.mkv",
			oldName:  "剧名.S01E01.mkv",
			wantRel:  RelationNoDimension,
			wantCode: RejectNoComparableDimension,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := trialRule()
			got := TrialVerdict(rs, TrialInput{
				NewName: tc.newName,
				OldName: tc.oldName,
			})
			if got.Relation != tc.wantRel {
				t.Fatalf("relation = %q，期望 %q（trace=%q）", got.Relation, tc.wantRel, got.Trace)
			}
			if len(got.Reasons) != 1 {
				t.Fatalf("期望恰好 1 条理由，实际 %d 条：%+v", len(got.Reasons), got.Reasons)
			}
			r := got.Reasons[0]
			if r.Code != tc.wantCode {
				t.Fatalf("code = %q，期望 %q", r.Code, tc.wantCode)
			}
			if tc.wantField != "" && r.Field != tc.wantField {
				t.Fatalf("field = %q，期望 %q", r.Field, tc.wantField)
			}
			// 同源断言：理由里的 new/old 必须**字面出现在 Trace 里**。
			// 这不是弱断言 —— 如果两边各算各的，值几乎不可能同时对上。
			if r.New != "" && !strings.Contains(got.Trace, r.New) {
				t.Fatalf("理由里的 New=%q 没出现在 Trace=%q 里，两者不是同源", r.New, got.Trace)
			}
			if r.Old != "" && !strings.Contains(got.Trace, r.Old) {
				t.Fatalf("理由里的 Old=%q 没出现在 Trace=%q 里，两者不是同源", r.Old, got.Trace)
			}
		})
	}
}

// TestTieHasNoRejectReason 逐项持平、靠体积兜底没兜住时不该有驳回理由。
//
// 「两边一样」不是「被某个维度驳回」。给出 inferior_dimension 会让统计口径
// 说谎：用户在界面上按 code 筛选「被分辨率驳回」，实际这条根本没被分辨率驳回。
func TestTieHasNoRejectReason(t *testing.T) {
	rs := trialRule()
	// 同样的质量、同样的槽位，新版体积更小 ⇒ 兜底没兜住 ⇒ tie。
	got := TrialVerdict(rs, TrialInput{
		NewName: "剧名.S01E01.2160p.x265.mkv",
		NewSize: 1 << 30, HasNewSize: true,
		OldName: "剧名.S01E01.2160p.x265.mkv",
		OldSize: 9 << 30, HasOldSize: true,
	})
	if got.Relation != RelationTie {
		t.Fatalf("relation = %q，期望 %q（trace=%q）", got.Relation, RelationTie, got.Trace)
	}
	if len(got.Reasons) != 0 {
		t.Fatalf("tie 不该有驳回理由，实际 %+v", got.Reasons)
	}
	if strings.TrimSpace(got.Trace) == "" {
		t.Fatal("tie 也必须有人读的 Trace，否则界面上是空白")
	}
}

// TestNewWinsHasNoRejectReason 新版胜出没有「被驳回」这回事。
func TestNewWinsHasNoRejectReason(t *testing.T) {
	rs := trialRule()
	// 同槽位 ⇒ 只能在色深/制作组这类槽位没覆盖的维度上分胜负。
	got := TrialVerdict(rs, TrialInput{
		NewName: "剧名.S01E01.2160p.x265.10bit.mkv",
		OldName: "剧名.S01E01.2160p.x265.8bit.mkv",
	})
	if got.Relation != RelationNewWins {
		t.Fatalf("relation = %q，期望 %q（trace=%q）", got.Relation, RelationNewWins, got.Trace)
	}
	if len(got.Reasons) != 0 {
		t.Fatalf("新版胜出不该有驳回理由，实际 %+v", got.Reasons)
	}
}

// TestNoSlotReason 不同槽位给出 no_slot，且两个都保留。
func TestNoSlotReason(t *testing.T) {
	rs := trialRule()
	// 720p 与 2160p 是不同槽位 —— 真实扫描会分到不同槽位、两个都留。
	got := TrialVerdict(rs, TrialInput{
		NewName: "剧名.S01E01.720p.x264.mkv",
		OldName: "剧名.S01E01.2160p.x265.mkv",
	})
	if got.SameSlot {
		t.Fatal("720p 与 2160p 不该被判为同槽位")
	}
	if got.Relation != RelationNoDimension {
		t.Fatalf("relation = %q，期望 %q", got.Relation, RelationNoDimension)
	}
	if len(got.Reasons) != 1 || got.Reasons[0].Code != RejectNoSlot {
		t.Fatalf("期望一条 no_slot 理由，实际 %+v", got.Reasons)
	}
	// 槽位标签要能解释「为什么两个都保留」。
	if !strings.Contains(got.Trace, "槽位") {
		t.Fatalf("trace=%q 应当说明是槽位不同", got.Trace)
	}
	if got.NewSlotLabel == "" || got.OldSlotLabel == "" {
		t.Fatalf("两侧槽位标签都要给出来，实际 %q / %q", got.NewSlotLabel, got.OldSlotLabel)
	}
}

// ---- 门槛理由 ----

// TestGateReasonsCoverEveryThreshold 三条门槛各自给出对应 code。
func TestGateReasonsCoverEveryThreshold(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(rs *RuleSet)
		fileName  string
		wantCode  string
		wantField string
	}{
		{
			name:      "分辨率不足",
			mutate:    func(rs *RuleSet) { rs.MinResolution = 2160 },
			fileName:  "剧名.S01E01.1080p.x265.mkv",
			wantCode:  RejectBelowMinResolution,
			wantField: "resolution",
		},
		{
			name:   "声道不足",
			mutate: func(rs *RuleSet) { rs.MinChannels = 6 },
			// 声道数只能从真实音轨标签推：解析不出声道就是 0。
			fileName:  "剧名.S01E01.2160p.x265.AAC.mkv",
			wantCode:  RejectBelowMinChannels,
			wantField: "channels",
		},
		{
			name:      "缺字幕",
			mutate:    func(rs *RuleSet) { rs.RequireSubtitle = true },
			fileName:  "剧名.S01E01.2160p.x265.mkv",
			wantCode:  RejectMissingRequiredSubtitle,
			wantField: "subtitle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := trialRule()
			tc.mutate(rs)
			got := TrialVerdict(rs, TrialInput{
				NewName: tc.fileName,
				OldName: "剧名.S01E01.2160p.x265.10bit.mkv",
			})
			var hit *RejectReason
			for i := range got.GateReasons {
				if got.GateReasons[i].Code == tc.wantCode {
					hit = &got.GateReasons[i]
				}
			}
			if hit == nil {
				t.Fatalf("没找到 %q 的门槛理由，实际 %+v", tc.wantCode, got.GateReasons)
			}
			if hit.Field != tc.wantField {
				t.Fatalf("field = %q，期望 %q", hit.Field, tc.wantField)
			}
		})
	}
}

// TestNoGateReasonsWhenThresholdIsMet 达标的候选不该被门槛理由冤枉。
func TestNoGateReasonsWhenThresholdIsMet(t *testing.T) {
	rs := trialRule()
	rs.MinResolution = 1080
	rs.MinChannels = 6
	rs.RequireSubtitle = true
	got := TrialVerdict(rs, TrialInput{
		NewName: "剧名.S01E01.2160p.x265.TrueHD.中字.mkv",
		OldName: "剧名.S01E01.2160p.x265.10bit.mkv",
	})
	if len(got.GateReasons) != 0 {
		t.Fatalf("达标的候选不该有门槛理由，实际 %+v", got.GateReasons)
	}
}

// TestGateReasonValuesMatchTraceFormatting 门槛理由的 Old 值格式与判定一致。
//
// Old 填的是规则门槛（"2160p"/"6ch"），而不是旧文件的值 —— 门槛比的是
// 「候选 vs 规则」，拿旧文件来比就是另一回事了。
func TestGateReasonValuesMatchTraceFormatting(t *testing.T) {
	rs := trialRule()
	rs.MinResolution = 2160
	got := TrialVerdict(rs, TrialInput{
		NewName: "剧名.S01E01.1080p.x265.mkv",
		OldName: "剧名.S01E01.2160p.x265.mkv",
	})
	if len(got.GateReasons) != 1 {
		t.Fatalf("期望一条门槛理由，实际 %+v", got.GateReasons)
	}
	g := got.GateReasons[0]
	if g.Old != "2160p" {
		t.Fatalf("门槛值 = %q，期望 \"2160p\"", g.Old)
	}
	if g.New != "1080p" {
		t.Fatalf("候选值 = %q，期望 \"1080p\"（须与 Trace 用同一份格式化）", g.New)
	}
	if !strings.Contains(got.Trace, "1080p") {
		t.Fatalf("trace=%q 里应当出现候选的实际分辨率", got.Trace)
	}
}

// ---- 体积兜底 ----

// TestMissingSizeDoesNotDecideTheTrial 没填体积时，体积不参与判定。
//
// 这是 HasNewSize/HasOldSize 存在的全部理由：两个都没填时若拿 0 去比，
// 0 > 0 为假会被判成 tie，用户看到「体积不占优」而他根本没填过体积。
func TestMissingSizeDoesNotDecideTheTrial(t *testing.T) {
	rs := trialRule()
	name := "剧名.S01E01.2160p.x265.mkv"
	withoutSize := TrialVerdict(rs, TrialInput{NewName: name, OldName: name})
	if withoutSize.Relation != RelationTie {
		t.Fatalf("两文件都没填体积时应判「判不了」（tie），实际 %q（trace=%q）", withoutSize.Relation, withoutSize.Trace)
	}
	if !strings.Contains(withoutSize.Trace, "体积") {
		t.Fatalf("trace=%q 必须说明体积没参与判定，否则用户以为是体积输了", withoutSize.Trace)
	}
	if len(withoutSize.Reasons) != 0 {
		t.Fatalf("「判不了」不是「被驳回」，不该有理由：%+v", withoutSize.Reasons)
	}
	// 同样两个文件，一填体积（新版更大）就该赢 —— 证明体积兜底本身是通的，
	// 上面那条不是因为体积比较坏了。
	withSize := TrialVerdict(rs, TrialInput{
		NewName: name, NewSize: 9 << 30, HasNewSize: true,
		OldName: name, OldSize: 1 << 30, HasOldSize: true,
	})
	if withSize.Relation != RelationNewWins {
		t.Fatalf("填了体积且新版更大时 relation = %q，期望 %q（trace=%q）", withSize.Relation, RelationNewWins, withSize.Trace)
	}
}

// TestZeroSizeIsARealInput 体积 0 是合法输入，与「没填」不同。
func TestZeroSizeIsARealInput(t *testing.T) {
	rs := trialRule()
	name := "剧名.S01E01.2160p.x265.mkv"
	got := TrialVerdict(rs, TrialInput{
		NewName: name, NewSize: 0, HasNewSize: true,
		OldName: name, OldSize: 1 << 30, HasOldSize: true,
	})
	if got.Relation != RelationTie {
		t.Fatalf("新版体积 0 vs 旧版 1GB 应判 tie，实际 %q（trace=%q）", got.Relation, got.Trace)
	}
}

// ---- 试算与真实扫描同口径 ----

// TestTrialAgreesWithRealCompareOne 试算与真实 compareOne 必须一致。
//
// 验收 ④的前半：试算是给用户配规则时看的预览，真实扫描是执行。
// 两者给出相反结论时，用户会照着错的结论配规则，然后整个洗版行为都不可信。
//
// 这里不走真实扫描（那是 scan_test 的事），而是直接断言
// TrialVerdict 的 relation/trace 与 compareOne 逐字相同 ——
// 这样一旦有人让试算走「试算专用」逻辑立刻红。
func TestTrialAgreesWithRealCompareOne(t *testing.T) {
	pairs := [][2]string{
		{"剧名.S01E01.2160p.x265.10bit.mkv", "剧名.S01E01.1080p.x264.mkv"},
		{"剧名.S01E01.1080p.x264.mkv", "剧名.S01E01.2160p.x265.mkv"},
		{"剧名.S01E01.2160p.x265.mkv", "剧名.S01E01.2160p.h265.mkv"},
		{"剧名.S01E01.1080p.x264.mkv", "剧名.S01E01.1080p.x264.mkv"},
		{"剧名.S01E01.mkv", "剧名.S01E01.mkv"},
		{"剧名.S01E01.2160p.x265.10bit.TrueHD-CHD.mkv", "剧名.S01E01.2160p.x265.10bit-GRP.mkv"},
	}
	for _, p := range pairs {
		rs := trialRule()
		eff := rs.Effective()
		trial := TrialVerdict(rs, TrialInput{NewName: p[0], OldName: p[1]})
		if !trial.SameSlot {
			continue // 不同槽位根本不会进 compareOne
		}
		real := compareOne(
			trialFile(p[0], 0, false),
			[]libFile{trialFile(p[1], 0, false)},
			eff.WashRules,
			eff.GroupPriority,
		)
		if trial.Relation != real.Relation {
			t.Fatalf("%s vs %s：试算 relation=%q，真实 compareOne=%q", p[0], p[1], trial.Relation, real.Relation)
		}
		if trial.Trace != real.Trace {
			t.Fatalf("%s vs %s：试算 trace=%q，真实 compareOne=%q", p[0], p[1], trial.Trace, real.Trace)
		}
		if len(trial.Reasons) != len(real.Reasons) {
			t.Fatalf("%s vs %s：试算理由 %d 条，真实 %d 条", p[0], p[1], len(trial.Reasons), len(real.Reasons))
		}
	}
}

// TestTrialDimensionsComeFromTheSameSource 逐维度明细与 Trace 出自同一份。
func TestTrialDimensionsComeFromTheSameSource(t *testing.T) {
	rs := trialRule()
	got := TrialVerdict(rs, TrialInput{
		NewName: "剧名.S01E01.2160p.x265.8bit.mkv",
		OldName: "剧名.S01E01.2160p.x265.10bit.mkv",
	})
	if len(got.Dimensions) == 0 {
		t.Fatal("同槽位可比的输入必须给出逐维度明细，否则界面画不出表")
	}
	for _, d := range got.Dimensions {
		if !d.Better && !d.Worse && d.New == d.Old {
			continue // 持平项
		}
		if !strings.Contains(got.Trace, d.New) && d.New != "" {
			t.Fatalf("明细里的 %s=%q 没出现在 trace=%q 里", d.Field, d.New, got.Trace)
		}
	}
	// 决胜维度就是 Reasons 指的那一个 —— 不许出现
	// 「理由说分辨率、决胜在色深」这种口径漂移。
	if len(got.Reasons) == 1 && got.Reasons[0].Field != "" {
		var found bool
		for _, d := range got.Dimensions {
			if d.Field == got.Reasons[0].Field {
				found = true
				if !d.Worse {
					t.Fatalf("理由指向的 %s 维度不是 Worse，两处口径不一致", d.Field)
				}
			}
		}
		if !found {
			t.Fatalf("理由指向的维度 %q 不在明细里", got.Reasons[0].Field)
		}
	}
}

// TestTrialSummaryNeverEmpty 摘要任何分支都有人话。
func TestTrialSummaryNeverEmpty(t *testing.T) {
	rs := trialRule()
	rs.MinResolution = 2160
	inputs := []TrialInput{
		{NewName: "剧名.S01E01.2160p.x265.mkv", OldName: "剧名.S01E01.1080p.x264.mkv"},
		{NewName: "剧名.S01E01.1080p.x264.mkv", OldName: "剧名.S01E01.2160p.x265.mkv"},
		{NewName: "剧名.S01E01.mkv", OldName: "剧名.S01E01.mkv"},
		{NewName: "剧名.S01E01.720p.x264.mkv", OldName: "剧名.S01E01.2160p.x265.mkv"},
		{NewName: "剧名.S01E01.1080p.x264.mkv", OldName: "剧名.S01E01.1080p.x264.mkv"},
	}
	for i, in := range inputs {
		if s := TrialSummary(TrialVerdict(rs, in)); strings.TrimSpace(s) == "" {
			t.Fatalf("第 %d 个输入的摘要为空", i)
		}
	}
}

// ---- 规则指纹 ----

// TestFingerprintIsStableAcrossFieldOrder 数组顺序无关（验收 ③）。
//
// 用户在前端把「编码」从第 3 项拖到第 1 项，语义上一条规则都没变。
// 不先排序就哈希的话，一次纯粹的界面调整会让整批记录的指纹失效、
// 看起来像换过规则 —— 而用户永远查不出为什么。
func TestFingerprintIsStableAcrossFieldOrder(t *testing.T) {
	base := trialRule()
	reordered := trialRule()
	reordered.WashRules = []moviepilot.WashRule{
		{Field: "channels", Higher: true},
		{Field: "bitdepth", Higher: true},
		{Field: "format", Higher: true},
		{Field: "codec", Higher: true},
		{Field: "resolution", Higher: true},
		{Field: "group", Higher: true},
	}
	if got, want := reordered.Fingerprint(), base.Fingerprint(); got != want {
		t.Fatalf("洗版维度换顺序后指纹变了：%q != %q", got, want)
	}

	reorderedGroup := trialRule()
	reorderedGroup.GroupPriority = []string{"CHD", "FRDS", "CMCTV"}
	plainGroup := trialRule()
	plainGroup.GroupPriority = []string{"FRDS", "CMCTV", "CHD"}
	if got, want := reorderedGroup.Fingerprint(), plainGroup.Fingerprint(); got != want {
		t.Fatalf("制作组优先级换顺序后指纹变了：%q != %q", got, want)
	}
}

// TestFingerprintIsStableAcrossRepeatedCalls 同一份规则重复算必须一样。
func TestFingerprintIsStableAcrossRepeatedCalls(t *testing.T) {
	rs := trialRule()
	rs.MinChannels = 6
	rs.RequireSubtitle = true
	first := rs.Fingerprint()
	for i := 0; i < 5; i++ {
		if got := rs.Fingerprint(); got != first {
			t.Fatalf("第 %d 次算出 %q，首次是 %q", i, got, first)
		}
	}
	if len(first) != FingerprintLength {
		t.Fatalf("指纹长度 = %d，期望 %d", len(first), FingerprintLength)
	}
}

// TestFingerprintChangesWithEveryEffectiveField 任一有效字段变则变（验收 ③）。
func TestFingerprintChangesWithEveryEffectiveField(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(rs *RuleSet)
	}{
		{"最小分辨率", func(rs *RuleSet) { rs.MinResolution = 2160 }},
		{"最小声道", func(rs *RuleSet) { rs.MinChannels = 6 }},
		{"要求字幕", func(rs *RuleSet) { rs.RequireSubtitle = true }},
		{"败方动作", func(rs *RuleSet) { rs.LoserAction = LoserActionMove }},
		{"制作组优先级", func(rs *RuleSet) { rs.GroupPriority = []string{"FRDS"} }},
		{"维度顺序语义", func(rs *RuleSet) {
			// 只留 resolution 一项：维度集合变了。
			rs.WashRules = []moviepilot.WashRule{{Field: "resolution", Higher: true}}
		}},
		{"维度方向", func(rs *RuleSet) {
			// 同一组字段反转 Higher 是彻底不同的判定口径。
			rs.WashRules = []moviepilot.WashRule{{Field: "resolution", Higher: false}}
		}},
	}
	base := trialRule().Fingerprint()
	for _, m := range mutations {
		rs := trialRule()
		m.mutate(rs)
		if got := rs.Fingerprint(); got == base {
			t.Fatalf("改了「%s」之后指纹没变（%q）—— 这条记录会被误判成旧规则下的", m.name, got)
		}
	}
}

// TestFingerprintIgnoresNonEffectiveFields 不参与判定的字段不该改指纹。
//
// 改名字、改媒体库根不该让整批历史记录的归属变成「未知规则版本」；
// 反过来「规则变了」判不出来，就等于这个指纹没起到作用。
func TestFingerprintIgnoresNonEffectiveFields(t *testing.T) {
	base := trialRule().Fingerprint()
	mutations := []struct {
		name   string
		mutate func(rs *RuleSet)
	}{
		{"规则名", func(rs *RuleSet) { rs.Name = "另一个名字" }},
		{"媒体库根", func(rs *RuleSet) { rs.LibraryRoot = "/other" }},
		{"候选目录", func(rs *RuleSet) { rs.CandidateRoots = []string{"/other"} }},
		{"移动目录", func(rs *RuleSet) { rs.MoveDir = "/archive" }},
		{"每剧上限", func(rs *RuleSet) { rs.MaxRecordsPerSeries = 9 }},
		{"总开关", func(rs *RuleSet) { rs.Enabled = false }},
	}
	for _, m := range mutations {
		rs := trialRule()
		m.mutate(rs)
		if got := rs.Fingerprint(); got != base {
			t.Fatalf("改了「%s」（不参与判定）后指纹变了：%q != %q", m.name, got, base)
		}
	}
}

// TestEmptyWashRulesFingerprintsAsDefault 空规则与显式默认规则同指纹。
//
// 判定时两者都回落 DefaultWashRules，所以它们是**同一份规则**。
// 给出两个指纹的话，用户把 wash_rules 从空改成默认写法，
// 全部历史记录的规则版本归属就集体「变了」，而判定结果一条都没变。
func TestEmptyWashRulesFingerprintsAsDefault(t *testing.T) {
	empty := trialRule()
	empty.WashRules = nil
	explicit := trialRule()
	explicit.WashRules = append([]moviepilot.WashRule(nil), moviepilot.DefaultWashRules...)
	if got, want := empty.Fingerprint(), explicit.Fingerprint(); got != want {
		t.Fatalf("空规则与显式默认规则指纹不同：%q != %q", got, want)
	}
}

// TestNilRuleSetFingerprintIsEmpty nil 规则集不能 panic。
func TestNilRuleSetFingerprintIsEmpty(t *testing.T) {
	var rs *RuleSet
	if got := rs.Fingerprint(); got != "" {
		t.Fatalf("nil 规则集的指纹 = %q，期望空串", got)
	}
}
