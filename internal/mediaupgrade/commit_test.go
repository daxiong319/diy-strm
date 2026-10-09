package mediaupgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 同一槽位内的洗版用例统一用这一对文件：
//
//	现版：2160p x265 8bit（被压过色深的那种）
//	新版：2160p x265 10bit
//
// 为什么不用「1080p 换成 2160p」这种更顺手的例子：分辨率在槽位里，
// 1080p 与 2160p 根本不同槽，按设计就该两个都保留（见 slot_test.go）。
// 槽位内真正能分出高下的维度是 format / bitdepth / 体积，
// 10bit 替 8bit 正是现实里最常见的一次洗版。
const (
	oldName = "剧名.S01E01.2160p.x265.mkv"
	newName = "剧名.S01E01.2160p.x265.10bit.mkv"
)

// stageOldAndNew 造出「现版在库、新版在候选目录」的场景。
func stageOldAndNew(t *testing.T) (oldPath, newPath string) {
	t.Helper()
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	oldPath = writeVideo(t, filepath.Join(lib, oldName), 3<<30)
	newPath = writeVideo(t, filepath.Join(cand, newName), 9<<30)
	return oldPath, newPath
}

// TestScanOnlyJudgesAndNeverTouchesFiles 断言扫描阶段一个文件都不动。
//
// 这是整套设计的地基：判定与提交必须分离。
// 如果扫描顺手删了文件，后面所有的快照复核、提交锁都成了摆设。
func TestScanOnlyJudgesAndNeverTouchesFiles(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)

	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath)))
	scanner := NewScanner(db, newFakeSettings(nil))

	scan, err := scanner.Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	rec := onlyRecord(t, db, scan.ID)
	if rec.Status != RecordStatusPending {
		t.Fatalf("扫描产出的记录状态是 %q，期望 pending（扫描只判定不执行）", rec.Status)
	}
	if rec.QualityRelation != RelationNewWins {
		t.Fatalf("关系是 %q，期望 new_wins（trace：%s）", rec.QualityRelation, rec.Trace)
	}
	if rec.LoserPath != oldPath {
		t.Errorf("败方是 %q，期望旧文件 %q", rec.LoserPath, oldPath)
	}
	if rec.SnapshotHash == "" {
		t.Error("快照哈希为空，提交阶段就无从复核")
	}
	for _, p := range []string{oldPath, newPath} {
		if !fileExists(p) {
			t.Fatalf("扫描删掉了文件 %s —— 扫描阶段绝不允许动文件", p)
		}
	}
}

// TestConcurrentExecuteDeletesExactlyOnce 是这套设计最核心的用例（验收②的并发面）。
//
// 8 个**互相独立的** Committer（各自一套内存锁）同时抢同一条记录：
// 提交锁必须靠数据库 CAS 决出唯一赢家，而不是靠进程内互斥。
// 如果锁只做在内存里，这 8 个会各自复核各自删一遍，断言 deleteCalls()==1 会当场失败。
func TestConcurrentExecuteDeletesExactlyOnce(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)

	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath)))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	deleter := newCountingDeleter()
	const goroutines = 8
	var (
		wg       sync.WaitGroup
		start    = make(chan struct{})
		results  = make([]*ExecuteResult, goroutines)
		errsList = make([]error, goroutines)
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 每个 goroutine 一个全新 Committer：内存锁互不相干，
			// 抢锁只能靠 DB 里的 UPDATE ... WHERE status='pending'。
			committer := newTestCommitter(db, deleter.asBatcher())
			<-start
			results[idx], errsList[idx] = committer.ExecuteScan(context.Background(), scan.ID)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errsList {
		if err != nil {
			t.Fatalf("第 %d 个执行器报错：%v", i, err)
		}
		if results[i] == nil {
			t.Fatalf("第 %d 个执行器没拿到结果", i)
		}
	}

	rec := onlyRecord(t, db, scan.ID)
	if rec.Status != RecordStatusExecuted {
		t.Fatalf("记录最终状态是 %q，期望 executed", rec.Status)
	}
	if fileExists(oldPath) {
		t.Errorf("旧文件还在，loser_action=delete 应该删掉它")
	}
	if !fileExists(newPath) {
		t.Errorf("新版被删了 —— 洗版只动败方，候选永远不能被删")
	}
	if got := deleter.deleteCalls(); got != 1 {
		t.Errorf("删除接口被调用 %d 次，期望恰好 1 次 —— 提交锁失效，%d 个执行器都动了手", got, goroutines)
	}

	// 只有拿到锁的那一个会报 executed，其余要么看到 0 条待提交，要么抢锁失败。
	var executed, expired int
	for _, r := range results {
		executed += r.Executed
		expired += r.Expired
	}
	if executed != 1 {
		t.Errorf("各执行器报告的 executed 之和是 %d，期望 1", executed)
	}
	if expired != 0 {
		t.Errorf("不该出现过期标记（expired=%d）", expired)
	}
}

// TestExecuteExpiresWhenLibraryChanged 断言验收②：
// 扫描之后用户往库里塞了同集的另一个版本，执行阶段必须整条标 expired 且一个文件都不删。
//
// 这正是判定与提交分离的意义：判定是基于一个快照做的，
// 快照对不上就说明判定的前提没了，硬着头支执行就是在删一个用户可能刚放回去的文件。
func TestExecuteExpiresWhenLibraryChanged(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)
	lib := filepath.Dir(oldPath)

	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(lib, filepath.Dir(newPath)))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if onlyRecord(t, db, scan.ID).Status != RecordStatusPending {
		t.Fatalf("前置条件不成立：扫描后记录不是 pending")
	}

	// 用户往媒体库里塞了同一集的另一个版本（1080p，与现版不同槽位但同作品）。
	// 快照记的是该作品的**全部**库内文件，所以它一变哈希就变。
	writeVideo(t, filepath.Join(lib, "剧名.S01E01.1080p.x265.mkv"), 2<<30)

	deleter := newCountingDeleter()
	res, err := newTestCommitter(db, deleter.asBatcher()).ExecuteScan(context.Background(), scan.ID)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if res.Expired != 1 {
		t.Fatalf("expired=%d，期望 1", res.Expired)
	}

	rec := onlyRecord(t, db, scan.ID)
	if rec.Status != RecordStatusExpired {
		t.Fatalf("记录状态是 %q，期望 expired", rec.Status)
	}
	if !strings.Contains(rec.Message, MessageExpired) {
		t.Errorf("提示信息 %q 里没有 %q", rec.Message, MessageExpired)
	}
	if !fileExists(oldPath) {
		t.Fatalf("判定已过期却把旧文件删了 —— 这正是要防的误删")
	}
	if got := deleter.deleteCalls(); got != 0 {
		t.Errorf("判定已过期仍然调了 %d 次删除，期望 0 次", got)
	}
}

// TestExecuteExpiresWhenOldFileContentChanged 断言同路径同文件名、只改了内容也判过期。
func TestExecuteExpiresWhenOldFileContentChanged(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)
	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath)))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	// 文件名不变，只把内容换了（大小变了）⇒ 快照哈希对不上。
	if err := os.Truncate(oldPath, 5<<30); err != nil {
		t.Fatalf("改写旧文件大小失败：%v", err)
	}

	deleter := newCountingDeleter()
	if _, err := newTestCommitter(db, deleter.asBatcher()).ExecuteScan(context.Background(), scan.ID); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	rec := onlyRecord(t, db, scan.ID)
	if rec.Status != RecordStatusExpired {
		t.Fatalf("旧文件内容变了但记录状态是 %q，期望 expired", rec.Status)
	}
	if !fileExists(oldPath) {
		t.Fatalf("内容变了却把旧文件删了")
	}
}

// TestExecuteIsIdempotentForSameScan 断言同一个 scan 重复执行不会第二次动手。
func TestExecuteIsIdempotentForSameScan(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)
	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath)))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	deleter := newCountingDeleter()
	committer := newTestCommitter(db, deleter.asBatcher())
	if _, err := committer.ExecuteScan(context.Background(), scan.ID); err != nil {
		t.Fatalf("首次执行失败：%v", err)
	}
	if fileExists(oldPath) {
		t.Fatalf("首次执行没删掉旧文件")
	}
	if _, err := committer.ExecuteScan(context.Background(), scan.ID); err != nil {
		t.Fatalf("二次执行失败：%v", err)
	}
	if got := deleter.deleteCalls(); got != 1 {
		t.Errorf("执行两次共调用删除 %d 次，期望仍是 1 次", got)
	}
}

// TestLoserActionKeepRetainsOldFile 断言验收⑤的 keep 分支。
func TestLoserActionKeepRetainsOldFile(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)
	db := openTestDB(t)
	rs := testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath))
	rs.LoserAction = LoserActionKeep
	ruleID := saveRule(t, db, rs)

	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	rec := onlyRecord(t, db, scan.ID)
	if rec.LoserAction != LoserActionKeep {
		t.Fatalf("记录里的 loser_action 是 %q，期望 keep", rec.LoserAction)
	}

	deleter := newCountingDeleter()
	res, err := newTestCommitter(db, deleter.asBatcher()).ExecuteScan(context.Background(), scan.ID)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if res.Executed != 1 {
		t.Fatalf("executed=%d，期望 1（keep 也是一个已完成的动作）", res.Executed)
	}
	if !fileExists(oldPath) {
		t.Fatalf("loser_action=keep 却把旧文件删了")
	}
	if got := deleter.deleteCalls(); got != 0 {
		t.Errorf("loser_action=keep 却调了 %d 次删除", got)
	}
}

// TestLoserActionMoveMovesOldFile 断言 move 分支落到 move_dir 而不是删掉。
func TestLoserActionMoveMovesOldFile(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)
	moveDir := filepath.Join(t.TempDir(), "trash")

	db := openTestDB(t)
	rs := testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath))
	rs.LoserAction = LoserActionMove
	rs.MoveDir = moveDir
	ruleID := saveRule(t, db, rs)

	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	deleter := newCountingDeleter()
	if _, err := newTestCommitter(db, deleter.asBatcher()).ExecuteScan(context.Background(), scan.ID); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if fileExists(oldPath) {
		t.Errorf("move 分支后旧文件还在原处")
	}
	moved := filepath.Join(moveDir, filepath.Base(oldPath))
	if !fileExists(moved) {
		t.Errorf("旧文件没有出现在移动目标目录 %s", moved)
	}
	if got := deleter.deleteCalls(); got != 0 {
		t.Errorf("move 分支却调了 %d 次删除", got)
	}
}

// TestMoveWithoutMoveDirIsRejected 断言「移动但不配目标目录」在规则校验阶段就被拦下。
//
// 关键在于拦下之后**不删**：没配目标目录就退回删除，是最不能出现的一种兜底。
func TestMoveWithoutMoveDirIsRejected(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)
	db := openTestDB(t)
	rs := testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath))
	rs.LoserAction = LoserActionMove
	rs.MoveDir = ""
	ruleID := saveRule(t, db, rs)

	_, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err == nil {
		t.Fatalf("没配移动目标目录却扫描成功了")
	}
	if !strings.Contains(err.Error(), "移动目标目录") {
		t.Errorf("报错 %q 里没有说明是缺移动目标目录", err)
	}
	if !fileExists(oldPath) {
		t.Fatalf("校验失败竟然把旧文件删了")
	}
	if got := len(recordsOf(t, db, 1)); got != 0 {
		t.Errorf("扫描失败却留下了 %d 条记录", got)
	}
}

// TestUnreachableLibraryRootProducesNoRecords 断言库目录读不到时不硬造记录。
func TestUnreachableLibraryRootProducesNoRecords(t *testing.T) {
	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet("/nonexistent/lib", "/nonexistent/cand"))

	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	// 库目录不存在 ⇒ 没有库内文件 ⇒ 无从洗版，不产生记录，这是期望的。
	if got := len(recordsOf(t, db, scan.ID)); got != 0 {
		t.Fatalf("库目录不存在却产出了 %d 条记录", got)
	}
}

// TestLoserVanishedBeforeCommitMarksExpired 断言现版在扫描后被移走时不删任何东西。
func TestLoserVanishedBeforeCommitMarksExpired(t *testing.T) {
	oldPath, newPath := stageOldAndNew(t)
	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(filepath.Dir(oldPath), filepath.Dir(newPath)))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	// 用户自己把现版挪走了：库内文件集合变了，复核阶段就会拦下。
	if err := os.Remove(oldPath); err != nil {
		t.Fatalf("移除旧文件失败：%v", err)
	}
	deleter := newCountingDeleter()
	if _, err := newTestCommitter(db, deleter.asBatcher()).ExecuteScan(context.Background(), scan.ID); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	rec := onlyRecord(t, db, scan.ID)
	if rec.Status != RecordStatusExpired {
		t.Errorf("现版消失后记录状态是 %q，期望 expired", rec.Status)
	}
	if got := deleter.deleteCalls(); got != 0 {
		t.Errorf("却调了 %d 次删除", got)
	}
}
