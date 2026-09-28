package syncstrm

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"diy-strm/internal/baidupan"
	"diy-strm/internal/cloud189"
	"diy-strm/internal/quark"
	"diy-strm/internal/v115open"
)

// cloud189Driver 天翼云盘驱动
// 列表走 /api/open/file/listFiles.action，直链走 getNewVlcVideoPlayUrl 302
type cloud189Driver struct {
	s      *SyncStrm
	client *cloud189.Client
}

// NewCloud189Driver 天翼云盘 STRM 驱动
func NewCloud189Driver(client *cloud189.Client) *cloud189Driver {
	return &cloud189Driver{client: client}
}

func (d *cloud189Driver) SetSyncStrm(s *SyncStrm) {
	d.s = s
}

// GetNetFileFiles 获取目录下的全部文件和子目录
func (d *cloud189Driver) GetNetFileFiles(ctx context.Context, parentPath, parentPathId string) ([]*SyncFileCache, error) {
	select {
	case <-ctx.Done():
		d.s.Sync.Logger.Infof("获取天翼云盘文件列表的上下文已取消，path=%s", parentPath)
		return nil, ctx.Err()
	default:
	}
	files, err := d.client.ListFiles(ctx, parentPathId)
	if err != nil {
		d.s.Sync.Logger.Errorf("获取天翼云盘文件列表失败：目录 %s（%s），%v", parentPath, parentPathId, err)
		return nil, err
	}
	items := make([]*SyncFileCache, 0, len(files))
	for _, f := range files {
		atomic.AddInt64(&d.s.TotalFile, 1)
		d.s.PublishProgress(false)
		fileType := v115open.TypeFile
		if f.IsDir {
			fileType = v115open.TypeDir
		}
		item := &SyncFileCache{
			ParentId: parentPathId,
			FileId:   f.ID,
			PickCode: f.ID,
			Path:     parentPath,
			FileName: f.Name,
			FileType: fileType,
			FileSize: f.Size,
			MTime:    f.LastOpTime.Unix(),
		}
		items = append(items, item)
	}
	return items, nil
}

// CreateDirRecursively 递归创建目录
func (d *cloud189Driver) CreateDirRecursively(ctx context.Context, path string) (string, string, error) {
	pathId, err := d.client.EnsureFolderPath(ctx, "-11", path)
	if err != nil {
		return "", "", err
	}
	return pathId, path, nil
}

// GetPathIdByPath 路径转 ID
func (d *cloud189Driver) GetPathIdByPath(ctx context.Context, path string) (string, error) {
	return d.client.EnsureFolderPath(ctx, "-11", path)
}

// MakeStrmContent 生成 STRM 内容（URL 指向 /cloud189/url/video.ext）
func (d *cloud189Driver) MakeStrmContent(sf *SyncFileCache) string {
	u, err := url.Parse(d.s.Config.StrmBaseUrl)
	if err != nil {
		d.s.Sync.Logger.Errorf("解析 STRM 直连地址失败：%s，错误：%v", d.s.Config.StrmBaseUrl, err)
		return ""
	}
	ext := filepath.Ext(sf.FileName)
	u.Path = fmt.Sprintf("/cloud189/url/video%s", ext)
	params := url.Values{}
	params.Add("pickcode", sf.PickCode)
	if d.s.Account != nil && d.s.Account.ID > 0 {
		params.Add("account", strconv.FormatUint(uint64(d.s.Account.ID), 10))
	}
	if pathValue := strmPathQueryValue(d.s.Config.StrmUrlNeedPath, sf); pathValue != "" {
		params.Add("path", pathValue)
	}
	u.RawQuery = encodeStrmQueryPathLast(params)
	return u.String()
}

func (d *cloud189Driver) GetTotalFileCount(ctx context.Context) (int64, string, error) {
	return 0, "", nil
}

func (d *cloud189Driver) GetDirsByPathId(ctx context.Context, pathId string) ([]pathQueueItem, error) {
	files, err := d.client.ListFiles(ctx, pathId)
	if err != nil {
		return nil, err
	}
	items := make([]pathQueueItem, 0)
	for _, f := range files {
		if f.IsDir {
			items = append(items, pathQueueItem{PathId: f.ID, Path: f.Name})
		}
	}
	return items, nil
}

func (d *cloud189Driver) GetFilesByPathId(ctx context.Context, rootPathId string, offset, limit int) ([]v115open.File, error) {
	files, err := d.client.ListFiles(ctx, rootPathId)
	if err != nil {
		return nil, err
	}
	out := make([]v115open.File, 0, len(files))
	for _, f := range files {
		if f.IsDir {
			continue
		}
		out = append(out, v115open.File{FileId: f.ID, FileName: f.Name, FileSize: f.Size})
	}
	if offset >= len(out) {
		return nil, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], nil
}

func (d *cloud189Driver) DetailByFileId(ctx context.Context, fileId string) (*SyncFileCache, error) {
	// 天翼按 fileID 查详情走 searchFiles；简化：列出父目录匹配（上层会用 path 参数）
	return nil, fmt.Errorf("天翼暂不支持按 fileId 查详情（依赖 path 参数）")
}

func (d *cloud189Driver) DeleteFile(ctx context.Context, parentId string, fileIds []string) error {
	for _, id := range fileIds {
		if err := d.client.DeleteFile(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (d *cloud189Driver) GetFilesByPathMtime(ctx context.Context, rootPathId string, offset, limit int, mtime int64) (*baidupan.FileListAllResponse, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// 夸克驱动
// ---------------------------------------------------------------------------

type quarkDriver struct {
	s      *SyncStrm
	client *quark.Client
}

// NewQuarkDriver 夸克 STRM 驱动
func NewQuarkDriver(client *quark.Client) *quarkDriver {
	return &quarkDriver{client: client}
}

func (d *quarkDriver) SetSyncStrm(s *SyncStrm) {
	d.s = s
}

func (d *quarkDriver) GetNetFileFiles(ctx context.Context, parentPath, parentPathId string) ([]*SyncFileCache, error) {
	select {
	case <-ctx.Done():
		d.s.Sync.Logger.Infof("获取夸克文件列表的上下文已取消，path=%s", parentPath)
		return nil, ctx.Err()
	default:
	}
	files, err := d.client.ListFiles(ctx, parentPathId)
	if err != nil {
		d.s.Sync.Logger.Errorf("获取夸克文件列表失败：目录 %s（%s），%v", parentPath, parentPathId, err)
		return nil, err
	}
	items := make([]*SyncFileCache, 0, len(files))
	for _, f := range files {
		atomic.AddInt64(&d.s.TotalFile, 1)
		d.s.PublishProgress(false)
		fileType := v115open.TypeFile
		if f.IsDir {
			fileType = v115open.TypeDir
		}
		items = append(items, &SyncFileCache{
			ParentId: parentPathId,
			FileId:   f.Fid,
			PickCode: f.Fid,
			Path:     parentPath,
			FileName: f.Name,
			FileType: fileType,
			FileSize: f.Size,
			MTime:    0,
		})
	}
	return items, nil
}

func (d *quarkDriver) CreateDirRecursively(ctx context.Context, path string) (string, string, error) {
	pathId, err := quarkEnsureFolderPathSync(ctx, d.client, "0", path)
	if err != nil {
		return "", "", err
	}
	return pathId, path, nil
}

func (d *quarkDriver) GetPathIdByPath(ctx context.Context, path string) (string, error) {
	return quarkEnsureFolderPathSync(ctx, d.client, "0", path)
}

func (d *quarkDriver) MakeStrmContent(sf *SyncFileCache) string {
	u, err := url.Parse(d.s.Config.StrmBaseUrl)
	if err != nil {
		d.s.Sync.Logger.Errorf("解析 STRM 直连地址失败：%s，错误：%v", d.s.Config.StrmBaseUrl, err)
		return ""
	}
	ext := filepath.Ext(sf.FileName)
	u.Path = fmt.Sprintf("/quark/url/video%s", ext)
	params := url.Values{}
	params.Add("pickcode", sf.PickCode)
	if d.s.Account != nil && d.s.Account.ID > 0 {
		params.Add("account", strconv.FormatUint(uint64(d.s.Account.ID), 10))
	}
	if pathValue := strmPathQueryValue(d.s.Config.StrmUrlNeedPath, sf); pathValue != "" {
		params.Add("path", pathValue)
	}
	u.RawQuery = encodeStrmQueryPathLast(params)
	return u.String()
}

func (d *quarkDriver) GetTotalFileCount(ctx context.Context) (int64, string, error) {
	return 0, "", nil
}

func (d *quarkDriver) GetDirsByPathId(ctx context.Context, pathId string) ([]pathQueueItem, error) {
	files, err := d.client.ListFiles(ctx, pathId)
	if err != nil {
		return nil, err
	}
	items := make([]pathQueueItem, 0)
	for _, f := range files {
		if f.IsDir {
			items = append(items, pathQueueItem{PathId: f.Fid, Path: f.Name})
		}
	}
	return items, nil
}

func (d *quarkDriver) GetFilesByPathId(ctx context.Context, rootPathId string, offset, limit int) ([]v115open.File, error) {
	files, err := d.client.ListFiles(ctx, rootPathId)
	if err != nil {
		return nil, err
	}
	out := make([]v115open.File, 0, len(files))
	for _, f := range files {
		if f.IsDir {
			continue
		}
		out = append(out, v115open.File{FileId: f.Fid, FileName: f.Name, FileSize: f.Size})
	}
	if offset >= len(out) {
		return nil, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], nil
}

func (d *quarkDriver) DetailByFileId(ctx context.Context, fileId string) (*SyncFileCache, error) {
	return nil, fmt.Errorf("夸克暂不支持按 fileId 查详情（依赖 path 参数）")
}

func (d *quarkDriver) DeleteFile(ctx context.Context, parentId string, fileIds []string) error {
	return d.client.DeleteFile(ctx, fileIds)
}

func (d *quarkDriver) GetFilesByPathMtime(ctx context.Context, rootPathId string, offset, limit int, mtime int64) (*baidupan.FileListAllResponse, error) {
	return nil, nil
}

// quarkEnsureFolderPathSync 夸克逐级确保目录存在（syncstrm 包内独立实现）
func quarkEnsureFolderPathSync(ctx context.Context, client *quark.Client, rootID, path string) (string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return rootID, nil
	}
	current := rootID
	for _, seg := range strings.Split(path, "/") {
		files, err := client.ListFiles(ctx, current)
		if err != nil {
			return "", err
		}
		var found string
		for _, f := range files {
			if f.IsDir && f.Name == seg {
				found = f.Fid
				break
			}
		}
		if found == "" {
			found, err = client.CreateFolder(ctx, current, seg)
			if err != nil {
				return "", err
			}
		}
		current = found
	}
	return current, nil
}
