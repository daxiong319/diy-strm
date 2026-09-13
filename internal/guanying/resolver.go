package guanying

import (
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

	"diy-strm/internal/helpers"
	"time"
)

// ---------------------------------------------------------------------------
// 防失联域名解析：观影官网地址会不定期更换，防失联页
// https://www.xn--ykq321c.com（观影.com）的 check.js 内嵌当前全部官方域名。
// 客户端启动/会话失效时从该页解析候选域名并探活，自动切换可用站点。
// ---------------------------------------------------------------------------

const (
	guanyingDirectoryPage = "https://www.xn--ykq321c.com/check.js?4.4"
	guanyingProbePath     = "/" // 探活路径：PoW 墙站点根路径返回挑战页，正常站点返回站点页
	guanyingResolveTTL    = 6 * time.Hour
)

// fallbackBaseDomains 兜底域名（防失联页不可用时依次尝试）
var fallbackBaseDomains = []string{"https://guanying.app", "https://guanying.site"}

var (
	domainURLRe = regexp.MustCompile(`url:\s*'https?://([^']+)'`)
	// powChallengeRe filejin PoW 挑战页标记（/auth/login 等业务路径在未验证时返回 404，
	// 只有根路径返回挑战页，因此必须以根路径探测）
	powChallengeRe = regexp.MustCompile(`浏览器安全验证|pow-scope|powSolve|filejin`)
	// parkedPageRe 域名停放页（guanying.app 跳 /lander）
	parkedPageRe = regexp.MustCompile(`"/lander"|window\.location\.href="/lander"`)
)

type domainResolution struct {
	BaseURL    string    `json:"base_url"`
	Source     string    `json:"source"` // directory/fallback/static/pow
	CheckedAt  time.Time `json:"checked_at"`
	Candidates []string  `json:"candidates"`
	Skipped    []string  `json:"skipped"`           // PoW 求解失败/停放等不可直连的域名
	Cookies    string    `json:"cookies,omitempty"` // PoW 验证通过后的 cookie（业务 jar 需注入）
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
	if res == nil || res.BaseURL == "" {
		helpers.AppLogger.Warnf("观影域名解析失败：候选=%v 跳过=%v（将回退兜底域名）", res.Candidates, res.Skipped)
	} else {
		helpers.AppLogger.Infof("观影域名解析：%s（%s，候选 %d 个）", res.BaseURL, res.Source, len(res.Candidates))
	}
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

	// 2) 逐个探活：根路径需非挑战响应（PoW 墙站点的业务路径在未验证时全部 404）
	for _, base := range candidates {
		if err := ctx.Err(); err != nil {
			break
		}
		helpers.AppLogger.Infof("观影探测候选：%s", base)
		body, status, err := client.Probe(ctx, strings.TrimRight(base, "/")+guanyingProbePath)
		if err != nil || status == 0 {
			res.Skipped = append(res.Skipped, base+"（不可达）")
			continue
		}
		if parkedPageRe.MatchString(body) {
			res.Skipped = append(res.Skipped, base+"（域名停放）")
			continue
		}
		if powChallengeRe.MatchString(body) {
			// PoW 验证墙：求解 RSW 谜题换取验证 cookie
			domain := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
			working, cookies, perr := ResolveWithPoW(ctx, domain)
			if perr == nil && working != "" {
				res.BaseURL = working
				res.Source = "directory+pow"
				res.Cookies = cookies
				return res
			}
			res.Skipped = append(res.Skipped, base+"（PoW 求解失败："+perr.Error()+"）")
			continue
		}
		// 200 且非挑战页 → 站点可用
		res.BaseURL = strings.TrimRight(base, "/")
		res.Source = "directory"
		return res
	}

	// 3) 兜底域名
	for _, base := range fallbackBaseDomains {
		body, status, err := client.Probe(ctx, strings.TrimRight(base, "/")+guanyingProbePath)
		if err == nil && status > 0 && !powChallengeRe.MatchString(body) && !parkedPageRe.MatchString(body) {
			res.BaseURL = strings.TrimRight(base, "/")
			res.Source = "fallback"
			return res
		}
		res.Skipped = append(res.Skipped, base+"（不可达/验证墙/停放）")
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
// 1. GET /res/pow 获取挑战 {N,x,t}（filejin powSolve 契约）
// 2. 解算 y = x^(2^t) mod N
// 3. POST /res/pow 提交 form 表单 y=<hex>（注意：上游只接受表单编码，JSON 会被静默拒绝）
// 4. 验证通过后返回站点地址与验证 cookie（业务请求需携带）
// 返回（baseURL, 序列化 cookie, error）。
func ResolveWithPoW(ctx context.Context, domain string) (string, string, error) {
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
		return "", "", fmt.Errorf("获取 PoW 挑战失败：%v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()

	var challenge struct {
		N string `json:"N"`
		X string `json:"x"`
		T int    `json:"t"`
	}
	if err := json.Unmarshal(body, &challenge); err != nil || challenge.N == "" {
		return "", "", fmt.Errorf("PoW 挑战解析失败（可能无验证）：body=%s", truncateForLog(string(body), 100))
	}

	// Step 2: RSW 解算 y = x^(2^t) mod N
	y, err := solveRSW(challenge.N, challenge.X, challenge.T)
	if err != nil {
		return "", "", fmt.Errorf("PoW 解算失败：%v", err)
	}

	// Step 3: 表单编码提交结果（对齐 powSolve submitResult：application/x-www-form-urlencoded）
	form := url.Values{"y": {y}}
	req2, _ := http.NewRequestWithContext(ctx, "POST", base+"/res/pow", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("User-Agent", userAgentText)
	resp2, err := client.Do(req2)
	if err != nil {
		return "", "", fmt.Errorf("提交 PoW 结果失败：%v", err)
	}
	body2, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
	resp2.Body.Close()
	var verify struct {
		Success bool `json:"success"`
	}
	_ = json.Unmarshal(body2, &verify)
	if !verify.Success {
		return "", "", fmt.Errorf("PoW 验证被拒绝（HTTP %d）：%s", resp2.StatusCode, truncateForLog(string(body2), 80))
	}

	// Step 4: 探活根路径（不再返回挑战页即通过）
	req3, _ := http.NewRequestWithContext(ctx, "GET", base+"/", nil)
	req3.Header.Set("User-Agent", userAgentText)
	resp3, err := client.Do(req3)
	if err != nil {
		return "", "", fmt.Errorf("探活失败：%v", err)
	}
	body3, _ := io.ReadAll(io.LimitReader(resp3.Body, 8192))
	resp3.Body.Close()
	if resp3.StatusCode >= 200 && resp3.StatusCode < 400 && !powChallengeRe.MatchString(string(body3)) {
		u, _ := url.Parse(base + "/")
		var pairs []map[string]string
		for _, ck := range client.Jar.Cookies(u) {
			pairs = append(pairs, map[string]string{"name": ck.Name, "value": ck.Value, "domain": ck.Domain, "path": ck.Path})
		}
		raw, _ := json.Marshal(pairs)
		return base, string(raw), nil
	}
	return "", "", fmt.Errorf("PoW 验证后仍不可访问（HTTP %d）", resp3.StatusCode)
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
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ = rand.Read
var _ = url.Parse
