package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"litepan/internal/automation"
	"litepan/internal/cloudref"
	"litepan/internal/domain"
	"litepan/internal/mediaupgrade"
	"litepan/internal/settings"
	"litepan/internal/store"
)

// stubRuleLister 是 automation 服务的测试替身。
type stubRuleLister struct {
	rules []automation.RuleView
	err   error
}

func (s stubRuleLister) ListRules(context.Context) ([]automation.RuleView, error) {
	return s.rules, s.err
}

func casRule(id int64, name, path string) automation.RuleView {
	cfg := map[string]any{}
	if path != "" {
		cfg["path"] = path
	}
	return automation.RuleView{
		ID: id, Name: name,
		TriggerType:   domain.AutomationTriggerCasAutoSave,
		TriggerConfig: cfg,
		Status:        domain.AutomationStatusRunning,
	}
}

// TestMonitorSourcesOnlyFromDirectoryTriggers 只收「目录型」触发器。
//
// 把定时/webhook 规则也算进来会让「不是源目录」这条 warn 变成假话：
// 用户看到红色提示说这个目录没人整理，实际上他确实配过一条定时整理规则。
func TestMonitorSourcesOnlyFromDirectoryTriggers(t *testing.T) {
	lister := stubRuleLister{rules: []automation.RuleView{
		casRule(1, "CAS转存", "/watch/cas"),
		{ID: 2, Name: "离线监控", TriggerType: domain.AutomationTriggerOfflineDownload,
			TriggerConfig: map[string]any{"path": "/watch/offline"}, Status: domain.AutomationStatusRunning},
		{ID: 3, Name: "每天整理", TriggerType: domain.AutomationTriggerDaily,
			TriggerConfig: map[string]any{"hour": 3}, Status: domain.AutomationStatusRunning},
		{ID: 4, Name: "回调", TriggerType: domain.AutomationTriggerWebhook,
			TriggerConfig: map[string]any{"path_prefix": "/hook"}, Status: domain.AutomationStatusRunning},
		{ID: 5, Name: "观影报告", TriggerType: domain.AutomationTriggerPlayReport,
			TriggerConfig: map[string]any{}, Status: domain.AutomationStatusRunning},
	}}

	got, notes := monitorSourcesFrom(lister)
	want := []string{"/watch/cas", "/watch/offline"}
	if len(got) != len(want) {
		t.Fatalf("源目录=%v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项=%q，期望 %q", i, got[i], want[i])
		}
	}
	if len(notes) != 0 {
		t.Errorf("正常路径不该有 Notes：%v", notes)
	}
}

// TestMonitorSourcesSkipEmptyPath 是这个函数最重要的一条。
//
// cas_autosave 的 path 留空 = 「任意目录的转存都触发」。把它算成源目录
// 的话，任何路径都判成是源目录，「不是源目录」这条 warn 永远不会出现，
// 用户看到的是一个永远亮绿灯的假提示 —— 比没有提示更糟。
func TestMonitorSourcesSkipEmptyPath(t *testing.T) {
	lister := stubRuleLister{rules: []automation.RuleView{
		casRule(1, "全盘监听", ""),
		{ID: 2, Name: "无path键", TriggerType: domain.AutomationTriggerCasAutoSave,
			TriggerConfig: map[string]any{"other": 1}, Status: domain.AutomationStatusRunning},
		{ID: 3, Name: "空配置", TriggerType: domain.AutomationTriggerOfflineDownload,
			TriggerConfig: nil, Status: domain.AutomationStatusRunning},
		{ID: 4, Name: "真目录", TriggerType: domain.AutomationTriggerCasAutoSave,
			TriggerConfig: map[string]any{"path": "/watch/cas"}, Status: domain.AutomationStatusRunning},
	}}

	got, _ := monitorSourcesFrom(lister)
	if len(got) != 1 || got[0] != "/watch/cas" {
		t.Fatalf("源目录=%v，期望只有 [/watch/cas]", got)
	}

	// 全是空 path ⇒ 一条源目录都没有 ⇒ 下游不该出任何源目录提示。
	if got, _ := monitorSourcesFrom(stubRuleLister{rules: []automation.RuleView{casRule(1, "全盘", "")}}); len(got) != 0 {
		t.Errorf("全空 path 时源目录应为空，实际 %v", got)
	}
}

// TestMonitorSourcesSkipPausedRules 暂停的规则不触发任何东西，
// 算成源目录会让用户以为「我明明配了这条规则」。
func TestMonitorSourcesSkipPausedRules(t *testing.T) {
	paused := casRule(1, "已暂停", "/watch/paused")
	paused.Status = domain.AutomationStatusPaused
	lister := stubRuleLister{rules: []automation.RuleView{paused, casRule(2, "运行中", "/watch/cas")}}

	got, _ := monitorSourcesFrom(lister)
	if len(got) != 1 || got[0] != "/watch/cas" {
		t.Errorf("源目录=%v，期望跳过暂停规则只剩 [/watch/cas]", got)
	}
}

// TestMonitorSourcesRejectNonStringPath 用户在 JSON 表单里能填出任何东西。
// 数字/数组不是合法目录，当成「不参与」比硬转成字符串靠谱 ——
// 硬转会造出一个 /123 这种不存在的根，让「在不在库内」变成掷硬币。
func TestMonitorSourcesRejectNonStringPath(t *testing.T) {
	lister := stubRuleLister{rules: []automation.RuleView{
		{ID: 1, Name: "数字", TriggerType: domain.AutomationTriggerCasAutoSave,
			TriggerConfig: map[string]any{"path": 123}, Status: domain.AutomationStatusRunning},
		{ID: 2, Name: "数组", TriggerType: domain.AutomationTriggerCasAutoSave,
			TriggerConfig: map[string]any{"path": []string{"/a"}}, Status: domain.AutomationStatusRunning},
		{ID: 3, Name: "正常", TriggerType: domain.AutomationTriggerCasAutoSave,
			TriggerConfig: map[string]any{"path": "/watch/cas"}, Status: domain.AutomationStatusRunning},
	}}
	got, _ := monitorSourcesFrom(lister)
	if len(got) != 1 || got[0] != "/watch/cas" {
		t.Errorf("源目录=%v，期望 [/watch/cas]", got)
	}
	if s := anyToString(123); s != "" {
		t.Errorf("anyToString(123)=%q，期望空串", s)
	}
	if s := anyToString("/a"); s != "/a" {
		t.Errorf("anyToString(\"/a\")=%q", s)
	}
}

// TestMonitorSourcesDegradeWhenListFails 读失败不能 500 也不能假装没配：
// 降级成「不给源目录提示」，并把失败原因如实带回前端。
// 假装没配的话界面会安静地什么都不显示，用户以为功能坏了；
// 假装配了的话会给出一堆假提示。
func TestMonitorSourcesDegradeWhenListFails(t *testing.T) {
	got, notes := monitorSourcesFrom(stubRuleLister{err: errors.New("数据库炸了")})
	if len(got) != 0 {
		t.Errorf("读失败时源目录应为空，实际 %v", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "数据库炸了") {
		t.Errorf("读失败原因应回给前端，实际 %v", notes)
	}
	if !strings.Contains(notes[0], "已省略") {
		t.Errorf("应说明相关提示被省略，实际 %q", notes[0])
	}
}

// TestMonitorSourcesDeduplicate 两条规则配了同一个目录是常事
// （CAS 与离线各一条，指向同一个待整理目录）。
func TestMonitorSourcesDeduplicate(t *testing.T) {
	lister := stubRuleLister{rules: []automation.RuleView{
		casRule(1, "A", "/watch/cas"),
		{ID: 2, Name: "B", TriggerType: domain.AutomationTriggerOfflineDownload,
			TriggerConfig: map[string]any{"path": "/watch/cas/"}, Status: domain.AutomationStatusRunning},
		{ID: 3, Name: "C", TriggerType: domain.AutomationTriggerCasAutoSave,
			TriggerConfig: map[string]any{"path": "watch/cas"}, Status: domain.AutomationStatusRunning},
	}}
	got, _ := monitorSourcesFrom(lister)
	if len(got) != 1 || got[0] != "/watch/cas" {
		t.Errorf("归一去重后应只剩 [/watch/cas]，实际 %v", got)
	}
}

// ── 路由与响应 ──────────────────────────────────────────────────────

func TestResolveDirRefEndpoint(t *testing.T) {
	h := &Handler{
		mediaUpgrade: newTestMediaUpgrade(t, []string{"/media/影视"}, ""),
		automation:   newTestAutomation(t, []automation.RuleView{casRule(1, "CAS", "/watch/cas")}),
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/dir-refs?path=/media/影视/某剧", nil)
	rec := httptest.NewRecorder()
	h.resolveDirRef(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码=%d，body=%s", rec.Code, rec.Body.String())
	}
	var got dirRefResp
	decodeEnvelope(t, rec.Body.Bytes(), &got)

	if !got.References.InLibrary {
		t.Errorf("/media/影视/某剧 应判定在库内，实际 %+v", got.References)
	}
	if !got.Configured {
		t.Errorf("两处配置都读到了，Configured 应为 true")
	}
	if got.Path != "/media/影视/某剧" {
		t.Errorf("Path=%q，期望归一后的原值", got.Path)
	}
	// 响应里必须带上「Emby 索引库不在覆盖范围内」这条边界。
	if !containsNote(got.Refs.Notes, "不覆盖 Emby") && !containsNote(got.Refs.Notes, "不含 Emby") {
		t.Errorf("缺少 Emby 覆盖边界说明：%v", got.Refs.Notes)
	}
	if !containsNote(got.Refs.Notes, "source_dir_id") {
		t.Errorf("缺少整理目录 source_dir_id 的边界说明：%v", got.Refs.Notes)
	}
}

// TestResolveDirRefEmptyPathIsNotAnError 用户在输入框里按下第一个字符
// 的瞬间就会带着半个路径来请求。红框没有意义，正确做法是安静地不给提示。
func TestResolveDirRefEmptyPathIsNotAnError(t *testing.T) {
	h := &Handler{
		mediaUpgrade: newTestMediaUpgrade(t, []string{"/media/影视"}, ""),
		automation:   newTestAutomation(t, []automation.RuleView{casRule(1, "CAS", "/watch/cas")}),
	}
	for _, raw := range []string{"", "  ", "\n", "/media/影视\n/media/电影"} {
		req := httptest.NewRequest(http.MethodGet, "/admin/dir-refs?path="+urlQueryEscape(raw), nil)
		rec := httptest.NewRecorder()
		h.resolveDirRef(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("path=%q 状态码=%d，期望 200", raw, rec.Code)
		}
		var got dirRefResp
		decodeEnvelope(t, rec.Body.Bytes(), &got)
		if len(got.References.Hints) != 0 || got.Path != "" {
			t.Errorf("path=%q 不该给出任何提示，实际 %+v", raw, got.References)
		}
	}
}

// TestResolveDirRefWithoutServices 服务未初始化（单测里、或某功能被裁掉）
// 时接口仍然可用，只是没有判据。
func TestResolveDirRefWithoutServices(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/admin/dir-refs?path=/media/影视", nil)
	rec := httptest.NewRecorder()
	h.resolveDirRef(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码=%d", rec.Code)
	}
	var got dirRefResp
	decodeEnvelope(t, rec.Body.Bytes(), &got)
	if got.Configured {
		t.Errorf("两个服务都没接时 Configured 应为 false，实际 %+v", got)
	}
	if len(got.References.Hints) != 0 {
		t.Errorf("没有配置依据时不该有任何提示，实际 %+v", got.References.Hints)
	}
	if len(got.Refs.LibraryRoots) != 0 || got.Refs.LibraryRoots == nil {
		t.Errorf("空切片应序列化为 [] 而不是 null，实际 %#v", got.Refs.LibraryRoots)
	}
}

// TestLibraryRootsFromReadsBothRuleRowsAndGlobalFallback 只读规则行的话，
// 用户在规则页改完库根，提示还按旧的全局值判 —— 静默误导。
// 只读全局设置的话，规则页的改动完全不被看见。
//
// ⚠️ 这条用例原先只测了 rootCollector（归一+去重），名字却写着
// libraryRootsFrom —— 变异时才发现：把 GlobalRule() 那一段整个摘掉，
// 它照样绿。名字里承诺的东西必须真的被测到，否则它是一张假通行证。
func TestLibraryRootsFromReadsBothRuleRowsAndGlobalFallback(t *testing.T) {
	// 两条来源各给一个不同的根，两条都必须出现在结果里。
	svc := newTestMediaUpgrade(t, []string{"/media/规则行"}, "/media/全局兜底")
	roots, notes := libraryRootsFrom(context.Background(), svc)

	if !containsString(roots, "/media/规则行") {
		t.Errorf("媒体库根里没有规则行配置的 %s，实际 %v —— "+
			"界面改的是规则行，只读全局值等于界面改了没有任何效果", "/media/规则行", roots)
	}
	if !containsString(roots, "/media/全局兜底") {
		t.Errorf("媒体库根里没有全局兜底设置 %s，实际 %v —— "+
			"没有规则的用户（只有默认设置）会得到空判据，提示永远不出现",
			"/media/全局兜底", roots)
	}
	if len(notes) != 0 {
		t.Errorf("两个来源都读成功时不该有边界说明，实际 %v", notes)
	}
}

// TestRootCollectorNormalizesAndDedupes rootCollector 自己的口径：
// 归一、去重、全空返回 nil（调用方靠 nil 判「没配过」）。
func TestRootCollectorNormalizesAndDedupes(t *testing.T) {
	g := newRootCollector()
	g.add("/media/global")
	g.add("/media/global/")
	g.add("  ")
	out := g.list()
	if len(out) != 1 || out[0] != "/media/global" {
		t.Errorf("归一去重=%v，期望 [/media/global]", out)
	}

	// nil / 全空输入必须返回 nil 而不是空切片：调用方用它判「没配」。
	if newRootCollector().list() != nil {
		t.Errorf("空收集器应返回 nil")
	}
	g2 := newRootCollector()
	g2.add("")
	g2.add("   ")
	g2.add("\n")
	if g2.list() != nil {
		t.Errorf("全是空值时应返回 nil，实际 %v", g2.list())
	}
}

// TestResolveDirRefUsesInjectedServiceEndToEnd 走真实 mediaupgrade/automation
// 服务（内存 sqlite），确认库根与源目录真的从库里读到了，
// 而不是被写死在 handler 里。
func TestResolveDirRefUsesInjectedServiceEndToEnd(t *testing.T) {
	mu := newTestMediaUpgrade(t, []string{"/media/影视"}, "")
	au := newTestAutomation(t, []automation.RuleView{casRule(1, "CAS", "/watch/cas")})
	h := &Handler{mediaUpgrade: mu, automation: au}

	cases := []struct {
		path           string
		inLibrary      bool
		isMonitor      bool
		wantHintSubstr string
	}{
		{"/media/影视/某剧", true, false, "不是自动整理的源目录"},
		{"/watch/cas/某剧", false, true, "不在任何媒体库内"},
		{"/downloads/某剧", false, false, "不在任何媒体库内"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/admin/dir-refs?path="+urlQueryEscape(tc.path), nil)
		rec := httptest.NewRecorder()
		h.resolveDirRef(rec, req)
		var got dirRefResp
		decodeEnvelope(t, rec.Body.Bytes(), &got)
		if got.References.InLibrary != tc.inLibrary {
			t.Errorf("%s: InLibrary=%v 期望 %v", tc.path, got.References.InLibrary, tc.inLibrary)
		}
		if got.References.IsMonitorSource != tc.isMonitor {
			t.Errorf("%s: IsMonitorSource=%v 期望 %v", tc.path, got.References.IsMonitorSource, tc.isMonitor)
		}
		if !strings.Contains(got.References.Hint, tc.wantHintSubstr) {
			t.Errorf("%s: Hint=%q，期望包含 %q", tc.path, got.References.Hint, tc.wantHintSubstr)
		}
	}
}

// TestCollectDirRefsAlwaysStatesItsBoundaries 前端要能向用户解释
// 「这个提示覆盖了什么、没覆盖什么」，所以边界说明不能因为
// 「暂时都读到了」就省掉 —— 恰恰是读到了才更要说明盲区在哪。
func TestCollectDirRefsAlwaysStatesItsBoundaries(t *testing.T) {
	h := &Handler{
		mediaUpgrade: newTestMediaUpgrade(t, []string{"/media/影视"}, ""),
		automation:   newTestAutomation(t, []automation.RuleView{casRule(1, "CAS", "/watch/cas")}),
	}
	refs, notes := h.collectDirRefs(context.Background())
	if len(refs.LibraryRoots) != 1 || refs.LibraryRoots[0] != "/media/影视" {
		t.Errorf("库根=%v", refs.LibraryRoots)
	}
	if len(refs.MonitorSources) != 1 || refs.MonitorSources[0] != "/watch/cas" {
		t.Errorf("源目录=%v", refs.MonitorSources)
	}
	if len(refs.EmbyLocations) != 0 {
		t.Errorf("EmbyLocations 暂无权威快照，应为空，实际 %v", refs.EmbyLocations)
	}
	if !containsNote(notes, "Emby") || !containsNote(notes, "source_dir_id") {
		t.Errorf("边界说明缺失：%v", notes)
	}

	// 没有服务时也要给出边界（前端会展示），只是判据为空。
	empty, notes2 := (&Handler{}).collectDirRefs(context.Background())
	if len(empty.LibraryRoots) != 0 || len(empty.MonitorSources) != 0 {
		t.Errorf("无服务时判据应为空：%+v", empty)
	}
	if len(notes2) != 2 {
		t.Errorf("无服务时仍应给出两条边界说明，实际 %d 条：%v", len(notes2), notes2)
	}
}

// TestDirRefRespSerializesHintTextAsString 用户界面直接展示 References.Hint，
// 所以它必须是字符串；Vyo 那种结构体里混字段的形态在 Go 里会变成
// "map[...]" 这样的字符串，前端只能原样显示给用户。
func TestDirRefRespSerializesHintTextAsString(t *testing.T) {
	refs := cloudref.DirRefs{LibraryRoots: []string{"/media/影视"}}
	resp := dirRefResp{
		Path:       "/downloads/x",
		References: cloudref.ResolveDirReferences(context.Background(), "/downloads/x", refs),
		Configured: true,
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		References struct {
			Hint      string `json:"Hint"`
			InLibrary bool   `json:"InLibrary"`
			Hints     []struct {
				Text string `json:"Text"`
				Tone string `json:"Tone"`
			} `json:"Hints"`
		} `json:"References"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.References.Hint == "" || probe.References.InLibrary {
		t.Errorf("序列化异常：%s", raw)
	}
	if len(probe.References.Hints) != 1 || probe.References.Hints[0].Tone != "warn" {
		t.Errorf("Hints 序列化异常：%s", raw)
	}
	if !strings.Contains(string(raw), "\"InLibrary\"") {
		t.Errorf("字段名应是 Go 原样（前端不做字段映射），实际 %s", raw)
	}
}

func containsNote(notes []string, substr string) bool {
	for _, n := range notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

// newTestMediaUpgrade 用真实迁移建表，再构造洗版服务并按给定库根建规则行。
//
// 走真实仓储（不是塞一个假 service）是因为这个接口的整个价值就建立在
// 「读的是用户真的配过的目录」之上：假 service 能让 handler 编译通过，
// 但仓储接线哪天断了不会有任何症状，提示会安静地变成「一条空配置」，
// 也就是界面上什么都不显示 —— 正是这个功能要消灭的那类静默。
//
// 库用 gorm 而不是 store.DB：洗版表的访问路径走 gorm（见
// internal/app/wire_mediaupgrade.go 的注释），迁移也照 mediaupgrade 自己
// 的测试脚手架跑 —— 跑 AutoMigrate 的话，迁移与模型不一致时测试是绿的、
// 线上是炸的。
func newTestMediaUpgrade(t *testing.T, roots []string, global string) *mediaupgrade.Service {
	t.Helper()
	ctx := context.Background()
	db := openDirRefGorm(t)

	svc := mediaupgrade.New(db, stubMediaUpgradeSettings{global: global})
	for i, root := range roots {
		// LoserAction 必须给值：规则校验把空串直接判死（mediaupgrade/rules.go:98-99），
		// 这里给 keep，因为它只是让这条规则合法入库，与本用例要验证的判据无关。
		if _, err := svc.SaveRule(ctx, &mediaupgrade.Rule{
			Name:        "rule" + strconv.Itoa(i),
			Source:      mediaupgrade.SourceLocal,
			LibraryRoot: root,
			LoserAction: mediaupgrade.LoserActionKeep,
			Enabled:     true,
		}); err != nil {
			t.Fatalf("写洗版规则失败: %v", err)
		}
	}
	return svc
}

// openDirRefGorm 建一个跑完真实迁移的 gorm 句柄。
func openDirRefGorm(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "dir_ref_test.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(on)&_pragma=synchronous(NORMAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	// 媒体库根来自洗版规则、监控源来自自动化规则，两张表缺哪张都会让
	// 「接口返回空判据」与「用户真的没配过」变得没法区分。
	// 0013 是自动化规则的建表迁移；0046 是 0037 的后续 ALTER
	// （给 media_upgrade_rules 加 category_scope），少跑它会在第一条
	// 涉及规则行的用例上炸 "no column named category_scope"，
	// 而根因其实是脚手架少跑了一个文件，看起来像新列写错了。
	for _, name := range []string{
		"0037_media_upgrade.sql",
		"0046_upgrade_category_scope.sql",
		"0013_automation.sql",
	} {
		runMigrationFile(t, db, filepath.Join("..", "store", "migrations", name))
	}
	return db
}

// runMigrationFile 裸按分号执行一个迁移文件（与 store.splitStatements 同口径）。
func runMigrationFile(t *testing.T, db *gorm.DB, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取迁移 %s 失败: %v", path, err)
	}
	for _, stmt := range strings.Split(string(raw), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("执行 %s 片段失败: %v\n片段: %s", path, err, stmt)
		}
	}
}

// newTestAutomation 用内存仓储构造自动化服务并写入触发器规则。
//
// 这里走 store.DB 而不是 gorm：自动化的仓储只要 domain 接口
// （store.New(db *store.DB)），而 store.Open(Memory:true) 直接跑全部迁移，
// 省掉手工挑迁移文件时漏文件的风险。洗版反过来只能走 gorm。
func newTestAutomation(t *testing.T, views []automation.RuleView) *automation.Service {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}

	st := store.New(db)
	svc := automation.New(automation.Options{Rules: st.AutomationRules, Runs: st.AutomationRuns})
	for _, v := range views {
		in := automation.RuleInput{
			Name:          v.Name,
			TriggerType:   v.TriggerType,
			TriggerConfig: v.TriggerConfig,
			Actions:       v.Actions,
			Status:        v.Status,
		}
		// 规则必须带至少一个执行动作（service_validate.go:265-266）。
		// 给 notify：它是唯一不查任何外部服务、不牵出「整理/STRM 任务存在性」
		// 校验的动作（service_validate.go:116-126），换别的动作都得先造一整套
		// 假的整理任务或 Emby 配置，本用例要验的判据跟它们没关系。
		if len(in.Actions) == 0 {
			in.Actions = []automation.RuleAction{{
				ID:   "a1",
				Type: domain.AutomationActionNotify,
				Name: "发通知",
			}}
		}
		// 离线下载触发器强制要求账号 ID（见 automation 的 normalizeInput），
		// 不补上的话造不出「离线监控目录」这种源目录配置。
		if v.TriggerType == domain.AutomationTriggerOfflineDownload {
			cfg := map[string]any{"account_id": 1}
			for k, val := range in.TriggerConfig {
				cfg[k] = val
			}
			in.TriggerConfig = cfg
		}
		if _, err := svc.CreateRule(ctx, in); err != nil {
			t.Fatalf("写自动化规则失败(%s): %v", v.Name, err)
		}
	}
	return svc
}

// stubMediaUpgradeSettings 是 settings.Service 的最小替身：
// 只按 global 返回媒体库根，其余键返回空。
type stubMediaUpgradeSettings struct {
	global string
}

func (s stubMediaUpgradeSettings) String(key string) string {
	if key == settings.KeyMOMediaUpgradeLibraryRoot {
		return s.global
	}
	return ""
}

func (s stubMediaUpgradeSettings) Int(string) int   { return 0 }
func (s stubMediaUpgradeSettings) Bool(string) bool { return false }

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

func decodeEnvelope(t *testing.T, raw []byte, dst any) {
	t.Helper()
	var resp struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("解析响应: %v, body = %s", err, raw)
	}
	if !resp.Success {
		t.Fatalf("期望 success=true, body = %s", raw)
	}
	if err := json.Unmarshal(resp.Data, dst); err != nil {
		t.Fatalf("解析 data: %v, body = %s", err, raw)
	}
}
