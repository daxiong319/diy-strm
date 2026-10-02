// Package embyrefresh 提供 Emby/Jellyfin 刷新任务的持久化队列与防抖调度。
//
// 设计目标：事件驱动（入库、下载完成、STRM 同步完成等）产生的刷新意图先写入
// emby_refresh_tasks，由后台扫描器按防抖窗口合并后统一提交，避免短时间内对
// Emby/Jellyfin 发起大量重复刷新。单条目的刷新失败时会降级为所在媒体库刷新。
package embyrefresh

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// 刷新目标类型。
const (
	TargetTypeLibrary = "library"
	TargetTypeItem    = "item"
)

// 刷新任务状态。
const (
	StatusPending    = "pending"
	StatusRefreshing = "refreshing"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
)

// 就绪判断原因。
const (
	ReasonEmptyTask       = "empty_task"
	ReasonNotPending      = "not_pending"
	ReasonDeadlineExpired = "deadline_expired"
	ReasonDebounce        = "debounce"
	ReasonEmptyTarget     = "empty_target"
	ReasonSyncRunning     = "sync_running"
	ReasonDownloadRunning = "download_running"
	ReasonReady           = "ready"
)

const (
	// DefaultDebounce 是新事件到达后到允许执行之间的防抖窗口。
	DefaultDebounce = 10 * time.Second
	// DefaultMaxWait 是单个任务自创建起的最长等待时间，超时后不再刷新。
	DefaultMaxWait = 6 * time.Hour
	// DefaultScanInterval 是后台扫描器的轮询周期。
	DefaultScanInterval = 60 * time.Second
	// ItemAggregationThreshold 是同一媒体库的待刷新条目合并为库级刷新的阈值。
	// 设为 <= 0 可关闭合并。
	ItemAggregationThreshold = 10
)

// Task 是一条持久化的刷新任务。
type Task struct {
	ID            int64
	TaskKey       string
	LibraryID     string
	LibraryName   string
	TargetType    string
	ItemIDs       []string
	Status        string
	LastEventAt   int64
	RefreshAfter  int64
	DeadlineAt    int64
	LastCheckedAt int64
	LastRefreshAt int64
	Error         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// LibraryTaskKey 生成库级任务的去重键。
func LibraryTaskKey(libraryID string) string {
	return TargetTypeLibrary + ":" + strings.TrimSpace(libraryID)
}

// ItemTaskKey 生成条目级任务的去重键。
func ItemTaskKey(itemID string) string {
	return TargetTypeItem + ":" + strings.TrimSpace(itemID)
}

// encodeItemIDs 把条目 ID 列表序列化为 JSON 文本，始终非空数组。
func encodeItemIDs(ids []string) string {
	ids = NormalizeItemIDs(ids)
	if ids == nil {
		ids = []string{}
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// decodeItemIDs 解析条目 ID 列表，解析失败时返回空列表。
func decodeItemIDs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return NormalizeItemIDs(ids)
}

// NormalizeItemIDs 去空白、去重并排序，保证同一集合产生稳定的序列化结果。
func NormalizeItemIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// UnionItemIDs 合并两组条目 ID，返回去重排序后的并集。
func UnionItemIDs(a, b []string) []string {
	merged := make([]string, 0, len(a)+len(b))
	merged = append(merged, a...)
	merged = append(merged, b...)
	return NormalizeItemIDs(merged)
}

// Readiness 是就绪判断的结果：是否可执行，以及原因字符串。
type Readiness struct {
	Ready  bool
	Reason string
}

// CheckReady 移植 IsEmbyLibraryRefreshTaskReady 的判断顺序：
// not_pending → deadline_expired → debounce → empty_target → ready。
//
// 关于 sync_running / download_running 两个门禁：老版本依据 sync_path_ids 判断
// STRM 同步与离线下载是否在进行，现版本既没有按同步路径索引的活动任务查询，
// strm.Service.IsTaskRunning 也只能按 STRM 任务 ID 查询（语义不同），
// 因此这两个门禁暂未接入，见 scan.go 中的 TODO。
func CheckReady(task *Task, now time.Time) Readiness {
	if task == nil {
		return Readiness{Ready: false, Reason: ReasonEmptyTask}
	}
	if task.Status != StatusPending {
		return Readiness{Ready: false, Reason: ReasonNotPending}
	}
	nowUnix := now.Unix()
	if task.DeadlineAt > 0 && task.DeadlineAt <= nowUnix {
		return Readiness{Ready: false, Reason: ReasonDeadlineExpired}
	}
	// 防抖只延长到截止时间为止：超过截止时间的等待不再顺延，否则任务永远无法执行。
	if task.RefreshAfter > nowUnix && task.DeadlineAt > nowUnix {
		return Readiness{Ready: false, Reason: ReasonDebounce}
	}
	if !hasTarget(task) {
		return Readiness{Ready: false, Reason: ReasonEmptyTarget}
	}
	return Readiness{Ready: true, Reason: ReasonReady}
}

// hasTarget 判断任务是否携带可刷新的目标。
func hasTarget(task *Task) bool {
	if task == nil {
		return false
	}
	if task.TargetType == TargetTypeItem {
		return len(task.ItemIDs) > 0
	}
	return strings.TrimSpace(task.LibraryID) != ""
}
