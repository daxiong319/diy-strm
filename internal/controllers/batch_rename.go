package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"diy-strm/internal/models"
	"diy-strm/internal/renamerule"
	"diy-strm/internal/requests"

	"github.com/gin-gonic/gin"
)

// batchRenameTargets 由前端条目转为规则引擎目标。
func batchRenameTargets(items []requests.BatchRenameItem) []renamerule.Target {
	targets := make([]renamerule.Target, 0, len(items))
	for _, item := range items {
		targets = append(targets, renamerule.Target{
			ID:       item.FileID,
			Name:     item.Name,
			Type:     item.Type,
			ParentID: item.ParentID,
		})
	}
	return targets
}

// batchRenameExistingNames 构建设名冲突检测映射（同一目录复用请求方提供的已有名称）。
func batchRenameExistingNames(existingNames []string, parentID string) map[string][]string {
	return map[string][]string{parentID: existingNames}
}

// BatchRenamePreview 批量重命名预览：按规则计算新名称并返回校验错误，不改动文件。
func BatchRenamePreview(c *gin.Context) {
	var req requests.BatchRenamePreviewRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	parentID := strings.TrimSpace(req.ParentID)
	if parentID == "" {
		parentID = "0"
	}
	rows := renamerule.Preview(batchRenameTargets(req.Items), req.Rules, req.KeepExt, req.FolderName)

	changedTargets := make([]renamerule.Target, 0, len(rows))
	for _, row := range rows {
		if row.Changed {
			changedTargets = append(changedTargets, row.Target)
		}
	}
	var errors []string
	if len(changedTargets) > 0 {
		errors = renamerule.ValidateRules(req.Rules)
		errors = append(errors, renamerule.ValidateTargets(changedTargets, batchRenameExistingNames(req.ExistingNames, parentID))...)
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "预览成功", Data: gin.H{
		"items":  rows,
		"errors": errors,
		"changed_count": len(changedTargets),
		"total_count":   len(rows),
	}})
}

type batchRenameApplySuccess struct {
	FileID  string `json:"file_id"`
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
}

type batchRenameApplyFailed struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// BatchRenameApply 批量重命名应用：按预览结果逐条重命名并写入历史记录（支持回滚）。
func BatchRenameApply(c *gin.Context) {
	var req requests.BatchRenameApplyRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}

	succ := make([]batchRenameApplySuccess, 0, len(req.Items))
	failed := make([]batchRenameApplyFailed, 0)
	historyTargets := make([]renamerule.Target, 0, len(req.Items))
	changedCount := 0

	for _, item := range req.Items {
		if item.NewName == item.Name {
			succ = append(succ, batchRenameApplySuccess{FileID: item.FileID, OldName: item.Name, NewName: item.Name})
			continue
		}
		if err := renameNetdiskFile(account, item.FileID, item.NewName); err != nil {
			failed = append(failed, batchRenameApplyFailed{FileID: item.FileID, Name: item.Name, Reason: err.Error()})
			continue
		}
		succ = append(succ, batchRenameApplySuccess{FileID: item.FileID, OldName: item.Name, NewName: item.NewName})
		historyTargets = append(historyTargets, renamerule.Target{
			ID:       item.FileID,
			Name:     item.NewName,
			NewName:  item.Name,
			ParentID: item.ParentID,
		})
		changedCount++
	}
	invalidateNetFileCacheForPath(account.SourceType, req.AccountID, req.ParentID)

	if changedCount > 0 {
		saveBatchRenameHistory(c, req, historyTargets, len(req.Items), changedCount)
	}

	message := fmt.Sprintf("重命名完成：成功 %d 个，失败 %d 个", len(succ), len(failed))
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: message, Data: gin.H{
		"success": succ,
		"failed":  failed,
		"success_count": len(succ),
		"fail_count":    len(failed),
	}})
}

// saveBatchRenameHistory 写入历史记录并累计常用组合使用次数。
func saveBatchRenameHistory(c *gin.Context, req requests.BatchRenameApplyRequest, targets []renamerule.Target, itemCount, changeCount int) {
	user, ok := CurrentUser(c)
	if !ok || user == nil {
		return
	}
	rulesJSON, err := json.Marshal(req.Rules)
	if err != nil {
		return
	}
	targetsJSON, err := json.Marshal(targets)
	if err != nil {
		return
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "批量重命名"
	}
	_ = models.CreateRenameHistory(&models.RenameHistory{
		UserID:      user.ID,
		Name:        label,
		Rules:       string(rulesJSON),
		KeepExt:     req.KeepExt,
		Targets:     string(targetsJSON),
		ItemCount:   itemCount,
		ChangeCount: changeCount,
	})
	models.IncrementRenamePresetUse(user.ID, string(rulesJSON), req.KeepExt)
}

// BatchRenameHistoryList 批量重命名历史记录。
func BatchRenameHistoryList(c *gin.Context) {
	user, ok := CurrentUser(c)
	if !ok || user == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取用户信息失败", Data: nil})
		return
	}
	list := models.ListRenameHistories(user.ID, 80)
	items := make([]gin.H, 0, len(list))
	for _, h := range list {
		items = append(items, gin.H{
			"id":           h.ID,
			"name":         h.Name,
			"rules":        h.Rules,
			"keep_ext":     h.KeepExt,
			"item_count":   h.ItemCount,
			"change_count": h.ChangeCount,
			"created_at":   h.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取成功", Data: items})
}

type batchRenameRollbackSuccess struct {
	FileID  string `json:"file_id"`
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
}

// BatchRenameRollback 按历史记录回滚重命名。
func BatchRenameRollback(c *gin.Context) {
	var req requests.BatchRenameRollbackRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	user, ok := CurrentUser(c)
	if !ok || user == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取用户信息失败", Data: nil})
		return
	}
	history := models.GetRenameHistory(req.HistoryID, user.ID)
	if history == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "历史记录不存在", Data: nil})
		return
	}
	var targets []renamerule.Target
	if err := json.Unmarshal([]byte(history.Targets), &targets); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "历史记录数据异常：" + err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}

	restored := make([]batchRenameRollbackSuccess, 0, len(targets))
	restoredIDs := make([]string, 0, len(targets))
	parentIDs := map[string]bool{}
	for _, target := range targets {
		if strings.TrimSpace(target.ID) == "" || strings.TrimSpace(target.Name) == "" {
			continue
		}
		if err := renameNetdiskFile(account, target.ID, target.Name); err != nil {
			continue
		}
		restored = append(restored, batchRenameRollbackSuccess{FileID: target.ID, OldName: target.NewName, NewName: target.Name})
		restoredIDs = append(restoredIDs, target.ID)
		if target.ParentID != "" {
			parentIDs[target.ParentID] = true
		}
	}
	for parentID := range parentIDs {
		invalidateNetFileCacheForPath(account.SourceType, req.AccountID, parentID)
	}

	// 已还原的条目从历史中移除；全部还原则删除历史记录
	if len(restoredIDs) > 0 {
		restoredSet := map[string]bool{}
		for _, id := range restoredIDs {
			restoredSet[id] = true
		}
		remaining := make([]renamerule.Target, 0, len(targets))
		for _, target := range targets {
			if !restoredSet[target.ID] {
				remaining = append(remaining, target)
			}
		}
		if len(remaining) == 0 {
			_ = models.DeleteRenameHistory(req.HistoryID, user.ID)
		} else {
			remainingJSON, err := json.Marshal(remaining)
			if err == nil {
				history.Targets = string(remainingJSON)
				history.ChangeCount = len(remaining)
				_ = models.SaveRenameHistory(history)
			}
		}
	}

	failedCount := len(targets) - len(restored)
	message := fmt.Sprintf("回滚完成：成功 %d 个，失败 %d 个", len(restored), failedCount)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: message, Data: gin.H{
		"success": restored,
		"fail_count": failedCount,
	}})
}

type batchRenamePresetData struct {
	ID       uint              `json:"id"`
	Name     string            `json:"name"`
	Rules    []renamerule.Rule `json:"rules"`
	KeepExt  bool              `json:"keep_ext"`
	UseCount int               `json:"use_count"`
}

// BatchRenamePresetList 常用组合列表。
func BatchRenamePresetList(c *gin.Context) {
	user, ok := CurrentUser(c)
	if !ok || user == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取用户信息失败", Data: nil})
		return
	}
	list := models.ListRenamePresets(user.ID)
	items := make([]batchRenamePresetData, 0, len(list))
	for _, p := range list {
		var rules []renamerule.Rule
		_ = json.Unmarshal([]byte(p.Rules), &rules)
		items = append(items, batchRenamePresetData{
			ID:       p.ID,
			Name:     p.Name,
			Rules:    rules,
			KeepExt:  p.KeepExt,
			UseCount: p.UseCount,
		})
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取成功", Data: items})
}

// BatchRenamePresetSave 保存常用组合（同名覆盖）。
func BatchRenamePresetSave(c *gin.Context) {
	var req requests.RenamePresetSaveRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	user, ok := CurrentUser(c)
	if !ok || user == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取用户信息失败", Data: nil})
		return
	}
	rulesJSON, err := json.Marshal(req.Rules)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "规则数据异常", Data: nil})
		return
	}
	if err := models.CreateRenamePreset(&models.RenamePreset{
		UserID:  user.ID,
		Name:    strings.TrimSpace(req.Name),
		Rules:   string(rulesJSON),
		KeepExt: req.KeepExt,
	}); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "常用组合已保存", Data: nil})
}

// BatchRenamePresetDelete 删除常用组合。
func BatchRenamePresetDelete(c *gin.Context) {
	user, ok := CurrentUser(c)
	if !ok || user == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取用户信息失败", Data: nil})
		return
	}
	var req struct {
		ID uint `json:"id" binding:"required"`
	}
	if err := c.ShouldBind(&req); err != nil || req.ID == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := models.DeleteRenamePreset(req.ID, user.ID); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已删除", Data: nil})
}