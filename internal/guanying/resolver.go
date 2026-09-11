package guanying

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 防失联域名解析：观影官网地址会不定期更换，防失联页
// https://www.xn--ykq321c.com（观影.com）的 check.js 内嵌当前全部官方域名。
// 客户端启动/会话失效时从该页解析候选域名并探活，自动切换可用站点。
// ---------------------------------------------------------------------------

const (
	guanyingDirectoryPage = "https://www.xn--ykq321c.com/check.js?4.4"
	guanyingProbePath     = "/auth/login" // 探活路径：正常站点 200/405，PoW 挑战页含标记
	guanyingResolveTTL    = 6 * time.Hour
)

// fallbackBaseDomains 兜底域名（防失联页不可用时依次尝试）
var fallbackBaseDomains = []string{"https://guanying.app", "https://guanying.site"}

var (
	domainURLRe = regexp.MustCompile(`url:\s*'https?://([^']+)'`)
	challengeRe = regexp.MustCompile(`浏览器安全验证|browser_pow`)
)

type domainResolution struct {
	BaseURL   string    `json:"base_url"`
	Source    string    `json:"source"` // directory/fallback/static
	CheckedAt time.Time `json:"checked_at"`
	Candidates []string `json:"candidates"`
	Skipped   []string  `json:"skipped"` // PoW 挑战等不可直连的域名
}

var (
	resolveMu       sync.Mutex
	cachedRes       *domainResolution
	resolving       bool
	resolveWaitChan chan *domainResolution
)

// ResolveBaseURL 获取当前可用的观影站点地址（带缓存；force 重新解析）
func ResolveBaseURL(ctx context.Context, force bool) *domainResolution {
	resolveMu.Lock()
	if !force && cachedRes != nil && time.Since(cachedRes.CheckedAt) < guanyingResolveTTL {
		res := cachedRes
		resolveMu.Unlock()
		return res
	}
	if resolving {
		// 已有解析在跑，等它
		ch := resolveWaitChan
		resolveMu.Unlock()
		if ch != nil {
			select {
			case res := <-ch:
				return res
			case <-ctx.Done():
				return cachedRes
			case <-time.After(20 * time.Second):
				return cachedRes
			}
		}
		return cachedRes
	}
	resolving = true
	resolveWaitChan = make(chan *domainResolution, 1)
	resolveMu.Unlock()
	defer func() {
		resolveMu.Lock()
		resolving = false
		resolveMu.Unlock()
	}()

	res := resolveNow(ctx)
	resolveMu.Lock()
	if res != nil && res.BaseURL != "" {
		cachedRes = res
	}
	resolveMu.Unlock()
	resolveWaitChan <- res
	return res
}

// resolveNow 执行一次完整解析：防失联页 check.js → 候选域名探活 → 兜底
func resolveNow(ctx context.Context) *domainResolution {
	res := &domainResolution{CheckedAt: time.Now(), Source: "fallback"}
	candidates := []string{}

	// 1) 从防失联 check.js 解析域名清单
	client := &httpShortClient{timeout: 10 * time.Second}
	if raw, err := client.Get(ctx, guanyingDirectoryPage); err == nil {
		for _, m := range domainURLRe.FindAllStringSubmatch(string(raw), -1) {
			host := strings.TrimSpace(m[1])
			if host == "" {
				continue
			}
			candidates = append(candidates, "https://"+host)
		}
	}
	// 去重保序
	seen := map[string]bool{}
	uniq := candidates[:0]
	for _, c := range candidates {
		if !seen[strings.ToLower(c)] {
			seen[strings.ToLower(c)] = true
			uniq = append(uniq, c)
		}
	}
	candidates = uniq
	res.Candidates = candidates

	// 2) 逐个探活：/auth/login 需非挑战响应
	for _, base := range candidates {
		if err := ctx.Err(); err != nil {
			break
		}
		body, status, err := client.Probe(ctx, strings.TrimRight(base, "/")+guanyingProbePath)
		if err != nil || status == 0 {
			res.Skipped = append(res.Skipped, base+"（不可达）")
			continue
		}
		if challengeRe.MatchString(body) {
			res.Skipped = append(res.Skipped, base+"（浏览器验证墙）")
			continue
		}
		// 200/404/405 均视为站点可达（路径差异不代表站点不可用）
		res.BaseURL = strings.TrimRight(base, "/")
		res.Source = "directory"
		return res
	}

	// 3) 兜底域名
	for _, base := range fallbackBaseDomains {
		body, status, err := client.Probe(ctx, strings.TrimRight(base, "/")+guanyingProbePath)
		if err == nil && status > 0 && !challengeRe.MatchString(body) {
			res.BaseURL = strings.TrimRight(base, "/")
			res.Source = "fallback"
			return res
		}
		res.Skipped = append(res.Skipped, base+"（不可达/验证墙）")
	}
	sort.Strings(res.Skipped)
	return res
}

// ResolveBaseURLString 便捷封装（解析失败返回兜底 guanying.app）
func ResolveBaseURLString(ctx context.Context, force bool) string {
	res := ResolveBaseURL(ctx, force)
	if res != nil && res.BaseURL != "" {
		return res.BaseURL
	}
	return "https://guanying.app"
}

// InvalidateDomainCache 会话失效/DNS 失败时清除解析缓存，下次请求重新解析
func InvalidateDomainCache() {
	resolveMu.Lock()
	cachedRes = nil
	resolveMu.Unlock()
}

// ---------------------------------------------------------------------------
// 轻量 HTTP 探活客户端
// ---------------------------------------------------------------------------

type httpShortClient struct {
	timeout time.Duration
}

func (c *httpShortClient) client() *http.Client {
	return &http.Client{Timeout: c.timeout}
}

func (c *httpShortClient) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgentText)
	req.Header.Set("Referer", "https://www.xn--ykq321c.com/")
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// Probe 探活：返回 body/状态码；PoW 挑战页也能拿到 body 供识别
func (c *httpShortClient) Probe(ctx context.Context, url string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", userAgentText)
	resp, err := c.client().Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", resp.StatusCode, err
	}
	return string(body), resp.StatusCode, nil
}

// EnsureFreshBaseURL 每次业务请求前调用：base 为空或 ctx 超时则重新解析。
// base 为当前已解析地址（调用方持有）。
func EnsureFreshBaseURL(ctx context.Context, force bool) string {
	res := ResolveBaseURL(ctx, force)
	if res == nil || res.BaseURL == "" {
		return fallbackBaseDomains[0]
	}
	return res.BaseURL
}
