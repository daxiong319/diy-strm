package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"litepan/internal/discover/discovery"
	"litepan/internal/discover/dmodels"
)

// 豆瓣海报防盗链代理。
//
// img*.doubanio.com 对无 Referer 的请求一律回 418（实测），而浏览器加载 <img>
// 时无法伪造 Referer（web/index.html 还显式设了 referrer: no-referrer），所以
// 豆瓣来源的海报在页面上恒为占位图。这里由服务端带上豆瓣域 Referer 抓图转发，
// 前端只请求本站 /api/admin/discovery/cover 即可。
//
// TMDB 图床（image.tmdb.org / mo_tmdb_image_host 指向域）同样纳入：大陆网络下
// 浏览器直连常加载不出，改由服务端抓图转发。
//
// 安全约束：只允许白名单内的豆瓣图片主机（+ /view/photo/ 路径）与 TMDB 图床主机，
// 避免变成任意 URL 的开放代理（SSRF）。

const (
	doubanCoverReferer = "https://movie.douban.com/"
	// doubanCoverProxyPath 必须与真实挂载路径一致：路由是
	// r.Route("/api") → r.Route("/admin") → r.Route("/discovery") 三层嵌套
	// （internal/api/router.go:219/:275/:357），所以少了 /admin 会 404。
	doubanCoverProxyPath = "/api/admin/discovery/cover"
	doubanCoverMaxBytes  = 12 << 20 // 12 MiB
)

// doubanCoverAllowedHost 判断主机是否属于允许代理的豆瓣图床
func doubanCoverAllowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	switch host {
	case "doubanio.com", "douban.com":
		return true
	}
	return strings.HasSuffix(host, ".doubanio.com") || strings.HasSuffix(host, ".douban.com")
}

// tmdbCoverAllowedHost 判断主机是否属于允许代理的 TMDB 图床
// （官方 image.tmdb.org，或 mo_tmdb_image_host 当前指向的自建反代域）
func tmdbCoverAllowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "image.tmdb.org" {
		return true
	}
	if base := strings.TrimSpace(dmodels.GlobalScrapeSettings.GetTmdbImageUrl()); base != "" {
		if u, err := url.Parse(base); err == nil && strings.EqualFold(u.Hostname(), host) {
			return true
		}
	}
	return false
}

// DoubanCoverProxyURL 把豆瓣图片 URL 改写为本站代理地址（非豆瓣图直接返回原值）。
// 前端 Item/详情里的海报字段统一走这个改写，保证封面可加载。
func DoubanCoverProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return raw
	}
	if !doubanCoverAllowedHost(u.Hostname()) {
		return raw
	}
	if !strings.HasPrefix(u.Path, "/view/photo/") {
		return raw
	}
	return doubanCoverProxyPath + "?u=" + url.QueryEscape(raw)
}

// discoveryCoverProxyURL 本包内出口改写入口：豆瓣（防盗链）与 TMDB 图床
// （大陆直连常不可达）统一改写为本站代理地址，其它地址原样保留。
func discoveryCoverProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return raw
	}
	switch {
	case tmdbCoverAllowedHost(u.Hostname()):
		return doubanCoverProxyPath + "?u=" + url.QueryEscape(raw)
	case doubanCoverAllowedHost(u.Hostname()) && strings.HasPrefix(u.Path, "/view/photo/"):
		return DoubanCoverProxyURL(raw)
	default:
		return raw
	}
}

// rewriteDiscoveryCovers 递归改写发现接口响应里的图片字段（poster/backdrop/
// 演员 profile/still），把豆瓣图床地址换成本站代理地址；非豆瓣地址原样保留。
func rewriteDiscoveryCovers(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok && isDiscoveryImageField(k) {
				t[k] = discoveryCoverProxyURL(s)
				continue
			}
			rewriteDiscoveryCovers(val)
		}
	case []any:
		for _, item := range t {
			rewriteDiscoveryCovers(item)
		}
	case []map[string]any:
		for _, item := range t {
			rewriteDiscoveryCovers(item)
		}
	case *discovery.PageResult:
		if t != nil {
			for i := range t.Items {
				t.Items[i].Poster = discoveryCoverProxyURL(t.Items[i].Poster)
			}
		}
	case []discovery.Item:
		for i := range t {
			t[i].Poster = discoveryCoverProxyURL(t[i].Poster)
		}
	case discovery.Item:
		// 值拷贝，仅用于兜底（调用方需自行取回）
		t.Poster = discoveryCoverProxyURL(t.Poster)
	}
}

// isDiscoveryImageField 判断 map 键是否为需要走封面代理的图片字段
func isDiscoveryImageField(key string) bool {
	switch key {
	case "poster", "backdrop", "profile", "still":
		return true
	}
	return false
}

// writeDiscoveryOK 返回发现接口响应，并把豆瓣海报改写为本站代理地址。
// 避免让前端关心防盗链：所有发现板块的海报字段统一在出口处归一。
func writeDiscoveryOK(w http.ResponseWriter, data any) {
	rewriteDiscoveryCovers(data)
	writeOK(w, data)
}

// discoveryCoverProxy GET /api/admin/discovery/cover?u=<douban image url>
func (h *Handler) discoveryCoverProxy(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("u"))
	if raw == "" {
		http.Error(w, "缺少图片地址", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		http.Error(w, "非法图片地址", http.StatusBadRequest)
		return
	}
	isDouban := doubanCoverAllowedHost(u.Hostname()) && strings.HasPrefix(u.Path, "/view/photo/")
	isTMDB := tmdbCoverAllowedHost(u.Hostname())
	if !isDouban && !isTMDB {
		http.Error(w, "不允许代理的图片地址", http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if isDouban {
		// 豆瓣图床要求豆瓣域 Referer，否则 418；TMDB 图床无此要求
		req.Header.Set("Referer", doubanCoverReferer)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1")
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")

	resp, err := coverProxyClient.Do(req)
	if err != nil {
		http.Error(w, "抓取海报失败："+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("图床返回 %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" || !strings.HasPrefix(ct, "image/") {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	_, _ = io.Copy(w, io.LimitReader(resp.Body, doubanCoverMaxBytes))
}

// coverProxyClient 海报代理专用客户端（不做重定向跟随到站外）
var coverProxyClient = &http.Client{
	Timeout: 25 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("重定向次数过多")
		}
		if !doubanCoverAllowedHost(req.URL.Hostname()) && !tmdbCoverAllowedHost(req.URL.Hostname()) {
			return fmt.Errorf("重定向到非白名单图床：%s", req.URL.Host)
		}
		return nil
	},
}
