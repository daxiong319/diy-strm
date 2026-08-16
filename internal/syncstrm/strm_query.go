package syncstrm

import (
	"net/url"
	"strings"

	"diy-strm/internal/models"
)

func encodeStrmQuery(params url.Values) string {
	return strings.ReplaceAll(params.Encode(), "+", "%20")
}

func encodeStrmQueryPathLast(params url.Values) string {
	pathValues, hasPath := params["path"]
	if !hasPath || len(pathValues) == 0 {
		return encodeStrmQuery(params)
	}

	otherParams := url.Values{}
	for key, values := range params {
		if key == "path" {
			continue
		}
		otherParams[key] = append([]string(nil), values...)
	}

	encodedQuery := encodeStrmQuery(otherParams)
	encodedPath := encodeStrmQuery(url.Values{"path": append([]string(nil), pathValues...)})
	if encodedQuery == "" {
		return encodedPath
	}
	if encodedPath == "" {
		return encodedQuery
	}
	return encodedQuery + "&" + encodedPath
}

func expectedStrmQueryForSyncFile(mode int, file *SyncFileCache, userID string) string {
	params := url.Values{}
	params.Add("pickcode", file.PickCode)
	params.Add("userid", userID)
	// 123 云盘附带父目录 ID，与 MakeStrmContent 保持一致，避免播放时只在根目录查找
	if file.SourceType == models.SourceType123 && file.ParentId != "" {
		params.Add("parentid", file.ParentId)
	}
	if pathValue := strmPathQueryValue(mode, file); pathValue != "" {
		params.Add("path", pathValue)
	}
	return encodeStrmQueryPathLast(params)
}
