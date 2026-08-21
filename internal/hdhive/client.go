// Package hdhive 提供影巢（HDHive）资源查询与解锁相关的数据结构。
//
// 数据字段与 hdhive.com Open API 及 hdhive-open.tgtodrive.top OAuth 代理完全兼容，
// 资源订阅引擎（hive_watcher）走 OAuth 代理通道读取这些结构。
package hdhive

// Resource 影巢资源条目
type Resource struct {
	Slug               string   `json:"slug"`
	Title              string   `json:"title"`
	ShareSize          string   `json:"share_size"`
	VideoResolution    []string `json:"video_resolution"`
	Source             []string `json:"source"`
	SubtitleLanguage   []string `json:"subtitle_language"`
	SubtitleType       []string `json:"subtitle_type"`
	Remark             string   `json:"remark"`
	UnlockPoints       int      `json:"unlock_points"`
	UnlockedUsersCount int      `json:"unlocked_users_count"`
	ValidateStatus     string   `json:"validate_status"`
	ValidateMessage    string   `json:"validate_message"`
	LastValidatedAt    string   `json:"last_validated_at"`
	IsOfficial         bool     `json:"is_official"`
	IsUnlocked         bool     `json:"is_unlocked"`
	User               *struct {
		ID        int    `json:"id"`
		Nickname  string `json:"nickname"`
		AvatarURL string `json:"avatar_url"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
}

// ShareDetail 分享详情（解锁前免费/积分判断）
type ShareDetail struct {
	Slug               string   `json:"slug"`
	Title              string   `json:"title"`
	PanType            string   `json:"pan_type"`
	ShareSize          string   `json:"share_size"`
	VideoResolution    []string `json:"video_resolution"`
	Source             []string `json:"source"`
	UnlockPoints       int      `json:"unlock_points"`
	ActualUnlockPoints int      `json:"actual_unlock_points"`
	IsUnlocked         bool     `json:"is_unlocked"`
	IsFreeForUser      bool     `json:"is_free_for_user"`
	UnlockMessage      string   `json:"unlock_message"`
	Media              *struct {
		Type    string `json:"type"`
		TMDBID  string `json:"tmdb_id"`
		Title   string `json:"title"`
		Season  string `json:"season"`
	} `json:"media"`
}

// UnlockResult 解锁结果
type UnlockResult struct {
	URL          string `json:"url"`
	AccessCode   string `json:"access_code"`
	FullURL      string `json:"full_url"`
	AlreadyOwned bool   `json:"already_owned"`
}
