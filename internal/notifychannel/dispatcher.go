package notifychannel

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"litepan/internal/domain"
	"litepan/internal/eventbus"
)

// Dispatcher 订阅事件总线上的 NotificationCreated，把通知转发到所有启用的外部渠道。
// 它只负责"投递"，不关心通知如何落库（那是 notification.Service 的职责）。
type Dispatcher struct {
	repo   domain.NotifyChannelRepository
	log    *slog.Logger
	getCfg func(ctx context.Context) ([]*domain.NotifyChannel, error)

	// retryQueue 补发队列仓储；nil 表示不启用补发（webhook 失败就只是失败）。
	//
	// 用 setter 而非构造函数参数：DispatchService 在 wire_http 里比仓储更早
	// 创建，注入顺序与 SetNotifier 的理由一致（避免为依赖对调装配顺序）。
	retryQueue domain.NotifyRetryRepository

	// now 便于测试注入假时钟。nil 时用 time.Now。
	now func() time.Time

	mu       sync.RWMutex
	channels []*domain.NotifyChannel
}

// NewDispatcher 构造Dispatcher；getCfg 为可选的覆盖（便于测试），nil 时用 repo.ListEnabled。
func NewDispatcher(repo domain.NotifyChannelRepository, log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	d := &Dispatcher{repo: repo, log: log, now: time.Now}
	d.getCfg = func(ctx context.Context) ([]*domain.NotifyChannel, error) {
		return d.repo.ListEnabled(ctx)
	}
	return d
}

// SetRetryQueue 注入补发队列仓储。必须在 StartRetryWorker 之前调用。
func (d *Dispatcher) SetRetryQueue(q domain.NotifyRetryRepository) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.retryQueue = q
	d.mu.Unlock()
}

// Register 订阅事件总线。
func (d *Dispatcher) Register(bus *eventbus.Bus) {
	if d == nil || bus == nil {
		return
	}
	eventbus.Subscribe(bus, d.onNotificationCreated)
}

// Refresh 重新加载启用渠道缓存（渠道增删改后调用）。
func (d *Dispatcher) Refresh(ctx context.Context) {
	if d == nil || d.getCfg == nil {
		return
	}
	channels, err := d.getCfg(ctx)
	if err != nil {
		d.log.Warn("刷新通知渠道缓存失败", "err", err)
		return
	}
	d.mu.Lock()
	d.channels = channels
	d.mu.Unlock()
	d.log.Info("通知渠道缓存已刷新", "enabled", len(channels))
}

func (d *Dispatcher) snapshot() []*domain.NotifyChannel {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]*domain.NotifyChannel, 0, len(d.channels))
	out = append(out, d.channels...)
	return out
}

// sceneFields 查出一条通知对应的场景与场景名，供模板注入。
//
// 未归入任何场景的分类返回两个空串而不是 "unknown"：拼一个假场景名填进
// 模板，用户会以为真有这么个场景存在。
func sceneFields(category string) (string, string) {
	scene := domain.SceneOf2(category)
	if scene == "" {
		return "", ""
	}
	label, ok := domain.SceneLabel(scene)
	if !ok {
		return string(scene), ""
	}
	return string(scene), label
}

func (d *Dispatcher) onNotificationCreated(ctx context.Context, e eventbus.NotificationCreated) {
	channels := d.snapshot()
	if len(channels) == 0 {
		return
	}
	scene, sceneLabel := sceneFields(e.Category)
	msg := Message{
		Title:      e.Title,
		Content:    e.Message,
		Tone:       toneFromLevel(e.Level),
		Scene:      scene,
		SceneLabel: sceneLabel,
	}
	for _, ch := range channels {
		if ch == nil || !ch.Enabled {
			continue
		}
		// 事件白名单过滤：config.events 非空时，仅当通知 category 命中其一才推送。
		cfg := decodeConfig(ch.Config)
		if !matchesEventFilter(cfg["events"], e.Category) {
			continue
		}
		// 场景过滤：config.scenes 非空时，仅当该场景命中其一才推送。
		// 未归类的 category（见 domain.UnmappedCategories）**恒放行** ——
		// 否则用户关掉所有场景会把账号认证失效这类告警一起静音。
		if !matchesSceneFilter(cfg["scenes"], e.Category) {
			d.log.Debug("通知被场景开关拦下", "channel", ch.Name, "scene", e.Category)
			continue
		}
		cfg = withDefaults(ch.Type, cfg)
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := Send(sendCtx, ch.Type, cfg, msg)
		cancel()
		if err != nil {
			d.log.Error("外部通知发送失败", "channel", ch.Name, "type", ch.Type, "err", err)
			d.maybeEnqueueRetry(ctx, ch, cfg, msg, e.Category, err)
		} else {
			d.log.Debug("外部通知已发送", "channel", ch.Name, "type", ch.Type)
		}
	}
}

// maybeEnqueueRetry 在发送失败且渠道是 webhook 时把这条通知排进补发队列。
//
// ⚠️ 只对 webhook 生效（任务书明确要求）：Telegram / Bark 这类渠道的失败
// 往往是 bot_token 失效、chat_id 填错，重发 5 次也不会好，只会在补发队列里
// 堆出一堆永远失败的噪音，把真正需要人工看的 webhook 失败淹掉。
//
// ⚠️ 也不对「不可重试」的失败入队（4xx、配置缺失、未知渠道）：这种失败
// 重发一万次也一样，进队列只是让用户多等五档退避（约 15 小时）才看到
// 一个当场就能看懂的 400。直接丢弃，但保留 Error 日志说明为什么不补发。
func (d *Dispatcher) maybeEnqueueRetry(ctx context.Context, ch *domain.NotifyChannel, cfg map[string]string, msg Message, category string, sendErr error) {
	if ch == nil || ch.Type != ChannelWebhook {
		return
	}
	if !RetryableError(sendErr) {
		d.log.Error("通知投递被拒且重试无望，不进补发队列", "channel", ch.Name,
			"category", category, "err", sendErr)
		return
	}
	d.mu.RLock()
	queue := d.retryQueue
	d.mu.RUnlock()
	if queue == nil {
		return
	}
	// 补发用与失败当刻完全一致的 cfg 快照：用户事后改了 webhook 地址之后，
	// 旧失败记录不该被改投到新地址上（那是两次不同的发送）。
	delay, ok := backoffFor(1)
	if !ok {
		// 退避表为空这种不可能发生的配置错误：宁可不入队也别静默丢。
		d.log.Error("补发退避表为空，无法排期首次重试", "channel", ch.Name)
		return
	}
	entry := domain.NotifyRetryEntry{
		EventScene:    domain.SceneOf2(category),
		ChannelType:   ch.Type,
		ChannelName:   ch.Name,
		TargetURL:     cfg["url"],
		ChannelConfig: encodeConfig(cfg),
		Title:         msg.Title,
		Content:       msg.Content,
		Tone:          msg.Tone,
		Attempts:      0,
		NextRetryAt:   d.now().Add(delay),
		LastError:     sendErr.Error(),
		Status:        domain.NotifyRetryStatusPending,
	}
	// 入队失败**不能**影响主流程：通知已经试过一次并失败了，
	// 再叠一条「入队失败」只会让排查变复杂，记日志即可。
	if _, err := queue.Enqueue(ctx, entry); err != nil {
		d.log.Error("通知进入补发队列失败", "channel", ch.Name, "id", ch.ID, "err", err)
		return
	}
	d.log.Info("通知已进入补发队列", "channel", ch.Name, "url", entry.TargetURL, "retry_in", delay)
}

// decodeConfig 把 JSON 配置字符串解析为 map；解析失败返回空 map（发送时由必填校验拦截）。
func decodeConfig(raw string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]string{}
	}
	return out
}

// encodeConfig 把 map 序列化为 JSON 字符串（供 API 层写入前调用）。
func encodeConfig(cfg map[string]string) string {
	b, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// withDefaults 合并渠道元数据里暗含的默认值（目前仅兜底，占位便于后续扩展）。
func withDefaults(channelType string, cfg map[string]string) map[string]string {
	return cfg
}

// matchesEventFilter 判断通知 category 是否命中 events 白名单；events 为空串表示全放行。
func matchesEventFilter(events, category string) bool {
	return matchesListFilter(events, category)
}

// matchesSceneFilter 判断通知是否命中渠道的场景白名单。
//
// 与 events 的关键差异：**未归类的 category 恒放行**。
// events 是内部 category 的白名单，用户自己填、自己负责；scenes 是面向用户的
// 订阅开关，档位粗（7 个场景），若在这里同样「未命中即拦下」，用户勾掉
// 「同步」场景就会连带收不到 Emby 入库、账号认证失效这些既不属于任何场景
// 也不该被静音的通知。
func matchesSceneFilter(scenes, category string) bool {
	if scene, ok := domain.SceneOf(category); ok {
		return matchesListFilter(scenes, string(scene))
	}
	return true
}

// matchesListFilter 逗号分隔白名单，空串表示全放行。
func matchesListFilter(list, value string) bool {
	list = strings.TrimSpace(list)
	if list == "" {
		return true
	}
	for _, item := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(item), value) {
			return true
		}
	}
	return false
}

func toneFromLevel(level string) string {
	switch strings.ToLower(level) {
	case "error", "danger":
		return "error"
	case "warn", "warning":
		return "warn"
	case "success":
		return "success"
	default:
		return "info"
	}
}
