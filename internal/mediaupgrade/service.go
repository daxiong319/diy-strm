// 洗版的服务编排层：把 Scanner（判定）与 Committer（提交）合成一个可注入的对象，
// 再补上规则集的增删改查，供 API 层直接调用。
//
// 这一层刻意不做事：所有安全闸门都在 Scanner/Committer 里，
// Service 只是「谁在什么时候调谁」的门面，保证 API 不会绕过闸门直接删文件。

package mediaupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"litepan/internal/moviepilot"
	"litepan/internal/settings"
)

// Service 洗版服务。
type Service struct {
	db        *gorm.DB
	settings  Settings
	scanner   *Scanner
	committer *Committer

	// categorySource 提供分类范围。nil = 不按分类筛选。
	//
	// 做成注入而不是构造参数：分类清单来自分类引擎，而分类引擎的规则是
	// 运行期可改的。如果在 New 的那一刻把清单抓成值存起来，用户改完分类
	// 规则再点扫描就会按旧清单筛 —— 而症状是"刚加的分类扫不出来"，
	// 要翻到几百行外的分类配置才看得出原因。
	categorySource func(context.Context) CategoryScope
}

// New 构造洗版服务。
func New(db *gorm.DB, svc Settings) *Service {
	return &Service{
		db:        db,
		settings:  svc,
		scanner:   NewScanner(db, svc),
		committer: NewCommitter(db, svc),
	}
}

// SetCategorySource 注入分类范围来源（装配层接分类引擎）。
//
// 传 nil 表示不按分类筛选，这是零值语义，不需要额外调用来取消。
func (s *Service) SetCategorySource(fn func(context.Context) CategoryScope) {
	if s == nil {
		return
	}
	s.categorySource = fn
}

// Scanner 暴露扫描器，供需要绕过 Service 的内部调用方使用。
func (s *Service) Scanner() *Scanner { return s.scanner }

// Committer 暴露提交器。
func (s *Service) Committer() *Committer { return s.committer }

// Scan 触发一次扫描。判定与提交分离：这里只产出记录，不动任何文件。
func (s *Service) Scan(ctx context.Context, ruleID uint) (*Scan, error) {
	if s == nil || s.scanner == nil {
		return nil, errors.New("洗版服务未初始化")
	}
	opts := ScanOptions{RuleID: ruleID}
	if s.categorySource != nil {
		opts.Category = s.categorySource(ctx)
	}
	return s.scanner.Scan(ctx, opts)
}

// Execute 提交一次扫描。内部先复核快照再动文件。
func (s *Service) Execute(ctx context.Context, scanID uint) (*ExecuteResult, error) {
	if s == nil || s.committer == nil {
		return nil, errors.New("洗版服务未初始化")
	}
	return s.committer.ExecuteScan(ctx, scanID)
}

// GetRecord 读单条判定记录。
func (s *Service) GetRecord(ctx context.Context, id uint) (*Record, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("洗版服务未初始化")
	}
	var rec Record
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&rec).Error; err != nil {
		return nil, fmt.Errorf("读取洗版记录 %d 失败: %w", id, err)
	}
	return &rec, nil
}

// ---- 规则集 CRUD ----

// ListRules 列出全部规则。
func (s *Service) ListRules(ctx context.Context) ([]Rule, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("洗版服务未初始化")
	}
	var rows []Rule
	if err := s.db.WithContext(ctx).Order("id asc").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取洗版规则失败: %w", err)
	}
	return rows, nil
}

// GetRule 读单条规则。
func (s *Service) GetRule(ctx context.Context, id uint) (*Rule, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("洗版服务未初始化")
	}
	var row Rule
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, fmt.Errorf("读取洗版规则 %d 失败: %w", id, err)
	}
	return &row, nil
}

// SaveRule 新建或更新一条规则。id==0 新建，否则按 id 更新。
//
// 更新不覆盖 Builtin 与 CreatedAt —— builtin 是给后续「全局回落到内置规则」留的锚点，
// 不该被 UI 随手保存改掉。
func (s *Service) SaveRule(ctx context.Context, row *Rule) (*Rule, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("洗版服务未初始化")
	}
	if row == nil {
		return nil, errors.New("规则为空")
	}
	rs := RuleFromRow(row)
	if err := rs.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(row.Name) == "" {
		row.Name = fmt.Sprintf("规则 #%d", row.ID)
	}
	if row.ID == 0 {
		if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
			return nil, fmt.Errorf("新建洗版规则失败: %w", err)
		}
		return row, nil
	}
	var prev Rule
	if err := s.db.WithContext(ctx).Where("id = ?", row.ID).First(&prev).Error; err != nil {
		return nil, fmt.Errorf("读取原规则 %d 失败: %w", row.ID, err)
	}
	row.Builtin = prev.Builtin
	row.CreatedAt = prev.CreatedAt
	if err := s.db.WithContext(ctx).Model(&Rule{}).Where("id = ?", row.ID).
		Select("Name", "Source", "LibraryRoot", "CandidateRoots", "MinResolution",
			"MinChannels", "RequireSubtitle", "MaxRecordsPerSeries", "LoserAction",
			"MoveDir", "GroupPriority", "WashRules", "CategoryScope", "Enabled").
		Updates(ruleUpdates(row)).Error; err != nil {
		return nil, fmt.Errorf("更新洗版规则 %d 失败: %w", row.ID, err)
	}
	return s.GetRule(ctx, row.ID)
}

// ruleUpdates 把规则行摊成 update map。
func ruleUpdates(row *Rule) map[string]any {
	return map[string]any{
		"name":                   row.Name,
		"source":                 row.Source,
		"library_root":           row.LibraryRoot,
		"candidate_roots":        row.CandidateRoots,
		"min_resolution":         row.MinResolution,
		"min_channels":           row.MinChannels,
		"require_subtitle":       row.RequireSubtitle,
		"max_records_per_series": row.MaxRecordsPerSeries,
		"loser_action":           row.LoserAction,
		"move_dir":               row.MoveDir,
		"group_priority":         row.GroupPriority,
		"wash_rules":             row.WashRules,
		// 归一后写入：空列表落成空串，于是「取消适用分类」也是一次真更新
		// 而不是"没改"。少了这一行的话用户在界面清空筛选框再保存，
		// 数据库里还留着旧分类，而 Select 的白名单也会把它排除在更新之外。
		"category_scope": EncodeRuleCategoryScope(ParseRuleCategoryScope(row.CategoryScope)),
		"enabled":        row.Enabled,
	}
}

// DeleteRule 删除一条规则。
func (s *Service) DeleteRule(ctx context.Context, id uint) error {
	if s == nil || s.db == nil {
		return errors.New("洗版服务未初始化")
	}
	var prev Rule
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&prev).Error; err != nil {
		return fmt.Errorf("读取洗版规则 %d 失败: %w", id, err)
	}
	if err := s.db.WithContext(ctx).Where("id = ?", id).Delete(&Rule{}).Error; err != nil {
		return fmt.Errorf("删除洗版规则 %d 失败: %w", id, err)
	}
	return nil
}

// ---- 全局设置视图 ----

// GlobalRule 把当前全局设置装配成一条「伪规则行」，供 UI 用同一套表单编辑。
// 这样用户既能在全局设置里配，也能在规则页里建具名规则，字段口径完全一致。
//
// 全局设置的 ID 恒为 0：0 是「回落到全局设置」的哨兵值（见 LoadRuleSet），
// 真给它一个 ID 反而会让 UI 编辑它时去改那条不存在的库内规则行。
func (s *Service) GlobalRule() (*Rule, error) {
	if s == nil {
		return nil, errors.New("洗版服务未初始化")
	}
	row := &Rule{
		Name:           "默认规则",
		Source:         SourceLocal,
		LoserAction:    LoserActionKeep,
		Builtin:        true,
		WashRules:      marshalWashRules(moviepilot.DefaultWashRules),
		CandidateRoots: "",
		MoveDir:        "",
	}
	if s.settings == nil {
		return row, nil
	}
	row.Enabled = s.settings.Bool(settings.KeyMOMediaUpgradeEnabled)
	row.Source = s.settings.String(settings.KeyMOMediaUpgradeSource)
	row.LibraryRoot = s.settings.String(settings.KeyMOMediaUpgradeLibraryRoot)
	row.CandidateRoots = s.settings.String(settings.KeyMOMediaUpgradeCandidateRoots)
	row.MinResolution = s.settings.Int(settings.KeyMOMediaUpgradeMinResolution)
	row.MinChannels = s.settings.Int(settings.KeyMOMediaUpgradeMinChannels)
	row.RequireSubtitle = s.settings.Bool(settings.KeyMOMediaUpgradeRequireSubtitle)
	row.MaxRecordsPerSeries = s.settings.Int(settings.KeyMOMediaUpgradeMaxRecordsPerSeries)
	row.LoserAction = s.settings.String(settings.KeyMOMediaUpgradeLoserAction)
	row.MoveDir = s.settings.String(settings.KeyMOMediaUpgradeMoveDir)
	row.GroupPriority = s.settings.String(settings.KeyMOMediaUpgradeGroupPriority)
	// 全局设置那一套（KeyMOMediaUpgrade*）里刻意**没有**分类范围这一项：
	// 分类是分类模板的事，再往设置里加一列就出现第二个权威源，
	// 而两个都能改同一件事时，用户改哪个生效取决于代码里谁先被读到。
	// 于是全局视图的 CategoryScope 恒为空 = 不按分类筛选。
	return row, nil
}

// marshalWashRules 把默认比较规则序列化成 UI 可编辑的 JSON。
//
// 反序列化侧的 moviepilot.ParseWashRules 对非法 JSON 一律回退默认，
// 所以这里序列化失败时宁可给空串（=默认），也不要塞一个解析不了的字符串进去。
func marshalWashRules(rules []moviepilot.WashRule) string {
	b, err := json.Marshal(rules)
	if err != nil {
		return ""
	}
	return string(b)
}
