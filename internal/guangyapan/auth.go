package guangyapan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"resty.dev/v3"
)

// accountHeaders 构建账号服务请求头（逆向自光鸭云盘 Web 端）
func (c *Client) accountHeaders() map[string]string {
	return map[string]string{
		"Accept":             "application/json, text/plain, */*",
		"Content-Type":       "application/json",
		"X-Device-Model":     "chrome%2F147.0.0.0",
		"X-Device-Name":      "PC-Chrome",
		"X-Device-Sign":      "wdi10." + c.deviceID + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"X-Net-Work-Type":    "NONE",
		"X-OS-Version":       "MacIntel",
		"X-Platform-Version": "1",
		"X-Protocol-Version": "301",
		"X-Provider-Name":    "NONE",
		"X-SDK-Version":      "9.0.2",
		"X-Client-Id":        c.clientID,
		"X-Client-Version":   "0.0.1",
		"X-Device-Id":        c.deviceID,
	}
}

// apiHeaders 构建 API 服务请求头
func (c *Client) apiHeaders() map[string]string {
	return map[string]string{
		"Accept":       "application/json, text/plain, */*",
		"Content-Type": "application/json",
		"Did":          c.deviceID,
		"Dt":           "4",
	}
}

// RefreshToken 使用刷新令牌换取新的访问令牌
func (c *Client) RefreshToken(ctx context.Context) error {
	refreshToken := c.GetRefreshToken()
	if strings.TrimSpace(refreshToken) == "" {
		return errors.New("光鸭云盘 refresh_token 为空，无法刷新令牌")
	}

	res, err := c.client.R().
		SetContext(ctx).
		SetHeaders(c.accountHeaders()).
		SetBody(map[string]interface{}{
			"client_id":     c.clientID,
			"grant_type":    "refresh_token",
			"refresh_token": refreshToken,
		}).
		Post(c.accountBase + "/v1/auth/token")
	if err != nil {
		return fmt.Errorf("光鸭云盘刷新令牌请求失败：%w", err)
	}
	defer res.Body.Close()

	var resp TokenResp
	if err := json.Unmarshal(res.Bytes(), &resp); err != nil {
		return fmt.Errorf("光鸭云盘刷新令牌响应解析失败：%w", err)
	}
	if res.StatusCode() >= 400 || resp.Error != "" || strings.TrimSpace(resp.AccessToken) == "" {
		return fmt.Errorf("光鸭云盘刷新令牌失败：%s", resp.ErrorDesc)
	}

	c.tokenMu.Lock()
	c.accessToken = strings.TrimSpace(resp.AccessToken)
	if strings.TrimSpace(resp.RefreshToken) != "" {
		c.refreshToken = strings.TrimSpace(resp.RefreshToken)
	}
	c.tokenMu.Unlock()
	return nil
}

// GetUserInfo 获取当前登录用户信息（用于验证令牌有效性）
func (c *Client) GetUserInfo(ctx context.Context) (*UserMeResp, error) {
	res, err := c.client.R().
		SetContext(ctx).
		SetHeaders(c.accountHeaders()).
		SetHeader("Authorization", "Bearer "+c.GetAccessToken()).
		Get(c.accountBase + "/v1/user/me")
	if err != nil {
		return nil, fmt.Errorf("光鸭云盘获取用户信息请求失败：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode() >= 400 {
		return nil, fmt.Errorf("光鸭云盘获取用户信息失败：status=%d body=%s", res.StatusCode(), res.String())
	}
	var me UserMeResp
	if err := json.Unmarshal(res.Bytes(), &me); err != nil {
		return nil, fmt.Errorf("光鸭云盘获取用户信息响应解析失败：%w", err)
	}
	if strings.TrimSpace(me.Sub) == "" {
		return nil, errors.New("光鸭云盘获取用户信息失败：返回用户标识为空")
	}
	return &me, nil
}

// Request 统一 API 请求方法：附带鉴权头，401/403 时自动刷新令牌后重试一次
// 返回解析后的通用响应（调用方继续校验 Code/Msg）
func (c *Client) Request(ctx context.Context, path string, body interface{}, out interface{}) error {
	if err := c.waitForPermission(ctx, path); err != nil {
		return err
	}

	do := func() (*resty.Response, error) {
		req := c.client.R().
			SetContext(ctx).
			SetHeaders(c.apiHeaders()).
			SetHeader("Authorization", "Bearer "+c.GetAccessToken())
		if body != nil {
			req.SetBody(body)
		}
		if out != nil {
			req.SetResult(out)
		}
		return req.Post(c.apiBase + path)
	}

	res, err := do()
	if err != nil {
		return fmt.Errorf("光鸭云盘请求失败 %s：%w", path, err)
	}
	defer res.Body.Close()

	if res.StatusCode() == http.StatusUnauthorized || res.StatusCode() == http.StatusForbidden {
		if err := c.RefreshToken(ctx); err != nil {
			return err
		}
		res, err = do()
		if err != nil {
			return fmt.Errorf("光鸭云盘请求失败 %s：%w", path, err)
		}
		defer res.Body.Close()
	}
	if res.StatusCode() >= 400 {
		return fmt.Errorf("光鸭云盘请求失败：status=%d body=%s", res.StatusCode(), res.String())
	}
	return nil
}
