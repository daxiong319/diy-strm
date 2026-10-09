package api

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"litepan/internal/adminauth"
	"litepan/internal/domain"
	"litepan/internal/mediarequest"
)

// ---------------------------------------------------------------- 身份
//
// 求片站有**自己的**会话（mediarequest.PortalCookieName），不复用 admin_session。
// 理由见 session.go 的 PortalCookieName 注释：cookie 不跨端口隔离，
// 混用会让两个端口显示互相矛盾的「当前身份」，而且求片站登出会把管理台一起踢掉。

// portalPrincipalCtxKey 求片站当前身份（Principal）。
type portalPrincipalCtxKey struct{}

// portalPrincipalFrom 取出求片站当前主体。
func portalPrincipalFrom(ctx context.Context) Principal {
	p, _ := ctx.Value(portalPrincipalCtxKey{}).(Principal)
	return p
}

// portalSession 返回求片站会话；没有登录时返回 nil,false。
func (h *Handler) portalSession(r *http.Request) (*mediarequest.SessionIdentity, bool) {
	if h.requestSigner == nil {
		return nil, false
	}
	id, ok := h.requestSigner.Read(r)
	if !ok {
		return nil, false
	}
	return &id, true
}

// portalActor 从会话算出求片站主体（Actor）。
//
// UserID==0 只在两种情况下出现：会话里就写着超管（真超管登录过），
// 或者…没有第二种 —— isSuper 由「会话用户名 == 当前 admin_username」判定，
// 与 T08 的 PrincipalFor 同一套规则，改一处不会漏另一处。
func (h *Handler) portalActor(ctx context.Context, id *mediarequest.SessionIdentity) (mediarequest.Actor, bool) {
	if id == nil {
		return mediarequest.Actor{}, false
	}
	return mediarequest.Actor{UserID: id.UserID, Username: id.Username, IsSuper: id.IsSuper}, true
}

// loadPortalPrincipal 依据会话重新算出 Principal。
//
// 每请求重算是刻意的：cookie 里只存身份，权限每次现算。
// 这样管理员在后台把人移除组之后，**下一个请求**就失效，
// 而不是让一个已经被收回权限的人继续用到 cookie 自然过期。
//
// 注意 IsSuper 用的是这里现算出来的结果，**不是** cookie 里的快照：
// 超管身份由「会话用户名 == 当前 admin_username」判定（与 T08 PrincipalFor
// 同一套规则），所以管理员改了 admin_username 之后，旧 cookie 立刻失去超管身份。
// 这一点必须在两个端口上完全一致，否则会出现「求片站还是超管、管理台已经不是」
// 这种更难查的现象。
func (h *Handler) loadPortalPrincipal(ctx context.Context, id *mediarequest.SessionIdentity) (Principal, error) {
	if h.rbac == nil {
		return Principal{}, nil
	}
	sess := rbacSession(&adminauth.Session{Username: id.Username, UserID: id.UserID})
	return h.rbac.PrincipalFor(ctx, sess)
}

// ---------------------------------------------------------------- 中间件

// requirePortalSession 要求已登录，未登录跳登录页（页面请求）或 401（接口请求）。
func (h *Handler) requirePortalSession(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := h.portalSession(r)
		if !ok {
			h.portalUnauthorized(w, r)
			return
		}
		p, err := h.loadPortalPrincipal(r.Context(), id)
		if err != nil {
			writeErr(w, domain.Wrap(domain.CodeInternal, err))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), portalPrincipalCtxKey{}, p)))
	}
}

// portalUnauthorized 未登录的回应。
//
// 页面请求 → 302 到 /login（用 Accept 判断，避免把浏览器导航也当成接口调用）；
// 接口请求 → 401 且带一个前端能识别的 error_type。
func (h *Handler) portalUnauthorized(w http.ResponseWriter, r *http.Request) {
	if wantsHTML(r) {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	writeJSON(w, http.StatusUnauthorized, Resp{
		Success:   false,
		Message:   "请先登录",
		ErrorType: string(domain.CodeAdminAuthRequired),
	})
}

// wantsHTML 判断请求是否来自浏览器导航。
func wantsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// portalEnabledGuard 求片总开关。
//
// 关闭时求片站不接活。注意**端口还会监听**（最多 15 秒），但所有业务接口
// 都已关闭 —— 这样「关了立刻就没人能提交」不需要等监听口收掉，
// 也不用依赖 supervisor 的轮询时机。验收⑧要求的是「不监听」，
// 那由 Listener 负责；这里是更严的一层：就算有人手工把请求打进这个口，
// 也一律 503。
func (h *Handler) portalEnabledGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.mediaRequest == nil || !h.mediaRequest.Enabled() {
			writeJSON(w, http.StatusServiceUnavailable, Resp{
				Success:   false,
				Message:   "求片中心未启用",
				ErrorType: string(domain.CodeNotImplement),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- 路由树

// RegisterRequestPortalRoutes 装请求片站的路由树。
//
// ⚠️ **这是本任务最容易做错的一处，务必读注释。**
//
// 任务书要求：求片端口上「只有登录页 + 求片页，后台接口一律 404」。
// 这里刻意**不**注册 `/api/*` 之外的任何管理后台路由，也不复用 `spaHandler`
// （它对未命中的路径一律回落 index.html，会把管理台整页送到求片站上）。
//
// 更重要的是**不注册兜底通配**：
//   - 不 `r.NotFound(spa)`（会把 404 变成 200 + 管理台页面）
//   - 不 `r.Mount("/api", adminRouter)`（那就等于把后台整个搬过来了）
//
// chi 的 NotFound 保持默认 404，于是「求片端口上不存在后台接口」这件事
// 由**结构**保证：将来有人给管理台加路由，这条路由不会、也不可能
// 出现在这个树上。逐条测试遍历（TestRequestPortalRejectsEveryAdminRoute）
// 只是再确认一次这个结构事实，不是它的前提。
func (h *Handler) RegisterRequestPortalRoutes(r chi.Router) {
	// 页面：两个入口共用同一个 SPA（单页应用内自己按 pathname 分流）。
	//
	// 用显式注册而不是「任意路径都回首页」——
	// 任务书只允许这两个页面存在，`/foo` 必须 404 而不是显示求片页。
	page := h.portalEnabled(http.HandlerFunc(h.portalPage))
	r.Get("/login", page)
	r.Get("/", page)

	// 静态资源：只放行构建产物目录。
	//
	// 单独一个 `r.Get("/assets/*", ...)` 而不是通配，是因为
	// /assets 下只可能有构建出来的 js/css/字体，宽一点也只是多几个静态文件；
	// 但如果哪天有人把 `r.Get("/*", portalPage)` 加进来做深链，
	// 404 隔离就全废了 —— TestRequestPortalRejectsEveryAdminRoute 会第一时间报出来。
	r.Get("/assets/*", h.portalEnabled(http.HandlerFunc(h.portalAsset)))

	// 接口。
	r.Post("/api/login", h.portalEnabled(http.HandlerFunc(h.portalLogin)))
	r.Post("/api/logout", h.portalEnabled(http.HandlerFunc(h.portalLogout)))
	r.Get("/api/me", h.requirePortalSession(h.portalEnabled(http.HandlerFunc(h.portalMe))))
	r.Get("/api/search", h.requirePortalSession(h.portalEnabled(http.HandlerFunc(h.portalSearch))))
	r.Post("/api/request", h.requirePortalSession(h.portalEnabled(http.HandlerFunc(h.portalSubmit))))
	r.Get("/api/mine", h.requirePortalSession(h.portalEnabled(http.HandlerFunc(h.portalMine))))
	r.Put("/api/tags", h.requirePortalSession(h.portalEnabled(http.HandlerFunc(h.portalSaveTags))))
	r.Get("/api/stats", h.requirePortalSession(h.portalEnabled(http.HandlerFunc(h.portalStats))))
}

// portalEnabled 给单个处理器套上「功能未启用就 503」的闸。
//
// **刻意不用 r.Use 装这个闸。** chi 的 Use 中间件包的是 routeHTTP，
// 也就是说它在**任何**请求上都会执行，包括没匹配到任何路由的请求 ——
// 于是 `/foo` 这种根本不存在的路径会拿到 503「求片中心未启用」而不是 404。
// 后果不只是难看：求片端口上所有路径都变成 503 之后，
// 「求片站没有后台接口」这条性质就没法验证了
// （TestRequestPortalRejectsEveryAdminRoute 会全红），
// 而且用户在地址栏打错一个字看到的也是「未启用」，比「页面不存在」困惑得多。
//
// 这里在**注册时**就把中间件链和具体处理器绑好（不是每请求绑一次，
// `h.portalEnabledGuard(next).ServeHTTP` 是方法值，每请求会重新分配一个包装器）：
// 匹配上的路由才有这层闸，没匹配上的走 chi 原生的纯 404。
func (h *Handler) portalEnabled(next http.HandlerFunc) http.HandlerFunc {
	return h.portalEnabledGuard(next).ServeHTTP
}

// ---------------------------------------------------------------- 页面与静态资源

// portalPage 返回求片站入口 HTML。
//
// 入口文件带 no-store 缓存：发版后浏览器必然重新拉取入口，
// 从而引用到新的 hash 资源（与管理台 index.html 同一条理由）。
func (h *Handler) portalPage(w http.ResponseWriter, r *http.Request) {
	if h.portalFS == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(portalBuildHint))
		return
	}
	h.portalCacheHeader(w, PortalEntryFile)
	_, _ = w.Write(h.portalHTML)
}

// portalBuildHint 前端没构建时给出的说明。
//
// 直接写清楚「跑 npm run build」，比给一个空白页有用得多。
const portalBuildHint = `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0"><title>求片中心</title></head>
<body style="font-family:system-ui;padding:24px;line-height:1.8">
<h1>求片中心前端未构建</h1>
<p>请在 <code>web/</code> 目录下执行 <code>npm install &amp;&amp; npm run build</code> 后重启服务。</p>
</body></html>`

// portalAsset 服务 /assets 下的构建产物。
func (h *Handler) portalAsset(w http.ResponseWriter, r *http.Request) {
	if h.portalFS == nil {
		http.NotFound(w, r)
		return
	}
	upath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	// path.Clean 会把 ../ 折掉，这里再钉一次前缀：宁可 404 也不能读出去。
	if !strings.HasPrefix(upath, "assets/") || strings.Contains(upath, "..") {
		http.NotFound(w, r)
		return
	}
	f, err := h.portalFS.Open(upath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	h.portalCacheHeader(w, upath)
	// ServeContent 需要 io.ReadSeeker（要支持 Range 与条件请求）；
	// fs.File 只保证 io.Reader，所以先把内容读进内存再交给它。
	// 构建产物都是几百 KB 量级，embed 本来就在二进制里，再读一遍可接受。
	data, err := io.ReadAll(f)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(data))
}

// portalCacheHeader 设置静态缓存头（与管理台同一套理由）。
func (h *Handler) portalCacheHeader(w http.ResponseWriter, upath string) {
	if strings.HasPrefix(upath, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
}

// EmbeddedWebFS 返回内嵌的前端构建产物根。
//
// 装配层要靠它把求片站的两样东西（入口页、assets）喂给 Deps，
// 而 webFS 是包级 embed、不可变，所以这里只导出一个只读访问器而不是搬走它。
func EmbeddedWebFS() fs.FS { return webFS }

// PortalBuildDir 是求片站构建产物在 webFS 里的子目录。
//
// 求片站**单独跑一次 Vite 构建**（web/vite.request.config.ts），
// 产物落在 web/<PortalBuildDir>/ 下，里面只有它自己的 request.html 与 assets/。
// 目录名与后台的 web/index.html、web/assets/ 完全错开，于是
// 「求片站发不出后台的静态包」和「求片站没有后台接口」一样，
// 由目录结构保证，而不是靠代码里逐个 if 排除。
const PortalBuildDir = "web/request"

// PortalEntryFile 是 PortalBuildDir 里的入口文件名。
//
// 为什么不是 index.html：Vite 给 html 入口的产物名直接取**源文件名**，
// 既不认 rollupOptions.input 的 key，也不认改过的 outDir。源文件若放在
// web/portal/index.html，产物会变成 request/portal/index.html（多套一层目录）；
// 源文件若叫 index.html，又会和后台的 web/index.html 撞名。
// 干脆两边都用同一个具名常量，改名时只会有一处编译不过。
const PortalEntryFile = "request.html"

// LoadPortalFS 从 webFS 里取出求片站构建产物（入口页 + assets/）。
//
// ⚠️ 这里**不能**退化成 `fs.Sub(webFS, "web")`：那等于把整个后台产物目录
// 交给求片站，`/assets/index-xxxx.js` 就能把后台的主包发出去。
// 两套产物同在一个 outDir 下时「哪些 chunk 属于求片站」只能靠文件名猜，
// 猜错一次就是一个静默的跨站泄漏；分目录则根本不存在这个问题。
//
// 单独一个函数是为了让「产物缺失」这件事在**装配期**就能测到：
// 请求进来才 404 的排查成本，远高于启动时一条日志。
func LoadPortalFS(webFS fs.FS) (assets fs.FS, index []byte) {
	sub, err := fs.Sub(webFS, PortalBuildDir)
	if err != nil {
		return nil, nil
	}
	index, err = fs.ReadFile(sub, PortalEntryFile)
	if err != nil {
		return nil, nil
	}
	return sub, index
}

// NewRequestPortalRouter 构造求片站自己的路由树。
//
// 与 NewRouter **完全独立**：不共享 mux、不共享中间件、不共享 NotFound。
// 这是「求片端口上不存在后台接口」的实现方式 ——
// 管理台的路由树加多少条路由都影响不到这里，因为这里压根没有引用它。
//
// 返回的 handler 会被 mediarequest.Listener 直接 Serve。
func NewRequestPortalRouter(d Deps) http.Handler {
	h := newHandler(d)
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	// 恢复 panic 是必须的：求片站是独立监听口，
	// 一个处理器里的 nil 解引用会让**整个进程**挂掉，
	// 连管理台一起陪葬 —— 那是「一个手机端 bug 打垮后台」的典型剧本。
	r.Use(chimw.Recoverer)
	// 刻意不装 requireAdmin / requirePermission / attachRequestLogger：
	// 它们都属于管理台那棵树。在求片站上重装一遍只会多出
	// 「这个中间件会不会在求片端口上误拦」这种排查题。
	h.RegisterRequestPortalRoutes(r)
	return r
}

// principalActor 把主体换成求片服务认识的 Actor。
//
// Username 一律取**显示名**（没有显示名才退回登录名），理由是这一栏会出现在
// 两处给家里人看的地方：「这部作品已经有人在求了（提交人：×××）」和
// 管理台审核列表里的申请人。存登录名的话，家里人的账号叫 xiaoming、
// 显示名叫「小明」，审核员看到的就是一串拼音。
// 归属靠 UserID，所以改显示名不会把历史求片单算到别人头上。
//
// 写成函数而不是 Principal 的方法：Principal 是 rbac.Principal 的类型别名，
// 外部类型不能加方法。
func principalActor(p Principal) mediarequest.Actor {
	name := strings.TrimSpace(p.DisplayName)
	if name == "" {
		name = p.Username
	}
	return mediarequest.Actor{UserID: p.UserID, Username: name, IsSuper: p.IsSuper}
}
