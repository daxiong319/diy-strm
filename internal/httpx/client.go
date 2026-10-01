package httpx

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"
)

const (
	DefaultTimeout         = 30 * time.Second
	defaultIdleConnTimeout = 90 * time.Second

	// envHTTPIPFamily 控制驱动出站请求的地址族选择。
	// 留空或 auto：IPv4 优先，IPv6 并发补位（默认）。
	// system：完全交回 Go 标准库的 happy eyeballs 行为。
	// ipv4 / ipv6：只使用该地址族。
	envHTTPIPFamily = "LITEPAN_HTTP_IP_FAMILY"
)

var (
	// readIPFamilyMode 读取地址族配置；测试中可替换。
	readIPFamilyMode = func() string {
		return strings.ToLower(strings.TrimSpace(os.Getenv(envHTTPIPFamily)))
	}
)

// httpIPFamily 返回当前生效的地址族策略。
func httpIPFamily() string {
	return readIPFamilyMode()
}

// defaultTransportDialContextPtr 记录 net/http.DefaultTransport 出厂自带的
// DialContext 指针，用于把「标准库默认拨号器」与「调用方刻意替换的拨号器」
// 区分开：克隆 DefaultTransport 后 DialContext 必然非空，因此不能用 nil 判断。
var defaultTransportDialContextPtr = func() uintptr {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok || tr == nil || tr.DialContext == nil {
		return 0
	}
	return reflect.ValueOf(tr.DialContext).Pointer()
}()

// hasCustomDialContext 判断 tr 的拨号器是否已被调用方替换为自定义实现。
// 例如集成测试会用 fake DialContext 把真实域名劫持到本机 httptest 服务，
// 这类拨号器必须原样保留，否则模拟测试会被地址族策略破坏。
func hasCustomDialContext(tr *http.Transport) bool {
	if tr.DialContext == nil || defaultTransportDialContextPtr == 0 {
		return false
	}
	return reflect.ValueOf(tr.DialContext).Pointer() != defaultTransportDialContextPtr
}

// ApplyDialStrategy 按配置为传输层装配地址族策略，供本包之外的
// 传输层构造点（如 WebDAV、扫码登录客户端）复用。
// 调用方已自定义 DialContext 的传输层会被跳过，见 hasCustomDialContext。
func ApplyDialStrategy(tr *http.Transport) {
	applyDialStrategy(tr)
}

func applyDialStrategy(tr *http.Transport) {
	if tr == nil || hasCustomDialContext(tr) {
		return
	}
	dialer := &net.Dialer{Timeout: DefaultTimeout, KeepAlive: 30 * time.Second}
	switch httpIPFamily() {
	case "system", "off", "default":
		return
	case "ipv4", "v4", "tcp4":
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if network == "tcp" {
				network = "tcp4"
			}
			return dialer.DialContext(ctx, network, addr)
		}
	case "ipv6", "v6", "tcp6":
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if network == "tcp" {
				network = "tcp6"
			}
			return dialer.DialContext(ctx, network, addr)
		}
	default:
		tr.DialContext = PreferIPv4Dialer(dialer)
	}
}

type ClientOptions struct {
	Timeout            time.Duration
	IdleConnTimeout    time.Duration
	DisableCompression bool
	DisableKeepAlives  bool
	Proxy              func(*http.Request) (*url.URL, error)
}

func NewClient(opts ClientOptions) *http.Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	idle := opts.IdleConnTimeout
	if idle <= 0 && !opts.DisableKeepAlives {
		idle = defaultIdleConnTimeout
	}
	if idle > 0 {
		tr.IdleConnTimeout = idle
	}
	if opts.DisableCompression {
		tr.DisableCompression = true
	}
	if opts.DisableKeepAlives {
		tr.DisableKeepAlives = true
		tr.MaxIdleConnsPerHost = 0
	}
	if opts.Proxy != nil {
		tr.Proxy = opts.Proxy
	}
	applyDialStrategy(tr)
	return &http.Client{Timeout: timeout, Transport: tr}
}

// NewStreamingClient 复用普通客户端的连接配置，但不限制整段文件传输时长。
func NewStreamingClient(base *http.Client, responseHeaderTimeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	derived := false
	if base != nil {
		if baseTransport, ok := base.Transport.(*http.Transport); ok {
			tr = baseTransport.Clone()
			derived = true
		}
	}
	if !derived {
		applyDialStrategy(tr)
	}
	if responseHeaderTimeout > 0 {
		tr.ResponseHeaderTimeout = responseHeaderTimeout
	}
	return &http.Client{Transport: tr}
}

func CloseClient(c *http.Client) {
	if c == nil {
		return
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		return
	}
	tr.CloseIdleConnections()
}
