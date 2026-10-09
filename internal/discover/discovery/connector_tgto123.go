package discovery

// T05 · tgto123 搜索连接器适配器。
//
// 位置说明：Connector 接口定义在 internal/discover/connector（纯类型包，方便 bot/
// 手动搜索等模块复用），但**适配器必须放在本包**——它要调用
// Tgto123SearchResources，那是本包的函数；connector 包 import discovery 会成环。
// 接口留外面、实现放里面，是这个循环依赖下唯一不别扭的切法。
//
// 为什么第一个包它：它已经在订阅流水线上（订阅执行器原来就写死调它），
// 包完之后行为完全不变，回归风险最低。第二源 hdhive 用来验形状。

import (
	"context"
	"fmt"
	"strings"

	"litepan/internal/discover/connector"
	"litepan/internal/discover/hdhive"
	"litepan/internal/discover/identity"
)

// KeyTgto123 连接器 key（存进订阅的 search_sources 列，发布后不改）。
const KeyTgto123 = "tgto123"

// Tgto123Connector tgto123 反代连接器。
// 检索能力来自 Tgto123SearchResources（POST /api/media/resources/search，sources=re0），
// 它把解锁与转存压成一次调用，所以本连接器没有 Resolve ——
// 候选一律是分享链接，走 identity.Manifest 的 metadata 档。
type Tgto123Connector struct{}

// NewTgto123Connector 构造 tgto123 连接器。
func NewTgto123Connector() *Tgto123Connector { return &Tgto123Connector{} }

func (c *Tgto123Connector) Key() string   { return KeyTgto123 }
func (c *Tgto123Connector) Label() string { return "TG转存服务 (RE0)" }

// Purpose tgto123 覆盖订阅/手动/机器人/助理与两种媒体类型
// —— 它是当前唯一在订阅流水线上跑通的源，不能收窄。
func (c *Tgto123Connector) Purpose() []string {
	return []string{
		connector.PurposeSubscriptions, connector.PurposeManual, connector.PurposeBot,
		connector.PurposeAgent, connector.PurposeMovie, connector.PurposeTV,
	}
}

// Ready 反代地址是否已配置（未配置时不发起注定失败的请求）。
func (c *Tgto123Connector) Ready() bool { return Tgto123ConfiguredURL() != "" }

// NeedsResolve 本连接器的候选都是分享链接，不需要先拿清单。
func (c *Tgto123Connector) NeedsResolve(item connector.Item) bool {
	return connector.DefaultNeedsResolve(item)
}

// Search 检索 RE0 资源。
func (c *Tgto123Connector) Search(ctx context.Context, q connector.Query) ([]connector.Item, error) {
	tmdbID := int64(0)
	if raw := strings.TrimSpace(q.TMDBID); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &tmdbID); err != nil {
			tmdbID = 0
		}
	}
	title := firstNonEmptyStr(q.Title, q.OriginalTitle)
	if title == "" && tmdbID <= 0 {
		return nil, fmt.Errorf("RE0 资源搜索需要标题或 TMDB ID")
	}
	resources, err := Tgto123SearchResources(ctx, title, tmdbID, q.MediaType, q.Year)
	if err != nil {
		return nil, err
	}
	items := make([]connector.Item, 0, len(resources))
	for _, r := range resources {
		it := ConnectorItemFromHive(r)
		it.SourceKey = KeyTgto123
		items = append(items, it)
	}
	return items, nil
}

// Resolve 不支持：本连接器的候选都不 NeedsResolve。
func (c *Tgto123Connector) Resolve(ctx context.Context, item connector.Item) (identity.Manifest, error) {
	return identity.Manifest{Kind: identity.EvidenceNone}, connector.ErrResolveUnsupported
}

// ConnectorItemFromHive RE0/HDHive 资源 → 归一化候选。
//
// 这是 tgto123 与 hdhive 两个连接器共用的转换（两者返回同一种 hdhive.Resource），
// 也保持与既有 resourceFromHive 同口径：来源标记、季集证据、积分字段都对齐，
// 免得包了接口之后订阅引擎看到的候选变了样。
func ConnectorItemFromHive(r hdhive.Resource) connector.Item {
	linkType := strings.ToLower(strings.TrimSpace(r.PanType))
	it := connector.Item{
		Kind:               connector.ClassifyKind(linkType),
		ShareCode:          r.Slug,
		Slug:               r.Slug,
		Title:              r.Title,
		Provider:           strings.ToLower(strings.TrimSpace(r.PanType)),
		Remark:             r.Remark,
		SizeBytes:          connector.ParseSizeBytes(r.ShareSize),
		IsUnlocked:         r.IsUnlocked,
		UnlockPoints:       r.UnlockPoints,
		ActualUnlockPoints: r.UnlockPoints,
		UnlockedUsersCount: r.UnlockedUsersCount,
		SpecTags:           append(append([]string{}, r.VideoResolution...), r.Source...),
		SubtitleLanguages:  r.SubtitleLanguage,
		// 原始资源挂在 Raw 上：订阅引擎把 Item 转回候选时直接复用既有的
		// resourceFromHive（同一个函数、同一个输入），保证「默认只有 tgto123」
		// 这条路径的候选与接口化之前逐字节一致。Item 上的归一化字段是给
		// 新代码（闸门、跨源排序、跨源去重）用的，不是给存量路径用的。
		Raw: map[string]any{hiveResourceRawKey: r},
	}
	// 站方标签先填画质（盘方判定优先），标题解析补齐其余维度。
	connector.ApplySpecTags(&it, r.VideoResolution)
	connector.ApplyQuality(&it)
	// 季集证据：与 resourceFromHive 用同一个解析器（extractEpisodeEvidence），
	// 保证连接器产出的 MediaType/季/集与既有 resourceFromHive 完全一致。
	mediaType, ep := extractEpisodeEvidence(it.Title, it.Remark)
	it.MediaType = mediaType
	if ep != nil {
		it.Season = derefInt(ep.SeasonNum, 0)
		it.Episode = derefInt(ep.EpisodeNum, 0)
		it.EndEpisode = derefInt(ep.EndEpisodeNum, 0)
	}
	return it
}
