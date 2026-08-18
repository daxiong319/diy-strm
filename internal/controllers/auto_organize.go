package controllers

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/moviepilot"
)

// autoOrganizeWatchInterval 自动整理监控间隔（与频道订阅引擎一致）
const autoOrganizeWatchInterval = 5 * time.Minute

// autoOrganizeRunMu 防止同一账号并发整理（监控轮询与手动触发互斥）
var (
	autoOrganizeRunMu sync.Mutex
	autoOrganizeBusy  = make(map[uint]bool)
)

// StartAutoOrganizeWatcher 启动云盘自动整理后台监控：
// 定期扫描启用自动整理的账号，发现待整理目录新增资源后按账号分类策略整理。
func StartAutoOrganizeWatcher(ctx context.Context) {
	go func() {
		// 启动后先执行一轮
		runAllAutoOrganizeOnce(ctx)
		ticker := time.NewTicker(autoOrganizeWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runAllAutoOrganizeOnce(ctx)
			}
		}
	}()
	helpers.AppLogger.Info("已启动云盘自动整理监控（每 5 分钟扫描一次待整理目录）")
}

// runAllAutoOrganizeOnce 跑一轮所有启用自动整理的账号（串行执行，避免触发网盘限流）
func runAllAutoOrganizeOnce(ctx context.Context) {
	configs, err := models.ListEnabledAutoOrganizeConfigs()
	if err != nil {
		helpers.AppLogger.Errorf("云盘自动整理：加载启用配置失败：%v", err)
		return
	}
	if len(configs) == 0 {
		return
	}
	for _, cfg := range configs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		runAutoOrganizeForConfig(ctx, &cfg)
	}
}

// runAutoOrganizeForConfig 对单个账号执行整理（带防重入）
func runAutoOrganizeForConfig(ctx context.Context, cfg *models.AutoOrganizeConfig) {
	autoOrganizeRunMu.Lock()
	if autoOrganizeBusy[cfg.AccountID] {
		autoOrganizeRunMu.Unlock()
		helpers.AppLogger.Warnf("云盘自动整理：账号 %d 正在进行上一轮整理，跳过本轮", cfg.AccountID)
		return
	}
	autoOrganizeBusy[cfg.AccountID] = true
	autoOrganizeRunMu.Unlock()

	defer func() {
		autoOrganizeRunMu.Lock()
		delete(autoOrganizeBusy, cfg.AccountID)
		autoOrganizeRunMu.Unlock()
	}()

	result := moviepilot.RunAutoOrganize(ctx, cfg)
	if len(result.Details) > 0 {
		helpers.AppLogger.Infof("云盘自动整理完成（账号 %d）：成功 %d / 识别失败 %d / 失败 %d", cfg.AccountID, result.Organized, result.Unrecognized, result.Failed)
	}
}

// autoOrganizeConfigView 前端展示的配置（附带账号信息）
type autoOrganizeConfigView struct {
	models.AutoOrganizeConfig
	AccountSourceType string `json:"account_source_type"`
	AccountUsername   string `json:"account_username"`
}

// GetAutoOrganizeConfigs 查询全部自动整理配置（含账号信息）
func GetAutoOrganizeConfigs(c *gin.Context) {
	configs, err := models.ListAutoOrganizeConfigs()
	if err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "加载自动整理配置失败：" + err.Error(), Data: nil})
		return
	}
	views := make([]autoOrganizeConfigView, 0, len(configs))
	for i := range configs {
		view := autoOrganizeConfigView{AutoOrganizeConfig: configs[i]}
		if account, aErr := models.GetAccountById(configs[i].AccountID); aErr == nil && account != nil {
			view.AccountSourceType = string(account.SourceType)
			view.AccountUsername = account.Username
		}
		views = append(views, view)
	}
	c.JSON(200, APIResponse[any]{Code: Success, Message: "", Data: views})
}

// SaveAutoOrganizeConfig 创建或更新自动整理配置
func SaveAutoOrganizeConfig(c *gin.Context) {
	var cfg models.AutoOrganizeConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if cfg.AccountID == 0 {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "请选择网盘账号", Data: nil})
		return
	}
	account, err := models.GetAccountById(cfg.AccountID)
	if err != nil || account == nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "账号不存在", Data: nil})
		return
	}
	switch account.SourceType {
	case models.SourceType123, models.SourceType115, models.SourceTypePan139, models.SourceTypeGuangYaPan:
	default:
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "该网盘类型暂不支持自动整理", Data: nil})
		return
	}
	if err := models.SaveAutoOrganizeConfig(&cfg); err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "保存失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(200, APIResponse[any]{Code: Success, Message: "保存成功", Data: cfg})
}

// DeleteAutoOrganizeConfig 删除自动整理配置
func DeleteAutoOrganizeConfig(c *gin.Context) {
	id := c.Param("id")
	cfgID, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := models.DeleteAutoOrganizeConfig(uint(cfgID)); err != nil {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "删除失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(200, APIResponse[any]{Code: Success, Message: "删除成功", Data: nil})
}

// RunAutoOrganizeNow 手动触发一次自动整理：
// 请求体 account_id 为空时整理所有启用账号，否则只整理指定账号（不论是否启用）。
func RunAutoOrganizeNow(c *gin.Context) {
	req := struct {
		AccountID uint `json:"account_id"`
	}{}
	_ = c.ShouldBindJSON(&req)

	runAll := req.AccountID == 0
	var configs []models.AutoOrganizeConfig
	if runAll {
		cfgList, err := models.ListEnabledAutoOrganizeConfigs()
		if err != nil {
			c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "加载配置失败：" + err.Error(), Data: nil})
			return
		}
		configs = cfgList
	} else {
		cfg, err := models.GetAutoOrganizeConfigByAccount(req.AccountID)
		if err != nil || cfg == nil {
			c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "该账号未配置自动整理", Data: nil})
			return
		}
		configs = append(configs, *cfg)
	}
	if len(configs) == 0 {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "暂无可整理的账号（先在设置中启用自动整理）", Data: nil})
		return
	}

	started := make([]uint, 0, len(configs))
	busySkipped := make([]uint, 0)
	for i := range configs {
		if startAutoOrganizeInBackground(configs[i]) {
			started = append(started, configs[i].AccountID)
		} else {
			busySkipped = append(busySkipped, configs[i].AccountID)
		}
	}
	if len(started) == 0 {
		c.JSON(200, APIResponse[any]{Code: BadRequest, Message: "该账号正在整理中，请稍后再试", Data: map[string]any{"busy": busySkipped}})
		return
	}
	c.JSON(200, APIResponse[any]{Code: Success, Message: "已开始整理", Data: map[string]any{"started": started, "busy": busySkipped}})
}

// startAutoOrganizeInBackground 在后台 goroutine 中异步执行一次整理：
// 运行使用独立 context（不随 HTTP 请求取消，避免请求超时/断连导致整理中断），
// 完成后写回配置的 last_run_at / last_result 供前端轮询展示。
// 返回 false 表示该账号已有整理在运行（防重入，跳过本次触发）。
func startAutoOrganizeInBackground(cfg models.AutoOrganizeConfig) bool {
	autoOrganizeRunMu.Lock()
	if autoOrganizeBusy[cfg.AccountID] {
		autoOrganizeRunMu.Unlock()
		return false
	}
	autoOrganizeBusy[cfg.AccountID] = true
	autoOrganizeRunMu.Unlock()

	go func(cfg models.AutoOrganizeConfig) {
		defer func() {
			if r := recover(); r != nil {
				helpers.AppLogger.Errorf("云盘自动整理账号 %d 执行异常：%v", cfg.AccountID, r)
			}
			autoOrganizeRunMu.Lock()
			delete(autoOrganizeBusy, cfg.AccountID)
			autoOrganizeRunMu.Unlock()
		}()
		result := moviepilot.RunAutoOrganize(context.Background(), &cfg)
		if len(result.Details) > 0 {
			helpers.AppLogger.Infof("云盘自动整理完成（账号 %d）：成功 %d / 识别失败 %d / 失败 %d", cfg.AccountID, result.Organized, result.Unrecognized, result.Failed)
		}
	}(cfg)
	return true
}
