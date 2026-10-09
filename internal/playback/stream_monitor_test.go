package playback

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"litepan/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// T11 三态判定：302 直连 vs 流代理，两条分支各有用例。
//
// 本任务最容易做错的地方：PickAction 分「302 重定向」与「流代理」。
//
//	302    → 网盘直链 → 客户端自己去拉 → 字节流**不经过**自己的服务器
//	         → CDN 直连 → 上行计 0
//	流代理 → 字节流经自己的服务器吐出去 → 计费中
//
// 所以监控必须挂在**流代理分支**上。挂到 api handler 层（而不是
// ServeHTTP 里的这一段）会把 CDN 直连也计上行，等于凭空记账。
//
// 这些用例走 serveResolved —— 生产代码里真实分流的那一段。
// 刻意不在测试里重抄一遍分支：两份实现会让生产改了而测试还绿。
// ─────────────────────────────────────────────────────────────────────────────

// recordingStreamMonitor 记录回调，并把「计费数字」也算出来，
// 好让用例直接断言 302 分支确实计 0 —— 只断言「回调发生过」
// 抓不住「回调里顺手累加了流量」这种错。
type recordingStreamMonitor struct {
	streamOpens   []domain.StreamEvent
	redirectOpens []domain.StreamEvent
	stops         []string
	// billedBytes 计费闸门：只有真正包了 writer 的流代理分支才算产生上行。
	billedBytes int64
	// bitrateBps 每条流代理会话的估算码率，模拟监控器侧的反推结果。
	bitrateBps    int64
	streamCalls   int
	redirectCalls int
}

func (m *recordingStreamMonitor) OnStreamOpen(_ *http.Request, ev domain.StreamEvent) http.ResponseWriter {
	m.streamCalls++
	m.streamOpens = append(m.streamOpens, ev)
	// 真实监控器在这里包 countingResponseWriter 并在 Write 里累加；
	// 这里只记「本次请求会产生上传」，具体字节由用例给。
	m.billedBytes += m.bitrateBps / 8
	return httptest.NewRecorder()
}

func (m *recordingStreamMonitor) OnStreamStop(id string) {
	m.stops = append(m.stops, id)
}

func (m *recordingStreamMonitor) OnRedirectOpen(_ *http.Request, ev domain.StreamEvent) {
	m.redirectCalls++
	m.redirectOpens = append(m.redirectOpens, ev)
}

func newMonitoredService(m *recordingStreamMonitor) *Service {
	s := NewService(nil, nil)
	s.SetStreamMonitor(m)
	return s
}

func externalReq(t *testing.T) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/strm/1/Movie.MKV", nil)
	r.RemoteAddr = "203.0.113.9:41234" // TEST-NET-3，公网段
	return r
}

func redirectResolved() Resolved {
	return Resolved{
		Mode: domain.DownloadRedirect,
		Link: domain.DownloadInfo{URL: "https://direct.example.com/f/1?sign=x"},
		File: domain.FileItem{ID: "1", Name: "Movie.MKV"},
	}
}

func proxyResolved() Resolved {
	return Resolved{
		Mode: domain.DownloadProxy,
		Link: domain.DownloadInfo{URL: "https://upstream.example.com/f/1"},
		File: domain.FileItem{ID: "1", Name: "Movie.MKV", Size: 4},
	}
}

// TestServeResolvedRedirectBranchNeverBills CDN 直连：回调发生、流量为 0（验收 ②）。
//
// 断言「不记流量」而不是「没回调」—— 302 直连同样要出现在播放监控面板上
// （用户要看谁在拉直链），只是上行必须是 0。
func TestServeResolvedRedirectBranchNeverBills(t *testing.T) {
	m := &recordingStreamMonitor{bitrateBps: 8_000_000}
	s := newMonitoredService(m)
	w := httptest.NewRecorder()

	if err := s.serveResolved(w, externalReq(t), Request{AccountID: 7, FileID: "1"}, redirectResolved(), Intent{}); err != nil {
		t.Fatalf("serveResolved 返回错误：%v", err)
	}
	if m.redirectCalls != 1 {
		t.Fatalf("302 分支回调次数 = %d，期望 1", m.redirectCalls)
	}
	if m.streamCalls != 0 {
		t.Fatalf("302 分支不该触发流代理回调，实际触发 %d 次 —— 会把 CDN 直连记成计费中", m.streamCalls)
	}
	if m.billedBytes != 0 {
		t.Fatalf("302 直连计了 %d 字节上行，期望 0 —— 字节流没经过自己的服务器，不能计费", m.billedBytes)
	}
	if len(m.redirectOpens) != 1 {
		t.Fatalf("302 直连事件条数 = %d，期望 1（面板上要能看到谁在拉直链）", len(m.redirectOpens))
	}
	if got := m.redirectOpens[0].RequestType; got != "" {
		t.Fatalf("302 事件里 RequestType = %q，期望空 —— request_type 由监控器按回调种类自己填，playback 不碰它", got)
	}
	// playback 该负责的是「事件里带齐了判三态所需的字段」：
	// 客户端 IP（判局域网）、网盘直链（CDN 直连的凭据）。
	if m.redirectOpens[0].ClientIP != "203.0.113.9" {
		t.Fatalf("ClientIP = %q，期望 203.0.113.9（判局域网要靠它）", m.redirectOpens[0].ClientIP)
	}
	if m.redirectOpens[0].OriginalURL != "https://direct.example.com/f/1?sign=x" {
		t.Fatalf("OriginalURL = %q，期望网盘直链（这是 CDN 直连的凭据）", m.redirectOpens[0].OriginalURL)
	}
}

// TestServeResolvedStreamBranchBills 计费中：回调发生、流量非 0（验收 ①）。
//
// 上游 URL 是不可达的假地址：只要分流选对了流代理分支，
// serveStream 就会去取流并失败。**这里不关心它是否失败** ——
// 本用例只断言「分流选了流代理 + 监控收到 open + 产生了计费」。
func TestServeResolvedStreamBranchBills(t *testing.T) {
	m := &recordingStreamMonitor{bitrateBps: 8_000_000}
	s := newMonitoredService(m)
	w := httptest.NewRecorder()

	_ = s.serveResolved(w, externalReq(t), Request{AccountID: 7, FileID: "1"}, proxyResolved(), Intent{})

	if m.streamCalls != 1 {
		t.Fatalf("流代理分支回调次数 = %d，期望 1", m.streamCalls)
	}
	if m.redirectCalls != 0 {
		t.Fatalf("流代理分支不该触发 302 回调，实际触发 %d 次", m.redirectCalls)
	}
	if m.billedBytes <= 0 {
		t.Fatalf("流代理分支计了 %d 字节上行，期望 > 0 —— 字节流经过了己的服务器，应计费", m.billedBytes)
	}
	if got := m.streamOpens[0].RequestType; got != "" {
		t.Fatalf("流代理事件里 RequestType = %q，期望空 —— 由监控器填", got)
	}
	if m.streamOpens[0].ClientIP != "203.0.113.9" {
		t.Fatalf("ClientIP = %q，期望 203.0.113.9", m.streamOpens[0].ClientIP)
	}
	if m.streamOpens[0].ItemName != "Movie.MKV" {
		t.Fatalf("ItemName = %q，期望 Movie.MKV（会话去噪与报告都按条目归组）", m.streamOpens[0].ItemName)
	}
	if m.streamOpens[0].AccountID != 7 {
		t.Fatalf("AccountID = %d，期望 7（监控器要按账号解析存储驱动）", m.streamOpens[0].AccountID)
	}
}

// countingProbe 返回一个会真的计数的 writer，模拟 countingResponseWriter。
// 它同时记录 serveStream 拿到的是不是「不是原始 writer」。
type countingProbe struct {
	sink     *bytes.Buffer
	wrapper  http.ResponseWriter
	written  int64
	calls    int
	sawOther bool // serveStream 是否拿到了别的 writer
}

func (p *countingProbe) OnStreamOpen(_ *http.Request, _ domain.StreamEvent) http.ResponseWriter {
	p.calls++
	return p.wrapper
}

func (p *countingProbe) OnStreamStop(string) {}

func (p *countingProbe) OnRedirectOpen(_ *http.Request, _ domain.StreamEvent) {
	p.calls = -1000 // 标记「流代理分支误触发了 302 回调」
}

// plainCountingWriter 记字节数的裸 writer（模拟 countingResponseWriter 的计数部分）。
type plainCountingWriter struct {
	http.ResponseWriter
	n *int64
}

func (w *plainCountingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	*w.n += int64(n)
	return n, err
}

// TestServeResolvedStreamBranchPassesWrapperDown 流代理分支必须把包了计数器的
// writer 交给 serveStream —— 否则 countingResponseWriter 建的会话永远收不到字节。
//
// 这是接线错误最容易发生的地方：回调调了、writer 拿到了，
// 但忘了把 out 传下去，表面全绿、实际全程计 0。
//
// 验证方式：让 monitor 返回一个「写字节会计数」的 writer，
// 再给 res.Link.LocalPath 指向一个真实临时文件，让 serveStream 走
// serveLocalFile 分支真的写出字节 —— 有字节才谈得上「传下去了」。
func TestServeResolvedStreamBranchPassesWrapperDown(t *testing.T) {
	tmp := writeTempMedia(t, "stream-monitor-wrapper")
	probe := &countingProbe{sink: &bytes.Buffer{}}
	probe.wrapper = &plainCountingWriter{ResponseWriter: httptest.NewRecorder(), n: &probe.written}

	s := NewService(nil, nil)
	s.SetStreamMonitor(probe)

	res := Resolved{
		Mode: domain.DownloadProxy,
		Link: domain.DownloadInfo{LocalPath: tmp, Size: int64(len("hello litepan"))},
		File: domain.FileItem{ID: "1", Name: "Movie.MKV", Size: int64(len("hello litepan"))},
	}
	r := externalReq(t)
	original := httptest.NewRecorder()
	if err := s.serveResolved(original, r, Request{AccountID: 7, FileID: "1"}, res, Intent{}); err != nil {
		t.Fatalf("serveResolved 返回错误：%v", err)
	}
	if probe.calls != 1 {
		t.Fatalf("OnStreamOpen 调用次数 = %d（负值表示误触发 302 回调），期望 1", probe.calls)
	}
	if probe.written == 0 {
		t.Fatal("serveStream 写出 0 字节经过计数器 —— 包了计数器的 writer 没往下传，真实监控会全程计 0")
	}
	if original.Body.Len() > 0 {
		t.Fatalf("原始 writer 收到了 %d 字节，期望 0（字节应全部经过计数 wrapper）", original.Body.Len())
	}
}

// TestNilMonitorKeepsOriginalWriter 监控未注入（nil）时必须原样用传入的 w。
//
// 拿 nil 去写会 panic，而这个分支恰好是「插件没启用」这条最常见的路径。
func TestNilMonitorKeepsOriginalWriter(t *testing.T) {
	s := NewService(nil, nil) // 不注入监控器
	if cw := s.wrapStreamMonitor(externalReq(t), domain.StreamEvent{}); cw != nil {
		t.Fatalf("未注入监控器时 wrapStreamMonitor 返回了非 nil writer：%v", cw)
	}

	// 真跑一次分流：本地文件分支应把字节原样写进传入的 writer。
	tmp := writeTempMedia(t, "stream-monitor-nil")
	res := Resolved{
		Mode: domain.DownloadProxy,
		Link: domain.DownloadInfo{LocalPath: tmp, Size: int64(len("hello litepan"))},
		File: domain.FileItem{ID: "1", Name: "Movie.MKV", Size: int64(len("hello litepan"))},
	}
	w := httptest.NewRecorder()
	if err := s.serveResolved(w, externalReq(t), Request{AccountID: 7, FileID: "1"}, res, Intent{}); err != nil {
		t.Fatalf("serveResolved 返回错误：%v", err)
	}
	if w.Body.Len() == 0 {
		t.Fatal("未注入监控器时没有字节写入原始 writer —— nil 分支把响应吞了")
	}
}

// TestForceProxyStillMonitors Intent.ForceProxy 时 PickAction 一定选流代理，
// 即使账号模式是 redirect —— 此时确实经自己服务器吐流，必须计费。
func TestForceProxyStillMonitors(t *testing.T) {
	m := &recordingStreamMonitor{bitrateBps: 4_000_000}
	s := newMonitoredService(m)
	tmp := writeTempMedia(t, "stream-monitor-force")
	res := Resolved{
		Mode: domain.DownloadRedirect,
		Link: domain.DownloadInfo{URL: "https://direct.example.com/f/1", LocalPath: tmp},
		File: domain.FileItem{ID: "1", Name: "Movie.MKV", Size: int64(len("hello litepan"))},
	}
	_ = s.serveResolved(httptest.NewRecorder(), externalReq(t),
		Request{AccountID: 7, FileID: "1"}, res, Intent{ForceProxy: true})

	if m.streamCalls != 1 || m.redirectCalls != 0 {
		t.Fatalf("ForceProxy 下应走流代理分支（stream=%d redirect=%d）", m.streamCalls, m.redirectCalls)
	}
	if m.billedBytes <= 0 {
		t.Fatal("ForceProxy 经自己服务器吐流，应计费")
	}
}

// TestServeResolvedRejectsDirectory 目录校验仍在这段分流里。
func TestServeResolvedRejectsDirectory(t *testing.T) {
	m := &recordingStreamMonitor{}
	s := newMonitoredService(m)
	res := Resolved{
		Mode: domain.DownloadProxy,
		Link: domain.DownloadInfo{URL: "https://upstream.example.com/f/1"},
		File: domain.FileItem{ID: "1", Name: "Season 01", IsDir: true},
	}
	err := s.serveResolved(httptest.NewRecorder(), externalReq(t),
		Request{AccountID: 7, FileID: "1"}, res, Intent{})
	if err == nil {
		t.Fatal("目录请求没有报错，校验被搬漏了")
	}
	if m.streamCalls != 0 || m.redirectCalls != 0 {
		t.Fatalf("目录请求不该建会话（stream=%d redirect=%d）", m.streamCalls, m.redirectCalls)
	}
}

// writeTempMedia 写一个小的本地媒体文件，供 serveLocalFile 分支读。
func writeTempMedia(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("hello litepan"), 0o600); err != nil {
		t.Fatalf("写临时媒体文件失败：%v", err)
	}
	return p
}
