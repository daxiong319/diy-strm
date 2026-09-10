package controllers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/emby"
	embyclientrestgo "diy-strm/internal/embyclient-rest-go"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/notification"
	"diy-strm/internal/notificationmanager"

	"github.com/gin-gonic/gin"
"regexp"
"strconv"
)

const embyTempImagePrefix = "qms_emby_"

type EmbyEvent struct {
	Title    string `json:"Title"`
	Date     string `json:"Date"`
	Event    string `json:"Event"`
	Severity string `json:"Severity"`
	Server   struct {
		Name    string `json:"Name"`
		ID      string `json:"Id"`
		Version string `json:"Version"`
	} `json:"Server"`
	Item struct {
		Name              string            `json:"Name"`
		ID                string            `json:"Id"`
		Type              string            `json:"Type"`
		IsFolder          bool              `json:"IsFolder"`
		FileName          string            `json:"FileName"`
		Path              string            `json:"Path"`
		Overview          string            `json:"Overview"`
		SeriesName        string            `json:"SeriesName"`
		SeasonName        string            `json:"SeasonName"`
		SeriesId          string            `json:"SeriesId"`
		SeasonId          string            `json:"SeasonId"`
		IndexNumber       int               `json:"IndexNumber"`
		ParentIndexNumber int               `json:"ParentIndexNumber"`
		ProductionYear    int               `json:"ProductionYear"`
		Genres            []string          `json:"Genres"`
		ImageTags         map[string]string `json:"ImageTags"`
	} `json:"Item"`
}

type newSeries struct {
	ID          string        // 剧的 ID
	Name        string        // 剧的名称
	Seasons     map[int][]int // 季的集 ID 列表
	LastUpdated time.Time     // 最后更新时间
}

var newSeriesBuffer = make(map[string]newSeries)
var newSeriesBufferMu = sync.Mutex{}

// 删除事件缓冲区
var deletedSeriesBuffer = make(map[string]newSeries)
var deletedSeriesBufferMu = sync.Mutex{}

// 播放事件去重缓存
var playbackEventCache = make(map[string]time.Time)
var playbackEventCacheMu = sync.Mutex{}

// 定义一个轮询剧集的协程，如果没有启动则第一次收到通知时启动它
var newSeriesBufferTickerStarted bool = false
var newSeriesBufferTickerStartedMu = sync.Mutex{}

// Webhook Emby 事件回调（公开接口）
// @Summary Emby Webhook
// @Description 接收 Emby 的事件回调（library.new）并触发通知或元数据提取
// @Tags Emby 管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /emby/webhook [post]
func Webhook(ctx *gin.Context) {
	// 将请求的 body 内容完整打印到日志
	var body []byte
	if ctx.Request.Body != nil {
		body, _ = io.ReadAll(ctx.Request.Body)
		helpers.AppLogger.Infof("Emby Webhook body：%s", string(body))
	}
	// GlobalEmbyConfig 为 nil（Emby 从未配置）必须单独短路：原条件 nil 时整式为假会穿透到下方解引用 panic
	if body == nil || models.GlobalEmbyConfig == nil || models.GlobalEmbyConfig.EmbyUrl == "" || models.GlobalEmbyConfig.EmbyApiKey == "" {
		ctx.JSON(http.StatusOK, gin.H{
			"message": "webhook",
		})
		return
	}

	// 检查是否启用鉴权
	if models.GlobalEmbyConfig.EnableAuth == 1 {
		// 优先从 X-API-Key 请求头读取，保留 api_key 查询参数兼容 Emby Webhook 配置。
		apiKey := apiKeyFromRequest(ctx)
		if apiKey == "" {
			ctx.JSON(http.StatusUnauthorized, gin.H{
				"message": "缺少 API Key",
			})
			return
		}

		// 验证 API Key。
		_, err := models.ValidateAPIKey(apiKey)
		if err != nil {
			helpers.AppLogger.Errorf("Emby Webhook API Key 验证失败：%v", err)
			ctx.JSON(http.StatusUnauthorized, gin.H{
				"message": "API Key 无效",
			})
			return
		}
	}

	// 处理 body 内容，解析成 JSON。
	var event EmbyEvent
	// 如果解析失败，记录错误日志并返回
	err := json.Unmarshal(body, &event)
	if err != nil {
		helpers.AppLogger.Errorf("Emby Webhook 解析 JSON 失败：%v", err)
		ctx.JSON(http.StatusOK, gin.H{
			"message": "webhook",
		})
		return
	}
	if event.Event == "library.new" {
		// 新入库通知
		// 如果是 Episode 就先存起来，等待 10 秒；如果后续有同 series 的 library.new 事件，就合并通知。
		// 触发通知
		go func() {
			if event.Item.Type == "Episode" {
				addItemToEpisodeBuffer(event.Item.SeriesId, event.Item.ParentIndexNumber, event.Item.IndexNumber)
				return
			}
			if event.Item.Type == "Movie" {
				sendNewMovieNotification(event.Item.ID)
				return
			}
			// Emby 4.8 批量入库只发一条合并事件（Item 为 Series/Season 代表，
			// 标题「将 N 项目添加到 X」），此前不处理导致入库通知永远收不到。
			if event.Item.Type == "Series" || event.Item.Type == "Season" || event.Item.Type == "Folder" || event.Item.Type == "BoxSet" {
				sendMergedMediaNotification(event.Item.ID, event.Title)
			}
		}()
		if event.Item.Type == "Movie" || event.Item.Type == "Episode" {
			// 触发媒体信息提取
			if models.GlobalEmbyConfig != nil && models.GlobalEmbyConfig.EnableExtractMediaInfo == 1 {
				go func() {
					// 获取 Emby 地址和 Emby API Key。
					url := fmt.Sprintf("%s/emby/Items/%s/PlaybackInfo?api_key=%s", models.GlobalEmbyConfig.EmbyUrl, event.Item.ID, models.GlobalEmbyConfig.EmbyApiKey)
					models.AddDownloadTaskFromEmbyMedia(url, event.Item.ID, event.Item.Name)
					if err != nil {
						helpers.AppLogger.Errorf("触发 Emby 信息提取失败：%v", err)
					}
				}()
			} else {
				helpers.AppLogger.Infof("Emby 媒体信息提取功能未启用，跳过媒体信息提取")
			}
		}
	}
	if event.Event == "library.new" || event.Event == "library.modified" {
		// 同步 Emby 条目到本地，用于更新 QMediaSync 本地索引。
		// 只同步可播放条目：Series/Season/Folder 是容器，单条同步查询（IncludeItemTypes=Movie,Video,Episode）
		// 永远查不到它们；Emby 4.8 批量入库的合并事件用 Series 代表（"将 N 项目添加到 X"），
		// 子条目各自有独立事件，跳过容器类型避免误报"未找到 Emby 条目"。
		switch event.Item.Type {
		case "Movie", "Episode", "Video":
			go func() {
				if changed, err := emby.SyncEmbyItemByID(event.Item.ID); err != nil {
					helpers.AppLogger.Warnf("Webhook 单条同步 Emby 条目失败，Item ID=%s，错误=%v", event.Item.ID, err)
				} else if changed {
					helpers.AppLogger.Infof("Webhook 单条同步 Emby 条目完成，Item ID=%s", event.Item.ID)
				}
			}()
		}
	}
	if event.Event == "library.deleted" {
		// 删除媒体通知
		if helpers.IsRelease {
			helpers.AppLogger.Infof("Emby 媒体已删除 %+v", event.Item)
		}
		// 触发通知
		// 删除消息也应该按照新入库消息一样对剧集进行分组
		go func() {
			if event.Item.Type == "Episode" {
				addItemToDeletedEpisodeBuffer(event.Item.SeriesId, event.Item.ParentIndexNumber, event.Item.IndexNumber, event.Item.SeriesName)
				return
			}
			if event.Item.Type == "Movie" {
				sendDeletedMovieNotification(event.Item.ID, event.Item.Name)
			}
		}()
		if event.Item.Type == "Movie" || event.Item.Type == "Episode" || event.Item.Type == "Season" || event.Item.Type == "Series" {
			// 触发联动删除
			if models.GlobalEmbyConfig != nil && models.GlobalEmbyConfig.EnableDeleteNetdisk == 1 {
				// 检查是否允许删除媒体库
				// if !models.IsDeleteNetdiskLibraryEnabled(event.) {
				// 	helpers.AppLogger.Infof("Emby 媒体库 %s 未配置允许删除，跳过删除", event.Item.LibraryId)
				// 	return
				// }
				switch event.Item.Type {
				case "Movie":
					// 电影：在网盘中将视频文件的父目录一起删除
					// 查找 Item.ID 对应的 SyncFileID。
					models.DeleteNetdiskMovieByEmbyItemId(event.Item.ID)
				case "Episode":
					// 集：删除视频文件和元数据（NFO、封面）。
					// 查找 Item.ID 对应的 SyncFileID。
					models.DeleteNetdiskEpisodeByEmbyItemId(event.Item.ID)
				case "Season":
					// 季：先检查视频文件的父目录。如果父目录是季文件夹，则删除该文件夹；如果父目录有 tvshow.nfo，则只删除该季所有集对应的视频文件和元数据（NFO、封面）。
					// 查找 EmbyMediaItem.SeasonID = Item.ID 的记录，取其中一条记录的 SyncFile.Path 作为季目录来处理。
					models.DeleteNetdiskSeasonByItemId(event.Item.ID)
				case "Series":
					// 剧：在网盘中删除 tvshow.nfo 的父目录。
					// 查找 EmbyMediaItem.SeriesID = Item.ID 的记录，取其中一条记录的 SyncFile.Path 作为剧目录来处理。
					models.DeleteNetdiskTvshowByItemId(event.Item.ID)
				default:
				}
			}
			if err := deleteLocalEmbyItemForWebhook(event.Item.Type, event.Item.ID); err != nil {
				helpers.AppLogger.Warnf("Webhook 删除本地 Emby 条目索引失败，Item ID=%s，类型=%s，错误=%v", event.Item.ID, event.Item.Type, err)
			}
		}
	}
	// 处理播放事件（playback.start、playback.pause、playback.stop）
	if event.Event == "playback.start" || event.Event == "playback.pause" || event.Event == "playback.stop" {
		go handlePlaybackEvent(body, event)
	}

	ctx.JSON(http.StatusOK, gin.H{
		"message": "webhook",
	})
}

func deleteLocalEmbyItemForWebhook(itemType string, itemID string) error {
	switch itemType {
	case "Movie", "Video", "Episode":
		return models.DeleteLocalEmbyItemByID(itemID)
	case "Season":
		return models.DeleteLocalEmbyItemsBySeasonID(itemID)
	case "Series":
		return models.DeleteLocalEmbyItemsBySeriesID(itemID)
	default:
		return nil
	}
}

func createEmbyTempImagePath(itemID string) (string, error) {
	sum := sha256.Sum256([]byte(itemID))
	pattern := fmt.Sprintf("%s%x_*.jpg", embyTempImagePrefix, sum)
	file, err := os.CreateTemp(os.TempDir(), pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func removeEmbyTempImage(imagePath string) error {
	if strings.TrimSpace(imagePath) == "" {
		return nil
	}
	tempDir, err := filepath.Abs(os.TempDir())
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(imagePath)
	if err != nil {
		return err
	}
	if filepath.Dir(absPath) != tempDir {
		return fmt.Errorf("拒绝删除临时目录外的 Emby 图片: %s", imagePath)
	}
	if !isEmbyTempImageName(filepath.Base(absPath)) {
		return fmt.Errorf("拒绝删除非受控 Emby 临时图片: %s", imagePath)
	}
	return os.Remove(absPath)
}

func isEmbyTempImageName(name string) bool {
	if !strings.HasPrefix(name, embyTempImagePrefix) || !strings.HasSuffix(name, ".jpg") {
		return false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(name, embyTempImagePrefix), ".jpg")
	if len(body) <= 65 || body[64] != '_' {
		return false
	}
	for _, r := range body[:64] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return len(body[65:]) > 0
}

func addItemToEpisodeBuffer(seriesId string, seasonNumber, episodeNumber int) {
	newSeriesBufferMu.Lock()
	defer newSeriesBufferMu.Unlock()
	if _, exists := newSeriesBuffer[seriesId]; !exists {
		newSeriesBuffer[seriesId] = newSeries{
			ID:          seriesId,
			Seasons:     make(map[int][]int),
			LastUpdated: time.Now(),
		}
	}
	series := newSeriesBuffer[seriesId]
	if _, exists := series.Seasons[seasonNumber]; !exists {
		series.Seasons[seasonNumber] = make([]int, 0)
	}
	series.Seasons[seasonNumber] = append(series.Seasons[seasonNumber], episodeNumber)
	series.LastUpdated = time.Now()
	newSeriesBuffer[seriesId] = series
	helpers.AppLogger.Infof("已将剧集添加到新剧集缓冲区，seriesID=%s，season=%d，episode=%d", seriesId, seasonNumber, episodeNumber)
	// 启动轮询协程
	newSeriesBufferTickerStartedMu.Lock()
	defer newSeriesBufferTickerStartedMu.Unlock()
	if !newSeriesBufferTickerStarted {
		newSeriesBufferTickerStarted = true
		go startNewSeriesBufferTicker()
	}
}

func addItemToDeletedEpisodeBuffer(seriesId string, seasonNumber, episodeNumber int, seriesName string) {
	deletedSeriesBufferMu.Lock()
	defer deletedSeriesBufferMu.Unlock()
	if _, exists := deletedSeriesBuffer[seriesId]; !exists {
		deletedSeriesBuffer[seriesId] = newSeries{
			ID:          seriesId,
			Name:        seriesName,
			Seasons:     make(map[int][]int),
			LastUpdated: time.Now(),
		}
	}
	series := deletedSeriesBuffer[seriesId]
	if _, exists := series.Seasons[seasonNumber]; !exists {
		series.Seasons[seasonNumber] = make([]int, 0)
	}
	series.Seasons[seasonNumber] = append(series.Seasons[seasonNumber], episodeNumber)
	series.LastUpdated = time.Now()
	deletedSeriesBuffer[seriesId] = series
	helpers.AppLogger.Infof("已将剧集添加到删除剧集缓冲区，seriesID=%s，season=%d，episode=%d", seriesId, seasonNumber, episodeNumber)
	// 启动轮询协程
	newSeriesBufferTickerStartedMu.Lock()
	defer newSeriesBufferTickerStartedMu.Unlock()
	if !newSeriesBufferTickerStarted {
		newSeriesBufferTickerStarted = true
		go startNewSeriesBufferTicker()
	}
}

// TestAddItemToEpisodeBuffer 测试 addItemToEpisodeBuffer 函数
func TestAddItemToEpisodeBuffer() {
	// 清空缓冲区
	newSeriesBufferMu.Lock()
	newSeriesBuffer = make(map[string]newSeries)
	newSeriesBufferMu.Unlock()

	// 测试添加第一个剧集
	seriesId := "64647"
	addItemToEpisodeBuffer(seriesId, 1, 9)
	addItemToEpisodeBuffer(seriesId, 1, 8)
	addItemToEpisodeBuffer(seriesId, 1, 5)
	addItemToEpisodeBuffer(seriesId, 1, 4)
	addItemToEpisodeBuffer(seriesId, 1, 3)
	addItemToEpisodeBuffer(seriesId, 1, 1)
	time.Sleep(3 * time.Second)
	addItemToEpisodeBuffer(seriesId, 2, 1)
	addItemToEpisodeBuffer(seriesId, 2, 2)
	addItemToEpisodeBuffer(seriesId, 2, 3)
}

func startNewSeriesBufferTicker() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		<-ticker.C
		newSeriesBufferMu.Lock()
		deletedSeriesBufferMu.Lock()
		helpers.AppLogger.Infof("检查剧集缓冲区，新增缓冲区大小=%d，删除缓冲区大小=%d", len(newSeriesBuffer), len(deletedSeriesBuffer))
		now := time.Now()
		// 到期条目先在锁内拷出并删除，通知在锁外异步发送，避免与 webhook 写侧并发读写 map（fatal 不可恢复）
		type expiredSeries struct {
			id      string
			name    string
			seasons map[int][]int
		}
		var newExpired, deletedExpired []expiredSeries

		// 处理新增缓冲区
		for _, series := range newSeriesBuffer {
			helpers.AppLogger.Infof("检查新增剧集，seriesID=%s，最后更新时间=%s", series.ID, series.LastUpdated.Format("2006-01-02 15:04:05"))
			if now.Sub(series.LastUpdated) >= 10*time.Second {
				helpers.AppLogger.Infof("新剧集缓冲区达到触发时间，发送入库通知，seriesID=%s，季数=%d", series.ID, len(series.Seasons))
				newExpired = append(newExpired, expiredSeries{id: series.ID, seasons: series.Seasons})
				delete(newSeriesBuffer, series.ID)
			} else {
				// 还没到时间，继续等待
				helpers.AppLogger.Infof("等待更多剧集入库通知，seriesID=%s，已缓存季数=%d", series.ID, len(series.Seasons))
			}
		}

		// 处理删除缓冲区
		for _, series := range deletedSeriesBuffer {
			helpers.AppLogger.Infof("检查删除剧集，seriesID=%s，最后更新时间=%s", series.ID, series.LastUpdated.Format("2006-01-02 15:04:05"))
			if now.Sub(series.LastUpdated) >= 10*time.Second {
				helpers.AppLogger.Infof("删除剧集缓冲区达到触发时间，发送删除通知，seriesID=%s，季数=%d", series.ID, len(series.Seasons))
				deletedExpired = append(deletedExpired, expiredSeries{id: series.ID, name: series.Name, seasons: series.Seasons})
				delete(deletedSeriesBuffer, series.ID)
			} else {
				// 还没到时间，继续等待
				helpers.AppLogger.Infof("等待更多剧集删除通知，seriesID=%s，已缓存季数=%d", series.ID, len(series.Seasons))
			}
		}
		empty := len(newSeriesBuffer) == 0 && len(deletedSeriesBuffer) == 0
		deletedSeriesBufferMu.Unlock()
		newSeriesBufferMu.Unlock()

		for _, e := range newExpired {
			go sendNewSeriesNotification(e.id, e.seasons)
		}
		for _, e := range deletedExpired {
			go sendDeletedSeriesNotification(e.id, e.name, e.seasons)
		}

		// 检查是否还有数据需要处理，如果没有则退出协程
		if empty {
			helpers.AppLogger.Infof("剧集缓冲区已清空，停止轮询协程")
			newSeriesBufferTickerStartedMu.Lock()
			newSeriesBufferTickerStarted = false
			newSeriesBufferTickerStartedMu.Unlock()
			return
		}
	}
}

// parseNewItemCountFromTitle 解析 Emby 4.8 合并事件标题「将 N 项目添加到 X」中的 N。
func parseNewItemCountFromTitle(title string) int {
	re := regexp.MustCompile(`将\s*(\d+)\s*项目添加到`)
	m := re.FindStringSubmatch(title)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// summarizeReleaseGroups 从文件名中提取发布组并按出现次数汇总（tgto123 _summarize_release_groups 语义）。
func summarizeReleaseGroups(fileNames []string) string {
	if len(fileNames) == 0 {
		return ""
	}
	counter := make(map[string]int)
	for _, name := range fileNames {
		base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
		// 取最后一个「-」之后的标签作为发布组候选，如 xxx.2160p.WEB-DL.H.265-Ocat
		if idx := strings.LastIndex(base, "-"); idx > 0 {
			group := strings.TrimSpace(base[idx+1:])
			// 过滤纯数字/过短/含点号的非发布组 token
			if group != "" && len(group) <= 32 && !strings.ContainsAny(group, ".0123456789") {
				counter[group]++
			}
		}
	}
	type groupCount struct {
		name  string
		count int
	}
	groups := make([]groupCount, 0, len(counter))
	for name, count := range counter {
		groups = append(groups, groupCount{name, count})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].count != groups[j].count {
			return groups[i].count > groups[j].count
		}
		return groups[i].name < groups[j].name
	})
	if len(groups) > 3 {
		groups = groups[:3]
	}
	parts := make([]string, 0, len(groups))
	for _, g := range groups {
		if g.count > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", g.name, g.count))
		} else {
			parts = append(parts, g.name)
		}
	}
	return strings.Join(parts, ", ")
}

// formatBytesHuman 体积人性化显示（tgto123 _format_size 语义）。
func formatBytesHuman(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	for _, u := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.2f %s", value, u)
		}
	}
	return fmt.Sprintf("%.2f EB", value)
}

// sendMergedMediaNotification 处理 Emby 4.8 批量入库合并事件（library.new，Item 为 Series/Season 代表）。
// 借鉴 tgto123 OrganizeLibraryNotifier：解析新增数量，拉取最新入库集明细，
// 构建「季集区间 + 体积 + 发布组」的消息并携带海报发送。
func sendMergedMediaNotification(itemId, eventTitle string) {
	detail := emby.GetEmbyItemDetail(itemId)
	if detail == nil {
		helpers.AppLogger.Errorf("获取 Emby 媒体 %s 详情失败，无法发送入库合并通知", itemId)
		return
	}

	newCount := parseNewItemCountFromTitle(eventTitle)
	// 拉取最新入库的集明细（新增数量已知时精确拉取）
	episodes := make([]embyclientrestgo.BaseItemDtoV2, 0)
	if newCount > 0 && (detail.Type == "Series" || detail.Type == "Season") {
		if models.GlobalEmbyConfig != nil && models.GlobalEmbyConfig.EmbyUrl != "" && models.GlobalEmbyConfig.EmbyApiKey != "" {
			client := embyclientrestgo.NewClient(models.GlobalEmbyConfig.EmbyUrl, models.GlobalEmbyConfig.EmbyApiKey)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fetchLimit := newCount
			if fetchLimit > 100 {
				fetchLimit = 100
			}
			_ = client.FetchMediaItemsByLibraryID(ctx, embyclientrestgo.EmbyItemsQuery{
				LibraryID:        itemId,
				Limit:            fetchLimit,
				IncludeItemTypes: "Episode",
				Fields:           "MediaSources",
			}, func(item embyclientrestgo.BaseItemDtoV2) error {
				episodes = append(episodes, item)
				return nil
			})
		}
	}

	// 组装扩展行：季集区间 / 体积 / 发布组
	seasons := make(map[int][]int)
	fileNames := make([]string, 0)
	var totalSize int64
	for _, ep := range episodes {
		season := ep.ParentIndexNumber
		if season == 0 {
			season = 1
		}
		seasons[season] = append(seasons[season], ep.IndexNumber)
		for _, source := range ep.MediaSources {
			totalSize += source.Size
			if source.Path != "" {
				fileNames = append(fileNames, source.Path)
			}
		}
	}

	extraLines := ""
	seasonEpisodes := formatSeasonEpisodes(seasons)
	if seasonEpisodes != "" {
		extraLines += fmt.Sprintf("📺 入库季集：%s（新增 %d 集）\n", seasonEpisodes, len(episodes))
	}
	if totalSize > 0 {
		extraLines += fmt.Sprintf("💾 体积：%s\n", formatBytesHuman(totalSize))
	}
	if groups := summarizeReleaseGroups(fileNames); groups != "" {
		extraLines += fmt.Sprintf("🏷️ 发布组：%s\n", groups)
	}

	content := buildMediaNotificationContent(detail, extraLines)
	mediaType := "电视剧"
	if detail.Type == "Movie" {
		mediaType = "电影"
	}
	helpers.AppLogger.Infof("已构建 Emby 合并入库通知：%s（新增 %d 项）", detail.Name, len(episodes))
	sendNewItemNotification(content, detail, mediaType)
}

// buildMediaNotificationContent 构建入库通知正文（tgto123 OrganizeLibraryNotifier._build_message 风格），
// extraLines 追加在入库时间之前（季集区间/体积/发布组等扩展信息）。
func buildMediaNotificationContent(detail *embyclientrestgo.BaseItemDtoV2, extraLines string) string {
	rate := "暂无评分"
	if detail.CommunityRating > 0 {
		rate = fmt.Sprintf("%.1f", detail.CommunityRating)
	}
	genes := "暂无数据"
	if len(detail.Genres) > 0 {
		genes = strings.Join(detail.Genres, ", ")
	}
	actors := "暂无数据"
	if len(detail.People) > 0 {
		actorNames := make([]string, 0)
		for _, person := range detail.People {
			if person.Type == "Actor" {
				actorNames = append(actorNames, person.Name)
				if len(actorNames) >= 5 {
					break
				}
			}
		}
		if len(actorNames) > 0 {
			actors = strings.Join(actorNames, ", ")
		}
	}
	addedTime := time.Now().Format("2006-01-02 15:04:05")
	if detail.DateCreated != "" {
		if parsed, err := time.Parse(time.RFC3339, detail.DateCreated); err == nil {
			addedTime = parsed.Format("2006-01-02 15:04:05")
		}
	}
	overview := detail.Overview
	if overview == "" {
		overview = "暂无简介"
	}
	tmdbID := ""
	if v, ok := detail.ProviderIds["Tmdb"]; ok {
		tmdbID = v
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d)\n\n", detail.Name, detail.ProductionYear)
	if tmdbID != "" {
		fmt.Fprintf(&b, "🆔 TMDB：%s\n", tmdbID)
	}
	fmt.Fprintf(&b, "⭐ 评分：%s\n", rate)
	fmt.Fprintf(&b, "🎭 类型：%s\n", genes)
	fmt.Fprintf(&b, "👤 主演：%s\n", actors)
	b.WriteString(extraLines)
	fmt.Fprintf(&b, "⏰ 入库时间：%s\n\n", addedTime)
	fmt.Fprintf(&b, "📝 简介\n%s", overview)
	return b.String()
}

// 发送新电影消息
func sendNewMovieNotification(itemId string) {
	detail := emby.GetEmbyItemDetail(itemId)
	if detail == nil {
		helpers.AppLogger.Errorf("获取 Emby 媒体 %s 详情失败，无法发送新电影通知", itemId)
		return
	}
	content := buildMediaNotificationContent(detail, "")
	helpers.AppLogger.Infof("已构建入库通知内容：电影 %s", detail.Name)
	sendNewItemNotification(content, detail, "电影")
}

func sendNewSeriesNotification(seriesId string, seasons map[int][]int) {
	detail := emby.GetEmbyItemDetail(seriesId)
	if detail == nil {
		helpers.AppLogger.Errorf("获取 Emby 媒体 %s 详情失败，无法发送新剧集通知", seriesId)
		return
	}
	extraLines := ""
	seasonEpisodes := formatSeasonEpisodes(seasons)
	if seasonEpisodes != "" {
		extraLines += fmt.Sprintf("📺 入库季集：%s\n", seasonEpisodes)
	}
	content := buildMediaNotificationContent(detail, extraLines)
	helpers.AppLogger.Infof("已构建入库通知内容：剧集 %s（%s）", detail.Name, seasonEpisodes)
	sendNewItemNotification(content, detail, "电视剧")
}

func sendNewItemNotification(content string, detail *embyclientrestgo.BaseItemDtoV2, mediaType string) {
	if models.GlobalEmbyConfig == nil || models.GlobalEmbyConfig.EnableMediaNotification != 1 {
		helpers.AppLogger.Infof("媒体入库通知未启用（enable_media_notification=0），跳过发送：%s", detail.Name)
		return
	}
	imagePath := ""
	if detail.ImageTags != nil {
		imageUrl := ""
		// 检查是否有 backdrop 或 banner。
		if tag, ok := detail.ImageTags["backdrop"]; ok {
			imageUrl = fmt.Sprintf("%s/emby/Items/%s/Images/Backdrop?tag=%s&api_key=%s", models.GlobalEmbyConfig.EmbyUrl, detail.Id, tag, models.GlobalEmbyConfig.EmbyApiKey)
		} else if tag, ok := detail.ImageTags["Primary"]; ok {
			imageUrl = fmt.Sprintf("%s/emby/Items/%s/Images/Primary?tag=%s&api_key=%s", models.GlobalEmbyConfig.EmbyUrl, detail.Id, tag, models.GlobalEmbyConfig.EmbyApiKey)
		}
		if imageUrl != "" {
			// 将图片下载到 /tmp 目录，作为通知图片。
			posterPath, perr := createEmbyTempImagePath(detail.Id)
			if perr != nil {
				helpers.AppLogger.Errorf("创建 Emby 海报临时文件失败：%v", perr)
			} else {
				derr := helpers.DownloadFile(imageUrl, posterPath, "QMediaSync")
				if derr != nil {
					_ = removeEmbyTempImage(posterPath)
					helpers.AppLogger.Errorf("下载 Emby 海报失败：%v", derr)
				} else {
					imagePath = posterPath
				}
			}
		}
	}
	notif := &models.Notification{
		Type:      models.MediaAdded,
		Title:     fmt.Sprintf("📚 Emby %s 入库通知", mediaType),
		Content:   content,
		Timestamp: time.Now(),
		Priority:  models.NormalPriority,
	}
	if imagePath != "" {
		notif.Image = imagePath
	}
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(context.Background(), notif); err != nil {
			helpers.AppLogger.Errorf("发送媒体入库通知失败：%v", err)
		}
	}
	// 删除临时图片文件
	if imagePath != "" {
		if err := removeEmbyTempImage(imagePath); err != nil && !os.IsNotExist(err) {
			helpers.AppLogger.Warnf("删除 Emby 海报临时文件失败：%v", err)
		}
	}
}

// 发送删除电影通知
func sendDeletedMovieNotification(itemId, itemName string) {
	content := fmt.Sprintf("电影名称：%s\n⏰ 删除时间：%s", itemName, time.Now().Format("2006-01-02 15:04:05"))
	notif := &models.Notification{
		Type:      models.MediaRemoved,
		Title:     "🗑️ Emby 媒体删除通知",
		Content:   content,
		Timestamp: time.Now(),
		Priority:  models.NormalPriority,
	}
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(context.Background(), notif); err != nil {
			helpers.AppLogger.Errorf("发送媒体删除通知失败：%s => %s，错误：%v", itemId, itemName, err)
		}
	}
}

// 发送删除剧集分组通知
func sendDeletedSeriesNotification(seriesId string, seriesName string, seasons map[int][]int) {
	// 拼接季集信息，格式：S1E1-E3; S2E1,E5。
	seasonEpisodes := formatSeasonEpisodes(seasons)

	content := fmt.Sprintf("电视剧名称：%s\n删除季集：%s\n⏰ 删除时间：%s", seriesName, seasonEpisodes, time.Now().Format("2006-01-02 15:04:05"))
	notif := &models.Notification{
		Type:      models.MediaRemoved,
		Title:     "🗑️ Emby 媒体删除通知",
		Content:   content,
		Timestamp: time.Now(),
		Priority:  models.NormalPriority,
	}
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(context.Background(), notif); err != nil {
			helpers.AppLogger.Errorf("发送媒体删除通知失败：%s（%s），错误：%v", seriesId, seriesName, err)
		}
	}
}

func formatSeasonEpisodes(seasons map[int][]int) string {
	if len(seasons) == 0 {
		return ""
	}

	seasonNumbers := make([]int, 0, len(seasons))
	for seasonNumber := range seasons {
		seasonNumbers = append(seasonNumbers, seasonNumber)
	}
	sort.Ints(seasonNumbers)

	seasonStrArr := make([]string, 0, len(seasons))
	for _, seasonNumber := range seasonNumbers {
		episodes := seasons[seasonNumber]
		if len(episodes) == 0 {
			continue
		}
		// 去重处理，避免同一集多次触发事件导致重复显示
		episodes = removeDuplicates(episodes)

		sort.Ints(episodes)
		seasonStr := fmt.Sprintf("S%d", seasonNumber)

		start := episodes[0]
		prev := episodes[0]
		for i := 1; i < len(episodes); i++ {
			if episodes[i] != prev+1 {
				if start == prev {
					seasonStr += fmt.Sprintf("E%d, ", start)
				} else {
					seasonStr += fmt.Sprintf("E%d-E%d, ", start, prev)
				}
				start = episodes[i]
			}
			prev = episodes[i]
		}
		if start == prev {
			seasonStr += fmt.Sprintf("E%d, ", start)
		} else {
			seasonStr += fmt.Sprintf("E%d-E%d, ", start, prev)
		}

		seasonStr = strings.TrimSuffix(seasonStr, ", ")
		seasonStrArr = append(seasonStrArr, seasonStr)
	}

	return strings.Join(seasonStrArr, "; ")
}

func removeDuplicates(episodes []int) []int {
	seen := make(map[int]struct{})
	result := make([]int, 0, len(episodes))
	for _, ep := range episodes {
		if _, exists := seen[ep]; !exists {
			seen[ep] = struct{}{}
			result = append(result, ep)
		}
	}
	return result
}

// handlePlaybackEvent 处理 Emby 播放事件
func handlePlaybackEvent(body []byte, event EmbyEvent) {
	// 解析完整的播放事件数据
	var playbackWebhook models.EmbyPlaybackWebhook
	if err := json.Unmarshal(body, &playbackWebhook); err != nil {
		helpers.AppLogger.Errorf("解析播放事件失败：%v", err)
		return
	}

	// 检查去重，1 分钟内不重复通知。
	cacheKey := fmt.Sprintf("%s_%s_%s_%s_%s",
		playbackWebhook.GetUserID(),
		playbackWebhook.Item.Type,
		playbackWebhook.Item.Name,
		playbackWebhook.GetDeviceName(),
		playbackWebhook.Event,
	)

	playbackEventCacheMu.Lock()
	if lastTime, exists := playbackEventCache[cacheKey]; exists {
		if time.Since(lastTime) < 1*time.Minute {
			helpers.AppLogger.Infof("播放事件去重跳过：%s（%v 前）", cacheKey, time.Since(lastTime))
			playbackEventCacheMu.Unlock()
			return
		}
	}
	playbackEventCache[cacheKey] = time.Now()

	// 清理超过 5 分钟的缓存项。
	for key, timestamp := range playbackEventCache {
		if time.Since(timestamp) > 5*time.Minute {
			delete(playbackEventCache, key)
		}
	}
	playbackEventCacheMu.Unlock()

	// 构造并发送通知
	notif := createPlaybackNotification(&playbackWebhook)
	imagePath := notif.Image // 保存图片路径以便后续清理
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(context.Background(), notif); err != nil {
			helpers.AppLogger.Errorf("发送播放通知失败：%v", err)
		}
	}

	// 删除临时图片文件
	if imagePath != "" {
		if err := removeEmbyTempImage(imagePath); err != nil && !os.IsNotExist(err) {
			helpers.AppLogger.Warnf("删除 Emby 海报临时文件失败：%v", err)
		}
	}
}

// createPlaybackNotification 构造播放通知
func createPlaybackNotification(webhook *models.EmbyPlaybackWebhook) *notification.Notification {
	// 构造通知内容
	title := fmt.Sprintf("%s %s %s ", webhook.GetEventTypeEmoji(), webhook.GetEventTypeName(), webhook.Item.Name)
	content := formatPlaybackNotificationContent(webhook)

	// 下载海报图片（如果有）
	imagePath := ""
	if webhook.Item.ImageTags != nil {
		if tag, ok := webhook.Item.ImageTags["Primary"]; ok {
			imageUrl := fmt.Sprintf("%s/emby/Items/%s/Images/Primary?tag=%s&api_key=%s",
				models.GlobalEmbyConfig.EmbyUrl,
				webhook.Item.ID,
				tag,
				models.GlobalEmbyConfig.EmbyApiKey)
			posterPath, perr := createEmbyTempImagePath(webhook.Item.ID)
			if perr != nil {
				helpers.AppLogger.Errorf("创建 Emby 海报临时文件失败：%v", perr)
			} else {
				derr := helpers.DownloadFile(imageUrl, posterPath, "QMediaSync")
				if derr != nil {
					_ = removeEmbyTempImage(posterPath)
					helpers.AppLogger.Errorf("下载 Emby 海报失败：%v", derr)
				} else {
					imagePath = posterPath
				}
			}
		}
	}

	// 构造通知元数据
	metadata := map[string]interface{}{}
	playbackDuration := webhook.GetPlaybackDuration()
	if playbackDuration > 0 {
		metadata["观看时长"] = models.FormatPlaybackDuration(playbackDuration)
	}

	notif := &notification.Notification{
		Type:      notification.NotificationType(webhook.GetNotificationEventType()),
		Title:     title,
		Content:   content,
		Metadata:  metadata,
		Timestamp: time.Now(),
		Priority:  notification.NormalPriority,
	}

	// 如果有图片，添加到通知
	if imagePath != "" {
		notif.Image = imagePath
	}

	return notif
}

// formatPlaybackNotificationContent 格式化播放通知内容
func formatPlaybackNotificationContent(webhook *models.EmbyPlaybackWebhook) string {
	var buf bytes.Buffer

	fmt.Fprintf(&buf, "用户：%s\n", webhook.GetUserName())
	fmt.Fprintf(&buf, "设备：%s (%s)\n", webhook.GetDeviceName(), webhook.GetClientName())
	// buf.WriteString(webhook.Item.Name)
	if webhook.Item.Type == "Episode" {
		fmt.Fprintf(&buf, "电视剧：%s\n", webhook.Item.SeriesName)
		fmt.Fprintf(&buf, "季集：S%dE%d\n", webhook.Item.SeasonNumber, webhook.Item.EpisodeNumber)
	}

	// 播放进度
	if models.GlobalEmbyConfig != nil && models.GlobalEmbyConfig.EnablePlaybackProgress == 1 {
		helpers.AppLogger.Infof("通知中需要显示播放进度")
		positionTicks := webhook.PlaybackInfo.PositionTicks
		runtimeTicks := webhook.PlaybackInfo.MediaSource.RunTimeTicks
		if positionTicks > 0 && runtimeTicks > 0 {
			positionStr := formatTicksToTime(positionTicks)
			runtimeStr := formatTicksToTime(runtimeTicks)
			percentage := float64(positionTicks) / float64(runtimeTicks) * 100
			fmt.Fprintf(&buf, "播放进度：%s / %s (%.0f%%)\n", positionStr, runtimeStr, percentage)
			helpers.AppLogger.Infof("通知中需要显示播放进度 %s / %s (%.0f%%)", positionStr, runtimeStr, percentage)
		} else if runtimeTicks > 0 {
			// start 事件没有 position，显示总时长。
			runtimeStr := formatTicksToTime(runtimeTicks)
			fmt.Fprintf(&buf, "时长：%s\n", runtimeStr)
			helpers.AppLogger.Infof("通知中需要显示时长 %s", runtimeStr)
		} else {
			helpers.AppLogger.Infof("无法显示播放进度，因为 positionTicks 或 runtimeTicks 为 0 %s", webhook.Item.ID)
		}
	} else {
		helpers.AppLogger.Infof("通知中不需要显示播放进度")
	}

	// 剧情简介
	if models.GlobalEmbyConfig != nil && models.GlobalEmbyConfig.EnablePlaybackOverview == 1 {
		detail := emby.GetEmbyItemDetail(webhook.Item.ID)
		if detail != nil && detail.Overview != "" {
			overview := detail.Overview
			runes := []rune(overview)
			if len(runes) > 100 {
				overview = string(runes[:100]) + "…"
			}
			fmt.Fprintf(&buf, "简介：%s\n", overview)
		}
	}

	return buf.String()
}

// formatTicksToTime 将 Emby Ticks（100 纳秒单位）转换为 HH:MM:SS 格式
func formatTicksToTime(ticks int64) string {
	totalSeconds := ticks / 10000000 // ticks to seconds
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}
