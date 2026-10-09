package playmonitor

import (
	"net"
	"net/http"
	"strings"
)

// 内网判定：只有「内网播放」才判局域网态，且不计费。
//
// 判定顺序与 参考实现 前端一致（PlayMonitorPanel 的 Y() 函数）：
// 先看 net 是否为 lan，为 lan 就直接是局域网，**origin 字段此时根本不看**；
// 非 lan 才按 origin（是否由自己服务器吐流）分出计费中 / CDN 直连。
// 这里的 classifyState 就是把这条顺序固化成服务端代码。

// isPrivateIP 判断单个 IP 是否落在私有/环回网段。
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	// 运营商级 NAT 常见的 100.64.0.0/10（RFC 6598）：不是 RFC1918，
	// 但内网拨回时走的就是这一段，漏掉会把内网播放误判成外网计费。
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
	}
	return false
}

// isLANHost 判断 host 是否内网主机。host 可以是裸 IP、带端口的 IP 或主机名。
func isLANHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if hostPort, _, err := net.SplitHostPort(host); err == nil {
		host = hostPort
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateIP(ip)
	}
	// 主机名不做 DNS 反查：取流入口上做一次解析会把首个播放请求的
	// 延迟全押在 DNS 上，而且可能把内网名解析到外网地址。
	// 拿不到 IP 就不算内网 —— 宁可漏判成外网，也不要误判成内网少计流量。
	return false
}

// clientIP 从请求里取客户端 IP：优先 X-Forwarded-For 首段，其次 X-Real-IP，
// 最后 RemoteAddr。
//
// 与 internal/adminauth/service.go:850 clientIP 同口径 —— 反代链路里
// RemoteAddr 是反代自己，必须走 Forwarded 头才拿得到真实客户端。
func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	addr := r.RemoteAddr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.TrimSpace(addr)
}

// requestIsLAN 判定一次取流请求是否来自内网。
//
// 只看客户端 IP，不看被访问的 Host：litepan 的 STRM 播放地址对外网
// 暴露的是同一个域名/端口，内网和外网走的是同一入口。
func requestIsLAN(r *http.Request) bool {
	return isLANHost(clientIP(r))
}
