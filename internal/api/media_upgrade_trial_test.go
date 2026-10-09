package api

import (
	"bytes"
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"litepan/internal/mediaupgrade"
)

// T31 · 规则试算端点。
//
// 试算是用户配规则时的唯一依据（「我这份规则到底会怎么判」），
// 所以它的三个性质必须由端点级用例钉住：不落库、与真实判定同源、
// 输入不完整时明确报错而不是猜。

const (
	trialOldName = "剧名.S01E01.2160p.x265.10bit.mkv"
	trialNewName = "剧名.S01E01.2160p.x265.TrueHD.中字.mkv"
)

type trialHarness struct {
	h  *Handler
	db *gorm.DB
	id uint
}

func newTrialHarness(t *testing.T) trialHarness {
	t.Helper()
	ctx := context.Background()
	db := openDirRefGorm(t)
	// 0048 给 media_upgrade_records 补了 reject_reasons/rule_fingerprint。
	// 端点不写这两列，但「不落库」那条断言要真的去查那张表；表结构不对的话
	// COUNT(*) 自己就报错，用例看起来像通过，其实根本没查。
	runMigrationFile(t, db, filepath.Join("..", "store", "migrations", "0048_wash_rejection_reasons.sql"))

	svc := mediaupgrade.New(db, stubMediaUpgradeSettings{})
	// LoserAction 必填：规则校验把空串直接判死（mediaupgrade/rules.go），
	// keep 足以让这条规则合法入库，与本组用例要验证的性质无关。
	saved, err := svc.SaveRule(ctx, &mediaupgrade.Rule{
		Name:        "试算规则",
		Source:      mediaupgrade.SourceLocal,
		LibraryRoot: "/media",
		WashRules:   `[{"field":"bitdepth","higher":false}]`,
		LoserAction: mediaupgrade.LoserActionKeep,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("准备试算规则失败: %v", err)
	}
	return trialHarness{h: &Handler{mediaUpgrade: svc}, db: db, id: saved.ID}
}

func (h trialHarness) post(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求体失败: %v", err)
	}
	// 挂真路由而不是直接调 handler：端点的存在本身就是可测的一部分，
	// 直接调 handler 会跳过「路由到底注册了没有」这一问。
	r := chi.NewRouter()
	r.Route("/admin/media-upgrade", func(r chi.Router) {
		r.Post("/rule-trial", h.h.mediaUpgradeRuleTrial)
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/media-upgrade/rule-trial", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func (h trialHarness) trial(t *testing.T, body map[string]any) mediaupgrade.TrialResult {
	t.Helper()
	rec := h.post(t, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("试算失败 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Item mediaupgrade.TrialResult `json:"item"`
	}
	decodeEnvelope(t, rec.Body.Bytes(), &env)
	return env.Item
}

func (h trialHarness) recordCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := h.db.Table("media_upgrade_records").Count(&n).Error; err != nil {
		t.Fatalf("统计判定记录失败: %v", err)
	}
	return n
}

func trialBody(ruleID uint) map[string]any {
	return map[string]any{
		"rule_id":      ruleID,
		"new_name":     trialNewName,
		"new_size":     12 << 30,
		"has_new_size": true,
		"old_name":     trialOldName,
		"old_size":     4 << 30,
		"has_old_size": true,
	}
}

// TestMediaUpgradeRuleTrialDoesNotWriteAnything 验收 ④：试算不落库。
//
// 「不落库」不只是「不新建判定记录」这一层。用户可能对着同一条规则
// 试算几十次，每次都写一条的话，「洗版记录」列表会被纯预览操作淹没，
// 而那份列表正是用户判断「上次扫描都干了什么」的依据。
func TestMediaUpgradeRuleTrialDoesNotWriteAnything(t *testing.T) {
	hs := newTrialHarness(t)
	before := hs.recordCount(t)

	for i := 0; i < 5; i++ {
		if hs.trial(t, trialBody(hs.id)).Relation == "" {
			t.Fatal("试算没有给出结论")
		}
	}
	if after := hs.recordCount(t); after != before {
		t.Fatalf("试算写入了 %d 条判定记录（%d → %d）", after-before, before, after)
	}

	// 规则行也不能被动：试算若顺手把草稿规则存进去，
	// 用户会发现「我只是试了一下，它把我原来那条规则覆盖了」。
	var rulesAfter int64
	if err := hs.db.Table("media_upgrade_rules").Count(&rulesAfter).Error; err != nil {
		t.Fatalf("统计规则行失败: %v", err)
	}
	if rulesAfter != 1 {
		t.Fatalf("试算之后规则行数 = %d，期望仍是 1", rulesAfter)
	}
}

// TestMediaUpgradeRuleTrialAcceptsInlineRule 前端能试算**还没保存的草稿**。
//
// 这是端点接受 rule 本体的理由：用户刚改完规则就想知道结果，
// 逼他先保存再回来是多余的一步，而且保存本身有副作用。
func TestMediaUpgradeRuleTrialAcceptsInlineRule(t *testing.T) {
	hs := newTrialHarness(t)
	res := hs.trial(t, map[string]any{
		"rule": map[string]any{
			"name":           "草稿",
			"source":         "local",
			"library_root":   "/media",
			"min_channels":   8,
			"loser_action":   "delete",
			"enabled":        true,
			"wash_rules":     `[{"field":"bitdepth","higher":false}]`,
			"group_priority": "FRDS",
		},
		"new_name": trialNewName, "new_size": 12 << 30, "has_new_size": true,
		"old_name": trialOldName, "old_size": 4 << 30, "has_old_size": true,
	})
	if res.RuleFingerprint == "" {
		t.Fatal("试算结果没有带规则指纹")
	}
	// min_channels=8，而新文件是真 HD 8 声道 ⇒ 门槛应当被满足。
	if len(res.GateReasons) != 0 {
		t.Fatalf("8 声道文件不该有门槛理由：%+v", res.GateReasons)
	}
	if hs.recordCount(t) != 0 {
		t.Fatal("草稿规则试算不该产生判定记录")
	}
}

// TestMediaUpgradeRuleTrialRequiresRuleOrRuleID 什么都没给必须明确报错。
//
// 兜底回落全局设置看起来更「贴心」，但那会让「我明明什么规则都没选」
// 变成一段来自用户看不见的规则集的结果 —— 试算的全部价值就是输入可见。
func TestMediaUpgradeRuleTrialRequiresRuleOrRuleID(t *testing.T) {
	hs := newTrialHarness(t)
	rec := hs.post(t, map[string]any{"new_name": trialNewName, "old_name": trialOldName})
	if rec.Code == http.StatusOK {
		t.Fatalf("既没给 rule 也没给 rule_id 却成功了：%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rule") {
		t.Fatalf("报错没有说清缺什么：%s", rec.Body.String())
	}
}

// TestMediaUpgradeRuleTrialRejectsBrokenWashRules 坏掉的洗版维度规则必须拦下。
//
// 静默回落默认规则更糟：用户会看到一份「自定义维度」的判定结果，
// 而界面上完全看不出系统用的是默认维度。
func TestMediaUpgradeRuleTrialRejectsBrokenWashRules(t *testing.T) {
	hs := newTrialHarness(t)
	rec := hs.post(t, map[string]any{
		"rule": map[string]any{
			"name":         "坏规则",
			"source":       "local",
			"library_root": "/media",
			"wash_rules":   `{"这不是数组"`,
			"loser_action": "delete",
			"enabled":      true,
		},
		"new_name": trialNewName,
		"old_name": trialOldName,
	})
	if rec.Code == http.StatusOK {
		t.Fatalf("wash_rules 解析失败却仍然给了结论：%s", rec.Body.String())
	}
}

// TestMediaUpgradeRuleTrialRequiresBothFileNames 两边文件名都必须有。
func TestMediaUpgradeRuleTrialRequiresBothFileNames(t *testing.T) {
	hs := newTrialHarness(t)
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"缺新文件", map[string]any{"rule_id": hs.id, "old_name": trialOldName}},
		{"缺现版", map[string]any{"rule_id": hs.id, "new_name": trialNewName}},
		{"只有空白", map[string]any{"rule_id": hs.id, "new_name": "   ", "old_name": trialOldName}},
	} {
		if rec := hs.post(t, tc.body); rec.Code == http.StatusOK {
			t.Errorf("%s：却成功了：%s", tc.name, rec.Body.String())
		}
	}
}

// TestMediaUpgradeRuleTrialMatchesStoredRule 给 rule_id 时必须真的读那一行。
//
// 端点若改成「忽略 rule_id 直接用全局规则」，上面几条用例全都还会绿
// （它们只断言成功与失败），所以必须有一条断言「读的是被点名的那一行」。
func TestMediaUpgradeRuleTrialMatchesStoredRule(t *testing.T) {
	hs := newTrialHarness(t)
	other, err := hs.h.mediaUpgrade.SaveRule(context.Background(), &mediaupgrade.Rule{
		Name:        "另一条",
		Source:      mediaupgrade.SourceLocal,
		LibraryRoot: "/media",
		MinChannels: 99, // 故意不可能满足
		LoserAction: mediaupgrade.LoserActionKeep,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("写第二条规则失败: %v", err)
	}
	if other.ID == hs.id {
		t.Fatal("两条规则 ID 相同，测试前提不成立")
	}

	first := hs.trial(t, trialBody(hs.id))
	second := hs.trial(t, trialBody(other.ID))

	if len(first.GateReasons) != 0 {
		t.Fatalf("第一条规则（无门槛）不该有门槛理由：%+v", first.GateReasons)
	}
	if len(second.GateReasons) != 1 {
		t.Fatalf("第二条规则（min_channels=99）应当给出 1 条门槛理由，实际 %+v", second.GateReasons)
	}
	if second.GateReasons[0].Code != mediaupgrade.RejectBelowMinChannels {
		t.Fatalf("门槛 code = %q，期望 %q", second.GateReasons[0].Code, mediaupgrade.RejectBelowMinChannels)
	}
	if first.RuleFingerprint == second.RuleFingerprint {
		t.Fatal("两条规则指纹相同 —— 指纹没把有效字段算进去")
	}
}

// TestMediaUpgradeRuleTrialWithoutServiceAnswers 洗版服务没装配时必须明确说没就绪。
//
// 与其它 media-upgrade 端点同口径：nil 服务下返回 200 空 JSON 的话，
// 前端会把「服务没起」显示成「试算结果是空」。
func TestMediaUpgradeRuleTrialWithoutServiceAnswers(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/media-upgrade/rule-trial",
		strings.NewReader(`{"new_name":"a","old_name":"b"}`))
	(&Handler{}).mediaUpgradeRuleTrial(rec, req)
	// mediaUpgradeReady 统一走 writeNotImplemented ⇒ 501（与其它洗版端点同口径）。
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("服务未就绪时返回 %d，期望 501：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "不支持") {
		t.Fatalf("响应没有说清是服务没就绪：%s", rec.Body.String())
	}
}

// TestWiringMediaUpgradeRuleTrialDelegatesVerdict 接线断言：端点真的调了
// mediaupgrade.TrialVerdict，而不是自己重写一份比较逻辑。
//
// T02 的预览端点就死在这：预览端点自己比较了一份，于是「预览说会洗」
// 而真实执行不洗，用户再也不信预览。试算是同一个形状的功能，
// 而且它的全部价值就是「和真实一致」，所以这条断言是必须的。
func TestWiringMediaUpgradeRuleTrialDelegatesVerdict(t *testing.T) {
	t.Run("委托给 TrialVerdict", func(t *testing.T) {
		if !mentionsSelector(t, "media_upgrade.go", "TrialVerdict") {
			t.Fatal("media_upgrade.go 没有引用 mediaupgrade.TrialVerdict —— " +
				"端点必须把判定完全委托出去")
		}
	})
	t.Run("端点不得自行比较", func(t *testing.T) {
		// compareOne 是 mediaupgrade 的包内函数，端点那个包里根本没有它；
		// 一旦真写进去，编译就断了。这条断言是留给「有人把比较逻辑抄一份
		// 到别的包里」的：那时 compareOne 的名字会跟着出现。
		for _, name := range []string{"QualityDimensions", "TraceFromDimensions", "CompareQuality"} {
			if mentionsSelector(t, "media_upgrade.go", name) {
				t.Errorf("media_upgrade.go 里出现了 %s —— 试算端点不得自行比较维度", name)
			}
		}
	})
	t.Run("规则指纹由 RuleSet 提供", func(t *testing.T) {
		// 指纹是判定的产物之一，端点自己拼一个就会和真实扫描对不上。
		if !mentionsSelector(t, "media_upgrade.go", "ValidateForTrial") {
			t.Fatal("media_upgrade.go 没有引用 ValidateForTrial —— " +
				"试算若跳过校验就会用一份真实扫描不会接受的规则给出结论")
		}
	})
}

// TestWiringMediaUpgradeRuleTrialRouteReachable 路由可达性（验收 ⑦）。
//
// 正反双向：router.go 少了注册 → 红；注册了但挂错子树（比如挂到 /admin 之外
// 或落到 media-organize 那边）→ 同样红。
func TestWiringMediaUpgradeRuleTrialRouteReachable(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 router.go 失败：%v", err)
	}
	// collectRoutes 只扫闭包顶层的语句，而 /media-upgrade 这棵子树是嵌在
	// 更深的 Route 里的，直接对 /admin 闭包跑会漏掉它（第一版就是这么写的，
	// 结果永远找不到这条路由，守卫自己成了永远为真的摆设）。所以先把
	// media-upgrade 那个闭包摘出来，再在它内部对账。
	body := findRouteFuncLit(file, "/media-upgrade")
	if body == nil {
		t.Fatal("router.go 里找不到 r.Route(\"/media-upgrade\", …)")
	}
	var all []string
	collectRoutes(body, "/api/admin/media-upgrade", &all)
	got := ""
	for _, p := range all {
		if sp := strings.SplitN(p, " ", 2); len(sp) == 2 && sp[1] == "/api/admin/media-upgrade/rule-trial" {
			got = sp[0] + " " + sp[1]
		}
	}
	if got != http.MethodPost+" /api/admin/media-upgrade/rule-trial" {
		t.Fatalf("rule-trial 注册成了 %q，期望 POST /api/admin/media-upgrade/rule-trial —— "+
			"挂错方法或挂错子树前端就是 404", got)
	}
}
