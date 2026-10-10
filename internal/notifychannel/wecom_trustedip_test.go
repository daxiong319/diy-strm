package notifychannel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// 企微的错误全在 body 的 errcode 上，HTTP 状态码几乎总是 200。
// 原来 httpPostJSON 丢弃 body，于是 60020 被当成成功 —— 用户看到「通知已发送」，
// 实际一条也没到，而且没有任何地方会报错。
func TestHTTPPostJSONSurfacesWeComErrcodeHiddenInA200Response(t *testing.T) {
	srv, restore := stubWeComHTTP(t, `{"errcode":60020,"errmsg":"not allow to access from your ip"}`)
	defer restore()

	err := httpPostJSON(context.Background(), httpClient, srv+"/cgi-bin/message/send", map[string]any{"a": 1})
	if err == nil {
		t.Fatalf("HTTP 200 + errcode=60020 必须返回错误，否则 60020 永远不会被触发自维护")
	}
	var we *WeComAPIError
	if !errors.As(err, &we) {
		t.Fatalf("应返回 *WeComAPIError（自维护要靠 errcode 字段判断，不能靠文案匹配）: %T %v", err, err)
	}
	if we.ErrCode != 60020 {
		t.Fatalf("errcode 应为 60020, got %d", we.ErrCode)
	}
	if !strings.Contains(we.Error(), "not allow to access from your ip") {
		t.Fatalf("错误信息应保留企微原文案: %v", we)
	}
}

func TestHTTPPostJSONAcceptsNormalWeComSuccess(t *testing.T) {
	srv, restore := stubWeComHTTP(t, `{"errcode":0,"errmsg":"ok","msgid":"MSGID"}`)
	defer restore()

	if err := httpPostJSON(context.Background(), httpClient, srv+"/cgi-bin/message/send", map[string]any{}); err != nil {
		t.Fatalf("errcode=0 时不该报错: %v", err)
	}
}

// 非 JSON 响应（某些网关会返回 HTML 错误页）不能被当成业务错误。
func TestHTTPPostJSONIgnoresNonJSONBody(t *testing.T) {
	srv, restore := stubWeComHTTP(t, `<html>gateway timeout</html>`)
	defer restore()

	if err := httpPostJSON(context.Background(), httpClient, srv+"/cgi-bin/message/send", map[string]any{}); err != nil {
		t.Fatalf("非 JSON 响应不该被当成企微业务错误: %v", err)
	}
}

func TestHTTPPostJSONStillReportsHTTPStatusErrors(t *testing.T) {
	srv, restore := stubWeComHTTPStatus(t, 502, `<html>bad gateway</html>`)
	defer restore()

	err := httpPostJSON(context.Background(), httpClient, srv+"/cgi-bin/message/send", map[string]any{})
	if err == nil {
		t.Fatalf("HTTP 5xx 必须报错")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("错误信息应含状态码: %v", err)
	}
}

func TestWeComAPIErrorObserverIsOptionalAndPanicSafe(t *testing.T) {
	// 没注入回调时不该 panic（默认装配之外的行为与 T24 之前一致）。
	previous := WeComAPIErrorObserver
	SetWeComAPIErrorObserver(nil)
	defer SetWeComAPIErrorObserver(previous)

	srv, restore := stubWeComHTTP(t, `{"errcode":60020,"errmsg":"not allow to access from your ip"}`)
	defer restore()
	_ = srv
	reportWeComAPIError(context.Background(), "message/send", &WeComAPIError{ErrCode: 60020})

	// 注入一个会 panic 的回调：可选功能不该把一条通知的发送流程带崩。
	SetWeComAPIErrorObserver(func(ctx context.Context, op string, err error) {
		panic("observer exploded")
	})
	reportWeComAPIError(context.Background(), "message/send", &WeComAPIError{ErrCode: 60020})
}

// sendWecomApp 的 token 阶段也必须上报 60020 —— 否则用户先看到一次 token 失败才等到修复。
func TestWeComAppReports60020FromTokenStageToo(t *testing.T) {
	srv, restore := stubWeComHTTP(t, `{"errcode":60020,"errmsg":"not allow to access from your ip"}`)
	defer restore()
	previousHost := weComAPIBaseHostForTest
	weComAPIBaseHostForTest = srv
	defer func() { weComAPIBaseHostForTest = previousHost }()

	var mu sync.Mutex
	var ops []string
	SetWeComAPIErrorObserver(func(ctx context.Context, op string, err error) {
		mu.Lock()
		ops = append(ops, op)
		mu.Unlock()
	})
	defer SetWeComAPIErrorObserver(nil)

	err := sendWecomApp(context.Background(), map[string]string{
		"corp_id":     "corp1",
		"corp_secret": "secret",
		"agent_id":    "1000002",
	}, Message{Title: "t", Content: "c"})
	if err == nil {
		t.Fatalf("gettoken 返回 60020 时 sendWecomApp 必须报错")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ops) == 0 || ops[0] != "gettoken" {
		t.Fatalf("gettoken 阶段的 60020 必须上报（它是最早抛 60020 的地方）: %v", ops)
	}
}

// ---------- helpers ----------

func stubWeComHTTP(t *testing.T, body string) (string, func()) {
	t.Helper()
	return stubWeComHTTPStatus(t, 200, body)
}

func stubWeComHTTPStatus(t *testing.T, status int, body string) (string, func()) {
	t.Helper()
	srv := newLocalServer(t, status, body)
	previous := httpClient
	httpClient = srv.client
	return srv.url, func() { httpClient = previous }
}
