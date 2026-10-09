package domain

import (
	"context"
	"encoding/json"
	"time"
)

const (
	AutomationStatusRunning = "running"
	AutomationStatusPaused  = "paused"

	AutomationRunRunning = "running"
	AutomationRunSuccess = "success"
	AutomationRunFailed  = "failed"

	AutomationTriggerDaily           = "daily"
	AutomationTriggerInterval        = "interval"
	AutomationTriggerAdvanced        = "advanced"
	AutomationTriggerWebhook         = "webhook"
	AutomationTriggerOfflineDownload = "offline_download"
	// AutomationTriggerCasAutoSave CAS 清单经通知渠道自动转存到网盘目录后触发。
	AutomationTriggerCasAutoSave = "cas_autosave"
	// AutomationTriggerPlayReport 观影报告：到点生成排行图并推送。
	//
	// 它是一个**排期型**触发器（每天/每周某天某时跑一次），但复用
	// advanced 的 weekly/monthly 语义而不是自己再发明一套 —— 排期计算
	// (nextAdvancedRun) 与校正逻辑已经在那里，少一个触发器就少一份分叉。
	AutomationTriggerPlayReport = "play_report"

	AutomationActionOrganize              = "organize"
	AutomationActionStrm                  = "strm"
	AutomationActionStrmScrape            = "strm_scrape"
	AutomationActionCacheClear            = "cache_clear"
	AutomationActionDelay                 = "delay"
	AutomationActionEmbyRefresh           = "emby_refresh"
	AutomationActionEmbyCompleteMediaInfo = "emby_complete_media_info"
	AutomationActionFnosScan              = "fnos_scan"
	AutomationActionFnosRefreshMetadata   = "fnos_refresh_metadata"
	// AutomationActionNotify 向已配置的通知渠道发送一条消息（入库通知）。
	AutomationActionNotify = "notify"

	AutomationConditionAlways      = "always"
	AutomationConditionPrevSuccess = "prev_success"
	AutomationConditionPrevFailed  = "prev_failed"
)

type AutomationRule struct {
	ID             int64
	Name           string
	TriggerType    string
	TriggerConfig  json.RawMessage
	Actions        json.RawMessage
	Status         string
	NextRunAt      time.Time
	LastRunAt      time.Time
	LastRunStatus  string
	LastRunMessage string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type AutomationRun struct {
	ID            int64
	RuleID        int64
	TriggerSource string
	Status        string
	Message       string
	Result        json.RawMessage
	StartedAt     time.Time
	FinishedAt    time.Time
	CreatedAt     time.Time
}

type AutomationRuleRepository interface {
	Create(ctx context.Context, rule *AutomationRule) (int64, error)
	Update(ctx context.Context, rule *AutomationRule) error
	Delete(ctx context.Context, id int64) error
	Get(ctx context.Context, id int64) (*AutomationRule, error)
	List(ctx context.Context, includePaused bool) ([]*AutomationRule, error)
}

type AutomationRunRepository interface {
	Create(ctx context.Context, run *AutomationRun) (int64, error)
	Update(ctx context.Context, run *AutomationRun) error
	List(ctx context.Context, ruleID int64, limit int) ([]*AutomationRun, error)
	Clear(ctx context.Context) (int, error)
}
