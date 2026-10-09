package mediaupgrade

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/glebarez/sqlite"

	"litepan/internal/moviepilot"
)

// 测试用设置：一层可编程的内存实现，键沿用 settings 包里的常量名。
//
// 之所以不复用 settings.Service：它要连 configs 表、要跑 registry 的 normalize，
// 而这里只需要「按 key 给我一个值」，用假实现能让用例一眼看清配置到底是什么。
type fakeSettings struct {
	mu sync.RWMutex
	v  map[string]string
}

func newFakeSettings(pairs map[string]string) *fakeSettings {
	m := map[string]string{}
	for k, val := range pairs {
		m[k] = val
	}
	return &fakeSettings{v: m}
}

func (f *fakeSettings) String(key string) string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.v[key]
}

func (f *fakeSettings) Int(key string) int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	n := 0
	for _, r := range f.v[key] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func (f *fakeSettings) Bool(key string) bool {
	return strings.EqualFold(strings.TrimSpace(f.String(key)), "true")
}

// openTestDB 用真实的 0037 迁移建表。
//
// 不走 AutoMigrate：生产跑的是迁移文件，测试跑的是模型结构，
// 两者一旦不一致（比如漏了个索引或列名写错），测试就会绿而线上炸。
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// DSN 与 internal/store/db.go:63-67 的生产口径一致（busy_timeout + WAL）。
	// 不加 busy_timeout 的话，提交锁并发用例里的多个写者会直接撞上 SQLITE_BUSY，
	// 测出来的就不是锁行为而是 SQLite 的默认争用行为。
	dsn := filepath.Join(t.TempDir(), "mediaupgrade_test.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(on)&_pragma=synchronous(NORMAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	// 与 store.splitStatements 同口径：裸按分号切。
	// 迁移文件的注释里本来就不许有分号，所以这样切是安全的。
	//
	// ⚠️ 0046 是 0037 的后续 ALTER（给 media_upgrade_rules 加 category_scope），
	// 0048 又是 0037 的后续 ALTER（给 media_upgrade_records 加
	// reject_reasons/rule_fingerprint）。必须一起跑。只跑 0037 的话
	// Record 结构体里多出的那两列在库里不存在，症状是第一条涉及记录的
	// 用例就报 "no column named reject_reasons"，而根因在这段脚手架
	// 少跑了一个文件 —— 看起来像是新列写错了。
	migrations := []string{
		"0037_media_upgrade.sql",
		"0046_upgrade_category_scope.sql",
		"0048_wash_rejection_reasons.sql",
	}
	for _, name := range migrations {
		migration, err := os.ReadFile(filepath.Join("..", "store", "migrations", name))
		if err != nil {
			t.Fatalf("读取 %s 迁移失败：%v", name, err)
		}
		for _, stmt := range strings.Split(string(migration), ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if err := db.Exec(stmt).Error; err != nil {
				t.Fatalf("执行 %s 迁移片段失败：%v\n片段：%s", name, err, stmt)
			}
		}
	}
	return db
}

// writeVideo 造一个指定大小的视频文件。
//
// 用 Truncate 而不是真写 size 字节：测试里的 3GB/9GB 只是为了逼出「新版更大」，
// 而 stat 看到的大小在稀疏文件上和真文件一模一样，写满却要好几秒一个用例。
func writeVideo(t *testing.T, path string, size int64) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("建文件失败：%v", err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatalf("设置文件大小失败：%v", err)
	}
	return path
}

// testRuleSet 直接构造一个规则集，跳过 settings 与库的往返。
//
// 扫描/执行链路里真正值得测的是「判定 → 快照 → 提交锁」，
// 规则集从哪来已经有 LoadRuleSet 自己的用例覆盖了。
func testRuleSet(libRoot, candRoot string) *RuleSet {
	rs := &RuleSet{
		Source:         SourceLocal,
		LibraryRoot:    libRoot,
		CandidateRoots: []string{candRoot},
		LoserAction:    LoserActionDelete,
		Enabled:        true,
	}
	rs.WashRules = moviepilot.DefaultWashRules
	return rs
}

// scanRuleID 把规则集落成一条库内规则行，让 LoadRuleSet(ruleID>0) 走通。
func saveRule(t *testing.T, db *gorm.DB, rs *RuleSet) uint {
	t.Helper()
	row := &Rule{
		Name:            "测试规则",
		Source:          rs.Source,
		LibraryRoot:     rs.LibraryRoot,
		CandidateRoots:  rs.CandidateRootsText(),
		MinResolution:   rs.MinResolution,
		MinChannels:     rs.MinChannels,
		RequireSubtitle: rs.RequireSubtitle,
		// 少了这两项，「每部剧记录上限」与「制作组优先级」用例会假绿：
		// 字段没落库，断言却在测一个恒为零的上限。
		MaxRecordsPerSeries: rs.MaxRecordsPerSeries,
		LoserAction:         rs.LoserAction,
		MoveDir:             rs.MoveDir,
		GroupPriority:       JoinRoots(rs.GroupPriority),
		WashRules:           marshalWashRules(rs.WashRules),
		Enabled:             true,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("写入规则行失败：%v", err)
	}
	rs.ID = row.ID
	return row.ID
}

// recordsOf 读某次扫描的全部记录。
func recordsOf(t *testing.T, db *gorm.DB, scanID uint) []Record {
	t.Helper()
	var out []Record
	if err := db.Where("scan_id = ?", scanID).Order("id asc").Find(&out).Error; err != nil {
		t.Fatalf("读取记录失败：%v", err)
	}
	return out
}

// onlyRecord 断言扫描恰好产出一条记录并返回它。
func onlyRecord(t *testing.T, db *gorm.DB, scanID uint) Record {
	t.Helper()
	recs := recordsOf(t, db, scanID)
	if len(recs) != 1 {
		t.Fatalf("期望恰好 1 条判定记录，实际 %d 条", len(recs))
	}
	return recs[0]
}

// countingDeleter 记录每一次真实删除尝试。
//
// 断言「只删了一次」比断言「文件不存在了」重要得多：
// 后者在「没人删，只是文件本来就不存在」时也会通过。
type countingDeleter struct {
	mu       sync.Mutex
	deleted  []string
	failures map[string]bool
	batchErr bool
	calls    int
}

func newCountingDeleter() *countingDeleter {
	return &countingDeleter{failures: map[string]bool{}}
}

func (d *countingDeleter) BatchDelete(paths []string) ([]string, error) {
	d.mu.Lock()
	d.calls++
	batchErr := d.batchErr
	d.mu.Unlock()
	if batchErr {
		return nil, os.ErrPermission
	}
	var failed []string
	for _, p := range paths {
		if err := os.Remove(p); err != nil {
			failed = append(failed, p)
			continue
		}
		d.mu.Lock()
		d.deleted = append(d.deleted, p)
		d.mu.Unlock()
	}
	return failed, nil
}

func (d *countingDeleter) SingleDelete(path string) error {
	d.mu.Lock()
	d.calls++
	fail := d.failures[path]
	d.mu.Unlock()
	if fail {
		return os.ErrPermission
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	d.mu.Lock()
	d.deleted = append(d.deleted, path)
	d.mu.Unlock()
	return nil
}

func (d *countingDeleter) deleteCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *countingDeleter) deletedPaths() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.deleted))
	copy(out, d.deleted)
	return out
}

// asBatcher 把计数删除器包装成提交器要用的批量删除器。
func (d *countingDeleter) asBatcher() *CloudDeleteBatcher {
	return &CloudDeleteBatcher{
		BatchDelete:  d.BatchDelete,
		SingleDelete: d.SingleDelete,
	}
}

// newTestCommitter 造一个提交器：指定删除实现，其余走默认。
func newTestCommitter(db *gorm.DB, deleter *CloudDeleteBatcher) *Committer {
	return &Committer{
		DB:       db,
		Settings: newFakeSettings(nil),
		Deleter:  deleter,
		now:      time.Now,
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
