package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"litepan/internal/discover/discovery"
	"litepan/internal/domain"
	"litepan/internal/offlinedownload"
)

// ---------------------------------------------------------------------------
// 资源转存 / 离线执行（补齐「搬了一半」的第 5 项：
// 之前只有 /resources/search + /copy-link，用户看到资源卡片却无法一键转存）。
// 转存走 tgto123 反代通道（Tgto123TransferResource：解锁 + 转存一体）；
// 离线（magnet/ed2k）走本机已接的离线下载引擎（internal/offlinedownload）。
// ---------------------------------------------------------------------------

// resourceTransferRequest 转存请求体
type resourceTransferRequest struct {
	Source    string `json:"source"`
	Provider  string `json:"provider"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	LinkType  string `json:"link_type"`
	ShareURL  string `json:"share_url"`
	AccountID int64  `json:"account_id"`
}

// transferMediaResource 一键转存资源到目标网盘
// POST /api/admin/discovery/resources/transfer
func (h *Handler) transferMediaResource(w http.ResponseWriter, r *http.Request) {
	var req resourceTransferRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "参数错误"))
		return
	}
	provider := discovery.Tgto123ResourceProviderKey(req.Provider)
	if provider == "" {
		provider = "123"
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "缺少资源 slug，无法转存"))
		return
	}
	// 离线种子（magnet/ed2k）没有可转存的网盘分享，必须走离线通道
	if lt := strings.ToLower(strings.TrimSpace(req.LinkType)); lt == "magnet" || lt == "ed2k" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "磁力/eD2k 资源请使用离线下载通道"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	message, err := discovery.Tgto123TransferResource(ctx, slug, provider)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"success":     true,
		"provider":    provider,
		"message":     message,
		"title":       req.Title,
		"finished_at": time.Now().Format(time.RFC3339),
	})
}

// offlineMediaResource 离线下载（magnet / ed2k）
// POST /api/admin/discovery/resources/offline
func (h *Handler) offlineMediaResource(w http.ResponseWriter, r *http.Request) {
	var req resourceTransferRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "参数错误"))
		return
	}
	link := strings.TrimSpace(req.ShareURL)
	if link == "" {
		link = strings.TrimSpace(req.Slug)
	}
	linkType := strings.ToLower(strings.TrimSpace(req.LinkType))
	if linkType == "" {
		linkType = detectOfflineLinkType(link)
	}
	if linkType != "magnet" && linkType != "ed2k" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "仅支持 magnet / ed2k 离线链接"))
		return
	}
	if link == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "缺少离线链接"))
		return
	}
	target := discovery.Tgto123ResourceProviderKey(req.Provider)
	accountID, err := h.resolveOfflineAccount(r.Context(), req.AccountID, target)
	if err != nil {
		writeErr(w, err)
		return
	}
	if h.offlineDownloads == nil {
		writeErr(w, domain.Errf(domain.CodeNotImplement))
		return
	}
	tasks, err := h.offlineDownloads.AddURLs(r.Context(), offlinedownload.AddURLParams{
		AccountID: accountID,
		URLs:      []string{link},
		FileName:  strings.TrimSpace(req.Title),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"success":    true,
		"provider":   target,
		"link_type":  linkType,
		"account_id": accountID,
		"message":    "离线下载任务已提交",
		"title":      req.Title,
		"tasks":      tasks,
		"created_at": time.Now().Format(time.RFC3339),
	})
}

// resolveOfflineAccount 选定离线下载账号：显式 account_id 优先，否则按目标网盘类型
// 匹配第一个可用账号（离线下载走原生驱动；无匹配时报错提示配置）。
func (h *Handler) resolveOfflineAccount(ctx context.Context, explicit int64, provider string) (int64, error) {
	if explicit > 0 {
		if err := h.requireOfflineAccount(ctx, explicit); err != nil {
			return 0, err
		}
		return explicit, nil
	}
	if h.accountSvc == nil {
		return 0, domain.Errorf(domain.CodeValidation, "请指定离线下载账号（account_id）")
	}
	list, err := h.accountSvc.List(ctx)
	if err != nil {
		return 0, err
	}
	want := normalizeOfflineDriveType(provider)
	for _, view := range list {
		if view.Account == nil || !view.Account.IsActive {
			continue
		}
		if want == "" || normalizeOfflineDriveType(view.Account.DriverType) == want {
			return view.Account.ID, nil
		}
	}
	if want == "" {
		return 0, domain.Errorf(domain.CodeValidation, "没有可用的离线下载账号，请先添加网盘账号")
	}
	return 0, domain.Errorf(domain.CodeValidation, "没有可用的%s账号，请先添加对应网盘账号", provider)
}

// requireOfflineAccount 校验显式指定的账号存在且可用
func (h *Handler) requireOfflineAccount(ctx context.Context, id int64) error {
	if h.accountSvc == nil {
		return nil
	}
	view, err := h.accountSvc.Get(ctx, id)
	if err != nil || view.Account == nil {
		return domain.Errorf(domain.CodeValidation, "账号不存在：%d", id)
	}
	return nil
}

// normalizeOfflineDriveType 离线转存目标键与驱动名归一（123/115/guangya/pan139…）
func normalizeOfflineDriveType(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "123", "123pan", "123_open", "123open":
		return "123"
	case "115", "115_open", "115open":
		return "115"
	case "guangya", "guangyapan", "gy":
		return "guangya"
	case "139", "pan139", "139_cloud", "cloud139", "mobile":
		return "pan139"
	case "189", "cloud189", "189_cloud":
		return "189"
	case "quark":
		return "quark"
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// detectOfflineLinkType 从链接推断离线类型
func detectOfflineLinkType(link string) string {
	lower := strings.ToLower(link)
	switch {
	case strings.HasPrefix(lower, "magnet:"):
		return "magnet"
	case strings.HasPrefix(lower, "ed2k://"):
		return "ed2k"
	}
	return ""
}
