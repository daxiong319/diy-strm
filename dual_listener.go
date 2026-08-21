package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
)

// dualProtoListener 在同一端口上同时提供 HTTP 与 HTTPS 服务
//
// 通过检测客户端首个字节 (TLS ClientHello 固定为 0x16) 将连接分诊到 HTTP 或 HTTPS
type dualProtoListener struct {
	httpLn *sniffListener
	tlsLn  *sniffListener
}

// sniffListener 向 http.Server 提供可 Accept 的连接通道
type sniffListener struct {
	connCh chan net.Conn
	errCh  chan error
}

func (l *sniffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connCh:
		return c, nil
	case err := <-l.errCh:
		return nil, err
	}
}

func (l *sniffListener) Close() error { return nil }

func (l *sniffListener) Addr() net.Addr { return nil }

// sniffConn 带缓冲的连接, 保证 Peek 过的字节不会丢失
type sniffConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *sniffConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// newDualProtoListener 在 ln 上创建 HTTP/HTTPS 分诊监听器
func newDualProtoListener(ln net.Listener) *dualProtoListener {
	l := &dualProtoListener{
		httpLn: &sniffListener{connCh: make(chan net.Conn), errCh: make(chan error)},
		tlsLn:  &sniffListener{connCh: make(chan net.Conn), errCh: make(chan error)},
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				l.httpLn.errCh <- err
				l.tlsLn.errCh <- err
				return
			}
			go func(c net.Conn) {
				br := bufio.NewReader(c)
				first, err := br.Peek(1)
				if err != nil {
					c.Close()
					return
				}
				sc := &sniffConn{Conn: c, r: br}
				if first[0] == 0x16 {
					l.tlsLn.connCh <- sc
				} else {
					l.httpLn.connCh <- sc
				}
			}(conn)
		}
	}()
	return l
}

// serveDualProto 在 addr 上同时提供 HTTP 与 HTTPS 服务
//
// HTTP 使用 app.httpServer, HTTPS 使用新建的 tlsServer (存入 app.httpsServer 以便停止时关闭)
func serveDualProto(addr, certFile, keyFile string, handler http.Handler, app *App) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("加载证书失败: %v", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	dual := newDualProtoListener(ln)

	tlsServer := &http.Server{
		Handler: handler,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
	}
	app.httpsServer = tlsServer
	go func() {
		if err := tlsServer.Serve(dual.tlsLn); err != nil && err != http.ErrServerClosed {
			fmt.Println("HTTPS Serve error:", err)
		}
	}()
	return app.httpServer.Serve(dual.httpLn)
}
