package mediaupgrade

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"litepan/internal/moviepilot"
	"litepan/internal/settings"
)

// TestSplitRootsAcceptsEverySeparator 用户在设置框里能打出的分隔符都得认。
//
// 目录列表是从文本框里来的，中英文标点混用、末尾多个换行都很常见。
// 只认英文逗号的话，用户粘进来一串「/a、/b」会被当成**一个**目录，
// 扫描静默地什么都不匹配，界面上表现为「扫过了但没发现」。
func TestSplitRootsAcceptsEverySeparator(t *testing.T) {
	for _, sep := range []string{",", "，", "、", ";", "；", "\n", "\t", " "} {
		raw := "/media/lib" + sep + "/media/cand"
		got := SplitRoots(raw)
		if len(got) != 2 || got[0] != "/media/cand" || got[1] != "/media/lib" {
			t.Errorf("分隔符 %q 切出 %v，期望 [/media/cand /media/lib]", sep, got)
		}
	}
}

// TestSplitRootsIsStable 去重 + 排序不是洁癖，是硬需求。
//
// 扫描阶段和提交阶段各自调一次 SplitRoots，
// 如果同一批目录两次进来顺序不同，「候选目录集合」的比较会被误判成发生了变化，
// 于是一条完全没变过的判定被标成 expired —— 用户看到的是「我什么都没动它就过期了」。
func TestSplitRootsIsStable(t *testing.T) {
	a := SplitRoots("/b,/a,/c,/a")
	b := SplitRoots("/c\n/a,/b")
	if len(a) != 3 {
		t.Fatalf("去重后 %d 项，期望 3 项：%v", len(a), a)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("同一批目录两次切出来不一致：%v vs %v", a, b)
		}
	}
	// 空白也要吃掉，否则 "/a, /b" 会切出一个叫 " /b" 的目录。
	got := SplitRoots("  /a  ,  /b  ,, ,\n ")
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Errorf("空白未清理干净：%q", got)
	}
	if len(SplitRoots("")) != 0 || len(SplitRoots("  ,；、\n")) != 0 {
		t.Errorf("空输入应切出空切片")
	}
}

func TestJoinRootsRoundTrips(t *testing.T) {
	list := SplitRoots("/b,/a")
	if JoinRoots(list) != "/a,/b" {
		t.Errorf("JoinRoots=%q，期望排序后的逗号串", JoinRoots(list))
	}
	if got := JoinRoots(SplitRoots(JoinRoots(list))); got != JoinRoots(list) {
		t.Errorf("往返不稳定：%q → %q", JoinRoots(list), got)
	}
}

// TestValidateRejectsConfigsThatWouldMisfire 校验的是会导致误删或空转的配置。
func TestValidateRejectsConfigsThatWouldMisfire(t *testing.T) {
	base := func() RuleSet {
		rs := testRuleSet("/lib", "/cand")
		return *rs
	}
	cases := []struct {
		name   string
		mutate func(*RuleSet)
		want   string
	}{
		{"扫描源为空", func(r *RuleSet) { r.Source = "" }, "扫描源不能为空"},
		{"扫描源不认识", func(r *RuleSet) { r.Source = "plex" }, "不支持的扫描源"},
		{"媒体库目录为空", func(r *RuleSet) { r.LibraryRoot = "  " }, "未配置媒体库根目录"},
		{"败方动作为空", func(r *RuleSet) { r.LoserAction = "" }, "败方动作不能为空"},
		{"败方动作不认识", func(r *RuleSet) { r.LoserAction = "rm -rf" }, "不支持的败方动作"},
		{"搬迁没给目标目录", func(r *RuleSet) { r.LoserAction = LoserActionMove }, "败方动作设为 move 时必须填写移动目标目录"},
		{"上限为负", func(r *RuleSet) { r.MaxRecordsPerSeries = -1 }, "每部剧记录上限不能为负数"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := base()
			tc.mutate(&rs)
			err := rs.Validate()
			if err == nil {
				t.Fatalf("期望被拦下，实际通过了")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误文案 %q 里没有 %q —— 文案要直接展示给用户，不能只是给日志看", err, tc.want)
			}
		})
	}
	def := base()
	if err := def.Validate(); err != nil {
		t.Errorf("默认规则集不该被拦下：%v", err)
	}
	ok := base()
	ok.LoserAction = LoserActionMove
	ok.MoveDir = "/archive"
	if err := ok.Validate(); err != nil {
		t.Errorf("move 配了目标目录就不该被拦下：%v", err)
	}
	// 上限 0 是合法值，意思是「不设上限」，不能当成「非法」。
	zero := base()
	zero.MaxRecordsPerSeries = 0
	if err := zero.Validate(); err != nil {
		t.Errorf("上限 0 应被接受（不设上限）：%v", err)
	}
}

// TestLoadRuleSetGlobalSwitchIsOffByDefault 断言默认状态下这个功能是关着的。
//
// 这是个会删用户文件的功能。默认打开意味着升级完版本，
// 用户的媒体库里就会莫名其妙少文件 —— 所以总开关关着时必须直接报 ErrDisabled，
// 而不是「静默地什么都不做」，否则用户会以为扫过了只是没发现。
func TestLoadRuleSetGlobalSwitchIsOffByDefault(t *testing.T) {
	db := openTestDB(t)
	if _, err := LoadRuleSet(db, newFakeSettings(nil), 0); !errors.Is(err, ErrDisabled) {
		t.Fatalf("总开关默认应关闭，实际返回 %v", err)
	}
	// 即便库根目录配好了，只要总开关没开就仍然是关的 —— 开关必须是闸门而不是建议。
	on := map[string]string{
		settings.KeyMOMediaUpgradeEnabled:        "true",
		settings.KeyMOMediaUpgradeLibraryRoot:    "/lib",
		settings.KeyMOMediaUpgradeSource:         SourceLocal,
		settings.KeyMOMediaUpgradeCandidateRoots: "/lib,/cand",
	}
	if _, err := LoadRuleSet(db, newFakeSettings(on), 0); err != nil {
		t.Fatalf("总开关打开后不该报未启用：%v", err)
	}
}

// TestLoadRuleSetAssemblesFromGlobalSettings 断言全局设置真的被拼成了规则集。
func TestLoadRuleSetAssemblesFromGlobalSettings(t *testing.T) {
	db := openTestDB(t)
	rs, err := LoadRuleSet(db, newFakeSettings(map[string]string{
		settings.KeyMOMediaUpgradeEnabled:             "true",
		settings.KeyMOMediaUpgradeSource:              SourceLocal,
		settings.KeyMOMediaUpgradeLibraryRoot:         "  /media/剧  ",
		settings.KeyMOMediaUpgradeCandidateRoots:      "/candB、/candA,/candA",
		settings.KeyMOMediaUpgradeLoserAction:         LoserActionDelete,
		settings.KeyMOMediaUpgradeMinResolution:       "1080",
		settings.KeyMOMediaUpgradeMinChannels:         "6",
		settings.KeyMOMediaUpgradeRequireSubtitle:     "true",
		settings.KeyMOMediaUpgradeMaxRecordsPerSeries: "3",
		settings.KeyMOMediaUpgradeGroupPriority:       "CHD,FRDS",
	}), 0)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if rs.ID != 0 {
		t.Errorf("全局规则的 ID 应恒为 0（0 是回落到全局设置的哨兵值），实际 %d", rs.ID)
	}
	if rs.LibraryRoot != "/media/剧" {
		t.Errorf("库根目录未去空白：%q", rs.LibraryRoot)
	}
	if len(rs.CandidateRoots) != 2 || rs.CandidateRoots[0] != "/candA" || rs.CandidateRoots[1] != "/candB" {
		t.Errorf("候选目录未去重排序：%v", rs.CandidateRoots)
	}
	if rs.MinResolution != 1080 || rs.MinChannels != 6 || !rs.RequireSubtitle || rs.MaxRecordsPerSeries != 3 {
		t.Errorf("数值/开关字段没读进来：%+v", rs)
	}
	if len(rs.GroupPriority) != 2 {
		t.Errorf("制作组优先级没切分：%v", rs.GroupPriority)
	}
	// 没配 wash_rules 时必须兜底成默认规则，否则比较会退化成「全都一样」。
	if len(rs.WashRules) == 0 {
		t.Errorf("未配置洗版规则时应兜底成默认规则集")
	}
}

// TestLoadRuleSetValidatesNamedRules 具名规则入库前也要过校验。
func TestLoadRuleSetValidatesNamedRules(t *testing.T) {
	db := openTestDB(t)
	bad := testRuleSet("", "/cand")
	id := saveRule(t, db, bad)
	if _, err := LoadRuleSet(db, newFakeSettings(nil), id); err == nil {
		t.Fatalf("库根目录为空的规则不该被放行")
	}
	if _, err := LoadRuleSet(db, newFakeSettings(nil), 9999); err == nil {
		t.Fatalf("不存在的规则 ID 应当报错而不是静默回落")
	}
	// 具名规则不查总开关：用户在规则页明确建了规则就是显式意图。
	good := testRuleSet("/lib", "/cand")
	rs, err := LoadRuleSet(db, newFakeSettings(nil), saveRule(t, db, good))
	if err != nil {
		t.Fatalf("读取具名规则失败：%v", err)
	}
	if rs.LibraryRoot != "/lib" || len(rs.CandidateRoots) != 1 {
		t.Errorf("具名规则字段没读回来：%+v", rs)
	}
}

// TestEffectiveFillsDefaults 只兜底默认值，不覆盖用户已经填的值。
func TestEffectiveFillsDefaults(t *testing.T) {
	got := (&RuleSet{}).Effective()
	if got.Source != SourceLocal || got.LoserAction != LoserActionKeep {
		t.Errorf("空规则集应兜底成 local+keep，实际 %+v", got)
	}
	if len(got.WashRules) == 0 {
		t.Errorf("空规则集应兜底成默认洗版规则")
	}
	set := (&RuleSet{Source: SourceEmby, LoserAction: LoserActionDelete}).Effective()
	if set.Source != SourceEmby || set.LoserAction != LoserActionDelete {
		t.Errorf("Effective 不该覆盖用户填的值：%+v", set)
	}
}

// TestSaveRuleCreateAndUpdate 走一遍规则 CRUD。
func TestSaveRuleCreateAndUpdate(t *testing.T) {
	db := openTestDB(t)
	svc := New(db, newFakeSettings(nil))
	ctx := context.Background()

	created, err := svc.SaveRule(ctx, &Rule{
		Name: "1080p 剧集", Source: SourceLocal, LibraryRoot: "/lib",
		CandidateRoots: "/cand", LoserAction: LoserActionDelete, Enabled: true,
	})
	if err != nil {
		t.Fatalf("新建失败：%v", err)
	}
	if created.ID == 0 {
		t.Fatalf("新建后没有拿到 ID")
	}
	list, err := svc.ListRules(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("列表应有 1 条，实际 %d 条（err=%v）", len(list), err)
	}

	// 更新：字段改得动。
	created.MinResolution = 1080
	created.LoserAction = LoserActionMove
	created.MoveDir = "/archive"
	updated, err := svc.SaveRule(ctx, created)
	if err != nil {
		t.Fatalf("更新失败：%v", err)
	}
	if updated.MinResolution != 1080 || updated.LoserAction != LoserActionMove || updated.MoveDir != "/archive" {
		t.Errorf("更新没落库：%+v", updated)
	}

	// 非法配置在入库前就被拦下，不该产生半条脏数据。
	if _, err := svc.SaveRule(ctx, &Rule{
		Name: "坏的", Source: SourceLocal, LibraryRoot: "/lib", LoserAction: LoserActionMove,
	}); err == nil {
		t.Errorf("move 缺目标目录的规则不该入库")
	}

	if err := svc.DeleteRule(ctx, created.ID); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if list, _ := svc.ListRules(ctx); len(list) != 0 {
		t.Errorf("删除后仍能列出 %d 条", len(list))
	}
	if err := svc.DeleteRule(ctx, 9999); err == nil {
		t.Errorf("删除不存在的规则应当报错")
	}
}

// TestSaveRuleCannotRewriteSystemColumns 更新走的是字段白名单，
// 请求体里的 builtin / created_at 不该被写进去。
//
// builtin 是给「全局回落到内置规则」留的锚点。
// 一旦它能被一个普通的保存请求改成 false，这条规则就再也回不到内置口径，
// 而且从界面上完全看不出它是什么时候变的。
func TestSaveRuleCannotRewriteSystemColumns(t *testing.T) {
	db := openTestDB(t)
	svc := New(db, newFakeSettings(nil))
	ctx := context.Background()

	created, err := svc.SaveRule(ctx, &Rule{
		Name: "内置规则", Source: SourceLocal, LibraryRoot: "/lib",
		LoserAction: LoserActionKeep, Enabled: true,
		Builtin: true,
	})
	if err != nil {
		t.Fatalf("新建失败：%v", err)
	}
	originalCreatedAt := created.CreatedAt

	created.Builtin = false
	created.CreatedAt = originalCreatedAt.Add(-72 * time.Hour)
	created.LibraryRoot = "/lib2"
	after, err := svc.SaveRule(ctx, created)
	if err != nil {
		t.Fatalf("更新失败：%v", err)
	}
	if !after.Builtin {
		t.Errorf("builtin 被请求体改掉了 —— 白名单失效")
	}
	if !after.CreatedAt.Equal(originalCreatedAt) {
		t.Errorf("created_at 被请求体改掉了：%v → %v", originalCreatedAt, after.CreatedAt)
	}
	if after.LibraryRoot != "/lib2" {
		t.Errorf("正常字段反而没更新：%q", after.LibraryRoot)
	}
}

// TestGlobalRuleIsTheSentinelZero 断言全局视图用 ID=0。
//
// 0 是 LoadRuleSet 里「回落到全局设置」的哨兵值。
// 给它一个真 ID，UI 编辑它时就会去改一条根本不存在的库内规则行，
// 用户改了半天全局设置却毫无变化 —— 这正是最难排查的一类接线错误。
func TestGlobalRuleIsTheSentinelZero(t *testing.T) {
	db := openTestDB(t)
	svc := New(db, newFakeSettings(map[string]string{
		settings.KeyMOMediaUpgradeEnabled:     "true",
		settings.KeyMOMediaUpgradeSource:      SourceEmby,
		settings.KeyMOMediaUpgradeLibraryRoot: "/emby/剧",
		settings.KeyMOMediaUpgradeLoserAction: LoserActionDelete,
	}))
	row, err := svc.GlobalRule()
	if err != nil {
		t.Fatalf("读取全局视图失败：%v", err)
	}
	if row.ID != 0 {
		t.Errorf("全局规则 ID 应为 0，实际 %d", row.ID)
	}
	if !row.Builtin {
		t.Errorf("全局规则应标为 builtin")
	}
	if !row.Enabled || row.Source != SourceEmby || row.LibraryRoot != "/emby/剧" {
		t.Errorf("全局视图没反映设置：%+v", row)
	}
	if row.LoserAction != LoserActionDelete {
		t.Errorf("败方动作没反映设置：%q", row.LoserAction)
	}
	// 这个视图必须能被 RuleFromRow 原样吃回去，否则 UI 的同一套表单会两套口径。
	if rs := RuleFromRow(row); rs.Validate() != nil {
		t.Errorf("全局视图自己过不了自己的校验：%v", rs.Validate())
	}
}

// TestRuleFromRowFallsBackToDefaultWashRules 非法 JSON 要回退默认，而不是回退成空。
func TestRuleFromRowFallsBackToDefaultWashRules(t *testing.T) {
	if rs := RuleFromRow(&Rule{WashRules: "{不是合法 JSON"}); len(rs.WashRules) == 0 {
		t.Errorf("非法 wash_rules 应回退成默认规则集，实际为空")
	}
	if rs := RuleFromRow(&Rule{}); len(rs.WashRules) != len(moviepilot.DefaultWashRules) {
		t.Errorf("未配 wash_rules 时应回退成默认规则集")
	}
}

// TestWashRulesRoundTripThroughStorage 比较规则要能原样存进去再取出来。
func TestWashRulesRoundTripThroughStorage(t *testing.T) {
	db := openTestDB(t)
	rs := testRuleSet("/lib", "/cand")
	rs.WashRules = []moviepilot.WashRule{{Field: "bitdepth", Higher: true}}
	id := saveRule(t, db, rs)
	back, err := LoadRuleSet(db, newFakeSettings(nil), id)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(back.WashRules) != 1 || back.WashRules[0].Field != "bitdepth" || !back.WashRules[0].Higher {
		t.Errorf("洗版规则往返后变了：%+v", back.WashRules)
	}
}
