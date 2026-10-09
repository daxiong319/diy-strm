package playmonitor

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
)

// countingResponseWriter 包一层 http.ResponseWriter，只做一件事：数吐出去的字节。
//
// 为什么必须包在 ResponseWriter 上而不是在业务代码里统计：
// internal/playback 里字节真正落盘有四条路径 ——
//
//	streamUpstreamBody → io.CopyBuffer      (range_proxy.go:257)
//	passthrough        → ReverseProxy.ServeHTTP (range_proxy.go:426)
//	serveContent       → copyN              (response.go:68)
//	writeRedirect      → 302，一字节正文都没有 (redirect.go:9)
//
// 每条路径各自加计数字段就得改四处、且极易漏掉其中一条。
// 在 serveStream 调用前把 w 换掉，四条路径**自动**全部被覆盖，
// 且不需要动 internal/playback 里任何一行既有取流代码。
//
// 为什么 WriteHeader 也要透传：serveContent 与 ReverseProxy 都会显式写
// 状态码，漏掉会把 200 变成 200 之外的东西。
type countingResponseWriter struct {
	http.ResponseWriter
	n int64
	// code 记录最终状态码（首个 WriteHeader 生效值）。
	code int
	// wroteHeader 标记是否已经写过响应头，避免二次 WriteHeader 被误判。
	wroteHeader bool
	// onDone 会话结束时回调，回报实际吐了多少字节。
	onDone func(bytes int64, code int)
}

func (w *countingResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	// 记实际写入成功的那部分：客户端中途断开时 Write 会返回短写+错误，
	// 把没送出去的字节算成上行会虚高流量。
	w.n += int64(n)
	return n, err
}

func (w *countingResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Bytes 已确认写出的字节数。
func (w *countingResponseWriter) Bytes() int64 { return w.n }

// Code 最终响应码；未写过响应头时返回 0。
func (w *countingResponseWriter) Code() int { return w.code }

// Finish 结束计数并触发 onDone（可重复调用，只有第一次生效）。
func (w *countingResponseWriter) Finish() {
	if w.onDone == nil {
		return
	}
	fn := w.onDone
	w.onDone = nil
	fn(w.n, w.code)
}

// Flush 透传：ReverseProxy 与 SSE/分块响应会依赖它。
// 播放器是边下边播的，丢了 Flush 会导致缓冲攒够一整块才吐给客户端。
func (w *countingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 透传：让 http.ResponseController 能拿到底层 writer，
// 也让内层类型断言（如 *internal/playback 的写钩子）继续工作。
func (w *countingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// 以下是 net/http 内部通过类型断言探测的可选接口，包装后必须逐个透传，
// 否则会静默退化（比如反向代理不压缩、HTTP/2 不 flush）。
var (
	_ http.Flusher  = (*countingResponseWriter)(nil)
	_ http.Hijacker = (*countingResponseWriter)(nil)
)

// Hijack 透传（WebSocket 升级场景）。
func (w *countingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("playmonitor: ResponseWriter 不支持 Hijack")
	}
	return h.Hijack()
}

// ReadFrom 透传：io.Copy 优先用 ReaderFrom 走零拷贝，
// 少透传这一层会让大文件下行从 sendfile 退化成用户态拷贝。
// 注意：实现了它就必须在这里计数，因为字节不再经过 Write。
func (w *countingResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(r)
		w.n += n
		return n, err
	}
	n, err := io.Copy(struct{ io.Writer }{w.ResponseWriter}, r)
	w.n += n
	return n, err
}
