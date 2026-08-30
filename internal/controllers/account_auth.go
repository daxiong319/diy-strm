package controllers

import (
	"net/http"

	"diy-strm/internal/authcheck"

	"github.com/gin-gonic/gin"
)

// GetAccountAuthStatusAPI 获取全部网盘账号的授权检测结果（缓存，不做实时检测）
// @Summary 获取账号授权状态
// @Description 返回各账号最近一次授权检测的结果（valid/checked_at/detail）
// @Tags 账号管理
// @Produce json
// @Success 200 {object} object
// @Router /account/auth-status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetAccountAuthStatusAPI(c *gin.Context) {
	c.JSON(http.StatusOK, APIResponse[map[uint]authcheck.Status]{
		Code:    Success,
		Message: "查询账号授权状态成功",
		Data:    authcheck.GetAll(),
	})
}

// CheckAccountAuthAPI 触发一次全量账号授权检测（同步执行，返回检测后状态）
// @Summary 检测账号授权有效性
// @Description 对全部有 Token 的账号做轻量授权校验；force=false 时跳过 5 分钟内已检测过的账号
// @Tags 账号管理
// @Produce json
// @Param force formData bool false "是否强制重新检测（默认 true）"
// @Success 200 {object} object
// @Router /account/check-auth [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func CheckAccountAuthAPI(c *gin.Context) {
	v := c.PostForm("force")
	if v == "" {
		v = c.Query("force")
	}
	force := v != "false" && v != "0"
	status := authcheck.CheckAll(force)
	c.JSON(http.StatusOK, APIResponse[map[uint]authcheck.Status]{
		Code:    Success,
		Message: "账号授权检测完成",
		Data:    status,
	})
}
