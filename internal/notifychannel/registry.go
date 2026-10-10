package notifychannel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 通知渠道（对齐 cloud-auto-save-x 17 渠道完整实现）
// 每个渠道一个 Send 函数 + 渠道元数据（前端配置 schema 由 Go 侧统一提供）。
// ---------------------------------------------------------------------------

// Message 通知消息统一结构
type Message struct {
	Title   string // 标题（不含前缀）
	Content string // 多行正文
	Tone    string // info/success/warn/error

	// Scene / SceneLabel 是 T12 起注入的场景字段，供渠道模板引用。
	//
	// 为什么不让渠道自己去查 category→场景的映射：那张表住在
	// internal/domain，而 17 个渠道只认 Message。让每个渠道各自 import
	// domain 去查，等于把场景判定复制了 17 份。发送侧查一次传进来，
	// 渠道侧就只剩"把字段填进模板"这一件事。
	//
	// 未归入任何场景的通知（见 domain.UnmappedCategories）两个字段都留空 ——
	// 模板里写了 {{.Scene}} 会渲染成空串，而不是把整条 `{{.Scene}}` 原样吐出来。
	Scene      string
	SceneLabel string
}

// ChannelWebhook 自定义 Webhook 渠道的 ID。
//
// 单独提成常量是因为补发队列（outbox）只对这一个渠道生效，
// 判定散落在 dispatcher 里用字面量 "webhook" 迟早会写错。
const ChannelWebhook = "webhook"

// ChannelMeta 渠道元数据（驱动前端配置表单渲染）
type ChannelMeta struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Required []string    `json:"required"`
	Note     string      `json:"note,omitempty"`
	Fields   []FieldMeta `json:"fields"`
}

type FieldMeta struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"text"` // text/password/select/textarea
	Placeholder string   `json:"placeholder,omitempty"`
	Hint        string   `json:"hint,omitempty"`
	Rows        int      `json:"rows,omitempty"`    // textarea
	Options     []Option `json:"options,omitempty"` // select
}

type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// httpClient 全局共享（带合理超时）
var httpClient = &http.Client{Timeout: 15 * time.Second}

// httpClientInsecure 跳过证书校验的客户端（自签证书场景）
var httpClientInsecure = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

// Sender 渠道发送函数签名
type Sender func(ctx context.Context, cfg map[string]string, msg Message) error

// Registry 渠道注册表
var Registry = map[string]struct {
	Meta ChannelMeta
	Send Sender
}{}

func register(meta ChannelMeta, send Sender) {
	Registry[meta.ID] = struct {
		Meta ChannelMeta
		Send Sender
	}{Meta: meta, Send: send}
}

// ChannelsMeta 返回全部渠道元数据（前端渲染配置表单）
func ChannelsMeta() []ChannelMeta {
	out := make([]ChannelMeta, 0, len(Registry))
	for _, ch := range Registry {
		out = append(out, ch.Meta)
	}
	return out
}

// Send 通过指定渠道发送通知
func Send(ctx context.Context, channelID string, cfg map[string]string, msg Message) error {
	ch, ok := Registry[channelID]
	if !ok {
		// 未知渠道不可能通过补发解决（重发还是同样的渠道 ID）。
		return newConfigError(channelID, "未知通知渠道")
	}
	// 校验必填字段
	for _, req := range ch.Meta.Required {
		if strings.TrimSpace(cfg[req]) == "" {
			// 配置缺失同样不可重试：补发时用的是发送当时的快照，
			// 快照里缺什么字段，补发一万次还是缺。
			return newConfigError(channelID, "缺少必填字段 %s", req)
		}
	}
	return ch.Send(ctx, cfg, msg)
}

// httpPostJSON 发 JSON POST
// ---------------------------------------------------------------------------
// 企业微信可信 IP 自维护（T24 / N-4-a）
//
// 用包级变量而不是给每个渠道都塞一个依赖：17 个渠道的签名都被固定成
// `func(ctx, map[string]string, Message) error`，为企微一个渠道改所有签名不划算，
// 而这里要的东西只有一个「企微接口报错时的回调」。
//
// 装配层（internal/app）负责注入；没注入时 notifychannel 的行为与 T24 之前完全一致。
// ---------------------------------------------------------------------------

// WeComAPIErrorObserver 在企微自建应用调用出错时被调用一次。
//
// **只观察不接管**：回调返回后原始错误照原样返回给发送流程，
// 因为修复白名单最多让下一次调用成功，这一条消息已经丢了 ——
// 让上游重试才有机会把它补发出去。
var WeComAPIErrorObserver func(ctx context.Context, op string, err error)

// SetWeComAPIErrorObserver 注入回调（装配层用；传 nil 表示取消）。
func SetWeComAPIErrorObserver(fn func(ctx context.Context, op string, err error)) {
	WeComAPIErrorObserver = fn
}

func httpPostJSON(ctx context.Context, client *http.Client, rawURL string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateForErr(raw))
	}
	// 企微的错误全部体现在 body 的 errcode 上，HTTP 状态码几乎总是 200。
	// 原来这里直接丢弃 body，于是 60020（出口 IP 不在可信列表）会被当成成功 ——
	// 用户看到「通知已发送」，实际一条也没到，而且没有任何地方会报错。
	if wecomErr := parseWeComErr(raw); wecomErr != nil {
		return wecomErr
	}
	return nil
}

// truncateForErr 截断错误响应体，避免把整页 HTML 塞进错误信息。
func truncateForErr(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 512 {
		return s[:512] + "…"
	}
	return s
}

// parseWeComErr 把企微响应体解析成错误；errcode 为 0 或不是 JSON 时返回 nil。
func parseWeComErr(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var body struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	if body.ErrCode == 0 {
		return nil
	}
	return &WeComAPIError{ErrCode: body.ErrCode, ErrMsg: body.ErrMsg, Body: truncateForErr(raw)}
}

// WeComAPIError 是企业微信接口的业务错误。
//
// 单独一个类型而不是裸 fmt.Errorf：可信 IP 自维护要靠 **errcode 字段**
// 判断是不是 60020，用字符串匹配错误文案是脆的 —— 企微随时可能改文案。
type WeComAPIError struct {
	ErrCode int
	ErrMsg  string
	Body    string
}

func (e *WeComAPIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("企业微信接口返回 errcode=%d errmsg=%q", e.ErrCode, e.ErrMsg)
}

// httpPostForm 发 form POST
func httpPostForm(ctx context.Context, client *http.Client, rawURL string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// hmacSHA256Hex HMAC-SHA256 十六进制
func hmacSHA256Hex(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// base64Std 标准 base64
func base64Std(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func timeNowMillis() int64 {
	return time.Now().UnixMilli()
}

func hmacSHA256Raw(secret, payload string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}
