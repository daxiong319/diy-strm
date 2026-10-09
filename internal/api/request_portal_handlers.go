package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"litepan/internal/discover/discovery"
	"litepan/internal/domain"
	"litepan/internal/mediarequest"
	"litepan/internal/rbac"
)

// ---------------------------------------------------------------- 请求/响应 DTO

// portalLoginRequest 登录请求。
//
// 字段名与管理台登录保持一致（username/password/remember），
// 少一层「这边叫什么那边叫什么」的转换，排查登录问题时能少想一步。
type portalLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

// portalMeDTO 当前身份。
type portalMeDTO struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name,omitempty"`
	IsSuper     bool   `json:"is_super"`
	// Permissions 有效权限项；前端据此决定要不要显示「我的求片」入口。
	Permissions []string `json:"permissions"`
	// DailyLimit 今天还能求几部；-1 表示不限。
	DailyLimit int `json:"daily_limit"`
	// UsedToday 今天已提交几部。
	UsedToday int `json:"used_today"`
	// RequireReview 这台机器上提交后是否需要审核。
	RequireReview bool `json:"require_review"`
	// TagMaxPerUser 每人标签上限；0 表示不限。
	TagMaxPerUser int `json:"tag_max_per_user"`
	// TagMaxLength 单个标签字数上限；0 表示不限。
	TagMaxLength int `json:"tag_max_length"`
}

// portalLogin POST /api/login。
//
// 走 adminauth.Authenticate（不是 Login）：那是一条**不落 cookie**的凭据校验路径，
// 校验逻辑（超管 / 委托用户 / 密码策略）与管理台逐字同一份，
// 求片站自己签 media_request_session。
//
// 也就是说：**求片站和管理台共用同一套账号密码**。
// 这是刻意的 —— 让家人再记一套账号是求片中心被弃用的头号原因；
// 靠 RBAC 区分「谁能求片、谁能管后台」本来就是 T08 设计好的分工。
func (h *Handler) portalLogin(w http.ResponseWriter, r *http.Request) {
	var req portalLoginRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if h.adminAuth == nil || h.requestSigner == nil {
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "登录服务未就绪"))
		return
	}
	sess, res, err := h.adminAuth.Authenticate(r.Context(), r, strings.TrimSpace(req.Username), req.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := h.loadPortalPrincipal(r.Context(), &mediarequest.SessionIdentity{
		Username: sess.Username, UserID: sess.UserID,
	})
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	// 能登录 ≠ 能求片：至少得有 request.submit 或 request.center.view。
	//
	// 这一条不是为了防攻击（登录的人本来就知道密码），而是为了防误会：
	// 运维同事的账号常常默认没有任何求片权限，让他进来看到「无权限」页面
	// 比直接拒绝登录更让人困惑。
	if h.rbac != nil && h.rbac.Enabled(r.Context()) &&
		!p.Can(rbac.PermRequestSubmit) && !p.Can(rbac.PermRequestCenterView) {
		writeErr(w, domain.Errorf(domain.CodePermissionDenied,
			"这个账号没有求片权限，请让管理员在「用户与权限」里勾选「提交求片」"))
		return
	}
	if err := h.requestSigner.Issue(w, r, mediarequest.SessionIdentity{
		Username: p.Username, UserID: p.UserID, IsSuper: p.IsSuper,
	}); err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	_ = res // LoginResult 目前只用不到，但保留接口一致性便于将来提示 must_change_password
	writeOK(w, h.portalMeDTOOf(r, p))
}

// portalLogout POST /api/logout。
func (h *Handler) portalLogout(w http.ResponseWriter, r *http.Request) {
	if h.requestSigner != nil {
		h.requestSigner.Clear(w, r)
	}
	writeOK(w, map[string]bool{"ok": true})
}

// portalMe GET /api/me。
func (h *Handler) portalMe(w http.ResponseWriter, r *http.Request) {
	writeOK(w, h.portalMeDTOOf(r, portalPrincipalFrom(r.Context())))
}

// portalMeDTOOf 组装身份与限额。
func (h *Handler) portalMeDTOOf(r *http.Request, p Principal) portalMeDTO {
	dto := portalMeDTO{
		Username:    p.Username,
		DisplayName: p.DisplayName,
		IsSuper:     p.IsSuper,
		Permissions: p.EffectivePermissions(),
		DailyLimit:  -1,
	}
	if h.mediaRequest == nil {
		return dto
	}
	actor := principalActor(p)
	dto.RequireReview = h.mediaRequest.RequireReview(actor)
	dto.TagMaxPerUser, dto.TagMaxLength = h.mediaRequest.TagLimits()
	if n, err := h.mediaRequest.UsedToday(r.Context(), actor); err == nil {
		dto.UsedToday = n
		if limit := h.mediaRequest.DailyLimit(); limit > 0 {
			dto.DailyLimit = limit
		}
	}
	return dto
}

// portalSearchRequest 搜索请求。
type portalSearchRequest struct {
	Query string `json:"query"`
	// Page 页码，从 1 起。
	Page int `json:"page"`
}

// portalSearch GET /api/search。
//
// 搜索能力用媒体发现那套（mediarequest.Searcher）。**不做任何二次过滤**：
// 搜索结果就是 TMDB 的公开元数据，返回它不会泄露任何东西；
// 真正的边界在提交那一步（能不能建订阅由权限与上限说了算）。
func (h *Handler) portalSearch(w http.ResponseWriter, r *http.Request) {
	if h.mediaRequest == nil {
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "求片功能未启用"))
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "请输入片名"))
		return
	}
	page := 1
	if v := strings.TrimSpace(r.URL.Query().Get("page")); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &page); err != nil || page < 1 {
			page = 1
		}
	}
	if page > 20 {
		page = 20
	}
	mt := strings.TrimSpace(r.URL.Query().Get("media_type"))
	if mt == "" {
		mt = "tv"
	}
	res, err := h.mediaRequest.Search(r.Context(), q, mt, page)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	// 滤掉没有 TMDB ID 的条目。
	//
	// 搜索是多来源的（douban/bangumi/anilist 也可能命中），而求片必须落到
	// TMDB ID 上 —— Submit 会拒掉 ID 为 0 的请求。与其让人点了之后收到一句
	// 「参数不对」，不如一开始就不给他点。
	items := make([]discovery.Item, 0, len(res.Items))
	for _, it := range res.Items {
		if it.TMDBID > 0 {
			items = append(items, it)
		}
	}
	writeOK(w, discovery.ActorsPage{Items: items, Page: res.Page, TotalPages: res.TotalPages, TotalItems: res.TotalItems})
}

// portalSubmit POST /api/request。
func (h *Handler) portalSubmit(w http.ResponseWriter, r *http.Request) {
	if h.mediaRequest == nil {
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "求片功能未启用"))
		return
	}
	p := portalPrincipalFrom(r.Context())
	var body struct {
		TmdbID    int64    `json:"tmdb_id"`
		Title     string   `json:"title"`
		MediaType string   `json:"media_type"`
		Season    int      `json:"season"`
		Notes     string   `json:"notes"`
		Tags      []string `json:"tags"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	res, err := h.mediaRequest.Submit(r.Context(), mediarequest.SubmitInput{
		Requester: principalActor(p),
		TMDBID:    body.TmdbID,
		Title:     body.Title,
		MediaType: body.MediaType,
		Season:    body.Season,
		Notes:     body.Notes,
		Tags:      body.Tags,
	})
	if err != nil {
		writeErr(w, wrapMediaRequestErr(err))
		return
	}
	writeOK(w, map[string]any{
		"id":           res.Request.ID,
		"status":       string(res.Request.Status),
		"needs_review": res.Request.Status == mediarequest.StatusPending,
		"message":      submitMessage(res),
		"warning":      res.Warning,
	})
}

// submitMessage 给家人看的一句话反馈。
//
// 文案分三种情况，因为这三件事的处理方式完全不同：
//   - 直接生效（免审）→ 告诉他已经建好订阅、会在资源出现时转存
//   - 待审核 → 告诉他要等管理员
//   - 免审但有告警 → **必须说出来**，否则他会以为已经在转存，等不到东西才发现没配保存目录
func submitMessage(res *mediarequest.SubmitResult) string {
	if res.Warning != "" {
		return "已收到，但暂时还转存不了：" + res.Warning
	}
	if res.Request.Status == mediarequest.StatusPending {
		return "已提交，等管理员审核通过后就会自动开始转存。"
	}
	return "已提交，订阅已建好：有资源时会自动转存。"
}

// portalMine GET /api/mine：我提交的求片。
func (h *Handler) portalMine(w http.ResponseWriter, r *http.Request) {
	if h.mediaRequest == nil {
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "求片功能未启用"))
		return
	}
	p := portalPrincipalFrom(r.Context())
	list, err := h.mediaRequest.ListMine(r.Context(), p.UserID, 100)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	// 求片站用「自己的单」DTO：不给审核人、驳回痕迹之外的内部状态。
	writeOK(w, map[string]any{"items": requestDTOList(list)})
}

// portalSaveTagsRequest 保存标签。
type portalSaveTagsRequest struct {
	TmdbID    int64    `json:"tmdb_id"`
	MediaType string   `json:"media_type"`
	Season    int      `json:"season"`
	Tags      []string `json:"tags"`
}

// portalSaveTags PUT /api/tags。
//
// **标签是替换语义，不是追加。** 界面上是「编辑这部作品的标签」，
// 让人能删掉之前打错的标签 —— 只增不减的话，20 个额度很快就再也腾不出来
// （那 20 个额度是给整部剧算的，见 TagsForWork 的注释）。
func (h *Handler) portalSaveTags(w http.ResponseWriter, r *http.Request) {
	if h.mediaRequest == nil {
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "求片功能未启用"))
		return
	}
	p := portalPrincipalFrom(r.Context())
	var body portalSaveTagsRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if err := h.mediaRequest.SaveTags(r.Context(), body.TmdbID, body.MediaType, p.UserID, body.Tags); err != nil {
		writeErr(w, wrapMediaRequestErr(err))
		return
	}
	// 回读而不是直接返回清洗后的入参：
	// 这样客户端拿到的一定是**库里真的存下了**的那份（去重、长度截断都已生效）。
	tags, err := h.mediaRequest.TagsOfUser(r.Context(), body.TmdbID, body.MediaType, p.UserID)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, map[string]any{"tags": tags})
}

// portalStats GET /api/stats：我自己的求片统计。
//
// 只统计**本人**的，家人之间不互相可见统计数字。
func (h *Handler) portalStats(w http.ResponseWriter, r *http.Request) {
	if h.mediaRequest == nil {
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "求片功能未启用"))
		return
	}
	p := portalPrincipalFrom(r.Context())
	rows, err := h.mediaRequest.Stats(r.Context(), 30)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	// 求片站的统计只给本人看。
	//
	// 过滤在服务端而不是前端：家人之间互相能看到对方的求片数量，
	// 会让「谁在偷偷看片子」这种问题变成可能。前端过滤是摆设 ——
	// 数据已经出去了。
	own := make([]requestStatsDTO, 0, len(rows))
	for _, row := range rows {
		if row.RequesterID == p.UserID {
			own = append(own, requestStatsDTOOf(row))
		}
	}
	writeOK(w, map[string]any{"items": own})
}

// wrapMediaRequestErr 把求片服务错误映射成 API 错误码。
//
// 三条纪律：
//
//  1. **上限/校验类错误必须是 400 且带原文**，而不是笼统的 500。
//     「今天已经求过 5 部了」是用户的正常操作反馈，不是系统故障；
//     报成 500 会让前端把它当异常弹窗，而不是显示成一句「今天的名额用完了」。
//  2. **原文要用 domain.Errorf 而不是 domain.Wrap**。Wrap 在 msg 为空时
//     用错误码的默认文案（CodeValidation 就是「参数错误」），底层那句
//     「这部作品已经有一条待审的求片单了（提交人：小明）」会被整段吞掉 ——
//     家人只会看到「参数错误」，完全不知道自己撞了什么。
//  3. 认不出来的一律 CodeInternal，且**不外泄底层文本**：库里真出问题时
//     手机上不该冒出 SQL 原文。
func wrapMediaRequestErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, mediarequest.ErrNotFound), errors.Is(err, mediarequest.ErrNotPending),
		errors.Is(err, mediarequest.ErrNotApproved), errors.Is(err, mediarequest.ErrAlreadyPending),
		errors.Is(err, mediarequest.ErrAlreadyLinked):
		return domain.Errorf(domain.CodeValidation, "%s", err.Error())
	}
	var le *mediarequest.LimitError
	if errors.As(err, &le) {
		code := domain.CodeValidation
		if le.Kind == mediarequest.LimitKindDaily {
			code = domain.CodeRateLimited
		}
		return domain.Errorf(code, "%s", err.Error())
	}
	if mediarequest.IsInvalid(err) {
		return domain.Errorf(domain.CodeValidation, "%s", err.Error())
	}
	return domain.Wrap(domain.CodeInternal, err)
}

// decodeJSONBody 解析请求体，失败时写 400 并返回 false。
//
// 1 MB 上限：求片提交体只有几个短字符串字段（片名/备注/20 个标签），
// 正常请求连 1 KB 都用不到。留 1 MB 是为了容忍「标签里贴了一段长文本」
// 这种边缘用法，同时挡住「一个 body 把内存吃光」。
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := decodeJSON(r, dst); err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "请求参数格式不对：%s", err))
		return false
	}
	return true
}
