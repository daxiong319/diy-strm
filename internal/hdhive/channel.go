package hdhive

import "context"

// ChannelClient 影巢通道统一接口：symedia 中转（*SymediaClient）与 tgtodrive 中转
// （*OAuthClient）同构实现，上层按账号通道选择客户端并做故障切换
type ChannelClient interface {
	Ping(ctx context.Context) error
	Me(ctx context.Context) (*OAuthAPIResponse, error)
	GetResources(ctx context.Context, mediaType, tmdbID string) (*OAuthAPIResponse, error)
	GetShareDetail(ctx context.Context, slug string) (*OAuthAPIResponse, error)
	UnlockResource(ctx context.Context, slug string) (*OAuthAPIResponse, error)
	Checkin(ctx context.Context, isGambler bool) (*OAuthAPIResponse, error)
}

// FeedClient 影巢 Feed 能力接口（榜单推荐 + 追剧日历数据源，与 tgto123 的
// HDHive feed 一致）。通道客户端可选择性实现；不支持时上层降级为 TMDB/豆瓣榜单。
type FeedClient interface {
	GetStreamingTop(ctx context.Context, provider, region, mediaType string) (*OAuthAPIResponse, error)
	GetCalendar(ctx context.Context, days int) (*OAuthAPIResponse, error)
}
