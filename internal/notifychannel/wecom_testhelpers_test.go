package notifychannel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// localServer 是指向本地桩的 HTTP 服务，供企微相关断言使用。
type localServer struct {
	url    string
	client *http.Client
}

func newLocalServer(t *testing.T, status int, body string) *localServer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return &localServer{url: srv.URL, client: srv.Client()}
}
