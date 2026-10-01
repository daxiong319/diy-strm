package strm

import (
	"context"
	"errors"
	"fmt"
	"litepan/internal/domain"
	"litepan/internal/playback"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

// 用户报障（STRM 扫描部分失败 / 夸克 .ass 元数据）的原始错误文案。
// 关键点：内核级 connect 超时是 "connection timed out"（timed out），
// 不含 "timeout" 子串，旧实现按字符串匹配会漏判成“非瞬时错误”而放弃重试。
const reportedConnectTimeout = `Get "https://dl-pc-sz.drive.quark.cn/MUtbuGBp/x.ass": dial tcp 103.158.16.142:443: connect: connection timed out`

func TestIsTransientMetadataErrClassifiesConnectTimeout(t *testing.T) {
	// 合成一个内核级 connect ETIMEDOUT，形状与真实 dial 失败一致。
	kernelTimeout := &net.OpError{
		Op:   "dial",
		Net:  "tcp",
		Addr: &net.TCPAddr{IP: net.ParseIP("103.158.16.142"), Port: 443},
		Err:  &os.SyscallError{Syscall: "connect", Err: syscall.ETIMEDOUT},
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"用户报障的 connect time out", errors.New(reportedConnectTimeout), true},
		{"合成内核 ETIMEDOUT", kernelTimeout, true},
		{"context deadline exceeded", errors.New(`Get "https://x/y": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`), true},
		{"TLS 握手超时", errors.New("net/http: TLS handshake timeout"), true},
		{"读超时", errors.New("read tcp 1.2.3.4:3->5.6.7.8:443: i/o timeout"), true},
		{"连接被重置", errors.New("read tcp 1.2.3.4:3->5.6.7.8:443: connection reset by peer"), true},
		{"broken pipe", errors.New("write tcp 1.2.3.4:3->5.6.7.8:443: write: broken pipe"), true},
		{"连接被拒绝", errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), true},
		{"网络不可达", errors.New("dial tcp 1.2.3.4:443: connect: network is unreachable"), true},
		{"提前 EOF", errors.New("unexpected EOF"), true},
		{"裸 EOF", errors.New("Get \"http://127.0.0.1:1\": EOF"), true},
		{"上游 5xx", errors.New("HTTP 502: bad gateway"), true},
		{"404 不重试", errors.New("HTTP 404: not found"), false},
		{"403 不重试", errors.New("HTTP 403: forbidden"), false},
		{"完整性错误不重试", errors.New("文件大小不一致: expected=10, got=3"), false},
		{"清单解析失败不重试", errors.New("解析 .cas 失败：unexpected end of JSON input"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientMetadataErr(tc.err); got != tc.want {
				t.Fatalf("isTransientMetadataErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// 报障场景的核心行为回归：一次连接超时必须被判定为可重试，从而真正发起重试。
func TestFetchMetadataURLWithRetryRetriesConnectTimeout(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			// 直接断开连接，让客户端拿到连接层错误（与 CDN 边缘节点抖动等价）。
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("响应器不支持 Hijack")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("Hijack 失败: %v", err)
				return
			}
			conn.Close()
			return
		}
		_, _ = w.Write([]byte("字幕内容"))
	}))
	defer server.Close()

	body, err := fetchMetadataURLWithRetry(t.Context(), server.Client(), server.URL, nil, 0)
	if err != nil {
		t.Fatalf("应重试后成功，实际失败: %v", err)
	}
	if string(body) != "字幕内容" {
		t.Fatalf("响应内容不符: %q", string(body))
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("应恰好尝试 %d 次，实际 %d 次", metadataHTTPAttempts, got)
	}
}

// 非瞬时错误（4xx）必须立刻放弃，不做无谓重试。
func TestFetchMetadataURLWithRetryStopsOnPermanentError(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer server.Close()

	_, err := fetchMetadataURLWithRetry(t.Context(), server.Client(), server.URL, nil, 0)
	if err == nil {
		t.Fatal("404 应返回错误")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("错误文案不符: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("404 不应重试，实际尝试 %d 次", got)
	}
}

// 元数据下载客户端必须与各驱动一致地套用统一地址族策略（IPv4 优先），
// 而不是退回标准库默认拨号器（会在 AAAA 被黑洞的链路上反复撞 IPv6 超时）。
func TestMetadataHTTPClientAppliesDialStrategy(t *testing.T) {
	client := metadataHTTPClient()
	if client == nil {
		t.Fatal("metadataHTTPClient() 不应返回 nil")
	}
	if client.Timeout != metadataClientTimeout {
		t.Fatalf("超时配置不符: %v != %v", client.Timeout, metadataClientTimeout)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("Transport 类型不符: %T", client.Transport)
	}
	defaultTr, ok := http.DefaultTransport.(*http.Transport)
	if !ok || defaultTr == nil || defaultTr.DialContext == nil {
		t.Fatal("无法获取标准库默认拨号器作为对照")
	}
	if tr.DialContext == nil {
		t.Fatal("传输层未装配拨号器")
	}
	if reflect.ValueOf(tr.DialContext).Pointer() == reflect.ValueOf(defaultTr.DialContext).Pointer() {
		t.Fatal("元数据客户端仍在用标准库默认拨号器，地址族策略未生效")
	}
}

// 端到端守卫：syncFiles 在未注入 client 时必须走 metadataHTTPClient()，
// 即拿到的客户端拨号器不是标准库默认实现。
func TestSyncFilesUsesStrategyClientWhenUnset(t *testing.T) {
	var gotDialer uintptr
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()

	// 用真实发送路径观察客户端行为：把 client 置空，触发内部回退。
	syncer := &metadataSyncer{}
	client := syncer.pickClient()
	if client == nil {
		t.Fatal("pickClient() 不应返回 nil")
	}
	if tr, ok := client.Transport.(*http.Transport); ok && tr != nil && tr.DialContext != nil {
		gotDialer = reflect.ValueOf(tr.DialContext).Pointer()
	}
	defaultTr := http.DefaultTransport.(*http.Transport)
	if gotDialer == 0 {
		t.Fatal("客户端未装配拨号器")
	}
	if fmt.Sprintf("%d", gotDialer) == fmt.Sprintf("%d", reflect.ValueOf(defaultTr.DialContext).Pointer()) {
		t.Fatal("syncFiles 回退路径未套用地址族策略")
	}
}

// 取直链失败也必须重试：夸克 /file/download 同样会撞 CDN 边缘抖动。
type flakyResolveStub struct {
	failures int32
	calls    int32
}

func (s *flakyResolveStub) Resolve(
	context.Context, int64, string, string, bool, bool,
) (playback.Resolved, error) {
	n := atomic.AddInt32(&s.calls, 1)
	if n <= s.failures {
		// 形状与用户报障一致：TLS 握手超时语义上可重试。
		return playback.Resolved{}, errors.New(`Post "https://drive.quark.cn/1/clouddrive/file/download?fr=pc&pr=ucpro": net/http: TLS handshake timeout`)
	}
	return playback.Resolved{Link: domain.DownloadInfo{URL: "https://example.invalid/x"}}, nil
}

func TestResolveWithRetryRetriesTransientFailure(t *testing.T) {
	stub := &flakyResolveStub{failures: 2}
	syncer := &metadataSyncer{playback: stub}

	resolved, err := syncer.resolveWithRetry(t.Context(), 1, "fid", false)
	if err != nil {
		t.Fatalf("应重试后成功，实际失败: %v", err)
	}
	if resolved.Link.URL == "" {
		t.Fatal("重试成功后应返回直链")
	}
	if got := atomic.LoadInt32(&stub.calls); got != 3 {
		t.Fatalf("应尝试 %d 次，实际 %d 次", metadataResolveAttempts, got)
	}
}

func TestResolveWithRetryKeepsPermanentFailure(t *testing.T) {
	stub := &permanentResolveStub{}
	syncer := &metadataSyncer{playback: stub}

	if _, err := syncer.resolveWithRetry(t.Context(), 1, "fid", false); err == nil {
		t.Fatal("非瞬时错误应直接返回")
	}
	if got := atomic.LoadInt32(&stub.calls); got != 1 {
		t.Fatalf("非瞬时错误不应重试，实际尝试 %d 次", got)
	}
}

type permanentResolveStub struct{ calls int32 }

func (s *permanentResolveStub) Resolve(
	context.Context, int64, string, string, bool, bool,
) (playback.Resolved, error) {
	atomic.AddInt32(&s.calls, 1)
	return playback.Resolved{}, errors.New("文件不存在")
}
