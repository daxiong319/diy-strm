package classifyorganize

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"litepan/internal/discover/ddb"
	"litepan/internal/domain"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// 目录整理分类：用户自定义规则
//
// 与内置模板（media/region/genre/custom）并存：用户未配置任何自定义规则时
// 完全回落既有模板逻辑，保证向后兼容；一旦配置了启用中的自定义规则，则按
// Position 升序评估，**首个命中胜出**（对齐 C 佬二级分类规则文件的优先级语义）。
// ---------------------------------------------------------------------------

// ClassifyRule 一条用户自定义分类规则。
// 一条规则对应一个目标目录（TargetPath，形如 A/B/C），携带若干条件。
type ClassifyRule struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// MediaType 限定规则适用的媒体类型：movie / tv / 空（两者皆适用）。
	MediaType string `gorm:"size:8;index" json:"media_type"`
	// TargetPath 目标目录相对路径，用 / 分隔，可为多级。
	TargetPath string `gorm:"size:512" json:"target_path"`
	// Conditions 条件列表的 JSON 序列化形式（[]RuleCondition）。
	Conditions string `gorm:"type:text" json:"-"`
	// Enabled 关闭的规则不参与评估。
	Enabled bool `gorm:"default:true" json:"enabled"`
	// Position 评估顺序，升序；小者优先。
	Position int `gorm:"index" json:"position"`
	// Remark 备注，仅用于界面展示。
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// 非持久化：出入参使用的条件列表。
	ConditionList []RuleCondition `gorm:"-" json:"conditions"`
}

func (ClassifyRule) TableName() string { return "classify_rules" }

// RuleCondition 一条分类条件。
// Key 为参考文件中的条件键（genre_ids / keywords / include_keywords /
// original_language / origin_country / year / series_keywords / series_actors）。
// Optional 对应参考文件的 `?` 前缀：软条件，组内任一软条件命中即可。
type RuleCondition struct {
	Key      string `json:"key"`
	Values   string `json:"values"`
	Optional bool   `json:"optional"`
}

// knownRuleKeys 参考文件使用的全部条件键。
var knownRuleKeys = []string{
	"genre_ids",
	"keywords",
	"include_keywords",
	"original_language",
	"origin_country",
	"year",
	"series_keywords",
	"series_actors",
}

func isKnownRuleKey(key string) bool {
	for _, item := range knownRuleKeys {
		if item == key {
			return true
		}
	}
	return false
}

// EnsureClassifySchema 建表，由装配层在 GORM 句柄就绪后调用。
func EnsureClassifySchema() error {
	db := ddb.MustDb()
	if db == nil {
		return fmt.Errorf("分类规则数据库句柄未初始化")
	}
	return db.AutoMigrate(&ClassifyRule{})
}

// dbOrNil 惰性取 GORM 句柄：service 在 ddb.Init 之前构造，因此不能在构造期缓存。
func (s *Service) dbOrNil() *gorm.DB {
	if s == nil {
		return nil
	}
	return ddb.MustDb()
}

// decodeConditions 把持久化的 JSON 还原成条件列表；空值返回空列表。
func decodeConditions(raw string) ([]RuleCondition, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var list []RuleCondition
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("解析规则条件失败: %w", err)
	}
	return list, nil
}

func encodeConditions(list []RuleCondition) (string, error) {
	raw, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("序列化规则条件失败: %w", err)
	}
	return string(raw), nil
}

// normalizeRuleCondition 校验并归一化单条条件。
func normalizeRuleCondition(in RuleCondition) (RuleCondition, error) {
	key := strings.ToLower(strings.TrimSpace(in.Key))
	if key == "" {
		return RuleCondition{}, fmt.Errorf("条件键不能为空")
	}
	if !isKnownRuleKey(key) {
		return RuleCondition{}, fmt.Errorf("不支持的条件键：%s（支持 %s）", in.Key, strings.Join(knownRuleKeys, "/"))
	}
	values := strings.TrimSpace(in.Values)
	if values == "" {
		return RuleCondition{}, fmt.Errorf("条件“%s”的取值不能为空", key)
	}
	if utf8.RuneCountInString(values) > 4000 {
		return RuleCondition{}, fmt.Errorf("条件“%s”的取值不能超过 4000 个字符", key)
	}
	for _, r := range values {
		if unicode.IsControl(r) {
			return RuleCondition{}, fmt.Errorf("条件“%s”的取值不能包含控制字符", key)
		}
	}
	if key == "year" {
		if _, err := parseYearValues(values); err != nil {
			return RuleCondition{}, fmt.Errorf("条件“year”的取值无效：%v", err)
		}
	}
	return RuleCondition{Key: key, Values: values, Optional: in.Optional}, nil
}

// normalizeRuleTargetPath 校验目标目录：允许 A/B/C，逐级校验片段。
func normalizeRuleTargetPath(raw string) (string, error) {
	raw = strings.Trim(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", fmt.Errorf("目标目录不能为空")
	}
	segments := strings.Split(raw, "/")
	if len(segments) > 8 {
		return "", fmt.Errorf("目标目录层级不能超过 8 级")
	}
	clean := make([]string, 0, len(segments))
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if err := validatePathSegment(segment); err != nil {
			return "", err
		}
		clean = append(clean, segment)
	}
	out := strings.Join(clean, "/")
	if utf8.RuneCountInString(out) > 400 {
		return "", fmt.Errorf("目标目录不能超过 400 个字符")
	}
	return out, nil
}

// normalizeRule 归一化一条规则（校验 + 规整）。
func normalizeRule(in ClassifyRule) (ClassifyRule, error) {
	out := in
	out.MediaType = strings.ToLower(strings.TrimSpace(in.MediaType))
	switch out.MediaType {
	case "", "movie", "tv":
	default:
		return ClassifyRule{}, fmt.Errorf("媒体类型只能为 movie / tv 或留空")
	}
	path, err := normalizeRuleTargetPath(in.TargetPath)
	if err != nil {
		return ClassifyRule{}, fmt.Errorf("目标目录无效：%w", err)
	}
	out.TargetPath = path
	out.Remark = strings.TrimSpace(in.Remark)
	if utf8.RuneCountInString(out.Remark) > 255 {
		return ClassifyRule{}, fmt.Errorf("备注不能超过 255 个字符")
	}
	if len(in.ConditionList) > 16 {
		return ClassifyRule{}, fmt.Errorf("单条规则的条件不能超过 16 项")
	}
	seen := make(map[string]bool, len(in.ConditionList))
	conditions := make([]RuleCondition, 0, len(in.ConditionList))
	for _, item := range in.ConditionList {
		condition, err := normalizeRuleCondition(item)
		if err != nil {
			return ClassifyRule{}, err
		}
		if seen[condition.Key] {
			return ClassifyRule{}, fmt.Errorf("同一条规则不能重复配置条件键：%s", condition.Key)
		}
		seen[condition.Key] = true
		conditions = append(conditions, condition)
	}
	out.ConditionList = conditions
	encoded, err := encodeConditions(conditions)
	if err != nil {
		return ClassifyRule{}, err
	}
	out.Conditions = encoded
	if out.Position < 0 {
		out.Position = 0
	}
	return out, nil
}

// ListRules 返回全部规则，按 Position 升序（同 Position 按 ID 升序）。
func (s *Service) ListRules(ctx context.Context) ([]ClassifyRule, error) {
	db := s.dbOrNil()
	if db == nil {
		return []ClassifyRule{}, nil
	}
	var rows []ClassifyRule
	if err := db.WithContext(ctx).Order("position asc, id asc").Find(&rows).Error; err != nil {
		return nil, domain.Errorf(domain.CodeInternal, "读取分类规则失败")
	}
	out := make([]ClassifyRule, 0, len(rows))
	for _, row := range rows {
		list, err := decodeConditions(row.Conditions)
		if err != nil {
			// 脏数据不应让整个列表不可用：跳过坏条件，保留规则本体。
			list = nil
		}
		row.ConditionList = list
		out = append(out, row)
	}
	return out, nil
}

// CreateRule 追加一条规则到末尾。
func (s *Service) CreateRule(ctx context.Context, in ClassifyRule) (ClassifyRule, error) {
	db := s.dbOrNil()
	if db == nil {
		return ClassifyRule{}, domain.Errorf(domain.CodeInternal, "分类规则数据库未就绪")
	}
	rule, err := normalizeRule(in)
	if err != nil {
		return ClassifyRule{}, domain.Errorf(domain.CodeValidation, "%v", err)
	}
	rule.ID = 0
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxPosition int
		if err := tx.Model(&ClassifyRule{}).Select("COALESCE(MAX(position), -1)").Scan(&maxPosition).Error; err != nil {
			return err
		}
		rule.Position = maxPosition + 1
		return tx.Create(&rule).Error
	})
	if err != nil {
		return ClassifyRule{}, domain.Errorf(domain.CodeInternal, "创建分类规则失败")
	}
	rule.ConditionList, _ = decodeConditions(rule.Conditions)
	return rule, nil
}

// UpdateRule 覆盖一条规则的内容（保持其 Position 不变）。
func (s *Service) UpdateRule(ctx context.Context, id uint, in ClassifyRule) (ClassifyRule, error) {
	db := s.dbOrNil()
	if db == nil {
		return ClassifyRule{}, domain.Errorf(domain.CodeInternal, "分类规则数据库未就绪")
	}
	if id == 0 {
		return ClassifyRule{}, domain.Errorf(domain.CodeValidation, "规则 ID 无效")
	}
	rule, err := normalizeRule(in)
	if err != nil {
		return ClassifyRule{}, domain.Errorf(domain.CodeValidation, "%v", err)
	}
	var existing ClassifyRule
	if err := db.WithContext(ctx).First(&existing, id).Error; err != nil {
		return ClassifyRule{}, domain.Errorf(domain.CodeNotFound, "分类规则不存在")
	}
	existing.MediaType = rule.MediaType
	existing.TargetPath = rule.TargetPath
	existing.Conditions = rule.Conditions
	existing.Enabled = rule.Enabled
	existing.Remark = rule.Remark
	if err := db.WithContext(ctx).Save(&existing).Error; err != nil {
		return ClassifyRule{}, domain.Errorf(domain.CodeInternal, "更新分类规则失败")
	}
	existing.ConditionList, _ = decodeConditions(existing.Conditions)
	return existing, nil
}

// DeleteRule 删除一条规则。
func (s *Service) DeleteRule(ctx context.Context, id uint) error {
	db := s.dbOrNil()
	if db == nil {
		return domain.Errorf(domain.CodeInternal, "分类规则数据库未就绪")
	}
	result := db.WithContext(ctx).Delete(&ClassifyRule{}, id)
	if result.Error != nil {
		return domain.Errorf(domain.CodeInternal, "删除分类规则失败")
	}
	if result.RowsAffected == 0 {
		return domain.Errorf(domain.CodeNotFound, "分类规则不存在")
	}
	return nil
}

// ReorderRules 按传入的 ID 顺序重排全部规则（未列出的规则排在最后，保持相对顺序）。
func (s *Service) ReorderRules(ctx context.Context, ids []uint) ([]ClassifyRule, error) {
	db := s.dbOrNil()
	if db == nil {
		return nil, domain.Errorf(domain.CodeInternal, "分类规则数据库未就绪")
	}
	rows, err := s.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	index := make(map[uint]int, len(ids))
	for position, id := range ids {
		if _, dup := index[id]; dup {
			return nil, domain.Errorf(domain.CodeValidation, "排序列表存在重复规则 ID")
		}
		index[id] = position
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, leftOK := index[rows[i].ID]
		right, rightOK := index[rows[j].ID]
		switch {
		case leftOK && rightOK:
			return left < right
		case leftOK:
			return true
		case rightOK:
			return false
		default:
			return rows[i].Position < rows[j].Position
		}
	})
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for position, row := range rows {
			if row.Position == position {
				continue
			}
			if err := tx.Model(&ClassifyRule{}).Where("id = ?", row.ID).
				Update("position", position).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, domain.Errorf(domain.CodeInternal, "保存分类规则顺序失败")
	}
	return s.ListRules(ctx)
}

// ReplaceRules 用给定规则整体替换现有规则（导入使用）。返回写入后的规则列表。
func (s *Service) ReplaceRules(ctx context.Context, rules []ClassifyRule) ([]ClassifyRule, error) {
	db := s.dbOrNil()
	if db == nil {
		return nil, domain.Errorf(domain.CodeInternal, "分类规则数据库未就绪")
	}
	normalized := make([]ClassifyRule, 0, len(rules))
	for index, item := range rules {
		rule, err := normalizeRule(item)
		if err != nil {
			return nil, domain.Errorf(domain.CodeValidation, "第 %d 条规则无效：%v", index+1, err)
		}
		rule.ID = 0
		rule.Position = index
		normalized = append(normalized, rule)
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 整表替换：导入语义是"用文件内容覆盖当前规则"。
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).
			Delete(&ClassifyRule{}).Error; err != nil {
			return err
		}
		if len(normalized) == 0 {
			return nil
		}
		return tx.CreateInBatches(&normalized, 200).Error
	})
	if err != nil {
		return nil, domain.Errorf(domain.CodeInternal, "导入分类规则失败")
	}
	return s.ListRules(ctx)
}
