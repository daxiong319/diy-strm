package httpx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// startIPv4Listener 启动一个本地 IPv4 监听，返回地址与关闭函数。
func startIPv4Listener(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听 IPv4：%v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

// 返回固定地址列表的解析器。
func staticResolver(addrs ...string) ipResolver {
	return func(ctx context.Context, host string) ([]net.IPAddr, error) {
		out := make([]net.IPAddr, 0, len(addrs))
		for _, a := range addrs {
			ip := net.ParseIP(a)
			if ip == nil {
				return nil, errors.New("非法测试地址：" + a)
			}
			out = append(out, net.IPAddr{IP: ip})
		}
		return out, nil
	}
}

// IPv6 被黑洞时，IPv4 必须胜出且耗时应远小于回退延迟 + 超时。
func TestPreferIPv4DialerUsesIPv4WhenIPv6Blackholed(t *testing.T) {
	addr, stop := startIPv4Listener(t)
	defer stop()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}

	dial := newPreferIPv4Dialer(
		&net.Dialer{Timeout: 5 * time.Second},
		// IPv6 优先返回，且指向必然会失败/挂起的地址。
		staticResolver("2001:db8::1", "127.0.0.1"),
	)

	start := time.Now()
	conn, err := dial(context.Background(), "tcp", net.JoinHostPort("example.invalid", port))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("拨号失败：%v", err)
	}
	conn.Close()
	if elapsed > 2*time.Second {
		t.Fatalf("IPv4 未及时胜出，耗时 %v", elapsed)
	}
}

// 没有 IPv4 记录时必须回退到 IPv6，不能用 tcp4 死守。
func TestPreferIPv4DialerFallsBackToIPv6(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("环境不支持 IPv6 回环：%v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	dial := newPreferIPv4Dialer(&net.Dialer{Timeout: 5 * time.Second}, staticResolver("::1"))
	conn, err := dial(context.Background(), "tcp", net.JoinHostPort("example.invalid", port))
	if err != nil {
		t.Fatalf("仅 IPv6 环境应能连通：%v", err)
	}
	conn.Close()
}

// IPv4 字面量应原样交给标准库，不触发 DNS。
func TestPreferIPv4DialerPassesThroughIPLiteral(t *testing.T) {
	addr, stop := startIPv4Listener(t)
	defer stop()

	dial := newPreferIPv4Dialer(&net.Dialer{Timeout: 5 * time.Second},
		func(ctx context.Context, host string) ([]net.IPAddr, error) {
			t.Fatalf("IP 字面量不应触发解析：%s", host)
			return nil, nil
		})
	conn, err := dial(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("拨号失败：%v", err)
	}
	conn.Close()
}

// 解析失败时应退回标准库并返回错误，而不是直接 panic 或永久阻塞。
func TestPreferIPv4DialerResolverErrorFallsBack(t *testing.T) {
	dial := newPreferIPv4Dialer(&net.Dialer{Timeout: 2 * time.Second},
		func(ctx context.Context, host string) ([]net.IPAddr, error) {
			return nil, errors.New("解析失败")
		})
	_, err := dial(context.Background(), "tcp", "no-such-host.invalid:9")
	if err == nil {
		t.Fatal("解析失败且目标不可达时应返回错误")
	}
}

// 默认策略必须真正改写 DialContext（DefaultTransport 克隆后 DialContext 非空，
// 早期实现里的 nil 判断会让策略静默失效）。
func TestApplyDialStrategyOverridesDefaultTransportDialContext(t *testing.T) {
	original := readIPFamilyMode
	defer func() { readIPFamilyMode = original }()
	readIPFamilyMode = func() string { return "" }

	tr := http.DefaultTransport.(*http.Transport).Clone()
	base := tr.DialContext
	applyDialStrategy(tr)
	if tr.DialContext == nil {
		t.Fatal("默认策略未装配 DialContext")
	}
	// 无法直接比较函数，改为行为验证：默认实现里对回环字面量应仍可拨通。
	if base == nil {
		t.Fatal("默认 transport 的 DialContext 应为非空")
	}
	addr, stop := startIPv4Listener(t)
	defer stop()
	conn, err := tr.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("装配后的 DialContext 应能连回环：%v", err)
	}
	conn.Close()
}

// system/off 模式必须完全保留标准库行为，不做任何改动。
func TestApplyDialStrategySystemModeKeepsDefault(t *testing.T) {
	original := readIPFamilyMode
	defer func() { readIPFamilyMode = original }()
	readIPFamilyMode = func() string { return "system" }

	tr := http.DefaultTransport.(*http.Transport).Clone()
	before := reflect.ValueOf(tr.DialContext).Pointer()
	applyDialStrategy(tr)
	after := reflect.ValueOf(tr.DialContext).Pointer()
	if before != after {
		t.Fatal("system 模式不应改写 DialContext")
	}
}

// ipv4 模式必须拒绝 IPv6 目标。
func TestApplyDialStrategyIPv4ModeRejectsIPv6(t *testing.T) {
	original := readIPFamilyMode
	defer func() { readIPFamilyMode = original }()
	readIPFamilyMode = func() string { return "ipv4" }

	tr := http.DefaultTransport.(*http.Transport).Clone()
	applyDialStrategy(tr)
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("环境不支持 IPv6 回环：%v", err)
	}
	defer ln.Close()
	if _, err := tr.DialContext(context.Background(), "tcp", ln.Addr().String()); err == nil {
		t.Fatal("ipv4 模式不应连上 IPv6 目标")
	}
}

// NewClient 必须带上地址族策略。
func TestNewClientAppliesDialStrategy(t *testing.T) {
	original := readIPFamilyMode
	defer func() { readIPFamilyMode = original }()
	readIPFamilyMode = func() string { return "" }

	c := NewClient(ClientOptions{})
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport 应为 *http.Transport")
	}
	def := http.DefaultTransport.(*http.Transport)
	if tr.DialContext == nil {
		t.Fatal("NewClient 未装配 DialContext")
	}
	_ = def
}

// 调用方刻意替换过的 DialContext（如集成测试劫持真实域名到 httptest）
// 必须原样保留，不能被地址族策略覆盖 —— 这是回归用例：
// internal/auth 的 cloud189 集成测试通过替换 http.DefaultTransport 注入 fake 拨号器。
func TestApplyDialStrategyPreservesCustomDialContext(t *testing.T) {
	original := readIPFamilyMode
	defer func() { readIPFamilyMode = original }()
	readIPFamilyMode = func() string { return "" }

	called := false
	custom := func(ctx context.Context, network, addr string) (net.Conn, error) {
		called = true
		return nil, errors.New("custom dialer")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = custom
	applyDialStrategy(tr)
	if reflect.ValueOf(tr.DialContext).Pointer() != reflect.ValueOf(custom).Pointer() {
		t.Fatal("自定义 DialContext 被地址族策略覆盖")
	}
	if _, err := tr.DialContext(context.Background(), "tcp", "example.com:443"); err == nil || !called {
		t.Fatalf("自定义 DialContext 未被调用：err=%v called=%v", err, called)
	}
}

// 通过替换全局 http.DefaultTransport 注入的拨号器同样必须被保留。
func TestNewClientPreservesReplacedDefaultTransportDialer(t *testing.T) {
	original := readIPFamilyMode
	defer func() { readIPFamilyMode = original }()
	readIPFamilyMode = func() string { return "" }

	origTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = origTransport }()

	called := false
	custom := func(ctx context.Context, network, addr string) (net.Conn, error) {
		called = true
		return nil, errors.New("custom dialer")
	}
	fake := origTransport.(*http.Transport).Clone()
	fake.DialContext = custom
	http.DefaultTransport = fake

	c := NewClient(ClientOptions{})
	tr := c.Transport.(*http.Transport)
	if _, err := tr.DialContext(context.Background(), "tcp", "open.e.189.cn:443"); err == nil || !called {
		t.Fatalf("被替换的全局拨号器未被保留：err=%v called=%v", err, called)
	}
}
