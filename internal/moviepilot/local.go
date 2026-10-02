package moviepilot

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// LocalFile 待上传的本地文件：源目录下的普通文件及其相对路径。
type LocalFile struct {
	// AbsPath 本地绝对路径。
	AbsPath string
	// RelPath 相对源目录的路径（含文件名，统一为 / 分隔）。
	RelPath string
	// Size 文件字节数。
	Size int64
}

// skipUploadSuffix 未完成下载文件的临时后缀：这类文件仍在写入，上传会得到残缺内容。
var skipUploadSuffix = []string{".!qb", ".part", ".partial", ".download", ".opdownload"}

// isSkipUploadFile 判断文件是否为未完成下载的临时文件。
func isSkipUploadFile(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range skipUploadSuffix {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// CollectLocalFiles 递归收集本地目录中的普通文件（跳过未完成下载的临时文件）。
// 目录本身不存在与目录为空返回同一个结果（空切片 + nil），调用方据此判定「等待落盘」。
func CollectLocalFiles(root string) ([]LocalFile, error) {
	root = strings.TrimRight(strings.TrimSpace(root), "/")
	if root == "" {
		return nil, fmt.Errorf("源目录为空")
	}
	if !pathExists(root) {
		// 目录不存在等同为空：MoviePilot 的完成信号可能早于文件落盘
		return nil, nil
	}
	var files []LocalFile
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if isSkipUploadFile(info.Name()) {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		files = append(files, LocalFile{
			AbsPath: p,
			RelPath: filepath.ToSlash(rel),
			Size:    info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	return files, nil
}

// localDirEmpty 目录内没有任何可上传的普通文件（含目录不存在）。
// MoviePilot 的「下载完成」信号可能早于文件真正落盘（qB 校验/搬移中），
// 空目录只代表文件尚未就绪，不代表应该失败。
func localDirEmpty(localPath string) bool {
	files, err := CollectLocalFiles(localPath)
	return err != nil || len(files) == 0
}

// localDirFingerprint 本地目录文件集指纹（相对路径 + 大小，含未完成临时文件）。
// 用于判定下载是否仍在写入：连续两次扫描指纹一致才认为文件集稳定。
func localDirFingerprint(root string) (string, error) {
	root = strings.TrimRight(strings.TrimSpace(root), "/")
	if root == "" {
		return "", fmt.Errorf("源目录为空")
	}
	var parts []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		parts = append(parts, filepath.ToSlash(rel)+"="+strconv.FormatInt(info.Size(), 10))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(parts)
	return strings.Join(parts, "|"), nil
}

// errEmptySource 源目录当前没有可上传文件。
// 区分两种情况：目录暂无文件（等待落盘，应重试）与有文件但全部已被既有批次处理（正常终态）。
type errEmptySource struct {
	msg  string
	wait bool
}

func (e *errEmptySource) Error() string { return e.msg }

// isErrEmptySource 判断错误是否为「源目录没有可上传文件」。
func isErrEmptySource(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*errEmptySource)
	return ok
}

// isErrEmptySourceWait 判断是否为「目录暂无文件、需等待落盘后重试」。
func isErrEmptySourceWait(err error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*errEmptySource)
	return ok && e.wait
}

// resolveLocalPath 把 MoviePilot 返回的保存路径映射为本地可访问路径。
// 带前缀映射后路径不存在时回退原路径（同机部署时 DownloadRoot 与 LocalViewRoot 一致）。
// contentPath 可能指向单个文件，此时取其所在目录。
func resolveLocalPath(raw, downloadRoot, localViewRoot string) string {
	mapped := mapPathPrefix(raw, downloadRoot, localViewRoot)
	if !pathExists(mapped) && pathExists(raw) {
		mapped = raw
	}
	if info, err := os.Stat(mapped); err == nil && !info.IsDir() {
		mapped = filepath.Dir(mapped)
	}
	if !pathExists(mapped) {
		return ""
	}
	return strings.TrimRight(filepath.ToSlash(mapped), "/")
}

// mapPathPrefix 前缀映射：MoviePilot 侧路径 → 本容器路径。
func mapPathPrefix(raw, from, to string) string {
	if raw == "" {
		return raw
	}
	from = strings.TrimRight(from, "/")
	to = strings.TrimRight(to, "/")
	if from != "" && to != "" && (raw == from || strings.HasPrefix(raw, from+"/")) {
		return to + strings.TrimPrefix(raw, from)
	}
	return raw
}

// pathExists 判断路径是否存在。
func pathExists(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// localFirstChunkMD5 计算本地文件前 n 字节的 MD5（用于网盘端指纹比对）。
func localFirstChunkMD5(path string, n int64) (string, error) {
	if n <= 0 {
		n = 1 << 20
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.CopyN(h, f, n); err != nil && err != io.EOF {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
