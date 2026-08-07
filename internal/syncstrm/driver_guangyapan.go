package syncstrm

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"

	"diy-strm/internal/baidupan"
	"diy-strm/internal/guangyapan"
	"diy-strm/internal/models"
	"diy-strm/internal/v115open"
)

// guangYaPanDriver 光鸭云盘驱动
// 列表接口为分页式（/userres/v1/file/get_file_list），下载链接使用文件 ID 作为 PickCode
type guangYaPanDriver struct {
	s      *SyncStrm
	client *guangyapan.Client
}

func NewGuangYaPanDriver(client *guangyapan.Client) *guangYaPanDriver {
	return &guangYaPanDriver{
		client: client,
	}
}

func (d *guangYaPanDriver) SetSyncStrm(s *SyncStrm) {
	d.s = s
}

// GetNetFileFiles 获取目录下的全部文件和子目录
func (d *guangYaPanDriver) GetNetFileFiles(ctx context.Context, parentPath, parentPathId string) ([]*SyncFileCache, error) {
	select {
	case <-ctx.Done():
		d.s.Sync.Logger.Infof("获取光鸭云盘文件列表的上下文已取消，path=%s", parentPath)
		return nil, ctx.Err()
	default:
	}
	files, err := d.client.GetFiles(ctx, parentPathId)
	if err != nil {
		d.s.Sync.Logger.Errorf("获取光鸭云盘文件列表失败：目录 %s（%s），%v", parentPath, parentPathId, err)
		return nil, err
	}
	fileItems := make([]*SyncFileCache, 0, len(files))
	for _, file := range files {
		atomic.AddInt64(&d.s.TotalFile, 1)
		d.s.PublishProgress(false)
		fileItem := SyncFileCache{
			ParentId:   parentPathId,
			FileId:     file.GetID(),
			PickCode:   file.GetID(),
			Path:       parentPath,
			FileName:   file.FileName,
			FileType:   v115open.TypeFile,
			FileSize:   file.FileSize,
			MTime:      file.UTime,
			SourceType: models.SourceTypeGuangYaPan,
		}
		if file.IsDir() {
			fileItem.FileType = v115open.TypeDir
			fileItem.IsVideo = false
			fileItem.IsMeta = false
		}
		fileItems = append(fileItems, &fileItem)
	}
	return fileItems, nil
}

// CreateDirRecursively 检查每一级目录是否存在，不存在则创建
// 返回最终目录 ID 与远程路径
func (d *guangYaPanDriver) CreateDirRecursively(ctx context.Context, path string) (pathId, remotePath string, err error) {
	relPath, err := filepath.Rel(d.s.TargetPath, path)
	if err != nil {
		return "", "", fmt.Errorf("计算相对路径失败：%s，错误：%v", path, err)
	}
	relPath = filepath.ToSlash(relPath)
	if !strings.HasPrefix(relPath, "/") {
		relPath = "/" + relPath
	}
	pathParts := strings.Split(relPath, "/")
	// 反向检查，找到哪一级不存在，再正向创建
	notExistIndex := -1
	lastExistsPathId := ""
	for i := len(pathParts) - 1; i >= 0; i-- {
		dir := filepath.Join(pathParts[:i+1]...)
		fsId, err := d.client.GetPathIdByPath(ctx, dir)
		if err != nil || fsId == "" {
			notExistIndex = i
			continue
		}
		lastExistsPathId = fsId
		break
	}
	if notExistIndex == -1 {
		return lastExistsPathId, relPath, nil
	}
	// 正向创建
	for i := notExistIndex + 1; i <= len(pathParts); i++ {
		dir := filepath.Join(pathParts[:i]...)
		currentFileId, err := d.client.CreateDir(ctx, lastExistsPathId, filepath.Base(dir))
		if err != nil {
			return "", "", fmt.Errorf("创建光鸭云盘目录失败：%s，错误：%v", dir, err)
		}
		// 将新添加的目录加入同步缓存
		syncFileCache := &SyncFileCache{
			FileId:     currentFileId,
			ParentId:   lastExistsPathId,
			Path:       filepath.ToSlash(filepath.Dir(dir)),
			FileName:   filepath.Base(dir),
			FileType:   v115open.TypeDir,
			IsVideo:    false,
			IsMeta:     false,
			SourceType: models.SourceTypeGuangYaPan,
		}
		syncFileCache.GetLocalFilePath(d.s.TargetPath, d.s.SourcePath)
		d.s.memSyncCache.Insert(syncFileCache)
		d.s.Sync.Logger.Infof("创建光鸭云盘目录成功：%s，目录 ID：%s", dir, currentFileId)
		lastExistsPathId = currentFileId
	}
	return lastExistsPathId, relPath, nil
}

func (d *guangYaPanDriver) GetPathIdByPath(ctx context.Context, path string) (string, error) {
	// 同步入口：优先使用保存的目录 ID（base_cid）定位，避免按名称从根目录逐级查找失败
	// 光鸭 API 不支持按路径查询，前端保存的 remote_path 可能只是目录短名（非完整路径）
	if path == d.s.SourcePath && d.s.SourcePathId != "" {
		// 验证目录仍然存在：能成功列出子项即有效（空目录同样合法）
		if _, err := d.client.GetFiles(ctx, d.s.SourcePathId); err == nil {
			return d.s.SourcePathId, nil
		} else {
			d.s.Sync.Logger.Warnf("光鸭云盘入口目录 %s（ID=%s）已无法访问，回退按名称查找：%v", path, d.s.SourcePathId, err)
		}
	}
	fsId, err := d.client.GetPathIdByPath(ctx, path)
	if err != nil {
		return "", err
	}
	return fsId, nil
}

func (d *guangYaPanDriver) MakeStrmContent(sf *SyncFileCache) string {
	// 生成 URL
	u, err := url.Parse(d.s.Config.StrmBaseUrl)
	if err != nil {
		d.s.Sync.Logger.Errorf("解析 STRM 直连地址失败：%s，错误：%v", d.s.Config.StrmBaseUrl, err)
		return ""
	}
	ext := filepath.Ext(sf.FileName)
	u.Path = fmt.Sprintf("/guangyapan/url/video%s", ext)
	params := url.Values{}
	params.Add("pickcode", sf.PickCode)
	params.Add("userid", d.s.Account.UserId)
	if pathValue := strmPathQueryValue(d.s.Config.StrmUrlNeedPath, sf); pathValue != "" {
		params.Add("path", pathValue)
	}
	u.RawQuery = encodeStrmQueryPathLast(params)
	return u.String()
}

func (d *guangYaPanDriver) GetTotalFileCount(ctx context.Context) (int64, string, error) {
	return 0, "", nil
}

func (d *guangYaPanDriver) GetDirsByPathId(ctx context.Context, pathId string) ([]pathQueueItem, error) {
	return nil, nil
}

func (d *guangYaPanDriver) GetFilesByPathId(ctx context.Context, rootPathId string, offset, limit int) ([]v115open.File, error) {
	return nil, nil
}

// DetailByFileId 获取单个文件详情（同步单个文件场景）
// 源路径的父目录即文件所在目录，先在父目录列表中定位文件
func (d *guangYaPanDriver) DetailByFileId(ctx context.Context, fileId string) (*SyncFileCache, error) {
	parentPath := filepath.ToSlash(filepath.Dir(d.s.SourcePath))
	parentId, err := d.client.GetPathIdByPath(ctx, parentPath)
	if err != nil {
		return nil, fmt.Errorf("获取目录 %s 失败：%v", parentPath, err)
	}
	files, err := d.client.GetFiles(ctx, parentId)
	if err != nil {
		return nil, fmt.Errorf("获取目录 %s 文件列表失败：%v", parentPath, err)
	}
	for i := range files {
		if files[i].GetID() != fileId {
			continue
		}
		file := files[i]
		fileItem := &SyncFileCache{
			FileId:     file.GetID(),
			FileName:   file.FileName,
			FileType:   v115open.TypeFile,
			SourceType: models.SourceTypeGuangYaPan,
			Path:       parentPath,
			ParentId:   parentId,
			MTime:      file.UTime,
			FileSize:   file.FileSize,
			IsVideo:    false,
			IsMeta:     false,
			Paths:      []v115open.FileDetailPath{},
		}
		if file.IsDir() {
			fileItem.FileType = v115open.TypeDir
			fileItem.IsVideo = false
			fileItem.IsMeta = false
		} else {
			fileItem.PickCode = file.GetID()
			fileItem.IsVideo = d.s.IsValidVideoExt(fileItem.FileName)
			fileItem.IsMeta = d.s.IsValidMetaExt(fileItem.FileName)
		}
		return fileItem, nil
	}
	return nil, fmt.Errorf("文件 %s 不存在于目录 %s", fileId, parentPath)
}

// DeleteFile 删除目录下的某些文件（放入回收站）
func (d *guangYaPanDriver) DeleteFile(ctx context.Context, parentId string, fileIds []string) error {
	return d.client.Delete(ctx, fileIds)
}

func (d *guangYaPanDriver) GetFilesByPathMtime(ctx context.Context, rootPathId string, offset, limit int, mtime int64) (*baidupan.FileListAllResponse, error) {
	return nil, nil
}
