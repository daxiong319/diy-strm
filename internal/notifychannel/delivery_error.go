package notifychannel

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// 补发判定所需的错误分类。
//
// 为什么必须自己分类：sendWebhook 原本对所有失败只返回
// fmt.Errorf("webhook HTTP %d", statusCode)，把响应体整个丢掉。
// 这样一个 400（地址写错了，重发一万次也一样）与一个 503（服务端临时
// 挂了，重发就好了）在补发 worker 眼里长得一模一样——只能等满五档退避
// 共约 15 小时才标 failed，用户等 15 小时才发现自己少了一个 `=` 号。
// 分成可重试/不可重试后，不可重试的当场标 failed 立刻可见。

// DeliveryError 是渠道投递失败的带分类错误。
//
// Retryable=false 表示「再发一次也不会成功」（地址写错、必填字段缺失、
// 对方明确拒绝），补发 worker 直接把它标 failed 而不是继续退避。
type DeliveryError struct {
	ChannelType string
	StatusCode  int
	Body        string
	Err         error
	Retryable   bool
}

func (e *DeliveryError) Error() string {
	var b strings.Builder
	b.WriteString("渠道 ")
	b.WriteString(e.ChannelType)
	b.WriteString(" 投递失败")
	if e.StatusCode > 0 {
		fmt.Fprintf(&b, ": HTTP %d", e.StatusCode)
	}
	if body := strings.TrimSpace(e.Body); body != "" {
		if len(body) > 200 {
			body = body[:200] + "…"
		}
		fmt.Fprintf(&b, ": %s", body)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	if !e.Retryable {
		b.WriteString("（重试不会成功）")
	}
	return b.String()
}

func (e *DeliveryError) Unwrap() error { return e.Err }

// AsDeliveryError 从错误链里取出投递错误；不是投递错误时返回 false。
func AsDeliveryError(err error) (*DeliveryError, bool) {
	var de *DeliveryError
	if errors.As(err, &de) {
		return de, true
	}
	return nil, false
}

// RetryableError 判断这次失败值不值得进补发队列。
//
// 规则：
//   - DeliveryError.Retryable 为 false（4xx）⇒ 不进队列，直接判死；
//     进了队列也只是让用户多等五档退避才看到同一个 400。
//   - 其它错误（网络抖动、超时、上下文取消、未知渠道）⇒ 进队列。
func RetryableError(err error) bool {
	if err == nil {
		return false
	}
	if de, ok := AsDeliveryError(err); ok {
		return de.Retryable
	}
	return true
}

// newConfigError 是「配置不对，重试无望」的一类失败。
func newConfigError(channelType, format string, args ...any) error {
	return &DeliveryError{
		ChannelType: channelType,
		Err:         fmt.Errorf(format, args...),
		Retryable:   false,
	}
}

// classifyStatus 把 HTTP 状态码分成可重试与不可重试。
//
// 429（限流）与 408（请求超时）虽是 4xx，但语义就是「稍后再来」；
// 其余 4xx 是对方的明确拒绝，重发只会重复被拒。
func classifyStatus(status int) bool {
	switch {
	case status == http.StatusTooManyRequests, status == http.StatusRequestTimeout:
		return true
	case status >= 500:
		return true
	case status >= 400:
		return false
	default:
		return true
	}
}

// httpFailure 把一次非 2xx 响应包装成带分类的投递错误。
//
// 响应体按「对方确实给了有用信息才带」的原则取：很多 webhook 服务端
// 返回 200 + 一堆错误描述，不看就会把真实原因当成成功。
func httpFailure(channelType string, status int, body []byte, err error) error {
	return &DeliveryError{
		ChannelType: channelType,
		StatusCode:  status,
		Body:        strings.TrimSpace(string(body)),
		Err:         err,
		Retryable:   classifyStatus(status),
	}
}
