package automation

import (
	"context"
	"encoding/json"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/eventbus"
)

func TestMatchCasAutoSave(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cfg   map[string]any
		event eventbus.CasAutoSaved
		want  bool
	}{
		{
			name:  "same account and directory",
			cfg:   map[string]any{"account_id": 2, "path": "影视/CAS待整理"},
			event: eventbus.CasAutoSaved{AccountID: 2, SaveDir: "影视/CAS待整理"},
			want:  true,
		},
		{
			name:  "descendant directory still matches",
			cfg:   map[string]any{"account_id": 2, "path": "影视/CAS待整理"},
			event: eventbus.CasAutoSaved{AccountID: 2, SaveDir: "影视/CAS待整理/2026"},
			want:  true,
		},
		{
			name:  "similar prefix is not descendant",
			cfg:   map[string]any{"account_id": 2, "path": "影视/CAS待整理"},
			event: eventbus.CasAutoSaved{AccountID: 2, SaveDir: "影视/CAS待整理备份"},
			want:  false,
		},
		{
			name:  "different account",
			cfg:   map[string]any{"account_id": 2, "path": "影视/CAS待整理"},
			event: eventbus.CasAutoSaved{AccountID: 3, SaveDir: "影视/CAS待整理"},
			want:  false,
		},
		{
			name:  "configured account does not match event without account",
			cfg:   map[string]any{"account_id": 2, "path": "影视/CAS待整理"},
			event: eventbus.CasAutoSaved{AccountID: 0, SaveDir: "影视/CAS待整理"},
			want:  false,
		},
		{
			name:  "empty account means any account",
			cfg:   map[string]any{"path": "影视/CAS待整理"},
			event: eventbus.CasAutoSaved{AccountID: 9, SaveDir: "影视/CAS待整理"},
			want:  true,
		},
		{
			name:  "empty path means any directory",
			cfg:   map[string]any{"account_id": 2},
			event: eventbus.CasAutoSaved{AccountID: 2, SaveDir: "随便/哪个目录"},
			want:  true,
		},
		{
			name:  "root path matches everything on that account",
			cfg:   map[string]any{"account_id": 2, "path": "/"},
			event: eventbus.CasAutoSaved{AccountID: 2, SaveDir: "任意/目录"},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := matchCasAutoSave(tt.cfg, tt.event); got != tt.want {
				t.Fatalf("matchCasAutoSave() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCasAutoSaveTriggerQueuesMatchingRunningRuleOnce(t *testing.T) {
	t.Parallel()

	triggerConfig, _ := json.Marshal(map[string]any{"account_id": 2, "path": "影视/CAS待整理"})
	rule := &domain.AutomationRule{
		ID:            31,
		Name:          "转存后整理入库",
		TriggerType:   domain.AutomationTriggerCasAutoSave,
		TriggerConfig: triggerConfig,
		Actions:       []byte("[]"),
		Status:        domain.AutomationStatusRunning,
	}
	service := New(Options{Rules: newAutomationRuleRepo(rule), Runs: &automationRunRepo{}})
	service.runningRuleID = 99
	event := eventbus.CasAutoSaved{
		AccountID: 2, DriveType: "cloud189", SaveDir: "影视/CAS待整理",
		FileName: "流浪地球2.cas",
	}

	service.onCasAutoSaved(context.Background(), event)
	service.onCasAutoSaved(context.Background(), event)

	if got := len(service.pendingRuns); got != 1 {
		t.Fatalf("匹配事件应去重后只排队一次，实际 %d", got)
	}
	queued := service.pendingRuns[0]
	if queued.ruleID != rule.ID || queued.triggerSource != domain.AutomationTriggerCasAutoSave {
		t.Fatalf("排队内容不正确: %#v", queued)
	}
	if queued.batch == nil || len(queued.batch.Files) != 1 || queued.batch.Files[0] != "流浪地球2.cas" {
		t.Fatalf("触发上下文未随队列入队: %#v", queued.batch)
	}
	if queued.batch.DriveType != "cloud189" || queued.batch.Dir != "影视/CAS待整理" {
		t.Fatalf("触发上下文网盘/目录不正确: %#v", queued.batch)
	}
}

func TestCasAutoSaveTriggerSkipsPausedAndOtherTriggerRules(t *testing.T) {
	t.Parallel()

	triggerConfig, _ := json.Marshal(map[string]any{"account_id": 2, "path": "影视/CAS待整理"})
	paused := &domain.AutomationRule{
		ID: 41, TriggerType: domain.AutomationTriggerCasAutoSave,
		TriggerConfig: triggerConfig, Actions: []byte("[]"),
		Status: domain.AutomationStatusPaused,
	}
	otherTrigger := &domain.AutomationRule{
		ID: 42, TriggerType: domain.AutomationTriggerOfflineDownload,
		TriggerConfig: triggerConfig, Actions: []byte("[]"),
		Status: domain.AutomationStatusRunning,
	}
	service := New(Options{Rules: newAutomationRuleRepo(paused, otherTrigger), Runs: &automationRunRepo{}})
	service.runningRuleID = 99

	service.onCasAutoSaved(context.Background(), eventbus.CasAutoSaved{
		AccountID: 2, SaveDir: "影视/CAS待整理", FileName: "a.cas",
	})

	if got := len(service.pendingRuns); got != 0 {
		t.Fatalf("暂停或非同类型规则不应入队，实际 %d", got)
	}
}

func TestCasAutoSaveTriggerIgnoresEventWithoutFileName(t *testing.T) {
	t.Parallel()

	triggerConfig, _ := json.Marshal(map[string]any{"path": "/"})
	rule := &domain.AutomationRule{
		ID: 51, TriggerType: domain.AutomationTriggerCasAutoSave,
		TriggerConfig: triggerConfig, Actions: []byte("[]"),
		Status: domain.AutomationStatusRunning,
	}
	service := New(Options{Rules: newAutomationRuleRepo(rule), Runs: &automationRunRepo{}})
	service.runningRuleID = 99

	service.onCasAutoSaved(context.Background(), eventbus.CasAutoSaved{AccountID: 2, SaveDir: "影视"})

	if got := len(service.pendingRuns); got != 0 {
		t.Fatalf("没有文件名的空事件不应触发联动，实际 %d", got)
	}
}
