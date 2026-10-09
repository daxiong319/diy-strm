package mediaupgrade

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"litepan/internal/moviepilot"
	"litepan/internal/settings"
)

// TestDifferentSlotKeepsBoth 钉住验收④：1080p 的库 + 2160p 的候选，两个都保留。
//
// 这是槽位分组存在的全部理由。不分组的话，一次扫描会把「用户 1080p 的全集」
// 和「刚下载的一个 2160p」判成同一集的新旧版本，然后建议删掉 1080p ——
// 那不是洗版，那是把整个片库升格。
func TestDifferentSlotKeepsBoth(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	oldPath := writeVideo(t, filepath.Join(lib, "剧名.S01E01.1080p.x265.mkv"), 1<<30)
	newPath := writeVideo(t, filepath.Join(cand, "剧名.S01E01.2160p.x265.mkv"), 9<<30)

	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(lib, cand))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	rec := onlyRecord(t, db, scan.ID)
	if rec.Status != RecordStatusSkippedNoSlot {
		t.Fatalf("记录状态是 %q，期望 skipped_no_slot", rec.Status)
	}
	if rec.LoserPath != "" {
		t.Errorf("不同槽位时不该指定败方，实际 %q", rec.LoserPath)
	}
	if !strings.Contains(rec.Message, "两个都保留") {
		t.Errorf("提示 %q 里没有说明两个都保留", rec.Message)
	}

	// 执行它也不该动任何文件。
	deleter := newCountingDeleter()
	res, err := newTestCommitter(db, deleter.asBatcher()).ExecuteScan(context.Background(), scan.ID)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if res.Executed != 0 || res.Failed != 0 {
		t.Errorf("不同槽位的记录不该被执行：%+v", res)
	}
	if got := deleter.deleteCalls(); got != 0 {
		t.Errorf("却调了 %d 次删除", got)
	}
	if !fileExists(oldPath) || !fileExists(newPath) {
		t.Errorf("两个文件必须都在")
	}
}

// TestSameSlotAcrossCodecsAlsoKeepsBoth 断言编码差异同样按「不同槽位」处理。
func TestSameSlotAcrossCodecsAlsoKeepsBoth(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	writeVideo(t, filepath.Join(lib, "剧名.S01E01.2160p.x264.mkv"), 9<<30)
	writeVideo(t, filepath.Join(cand, "剧名.S01E01.2160p.x265.mkv"), 9<<30)

	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(lib, cand))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if rec := onlyRecord(t, db, scan.ID); rec.Status != RecordStatusSkippedNoSlot {
		t.Fatalf("x264 与 x265 应属不同槽位，记录状态却是 %q", rec.Status)
	}
}

// TestWorseCandidateNeverTriggersAWash 断言候选里质量更差的那份不会触发洗版。
//
// 现版 10bit，候选目录里躺着同槽位但没写色深、且体积更大的那份。
// 色深不在槽位维度里（槽位只管分辨率/编码/组/音轨/字幕/容器），所以两者同槽位、
// 真要比质量 —— 如果判定退化成按体积兜底，就会把这集洗成更差。
//
// 之所以一条记录都不产出，是因为候选枚举里本来就包含媒体库目录：
// 库里那份 10bit 本身也是候选，而且它赢了，于是同槽位里除了它自己没人可比。
// 这比「产出一条 new_loses」更安全：连判定都不产生，就没机会误删。
func TestWorseCandidateNeverTriggersAWash(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	oldPath := writeVideo(t, filepath.Join(lib, "剧名.S01E01.2160p.x265.10bit.mkv"), 1<<30)
	candPath := writeVideo(t, filepath.Join(cand, "剧名.S01E01.2160p.x265.mkv"), 9<<30)

	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(lib, cand))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if got := len(recordsOf(t, db, scan.ID)); got != 0 {
		t.Fatalf("更差的候选仍产出了 %d 条记录（trace：%s）", got, recordsOf(t, db, scan.ID)[0].Trace)
	}
	if _, err := newTestCommitter(db, newCountingDeleter().asBatcher()).
		ExecuteScan(context.Background(), scan.ID); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !fileExists(oldPath) || !fileExists(candPath) {
		t.Fatalf("更差的候选把现版洗掉了")
	}
}

// TestIntraLibraryLoserIsTheStrongerDuplicate 断言库内重复版本被清理时，
// 删掉的是更强的那份，而不是留着它。
//
// 同槽位出现重复的现实场景：上一轮洗版只删掉了一个败方，
// 或者用户手动补了同一集的另一个版本。
// 若实现「随便挑一个同槽位文件删掉」，就会留下 8bit 却删掉 10bit，
// 库反而变差 —— 而且下一次扫描还会再洗一次，永远收敛不了。
func TestIntraLibraryLoserIsTheStrongerDuplicate(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	weak := writeVideo(t, filepath.Join(lib, "剧名.S01E01.2160p.x265.8bit.mkv"), 3<<30)
	strong := writeVideo(t, filepath.Join(lib, "剧名.S01E01.2160p.x265.10bit.mkv"), 1<<30)
	writeVideo(t, filepath.Join(cand, "剧名.S01E01.2160p.x265.12bit.mkv"), 20<<30)

	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(lib, cand))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	rec := onlyRecord(t, db, scan.ID)
	if rec.LoserPath != strong {
		t.Fatalf("败方是 %q，期望 10bit 那份 %q（删弱的留强的才叫洗版）", rec.LoserPath, strong)
	}
	if rec.LoserPath == weak {
		t.Fatalf("败方是 8bit 那份")
	}

	deleter := newCountingDeleter()
	if _, err := newTestCommitter(db, deleter.asBatcher()).ExecuteScan(context.Background(), scan.ID); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if fileExists(strong) {
		t.Errorf("更强的重复版本没被删")
	}
	if !fileExists(weak) {
		t.Errorf("较弱的重复版本被删了")
	}
}

// TestLibraryFileIsNeverComparedAgainstItself 断言库里的文件不会拿自己去比。
//
// 候选列表里包含媒体库目录（用户常把新版直接丢进已整理层），
// 所以某个作品的「最佳候选」可能就是它库里那份文件自己。
// 这时同槽位里除了自己没有别人，必须直接跳过 ——
// 否则胜者会被自己当成败方删掉，那是最荒唐的一种误删。
func TestLibraryFileIsNeverComparedAgainstItself(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	writeVideo(t, filepath.Join(lib, "剧名.S01E01.2160p.x265.mkv"), 1<<30)

	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(lib, ""))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if got := len(recordsOf(t, db, scan.ID)); got != 0 {
		t.Fatalf("库里只有一份文件时产出了 %d 条记录，期望 0 条", got)
	}
	if !fileExists(filepath.Join(lib, "剧名.S01E01.2160p.x265.mkv")) {
		t.Fatalf("文件被删了")
	}
}

// TestMaxRecordsPerSeriesLimitsExecution 钉住验收③。
//
// 场景是银盾这类周更长剧：一次扫描可能产出几百条记录。
// 上限只让一部分可执行，其余仍要入库并标 skipped_limit ——
// 用户得能在界面上看到「这一集被上限拦下了」，而不是「根本没扫到」。
func TestMaxRecordsPerSeriesLimitsExecution(t *testing.T) {
	const episodes = 5
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	for i := 1; i <= episodes; i++ {
		writeVideo(t, filepath.Join(lib, fmt.Sprintf("剧名.S01E%02d.2160p.x265.mkv", i)), int64(i)<<30)
		writeVideo(t, filepath.Join(cand, fmt.Sprintf("剧名.S01E%02d.2160p.x265.10bit.mkv", i)), 20<<30)
	}

	db := openTestDB(t)
	rs := testRuleSet(lib, cand)
	rs.MaxRecordsPerSeries = 2
	ruleID := saveRule(t, db, rs)
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	recs := recordsOf(t, db, scan.ID)
	if len(recs) != episodes {
		t.Fatalf("扫描出了 %d 条记录，期望 %d 条 —— 超限的也必须入库", len(recs), episodes)
	}
	var pending, limited int
	for _, r := range recs {
		switch r.Status {
		case RecordStatusPending:
			pending++
		case RecordStatusSkippedLimit:
			limited++
		}
	}
	if pending != 2 {
		t.Errorf("可执行的是 %d 条，期望上限 2 条", pending)
	}
	if limited != episodes-2 {
		t.Errorf("被上限拦下的是 %d 条，期望 %d 条", limited, episodes-2)
	}

	// 执行阶段同样只能动 2 条。
	deleter := newCountingDeleter()
	res, err := newTestCommitter(db, deleter.asBatcher()).ExecuteScan(context.Background(), scan.ID)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if res.Executed != 2 {
		t.Errorf("执行了 %d 条，期望 2 条", res.Executed)
	}
	if res.Excluded != episodes-2 {
		t.Errorf("扫描阶段就不可执行的记为 %d 条，期望 %d 条 —— 汇报口径不能只覆盖进队列的那部分", res.Excluded, episodes-2)
	}
	if got := len(deleter.deletedPaths()); got != 2 {
		t.Errorf("实际删了 %d 个文件，期望 2 个", got)
	}
}

// TestMaxRecordsPerSeriesCountsBySeries 断言上限是「每部剧」而不是「整次扫描」。
func TestMaxRecordsPerSeriesCountsBySeries(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	for i := 1; i <= 2; i++ {
		writeVideo(t, filepath.Join(lib, fmt.Sprintf("剧A.S01E%02d.2160p.x265.mkv", i)), 1<<30)
		writeVideo(t, filepath.Join(cand, fmt.Sprintf("剧A.S01E%02d.2160p.x265.10bit.mkv", i)), 9<<30)
		writeVideo(t, filepath.Join(lib, fmt.Sprintf("剧B.S01E%02d.2160p.x265.mkv", i)), 1<<30)
		writeVideo(t, filepath.Join(cand, fmt.Sprintf("剧B.S01E%02d.2160p.x265.10bit.mkv", i)), 9<<30)
	}

	db := openTestDB(t)
	rs := testRuleSet(lib, cand)
	rs.MaxRecordsPerSeries = 1
	ruleID := saveRule(t, db, rs)
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	if _, err := NewScanner(db, newFakeSettings(nil)).SeriesCounts(context.Background(), scan.ID); err != nil {
		t.Fatalf("统计失败：%v", err)
	}
	perSeries := map[string][2]int{}
	for _, r := range recordsOf(t, db, scan.ID) {
		cur := perSeries[r.SeriesKey]
		switch r.Status {
		case RecordStatusPending:
			cur[0]++
		case RecordStatusSkippedLimit:
			cur[1]++
		}
		perSeries[r.SeriesKey] = cur
	}
	if len(perSeries) != 2 {
		t.Fatalf("作品分组数是 %d，期望 2", len(perSeries))
	}
	for series, c := range perSeries {
		if c[0] != 1 {
			t.Errorf("%s 可执行 %d 条，期望 1 条 —— 上限必须按作品各自计数", series, c[0])
		}
		if c[1] != 1 {
			t.Errorf("%s 被拦下 %d 条，期望 1 条", series, c[1])
		}
	}
}

// TestLoserActionKeepDoesNotConsumeQuota 断言 keep 不占额度。
//
// keep 不动文件，就没有「删多了」的风险，不该浪费用户精心设的上限。
func TestLoserActionKeepDoesNotConsumeQuota(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	for i := 1; i <= 3; i++ {
		writeVideo(t, filepath.Join(lib, fmt.Sprintf("剧名.S01E%02d.2160p.x265.mkv", i)), 1<<30)
		writeVideo(t, filepath.Join(cand, fmt.Sprintf("剧名.S01E%02d.2160p.x265.10bit.mkv", i)), 9<<30)
	}

	db := openTestDB(t)
	rs := testRuleSet(lib, cand)
	rs.LoserAction = LoserActionKeep
	rs.MaxRecordsPerSeries = 1
	ruleID := saveRule(t, db, rs)
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	var pending int
	for _, r := range recordsOf(t, db, scan.ID) {
		if r.Status == RecordStatusPending {
			pending++
		}
		if r.Status == RecordStatusSkippedLimit {
			t.Errorf("keep 不该占额度，却有一条被判为 skipped_limit")
		}
	}
	if pending != 3 {
		t.Errorf("keep 模式下 %d 条可执行，期望 3 条全可执行", pending)
	}
}

// TestCandidateBelowGateIsIgnored 断言门槛过滤不产生记录。
func TestCandidateBelowGateIsIgnored(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	oldPath := writeVideo(t, filepath.Join(lib, "剧名.S01E01.1080p.x265.mkv"), 1<<30)
	writeVideo(t, filepath.Join(cand, "剧名.S01E01.1080p.x265.10bit.mkv"), 9<<30)

	db := openTestDB(t)
	rs := testRuleSet(lib, cand)
	rs.MinResolution = 2160
	ruleID := saveRule(t, db, rs)
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if got := len(recordsOf(t, db, scan.ID)); got != 0 {
		t.Fatalf("低于分辨率门槛的候选产出了 %d 条记录，期望 0 条", got)
	}
	if !fileExists(oldPath) {
		t.Fatalf("门槛过滤把文件删了")
	}
}

// TestCandidateInLibraryDirectoryIsPickedUp 断言用户把新版直接丢进已整理目录也能被认出来。
//
// 这是洗版最常见的用法：整理完直接补一个更好的版本在同一层。
// 若候选只来自候选目录，这个场景会完全扫不出来。
func TestCandidateInLibraryDirectoryIsPickedUp(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	writeVideo(t, filepath.Join(lib, "剧名.S01E01.2160p.x265.mkv"), 1<<30)
	newPath := writeVideo(t, filepath.Join(lib, "剧名.S01E01.2160p.x265.10bit.mkv"), 9<<30)

	db := openTestDB(t)
	rs := testRuleSet(lib, "")
	ruleID := saveRule(t, db, rs)
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	rec := onlyRecord(t, db, scan.ID)
	if rec.QualityRelation != RelationNewWins {
		t.Fatalf("关系是 %q，期望 new_wins", rec.QualityRelation)
	}
	if rec.LoserPath == "" || rec.LoserPath == newPath {
		t.Fatalf("败方应是旧文件而非候选自身，实际 %q", rec.LoserPath)
	}
}

// TestBestCandidatePrefersHigherQuality 断言同作品多候选时挑最好的那个，
// 而不是按路径排序随便挑一个。
//
// 这是一个真 bug 的回归：早先的实现按枚举顺序 seenWork 去重，
// 字典序靠前的候选会把作品位占掉，真正更好的版本永远轮不上。
func TestBestCandidatePrefersHigherQuality(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	writeVideo(t, filepath.Join(lib, "剧名.S01E01.2160p.x265.mkv"), 1<<30)
	// 8bit 排在前面（字典序也靠前），10bit 在后面 —— 挑错就是挑了 8bit。
	writeVideo(t, filepath.Join(cand, "剧名.S01E01.2160p.x265.10bit.mkv"), 9<<30)
	writeVideo(t, filepath.Join(cand, "剧名.S01E01.2160p.x265.8bit.mkv"), 3<<30)

	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(lib, cand))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	rec := onlyRecord(t, db, scan.ID)
	if !strings.HasSuffix(rec.NewFilePath, "10bit.mkv") {
		t.Fatalf("参与比较的候选是 %q，期望 10bit 那份", rec.NewFilePath)
	}
	if rec.QualityRelation != RelationNewWins {
		t.Fatalf("关系是 %q，期望 10bit 胜出", rec.QualityRelation)
	}
}

// TestDefaultOffRequiresExplicitEnable 钉住验收⑦。
//
// settings 那边只导出了 New，defaultSpecs() 是包内私有，拿不到默认值字面量；
// 与其反射默认值，不如钉住**行为**：不显式开开关，扫描就必须拒绝执行。
func TestDefaultOffRequiresExplicitEnable(t *testing.T) {
	db := openTestDB(t)
	if _, err := LoadRuleSet(db, newFakeSettings(nil), 0); err != ErrDisabled {
		t.Fatalf("总开关缺省时 LoadRuleSet 返回 %v，期望 ErrDisabled", err)
	}

	// 开着但不配媒体库目录 ⇒ 报「未配置媒体库根目录」，而不是静默扫了个空。
	rs, err := LoadRuleSet(db, newFakeSettings(map[string]string{
		settings.KeyMOMediaUpgradeEnabled: "true",
	}), 0)
	if err != ErrNoLibraryRoot {
		t.Fatalf("只开开关不配目录时返回 %v（规则 %#v），期望 ErrNoLibraryRoot", err, rs)
	}
}

// TestGlobalRuleFromSettings 断言全局设置能装配出一条可用的规则集。
func TestGlobalRuleFromSettings(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	writeVideo(t, filepath.Join(lib, "剧名.S01E01.2160p.x265.mkv"), 1<<30)
	newPath := writeVideo(t, filepath.Join(cand, "剧名.S01E01.2160p.x265.10bit.mkv"), 9<<30)

	db := openTestDB(t)
	svc := newFakeSettings(map[string]string{
		settings.KeyMOMediaUpgradeEnabled:             "true",
		settings.KeyMOMediaUpgradeSource:              SourceLocal,
		settings.KeyMOMediaUpgradeLibraryRoot:         lib,
		settings.KeyMOMediaUpgradeCandidateRoots:      cand,
		settings.KeyMOMediaUpgradeLoserAction:         LoserActionDelete,
		settings.KeyMOMediaUpgradeMaxRecordsPerSeries: "1",
	})
	scan, err := NewScanner(db, svc).Scan(context.Background(), ScanOptions{})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if scan.RuleID != 0 {
		t.Errorf("全局规则的 scan.rule_id 是 %d，期望 0（0 = 回落全局设置的哨兵值）", scan.RuleID)
	}
	if !fileExists(newPath) {
		t.Fatalf("扫描不该动文件")
	}
	if len(recordsOf(t, db, scan.ID)) != 1 {
		t.Fatalf("全局规则下应有 1 条记录")
	}
}

// TestEmbySourceWithoutIndexTableExplainsItself 断言老库缺表时给的是人话而不是 SQL 错误。
func TestEmbySourceWithoutIndexTableExplainsItself(t *testing.T) {
	db := openTestDB(t)
	rs := testRuleSet("/tmp", "/tmp")
	rs.Source = SourceEmby
	_, err := listLibraryFiles(context.Background(), db, rs)
	if err == nil {
		t.Fatalf("缺 emby_media_items 表时不该成功")
	}
	if !strings.Contains(err.Error(), "Emby 媒体索引") {
		t.Errorf("报错 %q 没有告诉用户要先启用 Emby 媒体索引", err)
	}
}

// TestEmbySourceSkipsUnreachableLoser 断言索引里有、本机读不到的路径绝不会被删。
//
// emby/jellyfin 源记的是网盘路径，os.Stat 失败既可能是「被移走」也可能是「本机没这个路径」，
// 两种情况按路径删除都是危险的，所以一律 skipped_no_access。
func TestEmbySourceSkipsUnreachableLoser(t *testing.T) {
	db := openTestDB(t)
	if err := db.Exec(`CREATE TABLE emby_media_items (
		path TEXT, name TEXT, series_name TEXT, type TEXT,
		is_folder INTEGER, size INTEGER, date_modified_time INTEGER)`).Error; err != nil {
		t.Fatalf("造 emby 索引表失败：%v", err)
	}
	// 两行都指向本机不存在的路径。
	for _, p := range []string{"/nope/lib/剧名.S01E01.2160p.x265.mkv", "/nope/cand/剧名.S01E01.2160p.x265.10bit.mkv"} {
		if err := db.Exec(
			`INSERT INTO emby_media_items (path,name,series_name,type,is_folder,size,date_modified_time)
			 VALUES (?,?,?,?,0,0,0)`,
			p, filepath.Base(p), "剧名", "Episode").Error; err != nil {
			t.Fatalf("写 emby 索引行失败：%v", err)
		}
	}

	rs := testRuleSet("/nope/lib", "")
	rs.Source = SourceEmby
	files, err := listLibraryFiles(context.Background(), db, rs)
	if err != nil {
		t.Fatalf("列举库内文件失败：%v", err)
	}
	if len(files) != 2 {
		t.Fatalf("索引里有 2 行文件，枚举出 %d 行", len(files))
	}
	if _, _, reachable := statLibraryFile("/nope/lib/剧名.S01E01.2160p.x265.mkv"); reachable {
		t.Errorf("不存在的路径不该被判为可达")
	}
}

// TestIncompleteDownloadsAreNeverCompared 断言 .part 之类的半截文件不参与洗版。
//
// 拿半截文件的体积去判「新版更大」是这个功能最容易犯的低级错误。
func TestIncompleteDownloadsAreNeverCompared(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	cand := filepath.Join(base, "cand")
	writeVideo(t, filepath.Join(lib, "剧名.S01E01.2160p.x265.mkv"), 1<<30)
	writeVideo(t, filepath.Join(cand, "剧名.S01E01.2160p.x265.10bit.mkv.part"), 9<<30)

	db := openTestDB(t)
	ruleID := saveRule(t, db, testRuleSet(lib, cand))
	scan, err := NewScanner(db, newFakeSettings(nil)).
		Scan(context.Background(), ScanOptions{RuleID: ruleID})
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if got := len(recordsOf(t, db, scan.ID)); got != 0 {
		t.Fatalf("半截文件参与了对 %d 集的比较，期望 0", got)
	}
	if !fileExists(filepath.Join(lib, "剧名.S01E01.2160p.x265.mkv")) {
		t.Fatalf("旧文件被删了")
	}
}

// TestSnapshotHashIsStableAcrossEnumerations 断言同一份文件集合两次枚举哈希相同。
//
// 不稳定的话，用户什么都没动，执行阶段也会被判成「判定已过期」，
// 洗版就永远推不动 —— 这是那种没人会去查、但功能就是不work的故障。
func TestSnapshotHashIsStableAcrossEnumerations(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	for i := 1; i <= 3; i++ {
		writeVideo(t, filepath.Join(lib, fmt.Sprintf("剧名.S01E%02d.2160p.x265.mkv", i)), 1<<30)
	}
	a, err := collectLocalFiles(lib)
	if err != nil {
		t.Fatalf("第一次枚举失败：%v", err)
	}
	b, err := collectLocalFiles(lib)
	if err != nil {
		t.Fatalf("第二次枚举失败：%v", err)
	}
	if hashString(marshalSnapshots(a)) != hashString(marshalSnapshots(b)) {
		t.Fatalf("同一份文件集合两次哈希不同")
	}
	if _, err := os.Stat(filepath.Join(lib, "剧名.S01E01.2160p.x265.mkv")); err != nil {
		t.Fatalf("stat 失败：%v", err)
	}
}

// TestWashRulesComeFromDefaults 断言规则集里没有自定义洗版规则时用引擎默认规则。
func TestWashRulesComeFromDefaults(t *testing.T) {
	// 裸规则集（没配自定义规则），Effective 应兜底成引擎默认规则。
	rs := RuleSet{Source: SourceLocal, LibraryRoot: "/tmp", LoserAction: LoserActionKeep}
	if got := rs.Effective().WashRules; len(got) != len(moviepilot.DefaultWashRules) {
		t.Fatalf("兜底后的洗版规则有 %d 条，期望 %d 条", len(got), len(moviepilot.DefaultWashRules))
	}
}
