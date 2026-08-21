package web

import (
	"diy-strm/emby302/config"
	"diy-strm/emby302/service/emby"
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
