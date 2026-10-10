package tgbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client 是 Bot API 的最小客户端。
//
// 只用标准库：项目不允许新增第三方依赖，且 Bot API 只是几个 HTTP 端点，
// 引一个 SDK 换来的便利远小于它的维护面。
type Client struct {
	Host  string
	Token string
	HTTP  *http.Client
	// Logger 收到每次请求的错误，方便在管理台排查「Bot 到底发出去没有」。
	Logger func(format string, args ...any)
}

const defaultTelegramHost = "https://api.telegram.org"

// NewClient 建一个客户端。Host 为空时用官方域名。
func NewClient(token string, host string) *Client {
	return &Client{
		Host:  normalizeHost(host),
		Token: token,
		HTTP:  &http.Client{Timeout: 20 * time.Second},
	}
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return defaultTelegramHost
	}
	return strings.TrimSuffix(host, "/")
}

// SendMessageOpts 是一次发消息的可选项。
type SendMessageOpts struct {
	// ParseMode 用 "Markdown"，格式错了 Telegram 会返回 400 —— 所以正文一律走
	// 纯文本；只有 Help 这类内容可控的地方才开 Markdown。
	ParseMode string
	// ThreadID 发到话题（群组 forum）。
	ThreadID int64
}

// SendMessage 往一个会话发文本。
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, opts SendMessageOpts) error {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if opts.ParseMode != "" {
		payload["parse_mode"] = opts.ParseMode
	}
	if opts.ThreadID != 0 {
		payload["message_thread_id"] = opts.ThreadID
	}
	var out apiResponse
	if err := c.call(ctx, "sendMessage", payload, &out); err != nil {
		return err
	}
	return nil
}

// SendMessageInt64 是给需要 chat id 的地方用的薄封装。
func (c *Client) SendMessageInt64(ctx context.Context, chatID int64, text string) error {
	return c.SendMessage(ctx, chatID, text, SendMessageOpts{})
}

// SetWebhook 把 Telegram 的更新推到 url。
//
// 用 webhook 而不是长轮询：NAS 场景长轮询会占住一个连接，
// 且 Telegram 的长轮询有超时与 pending 限制，不如 webhook 干净。
func (c *Client) SetWebhook(ctx context.Context, url, secret string) error {
	payload := map[string]any{"url": url}
	if secret != "" {
		// Telegram 会把这个值原样回送在 X-Telegram-Bot-Api-Secret-Token 头里，
		// 本地据此校验。没有它的话任何人都能往这个 webhook 塞消息。
		payload["secret_token"] = secret
	}
	// 静默丢弃重复更新：Telegram 在没收到 2xx 时会重投，
	// 而用户连点两遍 /organize 就该是两个任务，不该被去重成一次。
	payload["allowed_updates"] = []string{"message"}
	var out apiResponse
	return c.call(ctx, "setWebhook", payload, &out)
}

// DeleteWebhook 摘掉 webhook（关开关时调用）。
func (c *Client) DeleteWebhook(ctx context.Context) error {
	var out apiResponse
	return c.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, &out)
}

// WebhookInfo 读当前 webhook 状态。
func (c *Client) WebhookInfo(ctx context.Context) (webhookInfo, error) {
	var out webhookInfo
	if err := c.call(ctx, "getWebhookInfo", map[string]any{}, &out); err != nil {
		return webhookInfo{}, err
	}
	return out, nil
}

func (c *Client) call(ctx context.Context, method string, payload map[string]any, out any) error {
	if c == nil || strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("tgbot: 未配置 Bot Token")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/%s", c.Host, c.Token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		c.logf("tgbot: %s 请求失败: %v", method, err)
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.logf("tgbot: %s 读响应失败: %v", method, err)
		return err
	}
	var api apiResponse
	if err := json.Unmarshal(raw, &api); err == nil && api.OK {
		if out != nil && len(api.Result) > 0 {
			_ = json.Unmarshal(api.Result, out)
		}
		return nil
	}
	desc := strings.TrimSpace(api.Description)
	if desc == "" {
		desc = strings.TrimSpace(string(raw))
	}
	err = fmt.Errorf("tgbot: %s 失败（HTTP %d）: %s", method, resp.StatusCode, desc)
	c.logf("%v", err)
	return err
}

func (c *Client) logf(format string, args ...any) {
	if c == nil || c.Logger == nil {
		return
	}
	c.Logger(format, args...)
}

func itoaInt(v int) string { return strconv.Itoa(v) }
