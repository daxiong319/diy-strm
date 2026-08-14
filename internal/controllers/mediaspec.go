package controllers

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"diy-strm/internal/db"
	"diy-strm/internal/models"
)

// 媒体规格解析与洗版判定。
// 优先级链：分辨率 > 来源 > 编码 > 特效 > 体积。
// 采用加权总分便于"洗版目标达标"判定：
//
//	score = 分辨率*10000 + 来源*1000 + 编码*100 + 特效*10 + min(体积GB, 100)

// MediaSpec 资源规格
type MediaSpec struct {
	Resolution int     // 0未知 1=720p 2=1080p 3=2160p(4K)
	Source     int     // 0未知 1=HDTV 2=WEBRip 3=WEB-DL 4=BluRay 5=REMUX
	Codec      int     // 0未知 1=H264 2=H265
	Effect     int     // 0未知 1=SDR 2=HDR 3=Dolby Vision
	SizeGB     float64 // 体积（GB），解析不到为 0
}

var (
	specResRe = []struct {
		re  *regexp.Regexp
		val int
	}{
		{regexp.MustCompile(`(?i)2160p|4k\b|2160`), 3},
		{regexp.MustCompile(`(?i)1080p|1080i|full\s?hd|1920x1080`), 2},
		{regexp.MustCompile(`(?i)720p|1280x720|half\s?hd`), 1},
	}
	specSrcRe = []struct {
		re  *regexp.Regexp
		val int
	}{
		{regexp.MustCompile(`(?i)remux|uhd\.bluray`), 5},
		{regexp.MustCompile(`(?i)bluray|blu-ray|bdrip|bd\.?iso|蓝光|原盘`), 4},
		{regexp.MustCompile(`(?i)web-dl|webdl|web\.dl|dl\.web`), 3},
		{regexp.MustCompile(`(?i)webrip|web-rip`), 2},
		{regexp.MustCompile(`(?i)dvdrip|dvd|tvrip|hdtv`), 1},
	}
	specCodecRe = []struct {
		re  *regexp.Regexp
		val int
	}{
		{regexp.MustCompile(`(?i)h265|hevc|x265|\bhv1\b`), 2},
		{regexp.MustCompile(`(?i)h264|avc|x264`), 1},
	}
	specDvRe = regexp.MustCompile(`(?i)dolby[\s.]?vision|杜比视界|\bdv\b`)
	specHdrRe = regexp.MustCompile(`(?i)hdr10\+|hdr10|hdr`)
	specSizeRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(gb|g|tb|t)\b`)
)

// ParseMediaSpec 从帖子文本（含资源标题行）解析资源规格
func ParseMediaSpec(text string) MediaSpec {
	var s MediaSpec
	for _, r := range specResRe {
		if r.re.MatchString(text) {
			s.Resolution = r.val
			break
		}
	}
	for _, r := range specSrcRe {
		if r.re.MatchString(text) {
			s.Source = r.val
			break
		}
	}
	for _, r := range specCodecRe {
		if r.re.MatchString(text) {
			s.Codec = r.val
			break
		}
	}
	if specDvRe.MatchString(text) {
		s.Effect = 3
	} else if specHdrRe.MatchString(text) {
		s.Effect = 2
	} else {
		s.Effect = 1
	}
	for _, loc := range specSizeRe.FindAllStringSubmatchIndex(text, -1) {
		numStart := loc[2]
		if numStart > 0 {
			prev := text[numStart-1]
			if (prev >= '0' && prev <= '9') || (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') {
				continue // 数字紧贴字母（如 HDR10.1TB），非体积
			}
		}
		if v, err := strconv.ParseFloat(text[loc[2]:loc[3]], 64); err == nil {
			switch strings.ToLower(text[loc[4]:loc[5]]) {
			case "t", "tb":
				s.SizeGB = v * 1024
			default:
				s.SizeGB = v
			}
			break
		}
	}
	return s
}

// Score 规格加权总分（体积封顶 100，避免体积主导）
func (s MediaSpec) Score() int {
	size := s.SizeGB
	if size > 100 {
		size = 100
	}
	return s.Resolution*10000 + s.Source*1000 + s.Codec*100 + s.Effect*10 + int(size)
}

// BetterThan 新规格是否优于旧规格
func (s MediaSpec) BetterThan(o MediaSpec) bool {
	return s.Score() > o.Score()
}

// WashTargetScore 洗版目标的达标分数阈值（低于该分数视为"未达标"可继续洗版）
func WashTargetScore(target string) int {
	switch strings.TrimSpace(strings.ToLower(target)) {
	case "1080p":
		return 2*10000
	case "4k":
		return 3 * 10000
	case "4k_remux":
		return 3*10000 + 5*1000
	}
	return 0
}

// deleteOldFilesByTitle 在目标目录下按旧版本标题匹配并删除网盘文件
func deleteOldFilesByTitle(ctx context.Context, sourceType, targetDir, title string) (int, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, nil
	}
	var account models.Account
	switch sourceType {
	case string(models.SourceType123):
		if err := db.Db.Where("source_type = ?", models.SourceType123).Order("id asc").First(&account).Error; err != nil {
			return 0, fmt.Errorf("未找到 123 云盘账号：%v", err)
		}
		client := account.Get123Client()
		defer client.Close()
		parentID, err := client.FindDirByPath(ctx, targetDir)
		if err != nil {
			return 0, err
		}
		files, err := client.GetFiles(ctx, parentID)
		if err != nil {
			return 0, err
		}
		ids := make([]string, 0, len(files))
		for _, f := range files {
			if nameMatchesTitle(f.FileName, title) {
				ids = append(ids, strconv.FormatInt(f.FileId, 10))
			}
		}
		if len(ids) == 0 {
			return 0, nil
		}
		return len(ids), client.Delete(ctx, ids)
	case string(models.SourceTypeGuangYaPan):
		if err := db.Db.Where("source_type = ?", models.SourceTypeGuangYaPan).Order("id asc").First(&account).Error; err != nil {
			return 0, fmt.Errorf("未找到光鸭云盘账号：%v", err)
		}
		client := account.GetGuangYaPanClient()
		defer client.Close()
		parentID, err := client.GetPathIdByPath(ctx, targetDir)
		if err != nil {
			return 0, err
		}
		files, err := client.GetFiles(ctx, parentID)
		if err != nil {
			return 0, err
		}
		ids := make([]string, 0, len(files))
		for _, f := range files {
			if nameMatchesTitle(f.FileName, title) {
				ids = append(ids, f.FileID)
			}
		}
		if len(ids) == 0 {
			return 0, nil
		}
		return len(ids), client.Delete(ctx, ids)
	case string(models.SourceTypePan139):
		if err := db.Db.Where("source_type = ?", models.SourceTypePan139).Order("id asc").First(&account).Error; err != nil {
			return 0, fmt.Errorf("未找到中国移动云盘账号：%v", err)
		}
		client := account.GetPan139Client()
		defer client.Close()
		parentID, err := client.GetPathIdByPath(ctx, targetDir)
		if err != nil {
			return 0, err
		}
		files, err := client.GetFiles(ctx, parentID)
		if err != nil {
			return 0, err
		}
		ids := make([]string, 0, len(files))
		for _, f := range files {
			if nameMatchesTitle(f.FileName, title) {
				ids = append(ids, f.FileID)
			}
		}
		if len(ids) == 0 {
			return 0, nil
		}
		return len(ids), client.Delete(ctx, ids)
	}
	return 0, fmt.Errorf("不支持的网盘类型：%s", sourceType)
}

// nameMatchesTitle 文件名与旧版本标题匹配（精确或带规格后缀前缀）
func nameMatchesTitle(name, title string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	t := strings.ToLower(strings.TrimSpace(title))
	if n == "" || t == "" {
		return false
	}
	if n == t {
		return true
	}
	if strings.HasPrefix(n, t+".") || strings.HasPrefix(t, n+".") {
		return true
	}
	return false
}

// recordToSpec 将转存记录的规格字段还原为 MediaSpec
func recordToSpec(r *models.CloudTransferRecord) MediaSpec {
	return MediaSpec{
		Resolution: r.Resolution,
		Source:     r.Source,
		Codec:      r.Codec,
		Effect:     r.Effect,
		SizeGB:     r.SizeGB,
	}
}
