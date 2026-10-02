// Package embyindex 提供 Emby 本地媒体索引的同步与删除联动能力。
//
// 它维护「Emby 条目 → 网盘文件」的映射（老版 internal/models/emby_media.go 的四张表），
// 供其它功能解析某个 Emby 条目实际对应的网盘文件与 PickCode，
// 以及在 Emby 侧删除媒体时按映射联动删除网盘文件。
//
// 与老版的差异：旧版把每个网盘文件镜像进 SyncFile 表再按 sync_file_id 关联；
// 现版没有逐文件的 STRM 表，改为记录「网盘账号 + 根目录 + 相对路径」，
// 删除时通过 file.Service.ResolvePath 反查真实文件，映射不唯一时一律跳过。
package embyindex

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"litepan/internal/discover/embyclient"
	"litepan/internal/domain"
	"litepan/internal/file"
	"litepan/internal/settings"
	"litepan/internal/strm"
)

// EmbyIncrementalCursorOverlapSeconds 是增量同步游标的负向重叠窗口（秒）。
//
// 老版 internal/emby/emby.go:242 用 600 秒重叠，避免 Emby 的
// DateLastSaved 写入与我们的扫描之间出现竞态而漏掉刚入库的条目。
// 命名成常量是为了让测试可以断言重叠数学。
const EmbyIncrementalCursorOverlapSeconds int64 = 600

// embyIncrementalFields 是增量扫描需要 Emby 返回的字段集合，与老版保持一致。
const embyIncrementalFields = "DateCreated,DateModified,ParentId,PremiereDate,MediaStreams,Path,MediaSources,SeriesId,SeasonId,SeriesName,SeasonName,IndexNumber,ParentIndexNumber"

// embyScannedItemTypes 是纳入本地索引的条目类型：只索引真正的媒体，
// 文件夹与剧集容器不进 emby_media_items（老版同样只用 Movie,Video,Episode）。
const embyScannedItemTypes = "Movie,Video,Episode"

// embyInitialScanLimit 是单页拉取条目的数量。
const embyInitialScanLimit = 100

// Options 是构造同步引擎所需的外部依赖。
type Options struct {
	Index    domain.EmbyIndexRepository
	Files    *file.Service
	Strm     *strm.Service
	Settings *settings.Service
	Log      *slog.Logger
	Now      func() int64
	// Lookup 可选：PickCode → 网盘文件位置。现版没有逐文件表，缺省即「映射不可用」，
	// 删除联动会因此跳过删除（安全优先）。
	Lookup pickCodeLookup
	// RefreshSink 可选：扫描发现新条目/变更条目后投递刷新意图。
	// 为 nil 时整条刷新链路退化为空操作。
	RefreshSink RefreshIntentSink
	// RefreshAggregationThreshold 覆盖「超过多少条变化就改登记库级刷新」的阈值；
	// <=0 时使用 defaultRefreshAggregationThreshold。
	RefreshAggregationThreshold int
	// ScanInterval 是周期扫描的间隔；<=0 时使用 defaultScanInterval。
	// 仅由 App 的周期循环使用，测试可以直接调用 ScanOnce。
	ScanInterval time.Duration
}

// Service 是 Emby 本地索引同步引擎。
type Service struct {
	index    domain.EmbyIndexRepository
	files    *file.Service
	strm     *strm.Service
	settings *settings.Service
	log      *slog.Logger
	now      func() int64
	lookup   pickCodeLookup

	refreshSink      RefreshIntentSink
	refreshThreshold int
	scanInterval     time.Duration

	// syncRunning 保证同一时刻只有一个全量/增量扫描在跑，
	// 避免两次扫描互相清理对方刚写入的 last_seen_sync_run。
	syncRunning atomic.Bool

	// loopRunning 保证周期扫描循环只启动一次。
	loopRunning atomic.Bool
	// scanRequest 是「立刻扫一轮」的请求信号，容量 1 保证信号可以合并。
	scanRequest chan struct{}
	// scanCancel 取消当前正在执行的扫描（Stop 时使用）。
	scanMu     sync.Mutex
	scanCancel context.CancelFunc

	// configLoader 由内部/app 注入，用于取当前生效的 Emby 配置；
	// 受 scanMu 保护，因为它可能在 Start 之后才被替换。
	configLoader ConfigLoader
}

// New 构造同步引擎；Index 为空时返回 nil，表示该功能未装配。
func New(opts Options) *Service {
	if opts.Index == nil {
		return nil
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = func() int64 { return time.Now().Unix() }
	}
	return &Service{
		index:            opts.Index,
		files:            opts.Files,
		strm:             opts.Strm,
		settings:         opts.Settings,
		log:              log,
		now:              now,
		lookup:           opts.Lookup,
		refreshSink:      opts.RefreshSink,
		refreshThreshold: opts.RefreshAggregationThreshold,
		scanInterval:     opts.ScanInterval,
		scanRequest:      make(chan struct{}, 1),
	}
}

// BuildMinDateLastSaved 计算增量同步使用的 MinDateLastSaved 时间串。
//
// 从游标向前回退 overlapSeconds 秒（负向重叠），保证不会因为 Emby 侧的
// 时间戳写入延迟而漏掉条目；游标本身小于重叠窗口时退化为 Unix 0，
// 绝不产生负时间戳。返回值格式为 RFC3339 UTC。
func BuildMinDateLastSaved(cursor int64, overlapSeconds int64) string {
	if overlapSeconds < 0 {
		overlapSeconds = 0
	}
	if cursor > overlapSeconds {
		cursor -= overlapSeconds
	} else {
		cursor = 0
	}
	return time.Unix(cursor, 0).UTC().Format(time.RFC3339)
}

// SyncEnabled 判断当前 Emby 配置是否启用。
func (s *Service) SyncEnabled() bool {
	if s == nil || s.settings == nil {
		return false
	}
	return s.settings.Bool(settings.KeyEmbyEnabled)
}

// DeleteNetdiskEnabled 判断「Emby 删除联动网盘删除」开关是否打开；默认关闭。
func (s *Service) DeleteNetdiskEnabled() bool {
	if s == nil || s.settings == nil {
		return false
	}
	return s.settings.Bool(settings.KeyEmbyDeleteNetdiskEnabled)
}

// clientFor 基于 Emby 配置构造 Emby 客户端。
func (s *Service) clientFor(cfg Config) *embyclient.Client {
	return embyclient.NewClient(cfg.EmbyURL, cfg.APIKey)
}

// loadConfig 是调用方注入的「取当前生效 Emby 配置」回调。
//
// 本包不依赖 embyproxy，因此配置文件由调用方（internal/app）解析后注入；
// 返回 ok=false 表示当前没有可用配置，调用方应跳过本轮。
type ConfigLoader func(ctx context.Context) (Config, bool)

// SetConfigLoader 注入配置读取回调。必须在 Start 之前调用。
//
// 不注入时周期扫描不会执行任何工作：Config 里的地址与 API Key 只有调用方知道，
// 本包无法自行推断。这与 New 在 Index 为空时返回 nil 的约定一致——
// 缺少必要依赖时退化为空操作，而不是报错。
func (s *Service) SetConfigLoader(loader ConfigLoader) {
	if s == nil {
		return
	}
	s.scanMu.Lock()
	s.configLoader = loader
	s.scanMu.Unlock()
}

// currentConfig 取当前生效的 Emby 配置。
func (s *Service) currentConfig(ctx context.Context) (Config, bool) {
	if s == nil {
		return Config{}, false
	}
	s.scanMu.Lock()
	loader := s.configLoader
	s.scanMu.Unlock()
	if loader == nil {
		return Config{}, false
	}
	cfg, ok := loader(ctx)
	if !ok || !cfg.Usable() {
		return Config{}, false
	}
	return cfg, true
}

// Config 是本包需要的 Emby 连接信息子集。
//
// 它由调用方（internal/app）从 embyproxy 配置里投影出来，本包不依赖 embyproxy：
// 依赖方向保持 embyproxy → 调用方 → embyindex，避免两边互相引用形成循环导入。
// 之所以导出，是因为 PerformEmbySync 等公开方法必须能被外部包调用，
// 而它们都接收本类型作为参数；未导出的参数类型会让这些方法在包外无法调用。
type Config struct {
	// ConfigID 是调用方视角的 Emby 配置 ID，仅用于写入同步状态表与日志。
	ConfigID string
	// Name 是配置名称，仅用于日志与展示。
	Name string
	// EmbyURL 是 Emby/Jellyfin 服务地址（已去掉尾部斜杠）。
	EmbyURL string
	// APIKey 是真实的 API Key：调用方必须传未脱敏的值，脱敏串无法鉴权。
	APIKey string
	// LibraryIDs 是勾选扫描的媒体库 ID；为空表示「全部媒体库」。
	LibraryIDs []string
	// AllSelected 为 true 时表示已勾选全部媒体库，与 LibraryIDs 为空同义。
	AllSelected bool
}

// BuildConfig 是本包对外的配置构造入口，把零散的连接信息组装成 Config。
//
// 传空串/空切片会被归一化：LibraryIDs 里的空白项被丢弃，
// 这样调用方不必重复处理脏数据。是否可用（地址与 API Key 是否齐备）
// 由 PerformEmbySync 等执行入口校验，构造函数本身不做判断。
func BuildConfig(configID, name, embyURL, apiKey string, libraryIDs []string, allSelected bool) Config {
	return Config{
		ConfigID:    strings.TrimSpace(configID),
		Name:        strings.TrimSpace(name),
		EmbyURL:     strings.TrimRight(strings.TrimSpace(embyURL), "/"),
		APIKey:      strings.TrimSpace(apiKey),
		LibraryIDs:  normalizeLibraryIDs(libraryIDs),
		AllSelected: allSelected || len(normalizeLibraryIDs(libraryIDs)) == 0,
	}
}

// normalizeLibraryIDs 去掉空白项并复制一份，避免调用方后续改动切片影响已构造的配置。
func normalizeLibraryIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Usable 判断配置是否具备发起同步的最低条件：地址与 API Key 齐备。
func (c Config) Usable() bool {
	return strings.TrimSpace(c.EmbyURL) != "" && strings.TrimSpace(c.APIKey) != ""
}

// selectedLibraries 解析配置里勾选的媒体库 ID 列表。
// 返回空切片表示「全部媒体库」，与老版 SyncAllLibraries 语义一致。
func decodeSelectedLibraries(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" || raw == "null" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// librarySelection 描述一次扫描需要覆盖的媒体库集合。
type librarySelection struct {
	// libraries 是本次要扫描的媒体库。
	libraries []embyclient.EmbyLibrary
	// allLibraries 为 true 时表示「已选中全部媒体库」，
	// 增量扫描允许清理未出现在本次结果里的旧条目。
	allLibraries bool
}

// librarySelected 判断某个媒体库是否在选中范围内。
// config 为空、未勾选任何库（等同于全选）、或 libraryID 为空时都视为选中。
func librarySelected(selected []string, libraryID string) bool {
	if len(selected) == 0 {
		return true
	}
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return true
	}
	for _, id := range selected {
		if id == libraryID {
			return true
		}
	}
	return false
}

// itemLibraryKey 归一化媒体库标识用于比较。
func itemLibraryKey(libraryID string) string { return strings.TrimSpace(libraryID) }

// resolveItemLibrary 解析单个 Emby 条目所属的媒体库。
//
// 老版 internal/emby/emby.go:550 的契约：
//   - 恰好命中 1 个媒体库 → 返回该库；
//   - 命中多个 → 保留条目但不写 LibraryId（返回空串，不报错）；
//   - 一个都没命中 → 返回错误。
func (s *Service) resolveItemLibrary(client *embyclient.Client, itemID string) (libraryID string, libraryName string, err error) {
	folders, err := client.GetItemLibraryId(itemID)
	if err != nil {
		return "", "", err
	}
	switch len(folders) {
	case 0:
		return "", "", domain.Errorf(domain.CodeNotFound, "未找到 Emby 条目 %s 所属的媒体库", itemID)
	case 1:
		folder := folders[0]
		id := strings.TrimSpace(folder.ID)
		if id == "" {
			id = strings.TrimSpace(folder.ItemId)
		}
		return id, strings.TrimSpace(folder.Name), nil
	default:
		s.log.Warn("Emby 单条同步解析到多个媒体库候选，保留条目但不写入 LibraryId",
			"item_id", itemID, "candidates", len(folders))
		return "", "", nil
	}
}

// nowUnix 返回当前时间戳，便于测试注入。
func (s *Service) nowUnix() int64 {
	if s.now == nil {
		return time.Now().Unix()
	}
	return s.now()
}

// indexOneItem 把一条 Emby 条目写入本地索引，并在能解析出网盘文件时建立映射关系。
// syncRunID 为空表示单条同步（不参与批次清理）。
//
// 返回的 changed 表示「这条记录相对索引里的旧值确实发生了变化」：
//   - 索引里原本没有该条目 → changed = true（新增）；
//   - 关键字段（名称/路径/媒体源/所属库/季集编号等）有变化 → changed = true；
//   - 其余情况（含 bilibili 常见的「只是被重新扫到」）→ changed = false。
//
// 只有 changed 为 true 才应该触发 Emby 刷新：每次扫描都无脑刷新会让
// 媒体库反复扫描，与老版「只在新增/变更时登记刷新」的取向一致。
func (s *Service) indexOneItem(ctx context.Context, item embyclient.BaseItemDtoV2, libraryID, libraryName, syncRunID string) (changed bool, err error) {
	record := buildMediaItem(item, libraryID, syncRunID, s.nowUnix())
	if strings.TrimSpace(record.ItemID) == "" {
		return false, nil
	}
	previous, err := s.index.GetItem(ctx, record.ItemID)
	if err != nil {
		// 「查不到」是新增的正常路径；其它错误也不能阻断写入，
		// 只是无法判断是否变化，保守地按「已变化」处理（宁多刷一次不漏刷）。
		if !isNotFound(err) {
			s.log.Warn("读取 Emby 条目旧值失败，按已变化处理", "item_id", record.ItemID, "err", err)
			previous = nil
		} else {
			previous = nil
		}
	}
	if err := s.index.CreateOrUpdateItem(ctx, record); err != nil {
		return false, err
	}
	if err := s.linkNetdiskFile(ctx, item, libraryID, libraryName); err != nil {
		return false, err
	}
	return mediaItemChanged(previous, record), nil
}

// mediaItemChanged 判断新记录相对旧记录是否发生了对 Emby 有意义的变化。
//
// previous 为 nil 表示新增，必定视为变化。比较的字段刻意不包含
// LastSeenSyncRun / LastSeenAt —— 那两个字段每一轮扫描都会变，
// 拿它们做判据会让「每次扫描都触发全库刷新」。
func mediaItemChanged(previous, current *domain.EmbyMediaItem) bool {
	if previous == nil {
		return true
	}
	if current == nil {
		return false
	}
	return previous.Name != current.Name ||
		previous.Type != current.Type ||
		previous.LibraryID != current.LibraryID ||
		previous.Path != current.Path ||
		previous.PickCode != current.PickCode ||
		previous.MediaSourcePath != current.MediaSourcePath ||
		previous.SeriesID != current.SeriesID ||
		previous.SeasonID != current.SeasonID ||
		previous.IndexNumber != current.IndexNumber ||
		previous.ParentIndexNumber != current.ParentIndexNumber ||
		previous.DateModifiedTime != current.DateModifiedTime ||
		previous.DateCreatedTime != current.DateCreatedTime
}

// linkNetdiskFile 尝试把 Emby 条目关联到具体网盘文件。
//
// 解析不出 PickCode 时只记调试日志，不视为错误：不是所有媒体库条目
// 都来自网盘（本地媒体、其它来源的 STRM 都解析不到）。
func (s *Service) linkNetdiskFile(ctx context.Context, item embyclient.BaseItemDtoV2, libraryID, libraryName string) error {
	pickCode, _, err := ExtractPickCode(item.MediaSources)
	if err != nil || pickCode == "" {
		// 没有 PickCode 说明该条目不对应网盘直链，属正常情况。
		return nil
	}
	resolved, ok := s.resolveBackingFile(ctx, pickCode)
	if !ok {
		s.log.Debug("Emby 条目解析到 PickCode 但未匹配到网盘文件，仅保留条目",
			"item_id", item.Id, "pick_code", pickCode)
		return nil
	}
	rel := &domain.EmbyMediaSyncFile{
		EmbyItemID:   numericItemID(item.Id),
		SyncFileID:   resolved.TaskID,
		PickCode:     pickCode,
		SyncPathID:   resolved.TaskID,
		AccountID:    resolved.AccountID,
		RootID:       resolved.RootID,
		RelativePath: resolved.RelativePath,
		FileName:     resolved.FileName,
	}
	if err := s.index.CreateMediaSyncFile(ctx, rel); err != nil {
		return err
	}
	if libraryID != "" && resolved.TaskID > 0 {
		return s.index.CreateOrUpdateLibrarySyncPath(ctx, libraryID, resolved.TaskID, libraryName)
	}
	return nil
}

// numericItemID 把 Emby item id 转成整数。
// Emby 的 Id 是 32 位十六进制串，转不成整数时返回 0，
// 老版的 item_id_int 同样是尽力而为的数值投影，0 表示无法解析。
func numericItemID(itemID string) int64 { return parseItemIDInt(itemID) }
