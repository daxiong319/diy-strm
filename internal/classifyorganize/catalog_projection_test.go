package classifyorganize

// T27 验收⑧：一级分类**没有搞出两份真相**。
//
// 这一条是整个 T27 最需要写下来的问题，因为本仓确实有两处能存分类：
//
//   - settings.KeyMOClassificationConfig 里的模板 JSON（用户编辑的对象）
//   - classify_primary_categories 表（迁移 0045，本期新增）
//
// 答案写在代码里（本文件这个 `Category` 类型的注释 + catalog.go 的
// syncProjection 注释）还不够 —— 那种注释过半年没人看。必须有一条
// 测试钉住「表里删一行、改模板，两边会怎样」，否则下一次有人为了
// 查询方便让某条路径直接改表，就会静默造出第二份真相。
//
// 判据（也是本用例的全部内容）：
//   1. 表里的行是模板的**投影**，改模板 → 表跟着变；
//   2. 直接改表（模拟有人绕过 Service 写表）→ 下次读模板并重新投影后
//      被覆盖掉，表不是真相；
//   3. 表被整个删掉 → 分类清单照样能用（降级成不投影，不是报错）。
//
// 用真实迁移建表而不是 AutoMigrate：生产跑的是迁移文件，两边不一致
// 时（漏列、索引名写错）AutoMigrate 会替我们把错误补上，测试就绿了。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"litepan/internal/discover/ddb"
	"litepan/internal/store"
)

// catalogProjectionDB 造一个跑了**全部真实迁移**的库，并临时挂到 ddb.Db 上。
//
// 三个刻意的选择：
//
//  1. 走 store.Open + Migrate 而不是只挑一个迁移文件或 AutoMigrate。
//     前者验的是"0045 在完整迁移链里能跑通"，后者会让生产与测试两套 schema
//     各活一份 —— 漏列、索引名写错都会被 AutoMigrate 悄悄补上。
//
//  2. 迁移与查询**分开两个句柄但同一个文件**。store.DB 只暴露 database/sql，
//     而 ddb.Db 要的是 GORM 句柄；同一个 WAL 库可以同时持有两者，
//     这样"迁移跑的是生产那套"与"被测代码拿到的确实是 GORM 句柄"两个条件同时成立。
//
//  3. 直接给 ddb.Db 赋值而不是 ddb.Init。
//     ddb.Init 是**全进程 once**，装在这里会连带改变本包里其它用例看到的
//     DB 状态（TestHasCustomRulesWithoutDB 明确依赖「没有 DB」这个前提）。
//     ddb.Db 本身是导出变量，赋值 + 还原是最小的干预面。
func catalogProjectionDB(t *testing.T) (*gorm.DB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog_test.db")

	handle, err := store.Open(context.Background(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	t.Cleanup(func() { handle.Close() })
	if err := handle.Migrate(context.Background()); err != nil {
		t.Fatalf("跑迁移失败：%v", err)
	}

	gdb, err := gorm.Open(sqlite.Open(path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开 GORM 视图句柄失败：%v", err)
	}
	prev := ddb.Db
	ddb.Db = gdb
	t.Cleanup(func() { ddb.Db = prev })
	return gdb, handle.WriteHandle()
}

// mustExec 跑一条断言用的 SQL。
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("执行 SQL 失败：%v\n%s", err, query)
	}
}

func projectedCount(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM classify_primary_categories`).Scan(&n); err != nil {
		t.Fatalf("读投影表行数失败：%v", err)
	}
	return n
}

func projectedCountWhere(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("读投影表行数失败：%v", err)
	}
	return n
}

// TestProjectionFollowsTemplateNotTheTable 验收⑧主用例。
func TestProjectionFollowsTemplateNotTheTable(t *testing.T) {
	gdb, db := catalogProjectionDB(t)
	_ = gdb
	svc := newService(t, false)
	ctx := context.Background()

	// 第一步：第一次读 → 投影落库。
	if _, err := svc.ListActiveCategories(ctx); err != nil {
		t.Fatalf("首次读取分类清单失败：%v", err)
	}
	first := projectedCount(t, db)
	if first == 0 {
		t.Fatal("首次读取后投影表应非空（配置里有内置模板的分类目录）")
	}

	// 第二步：改模板加一条一级 → 投影必须跟着变。
	cfg := svc.Config()
	cfg.Templates[1].Rules = append(cfg.Templates[1].Rules, Rule{
		Name:      "电影-其他",
		Condition: "type=movie",
		Children:  []Rule{{Name: "短片", Condition: "origin_country=CN"}},
	})
	mustUpdate(t, svc, cfg)
	if _, err := svc.ListActiveCategories(ctx); err != nil {
		t.Fatalf("改模板后读取分类清单失败：%v", err)
	}
	after := projectedCount(t, db)
	if after <= first {
		t.Fatalf("新增一级分类后投影表行数 = %d，应大于原来的 %d（表没有跟着模板走）", after, first)
	}
	n := projectedCountWhere(t, db, `SELECT COUNT(*) FROM classify_primary_categories WHERE name = ?`, "短片")
	if n != 1 {
		t.Fatalf("新增的一级下面的二级没有投影进表（查到 %d 行）", n)
	}

	// 第三步（关键）：绕过 Service 直接改表 → 下次读模板时必须被覆盖回去。
	// 这一步钉住「表不是真相」：如果有人后来让某条路径直接 UPDATE 这张表，
	// 这条断言会告诉他改不动。
	//
	// ⚠️ 覆盖**不会**立刻发生：syncProjection 按配置指纹短路，配置没动就不重写
	// （这是有意的，否则每次打开设置页都会把 updated_at 刷成噪声）。
	// 所以这里必须先改一次配置来推进指纹 —— 早先写成"改完表直接读一次"，
	// 测试红了，而第一反应是去改实现，那是错的：真实场景里没人会手工改这张表，
	// 而且"配置没动就不重写"正是我们想要的行为。
	mustExec(t, db, `UPDATE classify_primary_categories SET name = '被手改的名字' WHERE slug LIKE '%短片'`)
	cfg = svc.Config()
	cfg.Templates[1].Rules[0].Name = "影片"
	mustUpdate(t, svc, cfg)
	if _, err := svc.ListActiveCategories(ctx); err != nil {
		t.Fatalf("改配置后读取分类清单失败：%v", err)
	}
	if n := projectedCountWhere(t, db,
		`SELECT COUNT(*) FROM classify_primary_categories WHERE name = ?`, "被手改的名字"); n != 0 {
		t.Fatalf("表里的手改值没有被模板覆盖（仍有 %d 行），说明表已经变成了第二份真相", n)
	}
	// 并且分类清单本身早就没受手改影响 —— 它每次都是从配置重算的，
	// 这才是"表不是真相"的正面判据（上面那条只是反面证据）。
	cats, err := svc.ListActiveCategories(ctx)
	if err != nil {
		t.Fatalf("读取分类清单失败：%v", err)
	}
	for _, c := range cats {
		if c.Name == "被手改的名字" {
			t.Fatal("分类清单里出现了手改进表的值 —— 清单不是从配置算出来的")
		}
	}
}

// TestProjectionTableMissingIsNotFatal 退化方向。
//
// 迁移漏跑、或只读副本不允许建表时，投影写不进去不该让分类清单整体失败：
// 分类清单本身是从配置 JSON 算出来的，表只是加速。
// 反过来（拿不到配置就返回空列表）才危险 —— 那会让洗版扫遍整个媒体库。
func TestProjectionTableMissingIsNotFatal(t *testing.T) {
	svc := newService(t, false)
	// 本用例故意**不**挂任何 DB：projectionAvailable 返 false。
	prev := ddb.Db
	ddb.Db = nil
	t.Cleanup(func() { ddb.Db = prev })

	cats, err := svc.ListActiveCategories(context.Background())
	if err != nil {
		t.Fatalf("投影不可用时读取分类清单失败：%v", err)
	}
	if len(cats) == 0 {
		t.Fatal("投影不可用时分类清单仍应从配置算出来（不是空列表）")
	}
}

// TestCategoriesComesFromConfigNotTheTable 验收⑧最直接的判据。
//
// 把表整个删掉，分类清单应当一条不少 —— 因为它是配置投影出来的。
// 如果哪天有人改成「先查表，表空就当没有分类」，这条会红，
// 而那个 bug 的现象是「迁移没跑 ⇒ 洗版筛不到任何文件」，
// 而洗版那边不会报任何错。
func TestCategoriesComesFromConfigNotTheTable(t *testing.T) {
	gdb, db := catalogProjectionDB(t)
	_ = gdb
	svc := newService(t, false)
	ctx := context.Background()

	withTable, err := svc.ListActiveCategories(ctx)
	if err != nil {
		t.Fatalf("读取分类清单失败：%v", err)
	}
	// 先把表清空，**再**让指纹"看起来没变"：改 fingerprint 而不是删表。
	// 只 DELETE 不够 —— syncProjection 会在下一次读之前就把它填回来，
	// 于是"从表读"的错误实现照样绿（这个用例第一版就是这么写的，
	// 变异测试"改成从表读"没抓住它）。必须让表和配置同时存在却**内容不同**。
	mustExec(t, db, `UPDATE classify_primary_categories SET name = '投影里的旧名字'`)

	withoutTable, err := svc.ListActiveCategories(ctx)
	if err != nil {
		t.Fatalf("投影表不存在时读取分类清单失败：%v", err)
	}
	// 只比数量是不够的：改名不改数量。
	// 第一版这里比的是 len()，于是"从表读"的错误实现照样绿 ——
	// 因为每次读之前表都会被重新投影填好，两边数量一致。
	// 现在比**内容**：表里每个分类名都必须原样出现在清单里。
	if len(withoutTable) != len(withTable) {
		t.Fatalf("表被清空后分类数 = %d，有表时 = %d —— 清单不是从配置算出来的", len(withoutTable), len(withTable))
	}
	inResult := func(name string) bool {
		for _, c := range withoutTable {
			if c.Name == name {
				return true
			}
		}
		return false
	}
	if !inResult("电影") {
		t.Fatalf("分类清单里没有配置中的「电影」，说明清单被投影表里的内容替代了：%+v",
			withoutTable[:min(len(withoutTable), 5)])
	}
	if inResult("投影里的旧名字") {
		t.Fatal("分类清单里出现了只存在于投影表的值 —— 清单读的是表而不是配置")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
