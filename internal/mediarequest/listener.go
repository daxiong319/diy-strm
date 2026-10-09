package mediarequest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"litepan/internal/settings"
)

// watchInterval 设置变化的自查间隔。
//
// 为什么是轮询而不是订阅设置变更事件：本仓的 settings.Service 没有任何变更通知
// 机制（只有 OnSettingsUpdated 刷缓存用的钩子，见 internal/app/wire_http.go）。
// 为了一个「开/关一个端口」去给设置层加一套订阅机制，改动面比这件事本身大得多。
//
// 15 秒的取舍：求片站的开关是「今晚要不要让家人用」这种粒度的操作，
// 等 15 秒完全无所谓；而轮询本身每秒一次的代价（一次 settings 读）也可以忽略。
// 真正影响体验的是「关掉之后别再让别人提交」，而这一点由 handler 里的
// Enabled() 检查**立即**生效（见 internal/api/request_portal.go）：
// 监听口会在下一轮关闭，而端口在关闭前就已经不接活了。
const watchInterval = 15 * time.Second

// Listener 求片站的独立监听口。
//
// 为什么单独一个 listener 而不是挂在管理台的 chi 上：
// 任务书要求「这个端口上只有登录页 + 求片页，后台接口一律 404」。
// 同一进程、同一个 router 做不到这一点 —— 只要挂在同一个树上，
// 将来有人给管理台加一条路由，这条路由就会自动出现在求片端口上。
// 独立 router + 独立端口是唯一能让「求片端口上不存在后台接口」这件事
// 由**结构**保证而不是靠人记得别往里加路由。
type Listener struct {
	// Handler 求片站的路由树（只含 4 条业务路由 + 静态资源）。
	Handler http.Handler
	// Cfg 提供 Enabled() 与端口。
	Cfg Settings
	// Log 记录起停与错误。
	Log Logger

	mu       sync.Mutex
	srv      *http.Server
	ln       net.Listener
	lastPort int
	lastErr  string
}

// running 当前是否在监听。
func (l *Listener) running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.srv != nil
}

// Port 返回当前实际监听的端口；未监听时返回 0。
func (l *Listener) Port() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ln == nil {
		return 0
	}
	if addr, ok := l.ln.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

// Run 阻塞运行：按设置的开关起停监听口，直到 ctx 取消。
//
// 整个循环对**绑定失败**是容错的：求片站的端口被占用（比如和 Vyo 的 7812 撞了）
// 只记日志，不退出，更不会把管理台一起拖死。
// 理由很直接 —— 管理台是这个软件的主入口，为了一个默认关闭的可选功能
// 让主入口挂掉，是不可接受的方向。错误文案相同就不重复刷日志。
func (l *Listener) Run(ctx context.Context) {
	if l == nil || l.Handler == nil {
		return
	}
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	l.sync(ctx)
	for {
		select {
		case <-ctx.Done():
			l.stop()
			return
		case <-ticker.C:
			l.sync(ctx)
		}
	}
}

// sync 让监听口的开合与设置对齐。
func (l *Listener) sync(ctx context.Context) {
	want := l.Cfg != nil && l.Cfg.Bool(settings.KeyMOMediaRequestEnabled)
	port := DefaultPortalPort
	if l.Cfg != nil {
		port = l.Cfg.Int(settings.KeyMOMediaRequestPort)
	}
	if !want {
		if l.running() {
			l.Log.Warn("求片中心已关闭，求片端口停止监听")
			l.stop()
		}
		return
	}
	if l.running() {
		// 已经在听了。端口变了要不要重启？
		// 要 —— 否则管理员把端口从 7812 改成 7813 之后，
		// 老端口还在服务、新端口不工作，现象是「我改了怎么还是老地址」。
		actual := l.Port()
		clamped := ClampPortalPort(port)
		if clamped == actual {
			return
		}
		l.Log.Info("求片端口已变更，重启监听", "from", actual, "to", clamped)
		l.stop()
	}

	clamped := ClampPortalPort(port)
	if clamped == 0 {
		l.setErr("求片端口配置非法（应在 1024~65535 之间），求片站未启动")
		return
	}
	if err := l.start(clamped); err != nil {
		l.setErr(err.Error())
		return
	}
	l.mu.Lock()
	l.lastErr = ""
	l.lastPort = clamped
	l.mu.Unlock()
	l.Log.Info("求片站已启动", "port", clamped)
}

// start 绑定并服务一个端口。
func (l *Listener) start(port int) error {
	addr := net.JoinHostPort("", strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           l.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return context.Background() },
	}
	l.mu.Lock()
	l.srv = srv
	l.ln = ln
	l.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.Log.Error("求片站监听异常退出", "err", err)
		}
	}()
	return nil
}

// stop 关闭监听口。
func (l *Listener) stop() {
	l.mu.Lock()
	srv := l.srv
	ln := l.ln
	l.srv = nil
	l.ln = nil
	l.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		// 关不掉就硬关：求片站是可选项，不值得为它把进程吊住。
		_ = srv.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
}

// setErr 记录错误，同一条不重复刷日志。
func (l *Listener) setErr(msg string) {
	l.mu.Lock()
	same := l.lastErr == msg
	l.lastErr = msg
	l.mu.Unlock()
	if !same {
		l.Log.Error("求片站未能监听，管理台不受影响", "err", msg)
	}
}

// LastError 返回最近一次起停失败的原因（成功时为空串）。
func (l *Listener) LastError() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastErr
}
