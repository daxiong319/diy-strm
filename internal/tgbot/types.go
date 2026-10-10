package tgbot

import "encoding/json"

// Telegram Bot API 的 Update / Message / Chat / User 结构。
//
// 这里只声明本项目真正用到的字段：Telegram 每次推送完整对象，
// 未声明的字段由 encoding/json 忽略，不需要也不应该把协议整个抄一遍。

// Update 是一次入站更新。字段指针用于区分「字段不存在」与「字段为零值」——
// 例如 Message 为 nil 表示这是一条 callback_query 或 poll_answer，不是消息。
type Update struct {
	UpdateID      int64    `json:"update_id"`
	Message       *Message `json:"message,omitempty"`
	ChannelPost   *Message `json:"channel_post,omitempty"`
	EditedMessage *Message `json:"edited_message,omitempty"`
}

// Message 是一条消息。Text 只在纯文本消息里有；Caption 覆盖图片等媒体的说明文字。
type Message struct {
	MessageID      int64    `json:"message_id"`
	Date           int64    `json:"date"`
	Chat           Chat     `json:"chat"`
	From           User     `json:"from"`
	Text           string   `json:"text,omitempty"`
	Caption        string   `json:"caption,omitempty"`
	Entities       []Entity `json:"entities,omitempty"`
	ReplyToMessage *Message `json:"reply_to_message,omitempty"`
}

// Entity 是消息里的富文本标记（命令、链接、@提及等）。
type Entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

// Chat 是会话。私聊 Type=="private"，群/频道是 "group"/"supergroup"/"channel"。
type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

// User 是一个 Telegram 用户。Bot 收到的每条消息都有 From。
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// IsPrivate 报告这个会话是不是私聊。
func (c Chat) IsPrivate() bool { return c.Type == "private" }

// IsGroup 报告这个会话是不是群或超级群。频道不计入：频道里没有「谁能发命令」的概念。
func (c Chat) IsGroup() bool { return c.Type == "group" || c.Type == "supergroup" }

// Display 返回适合写进日志或回复的用户名。
func (u User) Display() string {
	if u.Username != "" {
		return "@" + u.Username
	}
	if u.FirstName != "" {
		return u.FirstName
	}
	return "user " + itoa(u.ID)
}

// body 返回消息里可读的文本：文本消息用 Text，媒体消息用 Caption。
func (m Message) body() string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}

// webhookInfo 是 setWebhook/getWebhookInfo 的返回体。
type webhookInfo struct {
	OK                   bool   `json:"ok"`
	URL                  string `json:"url"`
	HasCustomCertificate bool   `json:"has_custom_certificate"`
	PendingUpdateCount   int    `json:"pending_update_count"`
	LastErrorMessage     string `json:"last_error_message,omitempty"`
}

// apiResponse 是 sendMessage 之类的通用返回体。
type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}
