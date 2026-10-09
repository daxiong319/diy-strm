// Package connector 定义搜索连接器（SearchConnector）协议。
//
// 背景：litepan 不是「缺搜索源」—— tgto123 / hdhive / 观影 / SeedHub / TG 公开频道
// 五套源都在，缺的是**接口**。原来订阅执行器把检索写死成一行
//
//	resources, err := Tgto123SearchResources(ctx, sub.Title, sub.TMDBID, mediaType, "")
//
// 每接一个新源都要改一遍订阅引擎、候选结构、评分、去重。本包把这些收敛成一个契约：
// 各源实现 Connector，订阅引擎只认 Key() 和 []Item。
//
// 与 T03 身份校验的关系（重要）：Resolve 的返回类型直接复用
// identity.Manifest，不另造一套 File/FileManifest。T03 定下的不变式是
// 「拿不到任何可验证证据就不转存」，而 Resolve 正是「怎么拿到证据」的出口 ——
// 两者共用同一个结构，改一处不会让另一处悄悄错位。
//
// 依赖边界：本包只依赖 identity 与标准库，**不依赖 internal/discover/discovery**。
// 具体适配器需要调用 discovery 里的 Tgto123SearchResources 等既有函数，
// 放在 discovery 包内以避免循环引用，接口本身留在本包让 bot/手动搜索等其它模块也能引用。
package connector

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"litepan/internal/discover/identity"
)

// ItemKind 候选的资源形态。
type ItemKind string

const (
	// ItemShareLink 网盘分享链接（123 / 115 / 光鸭 / 139 / 夸克…）
	ItemShareLink ItemKind = "share_link"
	// ItemMagnet 磁力链接，必须先 Resolve 拿到文件清单才能验证
	ItemMagnet ItemKind = "magnet"
	// ItemTorrent 种子文件，必须先 Resolve
	ItemTorrent ItemKind = "torrent"
	// ItemDirectLink 直链（http(s) 直下的单文件）
	ItemDirectLink ItemKind = "direct_link"
	// ItemEd2k ed2k 链接（RE0 的 pan_type 之一）
	ItemEd2k ItemKind = "ed2k"
)

// Purpose 连接器适用场景，与 参考实现 的 purpose 字段对齐。
// subscriptions=订阅自动检索 / manual=手动搜索 / bot=TG Bot /
// movie=仅电影 / tv=仅剧集 / agent=AI 助理。
const (
	PurposeSubscriptions = "subscriptions"
	PurposeManual        = "manual"
	PurposeBot           = "bot"
	PurposeMovie         = "movie"
	PurposeAgent         = "agent"
	PurposeTV            = "tv"
)

// Item 是各源归一化后的候选条目。
type Item struct {
	Kind ItemKind `json:"kind"`

	// ShareCode 分享码（参考实现 口径 ^[0-9A-Za-z]{1,16}$）。RE0 用 slug 承载这一位，
	// 它是站方稳定标识，也是 litepan 转存链（TransferResource(ctx, slug, provider)）的入参。
	ShareCode string `json:"share_code,omitempty"`
	// Slug 站方资源 ID。RE0/HDHive 用它当 ShareCode，litepan 转存链按 slug 走。
	Slug string `json:"slug,omitempty"`

	MagnetURI string `json:"magnet_uri,omitempty"`
	InfoHash  string `json:"info_hash,omitempty"`
	Ed2kLink  string `json:"ed2k_link,omitempty"`
	DirectURL string `json:"direct_url,omitempty"`

	Title     string `json:"title"`
	SourceKey string `json:"source_key"`
	Provider  string `json:"provider,omitempty"` // 目标网盘：123/115/guangya/pan139
	Remark    string `json:"remark,omitempty"`

	// Resolution 归一化分辨率：720 / 1080 / 2160，0=未知。
	// 4K 统一折成 2160（与 internal/moviepilot ParseQualityFromName 同一口径）。
	Resolution int    `json:"resolution,omitempty"`
	Codec      string `json:"codec,omitempty"` // h265 / h264 / av1 / mpeg / unknown
	HasDTS     bool   `json:"has_dts,omitempty"`
	HasHDR     bool   `json:"has_hdr,omitempty"`   // HDR10 / HDR10+ / Dolby Vision 都算
	HasAtmos   bool   `json:"has_atmos,omitempty"` // Atmos（含 Atmos TrueHD/EAC3 混流）

	SizeBytes   int64     `json:"size_bytes,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`

	// UnlockPoints / IsUnlocked / ActualUnlockPoints 是解锁经济性字段。
	// 候选评分沿用 litepan 既有口径：已解锁优先、积分低优先。
	UnlockPoints       int  `json:"unlock_points,omitempty"`
	ActualUnlockPoints int  `json:"actual_unlock_points,omitempty"`
	IsUnlocked         bool `json:"is_unlocked,omitempty"`
	UnlockedUsersCount int  `json:"unlocked_users_count,omitempty"`

	// IsFree 当前账号免费可解锁。只由能查 ShareDetail 的通道填（hdhive），
	// 拿不到的连接器留 false，调用方按「未知」处理而非「要花钱」。
	IsFree bool `json:"is_free,omitempty"`

	// Explicit 用户显式指定。参考实现 的候选打分里这是最高权重
	// （explicit 优先于新鲜度/清晰度），litepan 目前没有手动指定路径，
	// 字段先留在契约里供第三方连接器使用。
	Explicit bool `json:"explicit,omitempty"`

	// RequiresResolve 表示必须先 Resolve 拿清单才能验证身份（磁力/种子）。
	// 连接器在 Search 里填这个字段即可，NeedsResolve 提供带分派逻辑的默认值实现。
	RequiresResolve bool `json:"requires_resolve,omitempty"`

	// SpecTags 原始规格标签（分辨率/片源/字幕），保留给日志与规则匹配用。
	SpecTags []string `json:"spec_tags,omitempty"`
	// SubtitleLanguages 字幕语言标签。
	SubtitleLanguages []string `json:"subtitle_languages,omitempty"`

	// MediaType movie / tv（连接器从标题解析得到，解析不出留空）。
	MediaType string `json:"media_type,omitempty"`
	// Season / Episode / EndEpisode 季集证据，0 = 未知。
	Season     int `json:"season,omitempty"`
	Episode    int `json:"episode,omitempty"`
	EndEpisode int `json:"end_episode,omitempty"`

	// Raw 原始条目，供第三方连接器自取字段，不参与任何判定。
	Raw map[string]any `json:"-"`
}

// Query 检索请求。
type Query struct {
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title,omitempty"`
	TMDBID        string   `json:"tmdb_id,omitempty"`
	MediaType     string   `json:"media_type,omitempty"` // movie / tv
	Season        int      `json:"season,omitempty"`
	Year          string   `json:"year,omitempty"`
	Keywords      []string `json:"keywords,omitempty"`
}

// Connector 搜索连接器。
//
// 契约（实现方必须遵守，测试里有对应断言）：
//
//	Search     必须尊重 ctx 取消；超时由调用方加，不允许自己 sleep。
//	Search     返回的每个 Item 必须 SourceKey == Key()，否则合并阶段无法归因。
//	NeedsResolve 为 true 时，Search 产出的 Item 必须能被 Resolve 还原出清单，
//	          否则该候选会在 T03 身份校验处被 IDENTITY_MANIFEST_UNAVAILABLE 挡下。
//	Resolve    返回 identity.Manifest；拿不到清单时返回 Kind=EvidenceNone 的
//	          Manifest 加一个错误，让调用方能区分「源不支持」与「源失败」。
type Connector interface {
	// Key 稳定标识，用于配置、排序、账本归因，一旦发布不可改。
	Key() string
	// Label 界面显示名。
	Label() string
	// Purpose 适用场景，空切片表示不限。
	Purpose() []string
	// Ready 是否已配置可用。未就绪的连接器会被订阅引擎从源列表里剔除，
	// 而不是每次调用都失败。
	Ready() bool
	// NeedsResolve 该候选是否需要先 Resolve 才能拿到清单。
	NeedsResolve(item Item) bool
	// Search 检索候选。失败返回错误，由调用方决定降级还是中止。
	Search(ctx context.Context, q Query) ([]Item, error)
	// Resolve 拿清单。NeedsResolve 为 false 的连接器可以返回不支持错误。
	Resolve(ctx context.Context, item Item) (identity.Manifest, error)
}

// ErrResolveUnsupported 连接器不支持 Resolve（NeedsResolve 恒 false 的实现）。
var ErrResolveUnsupported = errors.New("该连接器不提供清单解析（NeedsResolve 恒为 false）")

// ErrNotConfigured 连接器未配置。
var ErrNotConfigured = errors.New("连接器未配置")

// ErrUnsupported 连接器不支持该场景（Purpose 不含请求场景）。
var ErrUnsupported = errors.New("连接器不支持该场景")

// DefaultNeedsResolve 是 NeedsResolve 的推荐默认实现：
// 只有磁力/种子需要先拿到文件清单才能验证身份。
// 分享链接类来源由站点直接给出标题/备注，走 metadata 档即可。
func DefaultNeedsResolve(item Item) bool {
	if item.RequiresResolve {
		return true
	}
	switch item.Kind {
	case ItemMagnet, ItemTorrent:
		return true
	default:
		return false
	}
}

// MetadataManifest 把资源级元数据（标题/备注/季集）打包成 identity.Manifest。
// 网盘分享链接类候选走这条：不下载任何东西，只用站方已经给出的信息构造证据。
// 与 discovery.manifestForCandidate 是同一套分档逻辑，抽到本包是为了让
// 连接器实现与订阅引擎共用一份口径。
//
// 磁力 / ed2k / torrent 一律给 EvidenceNone：它们本身**没有资源级元数据**，
// 磁力链接里除了种子哈希什么都读不出来，ed2k 只有文件名。给它们造一个
// metadata 档等于凭空捏造证据，会让 T03 那条「未通过身份校验，一律不转存」
// 变成一句空话 —— 所以这类候选必须先过 NeedsResolve 去连接器要清单，
// 要不到就留在 EvidenceNone 被身份闸门挡下。
func MetadataManifest(item Item) identity.Manifest {
	switch item.Kind {
	case ItemMagnet, ItemTorrent, ItemEd2k, ItemDirectLink:
		return identity.Manifest{Kind: identity.EvidenceNone}
	}
	text := strings.TrimSpace(strings.Join(nonEmpty(item.Title, item.Remark), " "))
	if text == "" {
		return identity.Manifest{Kind: identity.EvidenceNone}
	}
	m := identity.Manifest{
		Kind:       identity.EvidenceMetadata,
		Titles:     nonEmpty(item.Title, item.Remark),
		MediaType:  strings.ToLower(strings.TrimSpace(item.MediaType)),
		Season:     item.Season,
		Episode:    item.Episode,
		EndEpisode: item.EndEpisode,
	}
	return m
}

func nonEmpty(vals ...string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// Supports 判断连接器是否覆盖某场景。Purpose 为空表示不限。
func Supports(c Connector, purpose string) bool {
	purpose = strings.ToLower(strings.TrimSpace(purpose))
	if purpose == "" {
		return true
	}
	purposes := c.Purpose()
	if len(purposes) == 0 {
		return true
	}
	for _, p := range purposes {
		if strings.ToLower(strings.TrimSpace(p)) == purpose {
			return true
		}
	}
	return false
}

// Registry 连接器注册表，按 Key 索引。
type Registry struct {
	order []string
	items map[string]Connector
}

// NewRegistry 建一个空注册表。
func NewRegistry() *Registry {
	return &Registry{items: map[string]Connector{}}
}

// Register 注册一个连接器。同 Key 后注册的覆盖先注册的。
func (r *Registry) Register(c Connector) {
	if c == nil || strings.TrimSpace(c.Key()) == "" {
		return
	}
	key := strings.ToLower(strings.TrimSpace(c.Key()))
	if _, exists := r.items[key]; !exists {
		r.order = append(r.order, key)
	}
	r.items[key] = c
}

// Get 按 Key 取连接器。
func (r *Registry) Get(key string) (Connector, bool) {
	c, ok := r.items[strings.ToLower(strings.TrimSpace(key))]
	return c, ok
}

// Keys 按注册顺序返回全部 Key。
func (r *Registry) Keys() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// All 按注册顺序返回全部连接器。
func (r *Registry) All() []Connector {
	out := make([]Connector, 0, len(r.order))
	for _, k := range r.order {
		out = append(out, r.items[k])
	}
	return out
}

// Resolve 按给定顺序挑出可用的连接器：
// 未注册、Ready() 为 false 的直接剔除，并把剔除原因回传给调用方记日志。
func (r *Registry) Resolve(keys []string) (ready []Connector, skipped []SkipReason) {
	seen := map[string]bool{}
	for _, raw := range keys {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		c, ok := r.items[key]
		if !ok {
			skipped = append(skipped, SkipReason{Key: key, Reason: "未注册的搜索源"})
			continue
		}
		if !c.Ready() {
			skipped = append(skipped, SkipReason{Key: key, Reason: "未配置或不可用"})
			continue
		}
		ready = append(ready, c)
	}
	return ready, skipped
}

// SkipReason 某个源被剔除的原因。
type SkipReason struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// SearchOutcome 单源检索结果（成功或失败都返回，失败不阻断其它源）。
type SearchOutcome struct {
	Key     string        `json:"key"`
	Label   string        `json:"label,omitempty"`
	Items   []Item        `json:"items"`
	Err     error         `json:"-"`
	Elapsed time.Duration `json:"-"`
	Skipped bool          `json:"skipped,omitempty"` // 因总预算耗尽被跳过（未发起请求）
}

// SearchAll 按给定顺序依次检索所有连接器。
//
// 三条硬性行为（对应验收项 5）：
//  1. 单源超时**不影响**其它源 —— 每个源独立 ctx，超时只让这个源返回错误。
//  2. 单源报错**不影响**其它源 —— 错误落在对应 SearchOutcome 上，循环继续。
//  3. 总预算耗尽后剩余源**不发起请求**并标记 Skipped。
//
// 顺序执行而不是并发：订阅候选本身要按源顺序稳定排序（存量的 tgto123 结果必须
// 与改动前逐条一致，见变更说明），并发拿回来的顺序不定，排序稳定性就没法保证。
func SearchAll(ctx context.Context, connectors []Connector, q Query, perSourceTimeout, totalBudget time.Duration) []SearchOutcome {
	outcomes := make([]SearchOutcome, 0, len(connectors))
	if len(connectors) == 0 {
		return outcomes
	}
	var deadline time.Time
	if totalBudget > 0 {
		deadline = time.Now().Add(totalBudget)
	}
	for _, c := range connectors {
		out := SearchOutcome{Key: c.Key(), Label: c.Label()}
		if perSourceTimeout <= 0 {
			perSourceTimeout = DefaultConnectorTimeout
		}
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				out.Skipped = true
				out.Err = ErrBudgetExhausted
				outcomes = append(outcomes, out)
				continue
			}
			if remaining < perSourceTimeout {
				perSourceTimeout = remaining
			}
		}
		callCtx, cancel := context.WithTimeout(ctx, perSourceTimeout)
		start := time.Now()
		items, err := c.Search(callCtx, q)
		cancel()
		out.Elapsed = time.Since(start)
		out.Items = items
		out.Err = err
		outcomes = append(outcomes, out)
	}
	return outcomes
}

// DefaultConnectorTimeout 单源默认超时，与 参考实现 search_connectors.timeout_seconds=30 一致。
const DefaultConnectorTimeout = 30 * time.Second

// ErrBudgetExhausted 单轮搜索总预算耗尽，剩余源未发起请求。
var ErrBudgetExhausted = errors.New("单轮搜索总预算耗尽")

// Merge 把多个源的结果合并去重。
//
// 去重键刻意复用 T04 账本的 LedgerIdempotencyKey 口径
// （storage_slug + content_key + media_scope）：
// 同一个资源被两个源同时搜到时 content_key 相同（都是 res:<slug>），
// 只保留先出现的那个。保留策略是**先出现的赢**，因为 connectors 的顺序
// 就是用户在订阅里配置的优先级顺序。
//
// 返回的 items 按「源顺序 → 源内原顺序」排列，不在这里重排：
// 排序是订阅引擎的事（candidateSortKey 口径），这里只管去重与归一。
func Merge(outcomes []SearchOutcome) ([]Item, []Dropped) {
	items := make([]Item, 0, 16)
	dropped := make([]Dropped, 0, 8)
	seen := map[string]string{} // dedupKey → 保留它的 source key
	for _, o := range outcomes {
		if o.Err != nil || len(o.Items) == 0 {
			continue
		}
		for _, it := range o.Items {
			if it.SourceKey == "" {
				it.SourceKey = o.Key
			}
			key := DedupKey(it)
			if prev, dup := seen[key]; dup {
				dropped = append(dropped, Dropped{
					Key: o.Key, Title: it.Title, DedupKey: key, KeptFrom: prev,
				})
				continue
			}
			seen[key] = o.Key
			items = append(items, it)
		}
	}
	return items, dropped
}

// Dropped 一条被跨源去重丢弃的候选。
type Dropped struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	DedupKey string `json:"dedup_key"`
	KeptFrom string `json:"kept_from"`
}

// DedupKey 计算跨源去重键。
// content_key 与 T04 ledgerContentKey 同一口径：有 sha1 用 sha1:<hash>，否则 res:<slug>。
func DedupKey(it Item) string {
	if hash := NormalizeInfoHash(it.InfoHash); hash != "" {
		return "sha1:" + hash
	}
	// 磁力链接里的种子哈希是**内容身份**，优先于 slug：同一部片子在两个源里
	// 分享码不同、标题不同是常事，但种子哈希一定是同一份文件。
	// 不能只在 slug 为空时才看磁力 —— 那会漏掉「A 源带 slug、B 源只给磁力」
	// 这对同一份内容（而这恰恰是最常见的跨源重复形态）。
	if hash := infoHashFromMagnet(it.MagnetURI); hash != "" {
		return "sha1:" + hash
	}
	slug := strings.ToLower(strings.TrimSpace(firstNonEmpty(it.Slug, it.ShareCode)))
	if slug == "" {
		slug = strings.ToLower(strings.TrimSpace(it.Title))
	}
	if slug == "" {
		// 什么都没有的条目没有稳定身份：按源内序号退化，保证同一条不会被自己吃掉。
		slug = fmt.Sprintf("empty:%s", it.SourceKey)
	}
	return "res:" + slug
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ParseKeys 解析逗号分隔的源 key 列表（配置项与订阅列都用这个格式）。
func ParseKeys(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '|' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, f := range fields {
		key := strings.ToLower(strings.TrimSpace(f))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// SortItems 按 参考实现 的候选打分口径排序：显式指定 > 新鲜度 > 清晰度 > 码率。
//
// 这只用于**多源合并后**的重排，订阅引擎的主排序仍是既有的 candidateSortKey
// （已解锁优先 → 积分低优先 → 解锁人数），两者叠加成主键 + tie-break。
// 之所以不整体替换：验收项 1 要求存量订阅（默认只有 tgto123 一个源）
// 跑出来的结果与改动前一致。
func SortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		return ItemLess(items[i], items[j])
	})
}

// ItemLess 两个候选的先后（true = it 应排在 other 前面）。
//
// 与 SortItems 用的是同一个判定，单独暴露是为了让订阅引擎把「既有主键排序」
// 和「本文件的跨源排序」组合成一个比较函数时，不必先排序再回填位置。
func ItemLess(it, other Item) bool {
	return itemScore(it) > itemScore(other)
}

func itemScore(it Item) int {
	score := 0
	if it.Explicit {
		score += 1 << 20
	}
	// 新鲜度：一年内线性给到 1<<12，够压过清晰度但压不过 Explicit。
	if !it.PublishedAt.IsZero() {
		age := time.Since(it.PublishedAt)
		switch {
		case age < 0:
			score += 1 << 12
		case age < 365*24*time.Hour:
			score += 1<<12 - int(age/(24*time.Hour))*8
		}
	}
	score += it.Resolution / 240 // 2160 → 9
	score += CodecRank(it.Codec) * 2
	return score
}

// CodecRank 编码权重（越高越优）。
//
// ⚠️ 这里与 internal/moviepilot 的 codecRank 口径**故意不同**：moviepilot 那份是
// 「洗版画质比较」，av1 与 hevc 同为最高档（3）；参考实现 的 subscription_candidate
// 传输权重是 h265/hevc > h264 > av1，把 av1 压在最后。本函数跟 参考实现，
// 因为这里排的是「先转哪个」，受**客户端解码兼容性**与**压制组生态**约束更大
// —— av1 在国内网盘资源里生态最差，不是画质最好就值得优先。
func CodecRank(codec string) int {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "h265", "hevc", "x265":
		return 3
	case "h264", "avc", "x264":
		return 2
	case "av1":
		return 1
	case "mpeg", "mpeg4", "xvid", "divx":
		return 0
	default:
		return 0
	}
}
