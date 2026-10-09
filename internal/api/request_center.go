package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/mediarequest"
	"litepan/internal/rbac"
)

// RegisterRequestCenterRoutes 装求片中心的管理台路由。
//
// 全部挂在权限闸后面，分三层：
//   - request.review    审核（通过/驳回）—— 唯一会改变别人请求状态的权限
//   - request.all.view  看所有人的求片单
//   - request.center.view 进入求片中心（看自己的单、看规则、看统计）
//
// 「自己的单」不需要 all.view：request.mine.view 就是干这个的，
// 顺带保证一个只有 mine 权限的普通用户能看到自己的求片状态。
func (h *Handler) RegisterRequestCenterRoutes(r chi.Router) {
	if h.mediaRequest == nil {
		return
	}
	r.Route("/media-request", func(r chi.Router) {
		// 待审队列：只有审核员看得到别人的求片。
		r.Group(func(r chi.Router) {
			r.Use(h.requirePermission(rbac.PermRequestReview))
			r.Get("/pending", h.requestCenterPending)
			r.Get("/requests", h.requestCenterRequests)
			r.Get("/requests/{id}", h.requestCenterDetail)
			r.Post("/requests/{id}/review", h.requestCenterReview)
		})

		// 我的求片（任何有 request.center.view 的人）。
		r.Group(func(r chi.Router) {
			r.Use(h.requirePermission(rbac.PermRequestCenterView))
			r.Get("/mine", h.requestCenterMine)
			r.Get("/stats", h.requestCenterStats)
			r.Get("/rules", h.requestCenterRules)
			r.Put("/rules", h.requestCenterSaveRules)
			r.Post("/reconcile", h.requestCenterReconcile)
		})
	})
}

// requestCenterPending GET /admin/media-request/pending。
func (h *Handler) requestCenterPending(w http.ResponseWriter, r *http.Request) {
	if !h.requestCenterReady(w, r) {
		return
	}
	list, err := h.mediaRequest.ListPending(r.Context(), requestCenterLimit(r))
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, map[string]any{"items": requestReviewDTOList(list)})
}

// requestCenterRequests GET /admin/media-request/requests?status=approved。
func (h *Handler) requestCenterRequests(w http.ResponseWriter, r *http.Request) {
	if !h.requestCenterReady(w, r) {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	list, err := h.mediaRequest.ListByStatus(r.Context(), status, requestCenterLimit(r))
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, map[string]any{"items": requestReviewDTOList(list)})
}

// requestCenterDetail GET /admin/media-request/requests/{id}。
func (h *Handler) requestCenterDetail(w http.ResponseWriter, r *http.Request) {
	if !h.requestCenterReady(w, r) {
		return
	}
	id, ok := requestCenterID(w, r)
	if !ok {
		return
	}
	req, err := h.mediaRequest.Get(r.Context(), id)
	if err != nil {
		writeErr(w, wrapMediaRequestErr(err))
		return
	}
	warning, _ := h.mediaRequest.ReviewWarning(r.Context(), id)
	writeOK(w, map[string]any{"item": requestReviewDTOOf(*req), "warning": warning})
}

// requestCenterReview POST /admin/media-request/requests/{id}/review。
type requestCenterReviewBody struct {
	Approve bool   `json:"approve"`
	Reason  string `json:"reason"`
	Note    string `json:"note"`
}

func (h *Handler) requestCenterReview(w http.ResponseWriter, r *http.Request) {
	if !h.requestCenterReady(w, r) {
		return
	}
	id, ok := requestCenterID(w, r)
	if !ok {
		return
	}
	var body requestCenterReviewBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !body.Approve && strings.TrimSpace(body.Reason) == "" {
		// 驳回必须给理由。
		//
		// 不是流程洁癖：驳回不写理由的话，家人只会看到「被拒了」，
		// 既不知道是片名写错了还是没人有权限，也不知道能不能换个写法再求一次。
		// 理由是唯一能让他行动的信息。
		writeErr(w, domain.Errorf(domain.CodeValidation, "驳回时请填写原因，方便对方调整"))
		return
	}
	p := principalFromContext(r.Context())
	res, err := h.mediaRequest.Review(r.Context(), mediarequest.Actor{
		UserID: p.UserID, Username: p.Username, IsSuper: p.IsSuper,
	}, id, body.Approve, body.Reason, body.Note)
	if err != nil {
		writeErr(w, wrapMediaRequestErr(err))
		return
	}
	writeOK(w, map[string]any{"item": requestReviewDTOOf(*res.Request), "warning": res.Warning})
}

// requestCenterMine GET /admin/media-request/mine。
func (h *Handler) requestCenterMine(w http.ResponseWriter, r *http.Request) {
	if !h.requestCenterReady(w, r) {
		return
	}
	p := principalFromContext(r.Context())
	list, err := h.mediaRequest.ListMine(r.Context(), p.UserID, requestCenterLimit(r))
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, map[string]any{"items": requestDTOList(list)})
}

// requestCenterStats GET /admin/media-request/stats。
func (h *Handler) requestCenterStats(w http.ResponseWriter, r *http.Request) {
	if !h.requestCenterReady(w, r) {
		return
	}
	rows, err := h.mediaRequest.Stats(r.Context(), 30)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	out := make([]requestStatsDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, requestStatsDTOOf(row))
	}
	writeOK(w, map[string]any{"items": out})
}

// requestCenterRules GET /admin/media-request/rules。
func (h *Handler) requestCenterRules(w http.ResponseWriter, r *http.Request) {
	if !h.requestCenterReady(w, r) {
		return
	}
	rules, err := h.mediaRequest.Rules(r.Context())
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, map[string]any{"items": requestRuleDTOList(rules)})
}

// requestCenterSaveRules PUT /admin/media-request/rules。
//
// 整份覆盖（PUT 语义）。规则表本来就小，与其做增删改三套接口，
// 不如一次提交完整列表，服务端 diff —— 这样「先删规则 A、再建规则 B」
// 原子地发生，不会出现中间态把某个用户的求片临时变成要审核的。
func (h *Handler) requestCenterSaveRules(w http.ResponseWriter, r *http.Request) {
	if !h.requestCenterReady(w, r) {
		return
	}
	var body struct {
		Items []requestRuleInput `json:"items"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	items := make([]mediarequest.Rule, 0, len(body.Items))
	for _, in := range body.Items {
		items = append(items, in.toRule())
	}
	saved, err := h.mediaRequest.SaveRules(r.Context(), items)
	if err != nil {
		writeErr(w, wrapMediaRequestErr(err))
		return
	}
	writeOK(w, map[string]any{"items": saved})
}

// requestCenterReconcile POST /admin/media-request/reconcile。
//
// 手动触发对账。平时由后台 ticker 每 15 分钟跑一次，
// 这个接口是给「我刚改了保存目录，想现在就补建订阅」用的。
func (h *Handler) requestCenterReconcile(w http.ResponseWriter, r *http.Request) {
	if !h.requestCenterReady(w, r) {
		return
	}
	n, err := h.mediaRequest.Reconcile(r.Context())
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	writeOK(w, map[string]any{"handled": n})
}

// requestCenterReady 求片中心是否可用。
func (h *Handler) requestCenterReady(w http.ResponseWriter, r *http.Request) bool {
	if h.mediaRequest != nil && h.mediaRequest.Enabled() {
		return true
	}
	writeErr(w, domain.Errorf(domain.CodeNotImplement, "求片中心未启用"))
	return false
}

// requestCenterID 解析路径里的求片单 ID。
func requestCenterID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "id")
	var id int64
	if _, err := fmt.Sscanf(raw, "%d", &id); err != nil || id <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "求片单 ID 不对"))
		return 0, false
	}
	return id, true
}

// requestCenterLimit 解析列表条数上限。
func requestCenterLimit(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 200
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 {
		return 200
	}
	if n > 1000 {
		return 1000
	}
	return n
}
