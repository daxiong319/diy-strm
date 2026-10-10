package tgbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- 测试脚手架 ----

type captured struct {
	ChatID int64
	Text   string
}

type harness struct {
	svc    *Service
	sent   *[]captured
	nowFn  func() time.Time
	runner *fakeRunner
	search *fakeSearch
}

func (h *harness) replies() string {
	var b strings.Builder
	for _, c := range *h.sent {
		b.WriteString(c.Text)
		b.WriteString("\n--\n")
	}
	return b.String()
}

func (h *harness) last() string {
	if len(*h.sent) == 0 {
		return ""
	}
	return (*h.sent)[len(*h.sent)-1].Text
}

type fakeRunner struct {
	started     []Job
	cancelCalls []string
	cancelOK    bool
	startErr    error
}

func (f *fakeRunner) Start(ctx context.Context, actor Actor, kind string, payload string) (string, error) {
	if f.startErr != nil {
		return "", f.startErr
	}
	id := "task-" + itoa(int64(len(f.started)+1))
	f.started = append(f.started, Job{ID: id, Kind: kind, Actor: actor})
	return id, nil
}

func (f *fakeRunner) Cancel(ctx context.Context, actor Actor, taskID string) (bool, error) {
	f.cancelCalls = append(f.cancelCalls, taskID)
	return f.cancelOK, nil
}

type fakeSearch struct {
	hits []SearchHit
	err  error
}

func (f *fakeSearch) Search(ctx context.Context, keyword string, limit int) ([]SearchHit, error) {
	return f.hits, f.err
}

func testCfg() Config {
	return Config{
		Enabled:      true,
		Token:        "tok",
		AllowedUsers: []int64{1001},
		AllowedChats: []int64{-100500},
	}
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	sent := &[]captured{}
	h := &harness{
		sent:   sent,
		nowFn:  func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		runner: &fakeRunner{cancelOK: true},
		search: &fakeSearch{},
	}
	svc := New(Options{
		Config: cfg,
		Runner: h.runner,
		Search: h.search,
		Strm:   &fakeStrm{},
		Now:    h.nowFn,
		Log:    &nopLogger{},
	})
	svc.SetSuperLookup(func(ctx context.Context, msg *Message) bool {
		return msg.From.ID == 1001 // 测试里把白名单用户当超管
	})
	svc.pushOverride = func(ctx context.Context, chatID int64, text string) error {
		*sent = append(*sent, captured{ChatID: chatID, Text: text})
		return nil
	}
	h.svc = svc
	return h
}

type nopLogger struct{}

func (n *nopLogger) Debugf(string, ...any) {}
func (n *nopLogger) Warnf(string, ...any)  {}

type fakeStrm struct {
	got []string
}

func (f *fakeStrm) BotRunStrm(ctx context.Context, actor Actor, category string) (string, error) {
	f.got = append(f.got, category)
	return "strm-1", nil
}

func msg(chatID int64, chatType string, userID int64, text string) Update {
	return Update{UpdateID: 1, Message: &Message{
		MessageID: 1,
		Chat:      Chat{ID: chatID, Type: chatType},
		From:      User{ID: userID, Username: "u"},
		Text:      text,
	}}
}

// ---- 验收 1：私聊 /help 返回命令列表 ----

func TestPrivateHelpListsCommands(t *testing.T) {
	h := newHarness(t, testCfg())
	if !h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/help")) {
		t.Fatal("/help 没有被处理")
	}
	out := h.last()
	for _, want := range []string{"/help", "/search", "/organize", "/cancel", "【仅管理员】"} {
		if !strings.Contains(out, want) {
			t.Errorf("/help 输出里没有 %q：\n%s", want, out)
		}
	}
}

// ---- 验收 2：/search ----

func TestSearchReturnsResults(t *testing.T) {
	h := newHarness(t, testCfg())
	h.search.hits = []SearchHit{
		{Title: "流浪地球", Source: "TG", Size: "3.2GB", ShareURL: "https://115.com/s/abc"},
	}
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/search 流浪地球"))
	out := h.last()
	if !strings.Contains(out, "流浪地球") || !strings.Contains(out, "115.com/s/abc") {
		t.Fatalf("/search 输出不对：\n%s", out)
	}
}

func TestSearchWithoutKeywordShowsUsage(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/search"))
	if !strings.Contains(h.last(), "用法：/search") {
		t.Fatalf("无参数时应给用法：\n%s", h.last())
	}
}

// ---- 验收 3：/sub 只有超管 ----

type fakeSubscriber struct {
	got []string
}

func (f *fakeSubscriber) BotAddSubscription(ctx context.Context, actor Actor, title string) (string, error) {
	f.got = append(f.got, title)
	return "sub-1", nil
}

func TestSubIsSuperAdminOnly(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.SetSuperLookup(func(ctx context.Context, m *Message) bool { return false })
	sub := &fakeSubscriber{}
	h.svc.deps.Subscriber = sub

	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/sub 流浪地球"))
	if len(sub.got) != 0 {
		t.Fatal("非超管建了订阅")
	}
	if !strings.Contains(h.last(), "只有管理员可以使用") {
		t.Fatalf("非超管应收到权限提示，实际：\n%s", h.last())
	}

	h.svc.SetSuperLookup(func(ctx context.Context, m *Message) bool { return true })
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/sub 流浪地球"))
	if len(sub.got) != 1 || sub.got[0] != "流浪地球" {
		t.Fatalf("超管应能建订阅，实际 %v", sub.got)
	}
}

// ---- 验收 4：/organize 异步返回 task_id ----

func TestOrganizeReturnsTaskIDImmediately(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/organize 每周整理"))
	out := h.last()
	if !strings.Contains(out, "task-1") {
		t.Fatalf("/organize 应返回 task_id：\n%s", out)
	}
	if !strings.Contains(out, "/cancel task-1") {
		t.Fatalf("/organize 应告诉用户怎么取消：\n%s", out)
	}
}

func TestStrmReturnsTaskIDAndHonoursCategory(t *testing.T) {
	h := newHarness(t, testCfg())
	fs := h.svc.deps.Strm.(*fakeStrm)
	h.svc.deps.Categories = &fakeCategories{list: []string{"国产剧", "综艺"}}

	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/strm 国产剧"))
	if len(fs.got) != 1 || fs.got[0] != "国产剧" {
		t.Fatalf("分类参数没传下去：%v", fs.got)
	}
	if !strings.Contains(h.last(), "strm-1") {
		t.Fatalf("/strm 应返回 task_id：\n%s", h.last())
	}

	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/strm 不存在的分类"))
	if !strings.Contains(h.last(), "没有名为") {
		t.Fatalf("非法分类应被挡下并列出可用分类：\n%s", h.last())
	}
	if len(fs.got) != 1 {
		t.Fatalf("非法分类不应触发执行，实际执行 %d 次", len(fs.got))
	}
}

type fakeCategories struct{ list []string }

func (f *fakeCategories) BotCategories(ctx context.Context) ([]string, error) { return f.list, nil }

// ---- 验收 5：白名单外的用户被拒 ----

func TestUnknownPrivateUserIsRejected(t *testing.T) {
	h := newHarness(t, testCfg())
	handled := h.svc.HandleUpdate(context.Background(), msg(1002, "private", 1002, "/help"))
	if handled {
		t.Error("白名单外用户的命令被处理了")
	}
	if len(*h.sent) != 0 {
		t.Errorf("私聊里对陌生用户应该静默（回一句等于告诉他 Bot 在），实际回了：\n%s", h.replies())
	}
}

// ---- 验收 6：白名单外的群发链接不触发转存 ----

type fakeLink struct {
	starts []string
}

func (f *fakeLink) StartLinkTransfer(ctx context.Context, actor Actor, link string) (string, error) {
	f.starts = append(f.starts, link)
	return "xfer-1", nil
}
func (f *fakeLink) CancelLinkTransfer(ctx context.Context, actor Actor, taskID string) (bool, error) {
	return true, nil
}

func TestLinkInUnlistedGroupDoesNothing(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.cfg.GroupLinkEnabled = true
	link := &fakeLink{}
	h.svc.deps.Link = link

	h.svc.HandleUpdate(context.Background(), msg(-100999, "supergroup", 1001, "https://115.com/s/abc"))
	if len(link.starts) != 0 {
		t.Fatalf("白名单外的群不应触发转存，实际 %v", link.starts)
	}
	if len(*h.sent) != 0 {
		t.Errorf("群里应静默拒绝，实际回了：\n%s", h.replies())
	}
}

func TestLinkInListedGroupFromUnlistedUserIsIgnored(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.cfg.GroupLinkEnabled = true
	link := &fakeLink{}
	h.svc.deps.Link = link

	// 群在白名单里，但发消息的人不在。
	h.svc.HandleUpdate(context.Background(), msg(-100500, "supergroup", 9999, "https://115.com/s/abc"))
	if len(link.starts) != 0 {
		t.Fatalf("群里非白名单用户发链接不应转存，实际 %v", link.starts)
	}

	// 同样的人，在白名单里，就该能。
	h.svc.HandleUpdate(context.Background(), msg(-100500, "supergroup", 1001, "https://115.com/s/abc"))
	if len(link.starts) != 1 {
		t.Fatalf("白名单用户在放行的群里发链接应转存，实际 %v", link.starts)
	}
}

// ---- 验收 7：隐私模式 ----

func TestGroupLinkRequiresBothSwitchAndPrivacyMode(t *testing.T) {
	h := newHarness(t, testCfg())
	link := &fakeLink{}
	h.svc.deps.Link = link

	// 群链接开关关着 → 不响应（Telegram 隐私模式下本来就收不到这条消息，
	// 这里防的是「用户关了开关但 Bot 其实收得到」的情况）。
	h.svc.HandleUpdate(context.Background(), msg(-100500, "supergroup", 1001, "https://115.com/s/abc"))
	if len(link.starts) != 0 {
		t.Fatalf("群链接开关关着就不该转存，实际 %v", link.starts)
	}

	h.svc.cfg.GroupLinkEnabled = true
	h.svc.HandleUpdate(context.Background(), msg(-100500, "supergroup", 1001, "https://115.com/s/abc"))
	if len(link.starts) != 1 {
		t.Fatalf("两个开关都开就该转存，实际 %v", link.starts)
	}
}

// ---- 验收 8：24 条命令都有分支 ----

func TestEveryMuvyoCommandHasABranch(t *testing.T) {
	h := newHarness(t, testCfg())
	names := h.svc.registry.AllNames()
	// muvyo 的 24 条命令全集（含 Bot 自身的 /setprivacy）。
	want := []string{"help", "share", "newshare", "ed2k", "download", "sync", "strm",
		"status", "upgrade", "duplicate", "recognize", "organize", "search", "sub",
		"douban", "homepage", "emby", "monitor", "lifeevent", "update", "cancel",
		"setprivacy"}
	if len(want) != 22 {
		t.Fatalf("命令清单本身写错了：%d 条", len(want))
	}
	for _, name := range want {
		spec, ok := h.svc.registry.Lookup(name)
		if !ok {
			t.Errorf("命令 /%s 没有登记", name)
			continue
		}
		if spec.Supported && spec.Handler == nil {
			t.Errorf("命令 /%s 标了支持却没有 handler", name)
		}
		if !spec.Supported && spec.Summary == "" {
			t.Errorf("命令 /%s 标了不支持但没写清作用", name)
		}
	}
	if len(names) < len(want) {
		t.Errorf("命令表只有 %d 条", len(names))
	}
}

func TestUnsupportedCommandSaysSoInsteadOfSilence(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/douban"))
	out := h.last()
	if !strings.Contains(out, "暂未支持") {
		t.Fatalf("未实现的命令必须回「暂未支持」，实际：\n%s", out)
	}
}

func TestUnknownCommandIsDistinctFromUnsupported(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/nosuchcmd"))
	out := h.last()
	if !strings.Contains(out, "不认识") {
		t.Fatalf("不存在的命令应回「不认识」，实际：\n%s", out)
	}
	if strings.Contains(out, "暂未支持") {
		t.Fatal("「不认识」与「暂未支持」必须区分：前者是命令写错，后者是功能没做")
	}
}

// ---- 验收 9：/cancel ----

func TestCancelOwnsTask(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/organize"))
	taskID := h.runner.started[0].ID

	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/cancel "+taskID))
	if len(h.runner.cancelCalls) != 1 || h.runner.cancelCalls[0] != taskID {
		t.Fatalf("/cancel 没有把任务交给 runner：%v", h.runner.cancelCalls)
	}
	if !strings.Contains(h.last(), "已取消") {
		t.Fatalf("应回「已取消」：\n%s", h.last())
	}
}

func TestCancelRefusesForeignTask(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/organize"))
	taskID := h.runner.started[0].ID

	// 换一个**同样在白名单里**的私聊身份，同一个 task id。
	h.svc.cfg.AllowedUsers = append(h.svc.cfg.AllowedUsers, 2002)
	h.svc.HandleUpdate(context.Background(), msg(2002, "private", 2002, "/cancel "+taskID))
	if len(h.runner.cancelCalls) != 0 {
		t.Fatal("别人也能取消我的任务")
	}
	if !strings.Contains(h.last(), "不是") {
		t.Fatalf("应说明归属不符：\n%s", h.last())
	}
}

func TestCancelUnknownTaskTellsTruth(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/cancel task-nope"))
	if len(h.runner.cancelCalls) != 0 {
		t.Fatal("不存在的任务不该触达 runner")
	}
	if strings.Contains(h.last(), "已取消") {
		t.Fatalf("不存在的任务不能谎称已取消：\n%s", h.last())
	}
	if !strings.Contains(h.last(), "没有这个任务号") {
		t.Fatalf("应如实说查不到：\n%s", h.last())
	}
}

func TestCancelTwiceIsReportedNotSilentlyRepeated(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/organize"))
	taskID := h.runner.started[0].ID
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/cancel "+taskID))
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/cancel "+taskID))
	if len(h.runner.cancelCalls) != 1 {
		t.Fatalf("重复取消应被本地挡住，实际调了 runner %d 次", len(h.runner.cancelCalls))
	}
	if !strings.Contains(h.last(), "已经取消过") {
		t.Fatalf("应说明已取消过：\n%s", h.last())
	}
}

// ---- 参数解析 ----

func TestParseTextHandlesBotMention(t *testing.T) {
	p, ok := parseText("/strm@MyBot 国产剧")
	if !ok {
		t.Fatal("带 @提及的命令没被识别")
	}
	if p.Command != "strm" {
		t.Fatalf("命令名应剥掉 @提及，实际 %q", p.Command)
	}
	if p.Mention != "MyBot" {
		t.Fatalf("提及没被保留：%q", p.Mention)
	}
	if len(p.Args) != 1 || p.Args[0] != "国产剧" {
		t.Fatalf("参数没被正确切分：%v", p.Args)
	}
}

func TestParseTextRejectsPlainText(t *testing.T) {
	for _, s := range []string{"", "hello", "/", "看看这个"} {
		if _, ok := parseText(s); ok {
			t.Errorf("%q 不该被当成命令", s)
		}
	}
}

func TestLookupIsCaseInsensitiveAndStripsSlash(t *testing.T) {
	h := newHarness(t, testCfg())
	if _, ok := h.svc.registry.Lookup("/HELP"); !ok {
		t.Error("命令名应大小写不敏感")
	}
	if _, ok := h.svc.registry.Lookup(" h "); !ok {
		t.Error("命令名应容忍空白与前导斜杠")
	}
}

// ---- 链路完整性 ----

func TestMentionedCommandReachesHandlerInsteadOfNotSupported(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.SetSuperLookup(func(ctx context.Context, m *Message) bool { return true })
	sub := &fakeSubscriber{}
	h.svc.deps.Subscriber = sub
	h.svc.HandleUpdate(context.Background(), msg(-100500, "supergroup", 1001, "/sub@MyBot 流浪地球"))
	if len(sub.got) != 1 || sub.got[0] != "流浪地球" {
		t.Fatalf("带 @提及的命令没走到 handler：%v", sub.got)
	}
	out := h.last()
	if strings.Contains(out, "暂未支持") {
		t.Fatalf("群里带 @提及的命令被误判成未支持：\n%s", out)
	}
}

func TestEditedMessageIsNotReRun(t *testing.T) {
	h := newHarness(t, testCfg())
	u := msg(1001, "private", 1001, "/organize")
	u.EditedMessage = u.Message
	u.Message = nil
	h.svc.HandleUpdate(context.Background(), u)
	if len(h.runner.started) != 0 {
		t.Fatalf("编辑过的消息仍会触发一次新的整理任务（实际 %d 次）", len(h.runner.started))
	}
	if len(*h.sent) != 0 {
		t.Errorf("编辑过的消息不该有任何回应：\n%s", h.replies())
	}
}

func TestChannelPostIsHandled(t *testing.T) {
	h := newHarness(t, testCfg())
	u := msg(1001, "private", 1001, "/status")
	u.Message = nil
	u.ChannelPost = u.Message
	u.ChannelPost = &Message{Chat: Chat{ID: -100500, Type: "channel"}, From: User{ID: 1001}, Text: "/status"}
	h.svc.HandleUpdate(context.Background(), u)
	if strings.TrimSpace(h.last()) == "" {
		t.Fatal("频道消息应当被处理（订阅转发的场景）")
	}
}

// ---- 链接提取 ----

func TestExtractTransferLinkOnlyTakesNetdiskShares(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"看看这个 https://115.com/s/abc?code=1234", "https://115.com/s/abc?code=1234", true},
		{"magnet:?xt=urn:btih:abc", "magnet:?xt=urn:btih:abc", true},
		{"https://example.com/not-a-share", "", false},
		{"今天天气不错", "", false},
		{"https://pan.baidu.com/s/xyz 提取码 abcd", "https://pan.baidu.com/s/xyz", true},
	}
	for _, c := range cases {
		got, ok := ExtractTransferLink(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ExtractTransferLink(%q) = (%q,%v)，期望 (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// ---- 配置安全 ----

func TestEmptyWhitelistRejectsEverything(t *testing.T) {
	cfg := Config{Enabled: true, Token: "tok"}
	if cfg.Ready() {
		t.Fatal("空白名单的 Bot 不该 Ready")
	}
	h := newHarness(t, cfg)
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/help"))
	if !strings.Contains(h.last(), "白名单") {
		t.Fatalf("空白名单应给出明确原因：\n%s", h.last())
	}
}

func TestDisabledBotExplainsItself(t *testing.T) {
	cfg := testCfg()
	cfg.Enabled = false
	h := newHarness(t, cfg)
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/help"))
	if !strings.Contains(h.last(), "未启用") {
		t.Fatalf("关掉开关后应说明原因：\n%s", h.last())
	}
}

func TestParseIDListKeepsNegativeChatIDs(t *testing.T) {
	got := ParseIDList("1001, -100500 ,abc,,1001")
	want := []int64{1001, -100500}
	if len(got) != len(want) {
		t.Fatalf("解析结果 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("解析结果 %v，期望 %v", got, want)
		}
	}
}

// ---- webhook ----

func TestHandlerReturns404WhenDisabled(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.cfg.Enabled = false
	rec := &statusRecorder{code: 0}
	h.svc.Handler("secret").ServeHTTP(rec, newRequest(`{"update_id":1}`))
	if rec.code != 404 {
		t.Fatalf("未启用时 webhook 应 404，实际 %d", rec.code)
	}
}

func TestHandlerRejectsWrongSecretToken(t *testing.T) {
	h := newHarness(t, testCfg())
	rec := &statusRecorder{}
	req := newRequest(`{"update_id":1}`)
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong")
	h.svc.Handler("right").ServeHTTP(rec, req)
	if rec.code != 401 {
		t.Fatalf("错误的 secret token 应 401，实际 %d", rec.code)
	}
	if len(*h.sent) != 0 {
		t.Error("鉴权失败不应触发任何处理")
	}
}

func TestHandlerAlwaysReturns200AfterProcessing(t *testing.T) {
	h := newHarness(t, testCfg())
	rec := &statusRecorder{}
	req := newRequest(`{"update_id":1,"message":{"message_id":1,"chat":{"id":1001,"type":"private"},"from":{"id":1001},"text":"/help"}}`)
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "right")
	h.svc.Handler("right").ServeHTTP(rec, req)
	if rec.code != 200 {
		t.Fatalf("处理成功应 200，实际 %d", rec.code)
	}
	if len(*h.sent) == 0 {
		t.Fatal("webhook 应真的走完了处理链路")
	}
}

func TestSetPrivacyExplainsBotFatherNotFakesIt(t *testing.T) {
	h := newHarness(t, testCfg())
	h.svc.HandleUpdate(context.Background(), msg(1001, "private", 1001, "/setprivacy"))
	out := h.last()
	if !strings.Contains(out, "BotFather") || !strings.Contains(out, "隐私模式") {
		t.Fatalf("/setprivacy 应说明去哪开：\n%s", out)
	}
	if strings.Contains(out, "已关闭") || strings.Contains(out, "已开启") {
		t.Fatalf("Bot 不能自行改隐私模式，回复里不能出现「已关闭/已开启」：\n%s", out)
	}
}

// ---- webhook 测试辅助 ----

type statusRecorder struct {
	code int
	hdr  http.Header
	body strings.Builder
}

func (r *statusRecorder) Header() http.Header {
	if r.hdr == nil {
		r.hdr = http.Header{}
	}
	return r.hdr
}
func (r *statusRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *statusRecorder) WriteHeader(code int)        { r.code = code }

func newRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/tg/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestHandlerRefusesWhenNoSecretIsRegistered 钉住「没配期望值就等于不鉴权」这条。
//
// 这条是写装配层时才想清楚的后果：secret 由本服务生成、只存内存，
// 一旦某条路径只把请求头原样传回来（曾经真发生过），就等于自己对自己比较，
// 任何人都能调通 —— 而症状是「Bot 看起来在工作，只是任何人都能用我的网盘」。
func TestHandlerRefusesWhenNoSecretIsRegistered(t *testing.T) {
	h := newHarness(t, testCfg())
	rec := &statusRecorder{}
	req := newRequest(`{"update_id":1}`)
	// 请求头带了一个值，期望值却是空的：绝不能因为「两边看起来一样」就放行。
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "anything")
	h.svc.Handler("").ServeHTTP(rec, req)
	if rec.code != 401 {
		t.Fatalf("没有登记 secret 时必须 401，实际 %d", rec.code)
	}
	if len(*h.sent) != 0 {
		t.Error("鉴权失败不应触发任何处理")
	}
}

// TestNewRegistersARandomWebhookSecret 证明 secret 不是常量、也不是空串。
func TestNewRegistersARandomWebhookSecret(t *testing.T) {
	h := newHarness(t, testCfg())
	s1 := h.svc.WebhookSecret()
	if len(s1) < 32 {
		t.Fatalf("webhook secret 长度不足以防猜：%d", len(s1))
	}
	h2 := newHarness(t, testCfg())
	if h2.svc.WebhookSecret() == s1 {
		t.Error("两个实例的 webhook secret 相同 —— 它必须是每次启动重新生成的")
	}
}
