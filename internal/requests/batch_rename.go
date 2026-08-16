package requests

import (
	"fmt"
	"strings"

	"diy-strm/internal/renamerule"
	"diy-strm/internal/validation"
)

// BatchRenameItem 批量重命名条目（预览）。
type BatchRenameItem struct {
	FileID   string `json:"file_id"`
	Name     string `json:"name"`
	Type     int    `json:"type"` // 0=文件 1=目录
	ParentID string `json:"parent_id"`
}

// BatchRenamePreviewRequest 批量重命名预览请求。
type BatchRenamePreviewRequest struct {
	AccountID     uint               `json:"account_id"`
	ParentID      string             `json:"parent_id"`
	FolderName    string             `json:"folder_name"`
	KeepExt       bool               `json:"keep_ext"`
	Rules         []renamerule.Rule  `json:"rules"`
	Items         []BatchRenameItem  `json:"items"`
	ExistingNames []string           `json:"existing_names"` // 当前目录除 items 外的已有名称，用于同目录冲突检测
}

// Validate 校验批量重命名预览请求。
func (r BatchRenamePreviewRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if len(r.Rules) == 0 {
		return validation.New("rules", "至少需要一条规则")
	}
	if len(r.Rules) > 20 {
		return validation.New("rules", "单次最多 20 条规则")
	}
	ruleTypes := map[string]bool{}
	for _, t := range renamerule.RuleTypes {
		ruleTypes[t] = true
	}
	for i, rule := range r.Rules {
		if !ruleTypes[rule.Type] {
			return validation.New("rules", fmt.Sprintf("第 %d 条规则类型无效", i+1))
		}
	}
	if len(r.Items) == 0 {
		return validation.New("items", "不能为空")
	}
	if len(r.Items) > 2000 {
		return validation.New("items", "单次最多 2000 个文件")
	}
	for _, item := range r.Items {
		if strings.TrimSpace(item.FileID) == "" || strings.TrimSpace(item.Name) == "" {
			return validation.New("items", "文件 ID 与名称不能为空")
		}
	}
	if len(r.ExistingNames) > 20000 {
		return validation.New("existing_names", "数量过多")
	}
	return nil
}

// BatchRenameApplyItem 批量重命名应用条目。
type BatchRenameApplyItem struct {
	FileID   string `json:"file_id"`
	Name     string `json:"name"`
	NewName  string `json:"new_name"`
	ParentID string `json:"parent_id"`
}

// BatchRenameApplyRequest 批量重命名应用请求。
type BatchRenameApplyRequest struct {
	AccountID uint                     `json:"account_id"`
	ParentID  string                   `json:"parent_id"`
	Label     string                   `json:"label"` // 历史记录名称，默认"批量重命名"
	KeepExt   bool                     `json:"keep_ext"`
	Rules     []renamerule.Rule        `json:"rules"`
	Items     []BatchRenameApplyItem   `json:"items"`
}

// Validate 校验批量重命名应用请求。
func (r BatchRenameApplyRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if len(r.Items) == 0 {
		return validation.New("items", "不能为空")
	}
	if len(r.Items) > 2000 {
		return validation.New("items", "单次最多 2000 个文件")
	}
	for _, item := range r.Items {
		if strings.TrimSpace(item.FileID) == "" || strings.TrimSpace(item.Name) == "" {
			return validation.New("items", "文件 ID 与名称不能为空")
		}
		if err := validateFolderName(item.NewName); err != nil {
			return err
		}
	}
	return nil
}

// RenamePresetSaveRequest 保存常用组合请求。
type RenamePresetSaveRequest struct {
	Name    string            `json:"name"`
	KeepExt bool              `json:"keep_ext"`
	Rules   []renamerule.Rule `json:"rules"`
}

// Validate 校验保存常用组合请求。
func (r RenamePresetSaveRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return validation.New("name", "常用组合名称不能为空")
	}
	if len(r.Name) > 64 {
		return validation.New("name", "名称最长 64 个字符")
	}
	if len(r.Rules) == 0 {
		return validation.New("rules", "至少需要一条规则")
	}
	if len(r.Rules) > 20 {
		return validation.New("rules", "单次最多 20 条规则")
	}
	return nil
}

// BatchRenameRollbackRequest 回滚请求。
type BatchRenameRollbackRequest struct {
	AccountID uint `json:"account_id"`
	HistoryID uint `json:"history_id"`
}

// Validate 校验回滚请求。
func (r BatchRenameRollbackRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if r.HistoryID == 0 {
		return validation.New("history_id", "历史记录 ID 不能为空")
	}
	return nil
}