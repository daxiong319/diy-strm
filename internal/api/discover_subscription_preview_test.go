package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 视觉过滤预览接口的 HTTP 契约测试：
// 只发 JSON、只看 JSON，确保前端拿到的字段名/原因串与约定一致。
// （判定语义本身在 internal/discover/discovery/rule_match_test.go 覆盖）

func postPreview(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/discovery/subscriptions/preview",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	(&Handler{}).subscriptionPreviewMatch(rec, req)
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（原文 %s）", err, rec.Body.String())
	}
	return rec.Code, payload
}

func previewData(t *testing.T, body string) map[string]any {
	t.Helper()
	code, payload := postPreview(t, body)
	if code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d，期望 200", code)
	}
	if ok, _ := payload["success"].(bool); !ok {
		t.Fatalf("success 应为 true，实际响应 %#v", payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		t.Fatalf("响应缺少 data：%#v", payload)
	}
	return data
}

// previewItems 取出 data.items
func previewItems(t *testing.T, data map[string]any) []map[string]any {
	t.Helper()
	raw, _ := data["items"].([]any)
	// items 为空切片时 JSON 里是 []，这里返回 nil 也能被调用方正确判定
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestSubscriptionPreviewMatchEndpointReportsVerdicts(t *testing.T) {
	// 端到端：三条词表同时生效，逐条给出 blocked + 生产同款中文原因
	data := previewData(t, `{
		"message_keywords": ["庆余年"],
		"must_contain": ["4K"],
		"must_not_contain": ["预告"],
		"titles": [
			{"title": "庆余年 第二季 4K"},
			{"title": "庆余年 第二季 4K 预告"},
			{"title": "庆余年 第二季 1080P"},
			{"title": "狂飙 全集"}
		]
	}`)

	if active, _ := data["active"].(bool); !active {
		t.Error("active 应为 true")
	}
	if total, _ := data["total"].(float64); total != 4 {
		t.Errorf("total = %v，期望 4", data["total"])
	}
	if passed, _ := data["passed_count"].(float64); passed != 1 {
		t.Errorf("passed_count = %v，期望 1", data["passed_count"])
	}
	if blocked, _ := data["blocked_count"].(float64); blocked != 3 {
		t.Errorf("blocked_count = %v，期望 3", data["blocked_count"])
	}

	items := previewItems(t, data)
	if len(items) != 4 {
		t.Fatalf("items 长度 = %d，期望 4", len(items))
	}
	// 顺序与提交顺序一致（前端按行对齐粘贴的标题）
	if got, _ := items[0]["blocked"].(bool); got {
		t.Error("第 1 条应放行")
	}
	if reason, _ := items[0]["reason"].(string); reason != "" {
		t.Errorf("放行条目 reason 应为空串，实际 %q", reason)
	}
	cases := []struct {
		idx    int
		reason string
	}{
		{1, "标题正文命中排除词：预告"},
		{2, "标题正文未包含：4K"},
		{3, "未命中消息正文关键词"},
	}
	for _, tc := range cases {
		if blocked, _ := items[tc.idx]["blocked"].(bool); !blocked {
			t.Errorf("第 %d 条应被拦截", tc.idx+1)
		}
		if reason, _ := items[tc.idx]["reason"].(string); reason != tc.reason {
			t.Errorf("第 %d 条 reason = %q，期望 %q", tc.idx+1, reason, tc.reason)
		}
	}
	// match_text 用小写回显实际比对文本（标题 + "\n" + 备注，备注为空时末尾是换行）
	if mt, _ := items[0]["match_text"].(string); mt != "庆余年 第二季 4k\n" {
		t.Errorf("match_text = %q，期望 %q", mt, "庆余年 第二季 4k\n")
	}
}

func TestSubscriptionPreviewMatchEndpointEmptyListsPassAll(t *testing.T) {
	data := previewData(t, `{"titles": [{"title": "任意标题"}]}`)
	if active, _ := data["active"].(bool); active {
		t.Error("空词表时 active 应为 false")
	}
	if blocked, _ := data["blocked_count"].(float64); blocked != 0 {
		t.Errorf("空词表应全部放行，blocked_count = %v", data["blocked_count"])
	}
	if passed, _ := data["passed_count"].(float64); passed != 1 {
		t.Errorf("passed_count = %v，期望 1", data["passed_count"])
	}
}

func TestSubscriptionPreviewMatchEndpointRemarkIncludedInMatch(t *testing.T) {
	// 备注参与匹配（生产 matchText = 标题 + "\n" + 备注）
	data := previewData(t, `{
		"must_not_contain": ["失效"],
		"titles": [{"title": "某剧 4K", "remark": "链接已失效"}]
	}`)
	items := previewItems(t, data)
	if len(items) != 1 {
		t.Fatalf("items 长度 = %d，期望 1", len(items))
	}
	if blocked, _ := items[0]["blocked"].(bool); !blocked {
		t.Error("备注命中排除词时应拦截")
	}
	if mt, _ := items[0]["match_text"].(string); !strings.Contains(mt, "链接已失效") {
		t.Errorf("match_text 应包含备注，实际 %q", mt)
	}
}

func TestSubscriptionPreviewMatchEndpointTrimsAndSkipsBlankTitles(t *testing.T) {
	data := previewData(t, `{
		"message_keywords": ["庆余年"],
		"titles": [{"title": "  "}, {"title": "  庆余年 4K  "}]
	}`)
	if total, _ := data["total"].(float64); total != 1 {
		t.Fatalf("空行应被跳过，total = %v", data["total"])
	}
	items := previewItems(t, data)
	if len(items) != 1 {
		t.Fatalf("items 长度 = %d，期望 1", len(items))
	}
	if title, _ := items[0]["title"].(string); title != "庆余年 4K" {
		t.Errorf("标题应 TrimSpace，实际 %q", title)
	}
	if blocked, _ := items[0]["blocked"].(bool); blocked {
		t.Error("trim 后应命中关键词并放行")
	}
}

func TestSubscriptionPreviewMatchEndpointNoTitles(t *testing.T) {
	// 只给词表不给标题：返回空结果而不是报错，前端首次渲染不应炸
	data := previewData(t, `{"message_keywords": ["庆余年"]}`)
	if total, _ := data["total"].(float64); total != 0 {
		t.Errorf("total = %v，期望 0", data["total"])
	}
	if items := previewItems(t, data); len(items) != 0 {
		t.Errorf("items 应为空，实际 %d 条", len(items))
	}
}

func TestSubscriptionPreviewMatchEndpointRejectsMalformedJSON(t *testing.T) {
	code, payload := postPreview(t, `{"message_keywords": `)
	if code == http.StatusOK {
		t.Error("非法 JSON 不应返回 200")
	}
	if ok, _ := payload["success"].(bool); ok {
		t.Error("非法 JSON 不应返回 success=true")
	}
}
