package playback

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"litepan/internal/domain"
)

// TestServeRedirectResolvedEmits302 证明 /redirect 端点真的交出的是 302 + 真实直链。
//
// 断言 Location 而不是「有响应」：写错成 200 或把 Location 填成本站地址，
// 播放器会去一个拿不到字节的地方，表现是「一直转圈」，服务端却一切正常。
func TestServeRedirectResolvedEmits302(t *testing.T) {
	m := &recordingStreamMonitor{bitrateBps: 8_000_000}
	s := newMonitoredService(m)
	w := httptest.NewRecorder()
	r := externalReq(t)

	res := redirectResolved()
	if err := s.serveRedirectResolved(w, r, Request{AccountID: 7, FileID: "1"}, res, Intent{FileName: "Movie.MKV"}); err != nil {
		t.Fatalf("serveRedirectResolved 返回错误：%v", err)
	}
	if w.Code != http.StatusFound {
		t.Fatalf("状态码 = %d，期望 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != res.Link.URL {
		t.Fatalf("Location = %q，期望真实直链 %q", got, res.Link.URL)
	}
	// 只断言正文里没有媒体字节：http.Redirect 会写一段 <a> 提示正文，
	// 那不是「本站在转发内容」。断言「正文为空」会把一个正确的实现判成错的。
	if body := w.Body.String(); len(body) > 200 {
		t.Fatalf("302 响应不应带内容正文，得到 %q", body)
	}
	if m.redirectCalls != 1 {
		t.Fatalf("redirect 回调应发生 1 次，实际 %d", m.redirectCalls)
	}
	if m.billedBytes != 0 {
		t.Fatalf("302 直连不应产生任何计费上行，实际 %d 字节", m.billedBytes)
	}
	if cc := w.Header().Get("Cache-Control"); cc == "" {
		t.Fatal("直链有时效，302 必须带禁缓存头，否则播放器会缓存过期地址")
	}
}

// TestServeRedirectResolvedRefusesWhenNoDirectLink 网盘没给直链时必须报错。
//
// 关键在于**不能**悄悄退回流代理：这个端点的全部意义就是「给你直链」，
// 退回转发字节会让调用方以为自己拿到了直链，实测带宽时才发现流量又回到本站。
func TestServeRedirectResolvedRefusesWhenNoDirectLink(t *testing.T) {
	cases := []struct {
		name string
		res  Resolved
	}{
		{name: "只有本地路径没有直链", res: Resolved{Mode: domain.DownloadRedirect, File: domain.FileItem{ID: "1", Name: "a.mkv"}}},
		{name: "驱动声明必须流代理", res: Resolved{
			Mode: domain.DownloadRedirect,
			Link: domain.DownloadInfo{URL: "https://cdn.example/a.mkv", ForceProxy: true},
			File: domain.FileItem{ID: "1", Name: "a.mkv"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &recordingStreamMonitor{}
			s := newMonitoredService(m)
			w := httptest.NewRecorder()
			err := s.serveRedirectResolved(w, externalReq(t), Request{AccountID: 7, FileID: "1"}, tc.res, Intent{})
			if err == nil {
				t.Fatal("没有直链时必须报错，而不是悄悄改成流代理")
			}
			if w.Code != http.StatusOK && w.Header().Get("Location") != "" {
				t.Fatalf("报错路径不应发出 302，状态码 = %d，Location = %q", w.Code, w.Header().Get("Location"))
			}
			if m.redirectCalls != 0 {
				t.Fatalf("报错路径不应建立监控会话，实际回调 %d 次", m.redirectCalls)
			}
		})
	}
}

func TestServeRedirectResolvedRefusesDirectory(t *testing.T) {
	s := newMonitoredService(&recordingStreamMonitor{})
	w := httptest.NewRecorder()
	res := Resolved{Mode: domain.DownloadRedirect, Link: domain.DownloadInfo{URL: "https://cdn.example/d"}, File: domain.FileItem{ID: "1", IsDir: true}}
	if err := s.serveRedirectResolved(w, externalReq(t), Request{AccountID: 7, FileID: "1"}, res, Intent{}); err == nil {
		t.Fatal("目录不能 302 直连")
	}
}

// TestServeRedirectUsesIntentFileNameWhenEmpty 端点在 URL 没带文件名时
// 用解析结果里的文件名建监控会话，否则播放记录里会出现一条没有文件名的会话。
func TestServeRedirectUsesIntentFileNameWhenEmpty(t *testing.T) {
	m := &recordingStreamMonitor{}
	s := newMonitoredService(m)
	w := httptest.NewRecorder()
	res := redirectResolved()
	if err := s.serveRedirectResolved(w, externalReq(t), Request{AccountID: 7, FileID: "1"}, res, Intent{}); err != nil {
		t.Fatalf("serveRedirectResolved 返回错误：%v", err)
	}
	if len(m.redirectOpens) != 1 {
		t.Fatalf("应建立 1 条监控会话，实际 %d", len(m.redirectOpens))
	}
	if got := m.redirectOpens[0].ItemName; got != res.File.Name {
		t.Fatalf("监控会话文件名 = %q，期望回落到解析结果 %q", got, res.File.Name)
	}
}
