package api

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/medialibshare"
	"litepan/internal/playback"
)

// EmbeddedWebIndexHTML 读回内嵌前端包的 index.html。
//
// 分享页与管理台共用同一个构建产物（不是像求片站那样另开一次构建），
// 所以这里只需要把根 index.html 取出来原样吐给浏览器，
// 由前端 router 根据 /share/{code} 决定渲染哪个组件。
//
// 每次请求都读一次文件是有意的取舍：内嵌 FS 在进程生命周期里是只读的，
// 读开销是一次 map 查找加一次拷贝；换来的是不需要在 Deps 里多存一份
// 可能与前端构建脱节的 index.html 字节。
func EmbeddedWebIndexHTML() ([]byte, error) {
	return fs.ReadFile(EmbeddedWebFS(), "index.html")
}

// ---------------------------------------------------------------- 设计说明
//
// 访客侧是**免鉴权**路由：没有任何账号、没有 Cookie、没有令牌之外的身份。
// 它刻意不复用管理台的 /api 前缀，也不挂在 requireAdmin 组里，
// 而是自己一棵独立的小树（见 RegisterLibraryShareGuestRoutes）。
//
// 三条硬要求对应三处实现：
//
//	验收① 外部人不登录能直接播放
//	    ⇒ 取流走 /share-play/{code}/stream，头里带 X-Share-Token，
//	      令牌由 POST /share-play/{code}/token 用 code(+口令) 换。
//	验收② 令牌缺失/错误/过期 → 明确拒绝
//	    ⇒ shareStream 先 authorizeShareToken，失败一律 401 + 文案。
//	验收⑦ 访客路由树里一个管理接口都没有
//	    ⇒ 本文件只注册下面这四条路由，没有 r.Mount、没有通配、没有转发到
//	      管理 router 的分支；wiring_reachability_test.go 用 AST 扫描钉住这一点。
//
// ⚠️ 关于第 9 条验收（开关关闭时全部 404）：
// 这里**不用** r.Use(enabledGate) 那种全局中间件。
// chi 的 Use 在任何请求上都执行（包括没匹配上任何路由的），
// 会把「本来该 404 的路径」变成 503 —— 那正是需求要求看到的 404。
// 所以开关判断放在每个 handler 内部（shareEnabled），或者干脆不注册路由。

// RegisterLibraryShareGuestRoutes 注册访客侧的免鉴权路由。
//
// 注册在**管理台那棵 router 的 /api 之外**（router.go 里直接 r.Route("/share-play", ...)
// 以及 GET /share/{code}），所以它们与 requireAdmin 完全无关。
//
// 服务为 nil 时一个都不注册 ⇒ 404（验收⑨的前一半）。
func (h *Handler) RegisterLibraryShareGuestRoutes(r chi.Router) {
	if h.libraryShare == nil {
		return
	}
	r.Route("/share-play", func(r chi.Router) {
		r.Post("/{code}/token", h.shareIssueToken)
		r.Post("/{code}/event", h.shareRecordEvent)
		r.Get("/{code}/stream", h.shareStream)
		r.Head("/{code}/stream", h.shareStream)
	})
}

// shareEnabled 访客侧的总开关闸。
//
// 返回 false 时已经写完响应。对「不存在的短码」与「开关关闭」给的是
// 同一个 404（medialibshare.LookupByCode 把开关关闭也归一成 ErrNotFound）：
// 两者都答 404 是刻意的 —— 答 503 等于告诉扫描器「这个功能存在，
// 关掉之后就没了」，那是一条免费的情报，也让人以为改配置就能打开。
func (h *Handler) shareEnabled(w http.ResponseWriter) bool {
	if h.libraryShare == nil || !h.libraryShare.Enabled() {
		writeGuestShareErr(w, medialibshare.ErrNotFound)
		return false
	}
	return true
}

// shareTokenReq POST /share-play/{code}/token 的入参。
type shareTokenReq struct {
	Password  string `json:"password"`
	VisitorID string `json:"visitor_id"`
}

// shareIssueToken POST /share-play/{code}/token —— 用短码换访客令牌。
//
// 「短码 + 口令」换令牌这个形态是任务书定的：短码在 URL 里（门牌号），
// 口令是人告诉你的（钥匙的一半），两者合起来才签发完整令牌。
// 光有短码就够了的情况（没设口令的分享）是这个设计的常见情形。
func (h *Handler) shareIssueToken(w http.ResponseWriter, r *http.Request) {
	if !h.shareEnabled(w) {
		return
	}
	var in shareTokenReq
	if !decodeJSONBody(w, r, &in) {
		return
	}
	visitorID := strings.TrimSpace(in.VisitorID)
	if visitorID == "" {
		// 随机源不可用时必须失败：这里宁可让访客看一句「请重新打开链接」，
		// 也不能编一个可预测的 visitor_id —— 那会让同浏览器去重彻底失效，
		// 于是谁都能把 24 小时一次的 visitor 计数刷成任意多次。
		id, err := medialibshare.NewVisitorID()
		if err != nil {
			writeGuestShareErr(w, err)
			return
		}
		visitorID = id
	}
	res, err := h.libraryShare.IssueToken(r.Context(),
		chi.URLParam(r, "code"), in.Password, visitorID,
		clientIP(r), r.UserAgent())
	if err != nil {
		writeGuestShareErr(w, err)
		return
	}
	writeOK(w, libraryShareTokenDTO{
		Token:      res.Token,
		HasToken:   res.HasToken,
		IsNewVisit: res.IsNewVisit,
		Share:      libraryShareVisitorDTOOf(res.Share),
	})
}

// shareRecordEvent POST /share-play/{code}/event —— 前端上报 open/play/visitor。
//
// 这个端点**不要求令牌**：访客可能只是打开了页面还没拿到令牌（比如正卡在口令框），
// 而「打开了页面」本身就是一条要被记的统计。
// 它不构成安全边界 —— 真正的计数在取流和签发令牌两条路径上。
func (h *Handler) shareRecordEvent(w http.ResponseWriter, r *http.Request) {
	if !h.shareEnabled(w) {
		return
	}
	var in libraryShareEventReq
	if !decodeJSONBody(w, r, &in) {
		return
	}
	if err := h.libraryShare.RecordEvent(r.Context(),
		chi.URLParam(r, "code"), in.VisitorID, clientIP(r),
		in.Event, in.Label); err != nil {
		writeGuestShareErr(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

// shareStream GET/HEAD /share-play/{code}/stream —— 访客取流。
//
// 这里是验收⑧的关键：这一行之后直接是 h.playback.ServeHTTP，
// 与 /cas/play、/api/strm/play、/api/files/download 走的是**同一个**播放网关：
// 账号权限校验（exec.Check）、Resolve 直链、PickAction 挑 302/代理、
// serveStream 处理 Range 全都复用。没有第二套播放链路。
//
// 为什么 code 要出现在路径里而令牌只在头里：
// code 决定「放哪个文件」，令牌决定「你能不能放」。两者分开，
// 于是「把链接转发给第三个人」这件事天然不成立 —— 他有门牌号但没钥匙。
// 反过来如果令牌也在 URL 里，转发链接就等于转发钥匙。
func (h *Handler) shareStream(w http.ResponseWriter, r *http.Request) {
	if !h.shareEnabled(w) {
		return
	}
	sh, visit, err := h.authorizeShareToken(r)
	if err != nil {
		writeGuestShareErr(w, err)
		return
	}
	// ⚠️ code 也要对：令牌属于哪条分享由库里说了算，
	// 但路径里的 code 若与令牌对应的分享不是同一条，绝不能放行
	// （否则「A 的令牌 + B 的链接」能拿到 B 的文件）。
	if !strings.EqualFold(sh.Code, chi.URLParam(r, "code")) {
		writeGuestShareErr(w, medialibshare.ErrBadToken)
		return
	}
	// ⚠️ Intent 里**不能**填 FileName。
	//
	// 一开始这里传的是 sh.Title（「孤独摇滚 S01E01」这类展示名），
	// 结果取流回来的 Content-Type 是 application/octet-stream：
	// playback 用 FileName 推 Content-Type 和 Content-Disposition，
	// 而展示名没有扩展名 ⇒ mime.TypeByExtension 返回空 ⇒ 回落成二进制流，
	// 浏览器拿 <video> 播不了，下载下来的文件名也是错的。
	//
	// 留空让 playback 自己从驱动解析出的真实文件名取，两个头就都对了。
	// 分享标题只是给人看的标签，播放链路该认的是文件本身。
	if err := h.playback.ServeHTTP(w, r,
		playback.Request{AccountID: sh.AccountID, FileID: sh.FileID},
		playback.Intent{Inline: true},
	); err != nil {
		writeGuestShareErr(w, err)
		return
	}
	// RecordPlay 放在 ServeHTTP **之后**：ServeHTTP 内部已经
	// 校验了账号权限并解析出直链，失败时它自己会写错误响应。
	// 放在前面会把「没权限的文件」也计成一次播放。
	_ = h.libraryShare.RecordPlay(r.Context(), sh, visit, clientIP(r), "share", sh.Title)
}

// authorizeShareToken 校验 X-Share-Token 头。
func (h *Handler) authorizeShareToken(r *http.Request) (medialibshare.Share, medialibshare.Visit, error) {
	token := strings.TrimSpace(r.Header.Get(ShareTokenHeader))
	if token == "" {
		return medialibshare.Share{}, medialibshare.Visit{}, medialibshare.ErrBadToken
	}
	return h.libraryShare.AuthorizeToken(r.Context(), token)
}

// ---------------------------------------------------------------- 页面

// RegisterLibrarySharePageRoutes 注册访客分享页（HTML）。
//
// 只有一条路由，且**必须**注册在 router.go 里 spaHandler 的 `/*` 之前：
// `/share/{code}` 走的是同一个内嵌前端包（不是像求片站那样另开一次构建），
// 因为分享页和管理台共用一个产物目录，拆成两次构建只会多一处「两份不同步」。
//
// 与访客 API 一样，enabled=false 时不注册 ⇒ 404（验收⑨）。
func (h *Handler) RegisterLibrarySharePageRoutes(r chi.Router) {
	if h.libraryShare == nil {
		return
	}
	r.Get("/share/{code}", h.librarySharePage)
}

// librarySharePage GET /share/{code} —— 返回分享页。
//
// 这里只把短码交给前端，连「这条短码存不存在」都不查：
// 页面是 SPA，短码有效性由前端调 /share-play/{code}/token 时才判定。
// 多一次查询换来的好处只是「打不开时页面能早 200ms 报错」，
// 而代价是**每个访客的第一屏都要先打一次库** —— 对一个可能只在手机上
// 打开一次的页面，这笔不划算。错误信息前端会从 token 接口拿到。
func (h *Handler) librarySharePage(w http.ResponseWriter, r *http.Request) {
	if !h.shareEnabled(w) {
		return
	}
	index, err := EmbeddedWebIndexHTML()
	if err != nil {
		writeGuestShareErr(w, err)
		return
	}
	// no-store：分享页是按短码定制的入口，缓存它等于把片名留在中间代理里。
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodHead {
		_, _ = w.Write(index)
	}
}

// ---------------------------------------------------------------- 错误

// writeGuestShareErr 把访客侧错误归一为响应。
//
// 状态码的取法：访客端点没有会话，403 会让浏览器弹一个「登录」框
// （那是管理员会话的语义），401 同理。所以：
//   - 缺令牌 / 令牌无效 → 401，但 **不带** WWW-Authenticate，
//     且文案是「请重新打开分享链接」而不是「需要认证」——
//     前者告诉访客怎么做，后者会让访客去找管理员要账号。
//   - 口令错 / 参数错 → 400。
//   - 设备满 → 429（带 Retry-After 感觉合适，但那是 1 天后，
//     写上只会让人干等，所以不写）。
func writeGuestShareErr(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, medialibshare.ErrBadToken):
		writeJSON(w, http.StatusUnauthorized, Resp{
			Success:   false,
			Message:   "分享链接需要重新打开：令牌缺失、无效或已过期",
			ErrorType: string(domain.CodeAuthExpired),
		})
	case errors.Is(err, medialibshare.ErrPasswordRequired),
		errors.Is(err, medialibshare.ErrBadPassword),
		errors.Is(err, medialibshare.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, Resp{
			Success:   false,
			Message:   err.Error(),
			ErrorType: string(domain.CodeValidation),
		})
	case errors.Is(err, medialibshare.ErrDeviceLimit):
		writeJSON(w, http.StatusTooManyRequests, Resp{
			Success:   false,
			Message:   err.Error(),
			ErrorType: string(domain.CodeRateLimited),
		})
	case errors.Is(err, medialibshare.ErrNotFound),
		errors.Is(err, medialibshare.ErrDisabled):
		writeJSON(w, http.StatusNotFound, Resp{
			Success:   false,
			Message:   "分享不存在或已失效",
			ErrorType: string(domain.CodeNotFound),
		})
	default:
		// 播放网关的错误已经是 AppError（账号无权限、文件不存在等），
		// 这里保留它的状态码与文案 —— 它们本来就是给人看的。
		if ae, ok := domain.AsAppError(err); ok {
			writeJSON(w, ae.HTTPStatus(), Resp{
				Success:   false,
				Message:   ae.Message,
				ErrorType: string(ae.Code),
			})
			return
		}
		// 兜底：认不出来的错误不外泄底层文本。
		writeJSON(w, http.StatusInternalServerError, Resp{
			Success:   false,
			Message:   "分享服务内部错误",
			ErrorType: string(domain.CodeInternal),
		})
	}
}
