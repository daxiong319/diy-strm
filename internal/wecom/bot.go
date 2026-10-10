package wecom

import (
	"encoding/json"
	"strings"
)

// 企微智能机器人回调的消息结构。
//
// 这里只声明真正会用到的字段：企微每次推完整对象，
// 未声明的由 encoding/json 忽略 —— 抄一遍协议既没意义也会立刻过期。
//
// ⚠️ 字段名大小写必须与官方一致：企微回调不是大小写不敏感的
// （encoding/json 的匹配是「精确匹配或忽略大小写」，
// 但 `from.userid` 这种嵌套里的点号路径只能靠嵌套结构对上）。

// CallbackMessage 是解密后的一条消息。
type CallbackMessage struct {
	// Cmd 是回调命令（aibot_msg_callback / aibot_event_callback）。
	Cmd string `json:"cmd"`
	// MsgID 消息唯一 id。
	MsgID string `json:"msgid"`
	// ChatID 只在群聊回调里有值。
	//
	// 群准入判定就靠它：单聊没有 chatid，只能按 from.userid 判。
	ChatID string `json:"chatid"`
	// ChatType 是 single 或 group。
	ChatType string       `json:"chattype"`
	From     CallbackFrom `json:"from"`
	// ResponseURL 主动回复地址，每个只能用一次、有效期 1 小时。
	ResponseURL string `json:"response_url"`
	// ResponseCode 主动回复时要带的查询参数。
	ResponseCode string      `json:"response_code"`
	Msg          CallbackMsg `json:"msg"`
	// EventType 事件回调的类型（enter_chat / template_card_event / ...）。
	EventType string        `json:"eventtype"`
	Event     CallbackEvent `json:"event"`
}

// CallbackFrom 是发起人。
type CallbackFrom struct {
	// UserID 在机器人创建者是超管时是明文，否则是加密的 open_userid。
	// 本站不在聊天流程里解密 open_userid（那需要自建应用的 access_token），
	// 群准入因此只按 chatid 判。
	UserID string `json:"userid"`
	// OpenUserID 加密用户标识。
	OpenUserID string `json:"open_userid"`
}

// CallbackMsg 是消息体。
type CallbackMsg struct {
	Type                   string         `json:"type"`
	Text                   CallbackText   `json:"text"`
	Menu                   CallbackMenu   `json:"menu"`
	TemplateCard           map[string]any `json:"template_card"`
	Stream                 map[string]any `json:"stream"`
	Image                  map[string]any `json:"image"`
	StreamWithTemplateCard map[string]any `json:"stream_with_template_card"`
}

// CallbackText 是纯文本内容。
type CallbackText struct {
	Content string `json:"content"`
}

// CallbackMenu 是交互菜单点选（等价于 Telegram 的 /命令点选）。
type CallbackMenu struct {
	Event      CallbackMenuEvent `json:"event"`
	EventKey   string            `json:"event_key"`
	SelectedID string            `json:"selected_id"`
}

// CallbackMenuEvent 是菜单事件。
type CallbackMenuEvent struct {
	EventKey string `json:"event_key"`
}

// CallbackEvent 是事件回调的载荷。
type CallbackEvent struct {
	EventKey string `json:"event_key"`
	// Card 模板卡片事件里的卡片动作参数。
	Card CallbackCard `json:"card"`
}

// CallbackCard 是模板卡片动作。
type CallbackCard struct {
	ResponseID string `json:"response_id"`
}

// Text 返回这条消息的可读文本。
//
// **只认 text**（以及 menu 的点选事件）：企微智能机器人的被动回复里
// text 类型只用于进入会话欢迎语，普通消息是 markdown/stream。
// 但消息回调里的 msg.type=text 是正常纯文本聊天，群里 @机器人发一句话
// 就是这一路，所以它必须能取到，否则 Bot 在群里会完全不回话。
func (m CallbackMessage) Text() string {
	if s := strings.TrimSpace(m.Msg.Text.Content); s != "" {
		return s
	}
	if s := strings.TrimSpace(m.Msg.Menu.Event.EventKey); s != "" {
		return s
	}
	return ""
}

// IsGroup 报告这条消息来自群聊。
func (m CallbackMessage) IsGroup() bool {
	return m.ChatType == "group"
}

// ChatIDInt64 解析 chatid。取不到时返回 ok=false。
//
// 解析失败**不能当作「不是群」**：那等于把一个读不懂的 chatid 放给单聊路径，
// 单聊路径是按 userid 判的，于是白名单里任何一个人的群消息都会被放行。
func (m CallbackMessage) ChatIDInt64() (int64, bool) {
	v, ok := ParseInt64(m.ChatID)
	return v, ok
}

// UserIDInt64 解析发起人 id。取不到时返回 ok=false。
func (m CallbackMessage) UserIDInt64() (int64, bool) {
	v, ok := ParseInt64(m.From.UserID)
	return v, ok
}

// IsEnterChat 报告这是不是「用户进入会话」事件。
func (m CallbackMessage) IsEnterChat() bool {
	return m.EventType == "enter_chat"
}

// ParseInt64 解析一个十进制 id。
func ParseInt64(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	var v int64
	neg := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if i == 0 && (c == '-' || c == '+') {
			neg = c == '-'
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int64(c-'0')
	}
	if neg {
		v = -v
	}
	return v, true
}

// ---- 回包 ----

// EncryptedResponse 是被动回包。
//
// ⚠️ 字段拼写是 msgsignature（小写 s），与**入参**的 msg_signature 不同 ——
// 官方文档两处拼法不一致，写成同一个会被企微静默丢弃。
type EncryptedResponse struct {
	Encrypt      string `json:"encrypt"`
	MsgSignature string `json:"msgsignature"`
	Timestamp    string `json:"timestamp"`
	Nonce        string `json:"nonce"`
}

// MarkdownText 是给用户的纯文本回复。
//
// 企微智能机器人的被动回复**没有普通 text 类型**：text 只用于进入会话的欢迎语，
// 给消息回纯文本必须走 stream（带一个 id）或 markdown。
// 这里统一发 markdown，bot 助手没有富文本排版需求，流式的分片刷新反而更麻烦。
type MarkdownText struct {
	MsgType  string           `json:"msgtype"`
	Markdown *MarkdownContent `json:"markdown"`
}

// MarkdownContent 是 markdown 载荷。
type MarkdownContent struct {
	Content string `json:"content"`
}

// StreamReply 是流式回复。
type StreamReply struct {
	MsgType string     `json:"msgtype"`
	Stream  StreamBody `json:"stream"`
}

// StreamBody 是流式内容。
type StreamBody struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Finish  bool   `json:"finish"`
}

// newMarkdownReply 建一条 markdown 回包。
func newMarkdownReply(content string) MarkdownText {
	return MarkdownText{MsgType: "markdown", Markdown: &MarkdownContent{Content: content}}
}

// encodeJSON 序列化回包。
func encodeJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
