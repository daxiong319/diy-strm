package automation

import (
	"context"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/eventbus"
)

// CAS 自动转存完成触发器：把「资源已落到待整理目录」变成自动化入口。
//
// 链路是 CAS 转存 → 整理 → STRM → 生成本地元数据 → Emby 扫库 → 入库通知。
// 这里只负责第一跳（匹配规则并入队），后续动作由原有动作链执行。

// casTriggerBatch 一次触发携带的上下文。同一批 .cas 只跑一次联动，
// 但动作需要知道「这次是哪些文件」，才能把 {file} 占位符和入库通知写对。
type casTriggerBatch struct {
	AccountID int64
	DriveType string
	Dir       string
	Files     []string // 落盘后的文件名，含 .cas
	Filenames string   // 展示用的合并文件名，超过 3 个时省略
}

// onCasAutoSaved 收到转存成功事件后，匹配所有启用中的 cas_autosave 规则。
//
// 同一批文件会为每条命中的规则各排一次队；submitRun 的 dedupe 保证
// 同一条规则在排队期间不会被重复投递。
func (s *Service) onCasAutoSaved(ctx context.Context, event eventbus.CasAutoSaved) {
	if s == nil || s.rules == nil {
		return
	}
	batch := newCasTriggerBatch(event)
	if len(batch.Files) == 0 {
		return
	}
	rows, err := s.rules.List(ctx, false)
	if err != nil {
		s.log.Warn("读取 CAS 转存触发列表失败", "err", err)
		return
	}
	for _, row := range rows {
		if row == nil || row.Status != domain.AutomationStatusRunning || row.TriggerType != domain.AutomationTriggerCasAutoSave {
			continue
		}
		if !matchCasAutoSave(decodeMap(row.TriggerConfig), event) {
			continue
		}
		s.submitRunWithBatch(row.ID, domain.AutomationTriggerCasAutoSave, true, &batch)
	}
}

// newCasTriggerBatch 归一化事件里的文件信息。
// 事件理论上只带单个文件（转存是逐个落盘的），但保留切片是为了让
// 后续「一批多个」的调用方无需改动匹配与通知逻辑。
func newCasTriggerBatch(event eventbus.CasAutoSaved) casTriggerBatch {
	name := strings.TrimSpace(event.FileName)
	files := make([]string, 0, 1)
	if name != "" {
		files = append(files, name)
	}
	return casTriggerBatch{
		AccountID: event.AccountID,
		DriveType: event.DriveType,
		Dir:       event.SaveDir,
		Files:     files,
		Filenames: name,
	}
}

// matchCasAutoSave 判断事件是否落在规则配置的账号与目录范围内。
//
// 目录用前缀匹配，与离线下载触发器一致：用户配置「影视/CAS待整理」时，
// 转存到其子目录（例如「影视/CAS待整理/2026」）也算命中，
// 否则用户一旦在待整理目录下建了子目录，联动就会静默失效。
func matchCasAutoSave(cfg map[string]any, event eventbus.CasAutoSaved) bool {
	configuredAccount := int64(anyInt(cfg["account_id"]))
	// 账号 0 表示「任意账号」，此时靠目录区分；用户可能把多个网盘的
	// 待整理目录配成同一个相对路径（如都叫 CAS待整理）。
	if event.AccountID <= 0 && configuredAccount > 0 {
		return false
	}
	if configuredAccount > 0 && configuredAccount != event.AccountID {
		return false
	}
	configuredPath := normalizePath(anyString(cfg["path"]))
	if configuredPath == "/" {
		return true
	}
	eventPath := normalizePath(event.SaveDir)
	return eventPath == configuredPath || strings.HasPrefix(eventPath, configuredPath+"/")
}
