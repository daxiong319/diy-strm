package discovery

// T05 · hdhive（RE0 官方通道）搜索连接器适配器。
//
// 与 tgto123 的关键区别，也是为什么值得包第二个源：**hdhive 通道拿得到 ShareDetail**，
// 也就是拿得到 `IsFreeForUser` / `ActualUnlockPoints`。参考实现 的 115 连接器
// 标的正是「exact / 积分解锁」—— 在决定转不转之前先问一句「这次解锁要花我多少积分、
// 是不是免费」。litepan 原先只能在转存之后才知道花没花钱。
//
// 当前状态（照实说，不要当成已上线）：
//   litepan 里 hdhive 的四个通道客户端（OAuthClient / OfficialClient / SymediaClient /
//   NanShareClient）都实现了 hdhive.ChannelClient，但**没有一个装配点**——
//   internal/discover/discovery/explore.go 的 feedExecute 注释写明
//   「symedia/tgtodrive/nanshare/official 四通道的 feeds 上游已全部 404/下线」。
//   所以本连接器默认 Ready() 为 false，订阅引擎会把它从源列表里剔除并记日志。
//   接上通道客户端只需调 HdHiveChannelFactory，不需要动本文件任何逻辑。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"litepan/internal/discover/connector"
	"litepan/internal/discover/hdhive"
	"litepan/internal/discover/identity"
)

// KeyHdHive 连接器 key。
const KeyHdHive = "hdhive"

// HdHiveChannelFactory 返回一个可用的 RE0 通道客户端；返回 nil 表示未装配。
// 由装配层（未来的 OAuth/官方通道设置项）注入。
var HdHiveChannelFactory func(ctx context.Context) hdhive.ChannelClient

var hdHiveFactoryMu sync.RWMutex

// SetHdHiveChannelFactory 注入通道工厂（设置保存时调用；传 nil 表示卸掉）。
func SetHdHiveChannelFactory(fn func(ctx context.Context) hdhive.ChannelClient) {
	hdHiveFactoryMu.Lock()
	defer hdHiveFactoryMu.Unlock()
	HdHiveChannelFactory = fn
}

func hdHiveChannel(ctx context.Context) hdhive.ChannelClient {
	hdHiveFactoryMu.RLock()
	fn := HdHiveChannelFactory
	hdHiveFactoryMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

// HdHiveConnector RE0 官方通道连接器。
type HdHiveConnector struct{}

// NewHdHiveConnector 构造 hdhive 连接器。
func NewHdHiveConnector() *HdHiveConnector { return &HdHiveConnector{} }

func (c *HdHiveConnector) Key() string   { return KeyHdHive }
func (c *HdHiveConnector) Label() string { return "HDHive (RE0 官方通道)" }

// Purpose 与 参考实现 的 hdhive 连接器对齐：订阅 + 手动 + 机器人 + 助理。
// ⚠️ movie/tv 不写进 Purpose —— RE0 官方通道对电影/剧集分资源列表，
// 由 Query.MediaType 在 Search 里分派，不在能力声明里假装都能搜。
func (c *HdHiveConnector) Purpose() []string {
	return []string{
		connector.PurposeSubscriptions, connector.PurposeManual,
		connector.PurposeBot, connector.PurposeAgent,
	}
}

// Ready 通道客户端是否已装配。
func (c *HdHiveConnector) Ready() bool { return hdHiveChannel(context.Background()) != nil }

func (c *HdHiveConnector) NeedsResolve(item connector.Item) bool {
	return connector.DefaultNeedsResolve(item)
}

// Search 检索 RE0 资源（GET /api/resources/{mediaType}/{tmdbID}）。
func (c *HdHiveConnector) Search(ctx context.Context, q connector.Query) ([]connector.Item, error) {
	channel := hdHiveChannel(ctx)
	if channel == nil {
		return nil, connector.ErrNotConfigured
	}
	tmdbID := strings.TrimSpace(q.TMDBID)
	if tmdbID == "" {
		return nil, fmt.Errorf("HDHive 资源检索按 TMDB ID 定位，缺少 TMDB ID")
	}
	mediaType := strings.ToLower(strings.TrimSpace(q.MediaType))
	if mediaType != "tv" {
		mediaType = "movie"
	}
	resp, err := channel.GetResources(ctx, mediaType, tmdbID)
	if err != nil {
		return nil, err
	}
	resources, err := decodeHiveResources(resp)
	if err != nil {
		return nil, err
	}
	items := make([]connector.Item, 0, len(resources))
	for _, r := range resources {
		it := ConnectorItemFromHive(r)
		it.SourceKey = KeyHdHive
		// 官方通道的 PanType 语义与反代一致，但来源标记要区分开，
		// 否则跨源去重时无法归因「这条是哪个源先给出的」。
		connector.ApplyQuality(&it)
		items = append(items, it)
	}
	return items, nil
}

// Resolve 拉 ShareDetail，拿免费/实际积分判断，构造 metadata 档证据。
//
// 注意：ShareDetail 仍然**不含文件清单**（hdhive.ShareDetail 结构体里没有文件数组），
// 所以这里产出的是 metadata 档而不是 manifest 档 —— 磁力/ed2k 候选经这条路
// 依然拿不到清单，会在 T03 身份校验处被判 IDENTITY_MANIFEST_UNAVAILABLE。
// 这不是 bug，是这条上游接口的固有限制，与 T04 的偏差②是同一件事。
func (c *HdHiveConnector) Resolve(ctx context.Context, item connector.Item) (identity.Manifest, error) {
	if !connector.DefaultNeedsResolve(item) {
		return connector.MetadataManifest(item), nil
	}
	channel := hdHiveChannel(ctx)
	if channel == nil {
		return identity.Manifest{Kind: identity.EvidenceNone}, connector.ErrNotConfigured
	}
	slug := firstNonEmptyStr(item.Slug, item.ShareCode)
	if slug == "" {
		return identity.Manifest{Kind: identity.EvidenceNone}, fmt.Errorf("候选没有 slug，无法查分享详情")
	}
	resp, err := channel.GetShareDetail(ctx, slug)
	if err != nil {
		return identity.Manifest{Kind: identity.EvidenceNone}, err
	}
	m := connector.MetadataManifest(item)
	if resp == nil || len(resp.Data) == 0 {
		return m, nil
	}
	var detail hdhive.ShareDetail
	if err := json.Unmarshal(resp.Data, &detail); err != nil {
		return m, nil // 详情拿不到不降级已有证据
	}
	if detail.Media != nil {
		if detail.Media.TMDBID != "" {
			m.TMDBID = detail.Media.TMDBID
		}
		if detail.Media.Title != "" {
			m.Titles = append(m.Titles, detail.Media.Title)
		}
		if season := atoiSafe(detail.Media.Season, 0); season > 0 {
			m.Season = season
		}
	}
	// 证据升档：候选本身是磁力/ed2k（MetadataManifest 给了 EvidenceNone），
	// 但分享详情带回了站方对这份分享的媒体绑定 —— TMDB ID 与官方标题。
	// 这是**站方声明**，比资源标题里的压制组写法强，
	// 所以证据档位可以提升到 metadata，让 T03 的身份闸门正常走校验。
	if m.Kind == identity.EvidenceNone && (m.TMDBID != "" || len(m.Titles) > 0) {
		m.Kind = identity.EvidenceMetadata
	}
	// 注意：detail.IsFreeForUser / detail.ActualUnlockPoints（解锁要不要花积分）
	// 没能回传 —— Connector.Resolve 的签名只返回 identity.Manifest，
	// 没有位置放这类「转存经济性」信息。要用得改契约（见文件顶部说明）。
	return m, nil
}

// Unlock 解锁资源（POST /api/resources/unlock）。
// 不属于 Connector 契约（契约只有检索与清单），但订阅引擎要用它把
// 「付费解锁」这一步显式化：先问价、再决定要不要花积分。
func (c *HdHiveConnector) Unlock(ctx context.Context, slug string) (*hdhive.UnlockResult, error) {
	channel := hdHiveChannel(ctx)
	if channel == nil {
		return nil, connector.ErrNotConfigured
	}
	resp, err := channel.UnlockResource(ctx, slug)
	if err != nil {
		return nil, err
	}
	if resp == nil || !resp.Success {
		msg := "HDHive 解锁失败"
		if resp != nil {
			msg = firstNonEmptyStr(resp.Message, resp.Description, msg)
		}
		return nil, fmt.Errorf("%s", msg)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("HDHive 解锁无返回数据")
	}
	var out hdhive.UnlockResult
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return nil, fmt.Errorf("解析 HDHive 解锁结果失败：%v", err)
	}
	return &out, nil
}

// decodeHiveResources 把 OAuthAPIResponse 解成资源列表。
// RE0 的 data 可能是数组，也可能是 {items:[...]}，两种都收。
func decodeHiveResources(resp *hdhive.OAuthAPIResponse) ([]hdhive.Resource, error) {
	if resp == nil {
		return nil, fmt.Errorf("HDHive 无响应")
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", firstNonEmptyStr(resp.Message, resp.Description, "HDHive 资源检索失败"))
	}
	if len(resp.Data) == 0 {
		return nil, nil
	}
	var list []hdhive.Resource
	if err := json.Unmarshal(resp.Data, &list); err == nil {
		return list, nil
	}
	var wrapped struct {
		Items []hdhive.Resource `json:"items"`
		Data  []hdhive.Resource `json:"data"`
	}
	if err := json.Unmarshal(resp.Data, &wrapped); err != nil {
		return nil, fmt.Errorf("解析 HDHive 资源响应失败：%v", err)
	}
	if len(wrapped.Items) > 0 {
		return wrapped.Items, nil
	}
	return wrapped.Data, nil
}
