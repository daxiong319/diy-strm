package wecom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"litepan/internal/inboundbot"
)

// ---- 脚手架 ----

const (
	testToken     = "corp-secret"
	testAESKeyRaw = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG" // 43 字符，企微给的形态
	testUserID    = 1001
	testChatID    = -100500
)

func testAESKey(t *testing.T) string {
	t.Helper()
	if _, err := DecodeAESKey(testAESKeyRaw); err != nil {
		t.Fatalf("测试用的 AESKey 本身不合法：%v", err)
	}
	return testAESKeyRaw
}

type captured struct {
	Text string
}

type fakeRunner struct {
	started     []*inboundbot.Job
	cancelCalls []string
}

func (f *fakeRunner) Start(ctx context.Context, actor inboundbot.Actor, kind, payload string) (string, error) {
	id := "task-" + itoa(len(f.started)+1)
	f.started = append(f.started, &inboundbot.Job{ID: id, Kind: kind, Actor: actor})
	return id, nil
}

func (f *fakeRunner) Cancel(ctx context.Context, actor inboundbot.Actor, taskID string) (bool, error) {
	f.cancelCalls = append(f.cancelCalls, taskID)
	return true, nil
}

type fakeSubscriber struct {
	calls []string
}

func (f *fakeSubscriber) BotAddSubscription(ctx context.Context, actor inboundbot.Actor, title string) (string, error) {
	f.calls = append(f.calls, title)
	return "sub-1", nil
}

type fakeLink struct {
	starts []string
}

func (f *fakeLink) StartLinkTransfer(ctx context.Context, actor inboundbot.Actor, link string) (string, error) {
	f.starts = append(f.starts, link)
	return "link-1", nil
}

func (f *fakeLink) CancelLinkTransfer(ctx context.Context, actor inboundbot.Actor, taskID string) (bool, error) {
	return true, nil
}

type botHarness struct {
	bot     *Bot
	sent    *[]captured
	runner  *fakeRunner
	sub     *fakeSubscriber
	link    *fakeLink
	superID int64
}

func testBotCfg() BotConfig {
	return BotConfig{
		Enabled:      true,
		Token:        testToken,
		AESKey:       testAESKeyRaw,
		AllowedUsers: []int64{testUserID},
		AllowedChats: []int64{testChatID},
	}
}

func newBotHarness(t *testing.T, cfg BotConfig) *botHarness {
	t.Helper()
	h := &botHarness{
		sent:    &[]captured{},
		runner:  &fakeRunner{},
		sub:     &fakeSubscriber{},
		link:    &fakeLink{},
		superID: testUserID,
	}
	h.bot = NewBot(BotOptions{
		Config: cfg,
		Logger: &nopWecomLogger{},
		Now:    func() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) },
	})
	h.bot.deps.Runner = h.runner
	h.bot.deps.Subscriber = h.sub
	h.bot.deps.Link = h.link
	h.bot.SetSuperLookup(func(ctx context.Context, userID int64) bool { return userID == h.superID })
	h.bot.respondOverride = func(ctx context.Context, target ReplyTarget, text string) error {
		*h.sent = append(*h.sent, captured{Text: text})
		return nil
	}
	return h
}

func (h *botHarness) last() string {
	if len(*h.sent) == 0 {
		return ""
	}
	return (*h.sent)[len(*h.sent)-1].Text
}

func (h *botHarness) replies() string {
	var b strings.Builder
	for _, c := range *h.sent {
		b.WriteString(c.Text)
		b.WriteString("\n--\n")
	}
	return b.String()
}

type nopWecomLogger struct{}

func (n *nopWecomLogger) Debugf(string, ...any) {}
func (n *nopWecomLogger) Warnf(string, ...any)  {}

// postMessage 把一条消息加密后按企微的格式发进回调端点。
func (h *botHarness) postMessage(t *testing.T, msg CallbackMessage) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := encodeJSON(msg)
	if err != nil {
		t.Fatalf("编码回调消息失败: %v", err)
	}
	encrypted, err := EncryptMessage(testAESKeyRaw, raw)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	// ⚠️ base64 里的 '+' 会被查询串解析成空格，URL 里必须转义；
	// 签名算的是**转义前**的值（企微那边就是拿解码后的 encrypt 算的）。
	target := "/wecom/callback?timestamp=1700000000&nonce=abc123&encrypt=" +
		url.QueryEscape(encrypted) + "&msg_signature=" +
		ComputeSignature(testToken, "1700000000", "abc123", encrypted)
	req := httptest.NewRequest(http.MethodPost, target, nil)
	rec := httptest.NewRecorder()
	h.bot.Handler().ServeHTTP(rec, req)
	return rec
}

func privateMsg(from string, text string) CallbackMessage {
	return CallbackMessage{
		Cmd: "aibot_msg_callback", MsgID: "m1", ChatType: "single",
		From: CallbackFrom{UserID: from},
		Msg:  CallbackMsg{Type: "text", Text: CallbackText{Content: text}},
	}
}

func groupMsg(chatID, from, text string) CallbackMessage {
	return CallbackMessage{
		Cmd: "aibot_msg_callback", MsgID: "m2", ChatType: "group",
		ChatID: chatID, From: CallbackFrom{UserID: from},
		ResponseURL:  "https://example.invalid/cgi-bin/aibot/response?response_code=RC1",
		ResponseCode: "RC1",
		Msg:          CallbackMsg{Type: "text", Text: CallbackText{Content: text}},
	}
}

// ---- 加解密 ----

func TestEncryptDecryptRoundTrip(t *testing.T) {
	raw, err := EncryptMessage(testAESKeyRaw, `{"a":"中文"}`)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	plain, err := DecryptMessage(testAESKeyRaw, raw)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if string(plain) != `{"a":"中文"}` {
		t.Fatalf("往返后内容变了：%s", plain)
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	raw, err := EncryptMessage(testAESKeyRaw, "hello world")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	// 改动**尾部**（填充长度与填充字节所在的块）。
	// 包头那 16 字节随机数按设计不参与校验（它只是让同一段明文每次密文不同），
	// 所以只有改尾部才能证明「尾部校验真的在跑」——
	// 不校验填充的话，被改 1 位的密文有 1/256 的概率被当成一条正常消息，
	// 而 Bot 入口连着转存与跑整理。
	tampered := []byte(raw)
	if tampered[len(tampered)-1] == 'A' {
		tampered[len(tampered)-1] = 'B'
	} else {
		tampered[len(tampered)-1] = 'A'
	}
	if _, err := DecryptMessage(testAESKeyRaw, string(tampered)); err == nil {
		t.Fatal("被改动过的密文竟然解开了")
	}
}

func TestDecryptRejectsShortAndBadKey(t *testing.T) {
	if _, err := DecryptMessage(testAESKeyRaw, "YWJj"); err == nil {
		t.Fatal("长度不对的密文必须被拒")
	}
	if _, err := DecryptMessage("not-a-key", "YWJjZGVmZ2hpamtsbW5vcA=="); err == nil {
		t.Fatal("非法的 EncodingAESKey 必须被拒")
	}
}

func TestVerifySignatureIgnoresHexCase(t *testing.T) {
	sig := ComputeSignature(testToken, "1", "2", "3")
	if !VerifySignature(testToken, "1", "2", "3", strings.ToUpper(sig)) {
		t.Fatal("hex 大小写不该影响签名比对")
	}
	if VerifySignature(testToken, "1", "2", "3", sig[:len(sig)-1]+"0") {
		t.Fatal("签错了必须被拒")
	}
}

func TestVerifyEchoesPlaintextWithoutQuotes(t *testing.T) {
	h := newBotHarness(t, testBotCfg())
	echostr, err := EncryptMessage(testAESKeyRaw, "verify-me-42")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	sig := ComputeSignature(testToken, "1700000000", "n1", echostr)
	target := "/wecom/callback?timestamp=1700000000&nonce=n1&echostr=" +
		url.QueryEscape(echostr) + "&msg_signature=" + sig
	rec := httptest.NewRecorder()
	h.bot.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 校验返回 %d", rec.Code)
	}
	// 加引号或换行，企微一律判校验失败，而用户看到的是「地址不让保存」。
	if body := rec.Body.String(); body != "verify-me-42" {
		t.Fatalf("echostr 必须原样返回明文，实际 %q", body)
	}
}

// ---- 鉴权 ----

func TestCallbackRejectsBadSignature(t *testing.T) {
	h := newBotHarness(t, testBotCfg())
	req := httptest.NewRequest(http.MethodGet,
		"/wecom/callback?timestamp=1&nonce=n&echostr=x&msg_signature=deadbeef", nil)
	rec := httptest.NewRecorder()
	h.bot.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("签名不对必须 401，实际 %d", rec.Code)
	}
}

func TestCallbackIs404WhenDisabled(t *testing.T) {
	cfg := testBotCfg()
	cfg.Enabled = false
	h := newBotHarness(t, cfg)
	req := httptest.NewRequest(http.MethodGet, "/wecom/callback?msg_signature=xx", nil)
	rec := httptest.NewRecorder()
	h.bot.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未启用时应 404，实际 %d", rec.Code)
	}
}

// ---- 验收 ⑤：只接受配置内的群/来源 ----

func TestGroupNotInWhitelistIsIgnoredSilently(t *testing.T) {
	h := newBotHarness(t, testBotCfg())
	h.postMessage(t, groupMsg("-100999", "1001", "/status"))
	if len(h.runner.started) != 0 {
		t.Fatal("白名单外的群不该触发任何副作用")
	}
	if got := h.last(); got != "" {
		t.Fatalf("白名单外的群不该收到回话，实际：%s", got)
	}
}

func TestPrivateUserNotInWhitelistIsIgnored(t *testing.T) {
	h := newBotHarness(t, testBotCfg())
	h.postMessage(t, privateMsg("2002", "/status"))
	if len(*h.sent) != 0 {
		t.Fatalf("白名单外的私聊人不该收到回话，实际：%s", h.replies())
	}
}

func TestEmptyWhitelistRejectsEverything(t *testing.T) {
	cfg := testBotCfg()
	cfg.AllowedUsers = nil
	cfg.AllowedChats = nil
	h := newBotHarness(t, cfg)
	if d := CheckAccess(cfg, privateMsg("1001", "/status")); d.Allowed {
		t.Fatal("空白名单必须拒绝一切，不是「不限制」")
	}
	h.postMessage(t, privateMsg("1001", "/status"))
	// 空白名单要**说清楚**为什么没人能用：静默回「不限制」的反面
	// 是用户以为 Bot 坏了，而真正的原因（白名单没填）只有他知道。
	if !strings.Contains(h.last(), "白名单") {
		t.Fatalf("空白名单应明确说明原因，实际：\n%s", h.replies())
	}
	if len(h.runner.started) != 0 {
		t.Fatal("空白名单下不得起任务")
	}
}

func TestUnreadableChatIDIsNotTreatedAsPrivate(t *testing.T) {
	cfg := testBotCfg()
	msg := groupMsg("not-a-number", "1001", "/status")
	if d := CheckAccess(cfg, msg); d.Allowed {
		t.Fatal("读不出 chatid 的群消息绝不能按单聊路径放行")
	}
}

func TestGroupLinkNeedsBothSwitchAndListedUser(t *testing.T) {
	base := testBotCfg()
	link := "https://115.com/s/abc"

	// 开关关着：群里的裸链接一律忽略。
	h := newBotHarness(t, base)
	h.postMessage(t, groupMsg("-100500", "1001", link))
	if len(h.link.starts) != 0 {
		t.Fatalf("群链接开关关着就不该转存，实际 %v", h.link.starts)
	}

	// 开关开着但人不在白名单：仍然不转存。
	cfg := base
	cfg.GroupLinkEnabled = true
	h2 := newBotHarness(t, cfg)
	h2.postMessage(t, groupMsg("-100500", "2002", link))
	if len(h2.link.starts) != 0 {
		t.Fatalf("白名单外的人不该让群里的链接转存，实际 %v", h2.link.starts)
	}

	// 两个条件都满足：转存。
	h3 := newBotHarness(t, cfg)
	h3.postMessage(t, groupMsg("-100500", "1001", link))
	if len(h3.link.starts) != 1 {
		t.Fatalf("两个条件都满足就该转存，实际 %v", h3.link.starts)
	}
}

func TestGroupCommandWorksWithoutGroupLinkSwitch(t *testing.T) {
	h := newBotHarness(t, testBotCfg())
	h.postMessage(t, groupMsg("-100500", "1001", "/organize"))
	if len(h.runner.started) != 1 {
		t.Fatalf("白名单群里应能发命令，实际启动 %d 个任务", len(h.runner.started))
	}
	if !strings.Contains(h.last(), "task-1") {
		t.Fatalf("应回 task_id：\n%s", h.last())
	}
}

// ---- 验收 ⑥：普通用户不能触发「加订阅」 ----

func TestSubIsSuperAdminOnly(t *testing.T) {
	cfg := testBotCfg()
	h := newBotHarness(t, cfg)
	h.superID = -1 // 谁都不是超管

	h.postMessage(t, privateMsg("1001", "/sub 沙丘"))
	if len(h.sub.calls) != 0 {
		t.Fatal("非超管不得加订阅")
	}
	if !strings.Contains(h.last(), "管理员") {
		t.Fatalf("应明确说只有管理员能加订阅，实际：\n%s", h.last())
	}

	// 对照：换成超管就能加。
	h2 := newBotHarness(t, cfg)
	h2.superID = testUserID
	h2.postMessage(t, privateMsg("1001", "/sub 沙丘"))
	if len(h2.sub.calls) != 1 {
		t.Fatalf("超管应当能加订阅，实际调用 %d 次", len(h2.sub.calls))
	}
}

func TestSuperOnlyCommandsRejectedForOrdinaryWhitelistedUser(t *testing.T) {
	for _, name := range []string{"download", "sync", "upgrade", "update"} {
		h := newBotHarness(t, testBotCfg())
		h.superID = -1
		h.postMessage(t, privateMsg("1001", "/"+name+" 某个参数"))
		if len(h.runner.started) != 0 {
			t.Fatalf("/%s 非超管不该起任务", name)
		}
		if !strings.Contains(h.last(), "管理员") {
			t.Fatalf("/%s 应回权限不足，实际：\n%s", name, h.last())
		}
	}
}

// ---- 准入在副作用之前 ----

func TestAccessCheckRunsBeforeAnySideEffect(t *testing.T) {
	// 直接打 dispatch 之前的判据：白名单外的人发 /organize，
	// 请求里带的链接也不能被转存 —— 两个副作用必须都被挡住。
	cfg := testBotCfg()
	h := newBotHarness(t, cfg)
	h.postMessage(t, groupMsg("-100777", "2002", "https://115.com/s/abc"))
	if len(h.runner.started) != 0 || len(h.link.starts) != 0 {
		t.Fatalf("未授权来源不得触发任何副作用：task=%v link=%v", h.runner.started, h.link.starts)
	}
}

func TestEnterChatEventRepliesHelpWithoutSideEffect(t *testing.T) {
	h := newBotHarness(t, testBotCfg())
	msg := privateMsg("1001", "")
	msg.EventType = "enter_chat"
	rec := h.postMessage(t, msg)
	if rec.Code != http.StatusOK {
		t.Fatalf("enter_chat 也应恒 200，实际 %d", rec.Code)
	}
	if !strings.Contains(h.last(), "/organize") {
		t.Fatalf("进入会话应回一句命令帮助，实际：\n%s", h.last())
	}
	if len(h.runner.started) != 0 {
		t.Fatal("进入会话事件不该起任务")
	}
}

// ---- 命令解析 ----

func TestCommandMentionIsStripped(t *testing.T) {
	h := newBotHarness(t, testBotCfg())
	h.postMessage(t, groupMsg("-100500", "1001", "@小助手 /organize"))
	if len(h.runner.started) != 1 {
		t.Fatalf("群里 @机器人 /命令 必须能命中，实际启动 %d 个任务：\n%s", len(h.runner.started), h.replies())
	}
}

func TestHelpTitleFollowsPlatform(t *testing.T) {
	h := newBotHarness(t, testBotCfg())
	h.postMessage(t, privateMsg("1001", "/help"))
	out := h.last()
	if !strings.Contains(out, "企业微信机器人") {
		t.Fatalf("抬头应是平台名，实际：\n%s", out)
	}
	if strings.Contains(out, "Telegram") {
		t.Fatalf("共用层硬编码 Telegram 是错的，实际：\n%s", out)
	}
}

func TestUnknownAndUnsupportedAreDistinct(t *testing.T) {
	h := newBotHarness(t, testBotCfg())
	h.postMessage(t, privateMsg("1001", "/nosuchcmd"))
	if !strings.Contains(h.last(), "不认识") {
		t.Fatalf("不存在的命令应回「不认识」，实际：\n%s", h.last())
	}
	h2 := newBotHarness(t, testBotCfg())
	h2.postMessage(t, privateMsg("1001", "/douban"))
	if !strings.Contains(h2.last(), "暂未支持") {
		t.Fatalf("未实现的命令应回「暂未支持」，实际：\n%s", h2.last())
	}
}

func TestSetPrivacyIsNotRegisteredOnWeCom(t *testing.T) {
	// /setprivacy 是 BotFather 的概念，企微没有 —— 注册上去会让用户
	// 以为这个开关在本平台也生效。
	h := newBotHarness(t, testBotCfg())
	if _, ok := h.bot.Registry().Lookup("setprivacy"); ok {
		t.Fatal("企微侧不该登记 /setprivacy")
	}
}

func TestEverySharedCommandHasABranch(t *testing.T) {
	h := newBotHarness(t, testBotCfg())
	for _, spec := range h.bot.Registry().List() {
		if spec.Supported && spec.Handler == nil {
			t.Errorf("/%s 标了支持却没有 handler", spec.Name)
		}
		if !spec.Supported && spec.Summary == "" {
			t.Errorf("/%s 标了不支持却没写清作用", spec.Name)
		}
	}
	if len(h.bot.Registry().AllNames()) < 21 {
		t.Fatalf("命令表只有 %d 条", len(h.bot.Registry().AllNames()))
	}
}

// ---- /cancel 归属 ----

func TestCancelRefusesForeignTask(t *testing.T) {
	cfg := testBotCfg()
	cfg.AllowedUsers = []int64{testUserID, 2002}
	h := newBotHarness(t, cfg)
	h.postMessage(t, privateMsg("1001", "/organize"))
	taskID := h.runner.started[0].ID

	h.postMessage(t, privateMsg("2002", "/cancel "+taskID))
	if len(h.runner.cancelCalls) != 0 {
		t.Fatalf("别人的任务不该被取消：%v", h.runner.cancelCalls)
	}
}
