package app

import (
	"context"
	"strings"

	"litepan/internal/discover/embyclient"
	"litepan/internal/embyproxy"
)

// 未识别兜底里「跟随 Emby 已有位置」的装配（T14）。
//
// 只支持 Emby 一个媒体库实现：Jellyfin/本地媒体库没有同一套
// 按名称反查的接口，硬接只会做出一个看着能用、实际查不到的开关。
//
// 实现挂在 embyproxy.Service 上而不是自建客户端，是为了和「Emby 本地索引」
// 走同一份 LiveConfigs —— 那里面是明文 API Key，用脱敏快照去请求会 401。

func wireEmbyLocationLookup(proxy *embyproxy.Service) func(ctx context.Context, accountID int64, title string, year *int) string {
	return func(ctx context.Context, _ int64, title string, year *int) string {
		if proxy == nil {
			return ""
		}
		for _, cfg := range proxy.LiveConfigs() {
			if strings.TrimSpace(cfg.EmbyURL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
				continue
			}
			item, err := embyclient.NewClient(cfg.EmbyURL, cfg.APIKey).
				FindItemByTitle(ctx, title, year, false)
			if err != nil {
				return ""
			}
			// 查不到就是「没找到」，不是「出错了」——两者对兜底的影响完全一样
			// （都用兜底目录），但把错误往上抛只会让一次网络抖动变成整条兜底路径失效。
			return embyclient.WorkPath(item)
		}
		return ""
	}
}
