package httpx

import (
	"context"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"strings"
	"testing"
	"time"
)

// 真实网络取证：带 AAAA 记录且本机 IPv6 出口不通时，必须落到 IPv4。
func TestRealNetworkChoosesIPv4ForDualStackHost(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实网络取证")
	}
	// 真实外网探测需要显式开启，避免在无外网 / 无 DNS 的环境里误报。
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("LITEPAN_TEST_NETPROBE")), "1") {
		t.Skip("未设置 LITEPAN_TEST_NETPROBE=1，跳过真实网络取证")
	}
	c := NewClient(ClientOptions{Timeout: 20 * time.Second})
	const url = "https://drive.quark.cn/1/clouddrive/config?pr=ucpro&fr=pc"

	for i := 0; i < 6; i++ {
		var family string
		trace := &httptrace.ClientTrace{
			GotConn: func(ci httptrace.GotConnInfo) {
				if ci.Conn == nil {
					return
				}
				host, _, _ := net.SplitHostPort(ci.Conn.RemoteAddr().String())
				ip := net.ParseIP(host)
				if ip == nil {
					return
				}
				if ip.To4() != nil {
					family = "v4"
				} else {
					family = "v6"
				}
			},
		}
		ctx := httptrace.WithClientTrace(context.Background(), trace)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		resp, err := c.Do(req)
		elapsed := time.Since(start)
		if err != nil {
			t.Logf("#%d FAIL after %v: %v", i+1, elapsed.Round(time.Millisecond), err)
			continue
		}
		resp.Body.Close()
		t.Logf("#%d ok HTTP %d family=%s in %v", i+1, resp.StatusCode, family, elapsed.Round(time.Millisecond))
		if family == "v6" {
			t.Errorf("#%d 选中了 IPv6，说明优先策略未生效", i+1)
		}
	}
}
