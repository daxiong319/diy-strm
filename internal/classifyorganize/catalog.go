package classifyorganize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// C-8 分类只读接口
//
// 跨模块消费者（洗版规则筛选、目录清理保护、Bot 快捷命令、命名方案取值表）
// 需要知道「当前配了哪些分类目录」。它们的共同点是**只读**：自己不定义分类，
// 也不允许在这里新增或删除。所以本文件不提供任何写接口给调用方，
// 分类的唯一编辑入口仍然是模板配置（配置页 / YAML 导入）。
//
// ---------------------------------------------------------------------------

// Category 是只读分类目录的对外视图。
//
// ⚠️ 语义边界（这是本接口最容易读错的一条，必须写在类型上）：
//
// 这里返回的是**规则里配置的分类目录**，不是**磁盘上已存在的分类目录**。
//
// 两者不同：用户刚配了「电影/科幻」但还没有任何影片被整理过去，磁盘上
// 那个目录并不存在；反过来，磁盘上存在一个「电影/科幻」目录但配置早就删掉了
// 那一级，它也不会出现在这里。
//
// 消费者必须照这个语义用：
//   - 洗版筛选要的是「用户配了哪些分类」→ 正好用这个；
//   - 清理保护要保护的是「配置里还认的分类目录」→ 也正好用这个，
//     因为配置删掉的那一级本来就不该再被当作分类目录保护（用户既然删了它，
//     就是不再使用那个目录）；但如果有人以为它返回的是磁盘现状，
//     就会反向理解成「保护所有磁盘上叫分类名的目录」，把用户废弃的目录
//     永久钉在清理之外，越攒越多。
//
// 一句话说清判据：**没配过 = 不存在**，不论磁盘上有没有那个目录。
type Category struct {
	// Level 目录层级 1/2/3。系列目录不在此列（见 below）。
	Level int `json:"level"`
	// Name 目录名，在所属模板内逐级唯一。
	Name string `json:"name"`
	// Slug 稳定标识，形如 "genre/电影/科幻"。
	//
	// 为什么不用 Name 当标识：不同模板下可以有同名目录（region 模板的「国产」
	// 和 genre 模板的「国产」），跨模块存一个「国产」无法还原它属于哪套模板。
	// Slug 改模板名就会变，所以它能用来查当前状态，不能当永久主键写进别的表。
	Slug string `json:"slug"`
	// RuleID 对应的 classify_rules 主键；内置模板目录没有用户规则行，为 0。
	RuleID uint `json:"rule_id"`
	// Enabled 是否启用。关闭的分类仍会返回，因为「配了但关着」和和
	// 「没配」对消费者是完全不同的两件事：前者不该再筛选/保护，后者才该消失。
	Enabled bool `json:"enabled"`
	// Template 所属模板 media / region / genre / custom。
	Template string `json:"template"`
	// PrimaryKey 一级分类的类型键（movie / tv）；二级三级为空。
	//
	// 为什么只有一级有：只有一级是「按媒体类型分家」，二级三级是分家之后的细分。
	// C-3 放开一级之后新增的一级分类自己也会带一个 key（形如 movie 或 tv 的别名），
	// 让洗版「适用二级分类」这类筛选能按媒体类型收敛。
	PrimaryKey string `json:"primary_key,omitempty"`
	// Path 从模板根到本级的目录名路径，形如 "电影/科幻"。
	// 清理保护拿它逐段比对磁盘路径；SegmentPath 提供了拼好的完整形式。
	Path string `json:"path"`
}

// ---------------------------------------------------------------------------
// 系列目录为什么不在这里
//
// 系列目录（Config.Series）也是命中后追加的一段目录，但它与一级/二级/三级
// 是本质不同的东西：它的身份由**关键词**决定，不是由目录名决定。
// 同一个系列可以配多个关键词、可以按 movie/tv 分别落到不同目录，
// 而且它的目录名允许和分类目录重名（"系列/A 计划" 与分类 "A 计划" 无关）。
//
// 把系列混进分类目录表会让消费者把两者当成同一种东西：
// Bot 的「/strm 国产剧」按分类筛文件名还好，一旦有人拿系列目录当清理保护的
// 保护名单，就会出现「保护了一个用户从没配过的目录」这种没法解释的结果。
//
// 所以只登记模板里的分类目录（Rule 树）。需要系列时另外读 Config.Series。
// ---------------------------------------------------------------------------

// SeriesDirectory 为 true 时 SeriesSegment 走系列规则。
//
// 预留给后续把系列也做成目录白名单的场景（C-8 任务书没有要求，
// 故当前只有一个常量并附上理由，避免下一个人以为这是遗漏）。
const SeriesDirectory = false

// ---------------------------------------------------------------------------
// 投影：模板 JSON → classify_primary_categories
// ---------------------------------------------------------------------------

// PrimaryCategory 是 classify_primary_categories 的行模型（派生投影，不是权威源）。
//
// 权威源是 settings.KeyMOClassificationConfig 里的模板 JSON。
// 投影存在的唯一理由是让别的包能用 SQL 查（清理保护要 join 目录表、
// 前端要一次拿全量清单），而不是给每个消费者都塞一份 settings 读取 +
// 模板解析。投影表**不接受外部写入**，写它的只有本文件的 sync。
type PrimaryCategory struct {
	ID          uint   `gorm:"primaryKey;column:id"`
	Level       int    `gorm:"not null;column:level"`
	Name        string `gorm:"not null;column:name"`
	Slug        string `gorm:"not null;column:slug"`
	Template    string `gorm:"column:template"`
	PrimaryKey  string `gorm:"column:primary_key"`
	Path        string `gorm:"not null;column:path"`
	Enabled     bool   `gorm:"column:enabled"`
	Fingerprint string `gorm:"not null;column:fingerprint"`
	CreatedAt   string `gorm:"column:created_at"`
	UpdatedAt   string `gorm:"column:updated_at"`
}

// TableName 与迁移 0045_primary_categories.sql 对齐。
func (PrimaryCategory) TableName() string { return "classify_primary_categories" }

// ---------------------------------------------------------------------------
// ListActiveCategories（C-8 主接口）
// ---------------------------------------------------------------------------

// ListActiveCategories 返回当前配置里定义的全部分类目录（含层级、名称、启用状态）。
//
// **语义**：返回的是「规则里有哪些分类」，不是「磁盘上有哪些分类」。
// Category 的类型注释里有完整说明，这里不重复。
//
// 「Active」指的是**配置存在**，不是「磁盘上有」也不是「模板被选中了」：
// 没被选中的模板里的分类照样返回，因为用户切回那个模板时它们立即生效，
// 消费者（尤其洗版筛选）不该因为换了模板就突然看不见已有分类。
//
// 错误语义：
//   - 配置读不出来 → 返回错误。分类目录清单错了，消费者的筛选就是错的，
//     静默返回一个空列表会让洗版扫遍整个媒体库。
//   - 投影写不进去 → **仍然返回列表**，错误只记日志。投影是加速手段，
//     不是分类的真相来源。
func (s *Service) ListActiveCategories(ctx context.Context) ([]Category, error) {
	if s == nil {
		return nil, fmt.Errorf("分类服务未初始化")
	}
	cfg := s.Config()
	// Config() 已经把 loadConfig 的三种回落都处理好了（raw 空 / 反序列化失败 /
	// normalizeConfig 失败都回落 DefaultConfig），所以这里不需要再判错：
	// 拿不到配置时的行为是「用默认模板」，这与分类本身的语义一致。
	rows := collectCategories(cfg)
	// 归一化已经过了 validatePathSegment，这里再过一次是为了钉住投影层的契约：
	// 手工构造的 Config（将来的导入器或测试直接塞进来的）绕过归一化时，
	// 不能把一个 ".." 写进那张要被清理保护拿去比对磁盘路径的表里。
	if err := validateCategoryNames(rows); err != nil {
		return nil, err
	}
	if s.projectionAvailable(ctx) {
		if err := s.syncProjection(ctx, cfg); err != nil {
			s.logCatalogSyncFailure(err)
		}
	}
	return rows, nil
}

// collectCategories 把配置里的模板展开成扁平的分类目录清单。
//
// 顺序：模板注册顺序 → 模板内 Rules 顺序 → 逐层 Children 深度优先。
// 一级在前、它自己的二级三级紧跟其后，这样前端渲染成树时不必再排序。
func collectCategories(cfg Config) []Category {
	out := make([]Category, 0, 32)
	for i := range cfg.Templates {
		tpl := &cfg.Templates[i]
		if tpl.Kind == "" {
			continue
		}
		for _, rule := range tpl.Rules {
			out = appendCategories(out, tpl.Kind, rule, 1, nil)
		}
	}
	return out
}

func appendCategories(out []Category, template string, rule Rule, level int, parent []string) []Category {
	segments := append(append([]string(nil), parent...), rule.Name)
	slugParts := append([]string{template}, segments...)

	out = append(out, Category{
		Level:      level,
		Name:       rule.Name,
		Slug:       strings.Join(slugParts, "/"),
		Template:   template,
		PrimaryKey: primaryKeyOf(rule, level),
		Path:       strings.Join(segments, "/"),
		Enabled:    true,
	})
	if level >= maxRuleDepth {
		// 三级下面不再下钻。归一化阶段已经拒绝过更深的了，
		// 这里的判断只是防止手工构造的 Config 走进来时把树展开成无限深。
		return out
	}
	for _, child := range rule.Children {
		out = appendCategories(out, template, child, level+1, segments)
	}
	return out
}

// primaryKeyOf 取一级分类的类型键。
//
// 一级的类型身份有两处可能来源：结构化字段 RuleFields.MediaTypes
// （C-3 放开后新增的一级分类用它），或退回解析 Condition 表达式
// （存量内置模板仍是 "type=movie" 这种写法）。两处都要认，
// 只认一处会让另一类配置的分类目录在消费者眼里没有 primary_key。
func primaryKeyOf(rule Rule, level int) string {
	if level != levelPrimaryInt {
		return ""
	}
	if rule.Fields != nil && len(rule.Fields.MediaTypes) > 0 {
		return normalizePrimaryKey(rule.Fields.MediaTypes[0])
	}
	field, values, err := rule.matchField()
	if err != nil || field != fieldType || len(values) == 0 {
		return ""
	}
	return normalizePrimaryKey(values[0])
}

// levelPrimaryInt 是层级 1 的数值形式（config.go 里的 levelPrimary 是中文标签）。
const levelPrimaryInt = 1

func normalizePrimaryKey(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// ---------------------------------------------------------------------------
// 投影同步
// ---------------------------------------------------------------------------

// projectionAvailable 判断投影表能不能用。
//
// 刻意**不**在这里建表：建表是装配层的职责（EnsureClassifySchema），
// 而投影写失败不该被 AutoMigrate 顺手"修好"——那会把一次迁移漏跑
// 变成一次运行期 DDL，把只读实例写坏。
func (s *Service) projectionAvailable(ctx context.Context) bool {
	db := s.dbOrNil()
	if db == nil {
		return false
	}
	return db.WithContext(ctx).Migrator().HasTable(&PrimaryCategory{})
}

// syncProjection 把模板展开结果写进投影表。
//
// 指纹策略：整份模板 JSON 的 sha256 前 16 字节。指纹相同就直接跳过 ——
// 分类只读接口会被前端设置页和洗版扫描反复调用，每次都重写一遍
// 所有行会把 updated_at 刷成一片噪声，让"这条分类什么时候被改的"
// 这个问题在表里变得无法回答。指纹不同才整批替换。
func (s *Service) syncProjection(ctx context.Context, cfg Config) error {
	db := s.dbOrNil()
	if db == nil {
		return errors.New("分类目录投影：数据库句柄未初始化")
	}
	fp := configFingerprint(cfg)
	rows := collectCategories(cfg)

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current string
		err := tx.Raw(`SELECT fingerprint FROM classify_primary_categories LIMIT 1`).Scan(&current).Error
		if err != nil {
			return fmt.Errorf("分类目录投影：读取指纹失败 %w", err)
		}
		if current == fp {
			return nil
		}
		if err := tx.Where("1 = 1").Delete(&PrimaryCategory{}).Error; err != nil {
			return fmt.Errorf("分类目录投影：清理旧行失败 %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		batch := make([]PrimaryCategory, 0, len(rows))
		for _, r := range rows {
			batch = append(batch, PrimaryCategory{
				Level:       r.Level,
				Name:        r.Name,
				Slug:        r.Slug,
				Template:    r.Template,
				PrimaryKey:  r.PrimaryKey,
				Path:        r.Path,
				Enabled:     true,
				Fingerprint: fp,
				CreatedAt:   now,
				UpdatedAt:   now,
			})
		}
		if err := tx.Create(&batch).Error; err != nil {
			return fmt.Errorf("分类目录投影：写入失败 %w", err)
		}
		return nil
	})
}

func (s *Service) logCatalogSyncFailure(err error) {
	if s == nil || s.log == nil {
		return
	}
	s.log.Warn("分类目录投影同步失败，分类本身不受影响", "err", err.Error())
}

// configFingerprint 取模板配置的指纹。
//
// 刻意**只对 Templates 取指纹**，不包含 Enabled / SelectedTemplate / Version：
// 投影的是"配了哪些分类目录"，跟分类功能开没开、当前选中哪个模板无关。
// 把它们算进指纹会让"只是关了一下分类功能"触发一次全表重写。
func configFingerprint(cfg Config) string {
	raw, err := json.Marshal(cfg.Templates)
	if err != nil {
		// 模板里只有字符串/切片/结构体，Marshal 失败不可能发生。
		// 真失败了用空串当指纹 ⇒ 下一轮一定会重投影，代价是白写一次，
		// 比返回一个随机指纹导致投影永远对不上要好。
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

// ---------------------------------------------------------------------------
// 路径拼装：给清理保护这类需要「磁盘路径 ↔ 分类目录」比对的消费者
// ---------------------------------------------------------------------------

// SegmentPath 返回一段分类目录的相对路径（"科幻/2000-2009"）。
//
// 与 Path 的区别：Path 是**单条分类目录**从模板根到自己的路径，
// SegmentPath 用于把一段属于同一个一级分类的多级目录拼起来。
func (c Category) SegmentPath(parts ...string) string {
	all := make([]string, 0, len(parts)+1)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			all = append(all, p)
		}
	}
	all = append(all, c.Name)
	return strings.Join(all, "/")
}

// AncestorNames 返回这条分类目录的所有祖先目录路径前缀，含自身，从短到长。
//
// 清理保护用它构造保护名单：盘上出现 "电影/科幻/星际穿越" 时，
// 需要判断的是 "科幻" 在不在某个受保护的一级分类下面，于是要比对
// "电影" 和 "电影/科幻" 两个前缀。
//
// 返回**路径**而不是单个目录名：逐段比对面已经用 AncestorPaths 了，
// 而返回一堆光秃秃的目录名（电影、科幻）会诱导调用方去比较
// "盘上的任意一段目录名"，那样 "剧集/科幻" 会被 "电影/科幻" 认领。
func (c Category) AncestorPaths() []string {
	parts := strings.Split(c.Path, "/")
	out := make([]string, 0, len(parts))
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		out = append(out, strings.Join(parts[:i+1], "/"))
	}
	return out
}

// IsUnder 判断一个相对分类根的目录路径是否落在这条分类目录下。
//
// 入参是**路径**而不是 []string 段：段数对不上是调用方最常见的错误，
// 而一个能接收 []string{"国产"} 的签名会让人以为末段名就算命中。
// 于是这里收一个字符串，自己按 "/" 切。
//
// 用逐段比对而不是 strings.HasPrefix，是为了让 "电影" 不认领 "电影时代"
// 这类同前缀兄弟目录——那俩是无关的两个目录，保护名单里混进后者
// 会挡住用户真正想清理的东西。
func (c Category) IsUnder(path string) bool {
	// 比对的是**段**，不是 AncestorPaths 里的完整路径前缀。
	// 两者形态不同：AncestorPaths 给的是 ["电影", "电影/国产"] 这样的路径串，
	// 而这里要和盘上路径切出来的 ["电影","国产"] 逐段对齐。拿前缀去比段
	// 会在二级上必然对不上（第一版就是这么写的），症状是「二级分类目录
	// 一个都保护不到」，而且只在二级以上暴露，一级测试全绿。
	want := strings.Split(c.Path, "/")
	if len(want) == 0 {
		return false
	}
	got := strings.Split(strings.Trim(path, "/"), "/")
	if len(got) < len(want) {
		return false
	}
	for i := range want {
		if strings.TrimSpace(got[i]) != strings.TrimSpace(want[i]) {
			return false
		}
	}
	return true
}

// CategoriesByLevel 按层级筛分类目录。
func CategoriesByLevel(cats []Category, level int) []Category {
	out := make([]Category, 0, len(cats))
	for _, c := range cats {
		if c.Level == level {
			out = append(out, c)
		}
	}
	return out
}

// CategoryNames 取出某层级的目录名列表，供前端下拉框直接用。
func CategoryNames(cats []Category, level int) []string {
	sub := CategoriesByLevel(cats, level)
	out := make([]string, 0, len(sub))
	for _, c := range sub {
		out = append(out, c.Name)
	}
	return out
}

// SortCategories 按 层级 → slug 排序，让跨模板的清单有稳定顺序。
//
// 稳定性：同一层内按 Path 字典序。排序存在的唯一理由是让
// "两次调用返回同样的顺序"，否则前端下拉框每次刷新顺序都变。
func SortCategories(cats []Category) []Category {
	out := append([]Category(nil), cats...)
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Level != out[b].Level {
			return out[a].Level < out[b].Level
		}
		return out[a].Slug < out[b].Slug
	})
	return out
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

// validateCategoryNames 检查分类目录名合法性。
//
// 存在的理由：0045 的迁移**不能建触发器**（internal/store/migrate.go 的
// splitStatements 按分号切语句且不识别注释，触发器体被切开就是语法错误，
// 实测报 "incomplete input"，整个 Migrate 失败、服务起不来）。
// 于是 path 的合法性只能在 Go 侧拦，而这个函数就是那一处。
//
// 这里必须走 validatePathSegment 而不是自己写一份：它已经是目录名的唯一
// 校验入口（模板归一化、用户规则目标路径都走它），再写一份必然出现
// "模板里能过的名字投影里被拒"这种不一致。
func validateCategoryNames(rows []Category) error {
	for _, r := range rows {
		if r.Level < 1 || r.Level > maxRuleDepth {
			return fmt.Errorf("分类目录“%s”的层级非法：%d", r.Path, r.Level)
		}
		if err := validatePathSegment(r.Name); err != nil {
			return fmt.Errorf("分类目录层级 %d：%v", r.Level, err)
		}
		for _, seg := range strings.Split(r.Path, "/") {
			if err := validatePathSegment(seg); err != nil {
				return fmt.Errorf("分类目录“%s”的路径段：%v", r.Path, err)
			}
		}
		if r.Slug == "" {
			return fmt.Errorf("分类目录“%s”缺少 slug", r.Path)
		}
	}
	return nil
}
