package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/medialibshare"
	"litepan/internal/rbac"
)

// ---------------------------------------------------------------- 常量

const (
	// ShareTokenHeader 访客令牌请求头名（验收③）。
	//
	// 令牌只走这一个头，不接受 query 参数、不落 cookie —— 理由写在
	// handoff 的「为什么令牌走 header 不走 query」一节，简要说：
	// query 会进浏览器历史、进 Referer、进服务器访问日志、进 Referer 泄露链，
	// 而头只在请求本身里。
	ShareTokenHeader = "X-Share-Token"

	// shareRecentLimit 统计里返回的最近访客/流水条数上限。
	shareRecentLimit = 20

	// shareMaxTitleLen / shareMaxCommentLen 入库前的长度上限。
	shareMaxTitleLen   = 200
	shareMaxCommentLen = 500
)

// ---------------------------------------------------------------- 管理端

// RegisterLibraryShareRoutes 注册免登录分享页的**管理端**路由。
//
// 挂在 requireAdmin 组内（见 router.go 的 NewRouter），所以本函数只管权限、
// 不重复管会话 —— 与 RegisterRBACRoutes / RegisterRequestCenterRoutes 同一约定。
//
// 服务为 nil 时一个端点都不注册 ⇒ 全部 404。这是刻意的：
// 「功能没装」在路由表里应该表现为没有这条路径，而不是有一条专门回 403 的路径
// （后者会让「这个部署到底有没有分享功能」变成一个需要翻源码才能回答的问题）。
func (h *Handler) RegisterLibraryShareRoutes(r chi.Router) {
	if h.libraryShare == nil {
		return
	}
	r.Group(func(r chi.Router) {
		r.Use(h.requirePermission(rbac.PermShareManage))
		r.Route("/library-shares", func(r chi.Router) {
			r.Get("/", h.libraryShareList)
			r.Post("/", h.libraryShareCreate)
			r.Patch("/{id}", h.libraryShareSetExpiry)
			r.Delete("/{id}", h.libraryShareDelete)
			r.Get("/{id}/stats", h.libraryShareStats)
		})
	})
}

// libraryShareReady 是所有管理端 handler 的第一道判断。
//
// 与求片中心的 requestCenterReady 同义：开关关闭 ⇒ 明确回「未启用」，
// 而不是把「创建成功」之类的假象发给用户。
func (h *Handler) libraryShareReady(w http.ResponseWriter) bool {
	if h.libraryShare == nil || !h.libraryShare.Enabled() {
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "免登录分享未启用"))
		return false
	}
	return true
}

// libraryShareID 解析路径里的分享 ID。
func libraryShareID(r *http.Request) (string, error) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		return "", domain.Errorf(domain.CodeValidation, "缺少分享 ID")
	}
	return id, nil
}

// libraryShareCreateReq 管理端创建入参。
//
// ⚠️ 字段名必须与 web/src/api/libraryShare.ts 发的 snake_case 逐字一致：
// decodeJSON 开了 DisallowUnknownFields，差一个拼写就是 400，
// 而这个 400 只在真机上点「创建」时才看得见（见 request_dto.go 的同款备注）。
type libraryShareCreateReq struct {
	AccountID  int64  `json:"account_id"`
	FileID     string `json:"file_id"`
	Title      string `json:"title"`
	Season     int    `json:"season"`
	Comment    string `json:"comment"`
	Password   string `json:"password"`
	ExpireDays int    `json:"expire_days"`
	MaxDevices int    `json:"max_devices"`
}

// libraryShareStatsResp 管理端统计出参。
type libraryShareStatsResp struct {
	ShareID       string                 `json:"share_id"`
	Title         string                 `json:"title"`
	ViewCount     int                    `json:"view_count"`
	PlayCount     int                    `json:"play_count"`
	VisitorCount  int                    `json:"visitor_count"`
	MaxDevices    int                    `json:"max_devices"`
	ActiveDevices int                    `json:"active_devices"`
	ExpiresAt     string                 `json:"expires_at"`
	CreatedAt     string                 `json:"created_at"`
	RecentVisits  []libraryShareVisitDTO `json:"recent_visits"`
	RecentPlays   []librarySharePlayDTO  `json:"recent_plays"`
}

// libraryShareVisitDTO 一条访客会话的出参。
//
// ⚠️ 刻意**不带** token_hash：这一列是令牌的落库形态，
// 任何一处把它读出来往响应里塞，就多一条「数据库泄露即全员令牌泄露」的路径。
type libraryShareVisitDTO struct {
	ID         int64  `json:"id"`
	VisitorID  string `json:"visitor_id"`
	IPMasked   string `json:"ip_masked"`
	UserAgent  string `json:"user_agent"`
	Counted    bool   `json:"counted"`
	LastSeenAt string `json:"last_seen_at"`
}

// librarySharePlayDTO 一条取流流水的出参。
type librarySharePlayDTO struct {
	ID        string `json:"id"`
	VisitorID string `json:"visitor_id"`
	FileID    string `json:"file_id"`
	ItemLabel string `json:"item_label"`
	Method    string `json:"method"`
	IPMasked  string `json:"ip_masked"`
	StartedAt string `json:"started_at"`
}

// libraryShareList GET /admin/library-shares
func (h *Handler) libraryShareList(w http.ResponseWriter, r *http.Request) {
	if !h.libraryShareReady(w) {
		return
	}
	items, active, err := h.libraryShare.List(r.Context(), 200)
	if err != nil {
		writeErr(w, wrapLibraryShareErr(err))
		return
	}
	out := make([]libraryShareDTO, 0, len(items))
	for _, sh := range items {
		dto := libraryShareDTOOf(sh, r)
		dto.Active = active[sh.ID]
		out = append(out, dto)
	}
	writeOK(w, map[string]any{"items": out})
}

// libraryShareCreate POST /admin/library-shares
func (h *Handler) libraryShareCreate(w http.ResponseWriter, r *http.Request) {
	if !h.libraryShareReady(w) {
		return
	}
	var in libraryShareCreateReq
	if !decodeJSONBody(w, r, &in) {
		return
	}
	created, err := h.libraryShare.Create(r.Context(), medialibshare.CreateInput{
		AccountID:    in.AccountID,
		FileID:       in.FileID,
		Title:        in.Title,
		Season:       in.Season,
		Comment:      in.Comment,
		Password:     in.Password,
		ExpireDays:   in.ExpireDays,
		MaxDevices:   in.MaxDevices,
		CreatedBy:    libraryShareActorID(r),
		MaxTitleLen:  shareMaxTitleLen,
		MaxCommentLn: shareMaxCommentLen,
	})
	if err != nil {
		writeErr(w, wrapLibraryShareErr(err))
		return
	}
	// code 是明文短码，**只在这一次的响应里出现**：
	// 生成链接需要它，但库里只留哈希（medialibshare.Store.Insert 存 HashToken(code)）。
	// 日志里也不会出现它（见 Service.Create 的注释）。
	writeOK(w, map[string]any{
		"item":         libraryShareDTOOf(created.Share, r),
		"code":         created.Code,
		"has_password": created.HasPassword,
		"url":          libraryShareURL(r, created.Code),
	})
}

// libraryShareSetExpiry PATCH /admin/library-shares/{id}
func (h *Handler) libraryShareSetExpiry(w http.ResponseWriter, r *http.Request) {
	if !h.libraryShareReady(w) {
		return
	}
	id, err := libraryShareID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		ExpireDays int `json:"expire_days"`
	}
	if !decodeJSONBody(w, r, &in) {
		return
	}
	// days<0 = 永久。与 Create 的三态约定一致：
	// 用 0 同时表示「永久」和「没填」，两者会互相覆盖。
	if err := h.libraryShare.SetExpiry(r.Context(), id, in.ExpireDays); err != nil {
		writeErr(w, wrapLibraryShareErr(err))
		return
	}
	sh, err := h.libraryShare.Get(r.Context(), id)
	if err != nil {
		writeErr(w, wrapLibraryShareErr(err))
		return
	}
	writeOK(w, map[string]any{"item": libraryShareDTOOf(sh, r)})
}

// libraryShareDelete DELETE /admin/library-shares/{id}
func (h *Handler) libraryShareDelete(w http.ResponseWriter, r *http.Request) {
	if !h.libraryShareReady(w) {
		return
	}
	id, err := libraryShareID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.libraryShare.Delete(r.Context(), id); err != nil {
		writeErr(w, wrapLibraryShareErr(err))
		return
	}
	writeOK(w, map[string]any{"id": id})
}

// libraryShareStats GET /admin/library-shares/{id}/stats
func (h *Handler) libraryShareStats(w http.ResponseWriter, r *http.Request) {
	if !h.libraryShareReady(w) {
		return
	}
	id, err := libraryShareID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := h.libraryShare.Stats(r.Context(), id, shareRecentLimit)
	if err != nil {
		writeErr(w, wrapLibraryShareErr(err))
		return
	}
	visits := make([]libraryShareVisitDTO, 0, len(st.RecentVisits))
	for _, v := range st.RecentVisits {
		visits = append(visits, libraryShareVisitDTO{
			ID:         v.ID,
			VisitorID:  v.VisitorID,
			IPMasked:   v.IPMasked,
			UserAgent:  v.UserAgent,
			Counted:    v.Counted,
			LastSeenAt: FormatAPITime(v.LastSeenAt),
		})
	}
	plays := make([]librarySharePlayDTO, 0, len(st.RecentPlays))
	for _, p := range st.RecentPlays {
		plays = append(plays, librarySharePlayDTO{
			ID:        p.ID,
			VisitorID: p.VisitorID,
			FileID:    p.FileID,
			ItemLabel: p.ItemLabel,
			Method:    p.Method,
			IPMasked:  p.IPMasked,
			StartedAt: FormatAPITime(p.StartedAt),
		})
	}
	writeOK(w, libraryShareStatsResp{
		ShareID:       st.ShareID,
		Title:         st.Title,
		ViewCount:     st.ViewCount,
		PlayCount:     st.PlayCount,
		VisitorCount:  st.VisitorCount,
		MaxDevices:    st.MaxDevices,
		ActiveDevices: st.ActiveDevices,
		ExpiresAt:     tsPtrOrEmpty(st.ExpiresAt),
		CreatedAt:     FormatAPITime(st.CreatedAt),
		RecentVisits:  visits,
		RecentPlays:   plays,
	})
}

// libraryShareActorID 取创建者 ID（超管为 0）。
func libraryShareActorID(r *http.Request) int64 {
	p := principalFromContext(r.Context())
	if p.UserID == 0 {
		return 0
	}
	return p.UserID
}

// libraryShareURL 拼出分享链接。
//
// 用请求自身的 host 而不是配置里的地址：litepan 常见部署是内网 IP 或域名，
// 拼一个写死的地址会在换域名/换端口后静默给出打不开的链接。
func libraryShareURL(r *http.Request, code string) string {
	if code == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		return ""
	}
	return scheme + "://" + host + "/share/" + code
}

// wrapLibraryShareErr 把本包错误归一为 API 错误。
//
// 两种归一方式，理由与 wrapMediaRequestErr 相同：
//   - 用户能改正的（口令错、设备满、参数不合法）用 Errorf 带原文；
//   - 其余按内部错误处理，**不外泄底层文本** —— 库里真出问题时，
//     不该在访客手机上冒出一段 SQL。
func wrapLibraryShareErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, medialibshare.ErrDeviceLimit):
		return domain.Errorf(domain.CodeRateLimited, "%s", err.Error())
	case errors.Is(err, medialibshare.ErrPasswordRequired),
		errors.Is(err, medialibshare.ErrBadPassword),
		errors.Is(err, medialibshare.ErrInvalid):
		return domain.Errorf(domain.CodeValidation, "%s", err.Error())
	case errors.Is(err, medialibshare.ErrBadToken):
		return domain.Errorf(domain.CodeAuthExpired, "%s", err.Error())
	case errors.Is(err, medialibshare.ErrNotFound),
		errors.Is(err, medialibshare.ErrDisabled):
		return domain.Errorf(domain.CodeNotFound, "%s", err.Error())
	}
	return domain.Wrap(domain.CodeInternal, err)
}
