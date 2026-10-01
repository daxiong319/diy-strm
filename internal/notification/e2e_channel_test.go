package notification_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/eventbus"
	"litepan/internal/notification"
	"litepan/internal/notifychannel"
)

// recordingRepo 是只读的最小 NotifyChannelRepository 实现。
type recordingRepo struct {
	channels []*domain.NotifyChannel
}

func (r *recordingRepo) List(context.Context) ([]*domain.NotifyChannel, error) {
	return r.channels, nil
}

func (r *recordingRepo) ListEnabled(context.Context) ([]*domain.NotifyChannel, error) {
	return r.channels, nil
}

func (r *recordingRepo) Get(context.Context, int64) (*domain.NotifyChannel, error) {
	return nil, nil
}
func (r *recordingRepo) Create(context.Context, *domain.NotifyChannel) (int64, error) { return 0, nil }
func (r *recordingRepo) Update(context.Context, *domain.NotifyChannel) error          { return nil }
func (r *recordingRepo) Delete(context.Context, int64) error                          { return nil }

// notificationRepo 记录站内通知落库情况。
type notificationRepo struct {
	mu    sync.Mutex
	saved []*domain.Notification
}

func (r *notificationRepo) List(context.Context, int, int) ([]*domain.Notification, error) {
	return nil, nil
}
func (r *notificationRepo) UnreadCount(context.Context) (int, error)   { return 0, nil }
func (r *notificationRepo) MarkRead(context.Context, int64) error      { return nil }
func (r *notificationRepo) MarkAllRead(context.Context) (int64, error) { return 0, nil }
func (r *notificationRepo) Delete(context.Context, int64) error        { return nil }
func (r *notificationRepo) DeleteAll(context.Context) (int64, error)   { return 0, nil }
func (r *notificationRepo) DeleteByRef(context.Context, string, int64) (int64, error) {
	return 0, nil
}
func (r *notificationRepo) Create(_ context.Context, n *domain.Notification) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saved = append(r.saved, n)
	return int64(len(r.saved)), nil
}

// TestNotifyReachesExternalChannelEndToEnd 验证项③的核心链路：
//
//	notification.Service.Notify() → 落库 → 发布 NotificationCreated
//	  → notifychannel.Dispatcher → 外部渠道（Telegram HTTP 调用）
//
// 修复前的缺陷是 Notify 只落库、从不发布事件，因此 CAS 自动转存这类
// 「直接调用 Notify」的通知只进站内列表，永远到不了 Telegram。
// 本测试用一个假 Telegram 服务器作为外部渠道的观测点。
func TestNotifyReachesExternalChannelEndToEnd(t *testing.T) {
	type hit struct {
		path string
		body string
	}
	var (
		mu   sync.Mutex
		hits []hit
	)
	fakeTG := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, hit{path: r.URL.Path, body: string(raw)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer fakeTG.Close()

	cfg, err := json.Marshal(map[string]string{
		"api_host":  fakeTG.URL,
		"bot_token": "testtoken",
		"chat_id":   "12345",
	})
	if err != nil {
		t.Fatalf("序列化渠道配置失败：%v", err)
	}
	channels := []*domain.NotifyChannel{{
		ID:      1,
		Name:    "E2E Telegram",
		Type:    "telegram",
		Enabled: true,
		Config:  string(cfg),
	}}

	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = bus.Close(context.Background()) }()

	// 外部渠道 dispatcher 订阅总线并预载渠道缓存。
	dispatcher := notifychannel.NewDispatcher(&recordingRepo{channels: channels}, nil)
	dispatcher.Register(bus)
	dispatcher.Refresh(context.Background())

	repo := &notificationRepo{}
	svc := notification.NewService(notification.Options{
		Repo:     repo,
		Accounts: nil,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	svc.Register(bus)

	// 模拟 CAS 自动转存成功的通知（正是用户抱怨「没反应」的那一条）。
	svc.Notify(context.Background(), "success", "cas", "CAS 清单已自动转存",
		"已收到 仙逆剧场版.cas，识别为「天翼云盘」，已保存到 CAS/仙逆剧场版.cas。", 2, 0)

	if len(repo.saved) != 1 {
		t.Fatalf("通知应落库 1 条，实际 %d 条", len(repo.saved))
	}
	if repo.saved[0].Title != "CAS 清单已自动转存" {
		t.Fatalf("落库标题不符：%q", repo.saved[0].Title)
	}

	// 事件经总线异步投递，轮询等待外部渠道被调用。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(hits)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hits) == 0 {
		t.Fatal("Notify 后外部渠道未被调用：NotificationCreated 事件没有到达 dispatcher")
	}
	if !strings.Contains(hits[0].path, "testtoken") || !strings.Contains(hits[0].path, "sendMessage") {
		t.Fatalf("Telegram 调用路径不符：%q", hits[0].path)
	}
	if !strings.Contains(hits[0].body, "CAS 清单已自动转存") {
		t.Fatalf("外部渠道收到的正文缺少通知标题：%q", hits[0].body)
	}
}
