package controllers

import (
	"net/http"

	"diy-strm/internal/models"

	"github.com/gin-gonic/gin"
)

// AI 识别配置端点（对齐 tgto123 /api/ai-media-parser/* 形状；
// diy-strm 的 AI 配置本体在刮削设置 ScrapeSettings.Ai*，整理链已挂 IdentifyFileWithAI 兜底）

// GetAiMediaParserConfigAPI GET /api/ai-media-parser/config — 只读映射刮削 AI 配置
func GetAiMediaParserConfigAPI(c *gin.Context) {
	s := models.GlobalScrapeSettings
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{
		"config": gin.H{
			"enabled":     s.EnableAi != models.AiActionOff,
			"api_url":     s.GetAiBaseUrl(),
			"model":       s.AiModelName,
			"prompt":      s.AiPrompt,
			"timeout":     s.GetAiTimeout(),
			"has_api_key": s.GetAiApiKey() != "",
			"hint":        "AI 识别配置本体在「刮削设置-AI 识别」中修改；整理链在常规识别失败时自动调用 AI 兜底",
		},
	}})
}

// ClearAiMediaParserCacheAPI POST /api/ai-media-parser/cache/clear — 兼容端点
// diy-strm 的 AI 识别为即时调用（无持久缓存），保留端点做无操作成功返回。
func ClearAiMediaParserCacheAPI(c *gin.Context) {
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "AI 识别无持久缓存，无需清理"})
}
