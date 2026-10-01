package casintake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 回归背景：用户看到「23:14 ⚠️ WARNING 系统 CAS 接收：拉取更新失败」大量重复，
// 根因有三：① 客户端超时（65s）只是略大于服务端长轮询（50s），网络抖动就会
// "Client.Timeout exceeded while awaiting headers"；② handler 完全忽略 HTTP
// 状态码，把连接问题与上游业务错误混为一谈；③ 任何错误都按 errorRetryInterval
// 无退避重打一条 WARN。以下测试分别锁死这三点。

func TestPollClientTimeoutExceedsServerLongPoll(t *testing.T) {
	// 客户端超时必须显著大于服务端长轮询，否则响应头没回来就先超时。
	if pollClientTimeoutSeconds <= pollTimeoutSeconds {
		t.Fatalf("客户端超时 %ds 必须大于服务端长轮询 %ds", pollClientTimeoutSeconds, pollTimeoutSeconds)
	}
	if margin := pollClientTimeoutSeconds - pollTimeoutSeconds; margin < 15 {
		t.Fatalf("客户端超时余量仅 %ds，网络抖动会误判为超时，建议 ≥15s", margin)
	}
}

func TestClassifyPollError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want pollFailureKind
	}{
		{"客户端超时算瞬时故障",
			errors.New(`Get "https://api.telegram.org/bot123/getUpdates?timeout=50": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`),
			pollFailureTransient},
		{"context.DeadlineExceeded 算瞬时故障", context.DeadlineExceeded, pollFailureTransient},
		{"连接被拒算瞬时故障", syscall.ECONNREFUSED, pollFailureTransient},
		{"连接被拒（文案）算瞬时故障",
			errors.New(`dial tcp 127.0.0.1:12366: connect: connection refused`), pollFailureTransient},
		{"TLS 握手超时算瞬时故障",
			errors.New(`net/http: TLS handshake timeout`), pollFailureTransient},
		{"DNS 失败算瞬时故障",
			errors.New(`dial tcp: lookup api.telegram.org: no such host`), pollFailureTransient},
		{"409 冲突归类为 conflict",
			errors.New(`Telegram 返回错误（HTTP 409，error_code 409）: Conflict: terminated by other getUpdates request; make sure that only one bot instance is running`),
			pollFailureConflict},
		{"上游业务错误归类为 upstream",
			errors.New(`Telegram 返回错误（HTTP 400，error_code 400）: Bad Request: chat not found`),
			pollFailureUpstream},
		{"nil 不 panic", nil, pollFailureUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPollError(tc.err); got != tc.want {
				t.Fatalf("classifyPollError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestTransientNetworkIsNotConflict(t *testing.T) {
	// 客户端超时的报错文本里没有 conflict，绝不能被当成 bot 占用。
	err := errors.New(`context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)
	if isPollingConflict(err) {
		t.Fatal("客户端超时被误判为 bot 冲突：会错误提示用户停止其它实例")
	}
	if !isTransientNetworkError(err) {
		t.Fatal("客户端超时应被识别为瞬时网络故障")
	}
}

func TestFailureTrackerSuppressesRepeatedWarnings(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	track := newFailureTracker()
	err := errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")

	// 连续 40 次瞬时失败，只应在第一次打明细。
	for i := 0; i < 40; i++ {
		wait := track.observe(pollFailureTransient, err, 65*time.Second, 1, "https://api.telegram.org", log)
		if wait <= 0 {
			t.Fatalf("第 %d 次失败应返回正的重试间隔", i+1)
		}
	}
	lines := strings.Count(buf.String(), "拉取更新")
	if lines != 1 {
		t.Fatalf("40 次同类瞬时失败应只打 1 条明细，实际 %d 条：\n%s", lines, buf.String())
	}
	if !strings.Contains(buf.String(), "拉取更新超时/网络抖动") {
		t.Fatalf("日志应说明这是网络抖动而非上游故障：%s", buf.String())
	}
}

func TestFailureTrackerBacksOffAndCaps(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	track := newFailureTracker()
	err := errors.New("context deadline exceeded")

	first := track.observe(pollFailureTransient, err, time.Second, 1, "h", log)
	if first < errorRetryInterval {
		t.Fatalf("首次退避 %v 不应小于 %v", first, errorRetryInterval)
	}
	var last time.Duration
	for i := 0; i < 20; i++ {
		last = track.observe(pollFailureTransient, err, time.Second, 1, "h", log)
	}
	if last != transientMaxRetryInterval {
		t.Fatalf("持续失败后退避应封顶在 %v，实际 %v", transientMaxRetryInterval, last)
	}
}

func TestFailureTrackerLogsRecoveryOnlyWhenFailed(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 从未失败过：不应打出任何「恢复正常」日志。
	healthy := newFailureTracker()
	healthy.recovered(1, log)
	if buf.Len() != 0 {
		t.Fatalf("健康状态下不应输出恢复日志：%s", buf.String())
	}

	// 失败后恢复：应有一条收尾日志。
	buf.Reset()
	track := newFailureTracker()
	track.observe(pollFailureTransient, errors.New("i/o timeout"), time.Second, 1, "h", log)
	track.recovered(1, log)
	if !strings.Contains(buf.String(), "长轮询已恢复正常") {
		t.Fatalf("失败后恢复应打一条收尾日志：%s", buf.String())
	}
}

func TestFailureTrackerResetsWhenReasonChanges(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	track := newFailureTracker()
	track.observe(pollFailureTransient, errors.New("i/o timeout"), time.Second, 1, "h", log)
	before := buf.Len()
	// 故障原因变化必须立刻可见，不能被「已打过一条」抑制掉。
	track.observe(pollFailureUpstream, errors.New("Telegram 返回错误（HTTP 400，error_code 400）: chat not found"), time.Second, 1, "h", log)
	if buf.Len() == before {
		t.Fatal("失败原因变化时应立即打出新类别的日志")
	}
	if !strings.Contains(buf.String()[before:], "拉取更新失败") {
		t.Fatalf("应打出上游失败明细：%s", buf.String()[before:])
	}
}

// TestGetUpdatesHonorsHTTPErrorStatus 锁定状态码检查：非 JSON 的错误页
// 不能只报「解析响应失败」，要带上 HTTP 状态码。
func TestGetUpdatesHonorsHTTPErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	}))
	defer ts.Close()

	_, err := getUpdates(context.Background(), ts.Client(), ts.URL, "tok", 0)
	if err == nil {
		t.Fatal("HTTP 502 应返回错误")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("错误应包含 HTTP 状态码，实际：%v", err)
	}
}

// TestGetUpdatesReportsConflictWithErrorCode 锁定 409 冲突在措辞变化后仍可识别。
func TestGetUpdatesReportsConflictWithErrorCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"}`))
	}))
	defer ts.Close()

	_, err := getUpdates(context.Background(), ts.Client(), ts.URL, "tok", 0)
	if err == nil {
		t.Fatal("409 应返回错误")
	}
	if !isPollingConflict(err) {
		t.Fatalf("409 冲突应被 isPollingConflict 识别，实际错误：%v", err)
	}
}

// TestGetUpdatesSuccessPath 正常响应必须照常返回更新，且 offset 推进语义不变。
func TestGetUpdatesSuccessPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":7,"message":{"document":{"file_id":"f1","file_name":"a.cas"}}}]}`))
	}))
	defer ts.Close()

	updates, err := getUpdates(context.Background(), ts.Client(), ts.URL, "tok", 5)
	if err != nil {
		t.Fatalf("正常响应不应报错：%v", err)
	}
	if len(updates) != 1 || updates[0].UpdateID != 7 {
		t.Fatalf("应解析出 1 条 update_id=7，实际 %+v", updates)
	}
	if updates[0].Message == nil || updates[0].Message.Document == nil {
		t.Fatal("应解析出 document")
	}
}

// TestGetUpdatesErrorTextHasNoToken 日志与错误文本绝不能泄露 bot token。
func TestGetUpdatesErrorTextHasNoToken(t *testing.T) {
	const secret = "8475429746:AAF3e2XYwAFGojihKiF7y1pl8U-4L4GSxIo"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, "bad gateway")
	}))
	defer ts.Close()

	_, err := getUpdates(context.Background(), ts.Client(), ts.URL, secret, 0)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("错误文本泄露了 bot token：%v", err)
	}
}
