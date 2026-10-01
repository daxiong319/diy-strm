package notifychannel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ---------------------------------------------------------------------------
// 17 个通知渠道实现（对齐 cloud-auto-save-x notification_channels_raw.js）
// ---------------------------------------------------------------------------

func init() {
	// 1. Telegram Bot
	register(ChannelMeta{
		ID: "telegram", Title: "Telegram Bot",
		Required: []string{"bot_token", "chat_id"},
		Fields: []FieldMeta{
			{Key: "bot_token", Label: "Bot Token", Type: "password"},
			{Key: "chat_id", Label: "Chat ID", Type: "text"},
			{Key: "api_host", Label: "API Host (可选)", Type: "text", Placeholder: "https://api.telegram.org", Hint: "反代/自建域名可填此处"},
			{Key: "thread_id", Label: "话题 ID (可选)", Type: "text", Hint: "论坛型群组填写后消息发送到指定话题"},
			{Key: "proxy_url", Label: "代理 (可选)", Type: "text", Placeholder: "http://127.0.0.1:7890 或 socks5://127.0.0.1:1080", Hint: "支持 http/https/socks5，可内嵌 user:pass"},
			{Key: "proxy_username", Label: "代理用户名 (可选)", Type: "text"},
			{Key: "proxy_password", Label: "代理密码 (可选)", Type: "password"},
		},
	}, sendTelegram)

	// 2. Bark (iOS)
	register(ChannelMeta{
		ID: "bark", Title: "Bark (iOS)",
		Required: []string{"device_key"},
		Note:     "正文经 POST 提交，长多行内容不再受 URL 长度限制；可只填 Key，也可直接粘贴 App 生成的完整链接。",
		Fields: []FieldMeta{
			{Key: "device_key", Label: "Device Key / URL", Type: "text", Placeholder: "https://api.day.app/<key>"},
			{Key: "group", Label: "分组", Type: "text", Placeholder: "diy-strm"},
			{Key: "sound", Label: "提示音", Type: "text", Placeholder: "minuet"},
			{Key: "level", Label: "送达级别", Type: "select", Options: []Option{
				{Value: "", Label: "默认 (active)"}, {Value: "active", Label: "active"},
				{Value: "timeSensitive", Label: "timeSensitive（专注模式仍送达）"}, {Value: "passive", Label: "passive（静默）"},
			}},
			{Key: "icon", Label: "图标 URL (可选)", Type: "text"},
			{Key: "link", Label: "点击跳转 (可选)", Type: "text"},
			{Key: "archiver", Label: "保留历史 (isArchive)", Type: "select", Options: []Option{{Value: "", Label: "否"}, {Value: "true", Label: "是"}}},
		},
	}, sendBark)

	// 3. 企业微信机器人
	register(ChannelMeta{
		ID: "wecom_bot", Title: "企业微信机器人",
		Required: []string{"webhook_key"},
		Fields: []FieldMeta{
			{Key: "webhook_key", Label: "Webhook Key", Type: "password"},
		},
	}, sendWecomBot)

	// 4. 钉钉机器人
	register(ChannelMeta{
		ID: "dingtalk", Title: "钉钉机器人",
		Required: []string{"access_token", "secret"},
		Fields: []FieldMeta{
			{Key: "access_token", Label: "Access Token", Type: "password"},
			{Key: "secret", Label: "加签密钥", Type: "password"},
		},
	}, sendDingTalk)

	// 5. Server酱
	register(ChannelMeta{
		ID: "serverchan", Title: "Server酱",
		Required: []string{"send_key"},
		Fields: []FieldMeta{
			{Key: "send_key", Label: "SendKey", Type: "password"},
		},
	}, sendServerChan)

	// 6. PushPlus
	register(ChannelMeta{
		ID: "pushplus", Title: "PushPlus",
		Required: []string{"token"},
		Fields: []FieldMeta{
			{Key: "token", Label: "Token", Type: "password"},
			{Key: "topic", Label: "群组编码 (可选)", Type: "text"},
			{Key: "template", Label: "模板", Type: "text", Placeholder: "html / markdown / cloud"},
		},
	}, sendPushPlus)

	// 7. Ntfy
	register(ChannelMeta{
		ID: "ntfy", Title: "Ntfy",
		Required: []string{"topic"},
		Fields: []FieldMeta{
			{Key: "topic", Label: "Topic", Type: "text"},
			{Key: "url", Label: "服务器 URL", Type: "text", Placeholder: "https://ntfy.sh"},
			{Key: "token", Label: "Access Token (可选)", Type: "password"},
			{Key: "priority", Label: "优先级", Type: "text", Placeholder: "3"},
		},
	}, sendNtfy)

	// 8. 自定义 Webhook
	register(ChannelMeta{
		ID: "webhook", Title: "自定义 Webhook",
		Required: []string{"url", "method"},
		Note:     "URL 与 Body 支持 Go text/template 语法（含 {{ 即启用）；不含 {{ 的旧 $title/$content 写法继续可用。",
		Fields: []FieldMeta{
			{Key: "url", Label: "URL", Type: "text", Placeholder: "https://example.com/hook"},
			{Key: "method", Label: "Method", Type: "select", Options: []Option{
				{Value: "POST", Label: "POST"}, {Value: "PUT", Label: "PUT"}, {Value: "PATCH", Label: "PATCH"},
				{Value: "GET", Label: "GET"}, {Value: "DELETE", Label: "DELETE"},
			}},
			{Key: "body", Label: "Body 模板", Type: "textarea", Rows: 6, Placeholder: `{"title":"{{.Title}}","content":"{{.Content}}"}`, Hint: `留空时 JSON 模式自动使用 {"title":...,"content":...}`},
			{Key: "content_type", Label: "Content-Type", Type: "text", Placeholder: "application/json"},
			{Key: "headers", Label: "额外 Headers (JSON)", Type: "textarea", Rows: 3, Placeholder: `{"X-Token":"xxx"}`},
		},
	}, sendWebhook)

	// 9. 邮件 SMTP
	register(ChannelMeta{
		ID: "email", Title: "邮件 (SMTP)",
		Required: []string{"host", "to"},
		Fields: []FieldMeta{
			{Key: "host", Label: "SMTP 服务器", Type: "text", Placeholder: "smtp.example.com"},
			{Key: "port", Label: "端口", Type: "text", Placeholder: "留空按加密方式推断 (ssl=465/starttls=587)"},
			{Key: "encryption", Label: "加密方式", Type: "select", Options: []Option{
				{Value: "", Label: "starttls (默认)"}, {Value: "ssl", Label: "ssl 直连 (465)"},
				{Value: "starttls", Label: "starttls (587)"}, {Value: "none", Label: "不加密 (仅限内网)"},
			}},
			{Key: "username", Label: "登录账号", Type: "text"},
			{Key: "password", Label: "登录密码/授权码", Type: "password"},
			{Key: "from", Label: "发件地址", Type: "text"},
			{Key: "from_name", Label: "发件人显示名", Type: "text", Placeholder: "diy-strm 通知"},
			{Key: "to", Label: "收件人", Type: "textarea", Rows: 2, Placeholder: "多个用英文逗号分隔"},
			{Key: "subject_prefix", Label: "主题前缀 (可选)", Type: "text", Placeholder: "【diy-strm】"},
		},
	}, sendEmail)

	// 10. 飞书机器人
	register(ChannelMeta{
		ID: "feishu", Title: "飞书机器人",
		Required: []string{"webhook_key"},
		Fields: []FieldMeta{
			{Key: "webhook_key", Label: "Webhook 地址/Key", Type: "password", Placeholder: "群设置→群机器人→自定义机器人"},
			{Key: "secret", Label: "加签密钥 (可选)", Type: "password"},
			{Key: "keyword", Label: "自定义关键词 (可选)", Type: "text"},
		},
	}, sendFeishu)

	// 11. Gotify
	register(ChannelMeta{
		ID: "gotify", Title: "Gotify",
		Required: []string{"url", "app_token"},
		Fields: []FieldMeta{
			{Key: "url", Label: "服务器 URL", Type: "text", Placeholder: "https://gotify.example.com"},
			{Key: "app_token", Label: "Application Token", Type: "password"},
			{Key: "priority", Label: "优先级 (可选)", Type: "text", Placeholder: "5"},
		},
	}, sendGotify)

	// 12. PushDeer
	register(ChannelMeta{
		ID: "pushdeer", Title: "PushDeer",
		Required: []string{"token"},
		Fields: []FieldMeta{
			{Key: "token", Label: "Token", Type: "password", Placeholder: "PDUxxxxxx"},
			{Key: "url", Label: "服务器 URL (可选)", Type: "text", Placeholder: "https://api2.pushdeer.com"},
			{Key: "content_type", Label: "消息类型", Type: "select", Options: []Option{
				{Value: "", Label: "text (默认)"}, {Value: "markdown", Label: "markdown"}, {Value: "html", Label: "html"},
			}},
		},
	}, sendPushDeer)

	// 13. WxPusher
	register(ChannelMeta{
		ID: "wxpusher", Title: "WxPusher",
		Required: []string{"app_token", "uids"},
		Fields: []FieldMeta{
			{Key: "app_token", Label: "APP Token", Type: "password", Placeholder: "ATxxxxxx"},
			{Key: "uids", Label: "用户 UIDs", Type: "textarea", Rows: 2, Placeholder: "多个用英文逗号分隔"},
			{Key: "topic_ids", Label: "主题 Topic IDs (可选)", Type: "text"},
		},
	}, sendWxPusher)

	// 14. go-cqhttp (OneBot)
	register(ChannelMeta{
		ID: "gocqhttp", Title: "go-cqhttp (OneBot)",
		Required: []string{"url"},
		Note:     "user_id 与 group_id 至少填一个。",
		Fields: []FieldMeta{
			{Key: "url", Label: "HTTP API 地址", Type: "text", Placeholder: "http://127.0.0.1:5700"},
			{Key: "token", Label: "Access Token (可选)", Type: "password"},
			{Key: "user_id", Label: "好友 QQ (user_id)", Type: "text"},
			{Key: "group_id", Label: "群号 (group_id)", Type: "text"},
		},
	}, sendGoCQHTTP)

	// 15. QMsg
	register(ChannelMeta{
		ID: "qmsg", Title: "QMsg 推送",
		Required: []string{"key"},
		Fields: []FieldMeta{
			{Key: "key", Label: "发送 Key", Type: "password"},
			{Key: "targets", Label: "指定 QQ/群 (可选)", Type: "text", Hint: "留空推给自己；群加 g: 前缀"},
			{Key: "type", Label: "接口类型", Type: "select", Options: []Option{
				{Value: "", Label: "qmsg (默认)"}, {Value: "qmsgpush", Label: "qmsgpush"},
			}},
		},
	}, sendQMsg)

	// 16. Synology Chat
	register(ChannelMeta{
		ID: "synology_chat", Title: "Synology Chat",
		Required: []string{"url"},
		Note:     "粘贴 DSM Chat 机器人完整的 Incoming Webhook URL。",
		Fields: []FieldMeta{
			{Key: "url", Label: "Webhook URL", Type: "password"},
			{Key: "insecure_skip_verify", Label: "跳过证书校验", Type: "select", Options: []Option{{Value: "", Label: "否"}, {Value: "true", Label: "是 (自签证书)"}}},
		},
	}, sendSynologyChat)

	// 17. 企业微信应用
	register(ChannelMeta{
		ID: "wecom_app", Title: "企业微信应用",
		Required: []string{"corp_id", "corp_secret", "agent_id"},
		Fields: []FieldMeta{
			{Key: "corp_id", Label: "企业 ID (CorpID)", Type: "text"},
			{Key: "corp_secret", Label: "应用 Secret", Type: "password"},
			{Key: "agent_id", Label: "应用 AgentId", Type: "text"},
			{Key: "to_user", Label: "接收成员 (可选)", Type: "text", Placeholder: "留空为全员；多人用 | 分隔"},
		},
	}, sendWecomApp)
}

// ---------------------------------------------------------------------------
// 渠道实现
// ---------------------------------------------------------------------------

func sendTelegram(ctx context.Context, cfg map[string]string, msg Message) error {
	host := strings.TrimSpace(cfg["api_host"])
	if host == "" {
		host = "https://api.telegram.org"
	}
	apiURL := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimSuffix(host, "/"), cfg["bot_token"])
	payload := map[string]any{
		"chat_id": cfg["chat_id"],
		"text":    msg.Title + "\n" + msg.Content,
	}
	if tid := strings.TrimSpace(cfg["thread_id"]); tid != "" {
		payload["message_thread_id"] = tid
	}
	return httpPostJSON(ctx, httpClient, apiURL, payload)
}

func sendBark(ctx context.Context, cfg map[string]string, msg Message) error {
	key := cfg["device_key"]
	if strings.HasPrefix(key, "https://") || strings.HasPrefix(key, "http://") {
		u, err := url.Parse(key)
		if err != nil {
			return fmt.Errorf("Bark URL 解析失败: %w", err)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) > 0 {
			key = parts[len(parts)-1]
		}
	}
	apiURL := fmt.Sprintf("https://api.day.app/%s", key)
	payload := map[string]any{
		"title":   msg.Title,
		"body":    msg.Content,
		"group":   cfg["group"],
		"sound":   cfg["sound"],
		"level":   cfg["level"],
		"icon":    cfg["icon"],
		"link":    cfg["link"],
	}
	if cfg["archiver"] == "true" {
		payload["isArchive"] = "1"
	}
	return httpPostJSON(ctx, httpClient, apiURL, payload)
}

func sendWecomBot(ctx context.Context, cfg map[string]string, msg Message) error {
	apiURL := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=%s", cfg["webhook_key"])
	payload := map[string]any{
		"msgtype": "text",
		"text":    map[string]any{"content": msg.Title + "\n" + msg.Content},
	}
	return httpPostJSON(ctx, httpClient, apiURL, payload)
}

func sendDingTalk(ctx context.Context, cfg map[string]string, msg Message) error {
	ts := fmt.Sprintf("%d", timeNowMillis())
	sign := hmacSHA256Hex(cfg["secret"], ts+"\n"+cfg["secret"])
	apiURL := fmt.Sprintf("https://oapi.dingtalk.com/robot/send?access_token=%s&timestamp=%s&sign=%s",
		cfg["access_token"], ts, url.QueryEscape(sign))
	payload := map[string]any{
		"msgtype": "text",
		"text":    map[string]any{"content": msg.Title + "\n" + msg.Content},
	}
	return httpPostJSON(ctx, httpClient, apiURL, payload)
}

func sendServerChan(ctx context.Context, cfg map[string]string, msg Message) error {
	apiURL := fmt.Sprintf("https://sctapi.ftqq.com/%s.send", cfg["send_key"])
	form := url.Values{"title": {msg.Title}, "desp": {msg.Content}}
	return httpPostForm(ctx, httpClient, apiURL, form)
}

func sendPushPlus(ctx context.Context, cfg map[string]string, msg Message) error {
	payload := map[string]any{
		"token":    cfg["token"],
		"title":    msg.Title,
		"content":  msg.Content,
		"template": orDefault(cfg["template"], "html"),
	}
	if t := cfg["topic"]; t != "" {
		payload["topic"] = t
	}
	return httpPostJSON(ctx, httpClient, "https://www.pushplus.plus/send", payload)
}

func sendNtfy(ctx context.Context, cfg map[string]string, msg Message) error {
	base := orDefault(cfg["url"], "https://ntfy.sh")
	apiURL := strings.TrimSuffix(base, "/") + "/" + cfg["topic"]
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(msg.Content))
	if err != nil {
		return err
	}
	req.Header.Set("Title", msg.Title)
	if p := cfg["priority"]; p != "" {
		req.Header.Set("Priority", p)
	}
	if t := cfg["token"]; t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy HTTP %d", resp.StatusCode)
	}
	return nil
}

func sendWebhook(ctx context.Context, cfg map[string]string, msg Message) error {
	method := orDefault(cfg["method"], "POST")
	bodyTemplate := cfg["body"]
	contentType := orDefault(cfg["content_type"], "application/json")

	var body string
	if strings.Contains(bodyTemplate, "{{") {
		body = simpleTemplate(bodyTemplate, msg)
	} else if bodyTemplate != "" {
		body = strings.NewReplacer("$title", msg.Title, "$content", msg.Content).Replace(bodyTemplate)
	} else {
		if strings.Contains(contentType, "json") {
			b, _ := json.Marshal(map[string]string{"title": msg.Title, "content": msg.Content})
			body = string(b)
		} else {
			body = msg.Title + "\n" + msg.Content
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, cfg["url"], strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	if h := cfg["headers"]; h != "" {
		var m map[string]string
		if json.Unmarshal([]byte(h), &m) == nil {
			for k, v := range m {
				req.Header.Set(k, v)
			}
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook HTTP %d", resp.StatusCode)
	}
	return nil
}

func simpleTemplate(tmpl string, msg Message) string {
	r := strings.NewReplacer(
		"{{.Title}}", msg.Title,
		"{{.Content}}", msg.Content,
		"{{.Tone}}", msg.Tone,
	)
	return r.Replace(tmpl)
}

func sendEmail(ctx context.Context, cfg map[string]string, msg Message) error {
	return sendEmailImpl(ctx, cfg, msg)
}

func sendFeishu(ctx context.Context, cfg map[string]string, msg Message) error {
	key := cfg["webhook_key"]
	apiURL := key
	if !strings.HasPrefix(key, "http") {
		apiURL = "https://open.feishu.cn/open-apis/bot/v2/hook/" + key
	}
	content := msg.Title + "\n" + msg.Content
	if kw := cfg["keyword"]; kw != "" {
		content = kw + " " + content
	}
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]any{"text": content},
	}
	// 加签
	if secret := cfg["secret"]; secret != "" {
		ts := fmt.Sprintf("%d", timeNowMillis()/1000)
		sign := base64.StdEncoding.EncodeToString(hmacSHA256Raw(secret, ts+"\n"+secret))
		payload["timestamp"] = ts
		payload["sign"] = sign
	}
	return httpPostJSON(ctx, httpClient, apiURL, payload)
}

func sendGotify(ctx context.Context, cfg map[string]string, msg Message) error {
	apiURL := strings.TrimSuffix(cfg["url"], "/") + "/message?token=" + cfg["app_token"]
	priority := orDefault(cfg["priority"], "5")
	payload := map[string]any{
		"title":    msg.Title,
		"message":  msg.Content,
		"priority": json.Number(priority),
	}
	return httpPostJSON(ctx, httpClient, apiURL, payload)
}

func sendPushDeer(ctx context.Context, cfg map[string]string, msg Message) error {
	base := orDefault(cfg["url"], "https://api2.pushdeer.com")
	form := url.Values{
		"pushkey": {cfg["token"]},
		"text":    {msg.Title},
		"desp":    {msg.Content},
		"type":    {orDefault(cfg["content_type"], "text")},
	}
	return httpPostForm(ctx, httpClient, strings.TrimSuffix(base, "/")+"/message/push", form)
}

func sendWxPusher(ctx context.Context, cfg map[string]string, msg Message) error {
	uids := strings.Split(cfg["uids"], ",")
	for i := range uids {
		uids[i] = strings.TrimSpace(uids[i])
	}
	payload := map[string]any{
		"appToken":    cfg["app_token"],
		"content":     msg.Content,
		"summary":     msg.Title,
		"contentType": 1,
		"uids":        uids,
	}
	return httpPostJSON(ctx, httpClient, "https://wxpusher.zjiecode.com/api/send/message", payload)
}

func sendGoCQHTTP(ctx context.Context, cfg map[string]string, msg Message) error {
	apiURL := strings.TrimSuffix(cfg["url"], "/") + "/send_private_msg"
	if gid := cfg["group_id"]; gid != "" {
		apiURL = strings.TrimSuffix(cfg["url"], "/") + "/send_group_msg"
	}
	payload := map[string]any{
		"message": msg.Title + "\n" + msg.Content,
	}
	if uid := cfg["user_id"]; uid != "" {
		payload["user_id"] = json.Number(uid)
	}
	if gid := cfg["group_id"]; gid != "" {
		payload["group_id"] = json.Number(gid)
	}
	return httpPostJSON(ctx, httpClient, apiURL, payload)
}

func sendQMsg(ctx context.Context, cfg map[string]string, msg Message) error {
	apiURL := "https://qmsg.zendee.cn/send/" + cfg["key"]
	form := url.Values{"msg": {msg.Title + "\n" + msg.Content}}
	if t := cfg["targets"]; t != "" {
		form.Set("qq", t)
	}
	return httpPostForm(ctx, httpClient, apiURL, form)
}

func sendSynologyChat(ctx context.Context, cfg map[string]string, msg Message) error {
	form := url.Values{"payload": {msg.Title + "\n" + msg.Content}}
	client := httpClient
	if cfg["insecure_skip_verify"] == "true" {
		client = httpClientInsecure
	}
	return httpPostForm(ctx, client, cfg["url"], form)
}

func sendWecomApp(ctx context.Context, cfg map[string]string, msg Message) error {
	// 获取 access_token
	tokenURL := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		cfg["corp_id"], cfg["corp_secret"])
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	r, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("企微获取 token 失败: %w", err)
	}
	defer r.Body.Close()
	var tr struct {
		AccessToken string `json:"access_token"`
		ErrCode     int    `json:"errcode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&tr); err != nil || tr.AccessToken == "" {
		return fmt.Errorf("企微获取 token 失败: errcode=%d", tr.ErrCode)
	}

	sendURL := "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=" + tr.AccessToken
	payload := map[string]any{
		"agentid": cfg["agent_id"],
		"msgtype": "text",
		"text":    map[string]any{"content": msg.Title + "\n" + msg.Content},
	}
	if tu := cfg["to_user"]; tu != "" {
		payload["touser"] = tu
	} else {
		payload["touser"] = "@all"
	}
	return httpPostJSON(ctx, httpClient, sendURL, payload)
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// sendEmailImpl SMTP 邮件发送
func sendEmailImpl(ctx context.Context, cfg map[string]string, msg Message) error {
	return sendSMTP(ctx, cfg, msg)
}
