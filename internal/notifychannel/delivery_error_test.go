package notifychannel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/eventbus"
)

// 投递错误分类的用例。
//
// 这些断言的意义在于：4xx 与 5xx 在补发队列里必须是两种命运。
// 之前 sendWebhook 对两者都只返回 fmt.Errorf("webhook HTTP %d")，
// 一个地址打错一字的用户要等五档退避共约 15 小时才看到失败。

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		status int
		want   bool
		why    string
	}{
		{400, false, "地址写错，重发一样错"},
		{401, false, "密钥不对，重发一样 401"},
		{403, false, "被拒，重发没用"},
		{404, false, "路径不存在"},
		{408, true, "请求超时，语义就是稍后再来"},
		{429, true, "限流，明确要求稍后再来"},
		{500, true, "服务端内部错误，通常是临时的"},
		{502, true, "网关错误，通常是临时的"},
		{503, true, "服务不可用，通常会恢复"},
		{504, true, "网关超时"},
		{0, true, "网络层失败（连接被拒/TLS 错误/超时）"},
	}
	for _, c := range cases {
		if got := classifyStatus(c.status); got != c.want {
			t.Errorf("classifyStatus(%d)=%v want %v（%s）", c.status, got, c.want, c.why)
		}
	}
}

// TestSendWebhookSurfacesResponseBody 对方返回的错误描述必须能看到。
func TestSendWebhookSurfacesResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"missing X-Api-Key header"}`))
	}))
	defer srv.Close()

	err := sendWebhook(context.Background(), map[string]string{
		"url": srv.URL, "method": "POST",
	}, Message{Title: "t", Content: "c"})
	if err == nil {
		t.Fatal("webhook 返回 401 却算发送成功")
	}
	de, ok := AsDeliveryError(err)
	if !ok {
		t.Fatalf("错误不是 DeliveryError: %T %v", err, err)
	}
	if de.StatusCode != http.StatusUnauthorized {
		t.Errorf("状态码 = %d，期望 401", de.StatusCode)
	}
	if !strings.Contains(de.Body, "missing X-Api-Key header") {
		t.Errorf("响应体被丢掉了，Body=%q —— 用户无从判断该改哪", de.Body)
	}
	if de.Retryable {
		t.Error("401 被判成可重试 —— 会让用户白等五档退避")
	}
	if RetryableError(err) {
		t.Error("RetryableError(401) = true，期望 false")
	}
}

// TestSendWebhookServerErrorIsRetryable 5xx 应当进补发队列。
func TestSendWebhookServerErrorIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := sendWebhook(context.Background(), map[string]string{
		"url": srv.URL, "method": "POST",
	}, Message{Title: "t", Content: "c"})
	if err == nil {
		t.Fatal("webhook 返回 503 却算发送成功")
	}
	if !RetryableError(err) {
		t.Fatalf("503 被判成不可重试: %v", err)
	}
}

// TestSendWebhookSuccessDrainsBody 成功时也要把响应体读完，
// 否则 keep-alive 连接上会残留未读数据。
func TestSendWebhookSuccessDrainsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	if err := sendWebhook(context.Background(), map[string]string{
		"url": srv.URL, "method": "POST",
	}, Message{Title: "t", Content: "c"}); err != nil {
		t.Fatalf("期望成功，得到 %v", err)
	}
}

// TestNonRetryableWebhookFailureIsNotEnqueued 端到端：真实 webhook 返回 400
// 时通知不进补发队列。
func TestNonRetryableWebhookFailureIsNotEnqueued(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("malformed payload"))
	}))
	defer srv.Close()

	queue := newMemoryRetryRepo()
	ch := &domain.NotifyChannel{
		ID: 1, Type: ChannelWebhook, Name: "格式不兼容的接收端", Enabled: true,
		Config: `{"method":"POST","url":"` + srv.URL + `"}`,
	}
	publish := testBus(t, []*domain.NotifyChannel{ch}, queue)

	publish(eventbus.NotificationCreated{
		Level: "warning", Category: domain.NotificationCategoryStrmScanWarn,
		Title: "STRM 扫描部分失败", Message: "3 个任务失败",
	})

	// 先等「确实尝试过发送」——用队列始终为空判断不了（事件可能还没跑到）。
	mustEventually(t, func() bool { return atomic.LoadInt32(&hits) > 0 }, "webhook 压根没被调用")
	time.Sleep(50 * time.Millisecond)
	if entries := queue.snapshot(); len(entries) != 0 {
		t.Fatalf("400 失败进了补发队列 %d 条 —— 这种失败重发也不会成功", len(entries))
	}
}

// TestRetryableWebhookFailureIsEnqueued 与上一条互为对照：503 必须进队列。
func TestRetryableWebhookFailureIsEnqueued(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	queue := newMemoryRetryRepo()
	ch := &domain.NotifyChannel{
		ID: 1, Type: ChannelWebhook, Name: "临时挂掉的接收端", Enabled: true,
		Config: `{"method":"POST","url":"` + srv.URL + `"}`,
	}
	publish := testBus(t, []*domain.NotifyChannel{ch}, queue)
	publish(eventbus.NotificationCreated{
		Level: "warning", Category: domain.NotificationCategoryStrmScanWarn,
		Title: "STRM 扫描部分失败", Message: "3 个任务失败",
	})

	mustEventually(t, func() bool { return len(queue.snapshot()) == 1 },
		"502 失败没有进入补发队列 —— 临时故障正是补发该救的场景")
}

// TestMissingRequiredFieldIsNotRetryable 配置缺失同样不补发。
func TestMissingRequiredFieldIsNotRetryable(t *testing.T) {
	err := Send(context.Background(), ChannelWebhook, map[string]string{"method": "POST"}, Message{Title: "t"})
	if err == nil {
		t.Fatal("缺 url 却发送成功")
	}
	if RetryableError(err) {
		t.Fatalf("缺必填字段被判成可重试: %v —— 补发用的是同一份快照，永远缺", err)
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("错误里没点名缺哪个字段: %v", err)
	}
}

// TestUnknownChannelIsNotRetryable 未知渠道重发还是未知渠道。
func TestUnknownChannelIsNotRetryable(t *testing.T) {
	err := Send(context.Background(), "no_such_channel", map[string]string{}, Message{Title: "t"})
	if err == nil {
		t.Fatal("未知渠道却发送成功")
	}
	if RetryableError(err) {
		t.Errorf("未知渠道被判成可重试: %v", err)
	}
}

// TestDeliveryErrorBodyIsTruncated 超长响应体不该把整条记录撑爆。
func TestDeliveryErrorBodyIsTruncated(t *testing.T) {
	long := strings.Repeat("x", 5000)
	de := &DeliveryError{ChannelType: ChannelWebhook, StatusCode: 500, Body: long, Retryable: true}
	msg := de.Error()
	if len(msg) > 400 {
		t.Fatalf("错误信息长度 %d，期望截断到 400 以内", len(msg))
	}
	if !strings.Contains(msg, "重试不会成功") && !strings.Contains(msg, "HTTP 500") {
		t.Errorf("错误信息缺少状态码: %s", msg)
	}
}

// TestNilErrorIsNotRetryable 防御：nil 不能被判成可重试。
func TestNilErrorIsNotRetryable(t *testing.T) {
	if RetryableError(nil) {
		t.Error("nil 被判成可重试")
	}
}
