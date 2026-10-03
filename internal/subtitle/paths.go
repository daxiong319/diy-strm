package subtitle

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SubtitleFileNameForVideo 按策略给出视频对应的字幕文件路径。
//
// policy:
//   - "subtitle"：放到视频目录下的 subtitle/ 子目录
//   - 其它（含空、"same"）：与视频同目录同名
func SubtitleFileNameForVideo(videoPath string, format SubtitleFormat, policy string) string {
	if format == "" {
		format = FormatSRT
	}
	dir := filepath.Dir(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	if policy == "subtitle" {
		return filepath.Join(dir, "subtitle", base+"."+string(format))
	}
	return filepath.Join(dir, base+"."+string(format))
}

// copyFile 复制文件（用于校正前的原字幕备份）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开源文件失败：%w", err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("创建目标目录失败：%w", err)
	}
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("创建目标文件失败：%w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("复制文件失败：%w", err)
	}
	return out.Close()
}

// FileExists 判断路径是否是一个存在的普通文件。
func FileExists(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// FindSubtitleForVideo 按格式优先级找与视频同名的字幕文件。
func FindSubtitleForVideo(videoPath, dirPolicy string) string {
	for _, format := range []SubtitleFormat{FormatSRT, FormatASS, FormatSSA, FormatVTT, FormatSUB, FormatSUP} {
		candidate := SubtitleFileNameForVideo(videoPath, format, dirPolicy)
		if FileExists(candidate) {
			return candidate
		}
	}
	return ""
}
