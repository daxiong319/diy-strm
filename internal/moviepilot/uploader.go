package moviepilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"diy-strm/internal/helpers"
	"diy-strm/internal/pan139"
)

// LocalFile 待上传的本地文件
type LocalFile struct {
	AbsPath string // 本地绝对路径
	RelPath string // 相对源目录的路径（含文件名）
	Size    int64
}

// skipUploadSuffix 未完成下载文件的临时后缀
var skipUploadSuffix = []string{".!qB", ".part", ".partial", ".download", ".opdownload"}

// CollectLocalFiles 递归收集本地目录中的普通文件
func CollectLocalFiles(root string) ([]LocalFile, error) {
	var files []LocalFile
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		for _, suffix := range skipUploadSuffix {
			if strings.HasSuffix(strings.ToLower(name), strings.ToLower(suffix)) {
				helpers.AppLogger.Debugf("MoviePilot 上传跳过未完成文件：%s", path)
				return nil
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, LocalFile{AbsPath: path, RelPath: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("源目录 %s 中没有可上传的文件", root)
	}
	return files, nil
}

// EnsureRemotePath 确保 139 上存在该路径（逐级创建），返回最终目录 ID
func EnsureRemotePath(ctx context.Context, client *pan139.Client, path string) (string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "root", nil
	}
	parentID := "root"
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		files, err := client.GetFiles(ctx, parentID)
		if err != nil {
			return "", err
		}
		found := ""
		for _, f := range files {
			if f.IsDir() && f.FileName == part {
				found = f.GetID()
				break
			}
		}
		if found == "" {
			found, err = client.CreateDir(ctx, parentID, part)
			if err != nil {
				return "", fmt.Errorf("创建 139 目录 %s 失败：%v", part, err)
			}
			helpers.AppLogger.Infof("MoviePilot 上传：已在 139 创建目录 %s", part)
		}
		parentID = found
	}
	return parentID, nil
}

// UploadProgress 上传进度回调
type UploadProgress struct {
	FileIndex    int
	TotalFiles   int
	UploadedBytes int64
	TotalBytes   int64
	CurrentName  string
}

// UploadLocalDirToPan139 把本地目录完整上传到 139 指定目录（保留相对结构），返回上传文件数
// progress 可为 nil；secUploadRapid 命中时该文件仍需计入已上传数
func UploadLocalDirToPan139(ctx context.Context, client *pan139.Client, localRoot, remotePath string, progress func(*UploadProgress)) (int, error) {
	files, err := CollectLocalFiles(localRoot)
	if err != nil {
		return 0, err
	}
	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
	}

	targetID, err := EnsureRemotePath(ctx, client, remotePath)
	if err != nil {
		return 0, err
	}

	uploaded := 0
	var uploadedBytes int64
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return uploaded, err
		}
		dir := filepath.ToSlash(filepath.Dir(f.RelPath))
		parentID := targetID
		if dir != "." && dir != "" {
			parentID, err = EnsureRemotePath(ctx, client, strings.TrimRight(remotePath, "/")+"/"+dir)
			if err != nil {
				return uploaded, fmt.Errorf("创建上传子目录 %s 失败：%v", dir, err)
			}
		}
		name := filepath.Base(f.RelPath)

		// 计算 SHA256（秒传需要）
		hash, err := fileSha256(f.AbsPath)
		if err != nil {
			return uploaded, fmt.Errorf("计算 %s 的 SHA256 失败：%v", f.AbsPath, err)
		}
		file, err := os.Open(f.AbsPath)
		if err != nil {
			return uploaded, fmt.Errorf("打开文件 %s 失败：%v", f.AbsPath, err)
		}
		_, _, _, err = client.UploadFile(ctx, parentID, name, f.Size, hash, file, nil)
		file.Close()
		if err != nil {
			return uploaded, fmt.Errorf("上传 %s 失败：%v", f.AbsPath, err)
		}
		uploaded++
		uploadedBytes += f.Size
		if progress != nil {
			progress(&UploadProgress{FileIndex: i + 1, TotalFiles: len(files), UploadedBytes: uploadedBytes, TotalBytes: totalBytes, CurrentName: name})
		}
		helpers.AppLogger.Infof("MoviePilot 上传进度：%d/%d %s（%.1f%%）", i+1, len(files), name, float64(uploadedBytes)*100/float64(totalBytes))
	}
	return uploaded, nil
}

// fileSha256 计算文件 SHA256
func fileSha256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}