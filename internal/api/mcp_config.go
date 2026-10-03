package api

import (
	"net/http"
	"sort"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/mcp"
)

// MCP 设置与工具清单端点。

// mcpConfigResponse 是返回给前端的配置视图。
//
// 只暴露「是否已设置」布尔量，不回显任何密钥明文：
// assistant_api_key_set 为 true 表示已保存过 Key，前端据此显示
// 「已配置」占位而不是具体值。
type mcpConfigResponse struct {
	Enabled            bool     `json:"enabled"`
	AllowWriteTools    bool     `json:"allow_write_tools"`
	DisabledTools      []string `json:"disabled_tools"`
	AssistantEnabled   bool     `json:"assistant_enabled"`
	AssistantBaseURL   string   `json:"assistant_base_url"`
	AssistantModelName string   `json:"assistant_model_name"`
	AssistantPrompt    string   `json:"assistant_prompt"`
	// AssistantAPIKeySet 只表明是否已配置，绝不返回密钥本身。
	AssistantAPIKeySet bool `json:"assistant_api_key_set"`
	MaxToolRounds      int  `json:"max_tool_rounds"`
	Timeout            int  `json:"timeout"`

	// 生效值：MCP 未配置时回退到「AI 识别设置」，前端可据此提示用户。
	EffectiveBaseURL   string `json:"effective_base_url"`
	EffectiveModelName string `json:"effective_model_name"`
	EffectivePrompt    string `json:"effective_prompt"`
	EffectiveAPIKeySet bool   `json:"effective_api_key_set"`
}

// mcpToolView 描述单个工具的启用状态。
type mcpToolView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
	Enabled     bool   `json:"enabled"`
	Available   bool   `json:"available"`
	Visible     bool   `json:"visible"`
}

// mcpConfigEnvelope 是设置端点的响应体。
//
// 必须包一层 {config, tools, server_url}：前端读取的是 data.data.config 与
// data.data.tools。参考实现曾因后端返回扁平字段、前端仍读嵌套路径，
// 导致设置页静默空白（不报错、就是空白），极难排查。契约测试锁死了这个形状。
type mcpConfigEnvelope struct {
	Config    mcpConfigResponse `json:"config"`
	Tools     []mcpToolView     `json:"tools"`
	ServerURL string            `json:"server_url"`
}

// mcpConfigToResponse 把配置转成前端视图。
func mcpConfigToResponse(cfg *mcp.Config) mcpConfigResponse {
	eff := mcp.EffectiveAssistantConfig(cfg)
	return mcpConfigResponse{
		Enabled:            cfg.Enabled,
		AllowWriteTools:    cfg.AllowWriteTools,
		DisabledTools:      cfg.DisabledTools,
		AssistantEnabled:   cfg.AssistantEnabled,
		AssistantBaseURL:   cfg.AssistantBaseURL,
		AssistantModelName: cfg.AssistantModel,
		AssistantPrompt:    cfg.AssistantPrompt,
		// 只回布尔：密钥明文永不出网。
		AssistantAPIKeySet: cfg.AssistantAPIKey != "",
		MaxToolRounds:      cfg.MaxToolRounds,
		Timeout:            cfg.Timeout,

		EffectiveBaseURL:   eff.BaseURL,
		EffectiveModelName: eff.ModelName,
		EffectivePrompt:    eff.Prompt,
		EffectiveAPIKeySet: eff.APIKey != "",
	}
}

// mcpToolListResponse 列出全部工具及其当前可见性。
func mcpToolListResponse() []mcpToolView {
	snapshot := mcp.ConfigSnapshot()
	tools := mcp.DefaultRegistry().All()
	views := make([]mcpToolView, 0, len(tools))
	for _, tool := range tools {
		allowed := mcp.IsToolAllowed(tool, snapshot)
		views = append(views, mcpToolView{
			Name:        tool.Name(),
			Description: tool.Description(),
			ReadOnly:    tool.ReadOnly(),
			Enabled:     allowed,
			Available:   allowed,
			Visible:     allowed,
		})
	}
	return views
}

// mcpEnvelope 组装完整响应。
func mcpEnvelope(cfg *mcp.Config) mcpConfigEnvelope {
	return mcpConfigEnvelope{
		Config:    mcpConfigToResponse(cfg),
		Tools:     mcpToolListResponse(),
		ServerURL: mcpConfigPath,
	}
}

// mcpConfigGet 返回当前配置与工具清单。
func (h *Handler) mcpConfigGet(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.settings != nil) {
		return
	}
	writeOK(w, mcpEnvelope(mcp.ConfigFromSettings(h.settings)))
}

// updateMcpConfigRequest 是配置更新请求体。
type updateMcpConfigRequest struct {
	Enabled            *bool     `json:"enabled"`
	AllowWriteTools    *bool     `json:"allow_write_tools"`
	DisabledTools      *[]string `json:"disabled_tools"`
	AssistantEnabled   *bool     `json:"assistant_enabled"`
	AssistantBaseURL   *string   `json:"assistant_base_url"`
	AssistantModelName *string   `json:"assistant_model_name"`
	AssistantPrompt    *string   `json:"assistant_prompt"`
	// AssistantAPIKey 留空表示「保持原值」；清空用 clear_assistant_api_key。
	AssistantAPIKey      *string `json:"assistant_api_key"`
	ClearAssistantAPIKey bool    `json:"clear_assistant_api_key"`
	MaxToolRounds        *int    `json:"max_tool_rounds"`
	Timeout              *int    `json:"timeout"`
}

// unknownMcpTools 返回不在注册表中的工具名。
//
// 在前端禁用清单里写错一个名字，如果不校验就会静默失效
// （用户以为禁用了，实际没禁），所以显式报错。
func unknownMcpTools(names []string) []string {
	registry := mcp.DefaultRegistry()
	var unknown []string
	seen := make(map[string]bool, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, err := registry.Get(name); err != nil {
			key := strings.ToLower(name)
			if !seen[key] {
				seen[key] = true
				unknown = append(unknown, name)
			}
		}
	}
	sort.Strings(unknown)
	return unknown
}

// mcpConfigUpdate 保存配置。
func (h *Handler) mcpConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.settings != nil) {
		return
	}
	var req updateMcpConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	current := mcp.ConfigFromSettings(h.settings)
	updated := *current
	if req.Enabled != nil {
		updated.Enabled = *req.Enabled
	}
	if req.AllowWriteTools != nil {
		updated.AllowWriteTools = *req.AllowWriteTools
	}
	if req.DisabledTools != nil {
		if unknown := unknownMcpTools(*req.DisabledTools); len(unknown) > 0 {
			writeErr(w, domain.Errorf(domain.CodeValidation, "未知的工具名：%s", strings.Join(unknown, ", ")))
			return
		}
		updated.DisabledTools = *req.DisabledTools
	}
	if req.AssistantEnabled != nil {
		updated.AssistantEnabled = *req.AssistantEnabled
	}
	if req.AssistantBaseURL != nil {
		updated.AssistantBaseURL = strings.TrimSpace(*req.AssistantBaseURL)
	}
	if req.AssistantModelName != nil {
		updated.AssistantModel = strings.TrimSpace(*req.AssistantModelName)
	}
	if req.AssistantPrompt != nil {
		updated.AssistantPrompt = *req.AssistantPrompt
	}
	if req.AssistantAPIKey != nil && !req.ClearAssistantAPIKey {
		// 空字符串表示保持原密钥不变 —— 前端从不回显密钥，
		// 因此不能把「字段为空」当成「清空密钥」。
		updated.AssistantAPIKey = strings.TrimSpace(*req.AssistantAPIKey)
	}
	if req.ClearAssistantAPIKey {
		updated.AssistantAPIKey = ""
	}
	if req.MaxToolRounds != nil {
		updated.MaxToolRounds = *req.MaxToolRounds
	}
	if req.Timeout != nil {
		updated.Timeout = *req.Timeout
	}

	if err := mcp.UpdateConfig(r.Context(), h.settings, &updated, current, req.ClearAssistantAPIKey); err != nil {
		writeErr(w, err)
		return
	}
	saved := mcp.ConfigFromSettings(h.settings)
	writeOK(w, mcpEnvelope(saved))
}

// mcpToolsList 单独返回工具清单（前端刷新用）。
func (h *Handler) mcpToolsList(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{"tools": mcpToolListResponse()})
}
