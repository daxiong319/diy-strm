package controllers

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"diy-strm/internal/helpers"

	"github.com/gin-gonic/gin"
)

// maxBodyLogSize 响应体日志采样上限（避免代理大文件时占用内存）
const maxBodyLogSize = 8 << 10

// bodyLogWriter 包装 ResponseWriter：
// - 透传全部写入，同时保留响应体前 maxBodyLogSize 字节用于错误日志采样
// - 兼容 SSE（Flush）与普通写入
type bodyLogWriter struct {
	gin.ResponseWriter
	body        *bytes.Buffer
	wroteHeader bool
}

func (w *bodyLogWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	if w.body.Len() < maxBodyLogSize {
		remain := maxBodyLogSize - w.body.Len()
		if len(p) > remain {
			w.body.Write(p[:remain])
		} else {
			w.body.Write(p)
		}
	}
	return w.ResponseWriter.Write(p)
}

func (w *bodyLogWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *bodyLogWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *bodyLogWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("ResponseWriter 不支持 Hijack")
}

// RequestErrorLogger 请求错误日志中间件：
// 仅当响应状态 >= 400 时记录一行（方法/路径/状态/客户端IP/UA/来源/耗时/响应体采样），
// 用于定位线上 4xx/5xx（如旧前端、异常请求导致的 400 等），正常请求不产生日志噪音。
func RequestErrorLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		blw := &bodyLogWriter{ResponseWriter: c.Writer, body: bytes.NewBuffer(nil)}
		c.Writer = blw
		c.Next()
		status := c.Writer.Status()
		if status < 400 {
			return
		}
		bodySnippet := strings.TrimSpace(blw.body.String())
		if len(bodySnippet) > 1024 {
			bodySnippet = bodySnippet[:1024] + "..."
		}
		helpers.AppLogger.Warnf(
			"请求错误 status=%d method=%s path=%s ip=%s ua=%s referer=%s origin=%s cost=%s body=%s",
			status,
			c.Request.Method,
			c.Request.URL.RequestURI(),
			c.ClientIP(),
			cutString(c.Request.UserAgent(), 120),
			cutString(c.Request.Referer(), 160),
			cutString(c.Request.Header.Get("Origin"), 160),
			time.Since(start).Round(time.Millisecond),
			bodySnippet,
		)
	}
}

// cutString 截断字符串到指定长度
func cutString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}