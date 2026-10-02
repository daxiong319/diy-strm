package embyindex

import (
	"errors"
	"net/url"
	"strings"

	"litepan/internal/discover/embyclient"
)

// ErrPickCodeNotFound 表示没有从 Emby 媒体源路径里解析出 PickCode。
var ErrPickCodeNotFound = errors.New("未从 Emby 媒体源路径中解析到 PickCode")

// pickCodeQueryKeys 是网盘播放直链中可能携带 PickCode 的查询参数名，按优先级排列。
var pickCodeQueryKeys = []string{"pickcode", "pick_code"}

// ExtractPickCodeFromPath 从 Emby 媒体源路径中解析网盘 PickCode。
//
// 老版 internal/emby/emby.go:751 的行为契约：
//   - 空串返回空串；
//   - 不以 http:// 或 https:// 开头返回空串（本地路径、STRM 相对路径都不算直链）；
//   - URL 解析失败返回空串；
//   - 查询串里优先取 pickcode，其次 pick_code；
//   - 都没有命中时返回整条路径（老版的兜底语义：把直链本身当作标识）。
func ExtractPickCodeFromPath(rawPath string) string {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return ""
	}
	if !strings.HasPrefix(rawPath, "http://") && !strings.HasPrefix(rawPath, "https://") {
		return ""
	}
	parsed, err := url.Parse(rawPath)
	if err != nil {
		return ""
	}
	query := parsed.Query()
	for _, key := range pickCodeQueryKeys {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			return value
		}
	}
	return rawPath
}

// ExtractPickCode 从一组媒体源中解析 PickCode。
// 返回第一个能解析出 PickCode 的媒体源结果，以及该媒体源的路径。
// 全部解析失败时返回最后一条媒体源的路径与错误。
func ExtractPickCode(sources []embyclient.MediaSource) (pickCode string, mediaSourcePath string, err error) {
	if len(sources) == 0 {
		return "", "", ErrPickCodeNotFound
	}
	for _, source := range sources {
		if code := ExtractPickCodeFromPath(source.Path); code != "" {
			return code, source.Path, nil
		}
	}
	return "", sources[len(sources)-1].Path, ErrPickCodeNotFound
}
