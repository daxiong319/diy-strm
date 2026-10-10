package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 小工具集合：解密之后解析、构造请求、读回包。
//
// 都是「无状态」的那种代码，单独成文件是为了 botservice.go 里只留业务。

// maxBodyBytes 是回包/入站的读取上限。
//
// 回调里带的是整条消息，理论上很小；但这个端点在公网上，
// 不设上限的话一个「POST 一个 2GB body」就能把内存打满。
const maxBodyBytes = 1 << 20

// decodeBody 把请求体解到一个结构里。
func decodeBody(r *http.Request, out any) error {
	raw, err := readAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// readAll 有上限地读完一个 reader。
func readAll(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(r, maxBodyBytes))
}

// readBody 读完一个 http 响应体。
func readBody(resp *http.Response) ([]byte, error) {
	return readAll(resp.Body)
}

// unmarshal 解一段 JSON。
func unmarshal(raw []byte, out any) error { return json.Unmarshal(raw, out) }

// newPostRequest 造一个 JSON POST 请求。
func newPostRequest(ctx context.Context, endpoint, body string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	return req, nil
}

// errLinkNotWired 是「转存能力没接上」时的固定说法。
//
// 明确报错而不是静默成功：Bot 收到链接回一句「已转存」、实际什么都没发生，
// 用户过十分钟才会发现，而那时候他大概已经忘了自己发过什么。
var errLinkNotWired = fmt.Errorf("转存能力尚未接入，请先在管理台配置网盘账号")

// truncateText 截断一段文本，用在日志和错误里。
//
// 回调密文解密出来可能很长，**整段塞进错误信息等于把用户消息打进日志**。
// 这里只留头 200 字符并显式标出被截了多少。
func truncateText(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	return string(r[:max]) + "…（共 " + itoa(len(r)) + " 字）"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
