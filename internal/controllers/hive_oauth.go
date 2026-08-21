package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/hdhive"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// OAuth 授权状态
// ---------------------------------------------------------------------------

// HiveOAuthStatusAPI 获取影巢 OAuth 授权状态（GET /cloud/hive/oauth/status）
// 返回主账号状态 + 授权 URL（未授权时）
func HiveOAuthStatusAPI(c *gin.Context) {
	acc, err := models.GetHiveMainAccount()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取主账号失败：" + err.Error(), Data: nil})
		return
	}
	pub := acc.Public()
	data := gin.H{
		"account": pub,
	}
	if !pub.Authorized {
		client := hdhive.NewOAuthClient(acc.InstallID)
		data["auth_url"] = client.BuildAuthURL()
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: data})
}

// HiveOAuthStatusByAccount 刷新单个账号的授权状态（token_status + me），并落库
// 返回是否成功授权、更新后的公共账号信息与错误信息
func HiveOAuthStatusByAccount(ctx context.Context, acc *models.HiveOAuthAccount) (*models.PublicHiveAccount, bool, string) {
	client := hdhive.NewOAuthClient(acc.InstallID)
	meResp, err := client.Me(ctx)
	if err != nil {
		return acc.Public(), false, err.Error()
	}
	statusResp, err := client.TokenStatus(ctx)
	if err != nil {
		return acc.Public(), false, err.Error()
	}
	// 判断是否有 access token
	hasToken := false
	if statusResp.HasAccessToken != nil {
		hasToken = *statusResp.HasAccessToken
	}
	if !hasToken && len(statusResp.Data) > 0 {
		var d struct {
			HasAccessToken *bool `json:"has_access_token"`
		}
		if json.Unmarshal(statusResp.Data, &d) == nil && d.HasAccessToken != nil {
			hasToken = *d.HasAccessToken
		}
	}
	authorized := meResp.Success && hasToken
	if meResp.AuthRequired != nil && *meResp.AuthRequired {
		authorized = false
	}

	now := time.Now()
	if authorized && !acc.Authorized {
		acc.AuthorizedAt = &now
	}
	acc.Authorized = authorized
	acc.UserFetchedAt = &now
	if len(meResp.Data) > 0 {
		acc.UserInfo = string(meResp.Data)
	}
	if len(statusResp.Data) > 0 {
		acc.Status = string(statusResp.Data)
	} else {
		// 顶层字段回填
		if s, err := json.Marshal(statusPayload(statusResp)); err == nil {
			acc.Status = string(s)
		}
	}
	if err := models.SaveHiveAccount(acc); err != nil {
		return acc.Public(), authorized, "保存账号信息失败：" + err.Error()
	}
	if !authorized {
		return acc.Public(), false, "暂未检测到影巢 OAuth 授权结果，请完成授权后刷新状态"
	}
	return acc.Public(), true, ""
}

// statusPayload 将响应中可公开的字段转为 map
func statusPayload(resp *hdhive.OAuthAPIResponse) map[string]any {
	m := map[string]any{}
	if resp.HasAccessToken != nil {
		m["has_access_token"] = *resp.HasAccessToken
	}
	if resp.ExpiresAt != nil {
		m["expires_at"] = *resp.ExpiresAt
	}
	if resp.ExpiresInSeconds != nil {
		m["expires_in_seconds"] = *resp.ExpiresInSeconds
	}
	if resp.RefreshExpiresAt != nil {
		m["refresh_expires_at"] = *resp.RefreshExpiresAt
	}
	if resp.RefreshExpiresInSec != nil {
		m["refresh_expires_in_seconds"] = *resp.RefreshExpiresInSec
	}
	if resp.InstallHash != "" {
		m["install_hash"] = resp.InstallHash
	}
	return m
}

// HiveOAuthRefreshAPI 刷新主账号授权状态（POST /cloud/hive/oauth/refresh）
func HiveOAuthRefreshAPI(c *gin.Context) {
	acc, err := models.GetHiveMainAccount()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取主账号失败：" + err.Error(), Data: nil})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	pub, ok, msg := HiveOAuthStatusByAccount(ctx, acc)
	if !ok {
		client := hdhive.NewOAuthClient(acc.InstallID)
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{
			"authorized": false,
			"account":    pub,
			"auth_url":   client.BuildAuthURL(),
		}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "授权状态已刷新", Data: gin.H{"authorized": true, "account": pub}})
}

// HiveOAuthAuthURLAPI 生成主账号授权 URL（POST /cloud/hive/oauth/auth-url）
func HiveOAuthAuthURLAPI(c *gin.Context) {
	acc, err := models.GetHiveMainAccount()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取主账号失败：" + err.Error(), Data: nil})
		return
	}
	client := hdhive.NewOAuthClient(acc.InstallID)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "生成成功", Data: gin.H{"auth_url": client.BuildAuthURL()}})
}

// ---------------------------------------------------------------------------
// 签到
// ---------------------------------------------------------------------------

// HiveCheckinAPI 手动签到（POST /cloud/hive/oauth/checkin {mode: daily|gamble, account_id?}）
// account_id 为空时签到主账号
func HiveCheckinAPI(c *gin.Context) {
	var req struct {
		Mode      string `json:"mode"`
		AccountID uint   `json:"account_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	mode := hdhive.ResolveCheckinMode(req.Mode)
	var acc *models.HiveOAuthAccount
	var err error
	if req.AccountID > 0 {
		acc, err = models.GetHiveAccountByID(req.AccountID)
	} else {
		acc, err = models.GetHiveMainAccount()
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号不存在：" + err.Error(), Data: nil})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	ok, msg := RunHiveCheckin(ctx, acc, mode)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{"success": false, "account_id": acc.ID}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{"success": true, "account_id": acc.ID}})
}

// RunHiveCheckin 执行单个账号签到并落库（供 API 与定时任务共用）
func RunHiveCheckin(ctx context.Context, acc *models.HiveOAuthAccount, mode hdhive.CheckinMode) (bool, string) {
	client := hdhive.NewOAuthClient(acc.InstallID)
	// 先检查授权状态
	statusResp, err := client.TokenStatus(ctx)
	if err != nil {
		return false, "签到失败：" + err.Error()
	}
	hasToken := false
	if statusResp.HasAccessToken != nil {
		hasToken = *statusResp.HasAccessToken
	}
	if !hasToken && len(statusResp.Data) > 0 {
		var d struct {
			HasAccessToken *bool `json:"has_access_token"`
		}
		if json.Unmarshal(statusResp.Data, &d) == nil && d.HasAccessToken != nil {
			hasToken = *d.HasAccessToken
		}
	}
	if !hasToken {
		now := time.Now()
		acc.LastCheckinAt = &now
		acc.LastCheckinOK = false
		acc.LastCheckinMsg = "未授权，请在影巢设置中完成 OAuth 授权"
		acc.LastCheckinMode = string(mode)
		_ = models.SaveHiveAccount(acc)
		return false, acc.LastCheckinMsg
	}

	resp, err := client.Checkin(ctx, mode == hdhive.CheckinModeGamble)
	if err != nil {
		now := time.Now()
		acc.LastCheckinAt = &now
		acc.LastCheckinOK = false
		acc.LastCheckinMsg = "签到请求失败：" + err.Error()
		acc.LastCheckinMode = string(mode)
		_ = models.SaveHiveAccount(acc)
		return false, acc.LastCheckinMsg
	}

	// 解析响应消息
	msg := firstNonEmpty(resp.Message, resp.Description)
	checkedInToday := false
	if len(resp.Data) > 0 {
		var d struct {
			Message     string `json:"message"`
			Description string `json:"description"`
			CheckedIn   *bool  `json:"checked_in"`
		}
		if json.Unmarshal(resp.Data, &d) == nil {
			msg = firstNonEmpty(msg, d.Message, d.Description)
			checkedInToday = d.CheckedIn != nil && *d.CheckedIn
		}
	}
	success := hdhive.IsCheckinSuccess(resp.Code, &checkedInToday, msg)
	finalMsg := "签到成功"
	if !success {
		finalMsg = msg
		if finalMsg == "" {
			finalMsg = fmt.Sprintf("签到失败（code=%s）", resp.Code)
		}
	}
	now := time.Now()
	acc.LastCheckinAt = &now
	acc.LastCheckinOK = success
	acc.LastCheckinMsg = finalMsg
	acc.LastCheckinMode = string(mode)
	// 顺带刷新用户信息（签到后积分会变化）
	if len(resp.Data) > 0 {
		client.Me(ctx) // 预热，忽略错误
	}
	_ = models.SaveHiveAccount(acc)
	return success, finalMsg
}

// HiveCheckinAllAPI 主账号 + 全部启用子账号签到（POST /cloud/hive/oauth/checkin-all {mode}）
func HiveCheckinAllAPI(c *gin.Context) {
	var req struct {
		Mode string `json:"mode"`
	}
	_ = c.ShouldBindJSON(&req)
	mode := hdhive.ResolveCheckinMode(req.Mode)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	results := []gin.H{}
	mainAcc, err := models.GetHiveMainAccount()
	if err == nil && mainAcc.Authorized {
		ok, msg := RunHiveCheckin(ctx, mainAcc, mode)
		results = append(results, gin.H{"label": mainAcc.Label, "success": ok, "message": msg})
	}
	subs, _ := models.ListHiveSubAccounts()
	for i := range subs {
		if !subs[i].Enabled {
			continue
		}
		ok, msg := RunHiveCheckin(ctx, &subs[i], mode)
		results = append(results, gin.H{"label": subs[i].Label, "success": ok, "message": msg})
	}
	msg := fmt.Sprintf("已执行 %d 个账号签到", len(results))
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{"results": results}})
}

// firstNonEmpty 返回第一个非空字符串
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 子账号管理
// ---------------------------------------------------------------------------

// HiveSubAccountsAPI 子账号列表（GET /cloud/hive/sub-accounts）
func HiveSubAccountsAPI(c *gin.Context) {
	list, err := models.ListHiveSubAccounts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "查询失败：" + err.Error(), Data: nil})
		return
	}
	out := make([]*models.PublicHiveAccount, 0, len(list))
	for i := range list {
		out = append(out, list[i].Public())
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: out})
}

// HiveSubAccountAddAPI 新增子账号（POST /cloud/hive/sub-accounts {label}）
func HiveSubAccountAddAPI(c *gin.Context) {
	var req struct {
		Label string `json:"label"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	acc, err := models.AddHiveSubAccount(req.Label)
	if err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "新增失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "子账号已创建", Data: acc.Public()})
}

// HiveSubAccountUpdateAPI 更新子账号（PUT /cloud/hive/sub-accounts/:id {label, enabled}）
func HiveSubAccountUpdateAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的账号 ID", Data: nil})
		return
	}
	var req struct {
		Label   string `json:"label"`
		Enabled *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := models.UpdateHiveSubAccount(id, req.Label, req.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "更新失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "子账号已更新", Data: nil})
}

// HiveSubAccountDeleteAPI 删除子账号（DELETE /cloud/hive/sub-accounts/:id）
func HiveSubAccountDeleteAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的账号 ID", Data: nil})
		return
	}
	if err := models.DeleteHiveSubAccount(id); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "删除失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "子账号已删除", Data: nil})
}

// HiveSubAccountAuthURLAPI 生成子账号授权 URL（POST /cloud/hive/sub-accounts/:id/auth-url）
func HiveSubAccountAuthURLAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的账号 ID", Data: nil})
		return
	}
	acc, err := models.GetHiveAccountByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "账号不存在", Data: nil})
		return
	}
	client := hdhive.NewOAuthClient(acc.InstallID)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "生成成功", Data: gin.H{"auth_url": client.BuildAuthURL()}})
}

// HiveSubAccountRefreshAPI 刷新子账号状态（POST /cloud/hive/sub-accounts/:id/refresh）
func HiveSubAccountRefreshAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的账号 ID", Data: nil})
		return
	}
	acc, err := models.GetHiveAccountByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "账号不存在", Data: nil})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	pub, ok, msg := HiveOAuthStatusByAccount(ctx, acc)
	if !ok {
		client := hdhive.NewOAuthClient(acc.InstallID)
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{
			"authorized": false,
			"account":    pub,
			"auth_url":   client.BuildAuthURL(),
		}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "状态已刷新", Data: gin.H{"authorized": true, "account": pub}})
}

// HiveSubAccountCheckinAPI 子账号签到（POST /cloud/hive/sub-accounts/:id/checkin {mode}）
func HiveSubAccountCheckinAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的账号 ID", Data: nil})
		return
	}
	acc, err := models.GetHiveAccountByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "账号不存在", Data: nil})
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	_ = c.ShouldBindJSON(&req)
	mode := hdhive.ResolveCheckinMode(req.Mode)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	ok, msg := RunHiveCheckin(ctx, acc, mode)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{"success": false, "account_id": acc.ID}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{"success": true, "account_id": acc.ID}})
}

// RunHiveDailyCheckins 定时任务：主账号 + 启用子账号每日签到
// 开关 / 签到时间 / 签到模式均读取影巢设置（与 tgto123 的 HDHIVE_CHECKIN_ENABLE/TIME/TYPE、
// HDHIVE_SUB_CHECKIN_ENABLE/TIME/TYPE 一致），不在设置时间内时直接跳过。
func RunHiveDailyCheckins() {
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if models.GetHiveCheckinEnabled() && now.Hour() == models.GetHiveCheckinHour() {
		mode := hdhive.ResolveCheckinMode(models.GetHiveCheckinMode())
		mainAcc, err := models.GetHiveMainAccount()
		if err == nil && mainAcc.Authorized {
			if ok, msg := RunHiveCheckin(ctx, mainAcc, mode); ok {
				helpers.AppLogger.Infof("影巢定时签到：主账号签到成功（%s）", msg)
			} else {
				helpers.AppLogger.Errorf("影巢定时签到：主账号签到失败：%s", msg)
			}
		}
	}

	if models.GetHiveSubCheckinEnabled() && now.Hour() == models.GetHiveSubCheckinHour() {
		mode := hdhive.ResolveCheckinMode(models.GetHiveSubCheckinMode())
		subs, _ := models.ListHiveSubAccounts()
		for i := range subs {
			if !subs[i].Enabled {
				continue
			}
			if ok, msg := RunHiveCheckin(ctx, &subs[i], mode); ok {
				helpers.AppLogger.Infof("影巢定时签到：%s 签到成功（%s）", subs[i].Label, msg)
			} else {
				helpers.AppLogger.Errorf("影巢定时签到：%s 签到失败：%s", subs[i].Label, msg)
			}
		}
	}
}

// 供定时任务与外部调用的便捷入口
var _ = strconv.Itoa