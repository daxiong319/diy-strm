// Package embywebhook 承接 Emby 的 Webhook 事件：
// 解析 Emby 推送的 JSON、把「入库 / 删除 / 播放」三类事件整理成通知内容，
// 并把短时间内逐个到达的剧集事件合并成一条剧集通知。
//
// 老版实现（v0.3.0）把这些逻辑直接写在 HTTP 控制器与全局变量里，
// 本包刻意与 HTTP 解耦：不依赖 chi、不依赖数据库，只依赖注入进来的
// Client / Notifier 接口，因此可以单独写单元测试。
package embywebhook

import "time"

// EmbyEvent 是 Emby Webhook 的最外层结构。
// 注意：Emby 4.8 批量入库时不会逐个条目推送，而是推一条合并事件，
// Item.Type 为 Series/Season/Folder/BoxSet，Title 形如「将 12 项目添加到 剧名」。
type EmbyEvent struct {
	Title    string     `json:"Title"`
	Date     string     `json:"Date"`
	Event    string     `json:"Event"`
	Severity string     `json:"Severity"`
	Server   EmbyServer `json:"Server"`
	Item     EmbyItem   `json:"Item"`
}

// EmbyServer 描述推送方 Emby 服务端。
type EmbyServer struct {
	Name    string `json:"Name"`
	ID      string `json:"Id"`
	Version string `json:"Version"`
}

// EmbyItem 是事件涉及的媒体条目。只保留通知里用得到的字段。
type EmbyItem struct {
	Name              string            `json:"Name"`
	ID                string            `json:"Id"`
	Type              string            `json:"Type"`
	IsFolder          bool              `json:"IsFolder"`
	FileName          string            `json:"FileName"`
	Path              string            `json:"Path"`
	Overview          string            `json:"Overview"`
	SeriesName        string            `json:"SeriesName"`
	SeasonName        string            `json:"SeasonName"`
	SeriesID          string            `json:"SeriesId"`
	SeasonID          string            `json:"SeasonId"`
	IndexNumber       int               `json:"IndexNumber"`
	ParentIndexNumber int               `json:"ParentIndexNumber"`
	ProductionYear    int               `json:"ProductionYear"`
	Genres            []string          `json:"Genres"`
	ImageTags         map[string]string `json:"ImageTags"`
	CommunityRating   float64           `json:"CommunityRating"`
	DateCreated       string            `json:"DateCreated"`
	ProviderIds       map[string]string `json:"ProviderIds"`
	RunTimeTicks      int64             `json:"RunTimeTicks"`
	MediaSources      []EmbyMediaSource `json:"MediaSources"`
	ParentID          string            `json:"ParentId"`
}

// EmbyMediaSource 是条目的媒体源，通知里只用 Path（回溯源文件路径）与 Size（体积）。
type EmbyMediaSource struct {
	Path string `json:"Path"`
	Size int64  `json:"Size"`
}

// 事件类型常量。
const (
	EventLibraryNew      = "library.new"
	EventLibraryModified = "library.modified"
	EventLibraryDeleted  = "library.deleted"
	EventPlaybackStart   = "playback.start"
	EventPlaybackPause   = "playback.pause"
	EventPlaybackStop    = "playback.stop"
)

// 媒体类型常量（Emby Item.Type）。
const (
	MediaTypeMovie   = "Movie"
	MediaTypeEpisode = "Episode"
	MediaTypeSeries  = "Series"
	MediaTypeSeason  = "Season"
	MediaTypeFolder  = "Folder"
	MediaTypeBoxSet  = "BoxSet"
	MediaTypeAudio   = "Audio"
)

// MergeWindow 是剧集合并缓冲的时间窗：窗口内到达的同一部剧的单集事件合并成一条通知。
// 定义成变量而不是常量，方便测试缩短窗口。
var MergeWindow = 10 * time.Second

// mergeTickInterval 是缓冲区轮询间隔。老版固定 5 秒。
// 取窗口的 1/2 是为了让合并窗口真正生效：若 tick 固定为 5s，任何小于 5s 的
// 窗口都要等到下一个 tick 才会被刷出，窗口设置形同虚设。
var mergeTickInterval = 5 * time.Second

// tickInterval 计算给定合并窗口对应的轮询间隔。
func tickInterval(window time.Duration) time.Duration {
	if window <= 0 {
		return mergeTickInterval
	}
	if half := window / 2; half > 0 && half < mergeTickInterval {
		return half
	}
	return mergeTickInterval
}

// Now 是当前时间来源，测试可替换。
var Now = time.Now

// playbackDedupWindow 是播放事件去重窗口：同一用户+设备+条目+事件在窗口内只通知一次。
var playbackDedupWindow = time.Minute

// playbackCacheTTL 是播放去重缓存条目的存活时间。
var playbackCacheTTL = 5 * time.Minute
