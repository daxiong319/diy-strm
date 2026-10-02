package embywebhook

// 本文件补充播放事件所需的结构体。types.go 里只声明了入库/删除事件用到的字段，
// 播放事件（playback.start|pause|stop）另有 User / Session / PlaybackInfo 三段。
// 这些结构体与 EmbyEvent 的组合关系在 service 侧装配，避免改动 types.go 既有定义。

// EmbyUser 是播放事件的用户信息。
type EmbyUser struct {
	Name string `json:"Name"`
	ID   string `json:"Id"`
}

// EmbySession 是播放事件的会话信息（设备与客户端）。
type EmbySession struct {
	DeviceName string `json:"DeviceName"`
	Client     string `json:"Client"`
}

// EmbyPlaybackInfo 是播放事件的进度信息。
type EmbyPlaybackInfo struct {
	PositionTicks int64  `json:"PositionTicks"`
	PlaySessionID string `json:"PlaySessionId"`
	// RunTimeTicks 是媒体总时长，部分 Emby 版本放在这里。
	RunTimeTicks int64 `json:"RunTimeTicks"`
}

// IsPlaybackEvent 判断是否为播放类事件。
func IsPlaybackEvent(event string) bool {
	switch event {
	case EventPlaybackStart, EventPlaybackPause, EventPlaybackStop:
		return true
	default:
		return false
	}
}

// IsMergedIngestType 判断条目类型是否属于 Emby 4.8 批量入库的合并事件类型。
func IsMergedIngestType(mediaType string) bool {
	switch mediaType {
	case MediaTypeSeries, MediaTypeSeason, MediaTypeFolder, MediaTypeBoxSet:
		return true
	default:
		return false
	}
}
