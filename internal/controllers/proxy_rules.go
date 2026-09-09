package controllers

import (
	"encoding/json"
	"net/http"

	"diy-strm/internal/models"

	"github.com/gin-gonic/gin"
)

// 多规则反代 CRUD（对齐 tgto123 /api/emby_proxy/config* 形状）。
// 启停生命周期由 emby302/proxyrules.ReloadProxyRules 统一管理（保存后调用）。

// ReloadEmbyProxyRulesFn 规则保存后重建监听（main 注入，避免循环 import）
var ReloadEmbyProxyRulesFn func()

// GetProxyRulesAPI GET /api/emby302/proxy-rules
func GetProxyRulesAPI(c *gin.Context) {
	rules, err := models.ListEmby302ProxyRules()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "读取反代规则失败：" + err.Error()})
		return
	}
	// api_key 脱敏返回（前端编辑时留空表示不修改）
	out := make([]gin.H, 0, len(rules))
	for _, r := range rules {
		out = append(out, gin.H{
			"id": r.ID, "proxy_type": r.ProxyType, "name": r.Name,
			"host": r.Host, "listen_port": r.ListenPort, "enabled": r.Enabled,
			"path_mappings": r.PathMappingList(),
			"has_api_key":   r.APIKey != "",
		})
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{"rules": out}})
}

// SaveProxyRuleAPI POST /api/emby302/proxy-rules/save {id?（0=新建）, proxy_type, name, host, api_key?, listen_port, enabled, path_mappings?}
func SaveProxyRuleAPI(c *gin.Context) {
	var req struct {
		ID           uint                `json:"id"`
		ProxyType    string              `json:"proxy_type"`
		Name         string              `json:"name"`
		Host         string              `json:"host"`
		APIKey       string              `json:"api_key"`
		ListenPort   int                 `json:"listen_port"`
		Enabled      bool                `json:"enabled"`
		PathMappings []map[string]string `json:"path_mappings"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error()})
		return
	}
	rule := &models.Emby302ProxyRule{
		ProxyType: req.ProxyType, Name: req.Name, Host: req.Host,
		APIKey: req.APIKey, ListenPort: req.ListenPort, Enabled: req.Enabled,
	}
	if req.ID > 0 {
		existing, err := models.GetEmby302ProxyRule(req.ID)
		if err != nil {
			c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "规则不存在"})
			return
		}
		// api_key 留空 = 保留原值（脱敏回显约定）
		if rule.APIKey == "" {
			rule.APIKey = existing.APIKey
		}
		// UI 暂无映射编辑器：更新时保留既有 path_mappings，防止全字段覆盖清空
		if len(req.PathMappings) == 0 {
			rule.PathMappings = existing.PathMappings
		}
		rule.ID = existing.ID
		rule.CreatedAt = existing.CreatedAt
	}
	if err := models.SaveEmby302ProxyRule(rule); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	fireProxyRulesReload()
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "反代规则已保存并应用", Data: gin.H{"id": rule.ID}})
}

// ToggleProxyRuleAPI POST /api/emby302/proxy-rules/toggle {id, enabled}
func ToggleProxyRuleAPI(c *gin.Context) {
	var req struct {
		ID      uint `json:"id"`
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误"})
		return
	}
	rule, err := models.GetEmby302ProxyRule(req.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "规则不存在"})
		return
	}
	rule.Enabled = req.Enabled
	if err := models.SaveEmby302ProxyRule(rule); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	fireProxyRulesReload()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "规则状态已更新"})
}

// DeleteProxyRuleAPI POST /api/emby302/proxy-rules/delete {id}
func DeleteProxyRuleAPI(c *gin.Context) {
	var req struct {
		ID uint `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误"})
		return
	}
	if err := models.DeleteEmby302ProxyRule(req.ID); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "删除失败：" + err.Error()})
		return
	}
	fireProxyRulesReload()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "反代规则已删除"})
}

func fireProxyRulesReload() {
	if ReloadEmbyProxyRulesFn != nil {
		go ReloadEmbyProxyRulesFn()
	}
}

func jsonMarshalPathMappings(mappings []map[string]string) (string, error) {
	raw, err := json.Marshal(mappings)
	return string(raw), err
}
