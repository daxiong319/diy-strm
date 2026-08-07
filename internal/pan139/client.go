package pan139

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"resty.dev/v3"
)

// API 地址常量（逆向自中国移动云盘 Web 端，协议参考 alist 139Yun 驱动）
const (
	// RefreshTokenURL 令牌刷新接口
	RefreshTokenURL = "https://aas.caiyun.feixin.10086.cn:443/tellin/authTokenRefresh.do"
	// RouteQueryURL 个人云路由查询接口
	RouteQueryURL = "https://user-njs.yun.139.com/user/route/qryRoutePolicy"

	// RefreshThreshold 有效期小于该值时自动刷新（15 天）
	RefreshThreshold = 1000 * 60 * 60 * 24 * 15
)

// Client 中国移动云盘（139）逆向 API 客户端
// 认证方式：Authorization（base64 编码的 Basic 凭据，内含令牌与过期时间，到期自动刷新）
type Client struct {
	accountID     uint
	authorization string
	account       string // 解码 Authorization 得到的账号（手机号/邮箱）
	personalHost  string // 个人云 API 域名（路由查询获得）

	// authChanged 令牌刷新后的回调（用于持久化新 Authorization）
	authChanged func(newAuth string)

	tokenMu sync.RWMutex
	hostMu  sync.RWMutex
	client  *resty.Client

	limiterLock sync.RWMutex
	limiters    map[string]*rate.Limiter
}

// NewClient 创建中国移动云盘客户端
// authorization 为 Web 端抓取的 Authorization（base64 编码，格式 accountId:account:token|...|过期毫秒时间戳）
func NewClient(accountID uint, authorization string) *Client {
	client := resty.New()
	client.SetTimeout(60 * time.Second)
	client.SetRetryCount(0)
	return &Client{
		accountID:     accountID,
		authorization: strings.TrimSpace(authorization),
		client:        client,
		limiters:      make(map[string]*rate.Limiter),
	}
}

// SetAuthorization 设置授权凭据（从数据库恢复会话时使用）
func (c *Client) SetAuthorization(auth string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.authorization = strings.TrimSpace(auth)
	// 凭据变更后需重新解析账号与刷新个人云域名
	c.account = ""
	c.hostMu.Lock()
	c.personalHost = ""
	c.hostMu.Unlock()
}

// GetAuthorization 获取当前授权凭据
func (c *Client) GetAuthorization() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.authorization
}

// SetAuthChanged 设置令牌刷新回调（用于将新 Authorization 持久化到数据库）
func (c *Client) SetAuthChanged(cb func(newAuth string)) {
	c.authChanged = cb
}

// GetAccount 返回解析出的账号名（手机号/邮箱）
// 未解析过时返回空字符串
func (c *Client) GetAccount() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.account
}

// SetRateLimit 为指定 API 路径设置 QPS 限流
func (c *Client) SetRateLimit(path string, qps int) {
	c.limiterLock.Lock()
	defer c.limiterLock.Unlock()
	if qps <= 0 {
		qps = 2
	}
	c.limiters[path] = rate.NewLimiter(rate.Limit(qps), 1)
}

// waitForPermission 等待限流许可
func (c *Client) waitForPermission(ctx context.Context, path string) error {
	c.limiterLock.RLock()
	limiter, exists := c.limiters[path]
	c.limiterLock.RUnlock()
	if exists {
		return limiter.Wait(ctx)
	}
	return nil
}

// Close 关闭底层 HTTP 客户端
func (c *Client) Close() error {
	if c.client != nil {
		c.client.Close()
	}
	return nil
}

// authInfo Authorization 解析结果
type authInfo struct {
	authorization string // 原始凭据（base64）
	account       string
	token         string // token|xxx|xxx|expiration 中的第一段
	expiration    int64  // 毫秒时间戳
}

// parseAuthorization 解析当前客户端 Authorization 凭据
func (c *Client) parseAuthorization() (*authInfo, error) {
	return parseAuthorizationValue(c.GetAuthorization())
}

// parseAuthorizationValue 解析 Authorization 凭据（包级，扫码登录复用）
// 格式：base64(accountId:account:token|xxx|xxx|过期毫秒时间戳)，至少 3 段且 token 段至少 4 个子段
func parseAuthorizationValue(auth string) (*authInfo, error) {
	auth = strings.TrimSpace(auth)
	if auth == "" {
		return nil, fmt.Errorf("中国移动云盘 Authorization 为空")
	}
	decoded, err := base64.StdEncoding.DecodeString(auth)
	if err != nil {
		return nil, fmt.Errorf("中国移动云盘 Authorization 解码失败：%v", err)
	}
	splits := strings.Split(string(decoded), ":")
	if len(splits) < 3 {
		return nil, fmt.Errorf("中国移动云盘 Authorization 格式无效（不足 3 段）")
	}
	strs := strings.Split(splits[2], "|")
	if len(strs) < 4 {
		return nil, fmt.Errorf("中国移动云盘 Authorization 格式无效（token 段不足 4 个子段）")
	}
	expiration, err := parseMilliTimestamp(strs[3])
	if err != nil {
		return nil, fmt.Errorf("中国移动云盘 Authorization 过期时间无效：%v", err)
	}
	return &authInfo{
		authorization: auth,
		account:       splits[1],
		token:         strs[0],
		expiration:    expiration,
	}, nil
}

// ensureAuth 确保 Authorization 有效：剩余有效期不足时自动刷新
func (c *Client) ensureAuth(ctx context.Context) error {
	info, err := c.parseAuthorization()
	if err != nil {
		return err
	}
	c.tokenMu.RLock()
	account := c.account
	c.tokenMu.RUnlock()
	if account == "" {
		c.tokenMu.Lock()
		c.account = info.account
		c.tokenMu.Unlock()
	}

	remain := info.expiration - time.Now().UnixMilli()
	if remain > RefreshThreshold {
		// 有效期充足，无需刷新
		return nil
	}
	if remain < 0 {
		return fmt.Errorf("中国移动云盘 Authorization 已过期，请重新获取")
	}
	return c.refresh(ctx, info)
}

// refresh 调用刷新接口获取新令牌
func (c *Client) refresh(ctx context.Context, info *authInfo) error {
	reqBody := "<root><token>" + info.token + "</token><account>" + info.account + "</account><clienttype>656</clienttype></root>"
	res, err := c.client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/xml").
		SetBody(reqBody).
		Post(RefreshTokenURL)
	if err != nil {
		return fmt.Errorf("中国移动云盘刷新令牌请求失败：%v", err)
	}
	defer res.Body.Close()
	var resp RefreshTokenResp
	if err := xml.Unmarshal(res.Bytes(), &resp); err != nil {
		return fmt.Errorf("中国移动云盘刷新令牌响应解析失败：%v", err)
	}
	if resp.Return != "0" || strings.TrimSpace(resp.Token) == "" {
		return fmt.Errorf("中国移动云盘刷新令牌失败：return=%s desc=%s", resp.Return, resp.Desc)
	}
	// 重新组装 Authorization
	decoded, err := base64.StdEncoding.DecodeString(c.GetAuthorization())
	if err != nil {
		return fmt.Errorf("中国移动云盘刷新令牌失败：原凭据解码失败：%v", err)
	}
	splits := strings.Split(string(decoded), ":")
	newAuth := base64.StdEncoding.EncodeToString([]byte(splits[0] + ":" + splits[1] + ":" + resp.Token))
	c.tokenMu.Lock()
	c.authorization = newAuth
	c.tokenMu.Unlock()
	if c.authChanged != nil {
		c.authChanged(newAuth)
	}
	return nil
}

// ensureHost 查询个人云 API 域名（每个账号独立，成功后缓存）
func (c *Client) ensureHost(ctx context.Context) error {
	c.hostMu.RLock()
	host := c.personalHost
	c.hostMu.RUnlock()
	if host != "" {
		return nil
	}
	if err := c.ensureAuth(ctx); err != nil {
		return err
	}
	c.tokenMu.RLock()
	account := c.account
	c.tokenMu.RUnlock()

	body := map[string]interface{}{
		"userInfo": map[string]interface{}{
			"userType":    1,
			"accountType": 1,
			"accountName": account,
		},
		"modAddrType": 1,
	}
	var resp RoutePolicyResp
	if err := c.requestRoute(ctx, body, &resp); err != nil {
		return err
	}
	for _, item := range resp.Data.RoutePolicyList {
		if item.ModName == "personal" && strings.TrimSpace(item.HttpsUrl) != "" {
			c.hostMu.Lock()
			c.personalHost = strings.TrimRight(item.HttpsUrl, "/")
			c.hostMu.Unlock()
			return nil
		}
	}
	return fmt.Errorf("中国移动云盘个人云域名查询失败：未找到 personal 路由")
}

// requestRoute 路由查询请求（使用 yun.139.com 头）
func (c *Client) requestRoute(ctx context.Context, body interface{}, out interface{}) error {
	req := c.client.R().
		SetContext(ctx).
		SetHeaders(webHeaders(c.GetAuthorization())).
		SetBody(body)
	if out != nil {
		req.SetResult(out)
	}
	res, err := req.Post(RouteQueryURL)
	if err != nil {
		return fmt.Errorf("中国移动云盘路由查询失败：%v", err)
	}
	defer res.Body.Close()
	if res.StatusCode() >= 400 {
		return fmt.Errorf("中国移动云盘路由查询失败：status=%d body=%s", res.StatusCode(), res.String())
	}
	return nil
}

// webHeaders yun.139.com 旧接口请求头（路由查询用）
func webHeaders(authorization string) map[string]string {
	return map[string]string{
		"Accept":         "application/json, text/plain, */*",
		"Content-Type":   "application/json",
		"CMS-DEVICE":     "default",
		"Authorization":  "Basic " + authorization,
		"mcloud-channel": "1000101",
		"mcloud-client":  "10701",
		"mcloud-version": "7.14.0",
		"Origin":         "https://yun.139.com",
		"Referer":        "https://yun.139.com/w/",
		"x-DeviceInfo":   "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||",
		"x-m4c-caller":   "PC",
		"x-m4c-src":      "10002",
		"x-SvcType":      "1",
	}
}

// Request 新个人盘 API 请求（自动签名 + 鉴权）
// 返回解析后的响应（调用方继续校验 Success）
func (c *Client) Request(ctx context.Context, path string, body interface{}, out interface{}) error {
	if err := c.waitForPermission(ctx, path); err != nil {
		return err
	}
	if err := c.ensureAuth(ctx); err != nil {
		return err
	}
	if err := c.ensureHost(ctx); err != nil {
		return err
	}
	c.hostMu.RLock()
	host := c.personalHost
	c.hostMu.RUnlock()

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("中国移动云盘请求参数序列化失败：%v", err)
	}
	randStr := randomString(16)
	ts := time.Now().Format("2006-01-02 15:04:05")
	sign := calSign(string(bodyBytes), ts, randStr)

	req := c.client.R().
		SetContext(ctx).
		SetHeaders(personalHeaders(c.GetAuthorization(), ts, randStr, sign)).
		SetBody(body)
	if out != nil {
		req.SetResult(out)
	}
	res, err := req.Post(host + path)
	if err != nil {
		return fmt.Errorf("中国移动云盘请求失败 %s：%v", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode() >= 400 {
		return fmt.Errorf("中国移动云盘请求失败：status=%d body=%s", res.StatusCode(), res.String())
	}
	return nil
}

// personalHeaders 新个人盘 API 请求头（含签名）
func personalHeaders(authorization, ts, randStr, sign string) map[string]string {
	return map[string]string{
		"Accept":               "application/json, text/plain, */*",
		"Content-Type":         "application/json",
		"Authorization":        "Basic " + authorization,
		"Caller":               "web",
		"Cms-Device":           "default",
		"Mcloud-Channel":       "1000101",
		"Mcloud-Client":        "10701",
		"Mcloud-Route":         "001",
		"Mcloud-Sign":          fmt.Sprintf("%s,%s,%s", ts, randStr, sign),
		"Mcloud-Version":       "7.14.0",
		"x-DeviceInfo":         "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||",
		"x-huawei-channelSrc":  "10000034",
		"x-inner-ntwk":         "2",
		"x-m4c-caller":         "PC",
		"x-m4c-src":            "10002",
		"x-SvcType":            "1",
		"X-Yun-Api-Version":    "v1",
		"X-Yun-App-Channel":    "10000034",
		"X-Yun-Channel-Source": "10000034",
		"X-Yun-Client-Info":    "||13|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||dW5kZWZpbmVk||",
		"X-Yun-Module-Type":    "100",
		"X-Yun-Svc-Type":       "1",
	}
}

// encodeURIComponent JS encodeURIComponent 等价实现
func encodeURIComponent(str string) string {
	r := url.QueryEscape(str)
	r = strings.Replace(r, "+", "%20", -1)
	r = strings.Replace(r, "%21", "!", -1)
	r = strings.Replace(r, "%27", "'", -1)
	r = strings.Replace(r, "%28", "(", -1)
	r = strings.Replace(r, "%29", ")", -1)
	r = strings.Replace(r, "%2A", "*", -1)
	return r
}

// calSign 计算 mcloud-sign 签名
// 算法：MD5(MD5(base64(字符排序后的 encodeURIComponent(body))) + MD5(ts:randStr)) 大写
func calSign(body, ts, randStr string) string {
	body = encodeURIComponent(body)
	chars := strings.Split(body, "")
	sort.Strings(chars)
	body = strings.Join(chars, "")
	body = base64.StdEncoding.EncodeToString([]byte(body))
	res := md5Hex(body) + md5Hex(ts+":"+randStr)
	return strings.ToUpper(md5Hex(res))
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// parseMilliTimestamp 解析毫秒时间戳
func parseMilliTimestamp(s string) (int64, error) {
	var v int64
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, err
	}
	return v, nil
}

// parseTime 解析 ISO8601 时间字符串为 Unix 秒
func parseTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	layouts := []string{
		"2006-01-02T15:04:05.999-07:00",
		"2006-01-02T15:04:05-07:00",
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// randomString 生成随机字符串
func randomString(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "0123456789abcdef0123456789abcdef"[:n]
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}
