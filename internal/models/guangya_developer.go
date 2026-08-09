package models

import (
	"time"

	"diy-strm/internal/db"

	"gorm.io/gorm"
)

// GuangYaDeveloperSetting 光鸭云盘开发者接口配置（每个光鸭源账号一份）
type GuangYaDeveloperSetting struct {
	BaseModel
	AccountID    uint   `gorm:"uniqueIndex" json:"account_id"`
	ClientID     string `gorm:"size:256" json:"client_id"`
	ClientSecret string `gorm:"size:512" json:"-"`
	Enabled      bool   `json:"enabled"`
}

func (*GuangYaDeveloperSetting) TableName() string {
	return "guangya_developer_settings"
}

// GuangYaReceiverToken 光鸭接收 TOKEN（小号秒传的目标授权）
type GuangYaReceiverToken struct {
	BaseModel
	AccountID uint   `gorm:"index" json:"account_id"`
	TokenID   string `gorm:"size:256" json:"token_id"`
	Remark    string `gorm:"size:256" json:"remark"`
}

func (*GuangYaReceiverToken) TableName() string {
	return "guangya_receiver_tokens"
}

// GuangYaTransferTaskStatus 小号秒传任务状态
const (
	GuangYaTransferStatusRunning   = "running"   // 执行中（含预审等待）
	GuangYaTransferStatusAuditing  = "auditing"  // 预审提交后等待人工审核
	GuangYaTransferStatusSuccess   = "success"   // 全部完成
	GuangYaTransferStatusFailed    = "failed"    // 失败
	GuangYaTransferStatusCancelled = "cancelled" // 已取消
)

// GuangYaTransferTask 光鸭小号秒传任务
type GuangYaTransferTask struct {
	BaseModel
	AccountID        uint      `gorm:"index" json:"account_id"`
	ReceiverTokenID  uint      `gorm:"index" json:"receiver_token_id"`
	ReceiverToken    string    `json:"receiver_token"`
	FileIDs          string    `gorm:"size:8192" json:"file_ids"` // JSON 数组
	FileNames        string    `gorm:"size:8192" json:"file_names"`
	Status           string    `gorm:"index" json:"status"`
	TaskID           string    `json:"task_id"` // upload_by_fileid 任务 ID
	PreTaskID        string    `json:"pre_task_id"`
	TotalCount       int       `json:"total_count"`
	SuccessCount     int       `json:"success_count"`
	SkippedCount     int       `json:"skipped_count"`
	FailedCount      int       `json:"failed_count"`
	ErrorMessage     string    `gorm:"size:1024" json:"error_message"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
}

func (*GuangYaTransferTask) TableName() string {
	return "guangya_transfer_tasks"
}

// ---------- 数据访问 ----------

func GetGuangYaDeveloperSetting(accountID uint) (*GuangYaDeveloperSetting, error) {
	var setting GuangYaDeveloperSetting
	err := db.Db.Where("account_id = ?", accountID).First(&setting).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &setting, nil
}

func SaveGuangYaDeveloperSetting(setting *GuangYaDeveloperSetting) error {
	var exists GuangYaDeveloperSetting
	err := db.Db.Where("account_id = ?", setting.AccountID).First(&exists).Error
	if err == gorm.ErrRecordNotFound {
		return db.Db.Create(setting).Error
	}
	if err != nil {
		return err
	}
	return db.Db.Model(&exists).Updates(map[string]interface{}{
		"client_id":     setting.ClientID,
		"client_secret": setting.ClientSecret,
		"enabled":       setting.Enabled,
	}).Error
}

func DeleteGuangYaDeveloperSetting(accountID uint) error {
	return db.Db.Where("account_id = ?", accountID).Delete(&GuangYaDeveloperSetting{}).Error
}

func GetGuangYaReceiverTokens(accountID uint) ([]GuangYaReceiverToken, error) {
	var tokens []GuangYaReceiverToken
	err := db.Db.Where("account_id = ?", accountID).Order("id DESC").Find(&tokens).Error
	return tokens, err
}

func CreateGuangYaReceiverToken(token *GuangYaReceiverToken) error {
	return db.Db.Create(token).Error
}

func DeleteGuangYaReceiverToken(id uint) error {
	return db.Db.Delete(&GuangYaReceiverToken{}, id).Error
}

func GetGuangYaReceiverToken(id uint) (*GuangYaReceiverToken, error) {
	var token GuangYaReceiverToken
	err := db.Db.First(&token, id).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func GetGuangYaTransferTasks(accountID uint, limit int) ([]GuangYaTransferTask, error) {
	var tasks []GuangYaTransferTask
	err := db.Db.Where("account_id = ?", accountID).Order("id DESC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

func GetGuangYaTransferTask(id uint) (*GuangYaTransferTask, error) {
	var task GuangYaTransferTask
	err := db.Db.First(&task, id).Error
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func CreateGuangYaTransferTask(task *GuangYaTransferTask) error {
	return db.Db.Create(task).Error
}

func SaveGuangYaTransferTask(task *GuangYaTransferTask) error {
	return db.Db.Save(task).Error
}

func UpdateGuangYaTransferTask(id uint, updates map[string]interface{}) error {
	return db.Db.Model(&GuangYaTransferTask{}).Where("id = ?", id).Updates(updates).Error
}

func DeleteGuangYaTransferTask(id uint) error {
	return db.Db.Delete(&GuangYaTransferTask{}, id).Error
}
