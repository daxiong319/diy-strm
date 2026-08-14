package controllers

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/notificationmanager"
	"diy-strm/internal/synccron"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// checkAndExtractSingleParam 检查并提取单个任务参数
// args： 参数列表
// 返回错误信息（如果参数格式错误）和提取的任务 ID（如果成功）
// 如果没有参数或参数为空，返回空错误和 0。
func checkAndExtractSingleParam(args []string) (string, uint) {
	if len(args) > 0 && args[0] != "" {
		param := args[0]
		// 检查参数是否以 # 开头且长度大于 1。
		if !(len(param) > 1 && param[0] == '#') {
			return "❌ 参数格式错误，请使用 #数字 格式", 0
		}
		// 尝试将参数转换为 uint。
		numStr := param[1:]
		id, parseErr := strconv.ParseUint(numStr, 10, 32)
		if parseErr != nil {
			return "❌ 参数格式错误，请使用 #数字 格式", 0
		}
		return "", uint(id)
	}
	return "", 0
}

// checkAndExtractMoreParam 检查并提取多个任务参数
// args： 参数列表
// 返回错误信息（如果参数格式错误）和提取的任务 ID 列表（如果成功）。
func checkAndExtractMoreParam(args []string) (string, []uint) {
	var taskIDs []uint
	for _, arg := range args {
		if arg != "" {
			// 检查参数是否以 # 开头且长度大于 1。
			if !(len(arg) > 1 && arg[0] == '#') {
				return "❌ 参数格式错误，请使用 #数字 #数字 格式", nil
			}
			// 尝试将参数转换为 uint。
			numStr := arg[1:]
			id, parseErr := strconv.ParseUint(numStr, 10, 32)
			if parseErr != nil {
				return "❌ 参数格式错误，请使用 #数字 #数字 格式", nil
			}
			taskIDs = append(taskIDs, uint(id))
		}
	}
	return "", taskIDs
}

// runStrmTask 执行 STRM 同步任务
// args： 可选参数，传入同步目录 ID 时只同步指定目录
// isFullSync： 是否执行全量同步
func runStrmTask(taskID uint, isFullSync bool) string {
	go runStrmTaskSync(taskID, isFullSync)
	// 返回开始执行的消息
	if isFullSync {
		return "🔄 开始执行全量 STRM 同步"
	}
	return "🔄 开始执行增量 STRM 同步"
}

func runStrmTaskSync(taskID uint, isFullSync bool) {
	// 先返回开始执行的消息
	taskIDs := []uint{}
	var title, content string

	// 设置通知信息
	if isFullSync {
		title = "✅ 全量 STRM 同步完成"
		content = "所有全量 STRM 同步任务已执行完毕"
	} else {
		title = "✅ 增量 STRM 同步完成"
		content = "所有增量 STRM 同步任务已执行完毕"
	}

	// 检查是否传入了目录 ID。
	if taskID > 0 {
		// 获取指定同步目录
		syncPath := models.GetSyncPathById(taskID)
		if syncPath != nil {
			// 如果是全量同步，设置标志
			if isFullSync {
				syncPath.SetIsFullSync(true)
			}
			// 同步指定目录
			taskObj := &synccron.NewSyncTask{
				ID:           syncPath.ID,
				SourcePath:   "",
				SourcePathId: "",
				TargetPath:   "",
				AccountId:    syncPath.AccountId,
				IsFile:       false,
				TaskType:     synccron.SyncTaskTypeStrm,
				SourceType:   syncPath.SourceType,
			}
			synccron.AddNewSyncTask(taskObj)
			taskIDs = []uint{taskID}
			// 设置通知内容
			if isFullSync {
				content = "目录：" + syncPath.RemotePath + "，全量 STRM 同步任务已执行完毕"
			} else {
				content = "目录：" + syncPath.RemotePath + "，增量 STRM 同步任务已执行完毕"
			}
		}

	} else {
		// 获取所有同步目录
		allSyncPaths, _ := models.GetSyncPathList(1, 10000000, false, "")
		for _, syncPath := range allSyncPaths {
			// 全量同步时设置标志
			if isFullSync {
				syncPath.SetIsFullSync(true)
			}
			// 同步目录
			taskObj := &synccron.NewSyncTask{
				ID:           syncPath.ID,
				SourcePath:   "",
				SourcePathId: "",
				TargetPath:   "",
				AccountId:    syncPath.AccountId,
				IsFile:       false,
				TaskType:     synccron.SyncTaskTypeStrm,
				SourceType:   syncPath.SourceType,
			}
			synccron.AddNewSyncTask(taskObj)
			taskIDs = append(taskIDs, syncPath.ID)
		}
		// 设置通知内容
		if isFullSync {
			content = "目录：全部，全量 STRM 同步任务已执行完毕"
		} else {
			content = "目录：全部，增量 STRM 同步任务已执行完毕"
		}
	}

	// 等待所有任务执行完成
	time.Sleep(2 * time.Second) // 等待任务队列初始化

	// 监控任务的状态
	waitForTasksCompletion(taskIDs, synccron.SyncTaskTypeStrm)

	// 所有任务执行完成，发送通知
	ctx := context.Background()
	notif := &models.Notification{
		Type:      models.SystemAlert,
		Title:     title,
		Content:   content,
		Timestamp: time.Now(),
		Priority:  models.NormalPriority,
	}
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		notificationmanager.GlobalEnhancedNotificationManager.SendNotification(ctx, notif)
	}
}

// runScrapeTask 执行刮削任务并在完成后发送通知
// taskID： 刮削目录 ID，传入 0 时执行所有目录。
func runScrapeTask(taskID uint) string {
	go runScrapeTaskSync(taskID)
	return "🔄 开始执行刮削任务"
}

func runScrapeTaskSync(taskID uint) {
	// 先返回开始执行的消息
	taskIDs := []uint{}
	var title, content string

	// 设置通知信息
	title = "✅ 刮削任务完成"
	content = "所有刮削任务已执行完毕"

	// 检查是否传入了目录 ID。
	if taskID > 0 {
		// 获取指定刮削目录
		scrapePath := models.GetScrapePathByID(taskID)
		if scrapePath != nil {
			// 执行刮削任务
			taskObj := &synccron.NewSyncTask{
				ID:           scrapePath.ID,
				SourcePath:   "",
				SourcePathId: "",
				TargetPath:   "",
				AccountId:    scrapePath.AccountId,
				IsFile:       false,
				TaskType:     synccron.SyncTaskTypeScrape,
				SourceType:   scrapePath.SourceType,
			}
			synccron.AddNewSyncTask(taskObj)
			taskIDs = []uint{taskID}
			// 设置通知内容
			content = "目录：" + scrapePath.SourcePath + "，刮削任务已执行完毕"
		}

	} else {
		// 获取所有刮削目录
		allScrapePaths := models.GetScrapePathes("")
		for _, scrapePath := range allScrapePaths {
			// 执行刮削任务
			taskObj := &synccron.NewSyncTask{
				ID:           scrapePath.ID,
				SourcePath:   "",
				SourcePathId: "",
				TargetPath:   "",
				AccountId:    scrapePath.AccountId,
				IsFile:       false,
				TaskType:     synccron.SyncTaskTypeScrape,
				SourceType:   scrapePath.SourceType,
			}
			synccron.AddNewSyncTask(taskObj)
			taskIDs = append(taskIDs, scrapePath.ID)
		}
		// 设置通知内容
		content = "目录：全部，刮削任务已执行完毕"
	}

	// 等待所有任务执行完成
	time.Sleep(2 * time.Second) // 等待任务队列初始化

	// 监控任务的状态
	waitForTasksCompletion(taskIDs, synccron.SyncTaskTypeScrape)

	// 所有任务执行完成，发送通知
	ctx := context.Background()
	notif := &models.Notification{
		Type:      models.SystemAlert,
		Title:     title,
		Content:   content,
		Timestamp: time.Now(),
		Priority:  models.NormalPriority,
	}
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		notificationmanager.GlobalEnhancedNotificationManager.SendNotification(ctx, notif)
	}
}

// SyncStrmInc 执行增量 STRM 同步并在完成后发送通知
// args： 可选参数，传入同步目录 ID 时只同步指定目录
func SyncStrmInc(args []string) helpers.CommandResponse {
	if errMsg, _ := checkAndExtractSingleParam(args); errMsg != "" {
		return helpers.CommandResponse{Text: errMsg}
	}
	_, taskID := checkAndExtractSingleParam(args)
	return helpers.CommandResponse{Text: runStrmTask(taskID, false)}
}

// SyncStrnFull 执行全量 STRM 同步并在完成后发送通知
// args： 可选参数，传入同步目录 ID 时只同步指定目录
func SyncStrnFull(args []string) helpers.CommandResponse {
	if errMsg, _ := checkAndExtractSingleParam(args); errMsg != "" {
		return helpers.CommandResponse{Text: errMsg}
	}
	_, taskID := checkAndExtractSingleParam(args)
	return helpers.CommandResponse{Text: runStrmTask(taskID, true)}
}

// Scrape 执行刮削任务并在完成后发送通知
// args： 可选参数，传入刮削目录 ID 时只执行指定目录的刮削
func Scrape(args []string) helpers.CommandResponse {
	if errMsg, _ := checkAndExtractSingleParam(args); errMsg != "" {
		return helpers.CommandResponse{Text: errMsg}
	}
	_, taskID := checkAndExtractSingleParam(args)
	return helpers.CommandResponse{Text: runScrapeTask(taskID)}
}

// waitForTasksCompletion 等待指定任务完成
func waitForTasksCompletion(taskIDs []uint, taskType synccron.SyncTaskType) {
	if len(taskIDs) == 0 {
		return
	}
	allCompleted := false
	for !allCompleted {
		time.Sleep(5 * time.Second)
		allCompleted = true
		for _, taskID := range taskIDs {
			status := synccron.CheckNewTaskStatus(taskID, taskType)
			if status == synccron.TaskStatusWaiting || status == synccron.TaskStatusRunning {
				allCompleted = false
				break
			}
		}
	}
}

// runScrapeThenStrm 先执行刮削任务，完成后再执行同步任务
// extractedIDs： 包含刮削目录 ID 和同步目录 ID 的数组，分别代表刮削目录 ID 和同步目录 ID
// 如果参数为 0，则执行所有目录的操作。
func runScrapeThenStrm(extractedIDs []uint) string {
	// 先返回开始执行的消息
	go func() {
		// 执行刮削任务
		{
			// 调用 runScrapeTask 执行刮削任务
			var scrapeTaskID uint
			if len(extractedIDs) > 0 && extractedIDs[0] > 0 {
				scrapeTaskID = extractedIDs[0]
			}
			runScrapeTaskSync(scrapeTaskID)

			// 等待上传下载任务完成
			time.Sleep(15 * time.Second)
		}

		// 执行同步任务
		{
			// 调用 runStrmTask 执行同步任务
			var syncTaskID uint
			if len(extractedIDs) > 1 && extractedIDs[1] > 0 {
				syncTaskID = extractedIDs[1]
			}
			runStrmTaskSync(syncTaskID, false)
		}

		// 发送完成通知
		ctx := context.Background()
		notif := &models.Notification{
			Type:      models.SystemAlert,
			Title:     "✅ 任务序列执行完成",
			Content:   "所有任务已全部执行完毕",
			Timestamp: time.Now(),
			Priority:  models.NormalPriority,
		}
		if notificationmanager.GlobalEnhancedNotificationManager != nil {
			notificationmanager.GlobalEnhancedNotificationManager.SendNotification(ctx, notif)
		}
	}()

	return "🔄 开始执行任务序列"
}

// runStrmThenScrape 先执行同步任务，完成后再执行刮削任务
// extractedIDs： 包含同步目录 ID 和刮削目录 ID 的数组，分别代表同步目录 ID 和刮削目录 ID
// 如果参数为 0，则执行所有目录的操作。
func runStrmThenScrape(extractedIDs []uint) string {
	// 先返回开始执行的消息
	go func() {
		// 执行同步任务
		{
			// 调用 runStrmTask 执行同步任务
			var syncTaskID uint
			if len(extractedIDs) > 0 && extractedIDs[0] > 0 {
				syncTaskID = extractedIDs[0]
			}
			runStrmTaskSync(syncTaskID, false)

			// 等待上传下载任务完成
			time.Sleep(15 * time.Second)
		}

		// 执行刮削任务
		{
			var hasNewScrapeFiles bool

			// 检查是否有新文件
			if len(extractedIDs) == 0 || extractedIDs[1] == 0 {
				// 检查所有刮削目录是否有新文件
				allScrapePaths := models.GetScrapePathes("")
				for _, scrapePath := range allScrapePaths {
					newScrapeFilesCount := models.GetScannedScrapeMediaFilesTotal(scrapePath.ID, scrapePath.MediaType)
					if newScrapeFilesCount > 0 {
						hasNewScrapeFiles = true
						break
					}
				}
			} else {
				// 检查指定刮削目录是否有新文件
				taskID := extractedIDs[1]
				scrapePath := models.GetScrapePathByID(taskID)
				if scrapePath != nil {
					newScrapeFilesCount := models.GetScannedScrapeMediaFilesTotal(scrapePath.ID, scrapePath.MediaType)
					if newScrapeFilesCount > 0 {
						hasNewScrapeFiles = true
					}
				}
			}

			// 执行刮削任务
			var scrapeTaskID uint
			if len(extractedIDs) > 1 && extractedIDs[1] > 0 {
				scrapeTaskID = extractedIDs[1]
			}
			runScrapeTaskSync(scrapeTaskID)

			// 刮削任务完成后，如果有新文件，触发 Emby 媒体库刷新。
			if hasNewScrapeFiles {
				var refreshIDs []uint
				// 使用同步任务的 ID（第一个任务）。
				if len(extractedIDs) > 0 && extractedIDs[0] > 0 {
					// 使用同步任务的 ID。
					syncPath := models.GetSyncPathById(extractedIDs[0])
					if syncPath != nil {
						refreshIDs = append(refreshIDs, extractedIDs[0])
						helpers.AppLogger.Infof("添加同步目录到 Emby 刷新列表：%s（ID：%d）", syncPath.RemotePath, extractedIDs[0])
					}
				} else if len(extractedIDs) == 0 || extractedIDs[0] == 0 {
					// 如果是全部同步，使用所有同步目录的 ID。
					allSyncPaths, _ := models.GetSyncPathList(1, 10000000, true, "")
					for _, syncPath := range allSyncPaths {
						refreshIDs = append(refreshIDs, syncPath.ID)
						helpers.AppLogger.Infof("添加同步目录到 Emby 刷新列表：%s（ID：%d）", syncPath.RemotePath, syncPath.ID)
					}
				}

				// 如果有需要刷新的目录，等待 30 秒后执行刷新。
				if len(refreshIDs) > 0 {
					go func(ids []uint) {
						for _, taskID := range ids {
							if err := models.RequestEmbyLibraryRefreshBySyncPathId(taskID); err != nil {
								helpers.AppLogger.Errorf("提交 Emby 媒体库刷新任务失败，同步目录 ID：%d，错误：%v", taskID, err)
							}
						}
					}(refreshIDs)
				}
			}
		}

		// 发送完成通知
		ctx := context.Background()
		notif := &models.Notification{
			Type:      models.SystemAlert,
			Title:     "✅ 任务序列执行完成",
			Content:   "所有任务已全部执行完毕",
			Timestamp: time.Now(),
			Priority:  models.NormalPriority,
		}
		if notificationmanager.GlobalEnhancedNotificationManager != nil {
			notificationmanager.GlobalEnhancedNotificationManager.SendNotification(ctx, notif)
		}
	}()

	return "🔄 开始执行任务序列"
}

// ScrapeThenStrm 先执行刮削任务，完成后再执行同步任务
// args： 参数格式为 #数字 #数字，分别代表刮削目录 ID 和同步目录 ID
// 如果参数为 0，则执行所有目录的操作。
func ScrapeThenStrm(args []string) string {
	// 检查参数格式
	if errMsg, _ := checkAndExtractMoreParam(args); errMsg != "" {
		return errMsg
	}

	// 解析参数
	_, extractedIDs := checkAndExtractMoreParam(args)

	// 调用 runScrapeThenStrm 执行任务序列
	return runScrapeThenStrm(extractedIDs)
}

// StrmThenScrape 先执行同步任务，完成后再执行刮削任务
// args： 参数格式为 #数字 #数字，分别代表同步目录 ID 和刮削目录 ID
// 如果参数为 0，则执行所有目录的操作。
func StrmThenScrape(args []string) string {
	// 检查参数格式
	if errMsg, _ := checkAndExtractMoreParam(args); errMsg != "" {
		return errMsg
	}

	// 解析参数
	_, extractedIDs := checkAndExtractMoreParam(args)

	// 调用 runStrmThenScrape 执行任务序列
	return runStrmThenScrape(extractedIDs)
}

// ParseStrmPathArgs 解析 get_strm_path 命令的参数
func ParseStrmPathArgs(args []string) (int, int) {
	page := 1
	pageSize := 20

	// 解析参数
	if len(args) >= 1 && args[0] != "" {
		if num, err := strconv.Atoi(args[0][1:]); err == nil && num > 0 {
			page = num
		}
	}

	if len(args) >= 2 && args[1] != "" {
		if num, err := strconv.Atoi(args[1][1:]); err == nil && num > 0 {
			pageSize = num
		}
	}

	return page, pageSize
}

// getStrmPath 获取同步路径列表
// args： 可选参数，传入页码和每页数量，格式为 #页码 #每页数量
func getStrmPath(args []string) helpers.CommandResponse {
	page, pageSize := ParseStrmPathArgs(args)

	// 获取同步路径列表
	syncPaths, total := models.GetSyncPathList(page, pageSize, false, "")

	// 格式化输出
	result := "📋 STRM 同步路径列表\n"
	result += fmt.Sprintf("第 %d 页，共 %d 条记录\n\n", page, total)

	for _, sp := range syncPaths {
		status := synccron.CheckNewTaskStatus(sp.ID, synccron.SyncTaskTypeStrm)
		statusStr := "⏸️ 空闲"
		switch status {
		case synccron.TaskStatusRunning:
			statusStr = "🔄 运行中"
		case synccron.TaskStatusWaiting:
			statusStr = "⏳ 等待中"
		}

		result += fmt.Sprintf("  ID：#%d\n", sp.ID)
		result += fmt.Sprintf("  原始路径：%s\n", sp.RemotePath)
		result += fmt.Sprintf("  目标路径：%s\n", sp.LocalPath)
		result += fmt.Sprintf("  状态：%s\n", statusStr)
		result += fmt.Sprintf("  来源：%s\n", sp.SourceType)
		result += fmt.Sprintf("  最后同步：%s\n\n", time.Unix(sp.UpdatedAt, 0).Format("2006-01-02 15:04"))
	}

	// 构建内联键盘
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, sp := range syncPaths {
		row := tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(
				fmt.Sprintf("#%d（增量同步）", sp.ID),
				fmt.Sprintf("strm_inc #%d", sp.ID),
			),
			tgbotapi.NewInlineKeyboardButtonData(
				fmt.Sprintf("#%d（全量同步）", sp.ID),
				fmt.Sprintf("strm_sync #%d", sp.ID),
			),
		)
		rows = append(rows, row)
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	return helpers.CommandResponse{
		Text:        result,
		ReplyMarkup: keyboard,
	}
}

// ParseScrapePathArgs 解析 get_scrape_path 命令的参数
func ParseScrapePathArgs(args []string) string {
	sourceType := ""

	// 解析参数
	if len(args) >= 1 && args[0] != "" {
		sourceType = args[0][1:]
	}

	return sourceType
}

// getScrapePath 获取刮削路径列表
// args： 可选参数，传入来源类型，格式为 #来源类型
func getScrapePath(args []string) helpers.CommandResponse {
	sourceType := ParseScrapePathArgs(args)

	// 获取刮削路径列表
	scrapePaths := models.GetScrapePathes(sourceType)

	// 格式化输出
	result := "🧹 刮削路径列表\n"
	result += fmt.Sprintf("共 %d 条记录\n\n", len(scrapePaths))

	for _, sp := range scrapePaths {
		status := synccron.CheckNewTaskStatus(sp.ID, synccron.SyncTaskTypeScrape)
		statusStr := "⏸️ 空闲"
		switch status {
		case synccron.TaskStatusRunning:
			statusStr = "🔄 运行中"
		case synccron.TaskStatusWaiting:
			statusStr = "⏳ 等待中"
		}

		result += fmt.Sprintf("  ID：#%d\n", sp.ID)
		result += fmt.Sprintf("  原始路径：%s\n", sp.SourcePath)
		result += fmt.Sprintf("  目标路径：%s\n", sp.DestPath)
		result += fmt.Sprintf("  状态：%s\n", statusStr)
		result += fmt.Sprintf("  来源：%s\n", sp.SourceType)
		result += fmt.Sprintf("  媒体类型：%s\n", sp.MediaType)
		result += fmt.Sprintf("  最后刮削：%s\n\n", time.Unix(sp.UpdatedAt, 0).Format("2006-01-02 15:04"))
	}

	// 构建内联键盘
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, sp := range scrapePaths {
		button := tgbotapi.NewInlineKeyboardButtonData(
			fmt.Sprintf("#%d（执行刮削）", sp.ID),
			fmt.Sprintf("scrape #%d", sp.ID),
		)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(button))
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	return helpers.CommandResponse{
		Text:        result,
		ReplyMarkup: keyboard,
	}
}

func StartListenTelegramBot() {
	mgr := notificationmanager.GlobalEnhancedNotificationManager

	myCommands := map[string]func([]string) helpers.CommandResponse{
		"strm_inc":        SyncStrmInc,
		"strm_sync":       SyncStrnFull,
		"scrape":          Scrape,
		"get_strm_path":   getStrmPath,
		"get_scrape_path": getScrapePath,
		"订阅":             handleSubCommand,
		// "scrape_strm": ScrapeThenStrm,
		// "strm_scrape": StrmThenScrape,
	}

	mgr.RegisterTelegramCommands(myCommands)
	mgr.RegisterTelegramTextHandler(handleTelegramShareSave)
	mgr.StartAll()
}

const defaultPan123SaveDir = "/"

var (
	pan123ShareLinkPattern  = regexp.MustCompile(`(?:https?://)?(?:[a-z0-9\-]+\.)*(?:123pan\.com|123pan\.cn|123684\.com|share\.123865\.com)/(?:s|123pan)/([A-Za-z0-9\-_]{6,})(?:\.html)?`)
	pan123SharePwdPattern   = regexp.MustCompile(`(?i)(?:提取码\s*[:：]?\s*|\bpwd\s*[:：=]?\s*)([A-Za-z0-9]{4,6})`)
	guangyaShareLinkPattern   = regexp.MustCompile(`(?:https?://)?(?:www\.)?guangyapan\.com/s/([A-Za-z0-9_\-]{6,})`)
	guangYaExtractCodePattern  = regexp.MustCompile(`[?&](?:code|shareCode)=([^&\s]+)`)
)

// parsePan123ShareText 从文本中解析 123 分享链接与提取码
func parsePan123ShareText(text string) (shareKey, sharePwd string) {
	trimmed := strings.TrimSpace(text)
	if m := pan123ShareLinkPattern.FindStringSubmatch(trimmed); m != nil {
		shareKey = m[1]
		if p := pan123SharePwdPattern.FindStringSubmatch(trimmed); p != nil {
			sharePwd = p[1]
		}
	}
	return shareKey, sharePwd
}

// handlePan123ShareSave 处理 123 云盘分享转存
func handlePan123ShareSave(text string, chatID int64) helpers.CommandResponse {
	shareKey, sharePwd := parsePan123ShareText(text)
	if shareKey == "" {
		return helpers.CommandResponse{}
	}
	targetDir := models.GetCloudSaveDirWithDefault(string(models.SourceType123), defaultPan123SaveDir)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	title, total, err := savePan123Share(ctx, shareKey, sharePwd, targetDir)
	if err != nil {
		helpers.AppLogger.Errorf("Telegram 123 转存失败（chatID=%d shareKey=%s 目标目录=%s）：%v", chatID, shareKey, targetDir, err)
		return helpers.CommandResponse{Text: "❌ 转存失败：" + htmlEscape(err.Error())}
	}
	helpers.AppLogger.Infof("Telegram 123 转存成功：chatID=%d shareKey=%s 分享「%s」共 %d 项已转存到 %s", chatID, shareKey, title, total, targetDir)
	return helpers.CommandResponse{Text: fmt.Sprintf("✅ 已转存分享「%s」共 %d 项到 %s", htmlEscape(title), total, htmlEscape(targetDir))}
}

// handleGuangYaShareSave 处理光鸭云盘分享链接转存（www.guangyapan.com/s/{shareId}）
func handleGuangYaShareSave(text string, chatID int64) helpers.CommandResponse {
	trimmed := strings.TrimSpace(text)
	m := guangyaShareLinkPattern.FindStringSubmatch(trimmed)
	if m == nil {
		return helpers.CommandResponse{}
	}
	shareID := m[1]
	shareCode := ""
	if cm := guangYaExtractCodePattern.FindStringSubmatch(trimmed); cm != nil {
		shareCode = cm[1]
	}
	targetDir := models.GetCloudSaveDirWithDefault(string(models.SourceTypeGuangYaPan), "/")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	title, total, err := saveGuangYaShare(ctx, shareID, shareCode, targetDir)
	if err != nil {
		helpers.AppLogger.Errorf("Telegram 光鸭转存失败（chatID=%d shareID=%s 目标目录=%s）：%v", chatID, shareID, targetDir, err)
		return helpers.CommandResponse{Text: "❌ 转存失败：" + htmlEscape(err.Error())}
	}
	helpers.AppLogger.Infof("Telegram 光鸭转存成功：chatID=%d shareID=%s 分享「%s」共 %d 项已转存到 %s", chatID, shareID, title, total, targetDir)
	return helpers.CommandResponse{Text: fmt.Sprintf("✅ 已转存分享「%s」共 %d 项到 %s", htmlEscape(title), total, htmlEscape(targetDir))}
}

const defaultPan139SaveDir = "/影视/待整理"

var pan139ShareLinkPattern = regexp.MustCompile(`(?:shareweb/#/)?w/i/([A-Za-z0-9_\-]{6,})`)

// parsePan139ShareText 从文本中解析 139 分享链接，返回 linkID 与可选目标目录
func parsePan139ShareText(text string) (linkID, saveDir string) {
	trimmed := strings.TrimSpace(text)
	if m := pan139ShareLinkPattern.FindStringSubmatch(trimmed); m != nil {
		linkID = m[1]
		rest := strings.TrimSpace(strings.Replace(trimmed, m[0], " ", 1))
		parts := strings.Fields(rest)
		for _, part := range parts {
			if strings.HasPrefix(part, "/") {
				saveDir = part
				break
			}
		}
		return linkID, saveDir
	}
	if match, _ := regexp.MatchString(`^[A-Za-z0-9_\-]{8,}$`, trimmed); match {
		return trimmed, ""
	}
	return "", ""
}

// handleTelegramShareSave 处理 Telegram 普通文本消息：识别网盘分享链接并转存
// 支持：123 云盘（123pan.com 等域名）、中国移动云盘（139）
func handleTelegramShareSave(text string, chatID int64, defaultDir string) helpers.CommandResponse {
	trimmed := strings.TrimSpace(text)

	if pan123ShareLinkPattern.MatchString(trimmed) {
		return handlePan123ShareSave(text, chatID)
	}
	if guangyaShareLinkPattern.MatchString(trimmed) {
		return handleGuangYaShareSave(text, chatID)
	}

	linkID, saveDir := parsePan139ShareText(text)
	if linkID == "" {
		return helpers.CommandResponse{}
	}
	if strings.TrimSpace(defaultDir) == "" {
		defaultDir = models.GetCloudSaveDirWithDefault(string(models.SourceTypePan139), defaultPan139SaveDir)
	}
	if strings.TrimSpace(saveDir) == "" {
		saveDir = defaultDir
	}
	helpers.AppLogger.Infof("Telegram 转存：收到分享链接 chatID=%d linkID=%s 目标目录=%q（默认目录=%q）", chatID, linkID, saveDir, defaultDir)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	title, total, err := savePan139Share(ctx, linkID, saveDir)
	if err != nil {
		helpers.AppLogger.Errorf("Telegram 转存失败（chatID=%d linkID=%s 目标目录=%q）：%v", chatID, linkID, saveDir, err)
		return helpers.CommandResponse{Text: "❌ " + htmlEscape(err.Error())}
	}
	helpers.AppLogger.Infof("Telegram 转存成功：chatID=%d linkID=%s 分享「%s」共 %d 项已转存到 %s", chatID, linkID, title, total, saveDir)
	return helpers.CommandResponse{Text: fmt.Sprintf("✅ 已转存分享「%s」共 %d 项到 %s", htmlEscape(title), total, htmlEscape(saveDir))}
}


// handleSubCommand TG 频道订阅命令
// 用法：
//   /订阅                    查看全部订阅
//   /订阅 添加 <网盘类型> @频道 关键词... [/目标目录]   新增订阅
//   /订阅 删除 #id           删除订阅
//   /订阅 启用 #id           启用订阅
//   /订阅 禁用 #id           暂停订阅
//   /订阅 测试 @频道         预览频道最近内容（不转存）
// 网盘类型：123 / 光鸭 / 139
func handleSubCommand(args []string) helpers.CommandResponse {
	if len(args) == 0 {
		return listSubsResponse()
	}
	action := args[0]
	rest := args[1:]
	switch action {
	case "添加", "add":
		return addSubResponse(rest)
	case "删除", "del", "rm":
		return delSubResponse(rest)
	case "启用", "on", "enable":
		return toggleSubResponse(rest, true)
	case "禁用", "off", "disable":
		return toggleSubResponse(rest, false)
	case "测试", "test", "preview":
		return testSubResponse(rest)
	case "执行", "run":
		return runSubResponse(rest)
	default:
		return listSubsResponse()
	}
}

func listSubsResponse() helpers.CommandResponse {
	subs, err := models.ListCloudSubscriptions("")
	if err != nil {
		return helpers.CommandResponse{Text: "❌ 查询订阅失败：" + htmlEscape(err.Error())}
	}
	if len(subs) == 0 {
		return helpers.CommandResponse{Text: "📭 暂无频道订阅。\n使用 /订阅 添加 123 @dianying 电影 电视剧 添加吧"}
	}
	var b strings.Builder
	b.WriteString("📋 <b>频道订阅列表</b>\n\n")
	for _, s := range subs {
		status := "✅ 启用"
		if !s.Enabled {
			status = "⏸ 禁用"
		}
		b.WriteString(fmt.Sprintf("#%d %s（%s）\n频道：%s\n关键词：%s\n目标目录：%s\n状态：%s",
			s.ID, htmlEscape(s.ChannelName()), parseSourceTypeName(s.SourceType), htmlEscape(s.ChannelName()), htmlEscape(s.Keywords), htmlEscape(s.TargetDir), status))
		if s.LastRunAt.IsZero() {
			b.WriteString("｜未运行")
		} else {
			b.WriteString(fmt.Sprintf("｜上次运行：%s", s.LastRunAt.Format("01-02 15:04")))
		}
		b.WriteString("\n\n")
	}
	b.WriteString("💡 操作：/订阅 添加 123 @dianying 关键词 /目录｜/订阅 删除 #1｜/订阅 启用 #1")
	return helpers.CommandResponse{Text: b.String()}
}

func addSubResponse(args []string) helpers.CommandResponse {
	if len(args) < 2 {
		return helpers.CommandResponse{Text: "❌ 用法：/订阅 添加 123 @频道 关键词... [/目标目录]" +
			"\n网盘类型：123 / 光鸭 / 139"}
	}
	sourceType := args[0]
	channel := args[1]
	if !ensureSourceTypeValid(sourceType) {
		return helpers.CommandResponse{Text: "❌ 不支持的网盘类型：" + htmlEscape(sourceType) + "（支持：123 / 光鸭 / 139）"}
	}
	if !strings.HasPrefix(channel, "@") {
		return helpers.CommandResponse{Text: "❌ 频道名必须以 @ 开头，例如 @dianying"}
	}
	targetDir := "/"
	var kwParts []string
	for _, a := range args[2:] {
		if strings.HasPrefix(a, "/") {
			targetDir = a
			continue
		}
		kwParts = append(kwParts, a)
	}
	if len(kwParts) == 0 {
		return helpers.CommandResponse{Text: "❌ 至少需要一个关键词，例如：/订阅 添加 123 @dianying 4K"}
	}
	sub := models.CloudSubscription{
		SourceType: sourceType,
		Channel:    channel,
		Keywords:   strings.Join(kwParts, " "),
		TargetDir:  targetDir,
		Enabled:    true,
	}
	if err := models.CreateCloudSubscription(&sub); err != nil {
		return helpers.CommandResponse{Text: "❌ 创建订阅失败：" + htmlEscape(err.Error())}
	}
	return helpers.CommandResponse{Text: fmt.Sprintf("✅ 订阅已创建：#%d\n频道：%s\n关键词：%s\n目标目录：%s\n匹配到的分享将自动转存到%s",
		sub.ID, htmlEscape(channel), htmlEscape(sub.Keywords), htmlEscape(targetDir), parseSourceTypeName(sourceType))}
}

func delSubResponse(args []string) helpers.CommandResponse {
	_, id := checkAndExtractSingleParam(args)
	if id == 0 {
		return helpers.CommandResponse{Text: "❌ 用法：/订阅 删除 #1"}
	}
	if err := models.DeleteCloudSubscription(id); err != nil {
		return helpers.CommandResponse{Text: "❌ 删除失败：" + htmlEscape(err.Error())}
	}
	return helpers.CommandResponse{Text: fmt.Sprintf("✅ 订阅 #%d 已删除", id)}
}

func toggleSubResponse(args []string, enabled bool) helpers.CommandResponse {
	_, id := checkAndExtractSingleParam(args)
	if id == 0 {
		action := "启用"
		if !enabled {
			action = "禁用"
		}
		return helpers.CommandResponse{Text: fmt.Sprintf("❌ 用法：/订阅 %s #1", action)}
	}
	if err := models.UpdateCloudSubscriptionEnabled(id, enabled); err != nil {
		return helpers.CommandResponse{Text: "❌ 更新失败：" + htmlEscape(err.Error())}
	}
	state := "已启用"
	if !enabled {
		state = "已禁用"
	}
	return helpers.CommandResponse{Text: fmt.Sprintf("✅ 订阅 #%d 已%s", id, state)}
}

func testSubResponse(args []string) helpers.CommandResponse {
	if len(args) == 0 {
		return helpers.CommandResponse{Text: "❌ 用法：/订阅 测试 @频道"}
	}
	channel := args[0]
	posts, err := PreviewChannel(channel, 5)
	if err != nil {
		return helpers.CommandResponse{Text: "❌ 抓取失败：" + htmlEscape(err.Error())}
	}
	if len(posts) == 0 {
		return helpers.CommandResponse{Text: fmt.Sprintf("📭 频道 %s 暂无内容", htmlEscape(channel))}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔍 <b>频道 %s 最近 %d 条</b>\n\n", htmlEscape(channel), len(posts)))
	for _, p := range posts {
		linkDesc := "（无链接）"
		types := make(map[string]int)
		for _, l := range p.Links {
			types[parseSourceTypeName(l.Type)]++
		}
		if len(types) > 0 {
			var parts []string
			for k, v := range types {
				parts = append(parts, fmt.Sprintf("%s×%d", k, v))
			}
			linkDesc = strings.Join(parts, " ")
		}
		b.WriteString(fmt.Sprintf("• <code>%s</code> %s｜%s\n%s\n\n",
			p.PostID, p.Time.Format("01-02 15:04"), linkDesc, htmlEscape(truncateRunes(p.Text, 80))))
	}
	return helpers.CommandResponse{Text: b.String()}
}

func runSubResponse(args []string) helpers.CommandResponse {
	_, id := checkAndExtractSingleParam(args)
	if id == 0 {
		return helpers.CommandResponse{Text: "❌ 用法：/订阅 执行 #1"}
	}
	sub, err := models.GetCloudSubscription(id)
	if err != nil {
		return helpers.CommandResponse{Text: "❌ 订阅不存在：#" + strconv.FormatUint(uint64(id), 10)}
	}
	msg, _ := RunSubscriptionOnce(sub)
	return helpers.CommandResponse{Text: msg}
}

// truncateRunes 按字符数截断（避免按字节截断破坏中文）
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// htmlEscape 转义 Telegram HTML 消息中的特殊字符
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
