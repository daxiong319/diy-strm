package app

import (
	"context"
	"strings"

	"litepan/internal/embyindex"
	"litepan/internal/embyproxy"
	"litepan/internal/embyrefresh"
	"litepan/internal/settings"
)

// embyindexRefreshSink 把 embyindex 的刷新意图桥接到自动化服务的刷新队列。
//
// 两边刻意不互相依赖：embyindex 只认自己的 RefreshIntent，
// embyrefresh 只认自己的 RequestRefreshParams，翻译放在装配层完成。
type embyindexRefreshSink struct {
	register func(ctx context.Context, req embyrefresh.RequestRefreshParams) error
}

// RegisterRefreshIntent 实现 embyindex.RefreshIntentSink。
func (s embyindexRefreshSink) RegisterRefreshIntent(ctx context.Context, intent embyindex.RefreshIntent) error {
	if s.register == nil {
		return nil
	}
	return s.register(ctx, embyrefresh.RequestRefreshParams{
		TargetType:  intent.TargetType,
		LibraryID:   intent.LibraryID,
		LibraryName: intent.LibraryName,
		ItemID:      intent.ItemID,
	})
}

// embyIndexConfigLoader 返回「取当前生效 Emby 配置」的回调，供 embyindex 周期扫描使用。
//
// 这里在装配层做投影，而不是让 embyindex 直接依赖 embyproxy：
// 依赖方向保持 embyproxy → app → embyindex，避免两包互相引用。
//
// 选中媒体库的范围：现版没有「勾选媒体库」的设置项，因此 AllSelected 恒为 true，
// 即扫描 Emby 上全部媒体库——与老版 SyncAllLibraries 的默认行为一致。
func embyIndexConfigLoader(proxy *embyproxy.Service, st *settings.Service) embyindex.ConfigLoader {
	return func(ctx context.Context) (embyindex.Config, bool) {
		if proxy == nil {
			return embyindex.Config{}, false
		}
		// 必须用 LiveConfigs 而不是 Snapshots：后者会给前端返回脱敏后的 API Key，
		// 用它去请求 Emby 会 401。
		configs := proxy.LiveConfigs()
		for _, cfg := range configs {
			if strings.TrimSpace(cfg.EmbyURL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
				continue
			}
			return embyindex.BuildConfig(cfg.ID, cfg.Name, cfg.EmbyURL, cfg.APIKey, nil, true), true
		}
		return embyindex.Config{}, false
	}
}
