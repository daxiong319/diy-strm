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

	mu       sync.RWMutex
	channels []*domain.NotifyChannel
}

// NewDispatcher 构造Dispatcher；getCfg 为可选的覆盖（便于测试），nil 时用 repo.ListEnabled。
func NewDispatcher(repo domain.NotifyChannelRepository, log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	d := &Dispatcher{repo: repo, log: log}
	d.getCfg = func(ctx context.Context) ([]*domain.NotifyChannel, error) {
		return d.repo.ListEnabled(ctx)
	}
	return d
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

func (d *Dispatcher) onNotificationCreated(ctx context.Context, e eventbus.NotificationCreated) {
	channels := d.snapshot()
	if len(channels) == 0 {
		return
	}
	msg := Message{Title: e.Title, Content: e.Message, Tone: toneFromLevel(e.Level)}
	for _, ch := range channels {
		if ch == nil || !ch.Enabled {
			continue
		}
		// 事件白名单过滤：config.events 非空时，仅当通知 category 命中其一才推送。
		cfg := decodeConfig(ch.Config)
		if !matchesEventFilter(cfg["events"], e.Category) {
			continue
		}
		cfg = withDefaults(ch.Type, cfg)
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := Send(sendCtx, ch.Type, cfg, msg)
		cancel()
		if err != nil {
			d.log.Error("外部通知发送失败", "channel", ch.Name, "type", ch.Type, "err", err)
		} else {
			d.log.Debug("外部通知已发送", "channel", ch.Name, "type", ch.Type)
		}
	}
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
	events = strings.TrimSpace(events)
	if events == "" {
		return true
	}
	for _, item := range strings.Split(events, ",") {
		if strings.EqualFold(strings.TrimSpace(item), category) {
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
