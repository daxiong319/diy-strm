package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"litepan/internal/moviepilot"
)

// ---------------------------------------------------------------------------
// MoviePilot 工具
//
// MoviePilot 服务是包级单例式注入：本包只保存接口，由宿主注册。
// ---------------------------------------------------------------------------

// MoviePilotService 是本包对 MoviePilot 的依赖。
type MoviePilotService interface {
	ListSubscribes(ctx context.Context) ([]*moviepilot.Subscribe, error)
	SubscribeSearch(ctx context.Context, subscribeID int64) error
	TestConnection(ctx context.Context, baseURL, token string) error
	ListDownloads(ctx context.Context) ([]*moviepilot.DownloadTorrent, error)
}

var moviePilotService MoviePilotService

// RegisterMoviePilot 注入 MoviePilot 服务。
func RegisterMoviePilot(svc MoviePilotService) {
	hookMu.Lock()
	defer hookMu.Unlock()
	moviePilotService = svc
}

// mpService 取已注入的 MoviePilot 服务。
func mpService() (MoviePilotService, error) {
	hookMu.RLock()
	svc := moviePilotService
	hookMu.RUnlock()
	if svc == nil {
		return nil, ErrRunnerNotRegistered
	}
	return svc, nil
}

// mediaPilotSubscribesTool 列出 MoviePilot 订阅。
type mediaPilotSubscribesTool struct{}

func (mediaPilotSubscribesTool) Name() string { return "moviepilot_subscription_list" }

func (mediaPilotSubscribesTool) Description() string {
	return "列出 MoviePilot 中的订阅及其状态。"
}

func (mediaPilotSubscribesTool) InputSchema() json.RawMessage {
	return objectSchema(nil, nil)
}

func (mediaPilotSubscribesTool) ReadOnly() bool { return true }

func (mediaPilotSubscribesTool) Handler(ctx context.Context, _ map[string]any) (any, error) {
	svc, err := mpService()
	if err != nil {
		return nil, err
	}
	subs, err := svc.ListSubscribes(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询 MoviePilot 订阅失败：%w", err)
	}
	return map[string]any{"total": len(subs), "items": subs}, nil
}

// mediaPilotDownloadsTool 列出 MoviePilot 下载任务。
type mediaPilotDownloadsTool struct{}

func (mediaPilotDownloadsTool) Name() string { return "moviepilot_download_list" }

func (mediaPilotDownloadsTool) Description() string {
	return "列出 MoviePilot 中的下载任务。"
}

func (mediaPilotDownloadsTool) InputSchema() json.RawMessage {
	return objectSchema(nil, nil)
}

func (mediaPilotDownloadsTool) ReadOnly() bool { return true }

func (mediaPilotDownloadsTool) Handler(ctx context.Context, _ map[string]any) (any, error) {
	svc, err := mpService()
	if err != nil {
		return nil, err
	}
	items, err := svc.ListDownloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询 MoviePilot 下载任务失败：%w", err)
	}
	return map[string]any{"total": len(items), "items": items}, nil
}

// mediaPilotSearchTool 触发 MoviePilot 订阅搜索（写操作）。
type mediaPilotSearchTool struct{}

func (mediaPilotSearchTool) Name() string { return "moviepilot_subscription_search" }

func (mediaPilotSearchTool) Description() string {
	return "让 MoviePilot 立即为指定订阅搜索一次资源（可能触发下载）。调用前必须先向用户确认。"
}

func (mediaPilotSearchTool) InputSchema() json.RawMessage {
	return objectSchema([]string{"id"}, map[string]any{
		"id": intProp("MoviePilot 订阅 ID"),
	})
}

func (mediaPilotSearchTool) ReadOnly() bool { return false }

func (mediaPilotSearchTool) Handler(ctx context.Context, args map[string]any) (any, error) {
	id, err := requireUint(args, "id", "订阅 ID")
	if err != nil {
		return nil, err
	}
	svc, err := mpService()
	if err != nil {
		return nil, err
	}
	if err := svc.SubscribeSearch(ctx, int64(id)); err != nil {
		return nil, fmt.Errorf("触发订阅搜索失败：%w", err)
	}
	return map[string]any{
		"subscription_id": id,
		"message":         fmt.Sprintf("已触发订阅 #%d 的搜索", id),
	}, nil
}

// mediaPilotTestTool 测试 MoviePilot 连通性。
type mediaPilotTestTool struct{}

func (mediaPilotTestTool) Name() string { return "moviepilot_test_connection" }

func (mediaPilotTestTool) Description() string {
	return "测试 MoviePilot 服务地址与令牌是否可用。不传参数时使用已保存的配置。"
}

func (mediaPilotTestTool) InputSchema() json.RawMessage {
	return objectSchema(nil, map[string]any{
		"base_url": strProp("MoviePilot 地址；留空使用已保存配置"),
		"token":    strProp("MoviePilot API 令牌；留空使用已保存配置"),
	})
}

func (mediaPilotTestTool) ReadOnly() bool { return true }

func (mediaPilotTestTool) Handler(ctx context.Context, args map[string]any) (any, error) {
	svc, err := mpService()
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSpace(argString(args, "base_url"))
	token := strings.TrimSpace(argString(args, "token"))
	if err := svc.TestConnection(ctx, baseURL, token); err != nil {
		return nil, fmt.Errorf("MoviePilot 连接失败：%w", err)
	}
	return map[string]any{"ok": true, "message": "MoviePilot 连接成功"}, nil
}
