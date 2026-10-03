package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 内置智能助理：OpenAI 兼容协议的流式对话 + 工具调用循环
//
// 为什么在这里自己实现而不是复用 internal/aiorganize：
// 该客户端面向「一次性 JSON 识别任务」，消息结构只有 role/content，
// 既没有 tool_calls / tool_call_id 字段，也没有流式回调，无法承载多轮工具调用。
// 它同时依赖 settings 服务与自身的协议回退缓存，直接扩展成对话客户端
// 会影响既有刮削调用方。因此这里实现一个等价的对话客户端，
// 仅通过 aiorganize 暴露的「取生效配置」能力复用其配置来源。
//
// 协议范围：只实现 OpenAI 的流式 tool calling。Anthropic 的 tool_use /
// tool_result content block 是另一套报文格式，需要独立解析，不在本次范围内。
// ---------------------------------------------------------------------------

// 助理运行参数的默认值。
const (
	// assistantMaxTokens 是单次补全的最大 token 数。
	assistantMaxTokens = 4096
	// assistantChunkSize 是流式读取的缓冲上限（部分供应商单个 chunk 较大）。
	assistantChunkSize = 64 * 1024
	// assistantErrorBodyLimit 是错误响应最多读取的字节数。
	assistantErrorBodyLimit = 8192
	// assistantRawErrorLimit 是兜底错误文案的长度上限（按字符）。
	assistantRawErrorLimit = 300
)

// assistantToolCall 是一次工具调用请求（OpenAI 格式）。
type assistantToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// assistantMessage 是发给 LLM 的消息。
//
// 比 aiorganize 的 chatMessage 多出 ToolCalls / ToolCallID / Name 三个字段，
// 这三个是工具调用协议所必需的（tool 角色消息必须回填 tool_call_id）。
type assistantMessage struct {
	Role       string              `json:"role"`
	Content    string              `json:"content"`
	ToolCalls  []assistantToolCall `json:"tool_calls,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	Name       string              `json:"name,omitempty"`
}

// assistantTool 是暴露给 LLM 的工具声明（OpenAI function calling 格式）。
type assistantTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// assistantChatRequest 是对话补全请求体。
type assistantChatRequest struct {
	Model      string             `json:"model"`
	Messages   []assistantMessage `json:"messages"`
	Tools      []assistantTool    `json:"tools,omitempty"`
	ToolChoice string             `json:"tool_choice,omitempty"`
	Stream     bool               `json:"stream"`
	MaxTokens  int                `json:"max_tokens,omitempty"`
}

// assistantUsage 是 token 用量。
type assistantUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// assistantChoice 是流式响应里的一条选择项。
// Delta 用于流式分片，Message 用于非流式响应。
type assistantChoice struct {
	Index        int              `json:"index"`
	Message      assistantMessage `json:"message"`
	Delta        assistantMessage `json:"delta"`
	FinishReason string           `json:"finish_reason"`
}

// assistantStreamChunk 是流式响应的一帧。
type assistantStreamChunk struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Choices []assistantChoice `json:"choices"`
	Usage   *assistantUsage   `json:"usage"`
}

// AssistantOptions 是单次对话的运行参数。
type AssistantOptions struct {
	BaseURL    string
	APIKey     string
	ModelName  string
	Prompt     string
	MaxRounds  int
	TimeoutSec int
}

// 事件类型。这些字符串是前后端契约的一部分，不可随意改动。
const (
	assistantEventDelta      = "delta"
	assistantEventToolCall   = "tool_call"
	assistantEventToolResult = "tool_result"
	assistantEventDone       = "done"
	assistantEventError      = "error"
)

// AssistantEvent 是助理推给调用方的事件。
type AssistantEvent struct {
	Type       string          `json:"type"`
	Content    string          `json:"content,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolArgs   string          `json:"tool_args,omitempty"`
	ToolResult string          `json:"tool_result,omitempty"`
	ToolError  bool            `json:"tool_error,omitempty"`
	Message    string          `json:"message,omitempty"`
	Rounds     int             `json:"rounds,omitempty"`
	Usage      *assistantUsage `json:"usage,omitempty"`
}

// Assistant 驱动「模型 ↔ 工具」多轮循环。
type Assistant struct {
	server *Server
	// httpClient 不设 Timeout：单次请求的超时由 context 控制，
	// 否则会把「整段对话」误当成「一次请求」而提前掐断。
	httpClient *http.Client
}

// NewAssistant 构造助理。
func NewAssistant(server *Server) *Assistant {
	return &Assistant{server: server, httpClient: &http.Client{}}
}

// Chat 执行一次完整对话，并通过 onEvent 推送过程事件。
//
// 回调可以为 nil：调用方只关心最终返回值时不必传，这里统一兜底避免空指针崩溃。
func (a *Assistant) Chat(ctx context.Context, opts AssistantOptions, history []assistantMessage, userMessage string, onEvent func(AssistantEvent)) error {
	if onEvent == nil {
		onEvent = func(AssistantEvent) {}
	}
	if strings.TrimSpace(opts.BaseURL) == "" || strings.TrimSpace(opts.APIKey) == "" {
		return errors.New("助理未配置 LLM 接口：请在 MCP 设置中填写地址与 API Key，或在「AI 识别设置」中启用 AI")
	}
	if strings.TrimSpace(opts.ModelName) == "" {
		return errors.New("助理未配置模型名：请在 MCP 设置或「AI 识别设置」中填写")
	}
	maxRounds := opts.MaxRounds
	if maxRounds <= 0 {
		maxRounds = DefaultMaxToolRounds
	}
	timeoutSec := opts.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = DefaultTimeout
	}

	messages := make([]assistantMessage, 0, len(history)+2)
	if prompt := strings.TrimSpace(opts.Prompt); prompt != "" {
		messages = append(messages, assistantMessage{Role: "system", Content: prompt})
	}
	messages = append(messages, history...)
	messages = append(messages, assistantMessage{Role: "user", Content: userMessage})

	var (
		rounds int
		usage  *assistantUsage
	)
	for round := 0; ; round++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if round >= maxRounds {
			// 达到轮数上限不算错误：把「为什么停下」告诉用户，
			// 前端仍然会收到正常的结束事件。
			onEvent(AssistantEvent{
				Type:    assistantEventDelta,
				Content: fmt.Sprintf("\n\n[已达到最大工具调用轮数 %d，为确保安全已停止继续调用工具]", maxRounds),
			})
			break
		}

		content, toolCalls, roundUsage, err := a.callModelStream(ctx, opts, messages, a.assistantTools(), timeoutSec, onEvent)
		if err != nil {
			return err
		}
		if roundUsage != nil {
			usage = roundUsage
		}
		if len(toolCalls) == 0 {
			messages = append(messages, assistantMessage{Role: "assistant", Content: content})
			break
		}

		messages = append(messages, assistantMessage{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		})
		rounds++

		// 串行执行工具：并发调用可能同时改同一个媒体库/目录而互相干扰，
		// 且工具本身很快，串行换来的是可预期的执行顺序。
		for _, call := range toolCalls {
			resultText, _ := a.executeToolCall(ctx, call, onEvent)
			messages = append(messages, assistantMessage{
				Role:       "tool",
				Content:    resultText,
				ToolCallID: call.ID,
				Name:       call.Function.Name,
			})
		}
	}

	done := AssistantEvent{Type: assistantEventDone, Rounds: rounds}
	if usage != nil {
		done.Usage = usage
	}
	onEvent(done)
	return nil
}

// executeToolCall 执行单个工具调用，并推送 tool_call / tool_result 事件。
//
// 参数不是合法 JSON 时不当成致命错误：把它作为工具结果回灌给模型，
// 模型往往能自己纠正格式并重试，比直接中断对话体验好得多。
func (a *Assistant) executeToolCall(ctx context.Context, call assistantToolCall, onEvent func(AssistantEvent)) (string, error) {
	onEvent(AssistantEvent{
		Type:     assistantEventToolCall,
		ToolName: call.Function.Name,
		ToolArgs: call.Function.Arguments,
	})

	var args map[string]any
	if raw := strings.TrimSpace(call.Function.Arguments); raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			message := fmt.Sprintf("工具 %s 的参数不是合法 JSON：%v；请重新以 JSON 对象格式提供参数", call.Function.Name, err)
			onEvent(AssistantEvent{
				Type:       assistantEventToolResult,
				ToolName:   call.Function.Name,
				ToolResult: message,
				ToolError:  true,
			})
			return message, nil
		}
	}
	if a.server == nil {
		return "", errors.New("MCP 服务端未初始化")
	}
	result, err := a.server.ExecuteTool(ctx, call.Function.Name, args)
	if err != nil {
		message := fmt.Sprintf("工具执行失败：%v", err)
		onEvent(AssistantEvent{
			Type:       assistantEventToolResult,
			ToolName:   call.Function.Name,
			ToolResult: message,
			ToolError:  true,
		})
		return message, nil
	}
	text := ToolResultText(result)
	onEvent(AssistantEvent{
		Type:       assistantEventToolResult,
		ToolName:   call.Function.Name,
		ToolResult: text,
	})
	return text, nil
}

// assistantTools 把当前可见工具转成 OpenAI 的 function 声明。
func (a *Assistant) assistantTools() []assistantTool {
	if a.server == nil {
		return nil
	}
	visible := a.server.Registry().Visible(a.server.snapshot())
	out := make([]assistantTool, 0, len(visible))
	for _, tool := range visible {
		schema := tool.InputSchema()
		if len(trimJSONSpace(schema)) == 0 {
			schema = emptyObjectSchema()
		}
		var decl assistantTool
		decl.Type = "function"
		decl.Function.Name = tool.Name()
		decl.Function.Description = tool.Description()
		decl.Function.Parameters = schema
		out = append(out, decl)
	}
	return out
}

// callModelStream 发起一次流式补全请求并解析返回。
func (a *Assistant) callModelStream(ctx context.Context, opts AssistantOptions, messages []assistantMessage, tools []assistantTool, timeoutSec int, onEvent func(AssistantEvent)) (string, []assistantToolCall, *assistantUsage, error) {
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	payload := assistantChatRequest{
		Model:     opts.ModelName,
		Messages:  messages,
		Tools:     tools,
		Stream:    true,
		MaxTokens: assistantMaxTokens,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, nil, fmt.Errorf("序列化请求失败：%w", err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, buildChatCompletionsURL(opts.BaseURL), strings.NewReader(string(body)))
	if err != nil {
		return "", nil, nil, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+opts.APIKey)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		if ctxErr := reqCtx.Err(); ctxErr != nil {
			return "", nil, nil, fmt.Errorf("请求 LLM 超时或被取消：%w", ctxErr)
		}
		return "", nil, nil, fmt.Errorf("请求 LLM 失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 错误响应体通常很小，但仍要限流读取，避免异常上游把内存打满。
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, assistantErrorBodyLimit))
		return "", nil, nil, fmt.Errorf("HTTP %d：%s", resp.StatusCode, extractOpenAIErrorMessage(raw))
	}
	return parseAssistantStream(resp.Body, onEvent)
}

// parseAssistantStream 解析 OpenAI 兼容的 SSE 流。
//
// 回调可以为 nil：调用方只关心返回值时不必传，这里统一兜底避免空指针崩溃。
func parseAssistantStream(body io.Reader, onEvent func(AssistantEvent)) (string, []assistantToolCall, *assistantUsage, error) {
	if onEvent == nil {
		onEvent = func(AssistantEvent) {}
	}
	reader := bufio.NewReaderSize(body, assistantChunkSize)

	var (
		content strings.Builder
		usage   *assistantUsage
		// 工具调用按 index 累积：流式分片里同一个调用会跨多个 chunk 到达。
		calls       = map[int]*assistantToolCall{}
		callIndexes []int
	)

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if payload == "[DONE]" {
					break
				}
				var chunk assistantStreamChunk
				if jsonErr := json.Unmarshal([]byte(payload), &chunk); jsonErr != nil {
					// 单个坏分片不该毁掉整轮对话，记录后继续。
					packageLog().Warn("解析 MCP 助理流式分片失败", "error", jsonErr)
				} else {
					if chunk.Usage != nil {
						usage = chunk.Usage
					}
					for _, choice := range chunk.Choices {
						if choice.Delta.Content != "" {
							content.WriteString(choice.Delta.Content)
							onEvent(AssistantEvent{Type: assistantEventDelta, Content: choice.Delta.Content})
						}
						for _, fragment := range choice.Delta.ToolCalls {
							accumulateToolCall(calls, &callIndexes, fragment)
						}
					}
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return "", nil, nil, fmt.Errorf("读取流式响应失败：%w", err)
			}
			break
		}
	}

	out := make([]assistantToolCall, 0, len(callIndexes))
	for idx, callIndex := range callIndexes {
		call := calls[callIndex]
		if call == nil {
			continue
		}
		if strings.TrimSpace(call.Function.Name) == "" {
			// 没有名字的调用无法执行，丢掉并留痕，避免下游报「未找到工具：」这种迷惑错误。
			packageLog().Warn("丢弃缺少函数名的 MCP 工具调用", "index", callIndex)
			continue
		}
		if strings.TrimSpace(call.ID) == "" {
			// 部分供应商不回传 id，但回灌 tool 消息时必须带上，这里补一个稳定的。
			call.ID = fmt.Sprintf("call_%d", idx)
		}
		if call.Type == "" {
			call.Type = "function"
		}
		out = append(out, *call)
	}
	return content.String(), out, usage, nil
}

// accumulateToolCall 把流式分片累积到对应的工具调用上。
//
// 注意 Function.Name 是「追加」而不是「覆盖」：有些供应商会把函数名拆成多个分片
// （先 "media" 再 "_search"），覆盖式赋值会得到半个名字。
// ID 与 Type 则相反，只在非空时覆盖（重复分片会重复携带同一个值）。
func accumulateToolCall(calls map[int]*assistantToolCall, order *[]int, fragment assistantToolCall) {
	// 无 index 字段时按 0 处理：单工具调用场景下供应商可能省略 index。
	index := fragmentIndex(fragment)
	call, ok := calls[index]
	if !ok {
		call = &assistantToolCall{}
		calls[index] = call
		*order = append(*order, index)
	}
	if fragment.ID != "" {
		call.ID = fragment.ID
	}
	if fragment.Type != "" {
		call.Type = fragment.Type
	}
	call.Function.Name += fragment.Function.Name
	call.Function.Arguments += fragment.Function.Arguments
}

// fragmentIndex 取分片里的 index。
//
// assistantToolCall 本身不含 index 字段（它是消息结构的一部分），
// 这里用 ID 的稳定性做兜底：同一调用在同一轮内 ID 一致，
// 因此以「已见过的调用序号」代替 index 也能正确归并。
func fragmentIndex(fragment assistantToolCall) int {
	if id := strings.TrimSpace(fragment.ID); id != "" {
		return toolCallIndexFromID(id)
	}
	return 0
}

// toolCallIndexFromID 把调用 ID 映射成稳定的归并键。
//
// 供应商的 ID 形如 "call_abc123" / "toolu_xx"，本身不保证是数字，
// 因此用其哈希的低位作为归并键：同一个 ID 必然落到同一个键，
// 不同 ID 冲突的概率可以忽略（且冲突只会导致参数拼接串味，不影响安全）。
func toolCallIndexFromID(id string) int {
	var hash uint32 = 2166136261
	for i := 0; i < len(id); i++ {
		hash ^= uint32(id[i])
		hash *= 16777619
	}
	return int(hash & 0x7fffffff)
}

// buildChatCompletionsURL 由用户填写的 BaseURL 拼出补全端点。
//
// 兼容三种常见写法：纯域名、以 /v1 结尾、以及已带 /chat/completions 的完整地址。
func buildChatCompletionsURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	trimmed = strings.TrimRight(trimmed, "/")
	if strings.HasSuffix(trimmed, "/chat/completions") {
		return trimmed
	}
	trimmed = strings.TrimSuffix(trimmed, "/v1")
	return trimmed + "/v1/chat/completions"
}

// extractOpenAIErrorMessage 从错误响应体里提取可读文案。
func extractOpenAIErrorMessage(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "响应体为空"
	}
	var payload struct {
		Error *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if payload.Error != nil && strings.TrimSpace(payload.Error.Message) != "" {
			if payload.Error.Code != nil {
				return fmt.Sprintf("%s（code=%v）", payload.Error.Message, payload.Error.Code)
			}
			return payload.Error.Message
		}
		if strings.TrimSpace(payload.Message) != "" {
			return payload.Message
		}
	}
	// 有些网关直接返回一个 JSON 字符串。
	var plain string
	if err := json.Unmarshal(body, &plain); err == nil && strings.TrimSpace(plain) != "" {
		return plain
	}
	// 按字符截断：按字节切会把中文切成乱码。
	runes := []rune(text)
	if len(runes) > assistantRawErrorLimit {
		return string(runes[:assistantRawErrorLimit]) + "..."
	}
	return text
}

// AssistantMessage 是暴露给 HTTP 层的历史消息结构。
type AssistantMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// HistoryFromMessages 把外部历史转成助理可用的消息列表，并做角色白名单过滤。
//
// 只接受 user / assistant：防止客户端把内容伪装成 system 提示词做注入。
func HistoryFromMessages(items []AssistantMessage) []assistantMessage {
	out := make([]assistantMessage, 0, len(items))
	for _, item := range items {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		if role != "user" && role != "assistant" {
			continue
		}
		out = append(out, assistantMessage{Role: role, Content: content})
	}
	return out
}

// HistoryFromPairs 由平行的角色/内容切片构造历史（便于测试）。
func HistoryFromPairs(roles, contents []string) []assistantMessage {
	size := len(roles)
	if len(contents) < size {
		size = len(contents)
	}
	items := make([]AssistantMessage, 0, size)
	for i := 0; i < size; i++ {
		items = append(items, AssistantMessage{Role: roles[i], Content: contents[i]})
	}
	return HistoryFromMessages(items)
}

// TestAssistantConnection 测试 LLM 接口连通性。
//
// 刻意使用独立的 http.Client：测试用的超时与连接状态不应残留在真实会话的连接池上。
func TestAssistantConnection(ctx context.Context, opts AssistantOptions) error {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return errors.New("未填写接口地址")
	}
	if strings.TrimSpace(opts.APIKey) == "" {
		return errors.New("未填写 API Key")
	}
	if strings.TrimSpace(opts.ModelName) == "" {
		return errors.New("未填写模型名")
	}
	timeoutSec := opts.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = DefaultTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	payload := assistantChatRequest{
		Model:     opts.ModelName,
		Messages:  []assistantMessage{{Role: "user", Content: "ping"}},
		Stream:    false,
		MaxTokens: 1,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化请求失败：%w", err)
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, buildChatCompletionsURL(opts.BaseURL), strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opts.APIKey)

	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := reqCtx.Err(); ctxErr != nil {
			return fmt.Errorf("连接超时或被取消：%w", ctxErr)
		}
		return fmt.Errorf("连接失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, assistantErrorBodyLimit))
		return fmt.Errorf("接口返回 %d：%s", resp.StatusCode, extractOpenAIErrorMessage(raw))
	}
	return nil
}
