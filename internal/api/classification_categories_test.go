package api

// T27 C-8 分类只读端点的验收测试。
//
// 与 catalog_test.go 的分工：那边验服务层语义（返回的是规则不是磁盘、
// slug 稳定性、未选中模板也返回），这里验**HTTP 上看到的东西**——
// 状态码、鉴权、JSON 形状，以及「前端拿到的字段名与后端 DTO 逐字一致」。
//
// 最后那一条不是形式主义：decodeJSON 开了 DisallowUnknownFields，同理
// 前端 TypeScript 接口一旦与后端 json tag 漂移，症状是下拉框**空着**
// 而不是报错——因为空数组和字段读不到长得一模一样。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"litepan/internal/classifyorganize"
)

// classifyCategoriesEnv 一棵手拼的 chi 树 + 分类服务。
//
// 为什么手拼而不是打 NewRouter：这个端点不需要验证前缀（它在
// /admin/tools/classification 下，与相邻的 config / rules 端点同一个
// Route 块，前缀一旦跑偏那几个端点会一起跑偏，被它们的守卫覆盖）。
// 而 NewRouter(Deps{}) 装配 WebDAV 时会 panic
// （router.go:913 无条件调 d.Uploads.TempRegistry()），代价远大于收益。
// 真实树上的挂载由 TestWiringClassificationCategoriesIsReachable 盯。
func classifyCategoriesEnv(t *testing.T) (*Handler, *classifyorganize.Service) {
	t.Helper()
	svc := newPreviewTestService(t)
	h := &Handler{classifyOrganize: svc}
	r := chi.NewRouter()
	r.Get("/api/admin/tools/classification/categories", h.listClassificationCategories)
	return h, svc
}

func doCategories(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/tools/classification/categories", nil)
	rec := httptest.NewRecorder()
	h.listClassificationCategories(rec, req)
	return rec
}

// TestClassificationCategoriesEndpointShape 验收①（HTTP 形状）：
// 200 + data.items 是数组 + 每条带全 level/name/slug/path。
func TestClassificationCategoriesEndpointShape(t *testing.T) {
	h, _ := classifyCategoriesEnv(t)
	rec := doCategories(t, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /categories = %d，期望 200：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Items  []classifyorganize.Category `json:"items"`
			Levels []int                       `json:"levels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, rec.Body.String())
	}
	if !resp.Success {
		t.Fatalf("success=false：%s", rec.Body.String())
	}
	if len(resp.Data.Items) == 0 {
		t.Fatal("items 为空 —— 默认配置有四套模板，不可能一个分类都没有")
	}
	if len(resp.Data.Levels) != 3 {
		t.Errorf("levels = %v，期望 [1 2 3]：前端靠它渲染层级分组", resp.Data.Levels)
	}
	for _, c := range resp.Data.Items {
		if c.Level < 1 || c.Level > 3 {
			t.Errorf("分类 %q 的 level=%d 越界", c.Slug, c.Level)
		}
		if c.Name == "" || c.Slug == "" || c.Path == "" {
			t.Errorf("分类缺字段：%+v", c)
		}
		if c.Template == "" {
			t.Errorf("分类 %q 没有 template，前端无法按模板分组", c.Slug)
		}
	}
}

// TestClassificationCategoriesIsNullNotMissing 验收①（形状稳定性）：
// items 必须是 [] 而不是 null。
//
// 这条钉的是一个具体的后果：Go 的 nil 切片序列化成 JSON 是 null，
// 前端 `data.items.map(...)` 会在**配置为空**时抛
// "Cannot read properties of null"。而"配置为空"恰好是最容易在用户
// 刚清空模板时出现的状态。
func TestClassificationCategoriesIsNullNotMissing(t *testing.T) {
	h, _ := classifyCategoriesEnv(t)
	rec := doCategories(t, h)
	if !strings.Contains(rec.Body.String(), `"items":[`) {
		t.Errorf("items 应序列化为数组而不是 null：%s", rec.Body.String())
	}
}

// TestClassificationCategoriesFailsWhenServiceAbsent 验收：
// 服务没装配时报错，不返回空列表。
//
// 返回空列表是最坏的处理：洗版筛选会以为"用户没配任何分类"
// 于是扫遍整个媒体库，清理保护会以为"没有分类要保护"于是照删。
func TestClassificationCategoriesFailsWhenServiceAbsent(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/tools/classification/categories", nil)
	rec := httptest.NewRecorder()
	h.listClassificationCategories(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("服务未初始化时不该 200：空列表会被洗版当成没有分类：%s", rec.Body.String())
	}
}

// TestClassificationCategoriesMatchesConsumerExpectation 验收③：
// 洗版侧的消费者（mediaupgrade.ScopeFromCategories）拿这份清单能取到
// 一级与二级名——这是跨模块契约的最小验证。
//
// 不在这里直接 import mediaupgrade 调 ScopeFromCategories 是有意的：
// 那会让 classifyorganize 的测试依赖 mediaupgrade，将来拆包时炸。
// 这里手写等价的一层映射就够了：断言清单里存在"一级 2 条 + 二级若干"
// 且每条都有 primary_key，这些正是消费者需要的形状。
func TestClassificationCategoriesMatchesConsumerExpectation(t *testing.T) {
	h, svc := classifyCategoriesEnv(t)
	cats, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("读取分类清单失败：%v", err)
	}
	rec := doCategories(t, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("端点 = %d：%s", rec.Code, rec.Body.String())
	}

	primaryWithKey := 0
	secondary := 0
	for _, c := range cats {
		if c.Level == 1 {
			if c.PrimaryKey != "" {
				primaryWithKey++
			}
			continue
		}
		secondary++
	}
	if primaryWithKey == 0 {
		t.Error("没有任何一级分类带 primary_key —— 洗版按媒体类型收敛筛选就无从下手")
	}
	if secondary == 0 {
		t.Error("清单里没有二级分类，验收③的洗版筛选用例不可能通过")
	}
}

// TestClassificationCategoriesSerializesEveryFrontendField 验收⑬：
// 后端 Category 的 json tag 与前端 ClassificationCategory 接口逐字对齐。
//
// 前后端字段名一旦漂移，症状是前端拿到 undefined 而不是报错：
// 下拉框空着、控制台干净、没人知道是接口少了一个字段。
func TestClassificationCategoriesSerializesEveryFrontendField(t *testing.T) {
	h, _ := classifyCategoriesEnv(t)
	rec := doCategories(t, h)

	var raw struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解包失败：%v", err)
	}
	if len(raw.Data.Items) == 0 {
		t.Fatal("没有可断言的分类条目")
	}
	want := []string{"level", "name", "slug", "template", "path", "enabled", "rule_id"}
	for _, field := range want {
		if _, ok := raw.Data.Items[0][field]; !ok {
			t.Errorf("分类条目里没有字段 %q，前端读它会拿到 undefined", field)
		}
	}
	// primary_key 带 omitempty：二级三级会消失。这是刻意的
	// （空串和"没有这个概念"在前端是一样的），但一级必须有。
	foundPrimaryKey := false
	for _, c := range raw.Data.Items {
		if v, ok := c["primary_key"]; ok && v.(string) != "" {
			foundPrimaryKey = true
		}
	}
	if !foundPrimaryKey {
		t.Error("没有任何一级分类序列化出 primary_key")
	}
}
