package notifychannel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/eventbus"
)

// 模板场景字段（{{.Scene}} / {{.SceneLabel}}）的用例。
//
// 为什么单独一个文件而不是并进 scene_retry_test.go：这里断言的不是
// 「通知有没有发出去」，而是**发出去的那条长什么样**。场景过滤用例即使
// 全绿，也可能在 dispatcher 里把 Scene 留空而不被发现 —— 过滤和注入是
// 两条独立的路径，前者管「发不发」，后者管「发的内容里有没有场景名」。
//
// 断言方式刻意选「起一个真的 httptest 服务器收 body」而不是直接调
// simpleTemplate：注入链路有两处拼接点（dispatcher 填 Message、
// simpleTemplate 展开），只测后者的话，上游忘了填字段这类 bug 会假绿。

// capturedSend 抓一条投递到底层的请求体。
type capturedSend struct {
	mu       sync.Mutex
	bodies   []string
	statuses []int
}

func (c *capturedSend) record(body string, status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, body)
	c.statuses = append(c.statuses, status)
}

func (c *capturedSend) last() (string, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) == 0 {
		return "", 0, false
	}
	i := len(c.bodies) - 1
	return c.bodies[i], c.statuses[i], true
}

// serveWebhook 起一个把所有投递记下来的 webhook 服务器。
func serveWebhook(t *testing.T, cap *capturedSend) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		cap.record(string(buf[:n]), http.StatusOK)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSceneFieldsInjectedIntoWebhookTemplate 验收「模板变量注入 scene 相关字段」。
//
// 走完整链路：eventbus 事件 → dispatcher 填 Message → sendWebhook 展开模板 →
// 真的 HTTP 请求体。中间任何一环漏填，body 里就还是字面的 {{.Scene}}。
func TestSceneFieldsInjectedIntoWebhookTemplate(t *testing.T) {
	cases := []struct {
		name         string
		category     string
		wantScene    string
		wantSceneLbl string
	}{
		{"上传场景", domain.NotificationCategoryUpload, "upload", "上传"},
		{"STRM 场景", "strm_scan_warn", "strm", "STRM"},
		{"订阅场景", domain.NotificationCategoryCas, "subscription", "订阅"},
		{
			// 未归类的分类恒定拿到空串，而不是 "unknown" 或字面量。
			name: "未归类分类给空串", category: domain.NotificationCategoryAuth,
			wantScene: "", wantSceneLbl: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap := &capturedSend{}
			srv := serveWebhook(t, cap)
			publish := testBus(t, []*domain.NotifyChannel{{
				ID: 1, Type: ChannelWebhook, Name: "运维告警", Enabled: true,
				Config: `{"url":"` + srv.URL + `","method":"POST",` +
					`"body":"scene={{.Scene}} label={{.SceneLabel}} title={{.Title}}"}`,
			}}, nil)

			publish(eventbus.NotificationCreated{
				Level: "info", Category: tc.category,
				Title: "标题", Message: "正文",
			})

			var body string
			mustEventually(t, func() bool {
				b, _, ok := cap.last()
				if ok {
					body = b
				}
				return ok
			}, "webhook 没收到投递")

			if !strings.Contains(body, "scene="+tc.wantScene) {
				t.Errorf("body=%q，期望含 scene=%q", body, tc.wantScene)
			}
			if !strings.Contains(body, "label="+tc.wantSceneLbl) {
				t.Errorf("body=%q，期望含 label=%q", body, tc.wantSceneLbl)
			}
			// 注入后不得残留未展开的占位符：simpleTemplate 认不出的字段会
			// 原样保留，所以「什么都没注入」和「注入了但拼错了键名」长得一样。
			if strings.Contains(body, "{{") {
				t.Errorf("body=%q 仍有未展开的模板占位符", body)
			}
			if !strings.Contains(body, "title=标题") {
				t.Errorf("body=%q，老字段 {{.Title}} 被新字段挤掉了", body)
			}
		})
	}
}

// TestUnmappedSceneTemplateRendersEmptyNotLiteral 回归防线：
// 未归类通知的模板里如果写了 {{.Scene}}，必须渲染成空串。
//
// 刻意不用 text/template —— 那套会把未定义字段渲染成 "<no value>"，
// 用户收到的是一封带机器味的告警信，而不是"这个通知没有场景"。
func TestUnmappedSceneTemplateRendersEmptyNotLiteral(t *testing.T) {
	got := simpleTemplate("[{{.Scene}}|{{.SceneLabel}}]", Message{
		Title: "t", Content: "c", Scene: "", SceneLabel: "",
	})
	if want := "[|]"; got != want {
		t.Errorf("未归类模板渲染 = %q，期望 %q（空串而不是 no value 或字面量）", got, want)
	}
}

// TestKnownFieldsSurviveSceneInjection 老模板不能因为新增字段而变行为。
func TestKnownFieldsSurviveSceneInjection(t *testing.T) {
	msg := Message{
		Title: "标题", Content: "正文", Tone: "warn",
		Scene: "backup", SceneLabel: "备份",
	}
	got := simpleTemplate("{{.Title}}/{{.Content}}/{{.Tone}}/{{.Scene}}/{{.SceneLabel}}", msg)
	want := "标题/正文/warn/backup/备份"
	if got != want {
		t.Errorf("simpleTemplate = %q，期望 %q", got, want)
	}
	// 正文里恰好出现 "Title" 字样时不能被裸字段名替换掉。
	body := "字段说明：Title 与 Content"
	if out := simpleTemplate(body, msg); out != body {
		t.Errorf("正文里的裸字段名被替换了：%q → %q", body, out)
	}
}

// TestRetryRedriveCarriesSceneFields 补发路径必须渲染出与首次投递一致的场景。
//
// ⚠️ 这是两条独立路径：dispatcher 从 e.Category 现算场景，retryWorker 从
// outbox 快照里的 EventScene 读场景。只测其中一条的话，另一条忘了填字段
// 会假绿 —— 而症状是「补发出去的通知里场景字段是空的」，只有人工比对
// 才发现得了。
func TestRetryRedriveCarriesSceneFields(t *testing.T) {
	repo := newMemoryRetryRepo()
	var mu sync.Mutex
	var msgs []Message
	worker := NewRetryWorker(repo, nil, func(_ context.Context, _, _ string, msg Message) error {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, msg)
		return nil
	})
	// 时钟必须推到入队时刻**之后**：dispatcher 写的是 now+1m 才重投，
	// 而 now 取自真实时间，假时钟停在起始点的话 RunOnce 一条都查不到，
	// 断言会退化成「没发出去所以没有场景字段」的假通过。
	clock := newFakeClock()
	clock.advance(72 * time.Hour)
	worker.SetClock(clock.now, 0)

	// 走真实 enqueue 路径，拿到的是 dispatcher 会写下的那份快照。
	publish := testBus(t, []*domain.NotifyChannel{{
		ID: 1, Type: ChannelWebhook, Name: "运维告警", Enabled: true,
		Config: `{"url":"https://hook.test/notify","method":"POST"}`,
	}}, repo)
	_, restore := stubSender(t, ChannelWebhook, errors.New("webhook HTTP 500"))
	defer restore()
	publish(eventbus.NotificationCreated{
		Level: "info", Category: domain.NotificationCategoryCas,
		Title: "订阅标题", Message: "订阅正文",
	})
	// Publish 是异步入队（eventbus 有消费者队列），必须等它落库。
	var entries []domain.NotifyRetryEntry
	mustEventually(t, func() bool {
		entries = repo.snapshot()
		return len(entries) == 1
	}, "补发队列没有入队记录")
	if entries[0].EventScene != domain.SceneSubscription {
		t.Fatalf("入队快照 EventScene = %q，期望 %q", entries[0].EventScene, domain.SceneSubscription)
	}

	worker.RunOnce(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(msgs) != 1 {
		t.Fatalf("补发投递条数 = %d，期望 1", len(msgs))
	}
	if msgs[0].Scene != "subscription" {
		t.Errorf("补发 Message.Scene = %q，期望 %q", msgs[0].Scene, "subscription")
	}
	if msgs[0].SceneLabel == "" {
		t.Error("补发 Message.SceneLabel 为空，管理台/补发邮件里会缺场景名")
	}
	if got := simpleTemplate("{{.Scene}}/{{.SceneLabel}}", msgs[0]); !strings.Contains(got, "subscription/") {
		t.Errorf("补发模板渲染 = %q，期望以 subscription/ 开头", got)
	}
}

// TestRetryRedriveUnmappedSceneStaysEmpty 补发路径同样不能给未归类通知编场景名。
func TestRetryRedriveUnmappedSceneStaysEmpty(t *testing.T) {
	repo := newMemoryRetryRepo()
	var mu sync.Mutex
	var last Message
	worker := NewRetryWorker(repo, nil, func(_ context.Context, _, _ string, msg Message) error {
		mu.Lock()
		defer mu.Unlock()
		last = msg
		return nil
	})
	clock := newFakeClock()
	clock.advance(72 * time.Hour)
	worker.SetClock(clock.now, 0)

	publish := testBus(t, []*domain.NotifyChannel{{
		ID: 1, Type: ChannelWebhook, Name: "运维告警", Enabled: true,
		Config: `{"url":"https://hook.test/notify","method":"POST"}`,
	}}, repo)
	_, restore := stubSender(t, ChannelWebhook, errors.New("webhook HTTP 500"))
	defer restore()
	publish(eventbus.NotificationCreated{
		Level: "error", Category: domain.NotificationCategoryAuth,
		Title: "登录失效", Message: "账号 token 过期",
	})
	mustEventually(t, func() bool { return len(repo.snapshot()) == 1 }, "补发队列没有入队记录")
	worker.RunOnce(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if last.Scene != "" || last.SceneLabel != "" {
		t.Errorf("未归类通知补发带上了场景 %q/%q，应为空", last.Scene, last.SceneLabel)
	}
	// 占位符必须被消费掉：未归类时结果是空串，不是把 {{.Scene}} 原样吐出去。
	if got := simpleTemplate("|{{.Scene}}|", last); got != "||" {
		t.Errorf("未归类补发渲染 = %q，期望 %q", got, "||")
	}
}
