package inspection

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// 检查器键。集中定义而不是散在各检查器文件里，是因为 Service 报告里的
// Checkers 顺序、预览里的 CheckerKey、前端页面的分组都按这个键对齐 ——
// 键改名会让已落库的快照里那一段 CheckerKey 认不出来。
const (
	KeyIndexAbnormal = "index_abnormal"
	KeyOrphanDir     = "orphan_dir"
	KeyEmptyDir      = "empty_dir"
	KeyDuplicate     = "duplicate"
	KeyTMDB          = "tmdb_check"
	KeyMissing       = "missing_episodes"
)

// Checker 是巡检套件的统一接口。
//
// 三个方法各管一件事，顺序不能混：
//
//	Key/Label  标识，不碰外部系统。
//	Scan       只读。它可以看到一切，但不允许改任何东西 —— 这一条是整个
//	           巡检可被信任的前提：用户按下「扫描」时，系统状态不会因为
//	           这一次按压而移动一字节。
//	Repair     不在这里。它由 Finding.Repair 描述，等用户在预览里点确认之后
//	           才由 Service 执行。
//
// 因此 Scan 不接收任何可以改变世界的句柄：想要动数据的检查器不会「顺手」动手，
// 它只能把想做的事写进 Finding.Repair，等人点头。
type Checker interface {
	Key() string
	Label() string
	Scan(ctx context.Context) ([]Finding, error)
}

// RepairAction 是一次修复动作的描述。
//
// 它同时被预览和执行读，所以字段必须足够让预览说清「会发生什么」，
// 而不是只说「会修一下」。Preview 字符串是给人看的，Params 是给执行器看的。
type RepairAction struct {
	// Kind 是执行器认识的修复种类（比如 delete_dir / rename / drop_index_row）。
	// 空 Kind 表示这条 Finding 只报告不修 —— 查漏补缺就是这种：
	// 系统不能替用户决定该下载第 5 集还是删掉整季。
	Kind string `json:"kind"`
	// Label 是一句话说明，列表里直接展示。
	Label string `json:"label"`
	// Preview 是执行前必读的完整描述，逐条拼进预览面板。
	Preview string `json:"preview"`
	// Params 是给执行器的参数，JSON 序列化后存进快照。
	Params map[string]string `json:"params,omitempty"`
	// Reversible 说明这次修复能不能退回去。破坏性动作必须显式写 false，
	// 不能靠默认零值蒙混：预览面板靠这个字段决定要不要二次确认。
	Reversible bool `json:"reversible"`
}

// Finding 是一个检查结果。
//
// Kind 与 Target 合起来是天然主键：同一目标同一类型只留一条，重复的
// 说明上游数据自己就打架了，合并成一条比展示两遍有用。
type Finding struct {
	CheckerKey string         `json:"checker_key"`
	Kind       string         `json:"kind"`
	Target     string         `json:"target"`
	Detail     map[string]any `json:"detail,omitempty"`
	Repair     RepairAction   `json:"repair"`
}

// ID 是 Finding 的稳定标识，回传预览与执行请求时用它。
func (f Finding) ID() string {
	return f.CheckerKey + "|" + f.Kind + "|" + f.Target
}

// ExecuteResult 是一次修复执行的结果。
type ExecuteResult struct {
	FindingID string `json:"finding_id"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
}

// Repairer 执行某一类修复动作。
//
// 单独抽出来是为了让 Service 保持「只调度」：谁真的去删目录、谁去改索引，
// 由实现方决定，Service 既不 import driver 也不 import store。
type Repairer interface {
	Repair(ctx context.Context, action RepairAction) (string, error)
}

// Registry 收集所有检查器与修复器。
type Registry struct {
	mu        sync.RWMutex
	checkers  []Checker
	byKey     map[string]Checker
	repairers map[string]Repairer
}

func NewRegistry() *Registry {
	return &Registry{byKey: map[string]Checker{}, repairers: map[string]Repairer{}}
}

func (r *Registry) Register(c Checker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byKey[c.Key()]; dup {
		return
	}
	r.checkers = append(r.checkers, c)
	r.byKey[c.Key()] = c
}

func (r *Registry) RegisterRepairer(kind string, r2 Repairer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repairers[kind] = r2
}

func (r *Registry) Checkers() []Checker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Checker, len(r.checkers))
	copy(out, r.checkers)
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func (r *Registry) Checker(key string) Checker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byKey[key]
}

func (r *Registry) repairer(kind string) Repairer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.repairers[kind]
}

// Keys 返回全部检查器键，顺序与 Checkers 一致。
func (r *Registry) Keys() []string {
	cs := r.Checkers()
	keys := make([]string, len(cs))
	for i, c := range cs {
		keys[i] = c.Key()
	}
	return keys
}

// PreviewLine 是预览面板的一行。
type PreviewLine struct {
	FindingID  string `json:"finding_id"`
	CheckerKey string `json:"checker_key"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Detail     string `json:"detail,omitempty"`
	Repair     string `json:"repair"`
	Reversible bool   `json:"reversible"`
}

// Preview 把一批 Finding 渲染成给人看的预览清单。
//
// 预览不是装饰：Service 的执行入口只接受 Preview 返回过的 ID，
// 也就是说「没预览过的动作」在系统里根本不存在，只有 Find 本身。
// 这是「所有修复动作都有预览」这条验收的实现方式 —— 用类型和调用路径保证，
// 而不是靠调用方记得先调预览。
func Preview(findings []Finding) []PreviewLine {
	lines := make([]PreviewLine, 0, len(findings))
	for _, f := range findings {
		lines = append(lines, PreviewLine{
			FindingID:  f.ID(),
			CheckerKey: f.CheckerKey,
			Kind:       f.Kind,
			Target:     f.Target,
			Detail:     detailText(f.Detail),
			Repair:     f.Repair.Preview,
			Reversible: f.Repair.Reversible,
		})
	}
	return lines
}

func detailText(detail map[string]any) string {
	if len(detail) == 0 {
		return ""
	}
	keys := make([]string, 0, len(detail))
	for k := range detail {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, detail[k]))
	}
	return strings.Join(parts, " ")
}