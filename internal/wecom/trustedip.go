package wecom

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// TrustedIP 维护企业微信自建应用的可信 IP 白名单。
//
// ## 为什么是「报错驱动」而不是「主动探测出口 IP」
//
// 直觉上应该主动探测：我从本机访问一个「告诉我我是谁」的接口，拿到出口 IP，写进白名单。
// 问题是**探测只能看见你主动走的那条路**。企业机器上常见的是：多网卡、多出口 NAT、
// 容器网络、走代理的请求、IPv4/IPv6 双栈。这些情况下主动探测拿到的是**主出口**，
// 而企业微信判 60020 时用的可能是另一条 —— 于是自动修复「成功」了，接口还是被拒，
// 用户看到的是「自动维护了，但没用」。
//
// 报错驱动反过来：**企业微信说哪个 IP 不行，那个 IP 就是它真正在用的**。
// 代价是 60020 的 errmsg 里并不带 IP（文案只有 `not allow to access from your ip`），
// 所以仍然要一次外部查询 —— 但那次查询发生在**已经被证伪的那次请求之后**，
// 问的是「刚才那次是哪个出口」，命中的是真正出问题的那条路。
//
// 因此本包的策略是：**主动探测只作为兜底**，且探测到的 IP 一律合并而非覆盖。

// Logger 是本包需要的最小日志接口。
type Logger interface {
	Debugf(format string, args ...any)
	Warnf(format string, args ...any)
}

// Config 是可信 IP 的开关。
type Config struct {
	// Enabled 关掉时，本包所有方法都是彻底的 no-op —— 不报错、不发请求。
	//
	// 这条是验收 ①：可信 IP 功能关着时，行为必须与「这个功能不存在」完全一致。
	// 报错的话用户会以为企业微信通知坏了，而实际上只是他没开这个可选功能。
	Enabled bool
	// Auto 关闭时只读不写（仍可查询当前白名单用于展示），不自动改写。
	Auto bool
}

// ExitIPLookup 查当前出口 IP。
//
// 独立成接口是因为**主动探测拿不到真实出口**这件事本身就是要测的：
// 默认实现（HTTPQueryExitIP）走第三方服务，测试里换成假实现，
// 才能断言「探测失败不会把白名单清空」。
type ExitIPLookup interface {
	Lookup(ctx context.Context) ([]string, error)
}

// TrustedIPService 是可信 IP 的维护者。
type TrustedIPService struct {
	client *Client
	// credentials 延迟读取 corp_id / corp_secret；为空时用 client。
	credentials func() (corpID, secret string, ok bool)
	config      Config
	lookup      ExitIPLookup
	log         Logger

	mu sync.Mutex
	// cooldown 记录上次写回的时间。
	//
	// 没有它的话：一次网络抖动导致企微返回 60020，上游按自己的节奏重试若干次，
	// 每一次都触发一次「读白名单 → 合并 → 写回」。写回是**整表覆盖**语义，
	// 高频写回不仅浪费配额，还在并发写时互相覆盖。所以同一 IP 的重复修复要压住。
	cooldown map[string]time.Time

	// Now 可注入。
	Now func() time.Time
}

// RepairCooldown 是同一个出口 IP 的两次自动修复之间的最小间隔。
const RepairCooldown = 10 * time.Minute

// NewTrustedIPService 构造服务。依赖缺失仍然构造成功，所有方法退化为 no-op。
func NewTrustedIPService(client *Client, cfg Config, lookup ExitIPLookup, log Logger) *TrustedIPService {
	return &TrustedIPService{
		client:   client,
		config:   cfg,
		lookup:   lookup,
		log:      log,
		cooldown: map[string]time.Time{},
		Now:      time.Now,
	}
}

func (s *TrustedIPService) now() time.Time {
	if s == nil || s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// SetCredentials 注入「延迟读取凭证」的回调。
//
// 延迟读取而不是构造时抓快照：企微自建应用的 corp_secret 是可以在管理台改的，
// 装配时抓一份的话，用户改完 Secret 之后自动修复会继续拿着旧的去调接口，
// 症状是「明明改了 Secret，可信 IP 修复还是一直失败」。
func (s *TrustedIPService) SetCredentials(fn func() (corpID, secret string, ok bool)) {
	if s == nil {
		return
	}
	s.credentials = fn
}

// client 返回一个带当前凭证的客户端。
//
// 每次现取凭证并重建：access_token 缓存跟着凭证走，
// 凭证换了而 token 缓存还在，就会拿着旧 token 去调新应用的接口。
func (s *TrustedIPService) currentClient() *Client {
	if s == nil || s.credentials == nil {
		return s.client
	}
	corpID, secret, ok := s.credentials()
	if !ok {
		return nil
	}
	base := s.client
	out := NewClient(corpID, secret)
	if base != nil {
		out.Host = base.Host
		out.HTTP = base.HTTP
		if base.Now != nil {
			out.Now = base.Now
		}
	}
	return out
}

func (s *TrustedIPService) enabled() bool {
	c := s.currentClient()
	return s != nil && s.config.Enabled && c != nil && c.CorpID != "" && c.Secret != ""
}

func (s *TrustedIPService) debugf(format string, args ...any) {
	if s != nil && s.log != nil {
		s.log.Debugf(format, args...)
	}
}

func (s *TrustedIPService) warnf(format string, args ...any) {
	if s != nil && s.log != nil {
		s.log.Warnf(format, args...)
	}
}

// trustIPList 是 get_trust_ip 的响应。
type trustIPList struct {
	ErrCode       int      `json:"errcode"`
	ErrMsg        string   `json:"errmsg"`
	TrustedIPList []string `json:"trusted_ip_list"`
}

// ListIPs 读当前可信 IP 白名单。
//
// 功能关闭时返回 (nil, nil) 而不是报错 —— 见 Config.Enabled 的注释。
func (s *TrustedIPService) ListIPs(ctx context.Context) ([]string, error) {
	if !s.enabled() {
		return nil, nil
	}
	var out trustIPList
	if err := s.currentClient().callTokenized(ctx, "/cgi-bin/cgi/get_trust_ip", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return normalizeIPs(out.TrustedIPList), nil
}

// Merge 把候选 IP 并进现有列表。
//
// **必须是并集，绝不能是「候选列表」直接替换。** 用户会为了别的出口 IP
// （比如家里的宽带、备用网络）手动加过条目；直接覆盖等于替他把这些条目删掉，
// 而他完全不知情 —— 直到哪天换回那条网络才发现接口又报 60020。
func Merge(existing, candidates []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(existing)+len(candidates))
	for _, group := range [][]string{existing, candidates} {
		for _, ip := range normalizeIPs(group) {
			if !seen[ip] {
				seen[ip] = true
				out = append(out, ip)
			}
		}
	}
	return out
}

// SetIPs 整表写回。
func (s *TrustedIPService) SetIPs(ctx context.Context, ips []string) error {
	if !s.enabled() {
		return nil
	}
	if !s.config.Auto {
		s.debugf("可信 IP 自动维护已关闭，跳过写回")
		return nil
	}
	return s.currentClient().callTokenized(ctx, "/cgi-bin/cgi/set_trust_ip", map[string]any{
		"trusted_ip_list": normalizeIPs(ips),
	}, nil)
}

// RepairResult 报告一次自动修复做了什么。
type RepairResult struct {
	// Fired 表示这次调用真的触发了修复。
	Fired bool
	// Before / After 是写回前后的完整列表。
	Before []string
	After  []string
	// Added 是本次新加进去的条目。
	Added []string
	// Reason 说明为什么触发，便于在页面上向用户解释。
	Reason string
}

// HandleNotAllowedIP 是 60020 的唯一处理入口。
//
// 流程：读现有列表 → 查出口 IP → 合并 → 写回 → **记日志（含变更前后的完整列表）**。
//
// 关键在「读现有列表」这一步：它不是可选项。不读就写，等于用一次探测结果
// 覆盖掉用户手工维护的全部条目。
func (s *TrustedIPService) HandleNotAllowedIP(ctx context.Context, reason string) (RepairResult, error) {
	if !s.enabled() {
		return RepairResult{}, nil
	}
	if !s.config.Auto {
		s.warnf("企业微信报 60020（出口 IP 不在可信列表），但自动维护已关闭，需手动把出口 IP 加进白名单：%s", reason)
		return RepairResult{}, nil
	}
	before, err := s.ListIPs(ctx)
	if err != nil {
		return RepairResult{}, fmt.Errorf("读取可信 IP 失败: %w", err)
	}
	ips, err := s.lookup.Lookup(ctx)
	if err != nil {
		return RepairResult{}, fmt.Errorf("查询当前出口 IP 失败: %w", err)
	}
	ips = normalizeIPs(ips)
	if len(ips) == 0 {
		// 查不到出口时**必须放弃**，不能拿空列表去写回 ——
		// 整表覆盖语义下，那等于把用户的整份白名单清空。
		return RepairResult{}, fmt.Errorf("查询不到当前出口 IP，已放弃自动修复以免清空白名单")
	}
	after := Merge(before, ips)
	added := diff(after, before)
	if len(added) == 0 {
		s.warnf("企业微信报 60020，但当前出口 %s 已在可信列表里；可能是多出口，只补了探测到的那条", strings.Join(ips, ","))
		return RepairResult{Fired: false, Before: before, After: before, Reason: "出口 IP 已在白名单"}, nil
	}
	if err := s.SetIPs(ctx, after); err != nil {
		return RepairResult{}, fmt.Errorf("写回可信 IP 失败: %w", err)
	}
	// 日志里放**变更前后的完整列表**：排障时要回答的是「现在白名单里到底有什么」，
	// 而不是「刚才加了哪一条」。secret 绝不进日志（Client 也不打它）。
	s.warnf("企业微信可信 IP 自维护：%s；变更前=%v；变更后=%v；本次新增=%v",
		reason, before, after, added)
	return RepairResult{Fired: true, Before: before, After: after, Added: added, Reason: reason}, nil
}

// ReportAPIError 供上层在任意企微调用出错时调用一次。
//
// 非 60020 直接返回 nil：别的错误码（token 失效、参数错）跟可信 IP 无关，
// 顺手去改白名单只会掩盖真正的原因。
func (s *TrustedIPService) ReportAPIError(ctx context.Context, op string, err error) error {
	if err == nil || !IsNotAllowedIP(err) {
		return err
	}
	if !s.enabled() {
		return err
	}
	if _, rerr := s.HandleNotAllowedIP(ctx, fmt.Sprintf("调用 %s 时被拒", op)); rerr != nil {
		s.warnf("企业微信可信 IP 自动修复失败：%v", rerr)
	} else {
		s.debugf("企业微信可信 IP 已自动修复，%s 可以重试", op)
	}
	return err
}

// ShouldRepair 判断此刻是否允许为这个 IP 做修复（冷却）。
func (s *TrustedIPService) ShouldRepair(ip string) bool {
	if !s.enabled() || !s.config.Auto {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.cooldown[ip]
	return !ok || s.now().Sub(last) >= RepairCooldown
}

func (s *TrustedIPService) markRepaired(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cooldown[ip] = s.now()
}

// normalizeIPs 去空白、去重、保持顺序。
//
// 顺序保留而不是排序：写回的是一份「用户的清单」，
// 排序会让每次写回都产生一次全表变更，日志里的 diff 也就永远显示全部条目变了。
func normalizeIPs(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func diff(after, before []string) []string {
	seen := map[string]bool{}
	for _, v := range before {
		seen[v] = true
	}
	var out []string
	for _, v := range after {
		if !seen[v] {
			out = append(out, v)
		}
	}
	return out
}

// HTTPQueryExitIP 是默认的出口 IP 查询实现。
//
// 依次问几个服务直到有一个成功。**多问几家不是为了拿更准的答案**，
// 而是出口 IP 本身就可能不止一个（IPv4/IPv6、不同目标的路由不同），
// 全部收下交给 Merge 去重，比只取第一个安全。
var exitIPEndpoints = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
}

type httpExitIPLookup struct {
	client *http.Client
}

func (l httpExitIPLookup) Lookup(ctx context.Context) ([]string, error) {
	httpClient := l.client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 8 * time.Second}
	}
	var lastErr error
	var out []string
	for _, endpoint := range exitIPEndpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		buf := make([]byte, 128)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			continue
		}
		if ip := strings.TrimSpace(string(buf[:n])); net.ParseIP(ip) != nil {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("所有出口 IP 查询服务都没有返回可解析的地址")
	}
	sort.Strings(out)
	return out, nil
}

// NewHTTPExitIPLookup 返回默认的出口 IP 查询器。
func NewHTTPExitIPLookup() ExitIPLookup { return httpExitIPLookup{} }
