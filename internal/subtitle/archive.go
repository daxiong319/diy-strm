package subtitle

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxArchiveEntries 是压缩包内条目数的上限。
// 中文站点字幕包通常"一部影片几个语言"；设上限是防 zip bomb 的第一道防线。
const maxArchiveEntries = 64

// unpackSubtitleArchive 从 zip 中挑一个字幕文件解到 destPath。
// want 非空时优先挑格式相符的，否则挑路径层级最浅的。
func unpackSubtitleArchive(data []byte, destPath string, want SubtitleFormat) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("解析字幕压缩包失败：%w", err)
	}
	if len(reader.File) == 0 {
		return errors.New("字幕压缩包为空")
	}
	if len(reader.File) > maxArchiveEntries {
		return fmt.Errorf("字幕压缩包内条目过多（%d），已拒绝解包", len(reader.File))
	}

	type entry struct {
		file  *zip.File
		name  string
		depth int
	}
	var candidates []entry
	for _, f := range reader.File {
		name := f.Name
		if f.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(name)
		if strings.HasPrefix(base, "._") || base == ".DS_Store" {
			continue
		}
		if DetectFormat(name) == "" {
			continue
		}
		candidates = append(candidates, entry{
			file:  f,
			name:  name,
			depth: strings.Count(filepath.ToSlash(name), "/"),
		})
	}
	if len(candidates) == 0 {
		return errors.New("字幕压缩包内没有可识别的字幕文件")
	}

	pick := candidates[0]
	if want != "" {
		for _, c := range candidates {
			if DetectFormat(c.name) == want {
				pick = c
				break
			}
		}
	} else {
		for _, c := range candidates {
			if c.depth < pick.depth {
				pick = c
			}
		}
	}

	rc, err := pick.file.Open()
	if err != nil {
		return fmt.Errorf("打开压缩包内字幕文件失败：%w", err)
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("创建字幕目录失败：%w", err)
	}
	tmp := destPath + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建临时字幕文件失败：%w", err)
	}
	// 单条目同样限流，防止压缩包内单个文件撑爆磁盘。
	written, copyErr := io.Copy(out, io.LimitReader(rc, maxSubtitleFileSize))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("解压字幕文件失败：%w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("关闭字幕文件失败：%w", closeErr)
	}
	if written == 0 {
		_ = os.Remove(tmp)
		return errors.New("解压出的字幕内容为空")
	}
	if err := os.Rename(tmp, destPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存字幕文件失败：%w", err)
	}
	return nil
}

// writeSubtitlePayload 按内容魔数决定解包还是直写。
// 统一在这里判断，使新增来源不必各自处理"到底是不是压缩包"。
func writeSubtitlePayload(data []byte, destPath string, want SubtitleFormat) error {
	if len(data) == 0 {
		return errors.New("字幕内容为空")
	}
	if isZipPayload(data) {
		return unpackSubtitleArchive(data, destPath, want)
	}
	if isRarPayload(data) {
		// rar 解包需外部 unrar，项目内不引入额外二进制依赖。
		return errors.New("字幕包为 RAR 格式，本模块不支持解包（站点已改版或该条目仅提供 rar）")
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("创建字幕目录失败：%w", err)
	}
	tmp := destPath + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入字幕文件失败：%w", err)
	}
	if err := os.Rename(tmp, destPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存字幕文件失败：%w", err)
	}
	return nil
}

func isZipPayload(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' &&
		(data[2] == 3 || data[2] == 5 || data[2] == 7)
}

func isRarPayload(data []byte) bool {
	return len(data) >= 7 && data[0] == 'R' && data[1] == 'a' && data[2] == 'r' &&
		data[3] == '!' && data[4] == 0x1a && data[5] == 0x07
}
