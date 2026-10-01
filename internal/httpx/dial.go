package httpx

import (
	"context"
	"errors"
	"net"
	"time"
)

// preferIPv4FallbackDelay 是 IPv4 优先拨号时留给 IPv4 的抢跑时间，之后并发尝试 IPv6。
// 取值与 Go 标准库 happy eyeballs 的默认错峰时间一致。
const preferIPv4FallbackDelay = 300 * time.Millisecond

// ipResolver 解析主机名对应的地址列表，测试中可替换。
type ipResolver func(ctx context.Context, host string) ([]net.IPAddr, error)

func defaultIPResolver(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// PreferIPv4Dialer 返回一个优先使用 IPv4 的 DialContext。
//
// 背景：部分主机（尤其是境外 VPS / 容器）没有可用的 IPv6 出口，而目标域名
// （如 drive.quark.cn、yun.139.com）同时存在 AAAA 记录。Go 标准库按 RFC 6724
// 优先 IPv6，当 IPv6 连接被黑洞时既不会快速失败、也不会回退到已解析出的 A 记录，
// 最终表现为 "context deadline exceeded (Client.Timeout exceeded while awaiting
// headers)" 或 "net/http: TLS handshake timeout"，被误判成网盘服务异常。
//
// 这里显式让 IPv4 抢跑 preferIPv4FallbackDelay，IPv6 稍后并发补位：先成功者胜出；
// 若 IPv4 全部失败，仍会继续尝试 IPv6，因此在真正只有 IPv6 的网络里依旧可用。
func PreferIPv4Dialer(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return newPreferIPv4Dialer(dialer, defaultIPResolver)
}

func newPreferIPv4Dialer(dialer *net.Dialer, lookup ipResolver) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if dialer == nil {
		dialer = &net.Dialer{Timeout: DefaultTimeout, KeepAlive: 30 * time.Second}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// 仅处理按主机名解析的 TCP；其余网络类型与 IP 字面量交回标准库。
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return dialer.DialContext(ctx, network, addr)
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil || net.ParseIP(host) != nil {
			return dialer.DialContext(ctx, network, addr)
		}
		resolved, err := lookup(ctx, host)
		if err != nil || len(resolved) == 0 {
			// 解析失败时退回标准库，让它给出原本的错误与重试语义。
			return dialer.DialContext(ctx, network, addr)
		}
		v4 := make([]string, 0, len(resolved))
		v6 := make([]string, 0, len(resolved))
		for _, item := range resolved {
			if item.IP == nil {
				continue
			}
			if ip4 := item.IP.To4(); ip4 != nil {
				v4 = append(v4, net.JoinHostPort(ip4.String(), port))
			} else {
				v6 = append(v6, net.JoinHostPort(item.IP.String(), port))
			}
		}
		switch {
		case len(v4) == 0 && len(v6) == 0:
			return dialer.DialContext(ctx, network, addr)
		case len(v4) == 0:
			return dialSequence(ctx, dialer, network, v6)
		case len(v6) == 0:
			return dialSequence(ctx, dialer, network, v4)
		}
		return dialHappyEyeballs(ctx, dialer, network, v4, v6)
	}
}

// dialSequence 依次尝试同一族内的地址，返回第一个成功连接；全失败时返回最后一次错误。
func dialSequence(ctx context.Context, dialer *net.Dialer, network string, addrs []string) (net.Conn, error) {
	var lastErr error
	for _, target := range addrs {
		conn, err := dialer.DialContext(ctx, network, target)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
	}
	if lastErr == nil {
		lastErr = errors.New("httpx: 没有可用的目标地址")
	}
	return nil, lastErr
}

// dialHappyEyeballs 让 IPv4 抢跑，随后并发 IPv6，先成功者胜出。
// 胜出后立即取消落败一方，并关闭其可能已建立的连接，避免拖慢返回或泄漏连接。
func dialHappyEyeballs(ctx context.Context, dialer *net.Dialer, network string, v4, v6 []string) (net.Conn, error) {
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		conn net.Conn
		err  error
	}
	// 缓冲区按两族各一条结果收敛，保证落败 goroutine 始终能写出结果后退出。
	results := make(chan outcome, 2)

	launched := 0
	v6Launched := false
	received := 0
	launch := func(addrs []string) {
		launched++
		go func() {
			conn, err := dialSequence(raceCtx, dialer, network, addrs)
			results <- outcome{conn: conn, err: err}
		}()
	}
	launch(v4)

	timer := time.NewTimer(preferIPv4FallbackDelay)
	defer timer.Stop()

	var winner net.Conn
	var lastErr error
	// 循环到「IPv4 尝试已收敛且 IPv6 已启动（或无需启动）」为止。
	for received < launched || (winner == nil && !v6Launched && ctx.Err() == nil) {
		select {
		case res := <-results:
			received++
			if res.conn != nil {
				if winner == nil {
					winner = res.conn
					cancel() // 落败一方尽快退出，不再拖慢返回。
				} else {
					res.conn.Close()
				}
			} else if res.err != nil {
				lastErr = res.err
			}
			// IPv4 先失败时立刻补位 IPv6，不必干等满回退延迟。
			if winner == nil && !v6Launched && received >= launched {
				v6Launched = true
				launch(v6)
			}
		case <-timer.C:
			if !v6Launched {
				v6Launched = true
				launch(v6)
			}
		}
	}

	// 排空在途结果：关闭多余连接（如落败一方已握手成功）。
	for received < launched {
		res := <-results
		received++
		if res.conn != nil {
			res.conn.Close()
		}
	}
	if winner != nil {
		return winner, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if lastErr == nil {
		lastErr = errors.New("httpx: 没有可用的目标地址")
	}
	return nil, lastErr
}
