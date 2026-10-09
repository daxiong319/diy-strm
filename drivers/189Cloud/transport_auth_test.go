package cloud189

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"litepan/internal/domain"
)

// TestIsSessionExpired 锁定 isSessionExpired 对三种错误的判定：
//  1. 纯字符串错误（含 InvalidSessionKey 标记）→ true；
//  2. AppError(CodeAuthExpired) → true；
//  3. AppError(CodeDriverError) 但文案含 InvalidSessionKey（上游 400 场景）→ 仍须 true，
//     这是「400 + InvalidSessionKey 未触发刷新重试」根因的回归用例。
func TestIsSessionExpired(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"无关错误", domain.Errf(domain.CodeDriverError), false},
		{"AppError 认证过期", domain.Errorf(domain.CodeAuthExpired, "天翼云盘认证会话已失效"), true},
		{
			"AppError 驱动错误但文案含 InvalidSessionKey",
			domain.Errorf(domain.CodeDriverError, `天翼云盘 API HTTP 400: {"errorCode":"InvalidSessionKey","errorMsg":"userSessionBO is null or fail to get sessionsecret by sessionkey","success":null}`),
			true,
		},
		{"裸字符串 InvalidSessionKey", errors.New("天翼云盘 API HTTP 400: InvalidSessionKey"), true},
		{"裸字符串 userSessionBO is null", errors.New("天翼云盘 API HTTP 400: userSessionBO is null"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSessionExpired(tc.err); got != tc.want {
				t.Fatalf("isSessionExpired(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRawJSONHTTP400InvalidSessionKeyIsAuthExpired 锁定 rawJSON 把「HTTP 400 + InvalidSessionKey」
// 归类为 CodeAuthExpired 而非 CodeDriverError，从而让 apiRequest 的刷新重试路径生效。
func TestRawJSONHTTP400InvalidSessionKeyIsAuthExpired(t *testing.T) {
	body := `{"errorCode":"InvalidSessionKey","errorMsg":"userSessionBO is null or fail to get sessionsecret by sessionkey","success":null}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	d := &Driver{client: srv.Client()}
	err := d.rawJSON(t.Context(), http.MethodGet, srv.URL+"/listFiles.action", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	ae, ok := domain.AsAppError(err)
	if !ok {
		t.Fatalf("expected *AppError, got %T: %v", err, err)
	}
	if ae.Code != domain.CodeAuthExpired {
		t.Fatalf("code = %q, want %q (full err: %v)", ae.Code, domain.CodeAuthExpired, err)
	}
}

// TestRawFormHTTP400InvalidSessionKeyIsAuthExpired 锁定 rawForm 同样归类为 CodeAuthExpired。
func TestRawFormHTTP400InvalidSessionKeyIsAuthExpired(t *testing.T) {
	body := `{"errorCode":"InvalidSessionKey","errorMsg":"userSessionBO is null","success":null}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	d := &Driver{client: srv.Client()}
	err := d.rawForm(t.Context(), http.MethodPost, srv.URL+"/batch/createBatchTask.action", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	ae, ok := domain.AsAppError(err)
	if !ok {
		t.Fatalf("expected *AppError, got %T: %v", err, err)
	}
	if ae.Code != domain.CodeAuthExpired {
		t.Fatalf("code = %q, want %q (full err: %v)", ae.Code, domain.CodeAuthExpired, err)
	}
}

// TestIs189AuthExpiredPayload 锁定 payload 标记判定，覆盖大小写与两种典型标记。
func TestIs189AuthExpiredPayload(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"InvalidSessionKey 大小写混合", `{"errorCode":"InvalidSessionKey"}`, true},
		{"userSessionBO is null", `{"errorMsg":"userSessionBO is null or fail to get sessionsecret by sessionkey"}`, true},
		{"UserInvalidOpenToken", `{"errorCode":"UserInvalidOpenToken"}`, true},
		{"UnifyAccountInfo is null", `{"errorCode":"UnifyAccountInfo is null"}`, true},
		{"正常错误", `{"errorCode":"SomeOtherError"}`, false},
		{"空串", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := is189AuthExpiredPayload([]byte(tc.raw)); got != tc.want {
				t.Fatalf("is189AuthExpiredPayload(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
