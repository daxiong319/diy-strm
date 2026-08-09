package embycheck

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	embyclientrestgo "diy-strm/internal/embyclient-rest-go"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
)

// Index Emby 媒体库索引，用于检测影片是否已入库
type Index struct {
	byTmdbID   map[int64]bool
	byNameYear map[string]bool
	builtAt    time.Time
	hasIndex   bool
}

var (
	mu            sync.Mutex
	cachedIndex   *Index
	indexTTL      = 5 * time.Minute
	indexBuildMu   sync.Mutex
)

// CheckItem 检测项
type CheckItem struct {
	TmdbID int64  `json:"tmdb_id"`
	Title  string `json:"title"`
	Year   int    `json:"year"`
}

// CheckResult 检测结果
type CheckResult struct {
	TmdbID int64 `json:"tmdb_id"`
	InEmby bool  `json:"in_emby"`
}

// Check 检测影片是否已入库（结果可能包含未知 tmdb_id 的项）
func Check(items []CheckItem) []CheckResult {
	index, err := getIndex()
	results := make([]CheckResult, 0, len(items))
	for _, item := range items {
		inEmby := false
		if err == nil && index != nil {
			if item.TmdbID > 0 && index.byTmdbID[item.TmdbID] {
				inEmby = true
			} else if item.Title != "" {
				key := normalizeKey(item.Title, item.Year)
				if index.byNameYear[key] {
					inEmby = true
				}
			}
		}
		results = append(results, CheckResult{TmdbID: item.TmdbID, InEmby: inEmby})
	}
	return results
}

func normalizeKey(title string, year int) string {
	title = strings.ToLower(strings.TrimSpace(title))
	title = strings.NewReplacer(" ", "", "：", ":", "·", ".", "-", "", "_", "").Replace(title)
	return fmt.Sprintf("%s|%d", title, year)
}

func getIndex() (*Index, error) {
	mu.Lock()
	defer mu.Unlock()
	if cachedIndex != nil && cachedIndex.hasIndex && time.Since(cachedIndex.builtAt) < indexTTL {
		return cachedIndex, nil
	}
	// 避免并发重复构建
	indexBuildMu.Lock()
	defer indexBuildMu.Unlock()
	if cachedIndex != nil && cachedIndex.hasIndex && time.Since(cachedIndex.builtAt) < indexTTL {
		return cachedIndex, nil
	}
	config, err := models.GetEmbyConfig()
	if err != nil || config == nil || config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return nil, fmt.Errorf("Emby 未配置")
	}
	index := &Index{
		byTmdbID:   map[int64]bool{},
		byNameYear: map[string]bool{},
		builtAt:    time.Now(),
	}
	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	virtualFolders, err := client.GetLibraryVirtualFolders()
	if err != nil {
		helpers.AppLogger.Warnf("获取 Emby 媒体库列表失败：%v", err)
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, library := range virtualFolders {
		collectionType := strings.ToLower(library.CollectionType)
		if collectionType != "movies" && collectionType != "tvshows" {
			continue
		}
		err := client.FetchMediaItemsByLibraryID(
			ctx,
			embyclientrestgo.EmbyItemsQuery{
				LibraryID:        library.ItemId,
				IncludeItemTypes: "Movie,Series",
				Fields:           "ProviderIds,ProductionYear",
			},
			func(item embyclientrestgo.BaseItemDtoV2) error {
				if tmdbID := parseTmdbID(item.ProviderIds); tmdbID > 0 {
					index.byTmdbID[tmdbID] = true
				}
				if item.Name != "" {
					index.byNameYear[normalizeKey(item.Name, item.ProductionYear)] = true
				}
				return nil
			},
		)
		if err != nil {
			helpers.AppLogger.Warnf("拉取 Emby 媒体库 %s 条目失败：%v", library.Name, err)
			continue
		}
	}
	index.hasIndex = true
	cachedIndex = index
	return cachedIndex, nil
}

func parseTmdbID(providerIds map[string]string) int64 {
	if providerIds == nil {
		return 0
	}
	for key, value := range providerIds {
		lower := strings.ToLower(key)
		if lower == "tmdb" || lower == "tmdbid" {
			var id int64
			if _, err := fmt.Sscanf(value, "%d", &id); err == nil {
				return id
			}
		}
	}
	return 0
}
