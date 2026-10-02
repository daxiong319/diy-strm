package embywebhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"litepan/internal/domain"
)

// Client 是 service 需要的 Emby 侧能力。
// 由 embyproxy.Service 结构化实现，测试可注入假实现，因此本包不依赖 HTTP/DB。
type Client interface {
	// GetItemDetail 按条目 ID 取详情；条目不存在返回 (nil, nil)。
	GetItemDetail(ctx context.Context, itemID string) (*ItemDetail, error)
	// GetSeriesEpisodes 按剧集 ID 取「季 → 集」映射。
	GetSeriesEpisodes(ctx context.Context, seriesID string) (map[int][]int, error)
}

// Notifier 把通知投递到通知中心（再由渠道分发到 Telegram/Bark 等）。
type Notifier interface {
	Notify(ctx context.Context, level, category, title, message string)
}

// Config 是 Webhook 通知的行为开关。
type Config struct {
	// Enabled 表示 Emby 已配置（地址与 API Key 齐备）。
	Enabled bool
	// AuthEnabled 为 true 时要求请求携带有效 API Key。
	AuthEnabled bool
	// MediaNotification 控制入库/删除通知总开关。
	MediaNotification bool
	// PlaybackOverview 控制播放通知里是否带简介。
	PlaybackOverview bool
	// PlaybackProgress 控制播放通知里是否带播放进度。
	PlaybackProgress bool
	// DeleteNetdisk 预留给「删除媒体同时删除网盘文件」的联动开关。
	// 当前版本不实现该联动，仅保留字段以便后续接入。
	DeleteNetdisk bool
}

// Service 处理 Emby Webhook 事件。
type Service struct {
	cfg      Config
	client   Client
	notifier Notifier
	log      *slog.Logger
	buffer   *Buffer

	// 播放事件去重：同一用户+设备+条目+事件在窗口内只通知一次。
	playbackMu    sync.Mutex
	playbackCache map[string]time.Time

	startOnce sync.Once
}

// New 创建 Service 并装配剧集合并缓冲区。
func New(cfg Config, client Client, notifier Notifier, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	svc := &Service{
		cfg:           cfg,
		client:        client,
		notifier:      notifier,
		log:           log,
		playbackCache: map[string]time.Time{},
	}
	svc.buffer = NewBuffer(svc.onSeriesFlush)
	return svc
}

// Start 启动后台合并 buffer。
func (s *Service) Start(ctx context.Context) {
	if s == nil || s.buffer == nil {
		return
	}
	s.startOnce.Do(func() {
		s.buffer.Start()
		go func() {
			<-ctx.Done()
			s.buffer.Stop()
		}()
	})
}

// Stop 停止后台合并 buffer；可重复调用。
func (s *Service) Stop() {
	if s == nil || s.buffer == nil {
		return
	}
	s.buffer.Stop()
}

// Config 返回当前配置快照。
func (s *Service) Config() Config {
	if s == nil {
		return Config{}
	}
	return s.cfg
}

// SetNotifier 在装配阶段注入通知器。
// 通知中心在 HTTP 装配层才构造，因此这里保留一个后置注入入口。
func (s *Service) SetNotifier(notifier Notifier) {
	if s == nil {
		return
	}
	s.notifier = notifier
}

// HandleWebhook 解析并处理一条 Emby Webhook 请求体。
// 返回错误表示请求体不合法或事件类型未知，由 HTTP 层映射为 400。
func (s *Service) HandleWebhook(ctx context.Context, raw []byte) error {
	if s == nil {
		return nil
	}
	event, err := ParseEvent(raw)
	if err != nil {
		return err
	}
	if !s.cfg.Enabled {
		// Emby 未配置时静默忽略，避免把噪音写进日志。
		return nil
	}
	return s.dispatch(ctx, event)
}

// ParseEvent 解析 Webhook 请求体；空体或非法 JSON 返回错误。
func ParseEvent(raw []byte) (EmbyEvent, error) {
	var event EmbyEvent
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return event, domain.Errorf(domain.CodeValidation, "请求体为空")
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return event, domain.Errorf(domain.CodeValidation, "请求体解析失败：%v", err)
	}
	if strings.TrimSpace(event.Event) == "" {
		return event, domain.Errorf(domain.CodeValidation, "缺少事件类型")
	}
	return event, nil
}

// dispatch 按事件类型分发。
func (s *Service) dispatch(ctx context.Context, event EmbyEvent) error {
	switch event.Event {
	case EventLibraryNew:
		s.handleLibraryNew(ctx, event)
	case EventLibraryModified:
		s.handleLibraryModified(ctx, event)
	case EventLibraryDeleted:
		s.handleLibraryDeleted(ctx, event)
	case EventPlaybackStart, EventPlaybackPause, EventPlaybackStop:
		s.handlePlayback(ctx, event)
	default:
		return domain.Errorf(domain.CodeValidation, "不支持的事件类型：%s", event.Event)
	}
	return nil
}

// handleLibraryNew 处理入库事件。
// Emby 4.8 批量入库只推一条合并事件（Item.Type 为 Series/Season/Folder/BoxSet），
// 必须走合并分支，否则入库通知永远收不到。
func (s *Service) handleLibraryNew(ctx context.Context, event EmbyEvent) {
	if event.Item.Type == MediaTypeEpisode {
		// 单集逐个到达，先进缓冲等窗口结束再合并成一条剧集通知。
		s.buffer.AddItem(event)
		return
	}
	if IsMergedIngestType(event.Item.Type) {
		s.notifyMergedIngest(ctx, event)
		return
	}
	s.notifyItem(ctx, event, domain.NotificationCategoryEmbyIngest)
}

// handleLibraryModified 处理条目变更事件，按入库同样处理（仅提示）。
func (s *Service) handleLibraryModified(ctx context.Context, event EmbyEvent) {
	if !s.cfg.MediaNotification {
		return
	}
	if event.Item.Type == MediaTypeEpisode {
		s.buffer.AddItem(event)
		return
	}
	if IsMergedIngestType(event.Item.Type) {
		s.notifyMergedIngest(ctx, event)
		return
	}
	s.notifyItem(ctx, event, domain.NotificationCategoryEmbyIngest)
}

// notifyMergedIngest 处理 Emby 4.8 的合并入库事件。
func (s *Service) notifyMergedIngest(ctx context.Context, event EmbyEvent) {
	if !s.cfg.MediaNotification {
		return
	}
	newCount := ParseNewItemCountFromTitle(event.Title)
	detail, err := s.detailOf(ctx, event)
	if err != nil {
		s.log.Warn("Emby 入库通知取条目详情失败", "item_id", event.Item.ID, "error", err)
	}

	seasons := map[int][]int{}
	totalSize := int64(0)
	fileNames := make([]string, 0)
	if detail != nil && detail.Type != MediaTypeMovie {
		if fetched, fetchErr := s.client.GetSeriesEpisodes(ctx, event.Item.ID); fetchErr == nil {
			seasons = fetched
		} else {
			s.log.Warn("Emby 入库通知取季集失败", "item_id", event.Item.ID, "error", fetchErr)
		}
		for _, source := range detail.MediaSources {
			totalSize += source.Size
			if source.Path != "" {
				fileNames = append(fileNames, source.Path)
			}
		}
	}

	extra := BuildExtraLines(FormatSeasonEpisodes(seasons), totalSize, SummarizeReleaseGroups(fileNames), newCount)
	// 标题需要区分季/合集，所以传原始类型而不是已归一化的显示名。
	// 条目详情若已归一为电影，则以详情为准（合并事件也可能指向单部电影）。
	rawType := event.Item.Type
	if detail != nil && detail.Type == MediaTypeMovie {
		rawType = MediaTypeMovie
	}
	s.sendIngest(ctx, detail, event, extra, rawType)
}

// notifyItem 处理单条目入库/变更事件。
func (s *Service) notifyItem(ctx context.Context, event EmbyEvent, category string) {
	if !s.cfg.MediaNotification {
		return
	}
	detail, err := s.detailOf(ctx, event)
	if err != nil {
		s.log.Warn("Emby 通知取条目详情失败", "item_id", event.Item.ID, "error", err)
	}
	s.sendIngest(ctx, detail, event, "", MediaTypeName(event.Item.Type))
}

// sendIngest 组装并投递入库通知。detail 为空时回退到事件自带字段。
func (s *Service) sendIngest(ctx context.Context, detail *ItemDetail, event EmbyEvent, extra, mediaType string) {
	if s.notifier == nil {
		return
	}
	content := ContentInput{Detail: fallbackDetail(detail, event), ExtraLines: extra, IngestedAt: nowStamp()}
	title := "📚 Emby " + MediaTypeTitleName(mediaType) + " 入库通知"
	s.notifier.Notify(ctx, "success", domain.NotificationCategoryEmbyIngest, title, BuildMediaNotificationContent(content))
}

// MediaTypeTitleName 返回通知标题中使用的媒体类型名。
//
// 与 MediaTypeName 的区别：季/合集在正文里归入"剧集/媒体"即可，但标题需要
// 能区分出究竟是季还是合集，否则用户从标题看不出入库粒度。
func MediaTypeTitleName(mediaType string) string {
	switch mediaType {
	case MediaTypeSeason:
		return "季"
	case MediaTypeBoxSet:
		return "合集"
	default:
		return MediaTypeName(mediaType)
	}
}

// handleLibraryDeleted 处理删除事件，只发通知。
//
// 扩展点：网盘删除联动由 embyindex 包负责，此处只通知。
func (s *Service) handleLibraryDeleted(ctx context.Context, event EmbyEvent) {
	if !s.cfg.MediaNotification {
		return
	}
	if event.Item.Type == MediaTypeEpisode {
		s.buffer.AddDeletedItem(event)
		return
	}
	if s.notifier == nil {
		return
	}
	seasons := map[int][]int{}
	if detail, err := s.detailOf(ctx, event); err == nil && detail != nil {
		seasons = map[int][]int{}
	}
	s.notifier.Notify(ctx, "warning", domain.NotificationCategoryEmbyDeleted,
		DeletedNotificationTitle, BuildDeletedContent(event, seasons))
}

// handlePlayback 处理播放事件，带 1 分钟去重。
func (s *Service) handlePlayback(ctx context.Context, event EmbyEvent) {
	playback, err := ParsePlaybackEvent(marshalEvent(event))
	if err != nil {
		s.log.Warn("Emby 播放事件解析失败", "error", err)
		return
	}
	key := playbackDedupKey(playback)
	if s.seenRecently(key) {
		return
	}
	if s.notifier == nil {
		return
	}
	title := PlaybackEventEmoji(event.Event) + " " + PlaybackEventName(event.Event) + " " + strings.TrimSpace(playback.Item.Name)
	content := BuildPlaybackContent(playback, s.cfg.PlaybackOverview, s.cfg.PlaybackProgress)
	s.notifier.Notify(ctx, "success", domain.NotificationCategoryEmbyPlayback, title, content)
}

// ParsePlaybackEvent 解析播放事件载荷。
func ParsePlaybackEvent(raw []byte) (PlaybackEvent, error) {
	var event PlaybackEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return event, domain.Errorf(domain.CodeValidation, "播放事件解析失败：%v", err)
	}
	return event, nil
}

// seenRecently 判断该播放事件是否在去重窗口内出现过；顺带清理过期条目。
func (s *Service) seenRecently(key string) bool {
	now := Now()
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	if last, ok := s.playbackCache[key]; ok && now.Sub(last) < playbackDedupWindow {
		return true
	}
	for existing, last := range s.playbackCache {
		if now.Sub(last) > playbackCacheTTL {
			delete(s.playbackCache, existing)
		}
	}
	s.playbackCache[key] = now
	return false
}

// onSeriesFlush 是缓冲区到期回调：把合并后的季集渲染成一条通知。
func (s *Service) onSeriesFlush(flush SeriesFlush) {
	if !s.cfg.MediaNotification || s.notifier == nil {
		return
	}
	seasons := FormatSeasonEpisodes(flush.Seasons)
	ctx := context.Background()
	if flush.Deleted {
		event := EmbyEvent{Item: EmbyItem{Name: flush.SeriesName, SeriesName: flush.SeriesName}}
		s.notifier.Notify(ctx, "warning", domain.NotificationCategoryEmbyDeleted,
			DeletedNotificationTitle, BuildDeletedContent(event, flush.Seasons))
		return
	}
	extra := BuildExtraLines(seasons, 0, "", 0)
	detail := ItemDetail{Name: flush.SeriesName, Type: MediaTypeSeries}
	content := BuildMediaNotificationContent(ContentInput{Detail: detail, ExtraLines: extra, IngestedAt: nowStamp()})
	s.notifier.Notify(ctx, "success", domain.NotificationCategoryEmbyIngest, "📚 Emby 剧集 入库通知", content)
}

// detailOf 取条目详情，客户端不可用时返回 nil。
func (s *Service) detailOf(ctx context.Context, event EmbyEvent) (*ItemDetail, error) {
	if s.client == nil || strings.TrimSpace(event.Item.ID) == "" {
		return nil, nil
	}
	return s.client.GetItemDetail(ctx, event.Item.ID)
}

// fallbackDetail 在详情缺失时用事件自带字段兜底，保证通知仍有内容。
func fallbackDetail(detail *ItemDetail, event EmbyEvent) ItemDetail {
	if detail != nil {
		return *detail
	}
	return ItemDetail{
		ID:                event.Item.ID,
		Name:              event.Item.Name,
		Type:              event.Item.Type,
		Overview:          event.Item.Overview,
		ProductionYear:    event.Item.ProductionYear,
		CommunityRating:   event.Item.CommunityRating,
		Genres:            event.Item.Genres,
		ProviderIDs:       event.Item.ProviderIds,
		ImageTags:         event.Item.ImageTags,
		DateCreated:       event.Item.DateCreated,
		MediaSources:      toMediaSources(event.Item.MediaSources),
		SeriesName:        event.Item.SeriesName,
		IndexNumber:       event.Item.IndexNumber,
		ParentIndexNumber: event.Item.ParentIndexNumber,
	}
}

// toMediaSources 把事件里的媒体源转成渲染用的结构。
func toMediaSources(sources []EmbyMediaSource) []MediaSource {
	if len(sources) == 0 {
		return nil
	}
	out := make([]MediaSource, 0, len(sources))
	for _, source := range sources {
		out = append(out, MediaSource{Path: source.Path, Size: source.Size})
	}
	return out
}

// playbackDedupKey 生成播放事件去重键。
func playbackDedupKey(event PlaybackEvent) string {
	return strings.Join([]string{
		strings.TrimSpace(event.User.ID),
		strings.TrimSpace(event.User.Name),
		strings.TrimSpace(event.Item.Type),
		strings.TrimSpace(event.Item.Name),
		strings.TrimSpace(event.Session.DeviceName),
		strings.TrimSpace(event.Event),
	}, "_")
}

// marshalEvent 把已解析的事件重新序列化，便于复用 PlaybackEvent 解析路径。
func marshalEvent(event EmbyEvent) []byte {
	data, err := json.Marshal(event)
	if err != nil {
		return nil
	}
	return data
}

// nowStamp 返回通知里使用的「入库时间」文本。
func nowStamp() string {
	return Now().Format("2006-01-02 15:04:05")
}
