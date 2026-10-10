package wecom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- 测试脚手架 ----------

type stubLookup struct {
	ips []string
	err error
}

func (s stubLookup) Lookup(ctx context.Context) ([]string, error) {
	return s.ips, s.err
}

type capturingLogger struct {
	mu    sync.Mutex
	warns []string
	debug []string
}

func (l *capturingLogger) Debugf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.debug = append(l.debug, fmt.Sprintf(format, args...))
}

func (l *capturingLogger) Warnf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(format, args...))
}

func (l *capturingLogger) allLogs() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.warns, "\n") + "\n" + strings.Join(l.debug, "\n")
}

// corpServer 是模拟企微网关的服务端。
type corpServer struct {
	mu sync.Mutex

	trustedIPs []string
	setCalls   [][]string
	getCalls   int
	setCallsN  int

	// corpSecret 出现在 token 请求里，用于断言「secret 没被打进日志」
	corpSecret string
	tokenCalls int

	srv *httptest.Server
}

func newCorpServer(t *testing.T, trusted []string) *corpServer {
	t.Helper()
	cs := &corpServer{trustedIPs: trusted, corpSecret: "SUPER-SECRET-VALUE"}
	mux := http.NewServeMux()
	mux.HandleFunc("/cgi-bin/gettoken", func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		cs.tokenCalls++
		cs.mu.Unlock()
		secret := r.URL.Query().Get("corpsecret")
		if secret != cs.corpSecret {
			w.Write([]byte(`{"errcode":40013,"errmsg":"invalid corpidorsecret"}`))
			return
		}
		cs.mu.Lock()
		tk := fmt.Sprintf("tk-%d", cs.tokenCalls)
		cs.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"access_token": tk, "expires_in": 7200})
	})
	mux.HandleFunc("/cgi-bin/cgi/get_trust_ip", func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		cs.getCalls++
		snapshot := append([]string(nil), cs.trustedIPs...)
		cs.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "trusted_ip_list": snapshot})
	})
	mux.HandleFunc("/cgi-bin/cgi/set_trust_ip", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			List []string `json:"trusted_ip_list"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		cs.mu.Lock()
		cs.setCallsN++
		cs.setCalls = append(cs.setCalls, body.List)
		cs.trustedIPs = body.List
		cs.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok"})
	})
	cs.srv = httptest.NewServer(mux)
	t.Cleanup(cs.srv.Close)
	return cs
}

func (cs *corpServer) lastSet() []string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.setCalls) == 0 {
		return nil
	}
	return cs.setCalls[len(cs.setCalls)-1]
}

func newSvc(t *testing.T, cs *corpServer, cfg Config, lookup ExitIPLookup, log Logger) *TrustedIPService {
	t.Helper()
	c := NewClient("corp1", cs.corpSecret)
	c.Host = cs.srv.URL
	return NewTrustedIPService(c, cfg, lookup, log)
}

// ---------- 验收①：关闭时行为与现在完全一致且不报错 ----------

func TestDisabledTrustedIPIsCompleteNoOp(t *testing.T) {
	cs := newCorpServer(t, []string{"1.2.3.4"})
	log := &capturingLogger{}
	s := newSvc(t, cs, Config{Enabled: false, Auto: true}, stubLookup{ips: []string{"9.9.9.9"}}, log)

	ips, err := s.ListIPs(context.Background())
	if err != nil {
		t.Fatalf("关闭时 ListIPs 不该报错: %v", err)
	}
	if ips != nil {
		t.Fatalf("关闭时不该发出任何请求拿到列表, got %v", ips)
	}
	res, err := s.HandleNotAllowedIP(context.Background(), "调用 message/send 时被拒")
	if err != nil {
		t.Fatalf("关闭时 HandleNotAllowedIP 不该报错: %v", err)
	}
	if res.Fired {
		t.Fatalf("关闭时不该触发修复")
	}
	if err := s.SetIPs(context.Background(), []string{"9.9.9.9"}); err != nil {
		t.Fatalf("关闭时 SetIPs 不该报错: %v", err)
	}
	if cs.setCallsN != 0 || cs.getCalls != 0 {
		t.Fatalf("关闭时不该发出任何 HTTP 请求, get=%d set=%d", cs.getCalls, cs.setCallsN)
	}
	if strings.Contains(log.allLogs(), "9.9.9.9") {
		t.Fatalf("关闭时不该记录出口 IP: %s", log.allLogs())
	}
}

// 非 60020 的错误不该触发任何修复，也不该被替换成别的错。
func TestReportAPIErrorIgnoresNonTrustedIPErrors(t *testing.T) {
	cs := newCorpServer(t, []string{"1.2.3.4"})
	s := newSvc(t, cs, Config{Enabled: true, Auto: true}, stubLookup{ips: []string{"9.9.9.9"}}, &capturingLogger{})
	orig := &APIError{ErrCode: 40014, ErrMsg: "invalid access_token"}
	got := s.ReportAPIError(context.Background(), "message/send", orig)
	if got != orig {
		t.Fatalf("非 60020 的错误应原样返回, got %v", got)
	}
	if cs.setCallsN != 0 {
		t.Fatalf("非 60020 不该触发写回")
	}
}

func TestAutoOffOnlyWarnsAndNeverWrites(t *testing.T) {
	cs := newCorpServer(t, []string{"1.2.3.4"})
	log := &capturingLogger{}
	s := newSvc(t, cs, Config{Enabled: true, Auto: false}, stubLookup{ips: []string{"9.9.9.9"}}, log)
	res, err := s.HandleNotAllowedIP(context.Background(), "被拒")
	if err != nil {
		t.Fatalf("Auto=false 不该报错: %v", err)
	}
	if res.Fired || cs.setCallsN != 0 {
		t.Fatalf("Auto=false 不该写回")
	}
	if !strings.Contains(strings.Join(log.warns, "\n"), "自动维护已关闭") {
		t.Fatalf("Auto=false 应留下可操作的告警, logs=%v", log.warns)
	}
}

// ---------- 验收②：读取当前白名单并与用户条目合并 ----------

func TestRepairMergesWithUserEntriesInsteadOfOverwriting(t *testing.T) {
	cs := newCorpServer(t, []string{"1.2.3.4", "10.0.0.9"})
	s := newSvc(t, cs, Config{Enabled: true, Auto: true}, stubLookup{ips: []string{"9.9.9.9"}}, &capturingLogger{})

	res, err := s.HandleNotAllowedIP(context.Background(), "调用 message/send 时被拒")
	if err != nil {
		t.Fatalf("HandleNotAllowedIP: %v", err)
	}
	if !res.Fired {
		t.Fatalf("应触发修复")
	}
	got := cs.lastSet()
	for _, want := range []string{"1.2.3.4", "10.0.0.9", "9.9.9.9"} {
		if !contains(got, want) {
			t.Fatalf("写回列表丢了 %q: %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("写回列表不该有重复或多余项: %v", got)
	}
	if len(res.Added) != 1 || res.Added[0] != "9.9.9.9" {
		t.Fatalf("Added 应只含本次新增: %v", res.Added)
	}
}

func TestMergeIsSetUnionPreservingOrderAndDeduplicating(t *testing.T) {
	got := Merge([]string{" 1.2.3.4 ", "1.2.3.4", ""}, []string{"1.2.3.4", "5.6.7.8"})
	want := []string{"1.2.3.4", "5.6.7.8"}
	if len(got) != len(want) {
		t.Fatalf("Merge 结果 %v != %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Merge 结果 %v != %v（顺序应保留）", got, want)
		}
	}
}

func TestRepairDoesNotRewriteWhenExitIPAlreadyListed(t *testing.T) {
	cs := newCorpServer(t, []string{"9.9.9.9", "1.2.3.4"})
	s := newSvc(t, cs, Config{Enabled: true, Auto: true}, stubLookup{ips: []string{"9.9.9.9"}}, &capturingLogger{})
	res, err := s.HandleNotAllowedIP(context.Background(), "被拒")
	if err != nil {
		t.Fatalf("HandleNotAllowedIP: %v", err)
	}
	if res.Fired {
		t.Fatalf("出口已在列表里不该触发写回")
	}
	if cs.setCallsN != 0 {
		t.Fatalf("没有新增项时不该写回（整表覆盖语义下白写一次只是白白消耗配额）")
	}
}

// 查不到出口 IP 时必须放弃：整表覆盖语义下拿空列表写回等于清空用户白名单。
func TestRepairAbortsWhenExitIPLookupFails(t *testing.T) {
	cs := newCorpServer(t, []string{"1.2.3.4", "10.0.0.9"})
	log := &capturingLogger{}
	s := newSvc(t, cs, Config{Enabled: true, Auto: true}, stubLookup{err: context.DeadlineExceeded}, log)

	if _, err := s.HandleNotAllowedIP(context.Background(), "被拒"); err == nil {
		t.Fatalf("查不到出口 IP 时应报错而不是静默写回")
	}
	if cs.setCallsN != 0 {
		t.Fatalf("查不到出口 IP 时绝不能写回（会清空白名单）")
	}
}

func TestRepairAbortsWhenExitIPLookupReturnsNothing(t *testing.T) {
	cs := newCorpServer(t, []string{"1.2.3.4"})
	s := newSvc(t, cs, Config{Enabled: true, Auto: true}, stubLookup{ips: nil}, &capturingLogger{})
	if _, err := s.HandleNotAllowedIP(context.Background(), "被拒"); err == nil {
		t.Fatalf("空出口列表应报错")
	}
	if cs.setCallsN != 0 {
		t.Fatalf("空出口列表绝不能写回")
	}
}

// ---------- 验收③ + ④：60020 自动修复，日志不含 secret ----------

func TestReportAPIErrorRepairsOn60020AndLogsWithoutSecret(t *testing.T) {
	cs := newCorpServer(t, []string{"1.2.3.4"})
	log := &capturingLogger{}
	s := newSvc(t, cs, Config{Enabled: true, Auto: true}, stubLookup{ips: []string{"9.9.9.9"}}, log)

	err := s.ReportAPIError(context.Background(), "message/send", &APIError{
		ErrCode: ErrCodeNotAllowedIP,
		ErrMsg:  "not allow to access from your ip",
	})
	if err == nil {
		t.Fatalf("原始错误必须仍返回给调用方（调用方可能自己重试），不该被吞掉")
	}
	if !IsNotAllowedIP(err) {
		t.Fatalf("原错误应保留 60020 语义")
	}
	got := cs.lastSet()
	if !contains(got, "9.9.9.9") || !contains(got, "1.2.3.4") {
		t.Fatalf("60020 后应合并写回, got %v", got)
	}
	logs := log.allLogs()
	if !strings.Contains(logs, "1.2.3.4") || !strings.Contains(logs, "9.9.9.9") {
		t.Fatalf("日志应含变更前后的完整列表: %s", logs)
	}
	if !strings.Contains(logs, "变更前") || !strings.Contains(logs, "变更后") {
		t.Fatalf("日志应明确区分变更前后: %s", logs)
	}
	if strings.Contains(logs, cs.corpSecret) || strings.Contains(logs, "SUPER-SECRET-VALUE") {
		t.Fatalf("日志泄露了 secret: %s", logs)
	}
	if strings.Contains(logs, "tk") && strings.Contains(logs, "access_token") {
		t.Fatalf("日志不该带 access_token: %s", logs)
	}
}

func TestIsNotAllowedIPSeesThroughWrappedErrors(t *testing.T) {
	wrapped := &wrappedErr{inner: &APIError{ErrCode: ErrCodeNotAllowedIP, ErrMsg: "not allow to access from your ip"}}
	if !IsNotAllowedIP(wrapped) {
		t.Fatalf("包过一层的 60020 也该认出来（中间隔 fmt.Errorf %%w 是常态）")
	}
	if IsNotAllowedIP(&APIError{ErrCode: 40014}) {
		t.Fatalf("40014 不是 60020")
	}
	if IsNotAllowedIP(nil) {
		t.Fatalf("nil 不是 60020")
	}
}

// ---------- 其它 ----------

func TestAccessTokenIsCachedAndRefreshedBeforeExpiry(t *testing.T) {
	cs := newCorpServer(t, nil)
	c := NewClient("corp1", cs.corpSecret)
	c.Host = cs.srv.URL
	now := time.Now()
	c.Now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := c.AccessToken(ctx); err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	before := c.accessToken
	if _, err := c.AccessToken(ctx); err != nil {
		t.Fatalf("AccessToken 二次: %v", err)
	}
	if c.accessToken != before || cs.tokenCalls != 1 {
		t.Fatalf("未过期不该重新取 token, calls=%d", cs.tokenCalls)
	}
	// 推进到「距过期不足 5 分钟」——提前刷新是这个实现存在的理由。
	now = now.Add(2*time.Hour - 4*time.Minute)
	c2 := NewClient("corp1", cs.corpSecret)
	c2.Host = cs.srv.URL
	c2.Now = func() time.Time { return now }
	c2.mu.Lock()
	c2.accessToken = "tk-stale"
	c2.tokenExpiry = now.Add(3 * time.Minute)
	c2.mu.Unlock()
	if _, err := c2.AccessToken(ctx); err != nil {
		t.Fatalf("临近过期刷新: %v", err)
	}
	if c2.accessToken == "tk-stale" {
		t.Fatalf("距过期不足 5 分钟时应重新取 token（避免正好在到期那一刻请求）")
	}
}

func TestShouldRepairRespectsCooldown(t *testing.T) {
	cs := newCorpServer(t, nil)
	s := newSvc(t, cs, Config{Enabled: true, Auto: true}, stubLookup{}, &capturingLogger{})
	now := time.Now()
	s.Now = func() time.Time { return now }
	if !s.ShouldRepair("9.9.9.9") {
		t.Fatalf("首次应允许修复")
	}
	s.markRepaired("9.9.9.9")
	if s.ShouldRepair("9.9.9.9") {
		t.Fatalf("冷却期内不该重复修复（写回是整表覆盖，高频写会互相覆盖）")
	}
	now = now.Add(RepairCooldown + time.Second)
	if !s.ShouldRepair("9.9.9.9") {
		t.Fatalf("冷却过后应重新允许")
	}
	if !s.ShouldRepair("8.8.8.8") {
		t.Fatalf("冷却是按 IP 记的，不该牵连别的 IP")
	}
}

func TestNewClientWithoutCredentialsFailsLoudly(t *testing.T) {
	c := NewClient("", "")
	if _, err := c.AccessToken(context.Background()); err == nil {
		t.Fatalf("未配置凭证时该报错，而不是拿到一个空 token 去请求")
	}
}

func TestHTTPExitIPLookupRejectsNonIPPayloads(t *testing.T) {
	var l httpExitIPLookup
	l.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader("<html>rate limited</html>")),
			Header:     http.Header{},
		}, nil
	})}
	if _, err := l.Lookup(context.Background()); err == nil {
		t.Fatalf("返回 HTML 时该报错，不能把 HTML 当 IP 写进白名单")
	}
}

// ---------- helpers ----------

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type wrappedErr struct{ inner error }

func (w *wrappedErr) Error() string { return "外层: " + w.inner.Error() }
func (w *wrappedErr) Unwrap() error { return w.inner }

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestCredentialsAreReadLazilyOnEveryCall 钉住「凭证延迟读取」。
//
// 装配时抓一份快照的话，用户在管理台改完 corp_secret 之后，
// 自动修复会继续拿着旧的去调接口 —— 症状是「明明改了 Secret 却一直修不好」。
func TestCredentialsAreReadLazilyOnEveryCall(t *testing.T) {
	var calls int
	secret := "first"
	svc := NewTrustedIPService(nil, Config{Enabled: true, Auto: true}, nil, nil)
	svc.SetCredentials(func() (string, string, bool) {
		calls++
		return "corp", secret, true
	})
	if !svc.enabled() {
		t.Fatal("拿到凭证后应当可用")
	}
	secret = "second"
	if !svc.enabled() {
		t.Fatal("凭证变更后仍应可用")
	}
	if calls < 2 {
		t.Fatalf("凭证回调只被调了 %d 次，说明被缓存了", calls)
	}
}

func TestMissingCredentialsDisableServiceQuietly(t *testing.T) {
	svc := NewTrustedIPService(nil, Config{Enabled: true, Auto: true}, nil, nil)
	svc.SetCredentials(func() (string, string, bool) { return "", "", false })
	if svc.enabled() {
		t.Fatal("没有可用凭证时不应视为可用")
	}
	// 关键：调用方不会因此看到错误。
	if err := svc.ReportAPIError(context.Background(), "gettoken", &APIError{ErrCode: ErrCodeNotAllowedIP}); err == nil {
		t.Fatal("原始错误应原样返回")
	}
}
