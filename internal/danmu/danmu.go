// Package danmu 弹幕联动（对齐 tgto123 新版 danmu 模块 + Misaka Danmaku 服务）：
// 302 播放成功后，按 STRM 路径解析 TMDB ID/季/集，调 Misaka API 自动导入下一集弹幕。
// 配置：danmu_api_url / danmu_api_key（discovery_settings 键值表）。
package danmu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"

	"gorm.io/gorm"
)

// 配置键（复用 discovery_settings 存储）
const (
	SettingAPIURL = "danmu_api_url"
	SettingAPIKey = "danmu_api_key"

	importPath = "/api/control/import/auto"
)

// Config 弹幕服务配置
type Config struct {
	APIURL string
	APIKey string
}

// GetConfig 读取配置（未配置返回 ok=false）
func GetConfig() (Config, bool) {
	var rows []struct {
		Key   string
		Value string
	}
	if err := db.Db.Table("discovery_settings").Select("key, value").Where("key IN ?", []string{SettingAPIURL, SettingAPIKey}).Find(&rows).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			helpers.AppLogger.Warnf("弹幕配置读取失败：%v", err)
		}
		return Config{}, false
	}
	cfg := Config{}
	for _, row := range rows {
		var v string
		if json.Unmarshal([]byte(row.Value), &v) == nil {
			switch row.Key {
			case SettingAPIURL:
				cfg.APIURL = strings.TrimRight(strings.TrimSpace(v), "/")
			case SettingAPIKey:
				cfg.APIKey = strings.TrimSpace(v)
			}
		}
	}
	if cfg.APIURL == "" || cfg.APIKey == "" {
		return Config{}, false
	}
	return cfg, true
}

// SaveConfig 保存配置
func SaveConfig(apiURL, apiKey string) error {
	for key, value := range map[string]string{
		SettingAPIURL: strings.TrimRight(strings.TrimSpace(apiURL), "/"),
		SettingAPIKey: strings.TrimSpace(apiKey),
	} {
		raw, _ := json.Marshal(value)
		setting := map[string]any{"key": key, "value": string(raw), "updated_at": time.Now()}
		if err := db.Db.Table("discovery_settings").
			Where("key = ?", key).
			Assign(setting).
			FirstOrCreate(&struct {
				Key   string
				Value string
			}{Key: key}).Error; err != nil {
			return err
		}
	}
	return nil
}

var (
	tmdbIDPattern   = regexp.MustCompile(`(?i)\{tmdb[-=](\d+)\}`)
	seasonPattern   = regexp.MustCompile(`(?i)season[\s._-]*(\d+)|S(\d{1,3})(?:E|$)`)
	episodePattern  = regexp.MustCompile(`(?i)E(\d{1,4})(?:\D|$)`)
	moviePathMarker = regexp.MustCompile(`(?i)/电影/|/movie/`)
)

// MediaInfo 从 STRM 本地路径解析媒体信息（对齐 tgto123 extract_* 函数族）
type MediaInfo struct {
	TmdbID  int64
	IsTV    bool
	Season  int
	Episode int
}

// ParseMediaInfo 从 STRM 文件路径提取 TMDB ID / 季 / 集。
// 路径约定：.../剧集分类/标题 (年份) {tmdb=xxx}/Season 01/xxx S01E05.strm；
// 目录名带 {tmdb=xxx}（整理链生成的标准命名）。
func ParseMediaInfo(strmPath string) (MediaInfo, bool) {
	if strmPath == "" {
		return MediaInfo{}, false
	}
	info := MediaInfo{Season: 1, Episode: 1}
	// 1. TMDB ID：优先整个路径里任意层级的 {tmdb=xxx}/{tmdb-xxx} 标记
	if m := tmdbIDPattern.FindStringSubmatch(strmPath); m != nil {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || id <= 0 {
			return MediaInfo{}, false
		}
		info.TmdbID = id
	} else {
		return MediaInfo{}, false
	}
	// 2. 电影/剧集判定：路径含 /电影/ 或文件名无集号 → 电影
	fileName := filepath.Base(strmPath)
	if moviePathMarker.MatchString(strmPath) {
		info.IsTV = false
		return info, true
	}
	// 季：Season 01 目录 / SxxExx 文件名
	if m := seasonPattern.FindStringSubmatch(filepath.ToSlash(strmPath)); m != nil {
		if m[1] != "" {
			info.Season, _ = strconv.Atoi(m[1])
		} else if m[2] != "" {
			info.Season, _ = strconv.Atoi(m[2])
		}
	}
	// 集：文件名 SxxExx / Exx
	if m := episodePattern.FindStringSubmatch(fileName); m != nil {
		info.Episode, _ = strconv.Atoi(m[1])
		info.IsTV = true
	} else if seasonPattern.MatchString(strmPath) && !moviePathMarker.MatchString(strmPath) {
		// 有 Season 目录但文件名无集号 → 仍按剧集（Episode=1 由上面缺省）
		info.IsTV = true
	} else {
		info.IsTV = false
	}
	return info, true
}

// ImportNextEpisode 302 播放成功后触发：导入下一集弹幕（tgto123 语义：
// 播放当前集 → 预取下一集弹幕，保证下集打开即有弹幕）。
// 异步执行不阻塞播放重定向；所有错误仅记日志。
func ImportNextEpisode(strmPath string) {
	cfg, ok := GetConfig()
	if !ok {
		return
	}
	info, ok := ParseMediaInfo(strmPath)
	if !ok {
		return
	}
	// 电视剧：下一集；电影：直接导入当前（Misaka 电影无分集）
	target := info
	if info.IsTV {
		target.Episode = info.Episode + 1
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		taskID, err := importAuto(ctx, cfg, target)
		if err != nil {
			helpers.AppLogger.Debugf("弹幕导入跳过（%s S%dE%d）：%v", strmPath, target.Season, target.Episode, err)
			return
		}
		helpers.AppLogger.Infof("弹幕联动：已触发导入 %s S%dE%d（taskId=%s）", filepath.Base(strmPath), target.Season, target.Episode, taskID)
	}()
}

// importAuto 调 Misaka /api/control/import/auto
func importAuto(ctx context.Context, cfg Config, info MediaInfo) (string, error) {
	form := url.Values{
		"api_key":   {cfg.APIKey},
		"searchType": {"tmdb"},
		"searchTerm": {strconv.FormatInt(info.TmdbID, 10)},
	}
	if info.IsTV {
		form.Set("mediaType", "tv_series")
		form.Set("season", strconv.Itoa(info.Season))
		form.Set("episode", strconv.Itoa(info.Episode))
	} else {
		form.Set("mediaType", "movie")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIURL+importPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return "", fmt.Errorf("Misaka 校验失败（422）：%s", truncate(string(body), 160))
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Misaka HTTP %d：%s", resp.StatusCode, truncate(string(body), 160))
	}
	var out struct {
		TaskID string `json:"taskId"`
	}
	_ = json.Unmarshal(body, &out)
	return out.TaskID, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
