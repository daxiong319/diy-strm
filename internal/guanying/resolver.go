package guanying

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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
			// PoW 验证墙：尝试求解 RSW 谜题后探活
			domain := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
			working, perr := ResolveWithPoW(ctx, domain)
			if perr == nil && working != "" {
				res.BaseURL = working
				res.Source = "directory+pow"
				return res
			}
			res.Skipped = append(res.Skipped, base+"（PoW 求解失败）")
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

// ---------------------------------------------------------------------------
// PoW (Proof of Work) 挑战求解：RSW 时间锁谜题 y = x^(2^t) mod N
// 官方域名（教父.com/星际穿越.com/hgeme.com 等）均需通过此验证才能访问
// ---------------------------------------------------------------------------

// ResolveWithPoW 对带 PoW 验证墙的域名执行完整解析：
// 1. GET /res/pow 获取挑战 {N,x,t}
// 2. 解算 y = x^(2^t) mod N
// 3. POST /res/pow 提交 {y} → 服务端设置验证 cookie
// 4. 探活 /auth/login 确认可用
func ResolveWithPoW(ctx context.Context, domain string) (string, error) {
	base := "https://" + domain
	client := &http.Client{
		Timeout: 30 * time.Second,
		Jar:     newCookieJar(),
	}

	// Step 1: 获取挑战
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/res/pow", nil)
	req.Header.Set("User-Agent", userAgentText)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取 PoW 挑战失败：%v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()

	var challenge struct {
		N string `json:"N"`
		X string `json:"x"`
		T int    `json:"t"`
	}
	if err := json.Unmarshal(body, &challenge); err != nil || challenge.N == "" {
		return "", fmt.Errorf("PoW 挑战解析失败（可能无验证）：body=%s", truncateForLog(string(body), 100))
	}

	// Step 2: RSW 解算 y = x^(2^t) mod N
	y, err := solveRSW(challenge.N, challenge.X, challenge.T)
	if err != nil {
		return "", fmt.Errorf("PoW 解算失败：%v", err)
	}

	// Step 3: 提交结果
	submitBody, _ := json.Marshal(map[string]string{"y": y})
	req2, _ := http.NewRequestWithContext(ctx, "POST", base+"/res/pow", bytes.NewReader(submitBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("User-Agent", userAgentText)
	resp2, err := client.Do(req2)
	if err != nil {
		return "", fmt.Errorf("提交 PoW 结果失败：%v", err)
	}
	resp2.Body.Close()

	// Step 4: 探活
	req3, _ := http.NewRequestWithContext(ctx, "GET", base+"/auth/login", nil)
	req3.Header.Set("User-Agent", userAgentText)
	resp3, err := client.Do(req3)
	if err != nil {
		return "", fmt.Errorf("探活失败：%v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode >= 200 && resp3.StatusCode < 400 {
		return base, nil
	}
	return "", fmt.Errorf("PoW 验证后仍不可访问（HTTP %d）", resp3.StatusCode)
}

// solveRSW 计算 RSW 时间锁 y = x^(2^t) mod N（连续平方 t 次）
func solveRSW(nHex, xHex string, t int) (string, error) {
	n, ok := new(big.Int).SetString(nHex, 16)
	if !ok {
		return "", fmt.Errorf("N 解析失败")
	}
	x, ok := new(big.Int).SetString(xHex, 16)
	if !ok {
		return "", fmt.Errorf("x 解析失败")
	}
	y := new(big.Int).Set(x)
	for i := 0; i < t; i++ {
		y.Mul(y, y)
		y.Mod(y, n)
	}
	return y.Text(16), nil
}

func newCookieJar() *cookieJarWrapper {
	jar, _ := cookiejar.New(nil)
	return &cookieJarWrapper{jar}
}

type cookieJarWrapper struct {
	jar *cookiejar.Jar
}

func (w *cookieJarWrapper) Cookies(u *url.URL) []*http.Cookie { return w.jar.Cookies(u) }
func (w *cookieJarWrapper) SetCookies(u *url.URL, cookies []*http.Cookie) {
	w.jar.SetCookies(u, cookies)
}

func truncateForLog(s string, n int) string {
	if len(s) <= n { return s }
	return s[:n] + "…"
}

var _ = rand.Read
var _ = url.Parse
