package classification

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrUnavailable = errors.New("classification enhancer unavailable")

// DetailLoader 由 Planner 注入，让分类增强可在需要时补齐 TMDB detail 级元数据。
type DetailLoader interface {
	Lookup(ctx context.Context, tmdbID string, mediaType string) (json.RawMessage, error)
}

type Request struct {
	MediaType string
	TMDBID    string
	Raw       map[string]any
	Loader    DetailLoader

	// Title 与 Year 供「只算不写」的预览场景用（C-7）。
	//
	// 分类本身不需要这两个字段：生产链路上标题与年份都已经躺在 Raw 里
	// （year 由 release_date 解析、keywords 由本地化标题来）。但预览端点
	// 接收的是用户在界面上手填的几个字段，没有 Raw 可依赖，得有个正规入口
	// 把它们放进来 —— 在端点里偷偷往 Raw 里塞键，等于让分类逻辑长出一个
	// 只有预览路径才存在的隐式契约。
	Title string
	Year  int
}

// Decision 只描述相对 move 目标根的分类建议，不携带网盘目录 ID，也不执行写操作。
type Decision struct {
	Applied          bool           `json:"applied"`
	Matched          bool           `json:"matched"`
	Template         string         `json:"template,omitempty"`
	Category         string         `json:"category,omitempty"`
	RelativeSegments []string       `json:"relative_segments,omitempty"`
	Evidence         map[string]any `json:"evidence,omitempty"`
	DegradedReason   string         `json:"degraded_reason,omitempty"`
}

type Enhancer interface {
	Available() bool
	Classify(ctx context.Context, req Request) (Decision, error)
}
