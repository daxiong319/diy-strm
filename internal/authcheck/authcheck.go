package authcheck

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/notificationmanager"
)

// displayAccountName 账号展示名：优先备注名，回退网盘用户名
func displayAccountName(account *models.Account) string {
	if account.Name != "" {
		return account.Name
	}
	return account.Username
}

// Status 单个网盘账号的授权检测结果
type Status struct {
	Valid     bool   `json:"valid"`       // 本次检测授权是否有效
	CheckedAt int64  `json:"checked_at"`  // 检测时间（unix 秒，0 表示从未检测）
	Detail    string `json:"detail"`      // 失效原因 / 成功详情
	Notified  bool   `json:"notified"`    // 当前失效状态是否已通知过
	Checking  bool   `json:"checking"`    // 是否正在检测（仅内存瞬时状态，不持久化语义）
}

// store 全部账号的授权状态缓存（accountID → Status）
var store = struct {
	sync.RWMutex
	m map[uint]Status
}{m: map[uint]Status{}}

// checking 防止多入口（定时任务/页面加载/手动按钮）并发全量检测
var checking int32

const (
	// perCheckTimeout 单账号轻量校验超时
	perCheckTimeout = 10 * time.Second
	// cacheTTL 非强制模式下跳过该时间内已检测过的账号
	cacheTTL = 5 * time.Minute
	// renotifyInterval 失效状态持续存在时，重复提醒的最小间隔
	renotifyInterval = 6 * time.Hour
	// maxWorkers 并发检测的账号数上限
	maxWorkers = 4
)

// checkOne 对单个账号做一次轻量授权校验（仅验证凭证可用，不产生副作用写入）
func checkOne(account *models.Account) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), perCheckTimeout)
	defer cancel()

	switch account.SourceType {
	case models.SourceType115:
		if _, err := account.Get115Client().UserInfo(); err != nil {
			return false, err.Error()
		}
	case models.SourceType123:
		if _, err := account.Get123Client().GetUserInfo(ctx); err != nil {
			return false, err.Error()
		}
	case models.SourceTypePan139:
		if _, err := account.GetPan139Client().GetFiles(ctx, "root"); err != nil {
			return false, err.Error()
		}
	case models.SourceTypeGuangYaPan:
		if _, err := account.GetGuangYaPanClient().GetFiles(ctx, ""); err != nil {
			return false, err.Error()
		}
	case models.SourceTypeBaiduPan:
		if _, err := account.GetBaiDuPanClient().GetUserInfo(ctx); err != nil {
			return false, err.Error()
		}
	case models.SourceTypeOpenList:
		if _, err := account.GetOpenListClient().GetUserInfo(account.Token); err != nil {
			return false, err.Error()
		}
	default:
		return false, "不支持的账号类型：" + string(account.SourceType)
	}
	return true, ""
}

// sendAuthNotification 发送授权状态变化通知到已配置的通知渠道
func sendAuthNotification(account *models.Account, valid bool, detail string) {
	title := "✅ 网盘账号授权已恢复"
	if !valid {
		title = "⚠️ 网盘账号授权已失效"
	}
	content := "账号：#" + strconv.Itoa(int(account.ID)) + " " + displayAccountName(account) +
		"\n类型：" + string(account.SourceType)
	if !valid && detail != "" {
		content += "\n原因：" + detail
	}
	content += "\n请到「网盘账号管理」重新授权\n⏰ 时间：" + time.Now().Format("2006-01-02 15:04:05")

	notif := &models.Notification{
		Type:      models.AccountAuthInvalid,
		Title:     title,
		Content:   content,
		Timestamp: time.Now(),
		Priority:  models.HighPriority,
	}
	if notificationmanager.GlobalEnhancedNotificationManager == nil {
		return
	}
	if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(context.Background(), notif); err != nil {
		helpers.AppLogger.Errorf("发送网盘账号授权通知失败：%v", err)
	}
}

// CheckAll 检测全部账号的授权有效性。
// force=false 时跳过 cacheTTL 内已检测过的账号；返回本次检测后的全量状态。
// 状态由「有效」转为「失效」（或首次检测即失效）时发送通知；
// 失效持续期间每 renotifyInterval 最多提醒一次；恢复时发送恢复通知。
func CheckAll(force bool) map[uint]Status {
	if !atomic.CompareAndSwapInt32(&checking, 0, 1) {
		helpers.AppLogger.Debug("账号授权检测已在运行，跳过本次触发")
		return GetAll()
	}
	defer atomic.StoreInt32(&checking, 0)

	accounts, err := models.GetAllAccount()
	if err != nil {
		helpers.AppLogger.Errorf("账号授权检测获取账号列表失败：%v", err)
		return GetAll()
	}

	type result struct {
		id     uint
		valid  bool
		detail string
	}
	results := make(chan result, len(accounts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxWorkers)

	now := time.Now().Unix()
	for i := range accounts {
		account := &accounts[i]
		if account.Token == "" {
			// 未授权账号不检测（前端已单独展示未授权状态）
			continue
		}
		if prev, ok := getStatus(account.ID); ok && !force && prev.CheckedAt > 0 && now-prev.CheckedAt < int64(cacheTTL.Seconds()) {
			continue
		}
		// 标记检测中，便于前端展示瞬时状态
		markChecking(account.ID, true)
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() {
				<-sem
				markChecking(account.ID, false)
			}()
			defer func() {
				if r := recover(); r != nil {
					helpers.AppLogger.Errorf("账号 %d 授权检测 panic：%v", account.ID, r)
					results <- result{id: account.ID, valid: false, detail: "检测异常"}
				}
			}()
			valid, detail := checkOne(account)
			results <- result{id: account.ID, valid: valid, detail: detail}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		applyResult(res.id, res.valid, res.detail)
	}
	return GetAll()
}

// applyResult 写入单个检测结果并处理状态变化通知
func applyResult(accountID uint, valid bool, detail string) {
	account, err := models.GetAccountById(accountID)
	if err != nil || account == nil {
		return
	}

	store.Lock()
	prev, hadPrev := store.m[accountID]
	store.m[accountID] = Status{
		Valid:     valid,
		CheckedAt: time.Now().Unix(),
		Detail:    detail,
		Notified:  prev.Notified,
	}
	store.Unlock()

	if valid {
		// 仅从「已知的失效状态」恢复时通知，避免首次检测即有效也发通知
		if hadPrev && prev.CheckedAt > 0 && !prev.Valid {
			helpers.AppLogger.Infof("账号 %d（%s）授权已恢复", account.ID, displayAccountName(account))
			sendAuthNotification(account, true, "")
		}
		return
	}

	// 失效：首次失效立即通知；持续失效按 renotifyInterval 节流
	now := time.Now()
	shouldNotify := true
	if hadPrev && !prev.Valid && prev.Notified && now.Unix()-prev.CheckedAt < int64(renotifyInterval.Seconds()) {
		shouldNotify = false
	}
	helpers.AppLogger.Warnf("账号 %d（%s）授权检测失效：%s", account.ID, displayAccountName(account), detail)
	if shouldNotify {
		sendAuthNotification(account, false, detail)
		store.Lock()
		if s, ok := store.m[accountID]; ok {
			s.Notified = true
			store.m[accountID] = s
		}
		store.Unlock()
	}
}

// markChecking 更新账号的瞬时「检测中」标记
func markChecking(accountID uint, v bool) {
	store.Lock()
	defer store.Unlock()
	s := store.m[accountID]
	s.Checking = v
	store.m[accountID] = s
}

func getStatus(accountID uint) (Status, bool) {
	store.RLock()
	defer store.RUnlock()
	s, ok := store.m[accountID]
	return s, ok
}

// GetAll 返回全部账号授权状态的副本
func GetAll() map[uint]Status {
	store.RLock()
	defer store.RUnlock()
	out := make(map[uint]Status, len(store.m))
	for id, s := range store.m {
		out[id] = s
	}
	return out
}
