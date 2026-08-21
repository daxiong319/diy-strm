package emby

import (
	"net/url"
	"regexp"
	"strings"

	"diy-strm/emby302/util/logs"
	"diy-strm/internal/controllers"

	"github.com/gin-gonic/gin"
)

// strmPointerPattern 匹配 tgto123 风格的 STRM 指针前缀
//
// 支持的形态:
//
//	play115://pickcode   play115:pickcode   /play115/pickcode
//	playgy://file_id     playgy:file_id     /playgy/file_id
//	play123://file_id    play123:file_id    /play123/file_id
//	play115share://...   (115 分享, 当前不支持, 回源处理)
var strmPointerPattern = regexp.MustCompile(`(?:^|/)(play115share|play115|playgy|play123)(?:[/:?]|$)`)

// redirectByStrmContent 根据 STRM 内容直接解析并重定向到网盘直链
//
// 支持 tgto123 风格指针 (play115:// / playgy:// / play123://)
// 以及 diy-strm 自家直链 URL (/115/url /pan123/url /guangyapan/url /baidupan/url /pan139/url)
//
// 返回 false 表示内容不识别, 由调用方走原有回源/内部请求流程
func redirectByStrmContent(c *gin.Context, strmContent string) bool {
	content := strings.TrimSpace(strmContent)
	if content == "" {
		return false
	}

	// 1. tgto123 风格指针
	if kind, payload, ok := parseStrmPointer(content); ok {
		return redirectStrmPointer(c, kind, payload)
	}

	// 2. diy-strm 自家直链 URL: 提取参数直接调用网盘直链接口, 避免内部二次 HTTP 请求
	u, err := url.Parse(content)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	q := u.Query()
	switch {
	case strings.Contains(u.Path, "/115/url") || strings.Contains(u.Path, "/115/newurl"):
		// 与原 getFinalRedirectLink 行为保持一致: 115 强制直链
		if q.Get("force") == "" {
			q.Set("force", "1")
		}
		logs.Success("STRM 自家直链 URL: 115 pickcode=%s", q.Get("pickcode"))
		setStrmQuery(c, q)
		controllers.Get115UrlByPickCode(c)
		return true
	case strings.Contains(u.Path, "/pan123/url"):
		logs.Success("STRM 自家直链 URL: 123 fileId=%s", q.Get("pickcode"))
		setStrmQuery(c, q)
		controllers.GetPan123UrlByPickCode(c)
		return true
	case strings.Contains(u.Path, "/guangyapan/url"):
		logs.Success("STRM 自家直链 URL: 光鸭 fileId=%s", q.Get("pickcode"))
		setStrmQuery(c, q)
		controllers.GetGuangYaPanUrlByPickCode(c)
		return true
	case strings.Contains(u.Path, "/baidupan/url"):
		logs.Success("STRM 自家直链 URL: 百度 fsId=%s", q.Get("pickcode"))
		setStrmQuery(c, q)
		controllers.GetBaiduPanUrlByPickCode(c)
		return true
	case strings.Contains(u.Path, "/pan139/url"):
		logs.Success("STRM 自家直链 URL: 139 fileId=%s", q.Get("pickcode"))
		setStrmQuery(c, q)
		controllers.GetPan139UrlByFileId(c)
		return true
	}
	return false
}

// parseStrmPointer 识别 tgto123 风格的 STRM 指针
func parseStrmPointer(content string) (kind, payload string, ok bool) {
	m := strmPointerPattern.FindStringSubmatch(content)
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimLeft(content[len(m[0]):], "/:? "), true
}

// redirectStrmPointer 处理 tgto123 风格 STRM 指针
func redirectStrmPointer(c *gin.Context, kind, payload string) bool {
	// 分离 query 参数, 如 play115://pickcode?xxx=1
	var q url.Values
	if idx := strings.IndexByte(payload, '?'); idx >= 0 {
		q, _ = url.ParseQuery(payload[idx+1:])
		payload = payload[:idx]
	}
	payload = strings.TrimSpace(payload)

	switch kind {
	case "play115":
		if payload == "" {
			return false
		}
		logs.Success("STRM 指针 play115: pickcode=%s", payload)
		vals := url.Values{"pickcode": {payload}, "force": {"1"}}
		if q != nil {
			for k, vs := range q {
				vals[k] = vs
			}
		}
		setStrmQuery(c, vals)
		controllers.Get115UrlByPickCode(c)
		return true
	case "playgy":
		if payload == "" {
			return false
		}
		logs.Success("STRM 指针 playgy: fileId=%s", payload)
		setStrmQuery(c, url.Values{"pickcode": {payload}})
		controllers.GetGuangYaPanUrlByPickCode(c)
		return true
	case "play123":
		if payload == "" {
			return false
		}
		logs.Success("STRM 指针 play123: fileId=%s", payload)
		setStrmQuery(c, url.Values{"pickcode": {payload}})
		controllers.GetPan123UrlByPickCode(c)
		return true
	case "play115share":
		logs.Warn("STRM 指针 play115share 暂不支持 (115 分享直链), 回源处理: %s", payload)
		return false
	}
	return false
}

// setStrmQuery 将 STRM 解析出的参数合并到当前请求上
func setStrmQuery(c *gin.Context, vals url.Values) {
	cur := c.Request.URL.Query()
	for k, vs := range vals {
		for _, v := range vs {
			cur.Set(k, v)
		}
	}
	c.Request.URL.RawQuery = cur.Encode()
	// 清空已解析的表单缓存, 确保 ShouldBind 能读到新参数
	c.Request.Form = nil
}
