package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// stubTool 是一个只用于测试的工具实现。
type stubTool struct {
	name     string
	readOnly bool
}

func (s stubTool) Name() string                 { return s.name }
func (s stubTool) Description() string          { return "测试工具 " + s.name }
func (s stubTool) InputSchema() json.RawMessage { return emptyObjectSchema() }
func (s stubTool) ReadOnly() bool               { return s.readOnly }
func (s stubTool) Handler(context.Context, map[string]any) (any, error) {
	return "ok", nil
}

func TestParseRequest(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr int
		wantOK  bool
	}{
		{name: "正常请求", body: `{"jsonrpc":"2.0","id":1,"method":"ping"}`, wantOK: true},
		{name: "缺少 jsonrpc 时自动补全", body: `{"id":1,"method":"ping"}`, wantOK: true},
		{name: "空请求体", body: `   `, wantErr: ErrCodeParseError},
		{name: "非法 JSON", body: `{"jsonrpc":`, wantErr: ErrCodeParseError},
		{name: "不支持的版本", body: `{"jsonrpc":"1.0","id":1,"method":"ping"}`, wantErr: ErrCodeInvalidRequest},
		{name: "缺少 method", body: `{"jsonrpc":"2.0","id":1}`, wantErr: ErrCodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, rpcErr := ParseRequest([]byte(tc.body))
			if tc.wantOK {
				if rpcErr != nil {
					t.Fatalf("期望解析成功，实际错误：%v", rpcErr)
				}
				if req == nil || req.JSONRPC != JSONRPCVersion {
					t.Fatalf("解析结果不符合预期：%+v", req)
				}
				return
			}
			if rpcErr == nil {
				t.Fatalf("期望解析失败，实际成功了：%+v", req)
			}
			if rpcErr.Code != tc.wantErr {
				t.Fatalf("错误码期望 %d，实际 %d（%s）", tc.wantErr, rpcErr.Code, rpcErr.Message)
			}
		})
	}
}

func TestRequestIsNotification(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`, want: true},
		{body: `{"jsonrpc":"2.0","id":null,"method":"ping"}`, want: true},
		{body: `{"jsonrpc":"2.0","id":0,"method":"ping"}`, want: false},
		{body: `{"jsonrpc":"2.0","id":"abc","method":"ping"}`, want: false},
	}
	for _, tc := range cases {
		req, rpcErr := ParseRequest([]byte(tc.body))
		if rpcErr != nil {
			t.Fatalf("解析 %s 失败：%v", tc.body, rpcErr)
		}
		if got := req.IsNotification(); got != tc.want {
			t.Fatalf("%s IsNotification 期望 %v，实际 %v", tc.body, tc.want, got)
		}
	}
}

func TestDecodeParams(t *testing.T) {
	var target struct {
		Name string `json:"name"`
	}
	if rpcErr := DecodeParams(nil, &target); rpcErr != nil {
		t.Fatalf("空 params 应当允许：%v", rpcErr)
	}
	if rpcErr := DecodeParams(json.RawMessage("null"), &target); rpcErr != nil {
		t.Fatalf("null params 应当允许：%v", rpcErr)
	}
	rpcErr := DecodeParams(json.RawMessage(`[1,2]`), &target)
	if rpcErr == nil || rpcErr.Code != ErrCodeInvalidParams {
		t.Fatalf("数组 params 应当报 -32602，实际：%v", rpcErr)
	}
	if rpcErr := DecodeParams(json.RawMessage(`{"name":"a"}`), &target); rpcErr != nil {
		t.Fatalf("对象 params 应当解析成功：%v", rpcErr)
	} else if target.Name != "a" {
		t.Fatalf("params 未正确解码：%+v", target)
	}
}

func TestInitializeProtocolNegotiation(t *testing.T) {
	server := NewServer(NewRegistry(), func() *McpConfigSnapshot { return &McpConfigSnapshot{} })

	// 不支持的版本：回落到服务端首选版本，且握手不报错。
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`
	resp := server.HandleMessage(t.Context(), []byte(body))
	if resp == nil || resp.Error != nil {
		t.Fatalf("握手不应失败：%+v", resp)
	}
	result, ok := resp.Result.(InitializeResult)
	if !ok {
		t.Fatalf("握手返回类型不符：%T", resp.Result)
	}
	if result.ProtocolVersion != ProtocolVersion {
		t.Fatalf("协议版本期望 %s，实际 %s", ProtocolVersion, result.ProtocolVersion)
	}
	if result.ServerInfo.Name != ServerName {
		t.Fatalf("服务端名期望 %s，实际 %s", ServerName, result.ServerInfo.Name)
	}
	if result.Capabilities.Tools == nil {
		t.Fatal("应当声明 tools 能力")
	}
	if result.Capabilities.Tools.ListChanged {
		t.Fatal("ListChanged 应当恒为 false")
	}

	// 受支持的旧版本：原样回显，保证旧客户端可用。
	legacy := `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"` + ProtocolVersionLegacy + `"}}`
	resp = server.HandleMessage(t.Context(), []byte(legacy))
	if resp == nil || resp.Error != nil {
		t.Fatalf("旧版本握手不应失败：%+v", resp)
	}
	result, _ = resp.Result.(InitializeResult)
	if result.ProtocolVersion != ProtocolVersionLegacy {
		t.Fatalf("协议版本期望回显 %s，实际 %s", ProtocolVersionLegacy, result.ProtocolVersion)
	}
}

func TestUnknownMethodReturnsMethodNotFound(t *testing.T) {
	server := NewServer(NewRegistry(), nil)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/execute"}`
	resp := server.HandleMessage(t.Context(), []byte(body))
	if resp == nil || resp.Error == nil {
		t.Fatalf("未实现方法应当报错：%+v", resp)
	}
	if resp.Error.Code != ErrCodeMethodNotFound {
		t.Fatalf("错误码期望 %d，实际 %d", ErrCodeMethodNotFound, resp.Error.Code)
	}
}

func TestResourcesAndPromptsReturnEmptyLists(t *testing.T) {
	server := NewServer(NewRegistry(), nil)
	for _, method := range []string{"resources/list", "prompts/list"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"}`
		resp := server.HandleMessage(t.Context(), []byte(body))
		if resp == nil || resp.Error != nil {
			t.Fatalf("%s 应当返回成功空清单：%+v", method, resp)
		}
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	server := NewServer(NewRegistry(), nil)
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	if resp := server.HandleMessage(t.Context(), []byte(body)); resp != nil {
		t.Fatalf("通知不应产生响应：%+v", resp)
	}
	// 未识别的通知同样必须静默忽略，否则客户端会断开连接。
	unknown := `{"jsonrpc":"2.0","method":"notifications/whatever"}`
	if resp := server.HandleMessage(t.Context(), []byte(unknown)); resp != nil {
		t.Fatalf("未知通知不应产生响应：%+v", resp)
	}
}

func TestPingReturnsEmptyObject(t *testing.T) {
	server := NewServer(NewRegistry(), nil)
	resp := server.HandleMessage(t.Context(), []byte(`{"jsonrpc":"2.0","id":7,"method":"ping"}`))
	if resp == nil || resp.Error != nil {
		t.Fatalf("ping 应当成功：%+v", resp)
	}
	if string(resp.ID) != "7" {
		t.Fatalf("响应 id 应当回显 7，实际 %s", string(resp.ID))
	}
}

func TestParseErrorResponseUsesNullID(t *testing.T) {
	server := NewServer(NewRegistry(), nil)
	resp := server.HandleMessage(t.Context(), []byte(`{`))
	if resp == nil || resp.Error == nil {
		t.Fatalf("非法报文应当报错：%+v", resp)
	}
	if string(resp.ID) != "null" {
		t.Fatalf("解析失败响应的 id 应为 null，实际 %s", string(resp.ID))
	}
	if resp.Error.Code != ErrCodeParseError {
		t.Fatalf("错误码期望 %d，实际 %d", ErrCodeParseError, resp.Error.Code)
	}
}

func TestIsToolAllowed(t *testing.T) {
	readOnly := stubTool{name: "media_search", readOnly: true}
	writer := stubTool{name: "netdisk_delete", readOnly: false}

	// nil 快照：只放行只读工具（配置未加载时宁可少给能力）。
	if !IsToolAllowed(readOnly, nil) {
		t.Fatal("nil 快照下只读工具应当可用")
	}
	if IsToolAllowed(writer, nil) {
		t.Fatal("nil 快照下写工具不应可用")
	}

	// 开启总开关但未开写权限。
	enabled := &McpConfigSnapshot{Enabled: true}
	if !IsToolAllowed(readOnly, enabled) {
		t.Fatal("开启后只读工具应当可用")
	}
	if IsToolAllowed(writer, enabled) {
		t.Fatal("未开写权限时写工具不应可用")
	}

	// 开启写权限。
	writable := &McpConfigSnapshot{Enabled: true, AllowWriteTools: true}
	if !IsToolAllowed(writer, writable) {
		t.Fatal("开启写权限后写工具应当可用")
	}

	// 禁用清单忽略大小写与空白。
	disabled := &McpConfigSnapshot{Enabled: true, AllowWriteTools: true, DisabledTools: []string{"  MEDIA_SEARCH "}}
	if IsToolAllowed(readOnly, disabled) {
		t.Fatal("禁用清单中的工具不应可用（大小写与空白应被忽略）")
	}
	if IsToolAllowed(nil, writable) {
		t.Fatal("nil 工具不应可用")
	}
}

func TestRegistryVisibleKeepsOrder(t *testing.T) {
	registry := NewRegistry()
	registry.Register(stubTool{name: "a", readOnly: true})
	registry.Register(stubTool{name: "b", readOnly: false})
	registry.Register(stubTool{name: "c", readOnly: true})
	// 同名重复注册不应改变顺序。
	registry.Register(stubTool{name: "a", readOnly: true})

	visible := registry.Visible(&McpConfigSnapshot{Enabled: true})
	got := strings.Join(ToolNames(visible), ",")
	if got != "a,c" {
		t.Fatalf("可见工具顺序期望 a,c，实际 %s", got)
	}
	all := strings.Join(ToolNames(registry.All()), ",")
	if all != "a,b,c" {
		t.Fatalf("全部工具顺序期望 a,b,c，实际 %s", all)
	}
}

func TestRegistryGetIsCaseInsensitive(t *testing.T) {
	registry := NewRegistry()
	registry.Register(stubTool{name: "Media_Search", readOnly: true})
	if _, err := registry.Get("media_search"); err != nil {
		t.Fatalf("大小写不敏感查找应当命中：%v", err)
	}
	if _, err := registry.Get("nope"); err == nil {
		t.Fatal("未知工具应当报错")
	}
}

func TestValidateToolNames(t *testing.T) {
	registry := NewRegistry()
	registry.Register(stubTool{name: "media_search", readOnly: true})
	registry.Register(stubTool{name: "netdisk_list", readOnly: true})

	if unknown := ValidateToolNames(registry, []string{"media_search", "MEDIADISK"}); len(unknown) != 1 || unknown[0] != "MEDIADISK" {
		t.Fatalf("未知工具名期望 [MEDIADISK]，实际 %v", unknown)
	}
	if unknown := ValidateToolNames(registry, []string{"", "  ", "netdisk_list"}); len(unknown) != 0 {
		t.Fatalf("空白项应被跳过，实际 %v", unknown)
	}
}

func TestArgHelpers(t *testing.T) {
	args := map[string]any{
		"name":    "媒体库",
		"count":   "12",
		"ratio":   float64(3),
		"flag":    "true",
		"off":     "0",
		"list":    []any{"a", " b ", ""},
		"csv":     "x, y ,z",
		"neg":     -5,
		"jsonnum": json.Number("42"),
		"boolish": true,
	}
	if got := argString(args, "name"); got != "媒体库" {
		t.Fatalf("argString 期望 媒体库，实际 %q", got)
	}
	if got := argString(args, "missing"); got != "" {
		t.Fatalf("缺失键应返回空串，实际 %q", got)
	}
	if got := argInt(args, "count"); got != 12 {
		t.Fatalf("字符串数字应被解析，实际 %d", got)
	}
	if got := argInt(args, "ratio"); got != 3 {
		t.Fatalf("浮点应被取整，实际 %d", got)
	}
	if got := argInt(args, "jsonnum"); got != 42 {
		t.Fatalf("json.Number 应被解析，实际 %d", got)
	}
	if got := argBool(args, "flag", false); !got {
		t.Fatal("\"true\" 应被识别为真")
	}
	if got := argBool(args, "off", true); got {
		t.Fatal("\"0\" 应被识别为假")
	}
	if got := argBool(args, "missing", true); !got {
		t.Fatal("缺失时应返回默认值")
	}
	if got := argUint(args, "neg"); got != 0 {
		t.Fatalf("负数应归零，实际 %d", got)
	}
	if got := argStringSlice(args, "csv"); len(got) != 3 || got[1] != "y" {
		t.Fatalf("逗号分隔字符串应被切分并去空白，实际 %v", got)
	}
	if got := argStringSlice(args, "list"); len(got) != 2 {
		t.Fatalf("数组应过滤空项，实际 %v", got)
	}
	if _, err := requireString(args, "missing", "名称"); err == nil {
		t.Fatal("缺少必填字符串参数应当报错")
	}
	if _, err := requireUint(map[string]any{"id": 0}, "id", "账号 ID"); err == nil {
		t.Fatal("必填正整数为 0 时应当报错")
	}
	if value, err := requireUint(map[string]any{"id": "9"}, "id", "账号 ID"); err != nil || value != 9 {
		t.Fatalf("字符串形式的正整数应当被接受，实际 %d err=%v", value, err)
	}
}

func TestClampPage(t *testing.T) {
	cases := []struct {
		page, pageSize         int
		wantPage, wantPageSize int
	}{
		{page: 0, pageSize: 0, wantPage: 1, wantPageSize: 20},
		{page: -3, pageSize: -1, wantPage: 1, wantPageSize: 20},
		{page: 2, pageSize: 500, wantPage: 2, wantPageSize: 100},
		{page: 5, pageSize: 30, wantPage: 5, wantPageSize: 30},
	}
	for _, tc := range cases {
		page, pageSize := clampPage(tc.page, tc.pageSize)
		if page != tc.wantPage || pageSize != tc.wantPageSize {
			t.Fatalf("clampPage(%d,%d) 期望 (%d,%d)，实际 (%d,%d)",
				tc.page, tc.pageSize, tc.wantPage, tc.wantPageSize, page, pageSize)
		}
	}
}

func TestToolDescriptorsFillsEmptySchema(t *testing.T) {
	descriptors := ToolDescriptors([]Tool{stubTool{name: "a", readOnly: true}})
	if len(descriptors) != 1 {
		t.Fatalf("期望 1 个描述，实际 %d", len(descriptors))
	}
	if !strings.Contains(string(descriptors[0].InputSchema), `"type":"object"`) {
		t.Fatalf("空 schema 应补成对象 schema，实际 %s", string(descriptors[0].InputSchema))
	}
}

func TestToolsCallErrorsAreBusinessResults(t *testing.T) {
	registry := NewRegistry()
	registry.Register(stubTool{name: "media_search", readOnly: true})
	server := NewServer(registry, func() *McpConfigSnapshot {
		return &McpConfigSnapshot{Enabled: true, DisabledTools: []string{"media_search"}}
	})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"media_search","arguments":{}}}`
	resp := server.HandleMessage(t.Context(), []byte(body))
	if resp == nil || resp.Error != nil {
		t.Fatalf("工具不可用应回成功响应：%+v", resp)
	}
	result, ok := resp.Result.(CallToolResult)
	if !ok || !result.IsError {
		t.Fatalf("应当返回 IsError=true 的结果：%+v", resp.Result)
	}

	// 未知工具名才是协议层参数错误。
	unknown := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"nope"}}`
	resp = server.HandleMessage(t.Context(), []byte(unknown))
	if resp == nil || resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("未知工具应当报 -32602：%+v", resp)
	}
	// 缺少 name 同样是参数错误。
	resp = server.HandleMessage(t.Context(), []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{}}`))
	if resp == nil || resp.Error == nil || resp.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("缺少 name 应当报 -32602：%+v", resp)
	}
}

func TestExecuteToolRespectsVisibility(t *testing.T) {
	registry := NewRegistry()
	registry.Register(stubTool{name: "netdisk_delete", readOnly: false})
	server := NewServer(registry, func() *McpConfigSnapshot { return &McpConfigSnapshot{Enabled: true} })
	if _, err := server.ExecuteTool(t.Context(), "netdisk_delete", nil); err == nil {
		t.Fatal("未开写权限时执行写工具应当报错")
	}
	if _, err := server.ExecuteTool(t.Context(), "nope", nil); err == nil {
		t.Fatal("未知工具应当报错")
	}
}

func TestStringifyToolResult(t *testing.T) {
	if got := stringifyToolResult(nil); got != "操作已完成（无返回内容）" {
		t.Fatalf("nil 结果文案不符：%s", got)
	}
	if got := stringifyToolResult("plain"); got != "plain" {
		t.Fatalf("字符串应原样返回：%s", got)
	}
	if got := stringifyToolResult(map[string]any{"a": 1}); !strings.Contains(got, "\n") {
		t.Fatalf("结构化结果应使用缩进 JSON：%s", got)
	}
}
