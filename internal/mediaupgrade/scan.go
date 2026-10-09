package mediaupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"litepan/internal/moviepilot"
)

// Scanner 负责「只判定不执行」的扫描阶段。
type Scanner struct {
	DB       *gorm.DB
	Settings Settings
	// now 可注入，便于测试固定快照时间。
	now func() time.Time
}

// NewScanner 构造扫描器。
func NewScanner(db *gorm.DB, svc Settings) *Scanner {
	return &Scanner{DB: db, Settings: svc, now: time.Now}
}

func (s *Scanner) clock() time.Time {
	if s == nil || s.now == nil {
		return time.Now()
	}
	return s.now()
}

// ScanOptions 一次扫描的入参。
type ScanOptions struct {
	// RuleID 指定库里的规则行；0 表示用全局设置装配默认规则集。
	RuleID uint
	// Category 限定这次扫描只管哪些分类目录（T27 C-8）。
	// 零值 = 不按分类筛选，与洗版其它可选条件的"没配就不限制"一致。
	Category CategoryScope
}

// Scan 执行一次扫描：枚举文件 → 按作品分组 → 槽位分组 → 比较 → 写记录。
//
// **它一个文件都不会删或移。** 产出的记录全是 status=pending，
// 要真正动文件必须另起 ExecuteScan。两者分开是这套设计的全部意义。
func (s *Scanner) Scan(ctx context.Context, opts ScanOptions) (*Scan, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("洗版扫描缺少数据库句柄")
	}
	rs, err := LoadRuleSet(s.DB, s.Settings, opts.RuleID)
	if err != nil {
		return nil, err
	}
	now := s.clock()

	scan := &Scan{
		Source:         rs.Source,
		LibraryRoot:    rs.LibraryRoot,
		CandidateRoots: rs.CandidateRootsText(),
		RuleID:         rs.ID,
		Status:         ScanStatusRunning,
		Message:        "扫描中",
		StartedAt:      &now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.DB.Create(scan).Error; err != nil {
		return nil, fmt.Errorf("创建洗版扫描记录失败: %w", err)
	}

	library, err := listLibraryFiles(ctx, s.DB, rs)
	if err != nil {
		return s.fail(scan, fmt.Sprintf("枚举媒体库失败：%v", err))
	}
	// 分类范围筛选（T27 C-8）。筛在候选枚举**之前**且只筛媒体库文件：
	// 候选文件可能还没被整理进任何分类目录，按分类筛它们会让
	// 「库里某部片子找不到候选里更好的版本」重新出现 ——
	// 那是洗版最核心的用途。
	//
	// 范围来自**这条规则自己**的 category_scope 列，而不是"分类引擎配了
	// 哪些分类目录"：后者会让每条规则都扫全库，用户在规则里填的
	// "只管国产剧"完全不起作用，而扫描结果看起来是正常的。
	scope := opts.Category
	if scope.Empty() && len(rs.CategoryNames) > 0 {
		scope = ScopeFromNames(rs.CategoryNames)
	}
	library, skippedByScope := categoryFilter(library, rs.LibraryRoot, scope)
	candidates, err := listCandidateFiles(ctx, s.DB, rs, library)
	if err != nil {
		return s.fail(scan, fmt.Sprintf("枚举候选目录失败：%v", err))
	}

	scan.LibraryFiles = len(library)
	scan.CandidateFiles = len(candidates)
	scan.Status = ScanStatusRunning
	scan.Message = fmt.Sprintf("已枚举 %d 个库内文件、%d 个候选文件", len(library), len(candidates))
	if err := s.DB.Model(scan).Updates(map[string]any{
		"library_files":   len(library),
		"candidate_files": len(candidates),
		"message":         scan.Message,
		"updated_at":      now,
	}).Error; err != nil {
		log.Printf("[mediaupgrade] 更新扫描进度失败 scan_id=%d: %v", scan.ID, err)
	}

	records, stats := s.evaluate(rs, library, candidates, scan.ID, now)

	if len(records) > 0 {
		if err := s.DB.CreateInBatches(records, 200).Error; err != nil {
			return s.fail(scan, fmt.Sprintf("写入判定记录失败：%v", err))
		}
	}

	finished := s.clock()
	scan.Status = ScanStatusSuccess
	if stats.failed > 0 {
		scan.Status = ScanStatusPartial
	}
	scan.TotalRecords = len(records)
	scan.NewWinsCount = stats.newWins
	scan.SkippedCount = stats.skipped
	scan.FailedCount = stats.failed
	scan.Message = fmt.Sprintf("判定完成：%d 条记录，其中新版胜出 %d 条、可执行 %d 条",
		len(records), stats.newWins, stats.executable)
	if n := len(skippedByScope); n > 0 {
		// 追加在**最终**消息上：这段 Message 在函数结尾会被整体覆写，
		// 写在枚举阶段等于没写（第一版就踩了这个，用户会以为媒体库变小了）。
		scan.Message += fmt.Sprintf("（分类范围外跳过 %d 个库内文件）", n)
	}
	scan.FinishedAt = &finished
	scan.UpdatedAt = finished
	if err := s.DB.Model(scan).Updates(map[string]any{
		"status":         scan.Status,
		"total_records":  scan.TotalRecords,
		"new_wins_count": scan.NewWinsCount,
		"skipped_count":  scan.SkippedCount,
		"failed_count":   scan.FailedCount,
		"message":        scan.Message,
		"finished_at":    finished,
		"updated_at":     finished,
	}).Error; err != nil {
		return nil, fmt.Errorf("更新扫描结果失败: %w", err)
	}
	log.Printf("[mediaupgrade] 扫描完成 scan_id=%d source=%s %s", scan.ID, scan.Source, scan.Message)
	return scan, nil
}

// scanStats 一次扫描的统计。
type scanStats struct {
	newWins    int
	skipped    int
	failed     int
	executable int
}

// fail 把扫描标成失败并返回错误。
func (s *Scanner) fail(scan *Scan, msg string) (*Scan, error) {
	finished := s.clock()
	scan.Status = ScanStatusFailed
	scan.Message = msg
	scan.FinishedAt = &finished
	scan.UpdatedAt = finished
	if err := s.DB.Model(scan).Updates(map[string]any{
		"status":      ScanStatusFailed,
		"message":     msg,
		"finished_at": finished,
		"updated_at":  finished,
	}).Error; err != nil {
		log.Printf("[mediaupgrade] 标记扫描失败时更新失败 scan_id=%d: %v", scan.ID, err)
	}
	log.Printf("[mediaupgrade] 扫描失败 scan_id=%d: %s", scan.ID, msg)
	return scan, fmt.Errorf("%s", msg)
}

// evaluate 核心判定：把候选与库内文件按作品分组、槽位分组后逐条比较。
func (s *Scanner) evaluate(rs *RuleSet, library, candidates []libFile, scanID uint, now time.Time) ([]Record, scanStats) {
	var stats scanStats

	// 库内文件按作品分组 —— 快照要记的是「这一集当时库里有哪几个文件」，
	// 包含所有槽位，而不只是被比较的那个槽位。
	libByWork := map[string][]libFile{}
	for _, f := range library {
		k := workKeyOf(f.Name).Key()
		libByWork[k] = append(libByWork[k], f)
	}

	// 每部剧已产出的记录数（max_records_per_series 的计数口径）。
	seriesCount := map[string]int{}

	// 同一作品可能有多个候选（几个版本都躺在候选目录里，或用户直接把新版丢进了已整理目录）。
	// **只让最好的那个参与比较**：早先按候选路径排序逐个 seenWork 去重，
	// 结果是「路径排序靠前的那个」先把作品占掉，真正更好的版本反而永远轮不上。
	// 更糟的是候选列表里混着媒体库自己的文件，谁排在前面完全取决于目录名的字典序。
	candByWork := map[string][]libFile{}
	workIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		k := workKeyOf(c.Name).Key()
		if _, seen := candByWork[k]; !seen {
			workIDs = append(workIDs, k)
		}
		candByWork[k] = append(candByWork[k], c)
	}
	sort.Strings(workIDs)

	records := make([]Record, 0, 16)

	for _, workID := range workIDs {
		wk := workKeyOf(candByWork[workID][0].Name)
		cand := bestCandidate(candByWork[workID], rs.GroupPriority, rs.WashRules)
		// 门槛过滤：低于分辨率/声道门槛的候选直接不参与，也不产生记录。
		//
		// ⚠️ 刻意**不**为门槛不过的候选补一条记录（T31 讨论过并否决）：
		// 扫描记录的口径是「库里有现版、这次判定给出了结论」，
		// 为了让 below_min_* 三个 code 能出现在列表里就往里塞记录，
		// 会让「洗版扫出了多少条」这个数字凭空变大、也会让 skipped 统计
		// 掺进一批根本没进入比较的文件。
		// 门槛类理由改由**规则试算**端点给出（见 TrialVerdict），
		// 用户问「我这份规则会不会把这个文件洗掉」在那里能问到答案。
		if !meetsCandidateGate(rs, cand) {
			continue
		}

		oldInWork := libByWork[workID]
		// 该作品没有任何库内文件时无从「洗版」，跳过（不算失败）。
		if len(oldInWork) == 0 {
			continue
		}

		// 槽位分组：只有同槽位的现版才进比较；不同槽位两个都保留。
		sameSlot := make([]libFile, 0, len(oldInWork))
		for _, o := range oldInWork {
			if o.Slot.Key() == cand.Slot.Key() {
				sameSlot = append(sameSlot, o)
			}
		}
		if len(sameSlot) == 0 {
			stats.skipped++
			records = append(records, s.buildRecord(rs, scanID, cand, wk, oldInWork, nil,
				RelationNoDimension, "不同版本槽位（"+cand.Slot.Label()+"），两个都保留",
				"", RecordStatusSkippedNoSlot, now, seriesCount, noSlotReasons()...))
			continue
		}

		// 同槽位里排除候选自己（用户把新版丢进了已整理目录的情况）。
		peers := make([]libFile, 0, len(sameSlot))
		for _, o := range sameSlot {
			if o.Path != cand.Path {
				peers = append(peers, o)
			}
		}
		if len(peers) == 0 {
			continue
		}

		v := compareOne(cand, peers, rs.WashRules, rs.GroupPriority)
		rec, stat := s.buildDecisionRecord(rs, scanID, cand, wk, oldInWork, peers, v, now, seriesCount)
		stats.newWins += stat.newWins
		stats.skipped += stat.skipped
		stats.failed += stat.failed
		stats.executable += stat.executable
		records = append(records, rec)
	}
	return records, stats
}

// bestCandidate 从同作品的多个候选里挑最好的一个。
//
// 口径复用 CompareQuality（逐项先决胜负），完全持平时取更大的那个；
// 两侧都解析不出质量时取路径靠前的，保证结果稳定可复现
// （枚举顺序一变就挑中不同的文件，会让快照哈希跟着抖）。
func bestCandidate(files []libFile, groupPriority []string, rules []moviepilot.WashRule) libFile {
	best := files[0]
	for _, f := range files[1:] {
		cmp := moviepilot.CompareQuality(f.Quality, best.Quality, groupPriority, rules)
		switch {
		case cmp > 0:
			best = f
		case cmp == 0 && f.Size > best.Size:
			best = f
		}
	}
	return best
}

// meetsCandidateGate 判断候选是否达到规则门槛。
func meetsCandidateGate(rs *RuleSet, f libFile) bool {
	if rs.MinResolution > 0 {
		res := 0
		if f.Quality != nil {
			res = f.Quality.Resolution
		}
		if res < rs.MinResolution {
			return false
		}
	}
	if rs.MinChannels > 0 {
		ch := 0
		if f.Quality != nil {
			ch = f.Quality.Channels
		}
		if ch < rs.MinChannels {
			return false
		}
	}
	if rs.RequireSubtitle && f.Slot.Subtitle != "sub" {
		return false
	}
	return true
}

// buildDecisionRecord 按判定结论产出记录，并处理 max_records_per_series。
func (s *Scanner) buildDecisionRecord(
	rs *RuleSet, scanID uint, cand libFile, wk workKey,
	allOld, peers []libFile, v verdict, now time.Time, seriesCount map[string]int,
) (Record, scanStats) {
	var stats scanStats

	// max_records_per_series：**可执行**记录数才是计数口径。
	// 已判定为「新版没赢」/「不同槽位」的记录不占额度 —— 它们本来就不删文件。
	seriesID := wk.SeriesKey
	status := RecordStatusPending
	message := v.Trace
	switch v.Relation {
	case RelationNewWins:
		message = "新版胜出：" + v.Trace
	case RelationNewLoses:
		status = RecordStatusSkippedNewLoses
		message = "新版没赢现版：" + v.Trace
	case RelationTie:
		status = RecordStatusSkippedNewLoses
		message = "质量持平且体积不占优：" + v.Trace
	case RelationNoDimension:
		status = RecordStatusSkippedNoDimension
		message = "无可比维度：" + v.Trace
	}

	// 只有「新版胜出且败方动作会动文件」的记录才占额度。
	wouldAct := v.Relation == RelationNewWins && rs.LoserAction != LoserActionKeep
	if wouldAct && rs.MaxRecordsPerSeries > 0 && seriesCount[seriesID] >= rs.MaxRecordsPerSeries {
		status = RecordStatusSkippedLimit
		message = fmt.Sprintf("超过单剧记录上限（%d 条），记录已保存但不执行", rs.MaxRecordsPerSeries)
	} else if wouldAct {
		seriesCount[seriesID]++
	}
	if wouldAct && status == RecordStatusPending {
		stats.newWins = 1
		stats.executable = 1
	} else if v.Relation == RelationNewWins {
		stats.newWins = 1
	}

	rec := s.buildRecord(rs, scanID, cand, wk, allOld, peers, v.Relation, v.Trace, v.Loser, status, now, seriesCount, v.Reasons...)
	rec.Message = message
	return rec, stats
}

// buildRecord 组装一条记录（含快照与哈希）。
func (s *Scanner) buildRecord(
	rs *RuleSet, scanID uint, cand libFile, wk workKey,
	allOld, peers []libFile, relation, trace, loserPath, status string,
	now time.Time, _ map[string]int, reasons ...RejectReason,
) Record {
	snapshot := marshalSnapshots(allOld)
	return Record{
		ScanID:      scanID,
		RuleID:      rs.ID,
		SeriesKey:   wk.SeriesKey,
		SeriesTitle: displayTitle(cand.Name),
		EpisodeKey:  wk.EpisodeKey,
		SlotKey:     cand.Slot.Key(),
		NewFilePath: cand.Path,
		NewFileName: cand.Name,
		NewSize:     cand.Size,
		NewQuality:  marshalJSON(cand.Quality),
		OldFiles:    marshalSnapshots(orEmptyLibFiles(peers)),
		// loser_action 快照进记录：执行时按判定当时的口径执行。
		// 用户中途把 delete 改成 keep，不该让一条早已判定好的记录突然开始删文件。
		LoserAction:     rs.LoserAction,
		QualityRelation: relation,
		Trace:           trace,
		// nil → 空串而不是 "null"：历史行与「新版胜出」的行都该是空串，
		// 前端按空串判「无结构化理由」，不用去特判字符串 "null"。
		RejectReasons:   marshalReasons(reasons),
		RuleFingerprint: rs.Fingerprint(),
		LoserPath:       loserPath,
		Snapshot:        snapshot,
		SnapshotHash:    hashString(snapshot),
		SnapshotAt:      now,
		Status:          status,
		Message:         trace,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// orEmptyLibFiles nil 转空切片（避免 JSON 里出现 "null"）。
func orEmptyLibFiles(in []libFile) []libFile {
	if in == nil {
		return []libFile{}
	}
	return in
}

// hashString 算 sha256 十六进制串。
func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ListScans 分页列出扫描任务。
func (s *Scanner) ListScans(ctx context.Context, limit int) ([]Scan, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("洗版扫描缺少数据库句柄")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []Scan
	err := s.DB.WithContext(ctx).Order("id desc").Limit(limit).Find(&out).Error
	return out, err
}

// GetScan 读单个扫描任务。
func (s *Scanner) GetScan(ctx context.Context, id uint) (*Scan, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("洗版扫描缺少数据库句柄")
	}
	var out Scan
	if err := s.DB.WithContext(ctx).Where("id = ?", id).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// ListRecords 列出某次扫描的判定记录。
func (s *Scanner) ListRecords(ctx context.Context, scanID uint, status string, limit int) ([]Record, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("洗版扫描缺少数据库句柄")
	}
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	q := s.DB.WithContext(ctx).Where("scan_id = ?", scanID)
	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("status = ?", st)
	}
	var out []Record
	err := q.Order("id asc").Limit(limit).Find(&out).Error
	return out, err
}

// SeriesCounts 统计某次扫描里每个作品的记录数，供 UI 看分布。
func (s *Scanner) SeriesCounts(ctx context.Context, scanID uint) (map[string]int, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("洗版扫描缺少数据库句柄")
	}
	type row struct {
		SeriesKey string
		N         int
	}
	var rows []row
	err := s.DB.WithContext(ctx).Model(&Record{}).
		Select("series_key, count(*) as n").
		Where("scan_id = ?", scanID).
		Group("series_key").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.SeriesKey] = r.N
	}
	return out, nil
}
