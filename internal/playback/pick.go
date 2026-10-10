package playback

import (
	"litepan/internal/domain"
)

type Action uint8

const (
	ActionRedirect Action = iota
	ActionStream
)

type Intent struct {
	ForceProxy bool
	// ForceRedirect 强制走 302 直连，优先级与 ForceProxy 同级（先到先得）。
	//
	// 为什么需要它：网盘驱动返回的 DownloadMode 有时是 DownloadProxy，
	// 此时即使用户选了「302 直连」也拿不到直链。这是用户的显式选择，
	// 不该被驱动默认值悄悄否决——但反过来也不能无条件 302：
	// 网盘没给直链（LocalPath 非空、URL 为空）时 302 出去会指向一个空地址。
	// 所以最终仍由 serveResolved 检查直链是否存在，不存在就退回流代理。
	ForceRedirect bool

	FileName     string
	Inline       bool
	WebDAV       bool
	OriginalFile bool
	// SkipRangeLimit：绕过同账号 Range 并发限流（视频海报取帧用）。
	// 提取本身串行（内部已限并发 1），ffmpeg 会并发发起多个请求，
	// 若全部计入限流名额会互相饿死（一个流的并行分片即可占满）。
	SkipRangeLimit bool
}

// allowsPlaybackResolve 只有真实播放请求才允许增强工具替换原始下载地址。
// WebDAV 和视频海报取帧都需要原始文件字节。
func (intent Intent) allowsPlaybackResolve() bool {
	return !intent.WebDAV && !intent.OriginalFile
}

func PickAction(mode domain.DownloadMode, link domain.DownloadInfo, intent Intent) Action {
	if intent.ForceProxy || link.ForceProxy {
		return ActionStream
	}
	if intent.ForceRedirect && link.URL != "" && !link.ForceProxy {
		return ActionRedirect
	}
	switch mode {
	case domain.DownloadRedirect:
		return ActionRedirect
	default:
		return ActionStream
	}
}
