package controllers

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/moviepilot"
)

// defaultWashScanCron 未配置 cron 时的默认定时扫描时间（每日凌晨 3 点）
const defaultWashScanCron = "0 3 * * *"

// 违规扫描防重入（手动/定时共用）
var (
	washScanMu   sync.Mutex
	washScanBusy = make(map[uint]bool)
	// washScanNextRun 各账号下一次定时扫描时间（内存态，重启后以「下一次 cron 窗口」为准）
	washScanNextRun = make(map[uint]time.Time)
)

// ListWashItemsAPI 查询待洗版清单 GET /api/wash/items?account_id=&status=
func ListWashItemsAPI(c *gin.Context) {
	accountID, _ := strconv.ParseUint(c.Query("account_id"), 10, 32)
	status := c.Query("status")
	items, err := models.ListWashItems(uint(accountID), status)
	if err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "加载待洗版清单失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(200, APIResponse[any]{Code: Success, Message: "", Data: items})
}

// WashStatsAPI 洗版统计 GET /api/wash/stats（各账号待洗版/已放弃数量）
func WashStatsAPI(c *gin.Context) {
	configs, err := models.ListAutoOrganizeConfigs()
	if err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "加载配置失败：" + err.Error(), Data: nil})
		return
	}
	type statRow struct {
		AccountID uint   `json:"account_id"`
		Pending   int64  `json:"pending"`
		Abandoned int64  `json:"abandoned"`
		Washed    int64  `json:"washed"`
	}
	rows := make([]statRow, 0, len(configs))
	for _, cfg := range configs {
		rows = append(rows, statRow{
			AccountID: cfg.AccountID,
			Pending:   models.CountWashItems(cfg.AccountID, models.WashStatusPending),
			Abandoned: models.CountWashItems(cfg.AccountID, models.WashStatusAbandoned),
			Washed:    models.CountWashItems(cfg.AccountID, models.WashStatusWashed),
		})
	}
	c.JSON(200, APIResponse[any]{Code: Success, Message: "", Data: rows})
}

// SetWashItemStatusAPI 批量放弃/恢复待洗版条目 POST /api/wash/items/status
func SetWashItemStatusAPI(c *gin.Context) {
	req := struct {
		IDs    []uint `json:"ids"`
		Status string `json:"status"`
	}{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "请选择条目", Data: nil})
		return
	}
	if req.Status != models.WashStatusAbandoned && req.Status != models.WashStatusPending && req.Status != models.WashStatusWashed {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "不支持的状态：" + req.Status, Data: nil})
		return
	}
	if err := models.SetWashItemStatus(req.IDs, req.Status); err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "更新失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(200, APIResponse[any]{Code: Success, Message: "已更新", Data: nil})
}

// ScanWashNowAPI 手动触发违规扫描 POST /api/wash/scan {account_id}
func ScanWashNowAPI(c *gin.Context) {
	req := struct {
		AccountID uint `json:"account_id"`
	}{}
	_ = c.ShouldBindJSON(&req)
	if req.AccountID == 0 {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "请选择网盘账号", Data: nil})
		return
	}
	cfg, err := models.GetAutoOrganizeConfigByAccount(req.AccountID)
	if err != nil || cfg == nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "该账号未配置自动整理（先保存自动整理配置后即可扫描洗版）", Data: nil})
		return
	}
	washScanMu.Lock()
	if washScanBusy[req.AccountID] {
		washScanMu.Unlock()
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "该账号正在扫描中，请稍后再试", Data: nil})
		return
	}
	washScanBusy[req.AccountID] = true
	washScanMu.Unlock()

	go func(cfg models.AutoOrganizeConfig) {
		defer func() {
			if r := recover(); r != nil {
				helpers.AppLogger.Errorf("违规扫描账号 %d 执行异常：%v", cfg.AccountID, r)
			}
			washScanMu.Lock()
			delete(washScanBusy, cfg.AccountID)
			washScanMu.Unlock()
		}()
		summary, sErr := moviepilot.RunWashScan(context.Background(), &cfg)
		if sErr != nil {
			helpers.AppLogger.Errorf("违规扫描失败（账号 %d）：%v", cfg.AccountID, sErr)
			return
		}
		moviepilot.NotifyWashScan(summary)
	}(*cfg)
	c.JSON(200, APIResponse[any]{Code: Success, Message: "扫描已开始", Data: nil})
}

// RunWashNowAPI 一键洗版：执行一轮自动整理（消费待整理目录中的新源，按洗版策略更优才覆盖）
// POST /api/wash/run {account_id}
func RunWashNowAPI(c *gin.Context) {
	req := struct {
		AccountID uint `json:"account_id"`
	}{}
	_ = c.ShouldBindJSON(&req)
	if req.AccountID == 0 {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "请选择网盘账号", Data: nil})
		return
	}
	cfg, err := models.GetAutoOrganizeConfigByAccount(req.AccountID)
	if err != nil || cfg == nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "该账号未配置自动整理", Data: nil})
		return
	}
	if !startAutoOrganizeInBackground(*cfg) {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "该账号正在整理中，请稍后再试", Data: nil})
		return
	}
	c.JSON(200, APIResponse[any]{Code: Success, Message: "已开始洗版整理", Data: nil})
}

// ListWashLogsAPI 查询洗版日志 GET /api/wash/logs?account_id=&limit=
func ListWashLogsAPI(c *gin.Context) {
	accountID, _ := strconv.ParseUint(c.Query("account_id"), 10, 32)
	limit, _ := strconv.Atoi(c.Query("limit"))
	logs, err := models.ListWashLogs(uint(accountID), limit)
	if err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "加载洗版日志失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(200, APIResponse[any]{Code: Success, Message: "", Data: logs})
}

// ClearWashLogsAPI 清空洗版日志 DELETE /api/wash/logs?account_id=
func ClearWashLogsAPI(c *gin.Context) {
	accountID, _ := strconv.ParseUint(c.Query("account_id"), 10, 32)
	if err := models.ClearWashLogs(uint(accountID)); err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "清空失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(200, APIResponse[any]{Code: Success, Message: "已清空", Data: nil})
}

// maybeRunWashScanScheduled 按账号配置的 cron 定时触发违规扫描（P1-2）：
// 由自动整理监控轮询调用；到点执行扫描并（可选）连带执行一轮整理洗版。
func maybeRunWashScanScheduled(ctx context.Context, cfg *models.AutoOrganizeConfig) {
	if cfg == nil || !cfg.WashScanAuto {
		return
	}
	cronStr := strings.TrimSpace(cfg.WashScanCron)
	if cronStr == "" {
		cronStr = defaultWashScanCron
	}
	nexts := helpers.GetNextTimeByCronStr(cronStr, 1)
	if len(nexts) == 0 {
		helpers.AppLogger.Warnf("违规扫描定时：cron 表达式无效（账号 %d）：%s", cfg.AccountID, cronStr)
		return
	}
	now := time.Now()
	washScanMu.Lock()
	next := washScanNextRun[cfg.AccountID]
	if next.IsZero() {
		next = nexts[0] // 首次：等待下一个 cron 窗口
		washScanNextRun[cfg.AccountID] = next
		washScanMu.Unlock()
		return
	}
	if now.Before(next) {
		washScanMu.Unlock()
		return
	}
	if washScanBusy[cfg.AccountID] {
		washScanNextRun[cfg.AccountID] = nexts[0]
		washScanMu.Unlock()
		return
	}
	washScanBusy[cfg.AccountID] = true
	nextRun := nexts[0]
	washScanMu.Unlock()

	go func(cfg models.AutoOrganizeConfig, nextRun time.Time) {
		defer func() {
			if r := recover(); r != nil {
				helpers.AppLogger.Errorf("定时违规扫描账号 %d 执行异常：%v", cfg.AccountID, r)
			}
			washScanMu.Lock()
			delete(washScanBusy, cfg.AccountID)
			washScanNextRun[cfg.AccountID] = nextRun
			washScanMu.Unlock()
		}()
		summary, sErr := moviepilot.RunWashScan(context.Background(), &cfg)
		if sErr != nil {
			helpers.AppLogger.Errorf("定时违规扫描失败（账号 %d）：%v", cfg.AccountID, sErr)
			return
		}
		moviepilot.NotifyWashScan(summary)
		// 扫描完成后按配置自动执行一轮整理洗版（消费待整理目录新源）
		if cfg.WashScanAuto && !startAutoOrganizeInBackground(cfg) {
			helpers.AppLogger.Warnf("定时洗版整理跳过（账号 %d 正在整理中）", cfg.AccountID)
		}
	}(*cfg, nextRun)
}