package mediaupgrade

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gorm.io/gorm"
)

// CloudDeleteBatcher 把「批量删」与「逐条删」分开的删除器。
//
// 为什么要有这一层：网盘侧的删除通常是一个批量接口（一次最多删 N 个），
// 批量调用失败的原因五花八门 —— 超时、部分条目已被删、鉴权过期、限额。
// 收到失败就整批放弃的话，N-1 个其实能删的文件也白删了；
// 反过来「批量返回成功」也不可信，中途网络断了客户端可能压根没收到响应。
// 所以口径是：批量先试，失败或部分失败的**逐条重试**，只有逐条也失败的才算真失败。
type CloudDeleteBatcher struct {
	// BatchDelete 尝试整批删除，返回未能删除的路径列表与整体错误。
	// 返回 nil 切片表示全部成功。
	BatchDelete func(paths []string) ([]string, error)
	// SingleDelete 降级逐条删除。
	SingleDelete func(path string) error
	// Log 可选日志出口。
	Log func(format string, args ...any)
}

// DeleteBatch 执行删除并返回最终失败明细。
//
// 语义：
//   - 批量全成功 → 返回空切片。
//   - 批量失败（整体 err 非空）→ 对**全部**路径逐条重试。
//   - 批量部分失败（返回了剩余路径）→ 只对剩余路径逐条重试。
//   - 逐条仍失败的记 stage=single 进结果，让调用方落库并在界面可见。
func (b *CloudDeleteBatcher) DeleteBatch(paths []string) []DeleteFailure {
	if b == nil || len(paths) == 0 {
		return nil
	}
	// remaining 必须从 nil 起步。写成 paths 起步的话，
	// 「批量全成功」这条最正常的路径会拿 paths 去逐条重试，
	// 于是对已经删掉的文件再 os.Remove 一次，报 ENOENT，整条记录被判成失败。
	var (
		remaining []string
		batchErr  error
	)
	switch {
	case b.BatchDelete == nil:
		// 没有批量实现，直接走逐条。
		remaining = paths
	default:
		rest, err := b.BatchDelete(paths)
		batchErr = err
		switch {
		case err != nil:
			// 整体报错：按契约 BatchDelete 应同时给出没能删掉的列表。
			// 若它没给（网络断了压根没收到响应），宁可全部逐条重试 ——
			// 逐条删除是幂等的，对已删掉的文件会报 ENOENT，但不会误删别的。
			remaining = rest
			if len(remaining) == 0 {
				remaining = paths
			}
		case len(rest) > 0:
			remaining = rest
		default:
			// 批量全成功：到此为止，不再逐条。
			return nil
		}
	}
	// 降级逐条
	failures := make([]DeleteFailure, 0)
	for _, p := range remaining {
		if b.SingleDelete == nil {
			failures = append(failures, DeleteFailure{
				Path: p, Stage: DeleteStageSingle,
				Reason: firstNonEmpty(errText(batchErr), "没有可用的单文件删除实现"),
			})
			continue
		}
		if err := b.SingleDelete(p); err != nil {
			// 「文件已经不在了」就是这次删除想要的结果，不算失败。
			//
			// 降级重试天然会撞上这个情况：批量接口可能嘴上说没删成功，
			// 实际已经把文件删了（响应丢了、客户端重试、或批量接口按自己的口径报错）。
			// 这时逐条再删一次拿到 ENOENT —— 若把它当失败，
			// 界面的失败清单就会列出一个磁盘上根本不存在的文件，
			// 用户按着清单去核对只会更困惑，而目标状态其实早就达成了。
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			failures = append(failures, DeleteFailure{Path: p, Stage: DeleteStageSingle, Reason: err.Error()})
			if b.Log != nil {
				b.Log("[mediaupgrade] 逐条删除仍失败 path=%s err=%v", p, err)
			}
		}
	}
	return failures
}

// errText 取 error 文本。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// firstNonEmpty 取第一个非空串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Committer 负责「提交」阶段：复核快照、拿提交锁、按败方动作动文件。
type Committer struct {
	DB       *gorm.DB
	Settings Settings
	Deleter  *CloudDeleteBatcher
	now      func() time.Time

	// scanLocks 是同一进程内按 scan_id 的互斥锁，
	// 避免同一个进程里两次并发的 ExecuteScan 重复扫同一个目录。
	// 跨进程互斥靠下面的 CAS（DB 是唯一裁决者），这把内存锁只是省 IO。
	scanLocks sync.Map // map[uint]*sync.Mutex
}

// NewCommitter 构造提交器。
func NewCommitter(db *gorm.DB, svc Settings) *Committer {
	return &Committer{DB: db, Settings: svc, now: time.Now, Deleter: defaultDeleter()}
}

func (c *Committer) clock() time.Time {
	if c == nil || c.now == nil {
		return time.Now()
	}
	return c.now()
}

func (c *Committer) scanLock(scanID uint) *sync.Mutex {
	v, _ := c.scanLocks.LoadOrStore(scanID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// defaultDeleter 默认的本地删除实现（真实删文件）。
func defaultDeleter() *CloudDeleteBatcher {
	return &CloudDeleteBatcher{
		BatchDelete: func(paths []string) ([]string, error) {
			rest := make([]string, 0, len(paths))
			var firstErr error
			for _, p := range paths {
				if err := os.Remove(p); err != nil {
					if firstErr == nil {
						firstErr = err
					}
					rest = append(rest, p)
				}
			}
			if len(rest) == len(paths) && firstErr != nil {
				return rest, firstErr
			}
			return rest, nil
		},
		SingleDelete: func(p string) error { return os.Remove(p) },
	}
}

// ExecuteResult 一次提交的结果。
type ExecuteResult struct {
	ScanID uint `json:"scan_id"`
	// Total 是**本次真正进入提交队列**的记录数（扫描时判为 pending 的那些）。
	Total    int `json:"total"`
	Executed int `json:"executed"`
	Expired  int `json:"expired"`
	Skipped  int `json:"skipped"`
	Failed   int `json:"failed"`
	// Excluded 是同一次扫描里在扫描阶段就已判定不可执行的记录数
	// （不同槽位、新版更差、无可比维度、被每部剧上限拦下……）。
	// 不单独报出来的话，用户看到「共 2 条」却不知道另外 3 条去了哪，
	// 只会得出「扫漏了」的结论。
	Excluded int    `json:"excluded"`
	Message  string `json:"message"`
}

// ExecuteScan 提交某次扫描产生的待执行记录。
//
// 流程（每条记录）：
//  1. CAS 把 status 从 pending 原子改成 executing。抢不到就跳过（别的执行方已经在做，或已判终态）。
//  2. 重新枚举当前库内文件，重算 sha256 快照。
//     - 对不上 → status=expired、message=「判定已过期」，**不做任何删除**。
//  3. 复核败方文件仍可达；不可达 → status=skipped_no_access，**不做任何删除**。
//  4. 按 loser_action 处理败方（keep 不动 / delete 删 / move 移），记录删除失败明细。
//  5. status=executed。
func (c *Committer) ExecuteScan(ctx context.Context, scanID uint) (*ExecuteResult, error) {
	if c == nil || c.DB == nil {
		return nil, fmt.Errorf("洗版提交缺少数据库句柄")
	}
	rs, err := LoadRuleSet(c.DB, c.Settings, scanRecordRuleID(c, scanID))
	if err != nil && !isDisabledErr(err) {
		// 规则已被删或校验失败时**不静默跳过**：提交阶段拿不到口径就不该删文件。
		return nil, err
	}
	if rs == nil {
		rs = &RuleSet{}
	}

	mu := c.scanLock(scanID)
	mu.Lock()
	defer mu.Unlock()

	var records []Record
	if err := c.DB.WithContext(ctx).Where("scan_id = ? AND status = ?", scanID, RecordStatusPending).
		Order("id asc").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("读取待提交记录失败: %w", err)
	}

	res := &ExecuteResult{ScanID: scanID, Total: len(records)}
	// 统计本次没进队列的那些，免得汇报口径只覆盖一部分记录。
	var scanTotal int64
	if err := c.DB.WithContext(ctx).Model(&Record{}).
		Where("scan_id = ?", scanID).Count(&scanTotal).Error; err != nil {
		log.Printf("[mediaupgrade] 统计扫描记录数失败 scan_id=%d: %v", scanID, err)
	} else if n := int(scanTotal) - len(records); n > 0 {
		res.Excluded = n
	}
	for i := range records {
		rec := &records[i]
		outcome := c.executeOne(ctx, rec, rs)
		switch outcome {
		case outcomeExecuted:
			res.Executed++
		case outcomeExpired:
			res.Expired++
		case outcomeSkipped:
			res.Skipped++
		case outcomeFailed:
			res.Failed++
		}
	}
	res.Message = fmt.Sprintf("提交完成：可提交 %d 条，已执行 %d、判定已过期 %d、跳过 %d、失败 %d",
		res.Total, res.Executed, res.Expired, res.Skipped, res.Failed)
	if res.Excluded > 0 {
		res.Message += fmt.Sprintf("；另有 %d 条在扫描阶段已判定不可执行（不同槽位/新版更差/无可比维度/超出每部剧上限）", res.Excluded)
	}
	log.Printf("[mediaupgrade] %s scan_id=%d", res.Message, scanID)
	return res, nil
}

// outcome 一次单条提交的结果分类。
type outcome int

const (
	outcomeExecuted outcome = iota
	outcomeExpired
	outcomeSkipped
	outcomeFailed
)

// claimRecord 用 CAS 抢提交锁：把 pending 原子改成 executing。
//
// 只有 RowsAffected==1 的那一方拿到执行权。这是跨进程的唯一裁决者，
// 因为 litepan 可能开多实例，内存锁挡不住另一个进程。
func (c *Committer) claimRecord(id uint) bool {
	now := c.clock()
	res := c.DB.Model(&Record{}).
		Where("id = ? AND status = ?", id, RecordStatusPending).
		Updates(map[string]any{
			"status":     RecordStatusExecuting,
			"updated_at": now,
		})
	return res.Error == nil && res.RowsAffected == 1
}

// executeOne 执行单条记录。
func (c *Committer) executeOne(ctx context.Context, rec *Record, rs *RuleSet) outcome {
	// 步骤 1：提交锁。
	if !c.claimRecord(rec.ID) {
		// 抢不到锁：可能已被其它执行方处理，或本条已判终态。都不算失败。
		return outcomeSkipped
	}

	fail := func(status, msg string) outcome {
		now := c.clock()
		_ = c.DB.Model(&Record{}).Where("id = ?", rec.ID).Updates(map[string]any{
			"status": status, "message": msg, "updated_at": now,
		}).Error
		switch status {
		case RecordStatusFailed:
			log.Printf("[mediaupgrade] 记录执行失败 record_id=%d %s", rec.ID, msg)
			return outcomeFailed
		case RecordStatusExpired:
			// 过期是「主动放弃」而不是「没处理成」，必须单独计数：
			// 界面上要能一眼看出这批记录有多少条是因为前提消失而没动手的。
			log.Printf("[mediaupgrade] 记录判定已过期 record_id=%d %s", rec.ID, msg)
			return outcomeExpired
		default:
			return outcomeSkipped
		}
	}

	// 步骤 2：快照复核 —— 重新枚举该作品当前的库内文件，重算哈希。
	live, err := c.reenumerate(rec)
	if err != nil {
		return fail(RecordStatusFailed, "复核库内文件失败："+err.Error())
	}
	liveSnap := marshalSnapshots(live)
	liveHash := hashString(liveSnap)
	if liveHash != rec.SnapshotHash {
		return fail(RecordStatusExpired, MessageExpired+"：提交前复核发现库内文件集合已变化，"+
			"本次判定基于旧的文件状态，已整条放弃，未删除任何文件")
	}

	// 步骤 3：败方文件可达性复核。
	if rec.LoserPath == "" {
		return fail(RecordStatusExecuted, "无败方文件，无需处理")
	}
	_, _, reachable := statLibraryFile(rec.LoserPath)
	if !reachable {
		return fail(RecordStatusSkippedNoAccess,
			"败方文件路径当前不可达（可能被移走或网盘未挂载），出于安全不做删除")
	}

	// 步骤 4：按败方动作处理（keep/delete/move）。
	action := firstNonEmpty(rec.LoserAction, rs.LoserAction, LoserActionKeep)
	switch action {
	case LoserActionKeep:
		now := c.clock()
		_ = c.DB.Model(&Record{}).Where("id = ?", rec.ID).Updates(map[string]any{
			"status": RecordStatusExecuted, "message": "败方动作=保留，未删除任何文件",
			"executed_at": now, "updated_at": now,
		}).Error
		return outcomeExecuted
	case LoserActionDelete:
		return c.applyDelete(rec)
	case LoserActionMove:
		return c.applyMove(rec, firstNonEmpty(rs.MoveDir))
	default:
		return fail(RecordStatusFailed, fmt.Sprintf("未知的败方动作 %q", action))
	}
}

// reenumerate 重新枚举某条记录对应作品的当前库内文件。
//
// 走的是扫描时的同一套口径（同一 source、同一 library_root、同一 workKey 分组），
// 所以复核的是「当时那批文件还在不在」，而不是「现在这个目录下有什么」—— 后者会把
// 用户新加的其它剧也算进来，把本来没变化的判定误判成过期。
func (c *Committer) reenumerate(rec *Record) ([]libFile, error) {
	scan, err := c.loadScan(rec.ScanID)
	if err != nil {
		return nil, err
	}
	rs := &RuleSet{
		Source:         scan.Source,
		LibraryRoot:    scan.LibraryRoot,
		CandidateRoots: SplitRoots(scan.CandidateRoots),
	}
	all, err := listLibraryFiles(context.Background(), c.DB, rs)
	if err != nil {
		return nil, err
	}
	wantKey := rec.SeriesKey + "|" + rec.EpisodeKey
	out := make([]libFile, 0, 4)
	for _, f := range all {
		if workKeyOf(f.Name).Key() == wantKey {
			out = append(out, f)
		}
	}
	return out, nil
}

// loadScan 读扫描任务。
func (c *Committer) loadScan(scanID uint) (*Scan, error) {
	var scan Scan
	if err := c.DB.Where("id = ?", scanID).First(&scan).Error; err != nil {
		return nil, fmt.Errorf("读取扫描 %d 失败: %w", scanID, err)
	}
	return &scan, nil
}

// applyDelete 执行败方删除（走 CloudDeleteBatcher，失败降级逐条）。
func (c *Committer) applyDelete(rec *Record) outcome {
	now := c.clock()
	failures := c.Deleter.DeleteBatch([]string{rec.LoserPath})
	if len(failures) > 0 {
		_ = c.DB.Model(&Record{}).Where("id = ?", rec.ID).Updates(map[string]any{
			"status":          RecordStatusFailed,
			"message":         fmt.Sprintf("删除败方文件失败（%d 项）：%s", len(failures), failures[0].Path),
			"delete_failures": marshalJSON(failures),
			"updated_at":      now,
		}).Error
		return outcomeFailed
	}
	_ = c.DB.Model(&Record{}).Where("id = ?", rec.ID).Updates(map[string]any{
		"status": RecordStatusExecuted, "message": "已按败方动作=删除移除旧文件",
		"delete_failures": "", "executed_at": now, "updated_at": now,
	}).Error
	return outcomeExecuted
}

// applyMove 执行败方移动（把旧文件挪到 move_dir，留个后悔药）。
func (c *Committer) applyMove(rec *Record, moveDir string) outcome {
	now := c.clock()
	if moveDir == "" {
		_ = c.DB.Model(&Record{}).Where("id = ?", rec.ID).Updates(map[string]any{
			"status": RecordStatusFailed, "message": "败方动作=移动但未配置目标目录", "updated_at": now,
		}).Error
		return outcomeFailed
	}
	if err := os.MkdirAll(moveDir, 0o755); err != nil {
		_ = c.DB.Model(&Record{}).Where("id = ?", rec.ID).Updates(map[string]any{
			"status": RecordStatusFailed, "message": "创建移动目标目录失败：" + err.Error(), "updated_at": now,
		}).Error
		return outcomeFailed
	}
	dst := filepath.Join(moveDir, filepath.Base(rec.LoserPath))
	if err := os.Rename(rec.LoserPath, dst); err != nil {
		_ = c.DB.Model(&Record{}).Where("id = ?", rec.ID).Updates(map[string]any{
			"status": RecordStatusFailed, "message": "移动败方文件失败：" + err.Error(), "updated_at": now,
		}).Error
		return outcomeFailed
	}
	_ = c.DB.Model(&Record{}).Where("id = ?", rec.ID).Updates(map[string]any{
		"status": RecordStatusExecuted, "message": "已按败方动作=移动挪走旧文件：" + dst,
		"executed_at": now, "updated_at": now,
	}).Error
	return outcomeExecuted
}

// isDisabledErr 判定是否「功能未启用」。
func isDisabledErr(err error) bool { return err == ErrDisabled }

// scanRecordRuleID 读取某次扫描用的规则 ID。
func scanRecordRuleID(c *Committer, scanID uint) uint {
	var ruleID uint
	if err := c.DB.Model(&Record{}).Where("scan_id = ?", scanID).
		Select("rule_id").Order("id asc").Limit(1).Scan(&ruleID).Error; err != nil {
		return 0
	}
	return ruleID
}
