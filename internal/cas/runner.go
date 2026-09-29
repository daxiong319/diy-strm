package cas

import (
	"context"
	"strconv"
	"time"

	"litepan/internal/upload"
)

// Runner 持有上传管理器，扫描已完成影视上传任务并触发 CAS 化。
type Runner struct {
	Uploads *upload.Manager
}

// NewRunner 构造 CAS 运行器
func NewRunner(u *upload.Manager) *Runner { return &Runner{Uploads: u} }

// RunOnce 扫描上传任务（success + 影视），转换为候选并执行一轮 CAS 化。
func (r *Runner) RunOnce(ctx context.Context) (generated, deleted, skipped, failed int, err error) {
	if r == nil || r.Uploads == nil {
		return 0, 0, 0, 0, nil
	}
	candidates := r.scanCandidates(ctx)
	return RunOnce(ctx, candidates)
}

// scanCandidates 从上传任务管理器收集 CAS 化候选（成功 + 含结果文件 ID）。
func (r *Runner) scanCandidates(ctx context.Context) []CasCandidate {
	var out []CasCandidate
	// List(ctx, accountID) 按账号；传 0 取全部账号
	for _, task := range r.Uploads.List(ctx, 0) {
		if task.Status != upload.StatusSuccess {
			continue
		}
		parentID, fileID := resultFileID(task.Result)
		if fileID == "" {
			continue
		}
		out = append(out, CasCandidate{
			UploadTaskID:   hashTaskID(task.TaskID),
			AccountID:      task.AccountID,
			SourceType:     task.DriverType,
			RemoteParentID: parentID,
			FileName:       task.FileName,
			RemoteFileID:   fileID,
			FileSize:       resultSize(task.Result),
			CompletedAt:    timeFromFloat(task.UpdatedAt),
		})
	}
	return out
}

// resultFileID 从任务 Result 提取 (parentID, fileID)
func resultFileID(result map[string]any) (parentID, fileID string) {
	if result == nil {
		return "", ""
	}
	parentID = anyStr(result["parent_id"])
	fileID = anyStr(result["file_id"])
	if fileID == "" {
		fileID = anyStr(result["fid"])
	}
	return parentID, fileID
}

func resultSize(result map[string]any) int64 {
	if result == nil {
		return 0
	}
	switch v := result["size"].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func timeFromFloat(f float64) time.Time {
	if f <= 0 {
		return time.Time{}
	}
	sec := int64(f)
	nsec := int64((f - float64(sec)) * 1e9)
	return time.Unix(sec, nsec)
}

// hashTaskID 把字符串任务 ID 稳定映射为 int64（CAS 幂等键）。
// 上传任务 TaskID 是字符串（如 UUID/批次+序号），用 FNV 哈希成 int64 存 upload_task_id。
func hashTaskID(s string) int64 {
	var h uint64 = 1469598103934665603 // FNV-1a 64 offset basis
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return int64(h & 0x7fffffffffffffff)
}

func anyStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case nil:
		return ""
	default:
		return ""
	}
}
