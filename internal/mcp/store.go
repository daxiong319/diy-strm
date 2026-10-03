package mcp

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 对话历史的容量上限。
const (
	// DefaultHistoryLimit 是送入 LLM 的历史消息条数上限（过多会挤占上下文）。
	DefaultHistoryLimit = 20
	// DefaultSessionMessagesLimit 是单个会话回显的消息条数上限。
	DefaultSessionMessagesLimit = 200
	// DefaultSessionListLimit 是会话列表返回的条数上限。
	DefaultSessionListLimit = 50
	// sessionTitleRunes 是会话标题截断长度（按字符，不是字节）。
	sessionTitleRunes = 24
)

// ChatMessage 是一条持久化的对话消息。
//
// 只持久化 user / assistant 两种角色：工具调用的中间过程作为 JSON
// 挂在触发它的那条 assistant 消息上，因为重放上下文只需要文本。
type ChatMessage struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	ToolCalls string `json:"tool_calls"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ChatSession 是会话列表项，由消息表聚合派生。
//
// 不单独建会话表：否则会出现「删了消息却忘了删会话行」的不一致状态。
type ChatSession struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
	CreatedAt string `json:"created_at"`
	Count     int64  `json:"count"`
}

// ChatStore 读写对话历史。
//
// 用 *sql.DB 而不是 GORM：本项目主库一律走手写 SQL（见 internal/store 各仓储），
// 只有 discover 子系统用 GORM AutoMigrate。MCP 属于主库范畴，故沿用前者。
type ChatStore struct {
	DB *sql.DB
}

// NewChatStore 构造历史仓储。db 为 nil 时所有写操作静默跳过、读操作返回空，
// 这样在极端情况下（例如迁移失败）对话功能不会整个不可用。
func NewChatStore(db *sql.DB) *ChatStore {
	return &ChatStore{DB: db}
}

// NewSessionID 生成会话 ID：时间戳前缀便于人眼排序，随机后缀保证唯一。
func NewSessionID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// 随机源不可用时退回时间派生的十六进制，保证 ID 仍然可用。
		return time.Now().Format("20060102T150405") + "-" + fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return time.Now().Format("20060102T150405") + "-" + hex.EncodeToString(buf[:])
}

// AppendMessage 追加一条消息。sessionID 为空或内容为空时不做任何事。
func (s *ChatStore) AppendMessage(ctx context.Context, msg *ChatMessage) error {
	if s == nil || s.DB == nil || msg == nil {
		return nil
	}
	if strings.TrimSpace(msg.SessionID) == "" || strings.TrimSpace(msg.Content) == "" {
		return nil
	}
	if strings.TrimSpace(msg.ToolCalls) == "" {
		msg.ToolCalls = "[]"
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO mcp_chat_messages(session_id, role, content, tool_calls)
		 VALUES(?,?,?,?)`,
		msg.SessionID, msg.Role, msg.Content, msg.ToolCalls)
	return err
}

// ListMessages 返回指定会话的消息，按时间正序。
//
// 先按 id 倒序取最近 limit 条，再在内存里反转：
// 这样长会话拿到的是「最新的上下文」，而不是最早的开场白。
func (s *ChatStore) ListMessages(ctx context.Context, sessionID string, limit int) ([]ChatMessage, error) {
	if s == nil || s.DB == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = DefaultSessionMessagesLimit
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, session_id, role, content, tool_calls, created_at, updated_at
		 FROM mcp_chat_messages WHERE session_id = ? ORDER BY id DESC LIMIT ?`,
		sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ChatMessage
	for rows.Next() {
		var msg ChatMessage
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content,
			&msg.ToolCalls, &msg.CreatedAt, &msg.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 反转成正序（最新一条在最后），LLM 才能按时间顺序读上下文。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ListSessions 返回会话列表，按最近更新时间倒序。
func (s *ChatStore) ListSessions(ctx context.Context, limit int) ([]ChatSession, error) {
	if s == nil || s.DB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = DefaultSessionListLimit
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT session_id,
		        MAX(updated_at) AS updated_at,
		        MIN(created_at) AS created_at,
		        COUNT(*)        AS count
		 FROM mcp_chat_messages
		 GROUP BY session_id
		 ORDER BY updated_at DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []ChatSession
	for rows.Next() {
		var item ChatSession
		if err := rows.Scan(&item.SessionID, &item.UpdatedAt, &item.CreatedAt, &item.Count); err != nil {
			return nil, err
		}
		sessions = append(sessions, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range sessions {
		title, err := s.firstUserTitle(ctx, sessions[i].SessionID)
		if err != nil {
			// 标题取不到不该让整个列表失败，退回占位标题。
			packageLog().Warn("读取 MCP 会话标题失败", "session", sessions[i].SessionID, "error", err)
			title = "新对话"
		}
		sessions[i].Title = title
	}
	return sessions, nil
}

// firstUserTitle 取会话里第一条用户消息，截断成标题。
func (s *ChatStore) firstUserTitle(ctx context.Context, sessionID string) (string, error) {
	var content string
	err := s.DB.QueryRowContext(ctx,
		`SELECT content FROM mcp_chat_messages
		 WHERE session_id = ? AND role = 'user' ORDER BY id ASC LIMIT 1`,
		sessionID).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "新对话", nil
	}
	if err != nil {
		return "", err
	}
	return SessionTitle(content), nil
}

// DeleteSession 删除整个会话，返回删除条数。
func (s *ChatStore) DeleteSession(ctx context.Context, sessionID string) (int64, error) {
	if s == nil || s.DB == nil || strings.TrimSpace(sessionID) == "" {
		return 0, nil
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM mcp_chat_messages WHERE session_id = ?`, sessionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SessionTitle 从首条消息派生会话标题：换行折成空格，超长按字符截断。
//
// 按 rune 而不是 byte 截断：中文标题按字节切会切出乱码。
func SessionTitle(content string) string {
	text := strings.Join(strings.Fields(strings.ReplaceAll(content, "\n", " ")), " ")
	if text == "" {
		return "新对话"
	}
	runes := []rune(text)
	if len(runes) <= sessionTitleRunes {
		return text
	}
	return string(runes[:sessionTitleRunes]) + "…"
}
