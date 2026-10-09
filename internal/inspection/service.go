package inspection

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"litepan/internal/domain"
)

// SnapshotTTL 是巡检快照的有效期。
//
// 取 1 小时：用户点完扫描一般就在这一小时内决定修不修。超过一小时，
// 结论可能已经因为用户自己的整理而失效，重新扫一次比拿旧结论动手更安全。
const SnapshotTTL = time.Hour

// ScanReport 是一次巡检的完整结果。
type ScanReport struct {
	SnapshotID string        `json:"snapshot_id"`
	ScannedAt  time.Time     `json:"scanned_at"`
	Checkers   []CheckerInfo `json:"checkers"`
	Preview    []PreviewLine `json:"preview"`
	Total      int           `json:"total"`
}

// CheckerInfo 是单个检查器的执行情况。
//
// 一个检查器出错不该让整轮巡检作废：用户需要知道「另外五项查出来了什么」，
// 而不是对着一个红色感叹号。所以这里逐项记状态，Scan 整体不返回 error。
type CheckerInfo struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Findings int      `json:"findings"`
	Error    string   `json:"error,omitempty"`
	Skipped  bool     `json:"skipped,omitempty"`
	Kinds    []string `json:"kinds,omitempty"`
}

// Service 是巡检套件的调度层。
//
// 它只做三件事：跑检查器、渲染预览、执行被确认过的动作。
// 它不认识目录、索引、TMDB —— 那些都在 Checker 与 Repairer 里。
// 这样加第七个检查器不需要动 Service，而 Service 本身小到可以一眼看完，
// 也就不会有「顺手在调度层补个删除」这种事。
type Service struct {
	repo   domain.InspectionRepository
	reg    *Registry
	now    func() time.Time
	ttl    time.Duration
}

// ServiceOptions 是 Service 的依赖。
type ServiceOptions struct {
	Repo      domain.InspectionRepository
	Registry  *Registry
	Now       func() time.Time
	TTL       time.Duration
}

func NewService(opts ServiceOptions) *Service {
	s := &Service{
		repo: opts.Repo,
		reg:  opts.Registry,
		now:  opts.Now,
		ttl:  opts.TTL,
	}
	if s.reg == nil {
		s.reg = NewRegistry()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.ttl <= 0 {
		s.ttl = SnapshotTTL
	}
	return s
}

// Checkers 返回检查器清单，供前端渲染列表。
func (s *Service) Checkers() []CheckerInfo {
	out := make([]CheckerInfo, 0)
	for _, c := range s.reg.Checkers() {
		out = append(out, CheckerInfo{Key: c.Key(), Label: c.Label()})
	}
	return out
}

// ScanAll 跑一遍全部检查器，并把结果连同预览一起存成快照。
//
// 返回的 snapshot_id 是执行修复的唯一凭据：没有它，Repair 就什么也做不了。
// 这就是「所有修复动作都有预览」的实现 —— 不是在界面上弹个确认框，
// 而是让未经渲染的动作在类型层面就无法被取用。
func (s *Service) ScanAll(ctx context.Context) (*ScanReport, error) {
	findings, infos := s.runCheckers(ctx)
	preview := Preview(findings)
	key := s.newSnapshotKey()
	payload, err := json.Marshal(findings)
	if err != nil {
		return nil, domain.Errorf(domain.CodeInternal, "序列化巡检结果失败: %v", err)
	}
	if err := s.repo.SaveInspectionSnapshot(ctx, key, joinIDs(findings), string(payload), s.now()); err != nil {
		return nil, err
	}
	s.pruneSnapshots(ctx)
	return &ScanReport{
		SnapshotID: key,
		ScannedAt:  s.now(),
		Checkers:   infos,
		Preview:    preview,
		Total:      len(findings),
	}, nil
}

func (s *Service) runCheckers(ctx context.Context) ([]Finding, []CheckerInfo) {
	var (
		findings []Finding
		infos    []CheckerInfo
	)
	for _, c := range s.reg.Checkers() {
		info := CheckerInfo{Key: c.Key(), Label: c.Label()}
		got, err := c.Scan(ctx)
		if err != nil {
			info.Error = err.Error()
			infos = append(infos, info)
			continue
		}
		kinds := map[string]bool{}
		for _, f := range got {
			// 检查器可能忘记填 CheckerKey（它是从自己身上知道 key 的，
			// 但漏填不该让这条 Finding 变成孤儿），这里补齐。
			if f.CheckerKey == "" {
				f.CheckerKey = c.Key()
			}
			kinds[f.Kind] = true
			findings = append(findings, f)
		}
		info.Findings = len(got)
		info.Kinds = sortedKeys(kinds)
		infos = append(infos, info)
	}
	// 稳定排序：同样的输入必须给出同样的顺序，否则预览里的序号会漂，
	// 用户按序号勾选就会勾错东西。
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].ID() < findings[j].ID() })
	return findings, infos
}

// GetPreview 取回一份快照的预览。快照过期或已消费时返回 nil。
func (s *Service) GetPreview(ctx context.Context, snapshotID string) ([]PreviewLine, bool) {
	if snapshotID == "" {
		return nil, false
	}
	_, ok := s.loadLive(ctx, snapshotID)
	if !ok {
		return nil, false
	}
	findings, err := s.loadFindings(ctx, snapshotID)
	if err != nil {
		return nil, false
	}
	return Preview(findings), true
}

// Repair 执行一批被确认过的修复动作。
//
// findingIDs 必须来自同一次扫描的快照。修复前会**再取一次文件体积/存在性**，
// 因为快照是扫描那一刻的结论，用户可能在这期间已经动了目录。
func (s *Service) Repair(ctx context.Context, snapshotID string, findingIDs []string) ([]ExecuteResult, error) {
	findings, err := s.loadFindings(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	if len(findings) == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "巡检快照为空或已过期，请重新扫描")
	}
	byID := make(map[string]Finding, len(findings))
	for _, f := range findings {
		byID[f.ID()] = f
	}
	var results []ExecuteResult
	executed := false
	for _, id := range findingIDs {
		f, ok := byID[id]
		if !ok {
			results = append(results, ExecuteResult{
				FindingID: id, Kind: "unknown", OK: false,
				Message: "这条不在本次扫描结果里（可能来自更早的一次扫描）",
			})
			continue
		}
		if f.Repair.Kind == RepairNone || f.Repair.Kind == "" {
			results = append(results, ExecuteResult{
				FindingID: id, Kind: f.Kind, Target: f.Target, OK: true,
				Message: "仅报告项，无需执行",
			})
			continue
		}
		rep := s.reg.repairer(f.Repair.Kind)
		if rep == nil {
			results = append(results, ExecuteResult{
				FindingID: id, Kind: f.Kind, Target: f.Target, OK: false,
				Message: "没有可用的修复器：" + f.Repair.Kind,
			})
			continue
		}
		msg, err := rep.Repair(ctx, f.Repair)
		if err != nil {
			results = append(results, ExecuteResult{
				FindingID: id, Kind: f.Kind, Target: f.Target, OK: false, Message: err.Error(),
			})
			continue
		}
		executed = true
		results = append(results, ExecuteResult{
			FindingID: id, Kind: f.Kind, Target: f.Target, OK: true, Message: msg,
		})
	}
	if executed {
		// 快照一次性作废：修完之后剩下的 Finding 可能已经不成立了
		// （比如父目录删了，子目录那条也就没了）。
		if err := s.repo.ConsumeInspectionSnapshot(ctx, snapshotID); err != nil {
			return results, err
		}
	}
	return results, nil
}

func (s *Service) loadFindings(ctx context.Context, snapshotID string) ([]Finding, error) {
	if _, ok := s.loadLive(ctx, snapshotID); !ok {
		return nil, domain.Errorf(domain.CodeValidation, "巡检快照已过期或已被执行，请重新扫描")
	}
	payload, _, _, err := s.repo.GetInspectionSnapshot(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	var findings []Finding
	if err := json.Unmarshal([]byte(payload), &findings); err != nil {
		return nil, domain.Errorf(domain.CodeInternal, "解析巡检快照失败: %v", err)
	}
	return findings, nil
}

// loadLive 判定快照是否仍然可用：存在、未消费、未过期。
func (s *Service) loadLive(ctx context.Context, snapshotID string) ([]string, bool) {
	payload, ids, dead, err := s.repo.GetInspectionSnapshot(ctx, snapshotID)
	if err != nil || dead || payload == "" {
		return nil, false
	}
	return ids, true
}

// pruneSnapshots 顺手清掉过期快照。
//
// 失败不往上抛：清理是卫生工作，为它让整次扫描失败没有意义。
func (s *Service) pruneSnapshots(ctx context.Context) {
	_, _ = s.repo.DeleteOldInspectionSnapshots(ctx, s.now().Add(-s.ttl*3))
}

func (s *Service) newSnapshotKey() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("snap-%d", s.now().UnixNano())
	}
	return "snap-" + hex.EncodeToString(buf[:])
}

func joinIDs(findings []Finding) string {
	ids := make([]string, len(findings))
	for i, f := range findings {
		ids[i] = f.ID()
	}
	// ID 里带的是路径，可能包含换行/逗号/冒号。用换行分隔并在存取两侧
	// 各自按同一约定切分；ID 内部不含换行（路径不会），所以这是可逆的。
	return strings.Join(ids, "\n")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}