package controllers

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
)

// 回归：emby302 反代路径下，中间件（ApiKeyChecker）触发 gin queryCache 固化后，
// setStrmQuery 修改 RawQuery，此时 c.Query 读到的是旧缓存（STRM URL 里的参数丢失），
// 必须用 url.ParseQuery(c.Request.URL.RawQuery) 实时解析才能拿到 account/fileid。
// 见 GetPan139UrlByFileId 的 liveQuery 处理。
func TestPan139LiveQueryReadsMergedStrmParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// 模拟播放器原始请求（Emby stream 请求，无 account/pickcode 参数）
	playerURL, _ := url.Parse("http://127.0.0.1:12333/emby/videos/70435/stream.mkv?DeviceId=iPhone&MediaSourceId=abc&X-Emby-Token=tok&Static=true")
	req := httptest.NewRequest("GET", playerURL.String(), nil)
	c.Request = req

	// 中间件先读一次 query —— 触发 gin queryCache 固化（此时还没有 STRM 参数）
	if got := c.Query("X-Emby-Token"); got != "tok" {
		t.Fatalf("前置中间件读取 X-Emby-Token 失败：%q", got)
	}

	// setStrmQuery：把 STRM 内容里的参数合并进 RawQuery 并清空 Form（emby302 strm_redirect.go 同款逻辑）
	strmParams := url.Values{
		"account":  {"3"},
		"pickcode": {"FnuwohjkEz5thimYZ979IX4szvGzEhNyb"},
		"path":     {"/media/影视/已整理/x.mp4"},
	}
	cur := c.Request.URL.Query()
	for k, vs := range strmParams {
		for _, v := range vs {
			cur.Set(k, v)
		}
	}
	c.Request.URL.RawQuery = cur.Encode()
	c.Request.Form = nil

	// 控制器用 gin 缓存读取：account 丢失（复现旧 bug 行为，证明 c.Query 不可用）
	if got := c.Query("account"); got != "" {
		t.Fatalf("预期 gin queryCache 固化导致 c.Query(\"account\") 为空，实际=%q", got)
	}

	// 控制器实时解析：account 可读（修复后的行为）
	liveQuery, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		t.Fatalf("实时解析 RawQuery 失败：%v", err)
	}
	if got := liveQuery.Get("account"); got != "3" {
		t.Fatalf("实时解析 account 失败：%q", got)
	}
	if got := liveQuery.Get("fileid"); got != "" {
		t.Fatalf("fileid 应为空（新链接用 pickcode）：%q", got)
	}
	if got := liveQuery.Get("pickcode"); got != "FnuwohjkEz5thimYZ979IX4szvGzEhNyb" {
		t.Fatalf("实时解析 pickcode 失败：%q", got)
	}
}
