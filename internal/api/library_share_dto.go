package api

import (
	"net/http"
	"time"

	"litepan/internal/medialibshare"
)

// 分享相关 DTO。
//
// 为什么要单独一层（与 request_dto.go 同一理由）：直接从 medialibshare.Share
// 出包会把 Go 字段名原样暴露成 PascalCase 的公共契约，
// 而前端已经按 snake_case 写了 —— 两边一改名就全线报错。
// 存储字段与对外字段隔开，改存储时不必改前端。

// libraryShareDTO 一条分享的管理端出参。
//
// ⚠️ 刻意**不带** PasswordHash，也**不带** Code：
// 前者任何时候都不该离开数据库，后者只应该在「刚创建」那一次响应里出现
// （见 libraryShareCreate）。
type libraryShareDTO struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Title       string `json:"title"`
	AccountID   int64  `json:"account_id"`
	FileID      string `json:"file_id"`
	Season      int    `json:"season"`
	Comment     string `json:"comment"`
	HasPassword bool   `json:"has_password"`
	// ExpiresAt 为空串表示永久。不用 null 是因为前端模板里
	// 直接 `share.expires_at || "永久"` 比判 null 更不容易写错。
	ExpiresAt    string `json:"expires_at"`
	ExpireDays   int    `json:"expire_days"`
	MaxDevices   int    `json:"max_devices"`
	ViewCount    int    `json:"view_count"`
	PlayCount    int    `json:"play_count"`
	VisitorCount int    `json:"visitor_count"`
	// Active 当前有效设备数（24 小时内有动作的会话），与 MaxDevices 对比着看。
	Active    int    `json:"active"`
	Revoked   bool   `json:"revoked"`
	Expired   bool   `json:"expired"`
	CreatedAt string `json:"created_at"`
	CreatedBy int64  `json:"created_by"`
	URL       string `json:"url"`
}

func libraryShareDTOOf(sh medialibshare.Share, r *http.Request) libraryShareDTO {
	now := time.Now().UTC()
	return libraryShareDTO{
		ID:           sh.ID,
		Code:         sh.Code,
		Title:        sh.Title,
		AccountID:    sh.AccountID,
		FileID:       sh.FileID,
		Season:       sh.Season,
		Comment:      sh.Comment,
		HasPassword:  sh.HasPassword(),
		ExpiresAt:    tsPtrOrEmpty(sh.ExpiresAt),
		ExpireDays:   medialibshare.DaysUntil(sh.ExpiresAt, now),
		MaxDevices:   sh.MaxDevices,
		ViewCount:    sh.ViewCount,
		PlayCount:    sh.PlayCount,
		VisitorCount: sh.VisitorCount,
		Active:       0,
		Revoked:      sh.Revoked(),
		Expired:      sh.Expired(now),
		CreatedAt:    FormatAPITime(sh.CreatedAt),
		CreatedBy:    sh.CreatedBy,
		URL:          libraryShareURL(r, sh.Code),
	}
}

// tsPtrOrEmpty 把可空时间转成 RFC3339 字符串，空指针转空串。
//
// 不复用 request_dto.go 的 tsOrEmpty：那个收 time.Time 非指针，
// 这里收 *time.Time —— expires_at 允许 NULL（永久），两者的零值语义不同
// （非指针零值是「时间无效」，指针零值是「没有期限」）。
func tsPtrOrEmpty(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------- 访客侧

// libraryShareVisitorDTO 访客看到的分享信息。
//
// 这是**唯一**会发给外人的结构，所以它必须极度保守：
// 不含 account_id / file_id / created_by / 任何计数。
// 外人只需要知道「片名是什么、要不要口令、什么时候过期」。
type libraryShareVisitorDTO struct {
	Title       string `json:"title"`
	Comment     string `json:"comment"`
	Season      int    `json:"season"`
	HasPassword bool   `json:"has_password"`
	ExpiresAt   string `json:"expires_at"`
	// ExpiresInDays 剩余天数，0 或负数表示永久。
	// 前端用它显示「3 天后失效」—— 直接算天数比在前端解析时间戳可靠。
	ExpiresInDays int `json:"expires_in_days"`
}

func libraryShareVisitorDTOOf(sh medialibshare.Share) libraryShareVisitorDTO {
	return libraryShareVisitorDTO{
		Title:         sh.Title,
		Comment:       sh.Comment,
		Season:        sh.Season,
		HasPassword:   sh.HasPassword(),
		ExpiresAt:     tsPtrOrEmpty(sh.ExpiresAt),
		ExpiresInDays: medialibshare.DaysUntil(sh.ExpiresAt, time.Now().UTC()),
	}
}

// libraryShareTokenDTO 签发令牌的响应体。
//
// Token 只在**新会话**时非空（medialibshare.IssueResult 的约定）：
// 复用的会话明文早给过了、库里不存，回空串让前端知道
// 「我 localStorage 里那个还有效，直接用」。
type libraryShareTokenDTO struct {
	Token string `json:"token"`
	// HasToken 告诉前端「带 X-Share-Token 就能播」，无论 Token 是否回传。
	HasToken   bool                   `json:"has_token"`
	IsNewVisit bool                   `json:"is_new_visit"`
	Share      libraryShareVisitorDTO `json:"share"`
}

// libraryShareEventReq 前端上报事件的入参。
type libraryShareEventReq struct {
	Event     string `json:"event"`
	VisitorID string `json:"visitor_id"`
	Label     string `json:"label"`
}
