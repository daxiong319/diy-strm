package app

import (
	"context"
	"fmt"
	"strings"

	"litepan/internal/upload"
)

// moviePilotBatchAdapter 让 MoviePilot 服务能查询某个上传批次（本服务创建的文件上传任务）的收敛情况。
//
// 上传管理器按 ClientTaskID 记录任务，本服务创建的任务统一使用
// `moviepilot-<任务ID>:` 前缀，因此按前缀过滤即可得到该批次的全部文件任务。
// 状态判定与 upload 包一致：success / failed / canceled / skipped 视为终态。
type moviePilotBatchAdapter struct {
	uploads *upload.Manager
}

// NewMoviePilotBatchAdapter 构造批次查询适配器。
func newMoviePilotBatchAdapter(uploads *upload.Manager) *moviePilotBatchAdapter {
	if uploads == nil {
		return nil
	}
	return &moviePilotBatchAdapter{uploads: uploads}
}

// BatchProgress 统计某 MoviePilot 任务下所有文件上传任务的进展。
// 返回 (是否全部终态, 失败数, 已上传数, 总数)。
func (a *moviePilotBatchAdapter) BatchProgress(taskID int64) (bool, int, int, int) {
	if a == nil || a.uploads == nil || taskID <= 0 {
		return false, 0, 0, 0
	}
	prefix := fmt.Sprintf("moviepilot-%d:", taskID)
	tasks := a.uploads.List(context.Background(), 0)
	total, uploaded, failed, unfinished := 0, 0, 0, 0
	for i := range tasks {
		t := &tasks[i]
		if !strings.HasPrefix(t.ClientTaskID, prefix) {
			continue
		}
		total++
		switch t.Status {
		case upload.StatusSuccess:
			uploaded++
		case upload.StatusFailed, upload.StatusCanceled, upload.StatusSkipped:
			failed++
		default:
			unfinished++
		}
	}
	if total == 0 {
		return false, 0, 0, 0
	}
	return unfinished == 0, failed, uploaded, total
}
