package requests

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"diy-strm/internal/validation"
)

// StringOrInt 兼容 JSON 数字或字符串的字段（如订阅 year，MP v2 要求字符串、旧调用方可能传数字）
type StringOrInt string

// UnmarshalJSON 同时接受 JSON 数字与字符串
func (s *StringOrInt) UnmarshalJSON(b []byte) error {
	var n int64
	if err := json.Unmarshal(b, &n); err == nil {
		*s = StringOrInt(strconv.FormatInt(n, 10))
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = StringOrInt(str)
		return nil
	}
	return errors.New("字段必须是数字或字符串")
}

// UpdateMoviePilotConfigRequest 更新 MoviePilot 配置请求
type UpdateMoviePilotConfigRequest struct {
	Enabled         bool   `json:"enabled"`
	BaseUrl         string `json:"base_url"`
	ApiToken        string `json:"api_token"`
	DownloadRoot    string `json:"download_root"`     // MoviePilot 侧下载根目录
	LocalViewRoot   string `json:"local_view_root"`   // 本容器内下载根目录
	UploadAccountId uint   `json:"upload_account_id"` // 目标网盘账号 ID
	UploadRoot      string `json:"upload_root"`       // 目标网盘上传根目录
	UploadRootId    string `json:"upload_root_id"`    // 目标网盘上传根目录 ID
	StrmLocalDir    string `json:"strm_local_dir"`    // STRM 本地输出目录
	PollInterval    int    `json:"poll_interval"`     // 轮询间隔分钟
	NotifyEnabled   bool   `json:"notify_enabled"`
	CategoryConfig  string `json:"category_config"`   // 分类策略配置（MoviePilot category.yaml 风格，空=默认）
}

// Validate 校验配置
func (r *UpdateMoviePilotConfigRequest) Validate() error {
	if err := validation.NonBlank("base_url", r.BaseUrl); err != nil {
		return err
	}
	if err := validation.NonBlank("api_token", r.ApiToken); err != nil {
		return err
	}
	if r.PollInterval < 1 {
		r.PollInterval = 5
	}
	return nil
}

// TestMoviePilotConnectionRequest 测试连接请求
type TestMoviePilotConnectionRequest struct {
	BaseUrl  string `json:"base_url" binding:"required"`
	ApiToken string `json:"api_token" binding:"required"`
}

// CreateMoviePilotSubscribeRequest 添加订阅请求
type CreateMoviePilotSubscribeRequest struct {
	Name         string     `json:"name" binding:"required"`
	Year         StringOrInt `json:"year"` // MP v2 兼容：year 为字符串（前端可能传数字或字符串）
	Type         string     `json:"type" binding:"required"` // movie/tv
	TmdbId       int64  `json:"tmdbid"`
	Season       int    `json:"season"`
	TotalEpisode int    `json:"total_episode"`
	SavePath     string `json:"save_path"`
	Sites        []int  `json:"sites,omitempty"`
}

// Validate 校验订阅参数
func (r CreateMoviePilotSubscribeRequest) Validate() error {
	if err := validation.NonBlank("name", r.Name); err != nil {
		return err
	}
	if r.Type != "movie" && r.Type != "tv" {
		return validation.New("type", "订阅类型必须为 movie 或 tv")
	}
	if r.TmdbId <= 0 {
		return validation.New("tmdbid", "缺少 TMDB ID")
	}
	if r.SavePath != "" {
		clean := filepath.ToSlash(strings.TrimSpace(r.SavePath))
		if !strings.HasPrefix(clean, "/") {
			return validation.New("save_path", "保存路径必须以 / 开头")
		}
	}
	return nil
}

// UpdateMoviePilotSubscribeStatusRequest 更新订阅状态请求
type UpdateMoviePilotSubscribeStatusRequest struct {
	State string `json:"state" binding:"required"` // R-订阅中 P-完成 S-停止
}

// Validate 校验状态
func (r UpdateMoviePilotSubscribeStatusRequest) Validate() error {
	if r.State != "R" && r.State != "P" && r.State != "S" {
		return validation.New("state", "状态必须为 R（订阅中）/ P（完成）/ S（停止）")
	}
	return nil
}

// ResolveMoviePilotFailedFileRequest 确认整理识别失败文件请求
type ResolveMoviePilotFailedFileRequest struct {
	MediaType string `json:"media_type"` // movie/tv
	Title     string `json:"title"`      // 媒体标题
	Year      int    `json:"year"`       // 年份（可选）
	Season    int    `json:"season"`     // 季号（剧集，可选，缺省 1）
	TmdbID    int64  `json:"tmdb_id"`    // TMDB ID（可选；从候选列表选中时传入，避免同名歧义）
}

// Validate 校验确认整理参数
func (r ResolveMoviePilotFailedFileRequest) Validate() error {
	if r.MediaType != "movie" && r.MediaType != "tv" {
		return validation.New("media_type", "媒体类型必须为 movie 或 tv")
	}
	if err := validation.NonBlank("title", r.Title); err != nil {
		return err
	}
	return nil
}