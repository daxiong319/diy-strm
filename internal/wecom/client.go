// Package wecom 处理企业微信的入站能力：可信 IP 自维护与智能机器人。
//
// 这个包里有两件互不相干但同属企业微信的事，刻意放在一起：
//
//   - 可信 IP（N-4-a）：自建应用换出口 IP 后被 60020 拒掉，这里自动把新出口补进白名单。
//   - 智能机器人（N-4-b）：接收入站消息。
//
// 放在一个包里是因为它们共用同一套企业微信凭证（corp_id + secret）和同一个
// access_token 缓存。分成两个包的话，同一份 token 要缓存两遍、刷新逻辑要写两遍，
// 而这两处一旦分叉，症状是「消息能发出去、但收不到」这类极难查的现象。
package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 默认企微 API 根地址。
const defaultAPIHost = "https://qyapi.weixin.qq.com"

// errCodeNotAllowedIP 是企业微信「当前出口 IP 不在可信列表」的错误码。
//
// 它的文案是 `not allow to access from your ip`，返回时**不带当前出口 IP** ——
// 这一点决定了本包的核心策略，见 TrustedIPService.ReportAPIError。
const ErrCodeNotAllowedIP = 60020

// ErrCodeInvalidMediaID 之类的其它错误码本包不关心；这里只把 60020 提出来，
// 因为它是唯一一个「机器可以自己修」的错。
type APIError struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
	// Body 是原始响应体（已截断）。留它是因为企微的 errmsg 时常只有
	// 「not allow to access from your ip」这种没有上下文的短句，
	// 而排查时唯一有用的信息恰恰是当时调的是哪个接口。
	Body string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("企业微信接口返回 errcode=%d errmsg=%q", e.ErrCode, e.ErrMsg)
}

// IsNotAllowedIP 判断一个错误是不是 60020。
//
// 用 errors.As 而不是类型断言：调用链中间隔着一层 fmt.Errorf("%w") 是常态
// （比如 HTTP 层的包装），断言会把真错认成不相关的错，于是自动修复永远不触发 ——
// 而用户看到的现象只是「通知偶尔发不出去」。
func IsNotAllowedIP(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.ErrCode == ErrCodeNotAllowedIP
}

// Client 是企业微信 API 的最小客户端。
//
// 只做三件事：拿 access_token、读可信 IP、写可信 IP。
// 刻意不复用 internal/notifychannel 的发送逻辑：那边是「按渠道配置发一条消息」，
// 这边是「维护一份 IP 白名单」，配置来源与生命周期都不同。
type Client struct {
	Host   string
	CorpID string
	Secret string
	HTTP   *http.Client

	// Now 可注入，便于测试。
	Now func() time.Time

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// NewClient 建一个客户端。Host 为空用官方域名。
func NewClient(corpID, secret string) *Client {
	return &Client{
		Host:   defaultAPIHost,
		CorpID: strings.TrimSpace(corpID),
		Secret: strings.TrimSpace(secret),
		HTTP:   &http.Client{Timeout: 15 * time.Second},
		Now:    time.Now,
	}
}

func (c *Client) now() time.Time {
	if c == nil || c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *Client) httpClient() *http.Client {
	if c == nil || c.HTTP == nil {
		return &http.Client{Timeout: 15 * time.Second}
	}
	return c.HTTP
}

func (c *Client) host() string {
	if c == nil || strings.TrimSpace(c.Host) == "" {
		return defaultAPIHost
	}
	return strings.TrimRight(c.Host, "/")
}

// APIError 把企微的错误响应转成 *APIError。
//
// 企微的接口在 HTTP 层几乎总是 200，出错只体现在 body 的 errcode 上，
// 所以**只看 HTTP 状态码会把每一个业务错误都当成成功** —— 这正是这个函数存在的原因。
func parseAPIError(body []byte, method string) error {
	var ae APIError
	if err := json.Unmarshal(body, &ae); err != nil {
		// 不是 JSON：返回原始文本，别假装自己懂。
		return fmt.Errorf("企业微信 %s 返回了非 JSON 响应：%s", method, truncate(string(body), 200))
	}
	if ae.ErrCode != 0 {
		ae.Body = truncate(string(body), 500)
		return &ae
	}
	return nil
}

// AccessToken 取 access_token，带内存缓存。
//
// 提前 5 分钟过期：企微返回的 access_token 有效期是 7200 秒，
// 而「正好在到期那一刻请求」是所有缓存实现最经典的 bug ——
// 宁可多刷一次，也不要在半夜的通知上翻车。
func (c *Client) AccessToken(ctx context.Context) (string, error) {
	if c == nil || c.CorpID == "" || c.Secret == "" {
		return "", fmt.Errorf("企业微信未配置 corp_id / corp_secret")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && c.now().Add(5*time.Minute).Before(c.tokenExpiry) {
		return c.accessToken, nil
	}
	u := fmt.Sprintf("%s/cgi-bin/gettoken?corpid=%s&corpsecret=%s", c.host(), url.QueryEscape(c.CorpID), url.QueryEscape(c.Secret))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("企业微信获取 access_token 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("企业微信 access_token 响应无法解析: %w", err)
	}
	if tr.ErrCode != 0 || tr.AccessToken == "" {
		return "", &APIError{ErrCode: tr.ErrCode, ErrMsg: tr.ErrMsg, Body: truncate(string(body), 500)}
	}
	c.accessToken = tr.AccessToken
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	c.tokenExpiry = c.now().Add(ttl)
	return c.accessToken, nil
}

// callTokenized 调一个需要 access_token 的 POST 接口并解出 out。
func (c *Client) callTokenized(ctx context.Context, path string, payload any, out any) error {
	token, err := c.AccessToken(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	u := c.host() + path + "?access_token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("企业微信 %s 请求失败: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := parseAPIError(raw, path); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("企业微信 %s 响应无法解析: %w", path, err)
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
