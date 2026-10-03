package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"litepan/internal/offlinedownload"
)

// ---------------------------------------------------------------------------
// 队列类工具（离线下载）
// ---------------------------------------------------------------------------

// OfflineLister 是本包对离线下载服务的依赖（只读）。
// 用接口而非具体类型：便于测试注入，也避免工具层紧绑某个具体服务实现。
type OfflineLister interface {
	List(ctx context.Context, accountID int64, refresh bool) ([]offlinedownload.Task, error)
}

// offlineService 保存由宿主注入的离线下载服务。
var offlineService OfflineLister

// RegisterOfflineDownloader 注入离线下载服务（由 internal/api 在初始化时调用）。
func RegisterOfflineDownloader(svc OfflineLister) {
	hookMu.Lock()
	defer hookMu.Unlock()
	offlineService = svc
}

// offlineListTool 列出离线下载任务。
type offlineListTool struct{}

func (offlineListTool) Name() string { return "download_queue_list" }

func (offlineListTool) Description() string {
	return "列出离线下载任务及其进度、状态。可指定账号 ID 过滤。"
}

func (offlineListTool) InputSchema() json.RawMessage {
	return objectSchema(nil, map[string]any{
		"account_id": intProp("网盘账号 ID；0 或不传表示全部账号"),
		"refresh":    boolPropDefault("是否强制刷新一次任务状态（较慢），默认 false", false),
	})
}

func (offlineListTool) ReadOnly() bool { return true }

func (offlineListTool) Handler(ctx context.Context, args map[string]any) (any, error) {
	hookMu.RLock()
	svc := offlineService
	hookMu.RUnlock()
	if svc == nil {
		return nil, ErrRunnerNotRegistered
	}
	accountID := int64(argUint(args, "account_id"))
	tasks, err := svc.List(ctx, accountID, argBool(args, "refresh", false))
	if err != nil {
		return nil, fmt.Errorf("查询离线下载任务失败：%w", err)
	}
	// 只回传 LLM 需要判断的字段，避免把诊断信息灌进上下文。
	items := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, map[string]any{
			"task_id":      task.TaskID,
			"name":         task.Name,
			"account_id":   task.AccountID,
			"account_name": task.AccountName,
			"status":       task.Status,
			"phase":        task.Phase,
			"progress":     task.Progress,
			"size":         task.Size,
			"speed_bytes":  task.SpeedBytes,
			"message":      task.Message,
			"error":        task.Error,
		})
	}
	return map[string]any{"total": len(items), "items": items}, nil
}
