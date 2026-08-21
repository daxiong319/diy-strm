package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"diy-strm/emby302/config"

	"github.com/gin-gonic/gin"
)

func TestInitRouterNoRouteFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 初始化最小配置, 避免 handler 中空指针 panic
	if err := config.ReadFromFile([]byte(`emby:
  host: http://localhost:8096
cache:
  enable: false
`)); err != nil {
		t.Fatal(err)
	}

	r := gin.New()

	// 模拟管理页静态路由与 API
	r.StaticFS("/assets", http.Dir(t.TempDir()))
	r.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "admin") })
	r.GET("/115/url/*filename", func(c *gin.Context) { c.String(http.StatusOK, "115url") })

	// 挂载 emby302 兜底路由, 若路由树冲突 (/*vars) 这里会 panic
	InitRouter(r)

	// 1 管理页路由优先匹配
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "admin" {
		t.Fatalf("管理页根路由被劫持: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/115/url/video.mkv?pickcode=abc", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "115url" {
		t.Fatalf("115 直链路由被劫持: %d %s", w.Code, w.Body.String())
	}

	// 2 未注册路径 (Emby 反代) 进入 NoRoute 兜底分发器, 不 panic 即可
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/emby/Items/1/PlaybackInfo?api_key=test", nil)
	r.ServeHTTP(w, req)

	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/videos/1/stream?MediaSourceId=2&api_key=test", nil)
	r.ServeHTTP(w, req)
}
