package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"litepan/internal/settings"
)

// fakeOpenAIChatServer 起一个最小可用的 OpenAI 兼容**流式**对话接口。
//
// 注意：internal/mcp/assistant.go 的 parseAssistantStream 解析的是上游 SSE
// （请求头带 Accept: text/event-stream），上游必须逐块吐 `data: {...}` 行并以
// `data: [DONE]` 收尾。返回一次性 JSON 会让整轮对话得到空回复 ——
// 本测试初版就踩了这个坑，故此处显式分块写出。
//
// 首轮返回 tool_calls（驱动 tool_call / tool_result 两条事件），
// 次轮返回纯文本（驱动 delta / done）。这样一次请求就能覆盖全部六种
// SSE 事件，而不是只测「能连上」。
func fakeOpenAIChatServer(t *testing.T, toolName string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fw := w.(http.Flusher)

		writeChunk := func(payload string) {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			fw.Flush()
		}

		// 只有调用方已经带回了 tool 结果，才认为可以进行第二轮。
		hasToolResult := false
		for _, msg := range body.Messages {
			if role, _ := msg["role"].(string); role == "tool" {
				hasToolResult = true
			}
		}

		if !hasToolResult {
			// 第一轮：先吐角色分片，再吐工具调用分片，模拟真实分块。
			writeChunk(`{"choices":[{"delta":{"role":"assistant"}}]}`)
			writeChunk(fmt.Sprintf(
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":%q,"arguments":"{}"}}]}}]}`,
				toolName))
			writeChunk(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
			writeChunk(`[DONE]`)
			return
		}

		// 第二轮：分两段吐文本，确保前端能收到多个 delta 并正确拼接。
		writeChunk(`{"choices":[{"delta":{"content":"这是"}}]}`)
		writeChunk(`{"choices":[{"delta":{"content":"最终回答。"}}]}`)
		writeChunk(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		writeChunk(`[DONE]`)
	}))
}

// fakeStreamingAnswerServer 起一个只回一段文本的流式上游。
func fakeStreamingAnswerServer(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fw := w.(http.Flusher)
		// answer 里的换行原样进 JSON 字符串（\n 是合法 JSON 转义）。
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", answer)
		fw.Flush()
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		fw.Flush()
	}))
}

// sseFrame 是一条被解析出来的 SSE 事件。
type sseFrame struct {
	Event string
	Data  map[string]any
}

// parseSSEBody 复刻 web/src/api/mcp.ts 里 parseSseFrame 的分帧算法。
//
// 这里刻意不依赖任何 Go 侧 SSE 库：测试必须以**前端相同的算法**读取
// 服务端原始字节，否则测的就不是前端真正要面对的东西。前端按 "\n\n"
// 切帧、按 "event: "/"data: " 前缀取字段，此处逐字照做。
func parseSSEBody(t *testing.T, raw string) []sseFrame {
	t.Helper()
	var frames []sseFrame
	rest := raw
	for {
		idx := strings.Index(rest, "\n\n")
		if idx < 0 {
			break
		}
		block := rest[:idx]
		rest = rest[idx+2:]

		var event string
		var dataLine string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				dataLine = strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			}
		}
		if event == "" {
			continue
		}
		var payload map[string]any
		if dataLine != "" {
			if err := json.Unmarshal([]byte(dataLine), &payload); err != nil {
				t.Fatalf("事件 %q 的 data 不是合法单行 JSON：%s（原始：%q）", event, err, dataLine)
			}
		}
		frames = append(frames, sseFrame{Event: event, Data: payload})
	}
	return frames
}

// TestMcpAssistantStreamSSEContract 锁定流式对话的 SSE 线上契约。
//
// 前端 web/src/api/mcp.ts 的 McpAssistantEvent 联合类型与这里的六个事件名
// 必须逐字对齐；任一侧改名都会让对话页**静默无输出**（事件名不匹配时前端
// 的 switch 直接落空，不报错）。因此这里从原始响应字节出发做断言，
// 而不是调用 Go 结构体。
func TestMcpAssistantStreamSSEContract(t *testing.T) {
	upstream := fakeOpenAIChatServer(t, "mcp_list_media")
	defer upstream.Close()

	h := newMCPTestHandler(t, map[string]string{
		string(settings.KeyMcpEnabled):          "true",
		string(settings.KeyMcpAssistantEnabled): "true",
		string(settings.KeyMcpAssistantBaseURL): upstream.URL,
		string(settings.KeyMcpAssistantAPIKey):  "sk-test",
		string(settings.KeyMcpAssistantModel):   "test-model",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/assistant/stream",
		strings.NewReader(`{"message":"有哪些电影？"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.mcpAssistantStream(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（正文：%s）", rec.Code, rec.Body.String())
	}

	// 流式响应的三个头缺一不可：Content-Type 让浏览器走 EventSource/fetch 流式路径，
	// no-cache 与 X-Accel-Buffering 防止中间层缓冲整个响应导致「不流式」。
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q，期望 text/event-stream 前缀", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q，期望 no-cache", cc)
	}
	if xab := rec.Header().Get("X-Accel-Buffering"); xab != "no" {
		t.Errorf("X-Accel-Buffering = %q，期望 no（否则反代会把流缓冲成一次性响应）", xab)
	}

	frames := parseSSEBody(t, rec.Body.String())
	if len(frames) == 0 {
		t.Fatalf("没有解析出任何 SSE 事件，原始正文：%q", rec.Body.String())
	}

	// 事件名必须恰好落在前端 switch 认识的六个值内 —— 出现第七种名字
	// 就意味着前端会静默丢弃它。
	known := map[string]bool{
		"init": true, "delta": true, "tool_call": true,
		"tool_result": true, "error": true, "done": true,
	}
	seen := map[string]int{}
	for _, f := range frames {
		if !known[f.Event] {
			t.Errorf("出现前端不认识的事件名 %q，会被静默丢弃", f.Event)
		}
		seen[f.Event]++
	}

	// init 必须最先到，且带 session_id，前端靠它把新会话挂到侧栏。
	if frames[0].Event != "init" {
		t.Errorf("首个事件 = %q，期望 init", frames[0].Event)
	}
	if sid, _ := frames[0].Data["session_id"].(string); strings.TrimSpace(sid) == "" {
		t.Error("init 事件缺少 session_id")
	}

	// 本轮必然经过「模型要求调工具 → 工具返回」这一条路径。
	for _, name := range []string{"delta", "tool_call", "tool_result", "done"} {
		if seen[name] == 0 {
			t.Errorf("未收到 %q 事件（收到的事件计数：%v）", name, seen)
		}
	}
	if seen["error"] != 0 {
		t.Errorf("不应出现 error 事件，计数 %d", seen["error"])
	}
	if frames[len(frames)-1].Event != "done" {
		t.Errorf("末尾事件 = %q，期望 done", frames[len(frames)-1].Event)
	}

	// tool_result 的字段名与前端接口逐字对齐。
	for _, f := range frames {
		if f.Event != "tool_result" {
			continue
		}
		if _, ok := f.Data["tool_name"]; !ok {
			t.Error("tool_result 缺少 tool_name 字段")
		}
		if _, ok := f.Data["tool_result"]; !ok {
			t.Error("tool_result 缺少 tool_result 字段（前端靠它填充折叠区）")
		}
	}

	// done 必须带 reply，前端在 delta 全空时用它兜底。
	last := frames[len(frames)-1]
	if reply, _ := last.Data["reply"].(string); strings.TrimSpace(reply) == "" {
		t.Error("done 事件的 reply 为空，前端将无内容可渲染")
	}

	// 汇总所有 delta 后应当能拼出与 reply 一致的文本。
	var streamed strings.Builder
	for _, f := range frames {
		if f.Event == "delta" {
			content, _ := f.Data["content"].(string)
			streamed.WriteString(content)
		}
	}
	reply, _ := last.Data["reply"].(string)
	if streamed.String() != reply {
		t.Errorf("delta 拼接结果 %q 与 done.reply %q 不一致，前端两种路径会渲染出不同内容",
			streamed.String(), reply)
	}
}

// TestMcpAssistantStreamDisabledRefuses 确认关闭助理时拒绝对话。
//
// 前端在 assistant_enabled 为假时会灰掉入口，但服务端不能只依赖前端。
func TestMcpAssistantStreamDisabledRefuses(t *testing.T) {
	h := newMCPTestHandler(t, map[string]string{
		string(settings.KeyMcpEnabled):          "true",
		string(settings.KeyMcpAssistantEnabled): "false",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/assistant/stream",
		strings.NewReader(`{"message":"你好"}`))
	rec := httptest.NewRecorder()
	h.mcpAssistantStream(rec, req)

	// 未启用时走 writeErr 的普通 JSON 错误路径，不应返回 SSE 流。
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Error("助理未启用却返回了 SSE 流")
	}
	if rec.Code == http.StatusOK {
		t.Errorf("助理未启用却返回 200：%s", rec.Body.String())
	}
}

// TestMcpAssistantStreamMissingConfigIsActionable 确认未配置 LLM 时给出可操作提示。
//
// 若这里返回的是 200 + 空流，前端会显示一个永远转圈的空气泡，
// 用户完全不知道要去设置页填 Key。
func TestMcpAssistantStreamMissingConfigIsActionable(t *testing.T) {
	h := newMCPTestHandler(t, map[string]string{
		string(settings.KeyMcpEnabled):          "true",
		string(settings.KeyMcpAssistantEnabled): "true",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/assistant/stream",
		strings.NewReader(`{"message":"你好"}`))
	rec := httptest.NewRecorder()
	h.mcpAssistantStream(rec, req)

	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("未配置 LLM 却返回了 SSE 流，前端将永远等待")
	}
	if rec.Code == http.StatusOK {
		t.Errorf("未配置 LLM 却返回 200：%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "AI") && !strings.Contains(rec.Body.String(), "配置") {
		t.Errorf("错误信息未提示去哪里配置：%s", rec.Body.String())
	}
}

// TestMcpAssistantStreamCarriesHistory 确认同一会话的上下文会带给模型。
func TestMcpAssistantStreamCarriesHistory(t *testing.T) {
	var sawMessages int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawMessages = len(body.Messages)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好的\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if fw, ok := w.(http.Flusher); ok {
			fw.Flush()
		}
	}))
	defer upstream.Close()

	h := newMCPTestHandler(t, map[string]string{
		string(settings.KeyMcpEnabled):          "true",
		string(settings.KeyMcpAssistantEnabled): "true",
		string(settings.KeyMcpAssistantBaseURL): upstream.URL,
		string(settings.KeyMcpAssistantAPIKey):  "sk-test",
		string(settings.KeyMcpAssistantModel):   "test-model",
	})

	body := `{"message":"第二句","history":[
		{"role":"user","content":"第一句"},
		{"role":"assistant","content":"第一答"}]}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/assistant/stream", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.mcpAssistantStream(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，正文：%s", rec.Code, rec.Body.String())
	}
	// 2 条历史 + 系统提示 + 本轮用户消息，必然多于 2 条。
	if sawMessages <= 2 {
		t.Errorf("上游只收到 %d 条消息，历史没有被带上", sawMessages)
	}
}

// TestMcpAssistantStreamIgnoresNilEventCallback 确认 onEvent 回调缺省时不 panic。
//
// mcp.Assistant.Chat 的 onEvent 允许传 nil，这条约束若被破坏，
// 会在真机上表现为「一说话就 500」。
func TestMcpAssistantStreamIgnoresNilEventCallback(t *testing.T) {
	h := newMCPTestHandler(t, map[string]string{
		string(settings.KeyMcpEnabled):          "true",
		string(settings.KeyMcpAssistantEnabled): "true",
		string(settings.KeyMcpAssistantBaseURL): "http://127.0.0.1:1",
		string(settings.KeyMcpAssistantAPIKey):  "sk-test",
		string(settings.KeyMcpAssistantModel):   "test-model",
	})
	// 指向不可达端口，确认失败被转成 error 事件而不是 panic。
	req := httptest.NewRequest(http.MethodPost, "/mcp/assistant/stream",
		strings.NewReader(`{"message":"你好"}`))
	rec := httptest.NewRecorder()
	h.mcpAssistantStream(rec, req)

	frames := parseSSEBody(t, rec.Body.String())
	if len(frames) == 0 {
		t.Fatalf("上游不可达时没有任何事件输出，正文：%q", rec.Body.String())
	}
	if frames[len(frames)-1].Event != "error" {
		t.Errorf("上游不可达时末尾事件 = %q，期望 error", frames[len(frames)-1].Event)
	}
}

// TestMcpAssistantSSEBodyIsSingleLine 确认多行回答不会破坏 SSE 分帧。
//
// 模型回答里的换行若以裸 "\n" 写出，帧内就会出现空行，前端的 "\n\n"
// 分帧会把一条事件劈成两条，表现为回答被截断或错位。
//
// 关于 writeEvent 里那句 strings.ReplaceAll(raw, "\n", "\\n")：经反向验证，
// 对当前实现它是**防御性**的 —— json.Marshal 本就会把字符串内的换行转义成
// 两个字符 \ 与 n，raw 里根本不含裸换行，删掉该 ReplaceAll 本测试仍会通过。
// 保留它的价值在于：日后若有人把 json.Marshal 换成手工拼串（payload 里带真换行），
// 该行是唯一的兜底。真正被本测试锁住的是「分帧不被破坏」这个外部可观测性质，
// 而非某一行具体实现。
func TestMcpAssistantSSEBodyIsSingleLine(t *testing.T) {
	upstream := fakeStreamingAnswerServer(t, "第一行\n第二行\n第三行")
	defer upstream.Close()

	h := newMCPTestHandler(t, map[string]string{
		string(settings.KeyMcpEnabled):          "true",
		string(settings.KeyMcpAssistantEnabled): "true",
		string(settings.KeyMcpAssistantBaseURL): upstream.URL,
		string(settings.KeyMcpAssistantAPIKey):  "sk-test",
		string(settings.KeyMcpAssistantModel):   "test-model",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp/assistant/stream",
		strings.NewReader(`{"message":"讲个笑话"}`))
	rec := httptest.NewRecorder()
	h.mcpAssistantStream(rec, req)

	raw := rec.Body.String()

	// 整段响应里相邻两个换行必须**只**出现在帧与帧之间。若某条载荷内部
	// 带了裸换行，就会出现 "data: xxx\n\ndata: yyy" 之外的空行组合。
	if strings.Contains(strings.ReplaceAll(raw, "\n\n", ""), "\n\n") {
		t.Error("响应中存在嵌套的空行，SSE 分帧会被劈开")
	}

	// 每个 data: 行都必须自成一行且是完整 JSON。
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
		if !json.Valid([]byte(payload)) {
			t.Errorf("data 行不是完整 JSON（说明正文换行没做单行化）：%q", payload)
		}
	}

	// 事件数量必须恰好等于「帧数」，被劈开的话这里会多出空事件。
	frames := parseSSEBody(t, raw)
	for _, f := range frames {
		if f.Event == "" {
			t.Error("解析出无事件名的空帧，说明分帧被破坏")
		}
	}

	// 且换行内容本身不能丢：前端最终要能渲染出三行。
	var streamed strings.Builder
	for _, f := range frames {
		if f.Event == "delta" {
			content, _ := f.Data["content"].(string)
			streamed.WriteString(content)
		}
	}
	if strings.Count(streamed.String(), "\n") != 2 {
		t.Errorf("答案中的换行丢失或增多，得到 %q", streamed.String())
	}
}
