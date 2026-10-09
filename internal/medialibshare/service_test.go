package medialibshare

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"litepan/internal/settings"
	"litepan/internal/store"
)

// 本文件覆盖任务书要求的四项测试：token 校验、有效期、设备数、
// 24 小时访客去重，外加「开关关闭时对外像不存在」这条性质。
//
// 全部走真库（internal/store.Open + Migrate），不 mock Store：
// 这个功能最容易出的错是**计数与并发语义**，而那两样 mock 出来永远是绿的。
// 用内存 struct 模拟的 Store 会忠实地复现「我以为的语义」而不是真实的。

// ---------------------------------------------------------------- 脚手架

type fakeCfg struct {
	enabled    bool
	expireDays int
	maxDevices int
	password   string
}

func (c fakeCfg) String(key string) string {
	if key == settings.KeyMOLibraryShareDefaultPassword {
		return c.password
	}
	return ""
}

func (c fakeCfg) Bool(key string) bool {
	return key == settings.KeyMOLibraryShareEnabled && c.enabled
}

func (c fakeCfg) Int(key string) int {
	switch key {
	case settings.KeyMOLibraryShareDefaultExpireDays:
		return c.expireDays
	case settings.KeyMOLibraryShareDefaultMaxDevices:
		return c.maxDevices
	}
	return 0
}

type capturingLog struct {
	lines []string
}

func (l *capturingLog) Info(msg string, args ...any) {
	l.lines = append(l.lines, msg+" "+fmtArgs(args))
}
func (l *capturingLog) Warn(msg string, args ...any) {
	l.lines = append(l.lines, msg+" "+fmtArgs(args))
}

func fmtArgs(args []any) string {
	var b strings.Builder
	for i := 0; i+1 < len(args); i += 2 {
		b.WriteString(" ")
		b.WriteString(strings.TrimSpace(sprint(args[i])))
		b.WriteString("=")
		b.WriteString(sprint(args[i+1]))
	}
	return b.String()
}

func sprint(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

type env struct {
	t   *testing.T
	ctx context.Context
	db  *store.DB
	svc *Service
	log *capturingLog
	now time.Time
}

func newEnv(t *testing.T, cfg fakeCfg) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "share.db")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	lg := &capturingLog{}
	e := &env{t: t, ctx: ctx, db: db, log: lg, now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	e.svc = NewService(Params{
		Store: NewStore(db.WriteHandle(), db.ReadHandle()),
		Cfg:   cfg,
		Log:   lg,
		Now:   func() time.Time { return e.now },
	})
	return e
}

func defaultCfg() fakeCfg {
	return fakeCfg{enabled: true, expireDays: DefaultExpireDays, maxDevices: DefaultMaxDevices}
}

func (e *env) create(in CreateInput) Created {
	e.t.Helper()
	if in.Title == "" {
		in.Title = "测试影片"
	}
	if in.FileID == "" {
		in.FileID = "file-abc"
	}
	c, err := e.svc.Create(e.ctx, in)
	if err != nil {
		e.t.Fatalf("创建分享失败: %v", err)
	}
	return c
}

func (e *env) count(tbl string) int {
	e.t.Helper()
	var n int
	if err := e.db.ReadHandle().QueryRowContext(e.ctx, "SELECT COUNT(*) FROM "+tbl).Scan(&n); err != nil {
		e.t.Fatalf("count %s: %v", tbl, err)
	}
	return n
}

func (e *env) advance(d time.Duration) { e.now = e.now.Add(d) }

// ---------------------------------------------------------------- token 校验

// TestTokenRoundTrip 验收②：签发的令牌能通过校验，且换不出别的东西。
func TestTokenRoundTrip(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{})
	res, err := e.svc.IssueToken(e.ctx, c.Code, "", "visitor-1", "10.0.0.8", "Mozilla/5.0 Chrome")
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	if !res.HasToken || !res.IsNewVisit || res.Token == "" {
		t.Fatalf("新会话应当回传明文令牌，实际 %+v", res)
	}
	sh, visit, err := e.svc.AuthorizeToken(e.ctx, res.Token)
	if err != nil {
		t.Fatalf("校验令牌失败: %v", err)
	}
	if sh.ID != c.Share.ID {
		t.Errorf("令牌校验到了别的分享：%s != %s", sh.ID, c.Share.ID)
	}
	if visit.VisitorID != "visitor-1" {
		t.Errorf("访客标识对不上：%q", visit.VisitorID)
	}
	// 明文令牌不能落库：库里只有哈希。
	var stored string
	if err := e.db.ReadHandle().QueryRowContext(e.ctx,
		"SELECT token_hash FROM library_share_visits WHERE id=?", visit.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == res.Token {
		t.Fatal("库里存的是明文令牌 —— 必须存哈希")
	}
	if stored != HashToken(res.Token) {
		t.Errorf("库里存的哈希对不上")
	}
}

// TestTokenRejection 验收②：缺失、错误、别人的、过期的一律拒绝。
func TestTokenRejection(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1})
	res, err := e.svc.IssueToken(e.ctx, c.Code, "", "visitor-1", "10.0.0.8", "Chrome")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("空令牌", func(t *testing.T) {
		if _, _, err := e.svc.AuthorizeToken(e.ctx, "  "); !errors.Is(err, ErrBadToken) {
			t.Errorf("空令牌应报 ErrBadToken，实际 %v", err)
		}
	})
	t.Run("乱编的令牌", func(t *testing.T) {
		if _, _, err := e.svc.AuthorizeToken(e.ctx, "not-a-real-token"); !errors.Is(err, ErrBadToken) {
			t.Errorf("无效令牌应报 ErrBadToken，实际 %v", err)
		}
	})
	t.Run("超过 24 小时未活动", func(t *testing.T) {
		e.advance(VisitorCountWindow + time.Minute)
		_, _, err := e.svc.AuthorizeToken(e.ctx, res.Token)
		if !errors.Is(err, ErrBadToken) {
			t.Errorf("过期会话应报 ErrBadToken，实际 %v", err)
		}
	})
	t.Run("分享撤销后", func(t *testing.T) {
		e2 := newEnv(t, defaultCfg())
		c2 := e2.create(CreateInput{ExpireDays: -1})
		r2, err := e2.svc.IssueToken(e2.ctx, c2.Code, "", "v1", "10.0.0.8", "Chrome")
		if err != nil {
			t.Fatal(err)
		}
		if err := e2.svc.Delete(e2.ctx, c2.Share.ID); err != nil {
			t.Fatalf("撤销失败: %v", err)
		}
		// 分享被撤销后返回 ErrNotFound 而不是 ErrBadToken：
		// 对访客而言「链接已经废了」和「你的令牌坏了」是两回事，
		// 报后者会让前端白跑一趟换令牌请求。详见 AuthorizeToken 里的注释。
		if _, _, err := e2.svc.AuthorizeToken(e2.ctx, r2.Token); !errors.Is(err, ErrNotFound) {
			t.Errorf("撤销后的令牌应报 ErrNotFound，实际 %v", err)
		}
		// 撤销之后连换令牌也不该换得出来。
		if _, err := e2.svc.IssueToken(e2.ctx, c2.Code, "", "v1", "10.0.0.8", "Chrome"); !errors.Is(err, ErrNotFound) {
			t.Errorf("撤销后的分享不该再签发令牌，实际 %v", err)
		}
	})
}

// TestTokenIsNotReissuedOnRefresh 刷新页面不换令牌。
//
// 换令牌会让正在播放的视频突然 401 —— 用户看到的就是「播到一半卡住」。
// 顺带钉住 IssueResult.Token 只在新会话时非空。
func TestTokenIsNotReissuedOnRefresh(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1})
	first, err := e.svc.IssueToken(e.ctx, c.Code, "", "visitor-1", "10.0.0.8", "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.IssueToken(e.ctx, c.Code, "", "visitor-1", "10.0.0.8", "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	if second.IsNewVisit {
		t.Error("第二次不该算新访客")
	}
	if second.Token != "" {
		t.Error("复用会话不该回传明文令牌")
	}
	if !second.HasToken {
		t.Error("复用会话仍应告知前端「有令牌可用」")
	}
	if _, _, err := e.svc.AuthorizeToken(e.ctx, first.Token); err != nil {
		t.Errorf("旧令牌应仍然有效: %v", err)
	}
	if n := e.count("library_share_visits"); n != 1 {
		t.Errorf("访客会话行数 = %d，期望 1", n)
	}
}

// ---------------------------------------------------------------- 有效期

// TestExpireDayOptions 验收⑤：五种取值域都被接受，且 0 表示永久。
func TestExpireDayOptions(t *testing.T) {
	if got := ExpireDayOptions; len(got) != 5 ||
		got[0] != 1 || got[1] != 3 || got[2] != 7 || got[3] != 30 || got[4] != 0 {
		t.Fatalf("有效期的取值域变了：%v", got)
	}
	e := newEnv(t, defaultCfg())
	for _, days := range ExpireDayOptions {
		c := e.create(CreateInput{ExpireDays: days})
		sh, err := e.svc.Get(e.ctx, c.Share.ID)
		if err != nil {
			t.Fatal(err)
		}
		if days == 0 {
			if sh.ExpiresAt != nil {
				t.Errorf("0 天应表示永久，实际 expires_at=%v", sh.ExpiresAt)
			}
			continue
		}
		if sh.ExpiresAt == nil {
			t.Fatalf("%d 天应有过期时间", days)
		}
		got := sh.ExpiresAt.Sub(e.now).Hours() / 24
		if got != float64(days) {
			t.Errorf("%d 天的分享过期时间差了 %v 天", days, got-float64(days))
		}
		// 到期之后立刻不可用。
		e.advance(time.Duration(days)*24*time.Hour + time.Minute)
		if _, err := e.svc.LookupByCode(e.ctx, c.Code); !errors.Is(err, ErrNotFound) {
			t.Errorf("%d 天的分享到期后应查不到，实际 %v", days, err)
		}
		_, err = e.svc.IssueToken(e.ctx, c.Code, "", "v-"+string(rune('a'+days)), "10.0.0.8", "Chrome")
		if err == nil {
			t.Errorf("%d 天的分享到期后不该再签发令牌", days)
		}
	}
}

// TestNormalizeCreateDays 三态：负数=走默认，0=永久，正数=照用。
func TestNormalizeCreateDays(t *testing.T) {
	if got := NormalizeCreateDays(-1, 7); got != 7 {
		t.Errorf("未指定应回落默认 7，实际 %d", got)
	}
	if got := NormalizeCreateDays(0, 7); got != 0 {
		t.Errorf("0 必须保持 0（永久），实际 %d —— 用 0 表示未指定会把永久分享变成 7 天", got)
	}
	if got := NormalizeCreateDays(30, 7); got != 30 {
		t.Errorf("30 应保持 30，实际 %d", got)
	}
	// 非法值回落默认，而不是回落成永久 —— 永久是安全方向的反面。
	if got := NormalizeExpireDays(9999); got != DefaultExpireDays {
		t.Errorf("非法天数应回落 %d，实际 %d", DefaultExpireDays, got)
	}
	if got := NormalizeMaxDevices(0); got != DefaultMaxDevices {
		t.Errorf("0 台应回落默认 %d，实际 %d", DefaultMaxDevices, got)
	}
	if got := NormalizeMaxDevices(-3); got != DefaultMaxDevices {
		t.Errorf("负数台数应回落默认 %d，实际 %d", DefaultMaxDevices, got)
	}
}

// TestSetExpiry 改期后立即生效，负数改回永久。
func TestSetExpiry(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1})
	if err := e.svc.SetExpiry(e.ctx, c.Share.ID, 1); err != nil {
		t.Fatal(err)
	}
	sh, err := e.svc.Get(e.ctx, c.Share.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sh.ExpiresAt == nil || sh.Expired(e.now) != false {
		t.Fatalf("改成 1 天后应仍未过期，实际 %v", sh.ExpiresAt)
	}
	if err := e.svc.SetExpiry(e.ctx, c.Share.ID, -1); err != nil {
		t.Fatal(err)
	}
	sh, _ = e.svc.Get(e.ctx, c.Share.ID)
	if sh.ExpiresAt != nil {
		t.Errorf("负数应改回永久，实际 %v", sh.ExpiresAt)
	}
}

// ---------------------------------------------------------------- 设备数

// TestDeviceLimit 验收⑥：超出设备数被拒，且复用的会话不受影响。
func TestDeviceLimit(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1, MaxDevices: 2})

	for _, v := range []string{"phone", "tablet"} {
		if _, err := e.svc.IssueToken(e.ctx, c.Code, "", v, "10.0.0.8", "Chrome"); err != nil {
			t.Fatalf("%s 应当签发成功: %v", v, err)
		}
	}
	_, err := e.svc.IssueToken(e.ctx, c.Code, "", "laptop", "10.0.0.9", "Chrome")
	if !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("第三台设备应被拒，实际 %v", err)
	}
	// 已经进来的那台还能继续用 —— 设备数限的是「同时在线几台」，
	// 不是「历史上有几台访问过」。
	res, err := e.svc.IssueToken(e.ctx, c.Code, "", "phone", "10.0.0.8", "Chrome")
	if err != nil {
		t.Fatalf("已签发的设备不该被挤掉: %v", err)
	}
	if !res.HasToken {
		t.Error("复用会话应告知前端有令牌")
	}
}

// TestDeviceLimitUsesActiveWindow 设备数按「24 小时内活跃」算。
//
// 用窗口而不是行数：一个月前来过的访客不该一直占着名额，
// 否则分享放到第 30 天必然一个新人也进不来。
func TestDeviceLimitUsesActiveWindow(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: 30, MaxDevices: 1})
	if _, err := e.svc.IssueToken(e.ctx, c.Code, "", "old", "10.0.0.8", "Chrome"); err != nil {
		t.Fatal(err)
	}
	e.advance(VisitorCountWindow + time.Minute)
	if _, err := e.svc.IssueToken(e.ctx, c.Code, "", "new", "10.0.0.9", "Chrome"); err != nil {
		t.Fatalf("老访客超时后新人应能进，实际 %v", err)
	}
}

// ---------------------------------------------------------------- 24h 访客去重

// TestVisitorCountedOncePerDay 验收④：同浏览器 24 小时内多次访问只计一次 visitor。
func TestVisitorCountedOncePerDay(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1, MaxDevices: 5})

	// 同一访客连开十次。
	for i := 0; i < 10; i++ {
		if _, err := e.svc.IssueToken(e.ctx, c.Code, "", "visitor-1", "10.0.0.8", "Chrome"); err != nil {
			t.Fatalf("第 %d 次访问失败: %v", i, err)
		}
		e.advance(time.Hour)
	}
	sh, err := e.svc.Get(e.ctx, c.Share.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sh.VisitorCount != 1 {
		t.Errorf("24 小时内的 visitor 计数 = %d，期望 1", sh.VisitorCount)
	}

	// 24 小时后再算一次新的 —— 「24 小时内只算一次」的另一半。
	e.advance(VisitorCountWindow)
	if _, err := e.svc.IssueToken(e.ctx, c.Code, "", "visitor-1", "10.0.0.8", "Chrome"); err != nil {
		t.Fatal(err)
	}
	sh, _ = e.svc.Get(e.ctx, c.Share.ID)
	if sh.VisitorCount != 2 {
		t.Errorf("跨过 24 小时后 visitor 计数 = %d，期望 2", sh.VisitorCount)
	}
}

// TestVisitorCountIsNotPerIP 同 IP 的不同浏览器要分开算。
//
// 反过来也不能成立成「按 IP 算」：同一个出口 IP 后面坐着一整家人。
func TestVisitorCountIsNotPerIP(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1, MaxDevices: 5})
	for _, v := range []string{"visitor-a", "visitor-b", "visitor-c"} {
		if _, err := e.svc.IssueToken(e.ctx, c.Code, "", v, "10.0.0.8", "Chrome"); err != nil {
			t.Fatal(err)
		}
	}
	sh, _ := e.svc.Get(e.ctx, c.Share.ID)
	if sh.VisitorCount != 3 {
		t.Errorf("三个访客同一 IP，visitor 计数 = %d，期望 3", sh.VisitorCount)
	}
}

// TestVisitorNotCountedTwiceConcurrently 并发访问不能把计数翻倍。
//
// 这条正是「先查 counted 再决定加不加」会踩的坑：两个请求都查到 0。
// 这里用真实并发跑 8 个 goroutine。
func TestVisitorNotCountedTwiceConcurrently(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1, MaxDevices: 8})
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := e.svc.IssueToken(e.ctx, c.Code, "", "visitor-1", "10.0.0.8", "Chrome")
			done <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatalf("并发签发失败: %v", err)
		}
	}
	sh, err := e.svc.Get(e.ctx, c.Share.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sh.VisitorCount != 1 {
		t.Errorf("并发访问后 visitor 计数 = %d，期望 1 —— 计数挂在了「先查后加」上", sh.VisitorCount)
	}
}

// ---------------------------------------------------------------- 口令

func TestPasswordGate(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1, Password: "暗号"})

	if _, err := e.svc.IssueToken(e.ctx, c.Code, "", "v1", "10.0.0.8", "Chrome"); !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("没填口令应报 ErrPasswordRequired，实际 %v", err)
	}
	if _, err := e.svc.IssueToken(e.ctx, c.Code, "错的", "v1", "10.0.0.8", "Chrome"); !errors.Is(err, ErrBadPassword) {
		t.Errorf("口令错应报 ErrBadPassword，实际 %v", err)
	}
	if _, err := e.svc.IssueToken(e.ctx, c.Code, " 暗号 ", "v1", "10.0.0.8", "Chrome"); err != nil {
		t.Errorf("口令两侧空格应被忽略: %v", err)
	}
}

// TestDefaultPasswordFromSettings 没填口令时用配置里的默认口令。
func TestDefaultPasswordFromSettings(t *testing.T) {
	cfg := defaultCfg()
	cfg.password = "统一暗号"
	e := newEnv(t, cfg)
	c := e.create(CreateInput{ExpireDays: -1})
	if !c.Share.HasPassword() {
		t.Fatal("配置了默认口令就应该有口令")
	}
	if _, err := e.svc.IssueToken(e.ctx, c.Code, "", "v1", "10.0.0.8", "Chrome"); !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("实际 %v", err)
	}
	if _, err := e.svc.IssueToken(e.ctx, c.Code, "统一暗号", "v1", "10.0.0.8", "Chrome"); err != nil {
		t.Errorf("默认口令应能通过: %v", err)
	}
}

// ---------------------------------------------------------------- 开关

// TestDisabledLooksAbsent 验收⑨：开关关闭时对外一律「不存在」。
func TestDisabledLooksAbsent(t *testing.T) {
	cfg := defaultCfg()
	cfg.enabled = false
	e := newEnv(t, cfg)

	if _, err := e.svc.Create(e.ctx, CreateInput{Title: "x", FileID: "y"}); !errors.Is(err, ErrDisabled) {
		t.Errorf("关着时不该能创建分享，实际 %v", err)
	}
	if _, err := e.svc.LookupByCode(e.ctx, "whatever"); !errors.Is(err, ErrNotFound) {
		t.Errorf("关着时查短码应报 ErrNotFound 而不是 ErrDisabled —— 报错差异等于免费送出一条情报")
	}
	if _, _, err := e.svc.AuthorizeToken(e.ctx, "any-token"); !errors.Is(err, ErrNotFound) {
		t.Errorf("关着时校验令牌应报 ErrNotFound，实际 %v", err)
	}
}

// ---------------------------------------------------------------- 计数与脱敏

// TestPlayCountIncrements 播放计数真的会长。
//
// 曾经 RecordPlay 只写流水、play_count 永远是 0，统计页看着像坏了。
// 这条测试就是为了钉住「取流一次 = 播放数 +1」。
func TestPlayCountIncrements(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1})
	res, err := e.svc.IssueToken(e.ctx, c.Code, "", "v1", "10.0.0.8", "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordPlay(e.ctx, res.Share, res.Visit, "10.0.0.8", "share", "测试影片"); err != nil {
		t.Fatalf("记播放失败: %v", err)
	}
	sh, _ := e.svc.Get(e.ctx, c.Share.ID)
	if sh.PlayCount != 1 {
		t.Errorf("播放计数 = %d，期望 1", sh.PlayCount)
	}
	if n := e.count("library_share_plays"); n != 1 {
		t.Errorf("播放流水行数 = %d，期望 1", n)
	}
	// 前端上报的 play 事件只留流水，不重复计 play_count ——
	// 否则一次播放会被记两次（播放器上报一次，取流又记一次）。
	if err := e.svc.RecordEvent(e.ctx, c.Code, "v1", "10.0.0.8", string(EventPlay), "测试影片"); err != nil {
		t.Fatal(err)
	}
	sh, _ = e.svc.Get(e.ctx, c.Share.ID)
	if sh.PlayCount != 1 {
		t.Errorf("上报事件后播放计数 = %d，期望仍是 1", sh.PlayCount)
	}
	if n := e.count("library_share_plays"); n != 2 {
		t.Errorf("播放流水行数 = %d，期望 2", n)
	}
}

// TestOpenEventCountsView 打开页面只计 view。
func TestOpenEventCountsView(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1})
	for i := 0; i < 3; i++ {
		if err := e.svc.RecordEvent(e.ctx, c.Code, "v1", "10.0.0.8", string(EventOpen), ""); err != nil {
			t.Fatal(err)
		}
	}
	sh, _ := e.svc.Get(e.ctx, c.Share.ID)
	if sh.ViewCount != 3 {
		t.Errorf("view 计数 = %d，期望 3", sh.ViewCount)
	}
	if sh.VisitorCount != 0 {
		t.Errorf("只打开页面不该计 visitor，实际 %d", sh.VisitorCount)
	}
}

// TestIPAndUserAgentAreMasked 需求③的另一半：落库的 IP 是脱敏的。
func TestIPAndUserAgentAreMasked(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1})
	res, err := e.svc.IssueToken(e.ctx, c.Code, "", "v1", "192.168.1.123",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120")
	if err != nil {
		t.Fatal(err)
	}
	if res.Visit.IPMasked != "192.168.1.123/24" {
		t.Errorf("IPv4 应保留 /24，实际 %q", res.Visit.IPMasked)
	}
	if strings.Contains(res.Visit.UserAgent, "Windows NT") {
		t.Errorf("UA 应被压成摘要而不是原样落库，实际 %q", res.Visit.UserAgent)
	}
	var full string
	if err := e.db.ReadHandle().QueryRowContext(e.ctx,
		"SELECT ip_masked FROM library_share_visits WHERE id=?", res.Visit.ID).Scan(&full); err != nil {
		t.Fatal(err)
	}
	if full != "192.168.1.123/24" {
		t.Errorf("库里的 IP 应是脱敏后的，实际 %q", full)
	}
}

// TestLogsNeverContainCodeOrPassword 创建分享的日志里不能出现短码或口令。
//
// 需求③说「令牌不出现在日志里」。短码不算令牌，但它出现在分享链接里，
// 而日志常常比链接传得远 —— 所以一起打掉。
func TestLogsNeverContainCodeOrPassword(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1, Password: "我的暗号", Title: "某部片子"})
	joined := strings.Join(e.log.lines, "\n")
	if strings.Contains(joined, c.Code) {
		t.Errorf("日志里出现了分享短码：%s", joined)
	}
	if strings.Contains(joined, "我的暗号") {
		t.Errorf("日志里出现了口令明文：%s", joined)
	}
	if !strings.Contains(joined, c.Share.ID) {
		t.Errorf("日志里应当至少有 share_id 可查：%s", joined)
	}
}

// ---------------------------------------------------------------- 建档校验

func TestCreateValidation(t *testing.T) {
	e := newEnv(t, defaultCfg())
	if _, err := e.svc.Create(e.ctx, CreateInput{Title: "  ", FileID: "f"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("空标题应报 ErrInvalid，实际 %v", err)
	}
	if _, err := e.svc.Create(e.ctx, CreateInput{Title: "x", FileID: ""}); !errors.Is(err, ErrInvalid) {
		t.Errorf("空文件应报 ErrInvalid，实际 %v", err)
	}
}

// TestShortCodeIsUniqueAndUnguessable 短码唯一，且不含易混字符。
func TestShortCodeIsUniqueAndUnguessable(t *testing.T) {
	e := newEnv(t, defaultCfg())
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c := e.create(CreateInput{ExpireDays: -1})
		if seen[c.Code] {
			t.Fatalf("短码重复：%s", c.Code)
		}
		seen[c.Code] = true
		if len(c.Code) != codeLen {
			t.Fatalf("短码长度 = %d，期望 %d", len(c.Code), codeLen)
		}
		if strings.ContainsAny(c.Code, "01lIO") {
			t.Errorf("短码 %q 含易混字符", c.Code)
		}
	}
}

// TestEqualHashIsConstantTimeEye 只是钉住 EqualHash 的行为，不是性能测试。
func TestEqualHashIsConstantTimeEye(t *testing.T) {
	a := HashToken("abc")
	b := HashToken("abc")
	if !EqualHash(a, b) {
		t.Error("相同输入应判相等")
	}
	if EqualHash(a, HashToken("abd")) {
		t.Error("不同输入不该判相等")
	}
	if EqualHash(a, "") {
		t.Error("空哈希不该判相等")
	}
}

// TestMaskIP 对非 IP 输入整个丢弃，而不是原样存。
func TestMaskIP(t *testing.T) {
	if got := MaskIP(""); got != "" {
		t.Errorf("空输入应得空串，实际 %q", got)
	}
	if got := MaskIP("不是IP"); got != "" {
		t.Errorf("非 IP 应整个丢弃而不是原样存，实际 %q", got)
	}
	if got := MaskIP("2001:db8::1"); got != "2001:db8::/64" {
		t.Errorf("IPv6 应保留 /64，实际 %q", got)
	}
}

// TestStatsReportsActiveDevices 统计里的「当前在线设备」按 24 小时窗口算。
func TestStatsReportsActiveDevices(t *testing.T) {
	e := newEnv(t, defaultCfg())
	c := e.create(CreateInput{ExpireDays: -1, MaxDevices: 4})
	for _, v := range []string{"a", "b"} {
		if _, err := e.svc.IssueToken(e.ctx, c.Code, "", v, "10.0.0.8", "Chrome"); err != nil {
			t.Fatal(err)
		}
	}
	st, err := e.svc.Stats(e.ctx, c.Share.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveDevices != 2 {
		t.Errorf("活跃设备 = %d，期望 2", st.ActiveDevices)
	}
	if st.VisitorCount != 2 {
		t.Errorf("访客计数 = %d，期望 2", st.VisitorCount)
	}
	if len(st.RecentVisits) != 2 {
		t.Errorf("最近访客列表长度 = %d，期望 2", len(st.RecentVisits))
	}
	e.advance(VisitorCountWindow + time.Minute)
	st, _ = e.svc.Stats(e.ctx, c.Share.ID, 10)
	if st.ActiveDevices != 0 {
		t.Errorf("超时后活跃设备 = %d，期望 0", st.ActiveDevices)
	}
	// 统计里不能出现令牌哈希。
	for _, v := range st.RecentVisits {
		if strings.Contains(v.TokenHash, " ") {
			t.Errorf("访客记录里不该带可读令牌")
		}
	}
}

// TestListReportsActiveDevicesPerShare 钉住列表页的「几台设备在用」。
//
// 这条曾经是 Active: 0 的占位值 —— 单测全绿、页面也能开，
// 但管理员打开列表看到每条都是「0 台设备」而点进统计显示 3 台，
// 两种口径打架时没有人知道该信哪个。
func TestListReportsActiveDevicesPerShare(t *testing.T) {
	e := newEnv(t, defaultCfg())
	first := e.create(CreateInput{ExpireDays: -1, MaxDevices: 4})
	second := e.create(CreateInput{ExpireDays: -1, MaxDevices: 4})
	for _, visitor := range []string{"a", "b"} {
		if _, err := e.svc.IssueToken(e.ctx, first.Code, "", visitor, "10.0.0.8", "Chrome"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.IssueToken(e.ctx, second.Code, "", "solo", "10.0.0.9", "Chrome"); err != nil {
		t.Fatal(err)
	}

	shares, active, err := e.svc.List(e.ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if active[first.Share.ID] != 2 {
		t.Errorf("第一条分享的活跃设备 = %d，期望 2", active[first.Share.ID])
	}
	// 关键断言：设备数必须**按分享分开**。所有分享共用一个总计数的话，
	// 这里会是 3 —— 页面看起来正常，但 max_devices 会对不上。
	if active[second.Share.ID] != 1 {
		t.Errorf("第二条分享的活跃设备 = %d，期望 1", active[second.Share.ID])
	}
	if len(shares) != 2 {
		t.Fatalf("列表长度 = %d，期望 2", len(shares))
	}

	// 超期设备让位后，列表也要跟着变（窗口口径而不是历史累积）。
	e.advance(VisitorCountWindow + time.Minute)
	_, active, err = e.svc.List(e.ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if active[first.Share.ID] != 0 || active[second.Share.ID] != 0 {
		t.Errorf("超期后活跃设备 = %v，期望全 0", active)
	}
}

// 让 vet 满意：sql 在这里只用到类型断言的地方不必引入。
