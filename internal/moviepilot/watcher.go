package moviepilot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/notification"
	"diy-strm/internal/notificationmanager"
	"diy-strm/internal/qbittorrent"
	"diy-strm/internal/synccron"
)

// 全局上传串行队列
var uploadQueue = make(chan *models.MoviePilotUploadTask, 32)
var watcherRunning atomicBool

// 源目录为空的"等待落盘"策略：MP 下载完成信号可能早于文件真正落盘（qb 校验/搬移中），
// 空源目录不判失败而是置为等待，由轮询自愈扫描在文件到位后自动重试入队；
// 等待超过上限仍未等到文件才终态失败，避免整批剧集因文件晚到而漏传，也避免任务永久挂起。
const (
	emptySourceWaitLimit = 48 * time.Hour // 等待文件落盘的总时限，超过仍未等到则终态失败
)

// queued 已入队待执行的上传任务（内存去重）：启动恢复/自愈/重试多处都可能入队同一任务，
// 不做去重会导致同一任务在串行队列里排多份、重复建批次重复上传。
var (
	queuedMu sync.Mutex
	queued   = map[uint]struct{}{}
)

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (b *atomicBool) Set(v bool) {
	b.mu.Lock()
	b.v = v
	b.mu.Unlock()
}

func (b *atomicBool) Get() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}

// CompareAndSwap 原子地「检查并占位」：Get+Set 分开调用存在间隙，并发 Start 会启动两套 worker
func (b *atomicBool) CompareAndSwap(oldV, newV bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.v != oldV {
		return false
	}
	b.v = newV
	return true
}

// enqueueUploadTask 入队上传任务：队列满时返回 false 而不是无限阻塞
// （HTTP 重试路径在请求协程里直接 send，队列满且 worker 忙时会一直阻塞到 HTTP 超时）。
// 同一任务已在队列中时直接返回 true（幂等），避免多处入队导致重复执行。
func enqueueUploadTask(task *models.MoviePilotUploadTask) bool {
	queuedMu.Lock()
	if _, ok := queued[task.ID]; ok {
		queuedMu.Unlock()
		return true
	}
	queued[task.ID] = struct{}{}
	queuedMu.Unlock()
	select {
	case uploadQueue <- task:
		return true
	default:
		queuedMu.Lock()
		delete(queued, task.ID)
		queuedMu.Unlock()
		return false
	}
}

// StartMoviePilotWatcher 启动后台轮询：常量运行，按配置周期检测 MP 下载完成并处理
func StartMoviePilotWatcher() {
	if !watcherRunning.CompareAndSwap(false, true) {
		return
	}

	// 上传 worker：串行处理上传任务
	go func() {
		for task := range uploadQueue {
			queuedMu.Lock()
			delete(queued, task.ID)
			queuedMu.Unlock()
			runUploadTask(task)
		}
	}()

	// 启动恢复：内存队列在重启后为空，重新入队未完成的上传任务（幂等，runUploadTask 会重建文件批次）
	go func() {
		var unfinished []models.MoviePilotUploadTask
		if err := db.Db.Where("status IN ?", []models.MoviePilotUploadStatus{
			models.MoviePilotUploadPending, models.MoviePilotUploadUploading,
		}).Find(&unfinished).Error; err == nil {
			restored, dropped := 0, 0
			for i := range unfinished {
				task := unfinished[i]
				if enqueueUploadTask(&task) {
					restored++
				} else {
					dropped++
				}
			}
			if restored > 0 {
				helpers.AppLogger.Infof("MoviePilot 启动恢复上传任务：%d 个", restored)
			}
			if dropped > 0 {
				// 被丢弃的任务仍是 Pending/Uploading 状态且已有 DB 记录（hash 幂等会跳过重建），
				// 需要人工在界面重试，必须留下日志线索而不是静默吞掉
				helpers.AppLogger.Warnf("MoviePilot 启动恢复：内存队列已满，%d 个任务未入队（请在上传任务页手动重试）", dropped)
			}
		}
		// 启动一次自愈扫描：空源失败/等待类任务若文件已落盘则自动恢复上传（无需等首个轮询周期）
		go func() {
			time.Sleep(15 * time.Second)
			healEmptySourceTasks()
		}()
	}()

	go func() {
		cfg := models.LoadMoviePilotConfig()
		interval := time.Duration(cfg.PollInterval) * time.Minute
		if interval <= 0 {
			interval = 5 * time.Minute
		}
		helpers.AppLogger.Infof("MoviePilot 订阅下载检测已启动，轮询间隔 %v", interval)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			current := models.LoadMoviePilotConfig()
			if !current.Enabled || strings.TrimSpace(current.BaseUrl) == "" || strings.TrimSpace(current.ApiToken) == "" {
				continue
			}
			if err := checkCompletedDownloads(); err != nil {
				helpers.AppLogger.Errorf("MoviePilot 检测下载任务失败：%v", err)
			}
			// 源目录晚落盘任务的自动恢复（空源失败/等待 → 文件到位后自动重试上传）
			healEmptySourceTasks()
			if err := applyPromotionLadder(current); err != nil {
				helpers.AppLogger.Errorf("MoviePilot 促销优先监督失败：%v", err)
			}
			autoDeleteSeeds(current)
		}
	}()
}

// applyPromotionLadder 促销优先阶梯监督：
// 按配置的促销优先级（默认 免费>2X免费>普通>50%>2X50%）为每条订阅中的订阅维护一个
// 「当前允许层」，并把 MP 订阅的 include 过滤设为第 0..当前层 的锚定正则：
//   - 始终只放行最高优先层的促销 → 站点出现该层促销时立即优先下载；
//   - 当前层持续 PromotionPatienceHours 小时无新下载 → 放宽到下一层（回退）；
//   - 任一层产生新下载 → 立即回到最高层重新计时（下一集继续优先高价值促销）；
//   - 订阅完成/暂停/删除 → 不再干预并清理阶梯状态。
func applyPromotionLadder(cfg *models.MoviePilotConfig) error {
	order := models.PromotionOrderList(cfg.PromotionOrder)
	if len(order) == 0 {
		return nil
	}
	patience := time.Duration(cfg.PromotionPatienceHours) * time.Hour
	if patience <= 0 {
		patience = 12 * time.Hour
	}

	client := NewClient(cfg.BaseUrl, cfg.ApiToken)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	subs, err := client.ListSubscribes(ctx)
	if err != nil {
		return err
	}

	// 最近下载时间（按 tmdbid 聚合，来自下载历史前 50 条）
	latestDownload := map[int64]time.Time{}
	if histories, herr := client.ListDownloadHistory(ctx, 1, 50); herr == nil {
		for _, h := range histories {
			if h == nil || h.TmdbId <= 0 || h.Date == "" {
				continue
			}
			if t, perr := time.ParseInLocation("2006-01-02 15:04:05", h.Date, time.Local); perr == nil {
				if t.After(latestDownload[h.TmdbId]) {
					latestDownload[h.TmdbId] = t
				}
			}
		}
	}

	activeIDs := make(map[uint]bool, len(subs))
	now := time.Now()
	for _, sub := range subs {
		if sub == nil || sub.TmdbId <= 0 {
			continue
		}
		// 只监督订阅中的订阅；完成/暂停时清空过滤并删除阶梯，恢复后重新从最高层开始
		if sub.State != "R" {
			if models.GetMoviePilotPromotionLadder(uint(sub.ID)) != nil {
				_ = client.UpdateSubscribeInclude(ctx, sub.ID, "")
				models.DeleteMoviePilotPromotionLadder(uint(sub.ID))
			}
			continue
		}
		activeIDs[uint(sub.ID)] = true

		ladder := models.GetMoviePilotPromotionLadder(uint(sub.ID))
		if ladder == nil {
			ladder = &models.MoviePilotPromotionLadder{SubscribeID: uint(sub.ID), Tier: 0, TierStartedAt: now.Unix()}
			_ = models.SaveMoviePilotPromotionLadder(ladder)
		} else {
			ladder.Tier = min(ladder.Tier, len(order)-1)
		}

		// 该片有新下载（晚于当前层开始时间）→ 回到最高层重新计时
		if dlAt, ok := latestDownload[sub.TmdbId]; ok && dlAt.Unix() > ladder.TierStartedAt && ladder.Tier > 0 {
			helpers.AppLogger.Infof("MoviePilot 促销阶梯：订阅 %s 有新下载，重置回最高优先层（%s）", sub.Name, order[0])
			ladder.Tier = 0
			ladder.TierStartedAt = now.Unix()
			_ = models.SaveMoviePilotPromotionLadder(ladder)
		} else if now.Unix()-ladder.TierStartedAt > int64(patience.Seconds()) && ladder.Tier < len(order)-1 {
			// 当前层耐心期内无新下载 → 放宽到下一层
			ladder.Tier++
			ladder.TierStartedAt = now.Unix()
			_ = models.SaveMoviePilotPromotionLadder(ladder)
			helpers.AppLogger.Infof("MoviePilot 促销阶梯：订阅 %s 当前层（%s）%.0f 小时无新下载，放宽到 %s",
				sub.Name, order[ladder.Tier-1], patience.Hours(), order[ladder.Tier])
			// 放宽后立即触发一次搜索，让新层的资源尽快参与
			if serr := client.SearchSubscribe(ctx, sub.ID); serr != nil {
				helpers.AppLogger.Warnf("MoviePilot 促销阶梯放宽后触发搜索失败：%v", serr)
			}
		}

		// include 与目标层不一致时才写 MP（避免每轮无谓写配置）
		want := PromotionTierIncludeRegex(order, ladder.Tier)
		if sub.Include != want {
			if uerr := client.UpdateSubscribeInclude(ctx, sub.ID, want); uerr != nil {
				helpers.AppLogger.Errorf("MoviePilot 促销阶梯：设置订阅 %s include 失败：%v", sub.Name, uerr)
			} else {
				helpers.AppLogger.Infof("MoviePilot 促销阶梯：订阅 %s 允许促销调整为 %s（第 %d/%d 层）",
					sub.Name, strings.Join(order[:ladder.Tier+1], ">"), ladder.Tier+1, len(order))
			}
		}
	}

	// 清理已删除订阅的阶梯状态
	for _, l := range models.ListMoviePilotPromotionLadders() {
		if !activeIDs[l.SubscribeID] {
			models.DeleteMoviePilotPromotionLadder(l.SubscribeID)
		}
	}
	return nil
}

// seedStartedAt 从 MP 下载历史推断种子的做种起点（下载完成时间）。
// 下载器列表接口不返回完成时间，历史记录 date（yyyy-MM-dd HH:mm:ss）是可靠来源。
func seedStartedAt(client *Client, ctx context.Context, hash string) time.Time {
	var started time.Time
	histories, err := client.ListDownloadHistory(ctx, 1, 50)
	if err != nil {
		return started
	}
	for _, h := range histories {
		if h == nil || h.DownloadHash != hash || h.Date == "" {
			continue
		}
		if t, perr := time.ParseInLocation("2006-01-02 15:04:05", h.Date, time.Local); perr == nil {
			started = t
		}
		break
	}
	return started
}

// autoDeleteSeeds 自动删种：做种达到 SeedRetentionHours 且对应上传任务已全部完成时，
// 删除种子及本地文件释放磁盘空间。0=关闭。
// 删除条件刻意从严：任务不存在（尚未建上传任务）或未到 uploaded 终态都不删，宁可多留不做种。
// 数据源优先级：配置了 qBittorrent（QbittorrentURL+账密）→ 直连 qB（做种时长用 qB 自报
// seeding_time，可靠且覆盖已从 MP 下载列表消失的老种子）；否则回退走 MP 删除接口。
func autoDeleteSeeds(cfg *models.MoviePilotConfig) {
	retention := cfg.SeedRetentionHours
	if retention <= 0 {
		return
	}
	if strings.TrimSpace(cfg.QbittorrentURL) != "" {
		autoDeleteSeedsViaQb(cfg, retention)
		return
	}
	autoDeleteSeedsViaMP(cfg, retention)
}

// autoDeleteSeedsViaQb 直连 qBittorrent 删种（推荐路径）
func autoDeleteSeedsViaQb(cfg *models.MoviePilotConfig, retention int) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	client := qbittorrent.NewClient(cfg.QbittorrentURL, cfg.QbittorrentUser, cfg.QbittorrentPass)
	torrents, err := client.ListTorrents(ctx)
	if err != nil {
		helpers.AppLogger.Warnf("MoviePilot 自动删种：连接 qBittorrent 失败：%v", err)
		return
	}
	minSeeded := time.Duration(retention) * time.Hour
	for _, tor := range torrents {
		if tor.Hash == "" {
			continue
		}
		// 只删下载完成（progress=100）且非下载/校验状态的种子；missingFiles 同样可删（文件已丢，纯占记录）
		if tor.Progress < 100 && tor.State != "missingFiles" {
			continue
		}
		task := models.FindMoviePilotUploadTask(tor.Hash)
		if task == nil || task.Status != models.MoviePilotUploadUploaded {
			continue
		}
		seeded := time.Duration(tor.SeedingTime) * time.Second
		if seeded < minSeeded {
			continue
		}
		// missingFiles：本地文件已不存在，只删种子记录（deleteFiles 无所谓）
		deleteFiles := tor.State != "missingFiles"
		name := tor.Name
		if len(name) > 24 {
			name = name[:24]
		}
		if err := client.DeleteTorrent(ctx, tor.Hash, deleteFiles); err != nil {
			helpers.AppLogger.Errorf("MoviePilot 自动删种失败：%s（hash=%s）：%v", name, tor.Hash[:min(12, len(tor.Hash))], err)
			continue
		}
		helpers.AppLogger.Infof("MoviePilot 自动删种完成（qB）：种子 %s…（做种 %s，上传任务 #%d 已完成%s），已删除种子%s",
			tor.Hash[:min(12, len(tor.Hash))], formatDurationCN(seeded), task.ID,
			map[bool]string{true: "、含本地文件", false: "（文件已丢失仅删记录）"}[deleteFiles],
			map[bool]string{true: "及本地文件", false: ""}[deleteFiles])
	}
}

// autoDeleteSeedsViaMP 经 MP 删除接口删种（未配置 qB 时的回退路径）。
// ⚠ 已知局限：MP /api/v1/download/ 只含仍在管理中的任务，纯做种老种子不在列表里
// 会被漏删；建议在 MP 订阅设置里配置 qBittorrent 地址以启用直连路径。
func autoDeleteSeedsViaMP(cfg *models.MoviePilotConfig, retention int) {
	client := NewClient(cfg.BaseUrl, cfg.ApiToken)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	torrents, err := client.ListDownloads(ctx)
	if err != nil {
		helpers.AppLogger.Warnf("MoviePilot 自动删种：获取下载任务失败：%v", err)
		return
	}
	for _, t := range torrents {
		if t == nil || t.Hash == "" || t.Name == "" {
			continue
		}
		// 只处理做种中的完成任务；仍在下载/校验的不动
		if t.Progress < 100 || (t.State != "seeding" && t.State != "completed" && t.State != "paused") {
			continue
		}
		task := models.FindMoviePilotUploadTask(t.Hash)
		if task == nil || task.Status != models.MoviePilotUploadUploaded {
			continue
		}
		started := seedStartedAt(client, ctx, t.Hash)
		if started.IsZero() {
			// MP 下载历史翻页查不到（老种子早已被新记录挤出前列）：
			// 回退用上传任务完成时刻做种起点——上传成功必然晚于下载完成开始做种，
			// 以此估算做种时长只会偏保守（晚删），不会早删。
			if task.UpdatedAt.IsZero() {
				continue
			}
			started = task.UpdatedAt
		}
		seeded := time.Since(started)
		if seeded < time.Duration(retention)*time.Hour {
			continue
		}
		if err := client.DeleteDownload(ctx, t.Hash, t.Name); err != nil {
			helpers.AppLogger.Errorf("MoviePilot 自动删种失败：%s（hash=%s）：%v", t.Title, t.Hash[:min(12, len(t.Hash))], err)
			continue
		}
		helpers.AppLogger.Infof("MoviePilot 自动删种完成：%s（做种 %s，上传任务 #%d 已完成），已删除种子及本地文件",
			t.Title, formatDurationCN(seeded), task.ID)
	}
}

// formatDurationCN 时长中文简写（如 25h3m / 3d2h）
func formatDurationCN(d time.Duration) string {
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd%dh", int(d.Hours()/24), int(d.Hours())%24)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// checkCompletedDownloads 检测 MP 已完成的下载任务并创建上传任务。
// 先检查下载列表中的完成任务，再兜底检查下载历史（任务完成后即从下载列表移除）。
func checkCompletedDownloads() error {
	cfg := models.LoadMoviePilotConfig()
	client := NewClient(cfg.BaseUrl, cfg.ApiToken)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	torrents, err := client.ListDownloads(ctx)
	if err != nil {
		return err
	}
	for _, t := range torrents {
		if t.Hash == "" {
			continue
		}
		// 完成判定：进度 100% 且非下载中
		if t.Progress < 100 || t.State == "downloading" {
			continue
		}
		if models.FindMoviePilotUploadTask(t.Hash) != nil {
			continue
		}
		localPath := resolveLocalPath(t, cfg)
		if localPath == "" {
			continue
		}

		mediaType := ""
		var tmdbId int64
		if t.Media != nil {
			if v, ok := t.Media["type"].(string); ok {
				mediaType = v
			}
			if v, ok := t.Media["tmdbid"].(float64); ok {
				tmdbId = int64(v)
			}
		}
		if err := createUploadTaskFromDownload(client, cfg, t.Hash, t.Title, t.Name, localPath, mediaType, tmdbId, t.SeasonEpisode); err != nil {
			helpers.AppLogger.Errorf("MoviePilot 为下载任务 %s 创建上传任务失败：%v", t.Name, err)
			continue
		}
	}
	// 兜底：下载历史中尚未捕获的完成任务
	return checkDownloadHistory()
}

// createUploadTaskFromDownload 创建上传任务并加入上传队列（hash 幂等 + 文件级占用去重）。
// 同一保存目录可能对应多条下载记录：季包补种场景整季种子（hash A）先上传了 E01~E10，
// 后续单集下载（hash B）把新集补进同一目录 —— 此时不应整体跳过，而应检测目录内是否存在
// 尚未被任何既有批次占用的新文件缺口；有缺口才建任务，文件级过滤保证只上传缺口文件。
// errUploadSkipped 目录已由既有批次处理且当前无新增文件缺口。
// 可能是暂态（新集文件尚未拷贝完落盘），由 checkDownloadHistory 记入重试、不推进游标，
// 文件落盘后下一轮即可补传；避免游标越过导致新集永久漏传（飞到我心上 E12 案例）。
var errUploadSkipped = errors.New("upload skipped: directory already handled without new files")

func createUploadTaskFromDownload(client *Client, cfg *models.MoviePilotConfig, hash, title, name, localPath, mediaType string, tmdbId int64, seasonEpisode string) error {
	// 目录已由其他 hash 处理过：仅当目录内仍有未被既有批次占用的新文件（新集/缺集）才放行
	if dup := models.FindMoviePilotUploadTaskByLocalPath(localPath, hash); dup != nil {
		if !localDirHasUnclaimedFiles(localPath) {
			helpers.AppLogger.Infof("MoviePilot 跳过重复上传：%s 的源目录 %s 已由任务 #%d（hash=%s）处理", title, localPath, dup.ID, dup.TorrentHash)
			return errUploadSkipped
		}
		helpers.AppLogger.Infof("MoviePilot 目录 %s 已由任务 #%d 处理过，但存在未上传的新文件，放行增量上传", localPath, dup.ID)
	}
	remotePath := strings.TrimRight(cfg.UploadRoot, "/")
	if remotePath == "" {
		remotePath = "/影视/订阅下载"
	}
	remotePath += "/" + filepath.Base(strings.TrimRight(localPath, "/"))
	if title == "" {
		title = name
	}
	task := &models.MoviePilotUploadTask{
		TorrentHash: hash,
		Title:       title,
		MediaType:   mediaType,
		TmdbId:      tmdbId,
		Season:      seasonEpisode,
		LocalPath:   localPath,
		RemotePath:  remotePath,
		Status:      models.MoviePilotUploadPending,
	}
	if err := models.CreateMoviePilotUploadTask(task); err != nil {
		return fmt.Errorf("创建上传任务失败：%v", err)
	}
	helpers.AppLogger.Infof("MoviePilot 检测到下载完成：%s → %s（139 目标 %s）", title, localPath, remotePath)
	if !enqueueUploadTask(task) {
		helpers.AppLogger.Warnf("MoviePilot 上传队列已满，任务 %s（%s）已落库待人工重试", hash, title)
	}
	return nil
}

// localDirHasUnclaimedFiles 目录内是否存在未被任何既有 MoviePilot 批次占用的本地文件。
// 用于季包补种目录的放行判定：目录整体已上传过，但出现了新集文件（不在任何批次的文件任务里）。
func localDirHasUnclaimedFiles(localPath string) bool {
	files, err := CollectLocalFiles(localPath)
	if err != nil {
		return false
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.AbsPath)
	}
	claimed := models.MoviePilotOccupiedByLocalPaths(paths, 0)
	for _, f := range files {
		if _, ok := claimed[filepath.Clean(f.AbsPath)]; !ok {
			return true
		}
	}
	return false
}

// 下载历史检测游标：已扫描到的最大历史 ID；attempts 记录本地路径未匹配的 hash 及上次尝试时间
var (
	historyMu       sync.Mutex
	lastHistoryID   int64
	historyAttempts = map[string]time.Time{}
)

// checkDownloadHistory 检查 MP 下载历史，为尚未捕获的完成下载创建上传任务。
// 历史接口按 id 降序返回（最新在前），lastHistoryID 记录已处理过的最大 id。
// 历史 path 为 MP 侧保存路径（如 alist:/中国移动云盘/影视/待整理/日韩剧集/xxx），
// 实际文件位于本地视图根目录（LocalViewRoot）下同名目录，故取 path 最后一段递归匹配。
func checkDownloadHistory() error {
	cfg := models.LoadMoviePilotConfig()
	client := NewClient(cfg.BaseUrl, cfg.ApiToken)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	histories, err := client.ListDownloadHistory(ctx, 1, 100)
	if err != nil {
		return err
	}
	if len(histories) == 0 {
		return nil
	}

	historyMu.Lock()
	base := lastHistoryID
	historyMu.Unlock()

	processed := 0
	maxID := base
	var minCreatedID int64
	stoppedEarly := false
	for _, h := range histories {
		// MP 历史接口按 id 降序返回（最新在前），遇到已处理过的记录即可停止
		if h.ID <= base {
			break
		}
		if h.DownloadHash == "" {
			if h.ID > maxID {
				maxID = h.ID
			}
			continue
		}
		if models.FindMoviePilotUploadTask(h.DownloadHash) != nil {
			if h.ID > maxID {
				maxID = h.ID
			}
			continue
		}
		// 本地路径未匹配过的任务 1 小时内不重试（文件可能尚未到位），且不推进游标
		historyMu.Lock()
		lastTry, tried := historyAttempts[h.DownloadHash]
		historyMu.Unlock()
		if tried && time.Since(lastTry) < time.Hour {
			continue
		}
		localPath := resolveHistoryLocalPath(h, cfg)
		if localPath == "" {
			historyMu.Lock()
			historyAttempts[h.DownloadHash] = time.Now()
			historyMu.Unlock()
			// 路径匹配失败必须留痕（常见于 LocalViewRoot/DownloadRoot 与下载器实际保存路径不一致），
			// 否则上传任务静默缺失无从排查
			helpers.AppLogger.Warnf("MoviePilot 下载历史 %s（%s）本地路径未匹配到（历史 path=%s，LocalViewRoot=%s），跳过创建上传任务；请检查 MoviePilot 设置中的下载根目录配置",
				h.Title, h.DownloadHash[:min(12, len(h.DownloadHash))], h.Path, cfg.LocalViewRoot)
			continue
		}
		mediaType := ""
		if h.Type == "电视剧" {
			mediaType = "tv"
		} else if h.Type == "电影" {
			mediaType = "movie"
		}
		if err := createUploadTaskFromDownload(client, cfg, h.DownloadHash, h.Title, h.TorrentName, localPath, mediaType, h.TmdbId, h.Seasons); err != nil {
			if errors.Is(err, errUploadSkipped) {
				// 暂态跳过（目录已处理但暂无新文件缺口）：记入重试且不推进游标，
				// 文件（新集）落盘后下一轮即可补传；首次跳过下一轮立即重试，之后 1 小时节流
				historyMu.Lock()
				if _, tried := historyAttempts[h.DownloadHash]; tried {
					historyAttempts[h.DownloadHash] = time.Now()
				} else {
					historyAttempts[h.DownloadHash] = time.Now().Add(-1 * time.Hour)
				}
				historyMu.Unlock()
				continue
			}
			helpers.AppLogger.Errorf("MoviePilot 为历史下载 %s（%s）创建上传任务失败：%v", h.Title, h.DownloadHash, err)
			// 真实错误（瞬时 DB/网络故障等）同样不推进游标，1 小时节流重试，
			// 否则一次故障即把该历史永久甩在游标之外
			historyMu.Lock()
			historyAttempts[h.DownloadHash] = time.Now()
			historyMu.Unlock()
			continue
		}
		processed++
		if minCreatedID == 0 || h.ID < minCreatedID {
			minCreatedID = h.ID
		}
		if h.ID > maxID {
			maxID = h.ID
		}
		if processed >= 20 {
			stoppedEarly = true
			break
		}
	}
	// 提前停止（本轮新建满 20 个任务）时游标只推进到本轮最早的新建记录：
	// 若推进到最新 ID，未扫到的更老记录（ID 介于旧游标与最新之间）下一轮会被 base 拦截永久漏处理；
	// 已建任务的记录下轮靠 hash 查重幂等跳过，代价可忽略
	cursorTarget := maxID
	if stoppedEarly && minCreatedID > 0 && minCreatedID < maxID {
		cursorTarget = minCreatedID
	}
	historyMu.Lock()
	if cursorTarget > lastHistoryID {
		lastHistoryID = cursorTarget
	}
	historyMu.Unlock()
	if processed > 0 {
		helpers.AppLogger.Infof("MoviePilot 下载历史检测完成：新增 %d 个上传任务", processed)
	}
	return nil
}

// resolveHistoryLocalPath 从下载历史记录定位容器内可访问的本地路径。
// 取历史 path 最后一段（MP 转移后的目录/文件名），在本地视图根下递归匹配（最多 3 层）。
func resolveHistoryLocalPath(h *DownloadHistory, cfg *models.MoviePilotConfig) string {
	lastSeg := path.Base(strings.TrimRight(strings.ReplaceAll(h.Path, "\\", "/"), "/"))
	if lastSeg == "" || lastSeg == "." || lastSeg == "/" {
		return ""
	}
	root := strings.TrimRight(cfg.LocalViewRoot, "/")
	if root == "" {
		root = strings.TrimRight(cfg.DownloadRoot, "/")
	}
	if root == "" {
		return ""
	}
	var found string
	count := 0
	scanDirMatch(root, lastSeg, 0, 3, &found, &count)
	// 历史指向单个文件时，匹配到文件取所在目录
	if count == 0 {
		scanFileMatch(root, lastSeg, 0, 3, &found, &count)
	}
	if count == 1 {
		return strings.TrimRight(filepath.ToSlash(found), "/")
	}
	if count > 1 {
		helpers.AppLogger.Warnf("MoviePilot 历史 %s 本地匹配到多个路径，跳过：%s", lastSeg, h.Path)
	}
	return ""
}

// scanDirMatch 在 root 下递归（maxDepth 层内）查找与 target 同名的目录，命中即停止
func scanDirMatch(root, target string, depth, maxDepth int, found *string, count *int) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(root, e.Name())
		if e.Name() == target {
			*found = p
			*count++
			return
		}
		scanDirMatch(p, target, depth+1, maxDepth, found, count)
	}
}

// scanFileMatch 在 root 下递归查找与 target 同名的文件，命中取所在目录
func scanFileMatch(root, target string, depth, maxDepth int, found *string, count *int) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if e.IsDir() {
			if !strings.HasPrefix(e.Name(), ".") {
				scanFileMatch(p, target, depth+1, maxDepth, found, count)
			}
			continue
		}
		if e.Name() == target {
			*found = filepath.Dir(p)
			*count++
		}
	}
}

// resolveLocalPath 把 MP 返回的保存路径映射为容器内可访问路径
func resolveLocalPath(t *DownloadTorrent, cfg *models.MoviePilotConfig) string {
	raw := t.ContentPath
	if raw == "" {
		raw = t.SavePath
	}
	mapped := mapPathPrefix(raw, cfg.DownloadRoot, cfg.LocalViewRoot)
	// 若带前缀映射后路径不存在，尝试原路径（同机部署时两个值一致）
	if !pathExists(mapped) && pathExists(raw) {
		mapped = raw
	}
	// contentPath 可能指向单个文件，此时取其所在目录
	if info, err := os.Stat(mapped); err == nil && !info.IsDir() {
		mapped = filepath.Dir(mapped)
	}
	if !pathExists(mapped) {
		helpers.AppLogger.Warnf("MoviePilot 下载路径 %s 不存在（映射后 %s），跳过", raw, mapped)
		return ""
	}
	return strings.TrimRight(filepath.ToSlash(mapped), "/")
}

// mapPathPrefix 前缀映射：mp 侧路径 → 本容器路径
func mapPathPrefix(raw, from, to string) string {
	if raw == "" {
		return raw
	}
	from = strings.TrimRight(from, "/")
	to = strings.TrimRight(to, "/")
	if from != "" && to != "" && (raw == from || strings.HasPrefix(raw, from+"/")) {
		return to + strings.TrimPrefix(raw, from)
	}
	return raw
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// runUploadTask 执行上传任务：创建文件级上传任务走系统统一上传队列
func runUploadTask(task *models.MoviePilotUploadTask) {
	// 任务可能在内存队列中排队期间被用户取消：重读 DB 状态，已取消/失败的任务不再执行
	// （否则 Uploading 会覆盖 canceled，"取消"对已入队任务无效）
	if fresh := models.GetMoviePilotUploadTask(task.ID); fresh != nil {
		if fresh.Status != models.MoviePilotUploadPending && fresh.Status != models.MoviePilotUploadUploading {
			helpers.AppLogger.Infof("MoviePilot 上传任务 #%d 状态已是 %s，跳过执行", task.ID, fresh.Status)
			return
		}
		task = fresh
	}
	cfg := models.LoadMoviePilotConfig()
	var account models.Account
	if err := db.Db.First(&account, cfg.UploadAccountId).Error; err != nil {
		failUploadTask(task, fmt.Errorf("上传账号不存在（ID=%d），请在设置中配置", cfg.UploadAccountId))
		return
	}

	task.Status = models.MoviePilotUploadUploading
	task.Error = ""
	_ = models.UpdateMoviePilotUploadTask(task)

	// 统计本地文件
	if files, err := CollectLocalFiles(task.LocalPath); err == nil {
		var totalBytes int64
		for _, f := range files {
			totalBytes += f.Size
		}
		task.TotalFiles = len(files)
		task.TotalBytes = totalBytes
		_ = models.UpdateMoviePilotUploadTask(task)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	// 重试场景：清理该任务旧的批次记录
	purgeMoviePilotDbTasks(task.ID)

	baseDirID, err := EnsureRemoteDir(ctx, &account, task.RemotePath)
	if err != nil {
		failUploadTask(task, fmt.Errorf("定位网盘上传目录失败：%v", err))
		return
	}
	created, err := CreateMoviePilotUploadTasks(ctx, &account, task.ID, task.LocalPath, task.RemotePath, baseDirID)
	if err != nil {
		if isErrEmptySourceWait(err) {
			// 源目录暂无文件 = 文件尚未落盘（下载完成判定早于文件就绪），
			// 置为等待状态由轮询自动重试，避免整批剧集漏传；仅超时才终态失败
			if task.EmptySourceSince == nil {
				now := time.Now()
				task.EmptySourceSince = &now
				_ = models.UpdateMoviePilotUploadTask(task)
			}
			if time.Since(*task.EmptySourceSince) >= emptySourceWaitLimit {
				failUploadTask(task, fmt.Errorf("源目录长时间无文件（已等待 %v）：%s", emptySourceWaitLimit, task.LocalPath))
				return
			}
			helpers.AppLogger.Warnf("MoviePilot 源目录暂无文件，等待落盘后自动重试：%s（已等 %v）", task.LocalPath, time.Since(*task.EmptySourceSince).Round(time.Minute))
			task.Status = models.MoviePilotUploadPending
			task.Error = fmt.Sprintf("源目录暂无文件，等待落盘后自动重试（已等待 %v）", time.Since(*task.EmptySourceSince).Round(time.Minute))
			_ = models.UpdateMoviePilotUploadTask(task)
			return
		}
		if isErrEmptySource(err) {
			// 目录有文件但全部已被其他批次占用：正常终态，无新增文件可传
			helpers.AppLogger.Infof("MoviePilot 源目录无新增文件（文件均已被既有批次处理）：%s", task.LocalPath)
			task.Status = models.MoviePilotUploadUploaded
			task.TotalFiles = 0
			task.Error = ""
			_ = models.UpdateMoviePilotUploadTask(task)
			return
		}
		failUploadTask(task, fmt.Errorf("创建上传任务失败：%v", err))
		return
	}
	if created == 0 {
		// 目录有文件且未被占用，却一条文件任务都没建成（建记录失败）：是错误，不能标 uploaded 静默丢片
		failUploadTask(task, fmt.Errorf("源目录存在文件但未能创建任何文件上传任务：%s", task.LocalPath))
		return
	}
	// 成功创建批次：清空等待计时，进入批次收敛
	if task.EmptySourceSince != nil {
		task.EmptySourceSince = nil
		task.Error = ""
		_ = models.UpdateMoviePilotUploadTask(task)
	}
	helpers.AppLogger.Infof("MoviePilot 已创建上传批次：%s 共 %d 个文件 → %s", task.Title, created, task.RemotePath)
	// 异步等待批次完成，避免阻塞串行上传队列
	go waitMoviePilotBatchFinalize(task, &account, cfg)
}

// waitMoviePilotBatchFinalize 轮询批次文件任务完成，聚合进度并执行收尾（整理/STRM/通知）
// 批次达到终态后做增量补传扫描，并校验本地文件集连续稳定（下载可能晚于完成判定落盘），
// 待文件集不再变化且无新增任务后批次才收敛结束
func waitMoviePilotBatchFinalize(task *models.MoviePilotUploadTask, account *models.Account, cfg *models.MoviePilotConfig) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	syncTicker := time.NewTicker(time.Minute)
	defer syncTicker.Stop()
	deadline := time.Now().Add(6 * time.Hour)
	lastFP := ""
	stableCount := 0
	for {
		// 等待当前批次全部进入终态
		for {
			<-ticker.C
			if moviePilotDbTasksFinished(task.ID) {
				break
			}
			totalBytes, uploadedBytes, totalFiles, uploadedFiles := moviePilotDbTaskProgress(task.ID)
			task.TotalBytes = totalBytes
			task.UploadedBytes = uploadedBytes
			task.TotalFiles = int(totalFiles)
			task.UploadedFiles = int(uploadedFiles)
			_ = models.UpdateMoviePilotUploadTask(task)
			if time.Now().After(deadline) {
				helpers.AppLogger.Errorf("MoviePilot 等待批次完成超时：%s", task.Title)
				break
			}
		}
		if time.Now().After(deadline) {
			break
		}
		// 批次终态后补齐新落盘文件并判定文件集稳定性
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		created, err := createMissingUploadTasks(ctx, task, account)
		cancel()
		if err != nil {
			helpers.AppLogger.Errorf("MoviePilot 批次 %s 增量补传失败：%v", task.Title, err)
			break
		}
		if created > 0 {
			lastFP = ""
			stableCount = 0
			continue // 有新任务，回到批次等待循环
		}
		fp, err := localDirFingerprint(task.LocalPath)
		if err != nil {
			helpers.AppLogger.Errorf("MoviePilot 批次 %s 文件集扫描失败：%v", task.Title, err)
			break
		}
		if fp == lastFP {
			stableCount++
		} else {
			stableCount = 0
		}
		lastFP = fp
		if stableCount >= 2 {
			// 文件集连续两次扫描一致只能说明没有新增文件，不能代表上传完成：
			// 必须等批次任务全部终态，否则会把仍在上传/卡住的任务误判为成功（假成功）
			if moviePilotDbTasksFinished(task.ID) {
				break // 全部终态，批次收敛
			}
			lastFP = ""
			stableCount = 0
			continue
		}
		if time.Now().After(deadline) {
			helpers.AppLogger.Errorf("MoviePilot 等待批次文件稳定超时：%s", task.Title)
			break
		}
		<-syncTicker.C
	}

	var failedCount int64
	db.Db.Model(&models.DbUploadTask{}).
		Where("movie_pilot_task_id = ? AND status IN ?", task.ID, []models.UploadStatus{
			models.UploadStatusFailed, models.UploadStatusCancelled,
		}).
		Count(&failedCount)
	totalBytes, uploadedBytes, totalFiles, uploadedFiles := moviePilotDbTaskProgress(task.ID)
	task.TotalBytes = totalBytes
	task.UploadedBytes = uploadedBytes
	task.TotalFiles = int(totalFiles)
	task.UploadedFiles = int(uploadedFiles)

	// 收敛循环可能因超时/补传扫描失败退出，此时批次仍有未终态（pending/uploading）任务：
	// 只统计 failed/cancelled 会把「还在传/卡住」误判为成功（假成功），必须先校验
	if !moviePilotDbTasksFinished(task.ID) {
		failUploadTask(task, fmt.Errorf("批次收敛超时，仍有 %d 个文件任务未完成（共 %d 个）", int64(totalFiles)-uploadedFiles, totalFiles))
		return
	}

	if failedCount > 0 {
		if int64(totalFiles) > 0 && failedCount >= int64(totalFiles) {
			failUploadTask(task, fmt.Errorf("全部文件上传失败（%d 个）", failedCount))
			return
		}
		task.Status = models.MoviePilotUploadUploaded
		task.Error = fmt.Sprintf("部分文件上传失败：%d 个", failedCount)
		_ = models.UpdateMoviePilotUploadTask(task)
		helpers.AppLogger.Warnf("MoviePilot 上传部分失败：%s：%s", task.Title, task.Error)
		notifyUploadFinished(task, false, task.Error)
	} else {
		task.Status = models.MoviePilotUploadUploaded
		task.Error = ""
		_ = models.UpdateMoviePilotUploadTask(task)
		helpers.AppLogger.Infof("MoviePilot 上传完成：%s 共 %d 个文件 → %s", task.Title, totalFiles, task.RemotePath)
		notifyUploadFinished(task, true, "")
	}

	// 网盘端整理（文件名解析）→ 整理成功目录生成 STRM
	organizeAndSyncStrm(task, account, cfg)
}

// organizeAndSyncStrm 上传完成后执行网盘端整理并按整理成功目录触发 STRM 同步
func organizeAndSyncStrm(task *models.MoviePilotUploadTask, account *models.Account, cfg *models.MoviePilotConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	rootID := strings.TrimSpace(task.RemotePath)
	if id, err := EnsureRemoteDir(ctx, account, task.RemotePath); err == nil && id != "" {
		rootID = id
	}
	// 整理目标根：上传根目录父级下的「已整理」（如 /影视/待整理 → 影视/已整理）
	organizeRoot := organizeRootPath(cfg.UploadRoot)
	result := organizeUploadedDir(ctx, account, rootID, task.RemotePath, organizeRoot, task)
	helpers.AppLogger.Infof("MoviePilot 整理完成：%s：成功 %d 个，失败 %d 个，无法识别 %d 个", task.Title, result.Organized, result.Failed, result.Unrecognized)
	if result.Organized == 0 {
		task.Error = fmt.Sprintf("无整理成功的文件（失败 %d，无法识别 %d）", result.Failed, result.Unrecognized)
		if task.Error == "" {
			task.Error = "无整理成功的文件"
		}
		_ = models.UpdateMoviePilotUploadTask(task)
		return
	}

	if strings.TrimSpace(cfg.StrmLocalDir) == "" {
		helpers.AppLogger.Warnf("MoviePilot 未配置 STRM 本地输出目录，跳过 STRM 生成：%s", task.Title)
		return
	}
	for _, dir := range result.SuccessDirs {
		sourcePath := strings.TrimRight(organizeRoot, "/") + "/" + dir
		TriggerStrmSyncForDir(account, sourcePath, cfg.StrmLocalDir)
	}
}

// failUploadTask 标记失败并通知
func failUploadTask(task *models.MoviePilotUploadTask, err error) {
	task.Status = models.MoviePilotUploadFailed
	task.Error = err.Error()
	_ = models.UpdateMoviePilotUploadTask(task)
	helpers.AppLogger.Errorf("MoviePilot 上传失败：%s：%v", task.Title, err)
	notifyUploadFinished(task, false, err.Error())
}

// notifyUploadFinished 发送上传完成/失败通知
func notifyUploadFinished(task *models.MoviePilotUploadTask, success bool, errMsg string) {
	cfg := models.LoadMoviePilotConfig()
	if !cfg.NotifyEnabled {
		return
	}
	notifType := notification.MediaAdded
	title := "✅ 订阅下载已上传网盘"
	if !success {
		notifType = notification.SystemAlert
		title = "❌ 订阅下载上传网盘失败"
	}
	content := fmt.Sprintf("%s\n本地路径：%s\n网盘路径：%s", task.Title, task.LocalPath, task.RemotePath)
	if !success {
		content += "\n错误：" + errMsg
	}
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		_ = notificationmanager.GlobalEnhancedNotificationManager.SendNotification(context.Background(), &notification.Notification{
			Type:    notifType,
			Title:   title,
			Content: content,
		})
	}
}

// TriggerStrmSyncForDir 对整理成功的网盘目录触发手动 STRM 同步（ID=0，按路径定位）
func TriggerStrmSyncForDir(account *models.Account, sourcePath, strmLocalDir string) {
	syncTask := &synccron.NewSyncTask{
		TaskType:   synccron.SyncTaskTypeStrm,
		SourcePath: sourcePath,
		AccountId:  account.ID,
		SourceType: account.SourceType,
		TargetPath: strings.TrimRight(strmLocalDir, "/"),
	}
	if err := synccron.AddNewSyncTask(syncTask); err != nil {
		helpers.AppLogger.Errorf("MoviePilot 触发 STRM 同步失败（%s）：%v", sourcePath, err)
		return
	}
	helpers.AppLogger.Infof("MoviePilot 已触发 STRM 同步：%s → %s", sourcePath, strmLocalDir)
}

// healEmptySourceTasks 自愈扫描：源目录文件晚于"下载完成"信号落盘时，
// 旧版会把空源任务直接判失败（或进入等待后无人再拉起），文件到位后 hash 幂等又挡住重建路径，
// 只能靠手动重试。本函数让这类任务在源目录出现可上传文件后自动恢复入队上传，彻底自愈闭环。
// 仅处理空源类失败/等待任务；用户手动取消或其他原因失败的任务不受影响。
func healEmptySourceTasks() {
	var tasks []models.MoviePilotUploadTask
	if err := db.Db.Where("status IN ?", []models.MoviePilotUploadStatus{
		models.MoviePilotUploadFailed, models.MoviePilotUploadPending,
	}).Find(&tasks).Error; err != nil {
		return
	}
	healed := 0
	for i := range tasks {
		task := &tasks[i]
		// 候选：failed 且错误为空源类（旧版判失败）；或 pending 且处于等待落盘（empty_source_since 非空）
		isFailedEmpty := task.Status == models.MoviePilotUploadFailed &&
			(strings.Contains(task.Error, "没有可上传的文件") || strings.Contains(task.Error, "没有待上传的新文件"))
		isWaiting := task.Status == models.MoviePilotUploadPending && task.EmptySourceSince != nil
		if !isFailedEmpty && !isWaiting {
			continue
		}
		if localDirEmpty(task.LocalPath) {
			// 文件仍未落盘：等待类任务超过总时限仍未等到 → 转终态失败（可见可清理），避免无限挂起
			if isWaiting && task.EmptySourceSince != nil && time.Since(*task.EmptySourceSince) >= emptySourceWaitLimit {
				task.Status = models.MoviePilotUploadFailed
				task.Error = fmt.Sprintf("源目录长时间无文件（已等待 %v），已终止：%s", emptySourceWaitLimit, task.LocalPath)
				task.EmptySourceSince = nil
				_ = models.UpdateMoviePilotUploadTask(task)
				helpers.AppLogger.Warnf("MoviePilot 自愈：%s 源目录等待超时，任务 #%d 置为失败", task.Title, task.ID)
			}
			continue // 其余继续原状态等下轮
		}
		// 目录已可上传：仅当存在未被其他批次占用的缺口文件才恢复（防重复传已被更新的批次处理完的目录）
		if !localDirHasUnclaimedFiles(task.LocalPath) {
			if task.Status == models.MoviePilotUploadFailed {
				task.Status = models.MoviePilotUploadUploaded
				task.Error = ""
				task.EmptySourceSince = nil
				_ = models.UpdateMoviePilotUploadTask(task)
				helpers.AppLogger.Infof("MoviePilot 自愈：%s 目录文件已由其他批次处理，任务 #%d 置为完成", task.Title, task.ID)
			}
			continue
		}
		task.Status = models.MoviePilotUploadPending
		task.Error = ""
		task.EmptySourceSince = nil
		if err := models.UpdateMoviePilotUploadTask(task); err != nil {
			continue
		}
		healed++
		helpers.AppLogger.Infof("MoviePilot 自愈：%s 源目录已出现可上传文件，自动恢复任务 #%d 上传", task.Title, task.ID)
		if !enqueueUploadTask(task) {
			helpers.AppLogger.Warnf("MoviePilot 自愈：任务 #%d 上传队列已满，保持 pending 待下轮入队", task.ID)
		}
	}
	if healed > 0 {
		helpers.AppLogger.Infof("MoviePilot 自愈扫描完成：恢复 %d 个待上传任务", healed)
	}
}

// RetryUploadTask 重试失败/取消的上传任务
func RetryUploadTask(taskID uint) bool {
	task := models.GetMoviePilotUploadTask(taskID)
	if task == nil {
		return false
	}
	if task.Status != models.MoviePilotUploadFailed && task.Status != models.MoviePilotUploadCanceled {
		return false
	}
	task.Status = models.MoviePilotUploadPending
	task.Error = ""
	task.UploadedFiles = 0
	task.UploadedBytes = 0
	if err := models.UpdateMoviePilotUploadTask(task); err != nil {
		return false
	}
	purgeMoviePilotDbTasks(task.ID)
	if !enqueueUploadTask(task) {
		helpers.AppLogger.Warnf("MoviePilot 上传队列已满，任务 #%d 重试入队失败（请稍后再试）", task.ID)
		return false
	}
	return true
}

// purgeMoviePilotDbTasks 删除某 MoviePilot 任务的文件级上传批次记录
func purgeMoviePilotDbTasks(mpTaskId uint) {
	db.Db.Where("movie_pilot_task_id = ?", mpTaskId).Delete(&models.DbUploadTask{})
}

// CancelUploadTask 取消待处理/失败的上传任务
func CancelUploadTask(taskID uint) bool {
	task := models.GetMoviePilotUploadTask(taskID)
	if task == nil {
		return false
	}
	if task.Status == models.MoviePilotUploadUploading {
		return false
	}
	task.Status = models.MoviePilotUploadCanceled
	return models.UpdateMoviePilotUploadTask(task) == nil
}
