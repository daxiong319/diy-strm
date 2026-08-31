// Package hdhive —— NanShare 中转通道客户端。
//
// hdhive.nanl.top 是 NanShare 提供的影巢（HDHive）OpenAPI 中转服务
// （协议经镜像脱壳完全还原，解密产物 _ns_out/core/hdhive_open_client.py）：
//   - 全部业务请求为 POST + 紧凑 JSON
//   - 请求签名：sign_text = "POST\n{path}\n{project_id}\n{timestamp}\n{nonce}\n{sha256(body)}"
//     signature = HMAC-SHA256(project_secret, sign_text) hex
//   - 签名头：X-NanShare-Project / X-NanShare-Timestamp / X-NanShare-Nonce
//     / X-NanShare-Body-SHA256 / X-NanShare-Signature
//   - 业务 body 需合并 {sdk_account_id, account_id}（中转按该 ID 绑定已授权账号）
//   - 中转响应形如 {"ok": true, "response": {<hdhive 标准 JSON>}}；OAuth 接口
//     (/api/nanshare/oauth/start|status|revoke) 直接返回结果对象
//   - OAuth 流程：oauth/start 拿 authorize_url → 用户在 hdhive.com 授权 → 浏览器
//     回跳 return_url → 我方轮询 oauth/status 确认（凭据由中转托管，本机不存 Token）
package hdhive

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// NanShare 中转常量
const (
	DefaultNanShareBaseURL  = "https://hdhive.nanl.top"
	// (项目 ID 移至下方 var，支持环境变量覆盖)
	nanshareRequestTimeout  = 15 * time.Second
	nanshareOAuthStartPath  = "/api/nanshare/oauth/start"
	nanshareOAuthStatusPath = "/api/nanshare/oauth/status"
	nanshareResourcesPath   = "/api/nanshare/open/resources"
	nanshareShareDetailPath = "/api/nanshare/open/share-detail"
	nanshareUnlockPath      = "/api/nanshare/open/unlock"
)

// nanshareProjectSecret 项目签名密钥（两段拼接，镜像 config 明文，可用环境变量覆盖）
var nanshareProjectSecret = "6_u3buyOMqP0EBvVFHbxEaJ1DVxihn9Td" + "MgZbQdOPNrtpUFNjmbsDs7a2adwLo0I"

// nanshareProjectID 项目标识（env HDHIVE_NANSHARE_PROJECT_ID 可覆盖）
var nanshareProjectID = "nanshare-main"

func init() {
	if v := strings.TrimSpace(os.Getenv("HDHIVE_NANSHARE_SECRET")); v != "" {
		nanshareProjectSecret = v
	}
	if v := strings.TrimSpace(os.Getenv("HDHIVE_NANSHARE_PROJECT_ID")); v != "" {
		nanshareProjectID = v
	}
}

// NanShareClient NanShare 中转通道客户端（有状态：持有一个账号的 sdk_account_id）
type NanShareClient struct {
	BaseURL      string
	ProjectID    string
	SDKAccountID string // 中转侧账号标识（本端生成，授权与业务调用一致）
	HTTP         *http.Client
}

// NewNanShareClient 创建 NanShare 通道客户端
func NewNanShareClient(sdkAccountID string) *NanShareClient {
	base := DefaultNanShareBaseURL
	if v := strings.TrimSpace(os.Getenv("HDHIVE_NANSHARE_BASE_URL")); v != "" {
		base = strings.TrimRight(v, "/")
	}
	return &NanShareClient{
		BaseURL:      base,
		ProjectID:    nanshareProjectID,
		SDKAccountID: sdkAccountID,
		HTTP:         &http.Client{Timeout: nanshareRequestTimeout},
	}
}

// NewNanShareSDKAccountID 生成新的 sdk_account_id（与 NanShare 前端 hdhive_{uuid8} 风格一致，
// 用 32 位 hex 保证唯一性）
func NewNanShareSDKAccountID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "diystrm_" + hex.EncodeToString(b)
}

// nanshareRelayResult 中转响应外壳
type nanshareRelayResult struct {
	OK             bool            `json:"ok"`
	Code           string          `json:"code"`
	Message        string          `json:"message"`
	Error          string          `json:"error"`
	Description    string          `json:"description"`
	ReauthRequired bool            `json:"reauth_required"`
	Response       json.RawMessage `json:"response"`
	// OAuth 接口字段（直接平铺在结果对象上）
	AuthorizeURL  string          `json:"authorize_url"`
	AuthorizeURL2 string          `json:"authorizeURL"`
	Authorized    *bool           `json:"authorized"`
	Account       json.RawMessage `json:"account"`
}

// accountPayload 业务调用需合并的账号标识
func (c *NanShareClient) accountPayload() map[string]any {
	return map[string]any{"sdk_account_id": c.SDKAccountID, "account_id": c.SDKAccountID}
}

// signedPost 签名 POST（全部接口均为 POST）
func (c *NanShareClient) signedPost(ctx context.Context, path string, body map[string]any) (*nanshareRelayResult, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	nonceHex := hex.EncodeToString(nonce)
	bodySHA := sha256.Sum256(payload)
	bodySHAHex := hex.EncodeToString(bodySHA[:])
	signText := strings.Join([]string{"POST", path, c.ProjectID, ts, nonceHex, bodySHAHex}, "\n")
	mac := hmac.New(sha256.New, []byte(nanshareProjectSecret))
	mac.Write([]byte(signText))
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "QMediaSync/HDHive-SDK")
	req.Header.Set("X-NanShare-Project", c.ProjectID)
	req.Header.Set("X-NanShare-Timestamp", ts)
	req.Header.Set("X-NanShare-Nonce", nonceHex)
	req.Header.Set("X-NanShare-Body-SHA256", bodySHAHex)
	req.Header.Set("X-NanShare-Signature", signature)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var result nanshareRelayResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("解析 NanShare 中转响应失败：%w", err)
	}
	if resp.StatusCode >= 400 && !result.OK {
		if result.Code == "" {
			result.Code = fmt.Sprintf("%d", resp.StatusCode)
		}
		result.OK = false
	}
	return &result, resp.StatusCode, nil
}

// toAPIResponse 将中转结果规整为统一 OAuthAPIResponse：
// ok 且 response 为对象 → response 即 hdhive 标准响应；其余构造失败响应
func toAPIResponseFromNanShare(result *nanshareRelayResult, statusCode int) *OAuthAPIResponse {
	if result.OK && len(result.Response) > 0 && strings.TrimSpace(string(result.Response)) != "null" {
		var inner OAuthAPIResponse
		if err := json.Unmarshal(result.Response, &inner); err == nil && (inner.Success || inner.Message != "" || len(inner.Data) > 0) {
			inner.StatusCode = statusCode
			return &inner
		}
		return &OAuthAPIResponse{Success: true, Data: result.Response, StatusCode: statusCode, Message: "OK"}
	}
	msg := firstNonEmptyStr(result.Message, result.Error, result.Description, result.Code)
	if msg == "" {
		msg = "NanShare 中转请求失败"
	}
	out := &OAuthAPIResponse{Success: false, Code: result.Code, Message: msg, StatusCode: statusCode}
	if result.ReauthRequired {
		f := true
		out.AuthRequired = &f
	}
	return out
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// openCall 业务调用（资源/详情/解锁）
func (c *NanShareClient) openCall(ctx context.Context, path string, payload map[string]any) (*OAuthAPIResponse, error) {
	body := c.accountPayload()
	for k, v := range payload {
		body[k] = v
	}
	result, statusCode, err := c.signedPost(ctx, path, body)
	if err != nil {
		return nil, err
	}
	return toAPIResponseFromNanShare(result, statusCode), nil
}

// ------------------------- ChannelClient 实现 -------------------------

// Ping 用 oauth/status 验证项目签名与账号绑定可用性
func (c *NanShareClient) Ping(ctx context.Context) error {
	if c.SDKAccountID == "" {
		return errors.New("nanshare 通道未配置账号标识")
	}
	resp, err := c.OAuthStatus(ctx)
	if err != nil {
		return err
	}
	if resp == nil {
		return errors.New("nanshare 通道状态查询无响应")
	}
	return nil
}

// Me nanshare 中转未开放独立 /me 端点：用 oauth/status 代替，
// authorized=false 时返回 Success=false（通用授权状态流程据此判定未授权）
func (c *NanShareClient) Me(ctx context.Context) (*OAuthAPIResponse, error) {
	resp, err := c.OAuthStatus(ctx)
	if err != nil {
		return nil, err
	}
	payload, perr := ParseNanShareStatus(resp)
	if perr != nil {
		return &OAuthAPIResponse{Success: false, Message: "解析授权状态失败：" + perr.Error()}, nil
	}
	if payload.Authorized == nil || !*payload.Authorized {
		return &OAuthAPIResponse{Success: false, Message: "暂未授权（请在授权页完成 hdhive.com 授权）", Data: payload.Account}, nil
	}
	return &OAuthAPIResponse{Success: true, Message: "OK", Data: payload.Account}, nil
}

// GetResources 按 TMDB 查询资源列表
func (c *NanShareClient) GetResources(ctx context.Context, mediaType, tmdbID string) (*OAuthAPIResponse, error) {
	if mediaType != "movie" && mediaType != "tv" {
		return &OAuthAPIResponse{Success: false, Message: "无效的 media_type: " + mediaType}, nil
	}
	return c.openCall(ctx, nanshareResourcesPath, map[string]any{"media_type": mediaType, "tmdb_id": tmdbID})
}

// GetShareDetail 获取分享详情（解锁前积分预检）
func (c *NanShareClient) GetShareDetail(ctx context.Context, slug string) (*OAuthAPIResponse, error) {
	return c.openCall(ctx, nanshareShareDetailPath, map[string]any{"slug": slug})
}

// UnlockResource 解锁资源
func (c *NanShareClient) UnlockResource(ctx context.Context, slug string) (*OAuthAPIResponse, error) {
	return c.openCall(ctx, nanshareUnlockPath, map[string]any{"slug": slug})
}

// Checkin nanshare 中转未开放签到端点
func (c *NanShareClient) Checkin(ctx context.Context, isGambler bool) (*OAuthAPIResponse, error) {
	return nil, errors.New("nanshare 通道不支持签到")
}

// ------------------------- OAuth 流程 -------------------------

// OAuthStart 发起授权，返回 authorize_url（用户在浏览器完成 hdhive.com 授权后回跳 return_url）
func (c *NanShareClient) OAuthStart(ctx context.Context, name, returnURL string) (string, error) {
	if c.SDKAccountID == "" {
		return "", errors.New("nanshare 通道未配置账号标识")
	}
	body := c.accountPayload()
	body["name"] = name
	body["return_url"] = returnURL
	result, _, err := c.signedPost(ctx, nanshareOAuthStartPath, body)
	if err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("%s", firstNonEmptyStr(result.Message, result.Error, result.Description, result.Code, "发起授权失败"))
	}
	url1 := firstNonEmptyStr(result.AuthorizeURL, result.AuthorizeURL2)
	if url1 != "" {
		return url1, nil
	}
	// authorize_url 可能包在 response 里
	if len(result.Response) > 0 {
		var inner struct {
			AuthorizeURL  string `json:"authorize_url"`
			AuthorizeURL2 string `json:"authorizeURL"`
		}
		if json.Unmarshal(result.Response, &inner) == nil {
			if u := firstNonEmptyStr(inner.AuthorizeURL, inner.AuthorizeURL2); u != "" {
				return u, nil
			}
		}
	}
	return "", errors.New("未取得授权地址")
}

// OAuthStatus 查询账号授权状态（授权结果由中转托管，本机凭据仅 sdk_account_id）
func (c *NanShareClient) OAuthStatus(ctx context.Context) (*OAuthAPIResponse, error) {
	if c.SDKAccountID == "" {
		return &OAuthAPIResponse{Success: false, Message: "nanshare 通道未配置账号标识"}, nil
	}
	result, statusCode, err := c.signedPost(ctx, nanshareOAuthStatusPath, c.accountPayload())
	if err != nil {
		return nil, err
	}
	if result.OK {
		// 状态结果直接透传（含 authorized/account 字段），供前端与落库逻辑解析
		raw, _ := json.Marshal(result)
		return &OAuthAPIResponse{Success: true, Data: raw, StatusCode: statusCode, Message: firstNonEmptyStr(result.Message, "OK")}, nil
	}
	return toAPIResponseFromNanShare(result, statusCode), nil
}

// NanShareStatusPayload 解析 oauth/status 的数据部分
type NanShareStatusPayload struct {
	Authorized     *bool           `json:"authorized"` // 兼容旧格式（中转当前版本不在顶层返回）
	Account        json.RawMessage `json:"account"`    // 中转账号对象（含 oauth_status）
	ReauthRequired bool            `json:"reauth_required"`
}

// nanShareAccountState 中转 account 对象内的授权状态字段
type nanShareAccountState struct {
	OAuthStatus    string `json:"oauth_status"`
	ReauthRequired bool   `json:"reauth_required"`
}

// ParseNanShareStatus 从 OAuthStatus 响应解析状态载荷。
// 中转把授权状态放在 account.oauth_status（authorized/pending/revoked），
// 顶层无 authorized 字段；仅在 oauth_status == "authorized" 时视为已授权。
func ParseNanShareStatus(resp *OAuthAPIResponse) (*NanShareStatusPayload, error) {
	if resp == nil || len(resp.Data) == 0 {
		return nil, errors.New("无状态数据")
	}
	var p NanShareStatusPayload
	if err := json.Unmarshal(resp.Data, &p); err != nil {
		return nil, err
	}
	// 顶层未显式给出 authorized 时，按 account.oauth_status 判定
	if p.Authorized == nil && len(p.Account) > 0 && string(p.Account) != "null" {
		var st nanShareAccountState
		if err := json.Unmarshal(p.Account, &st); err == nil {
			authorized := st.OAuthStatus == "authorized"
			p.Authorized = &authorized
			p.ReauthRequired = p.ReauthRequired || st.ReauthRequired
		}
	}
	return &p, nil
}
