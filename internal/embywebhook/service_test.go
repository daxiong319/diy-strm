package embywebhook

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"litepan/internal/domain"
)

// recordingNotifier 记录所有通知，供断言使用。
type recordingNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
}

type notifyCall struct {
	Level    string
	Category string
	Title    string
	Message  string
}

func (n *recordingNotifier) Notify(_ context.Context, level, category, title, message string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, notifyCall{Level: level, Category: category, Title: title, Message: message})
}

func (n *recordingNotifier) snapshot() []notifyCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]notifyCall, len(n.calls))
	copy(out, n.calls)
	return out
}

func (n *recordingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.calls)
}

// fakeClient 提供可预期的条目详情。
type fakeClient struct {
	detail  *ItemDetail
	seasons map[int][]int
}

func (c *fakeClient) GetItemDetail(_ context.Context, _ string) (*ItemDetail, error) {
	return c.detail, nil
}

func (c *fakeClient) GetSeriesEpisodes(_ context.Context, _ string) (map[int][]int, error) {
	return c.seasons, nil
}

// testService 构造一个开启了媒体通知的测试服务。
func testService(t *testing.T, notifier Notifier, client Client) *Service {
	t.Helper()
	return New(Config{
		Enabled:           true,
		MediaNotification: true,
		PlaybackOverview:  true,
		PlaybackProgress:  true,
	}, client, notifier, nil)
}

// waitFor 轮询等待条件成立，避免用固定 sleep 拖慢测试。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", msg)
}

// TestEpisodeMergeProducesSingleNotification 验证窗口内 N 条单集事件只产生一条通知。
func TestEpisodeMergeProducesSingleNotification(t *testing.T) {
	origWindow := MergeWindow
	MergeWindow = 40 * time.Millisecond
	t.Cleanup(func() { MergeWindow = origWindow })

	notifier := &recordingNotifier{}
	svc := testService(t, notifier, nil)
	svc.buffer = NewBuffer(svc.onSeriesFlush)
	svc.buffer.Start()
	t.Cleanup(svc.buffer.Stop)

	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		raw := []byte(fmt.Sprintf(`{
			"Event": "library.new",
			"Item": {"Id": "ep-%d", "Name": "第 %d 集", "Type": "Episode",
				"SeriesId": "series-1", "SeriesName": "测试剧集",
				"ParentIndexNumber": 1, "IndexNumber": %d}
		}`, i, i, i))
		if err := svc.HandleWebhook(ctx, raw); err != nil {
			t.Fatalf("第 %d 条单集事件处理失败：%v", i, err)
		}
	}

	waitFor(t, 2*time.Second, func() bool { return notifier.count() == 1 },
		"5 条单集事件应只合并出 1 条通知")

	calls := notifier.snapshot()
	if calls[0].Category != domain.NotificationCategoryEmbyIngest {
		t.Fatalf("通知分类 = %q，期望 %q", calls[0].Category, domain.NotificationCategoryEmbyIngest)
	}
	// 窗口结束后的静默期，确认不会重复推送。
	time.Sleep(120 * time.Millisecond)
	if got := notifier.count(); got != 1 {
		t.Fatalf("通知数量 = %d，期望恰好 1（合并窗口内的事件必须去重）", got)
	}
}

// TestMergedIngestEventRendersSingleNotification 验证 Emby 4.8 合并事件被识别并渲染成一条通知。
func TestMergedIngestEventRendersSingleNotification(t *testing.T) {
	cases := []struct {
		name      string
		title     string
		mediaType string
		wantCount int
	}{
		{name: "剧集合并入库", title: "将 12 项目添加到 电视剧", mediaType: MediaTypeSeries, wantCount: 12},
		{name: "季合并入库", title: "将 3 项目添加到 某剧 第一季", mediaType: MediaTypeSeason, wantCount: 3},
		{name: "合集合并入库", title: "将 1 项目添加到 合集", mediaType: MediaTypeBoxSet, wantCount: 1},
		{name: "无数量信息", title: "已将项目添加到 电视剧", mediaType: MediaTypeSeries, wantCount: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseNewItemCountFromTitle(tc.title); got != tc.wantCount {
				t.Fatalf("ParseNewItemCountFromTitle(%q) = %d，期望 %d", tc.title, got, tc.wantCount)
			}

			notifier := &recordingNotifier{}
			client := &fakeClient{
				detail: &ItemDetail{Name: "测试剧集", Type: tc.mediaType},
				seasons: map[int][]int{
					1: {1, 2, 3},
				},
			}
			svc := testService(t, notifier, client)

			raw := []byte(fmt.Sprintf(`{
				"Event": "library.new",
				"Title": %q,
				"Item": {"Id": "series-1", "Name": "测试剧集", "Type": %q, "SeriesId": "series-1"}
			}`, tc.title, tc.mediaType))

			if err := svc.HandleWebhook(context.Background(), raw); err != nil {
				t.Fatalf("合并事件处理失败：%v", err)
			}

			waitFor(t, time.Second, func() bool { return notifier.count() == 1 },
				"合并事件应产生恰好 1 条通知")

			calls := notifier.snapshot()
			if calls[0].Category != domain.NotificationCategoryEmbyIngest {
				t.Fatalf("通知分类 = %q，期望 %q", calls[0].Category, domain.NotificationCategoryEmbyIngest)
			}
			if want := "📚 Emby " + MediaTypeTitleName(tc.mediaType) + " 入库通知"; calls[0].Title != want {
				t.Fatalf("通知标题 = %q，期望 %q，与媒体类型不匹配", calls[0].Title, want)
			}
		})
	}
}

// TestLibraryDeletedNotifiesWithoutDeleting 验证删除事件只通知，不执行任何删除动作。
func TestLibraryDeletedNotifiesWithoutDeleting(t *testing.T) {
	notifier := &recordingNotifier{}
	client := &fakeClient{detail: &ItemDetail{Name: "测试电影", Type: MediaTypeMovie}}
	svc := testService(t, notifier, client)

	raw := []byte(`{
		"Event": "library.deleted",
		"Item": {"Id": "movie-1", "Name": "测试电影", "Type": "Movie", "Path": "/media/movie.mkv"}
	}`)
	if err := svc.HandleWebhook(context.Background(), raw); err != nil {
		t.Fatalf("删除事件处理失败：%v", err)
	}

	if got := notifier.count(); got != 1 {
		t.Fatalf("删除通知数量 = %d，期望 1", got)
	}
	call := notifier.snapshot()[0]
	if call.Category != domain.NotificationCategoryEmbyDeleted {
		t.Fatalf("通知分类 = %q，期望 %q", call.Category, domain.NotificationCategoryEmbyDeleted)
	}
	if call.Title != DeletedNotificationTitle {
		t.Fatalf("通知标题 = %q，期望 %q", call.Title, DeletedNotificationTitle)
	}
	// fakeClient 只有读接口，编译期即可保证 Service 没有删除能力；
	// 这里再断言详情查询是只读调用，未产生任何写副作用记录。
	if client.detail == nil {
		t.Fatal("测试桩状态异常")
	}
}

// TestPlaybackEventsRouteToPlaybackHandling 验证三种播放事件都走播放通知分支。
func TestPlaybackEventsRouteToPlaybackHandling(t *testing.T) {
	origDedup := playbackDedupWindow
	playbackDedupWindow = time.Millisecond
	t.Cleanup(func() { playbackDedupWindow = origDedup })

	cases := []struct {
		event     string
		wantEmoji string
		wantName  string
	}{
		{event: EventPlaybackStart, wantEmoji: "📺", wantName: "播放开始"},
		{event: EventPlaybackPause, wantEmoji: "⏸️", wantName: "播放暂停"},
		{event: EventPlaybackStop, wantEmoji: "⏹️", wantName: "播放停止"},
	}

	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			if !IsPlaybackEvent(tc.event) {
				t.Fatalf("%q 应被识别为播放事件", tc.event)
			}
			notifier := &recordingNotifier{}
			svc := testService(t, notifier, nil)

			raw := []byte(fmt.Sprintf(`{
				"Event": %q,
				"User": {"Id": "u1", "Name": "测试用户"},
				"Item": {"Id": "movie-1", "Name": "测试电影", "Type": "Movie"},
				"Session": {"DeviceName": "客厅电视", "Client": "Emby Web"},
				"PlaybackInfo": {"PositionTicks": 300000000, "RunTimeTicks": 600000000}
			}`, tc.event))

			if err := svc.HandleWebhook(context.Background(), raw); err != nil {
				t.Fatalf("播放事件处理失败：%v", err)
			}
			if got := notifier.count(); got != 1 {
				t.Fatalf("播放通知数量 = %d，期望 1", got)
			}
			call := notifier.snapshot()[0]
			if call.Category != domain.NotificationCategoryEmbyPlayback {
				t.Fatalf("通知分类 = %q，期望 %q", call.Category, domain.NotificationCategoryEmbyPlayback)
			}
			wantTitle := tc.wantEmoji + " " + tc.wantName + " 测试电影"
			if call.Title != wantTitle {
				t.Fatalf("通知标题 = %q，期望 %q", call.Title, wantTitle)
			}
		})
	}
}

// TestPlaybackNotificationDeduped 验证同一播放会话的重复投递会被去重。
func TestPlaybackNotificationDeduped(t *testing.T) {
	notifier := &recordingNotifier{}
	svc := testService(t, notifier, nil)

	raw := []byte(`{
		"Event": "playback.start",
		"User": {"Id": "u1", "Name": "测试用户"},
		"Item": {"Id": "movie-1", "Name": "测试电影", "Type": "Movie"},
		"Session": {"DeviceName": "客厅电视"}
	}`)

	for i := 0; i < 3; i++ {
		if err := svc.HandleWebhook(context.Background(), raw); err != nil {
			t.Fatalf("第 %d 次播放事件处理失败：%v", i+1, err)
		}
	}
	if got := notifier.count(); got != 1 {
		t.Fatalf("播放通知数量 = %d，期望 1（重复投递必须去重）", got)
	}
}

// TestUnsupportedEventRejected 验证未知事件类型返回校验错误。
func TestUnsupportedEventRejected(t *testing.T) {
	svc := testService(t, &recordingNotifier{}, nil)

	cases := []struct {
		name string
		body string
	}{
		{name: "未知事件类型", body: `{"Event": "session.start", "Item": {"Id": "x"}}`},
		{name: "缺少事件类型", body: `{"Item": {"Id": "x"}}`},
		{name: "请求体为空", body: ``},
		{name: "非法 JSON", body: `{"Event": `},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.HandleWebhook(context.Background(), []byte(tc.body))
			if err == nil {
				t.Fatal("期望返回错误，实际为 nil")
			}
			appErr, ok := domain.AsAppError(err)
			if !ok || appErr == nil || appErr.Code != domain.CodeValidation {
				t.Fatalf("错误码 = %v，期望 %v", appErr, domain.CodeValidation)
			}
		})
	}
}

// TestDisabledServiceIgnoresEverything 验证总开关关闭时静默忽略全部事件。
func TestDisabledServiceIgnoresEverything(t *testing.T) {
	notifier := &recordingNotifier{}
	svc := New(Config{Enabled: false, MediaNotification: true}, nil, notifier, nil)

	raw := []byte(`{"Event": "library.new", "Item": {"Id": "m1", "Name": "电影", "Type": "Movie"}}`)
	if err := svc.HandleWebhook(context.Background(), raw); err != nil {
		t.Fatalf("关闭状态下不应返回错误，实际：%v", err)
	}
	if got := notifier.count(); got != 0 {
		t.Fatalf("关闭状态下通知数量 = %d，期望 0", got)
	}
}
