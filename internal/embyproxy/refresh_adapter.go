package embyproxy

import (
	"context"

	"litepan/internal/embyrefresh"
)

// RefreshTaskAdapter 把 embyproxy.Service 适配为 embyrefresh 所需的接口。
//
// 之所以放在本包而不是 embyrefresh：依赖方向保持 embyproxy → embyrefresh，
// 由本包负责把 embyproxy 的请求/结果类型转换为 embyrefresh 的最小投影类型，
// 避免两个包互相引用。
type RefreshTaskAdapter struct {
	svc *Service
}

// NewRefreshTaskAdapter 构造适配器；svc 为 nil 时返回 nil。
func NewRefreshTaskAdapter(svc *Service) *RefreshTaskAdapter {
	if svc == nil {
		return nil
	}
	return &RefreshTaskAdapter{svc: svc}
}

// RefreshLibrary 实现 embyrefresh.Refresher。
func (a *RefreshTaskAdapter) RefreshLibrary(ctx context.Context, req embyrefresh.RefreshRequest) (embyrefresh.RefreshResult, error) {
	if a == nil || a.svc == nil {
		return embyrefresh.RefreshResult{}, nil
	}
	result, err := a.svc.RefreshLibrary(ctx, RefreshRequest{
		ConfigID:  req.ConfigID,
		Mode:      req.Mode,
		LibraryID: req.LibraryID,
		ItemID:    req.ItemID,
	})
	if err != nil {
		return embyrefresh.RefreshResult{}, err
	}
	return embyrefresh.RefreshResult{
		Mode:      result.Mode,
		LibraryID: result.LibraryID,
		ItemID:    result.ItemID,
	}, nil
}

// ListLibraries 实现 embyrefresh.Libraries。
func (a *RefreshTaskAdapter) ListLibraries(ctx context.Context, configIDs ...string) ([]embyrefresh.Library, error) {
	if a == nil || a.svc == nil {
		return nil, nil
	}
	libs, err := a.svc.ListLibraries(ctx, configIDs...)
	if err != nil {
		return nil, err
	}
	out := make([]embyrefresh.Library, 0, len(libs))
	for _, lib := range libs {
		out = append(out, embyrefresh.Library{ID: lib.ID, Name: lib.Name})
	}
	return out, nil
}
