package playback

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"litepan/internal/cache"
	"litepan/internal/core/driverexec"
	"litepan/internal/domain"
	"litepan/internal/driver"
)

type Service struct {
	exec        *driverexec.Executor
	cache       *cache.Service
	clientHTTP1 *http.Client
	clientH2    *http.Client
	rangeLimits accountRangeLimiter
	resolveHook DownloadResolverHook
	// redirectObserver 在每次 302 交付后回调一次，用于旁路落播放记录。
	redirectObserver RedirectObserver
	// streamMonitor 播放监控钩子（T11）。nil = 未注入，等价于监控关闭。
	// 注入后由取流入口按「三态」分流回调，监控器自己包字节计数器，
	// 这样 playback 侧不需要知道流量怎么算。
	streamMonitor StreamMonitor
	log           *slog.Logger
}

// DownloadResolverHook 允许外部插件在驱动解析前接管下载直链。
// 返回 handled=true 时使用返回的 DownloadInfo；handled=false 时回落驱动默认解析。
// playback 为 true 表示本次解析用于“播放/流式”（可接受转码直链），false 表示字节级读取（必须源文件）。
type DownloadResolverHook func(ctx context.Context, accountID int64, driverType, fileID, ua string, playback bool) (*domain.DownloadInfo, bool, error)

func NewService(exec *driverexec.Executor, c *cache.Service) *Service {
	return &Service{
		exec:        exec,
		cache:       c,
		clientHTTP1: &http.Client{Transport: newUpstreamTransport(false), CheckRedirect: stripRedirectReferer},
		clientH2:    &http.Client{Transport: newUpstreamTransport(true), CheckRedirect: stripRedirectReferer},
	}
}

// SetDownloadResolverHook 注入下载解析接管钩子，仅在服务启动前调用一次。
func (s *Service) SetDownloadResolverHook(h DownloadResolverHook) {
	s.resolveHook = h
}

// SetLogger 注入日志器；未注入时不记日志。
func (s *Service) SetLogger(log *slog.Logger) {
	s.log = log
}

// logAction 在 debug 级别记录本次播放的交付方式。
// 「交给播放层处理」不等于「由 LitePan 中转字节」：是否 302 取决于账号的下载模式，
// 排查播放问题时必须能看到最终动作。只记目标 host，直链里带签名/token，不能进日志。
// user_agent 用于分辨是哪个播放器回来取的流（直读约定下这一步是否发生是分水岭）。
func (s *Service) logAction(action string, mode domain.DownloadMode, rawURL, ua string) {
	if s == nil || s.log == nil {
		return
	}
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Host
	}
	modeName := "proxy"
	if mode == domain.DownloadRedirect {
		modeName = "redirect"
	}
	s.log.Debug("播放请求交付方式", "action", action, "mode", modeName, "target_host", host, "user_agent", ua)
}

func stripRedirectReferer(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	prev := via[len(via)-1]
	if prev.URL.Host != req.URL.Host || prev.URL.Scheme != req.URL.Scheme {
		req.Header.Del("Referer")
	}
	return nil
}

type Request struct {
	AccountID int64
	FileID    string
}

// RedirectObserver 在取流入口决定走 302 后回调一次，用于旁路落播放记录。
// accountID 为云盘账号，直接落在 Resolved 文件与下载链接上。
type RedirectObserver func(r *http.Request, accountID int64, res Resolved, intent Intent)

// SetRedirectObserver 注入 302 旁路观察者，仅在服务启动前调用一次。
func (s *Service) SetRedirectObserver(fn RedirectObserver) {
	s.redirectObserver = fn
}

// StreamMonitor 播放监控钩子（T11）。由 internal/playmonitor.Service 实现。
//
// ⚠️ 302 分支与流代理分支是**两个不同的回调**，这不是冗余而是口径：
// 302 直连的字节流完全不经过自己的服务器，计费为 0，
// 所以它只配 OnRedirectOpen（记一次「谁去拉直链了」），没有流量。
// 把两条分支合并成一个回调，迟早会在里面写出「if 计费则累加」的条件，
// 而那个条件的依据（PickAction 的结果）在两条分支里根本拿不到。
type StreamMonitor interface {
	// OnStreamOpen 字节流经自己服务器（PickAction 选中流代理）。
	// 返回包了字节计数的 http.ResponseWriter；
	// 返回 nil 表示这次请求不该建会话（监控关闭 / 拿不到条目名），
	// 调用方必须**原样使用传入的 w**。
	OnStreamOpen(r *http.Request, ev domain.StreamEvent) http.ResponseWriter
	// OnStreamStop 会话关闭（心跳丢失），转交一次停止事件。
	OnStreamStop(id string)
	// OnRedirectOpen 302 到网盘直链：只记 open，**不记流量**（计 0）。
	OnRedirectOpen(r *http.Request, ev domain.StreamEvent)
}

// SetStreamMonitor 注入播放监控钩子，仅在服务启动前调用一次。
//
// 钩子挂在 ServeHTTP 而不是各个 api handler 上：CAS 播放入口
// （internal/api/cas.go 的 casPlay）也走 ServeHTTP，挂 api 层会漏掉它。
func (s *Service) SetStreamMonitor(m StreamMonitor) {
	s.streamMonitor = m
}

// streamEvent 组装一次取流请求的监控事件。（账号、条目、请求/直链、来源）；
// 码率由监控器从「这次请求实际写出了多少字节、花了多久」反推，
// 比在这里猜 Content-Length 准 —— 一个 40GB 的电影按总长平均算
// 会得到几 Kbps 的荒谬值。storage_slug/type 是 115 时代的字段名，
// litepan 的对应概念是「哪个存储驱动」，由监控器按账号解析后填。
func streamEvent(r *http.Request, req Request, res Resolved, intent Intent, name string) domain.StreamEvent {
	return domain.StreamEvent{
		AccountID:   req.AccountID,
		ItemName:    name,
		StrmPath:    r.URL.Path,
		RequestURL:  r.URL.Path,
		OriginalURL: res.Link.URL,
		ClientIP:    clientIP(r),
		UserAgent:   r.UserAgent(),
	}
}

// wrapStreamMonitor 让监控器在流代理分支包一层字节计数器。
//
// 没注入监控器、或监控器判定这次请求不该建会话时，返回的 writer 为 nil，
// 调用方必须**原样使用传入的 w** —— 拿 nil 去写会 panic，
// 而这个 nil 分支恰好是「监控关闭」这条最常见的路径。
func (s *Service) wrapStreamMonitor(r *http.Request, ev domain.StreamEvent) http.ResponseWriter {
	if s.streamMonitor == nil {
		return nil
	}
	return s.streamMonitor.OnStreamOpen(r, ev)
}

// clientIP 取客户端 IP：X-Forwarded-For 首段 → RemoteAddr 截断。
// 与 internal/adminauth/service.go:850 同口径；播放监控还要用它判局域网。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request, req Request, intent Intent) error {
	if err := s.exec.Check(r.Context(), req.AccountID); err != nil {
		return err
	}
	ua := r.UserAgent()
	res, err := s.Resolve(r.Context(), req.AccountID, req.FileID, ua, false, intent.allowsPlaybackResolve())
	if err != nil {
		return err
	}
	return s.serveResolved(w, r, req, res, intent)
}

// serveResolved 是「已解析完成，按交付方式分流」的那一段。
//
// 单独成方法是为了让测试能跑**生产这一段**的字节：Resolve 需要
// exec 与驱动栈，测试里造不出来；若测试改为照抄这段分支逻辑，
// 就会出现两份实现，生产改了测试还绿 —— 那样的用例什么都证明不了。
// 目录校验也跟着搬进来，它和分支决策是一段不可拆的逻辑。
func (s *Service) serveResolved(w http.ResponseWriter, r *http.Request, req Request, res Resolved, intent Intent) error {
	if res.File.IsDir {
		return domain.Errorf(domain.CodeValidation, "不能下载目录")
	}
	ua := r.UserAgent()
	action := PickAction(res.Mode, res.Link, intent)
	if action == ActionRedirect {
		s.logAction("redirect", res.Mode, res.Link.URL, ua)
		if s.redirectObserver != nil {
			s.redirectObserver(r, req.AccountID, res, intent)
		}
		// 302 分支：监控**只记一次 open，不记任何流量**。
		// 字节流后面直接从网盘发给播放器，自己服务器一个字节都没吐，
		// 所以这一支的上行估算恒为 0 —— 记了就是凭空多算。
		if s.streamMonitor != nil {
			s.streamMonitor.OnRedirectOpen(r, streamEvent(r, req, res, intent, intent.FileName))
		}
		writeRedirect(w, r, res, intent)
		return nil
	}
	name := intent.FileName
	if name == "" {
		name = res.File.Name
	}
	s.logAction("stream", res.Mode, res.Link.URL, ua)
	// 流代理分支：字节流经自己服务器，外网时**计费中**。
	// 监控在这里包一层字节计数器，playback 侧不碰计费口径，
	// 只保证「所有吐字节的路径都经过这个 wrapper」。
	out := s.wrapStreamMonitor(r, streamEvent(r, req, res, intent, name))
	if out == nil {
		out = w
	}
	return s.serveStream(out, r, req, res, name, ua, intent)
}

func (s *Service) Resolve(ctx context.Context, accountID int64, fileID, ua string, refresh, playback bool) (Resolved, error) {
	if s.cache == nil {
		return s.resolveFresh(ctx, accountID, fileID, ua, playback)
	}

	key := cache.DownloadURLKey(accountID, fileID, resolveCacheVariant(ua, playback))
	if refresh {
		s.cache.InvalidateKey(key)
	} else if res, ok := cache.GetAs[Resolved](s.cache, key); ok {
		return res, nil
	}

	res, err := cache.CoalesceAs[Resolved](ctx, s.cache, key, func(callCtx context.Context) (Resolved, error) {
		if !refresh {
			if cached, ok := cache.GetAs[Resolved](s.cache, key); ok {
				return cached, nil
			}
		}
		fresh, err := s.resolveFresh(callCtx, accountID, fileID, ua, playback)
		if err != nil {
			return Resolved{}, err
		}
		ttl := fresh.Link.Expiration
		if ttl <= 0 {
			ttl = defaultLinkTTL
		}
		cache.SetAs(s.cache, key, fresh, ttl)
		return fresh, nil
	})
	if err != nil {
		return Resolved{}, err
	}
	return res, nil
}

// resolveCacheVariant 将播放直链与原始文件直链隔离，避免同一 UA 的缓存串用。
func resolveCacheVariant(ua string, playback bool) string {
	if playback {
		return ua + "\x00playback"
	}
	return ua + "\x00original"
}

func (s *Service) resolveFresh(ctx context.Context, accountID int64, fileID, ua string, playback bool) (Resolved, error) {
	var res Resolved
	err := s.exec.Run(ctx, accountID, func(drv driver.Driver) error {
		file := domain.FileItem{ID: fileID}
		var link *domain.DownloadInfo
		if s.resolveHook != nil {
			if info, handled, err := s.resolveHook(ctx, accountID, drv.Config().Name, fileID, ua, playback); handled {
				if err != nil {
					return err
				}
				link = info
			}
		}
		if link == nil {
			dl, err := driverexec.Require[driver.Downloader](drv)
			if err != nil {
				return err
			}
			got, err := dl.ResolveDownload(ctx, driver.DownloadRequest{FileID: fileID, UA: ua})
			if err != nil {
				return err
			}
			link = got
		}
		if link.URL == "" && link.LocalPath == "" && !link.ForceProxy {
			return domain.Errorf(domain.CodeDriverError, "驱动未返回下载地址")
		}
		if file.Size <= 0 && link.Size > 0 {
			file.Size = link.Size
		}
		if file.Name == "" && link.FileName != "" {
			file.Name = link.FileName
		}
		if file.Name == "" || file.Size <= 0 {
			if info, ok := drv.(driver.InfoGetter); ok {
				got, err := info.GetFileInfo(ctx, fileID)
				if err != nil {
					return err
				}
				file = *got
				if file.Size <= 0 && link.Size > 0 {
					file.Size = link.Size
				}
				if file.Name == "" && link.FileName != "" {
					file.Name = link.FileName
				}
			}
		}
		mode := link.Mode
		if mode == domain.DownloadRedirect && link.ForceProxy {
			mode = domain.DownloadProxy
		}
		res = Resolved{File: file, Link: *link, Mode: mode}
		return nil
	})
	return res, err
}

func (s *Service) InvalidateAccount(accountID int64) {
	if s.cache != nil {
		s.cache.InvalidateAccountType(accountID, cache.TypeDownloadURL)
	}
}

func (s *Service) InvalidateAll() {
	if s.cache != nil {
		s.cache.InvalidatePrefix(string(cache.TypeDownloadURL) + ":")
	}
}
