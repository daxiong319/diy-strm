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
