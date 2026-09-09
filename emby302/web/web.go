package web

import (
	"fmt"
	"net/http"

	"diy-strm/emby302/config"
	"diy-strm/emby302/service/emby"
	"diy-strm/emby302/util/logs"
	"diy-strm/emby302/web/cache"

	"github.com/gin-gonic/gin"
)

// InitRouter 将 Emby 302 反代路由挂载到外部 gin.Engine 上
//
// 通过 NoRoute 兜底注册: 管理页的具体路由优先匹配,
// 其余请求 (Emby 反代 / 播放 302) 全部进入本分发器。
// 中间件只作用于兜底请求, 不影响管理页接口。
//
// 注意: 不能使用 r.Any("/*vars") 注册, 会与已注册的静态路由
// (如 /assets) 冲突导致 gin 路由树 panic。
func InitRouter(r *gin.Engine) {
	initRulePatterns()

	handlers := []gin.HandlerFunc{
		referrerPolicySetter(),
		emby.ApiKeyChecker(),
		emby.DownloadStrategyChecker(),
	}
	if config.C.Cache.Enable {
		handlers = append(handlers, cache.CacheableRouteMarker(), cache.RequestCacher())
	}
	handlers = append(handlers, globalDftHandler)

	r.NoRoute(handlers...)
}

// StandaloneServer 独立端口形态的独立 Emby 302 反代服务：
// 与管理页完全隔离的专用端口，根路径即 Emby 本体（浏览器/播放器打开该端口看到的就是 Emby），
// 302 直链、STRM 指针、反代回源等行为与合并端口模式一致。
type StandaloneServer struct {
	port     string
	listener *http.Server
}

// StartStandalone 启动独立反代端口（阻塞前先校验端口占用由 http.Server 返回错误）。
func StartStandalone(port string) (*StandaloneServer, error) {
	initRulePatterns()

	r := gin.New()
	r.Use(gin.Recovery())
	// 与合并模式 NoRoute 链一致的中间件，独立端口下作为引擎级中间件全量生效
	r.Use(referrerPolicySetter())
	r.Use(emby.ApiKeyChecker())
	r.Use(emby.DownloadStrategyChecker())
	if config.C.Cache.Enable {
		r.Use(cache.CacheableRouteMarker())
		r.Use(cache.RequestCacher())
	}
	// 独立引擎无其它具体路由，catch-all 兜底接管全部请求（含根路径反代 Emby 首页）
	r.NoRoute(globalDftHandler)

	addr := "0.0.0.0:" + port
	srv := &http.Server{Addr: addr, Handler: r}
	go func() {
		logs.Success("Emby 302 独立反代端口已启动：%s（根路径即 Emby，浏览器/播放器直接填该端口）", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logs.Error("Emby 302 独立反代端口退出：%v", err)
		}
	}()
	return &StandaloneServer{port: port, listener: srv}, nil
}

// Stop 停止独立反代端口
func (s *StandaloneServer) Stop() {
	if s == nil || s.listener == nil {
		return
	}
	if err := s.listener.Close(); err != nil && err != http.ErrServerClosed {
		logs.Error("停止 Emby 302 独立反代端口失败：%v", err)
	}
	logs.Info("Emby 302 独立反代端口已停止：0.0.0.0:%s", s.port)
}

// RestartStandalone 按 (重)启独立反代端口；port 为空串或 "0" 表示停止并保持关闭。
// 返回当前运行中的服务实例（关闭时返回 nil）。
func RestartStandalone(old *StandaloneServer, port string) (*StandaloneServer, error) {
	old.Stop()
	if port == "" || port == "0" {
		return nil, nil
	}
	srv, err := StartStandalone(port)
	if err != nil {
		return nil, fmt.Errorf("启动 Emby 302 独立反代端口 %s 失败：%w", port, err)
	}
	return srv, nil
}
