// Package litepan 把 LitePan 驱动适配为 cas.RapidDriver，让 CAS 秒传走 LitePan 驱动体系。
package litepan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"litepan/internal/cas"
	"litepan/internal/cas/cloud189"
	"litepan/internal/domain"
	"litepan/internal/driver"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// Adapter 持有 driver.Manager，按账号解析 LitePan 驱动并包装成 cas.RapidDriver。
type Adapter struct {
	Manager *driver.Manager
	Account func(ctx context.Context, id int64) (*domain.Account, error)
}

// compile-time 检查
var _ = cas.RapidDriver(&rapidDriver{})

// Resolve 实现 cas 的 driverResolver：按账号 ID + sourceType 取网盘适配器。
func (a *Adapter) Resolve(ctx context.Context, accountID int64, sourceType string) cas.RapidDriver {
	if a == nil || a.Manager == nil {
		return nil
	}
	return &rapidDriver{mgr: a.Manager, accountID: accountID, sourceType: strings.ToLower(strings.TrimSpace(sourceType))}
}

// rapidDriver 包装一个 LitePan 驱动实例为 cas.RapidDriver。
type rapidDriver struct {
	mgr       *driver.Manager
	accountID int64
	// driverType 是 LitePan 驱动类型（189cloud/139_cloud/quark），用于构造五哈希/秒传分发。
	sourceType string
}

// SourceType 返回驱动类型标识
func (r *rapidDriver) SourceType() string { return r.sourceType }

// FetchFingerprint 免下载取五哈希：从驱动 ListFiles/GetFileInfo 的 FileItem.Hash 取。
func (r *rapidDriver) FetchFingerprint(ctx context.Context, parentID, fileID, fileName string) (cloud189.HashSet, bool) {
	var hs cloud189.HashSet
	drv, err := r.mgr.Get(ctx, r.accountID)
	if err != nil || drv == nil {
		return hs, false
	}
	// 优先 GetFileInfo 精确取；失败则 ListFiles 按 fileID/名称匹配
	if ig, ok := drv.(driver.InfoGetter); ok {
		if item, ierr := ig.GetFileInfo(ctx, fileID); ierr == nil && item != nil && item.Hash != nil {
			hs = hashSetFromFileItem(item)
			if !hashSetEmpty(hs) {
				return hs, true
			}
		}
	}
	lister, ok := drv.(driver.Lister)
	if !ok {
		return hs, false
	}
	items, lerr := lister.ListFiles(ctx, parentID)
	if lerr != nil {
		return hs, false
	}
	for _, it := range items {
		if it.ID == fileID || it.Name == fileName {
			hs = hashSetFromFileItem(&it)
			return hs, !hashSetEmpty(hs)
		}
	}
	return hs, false
}

// DeleteFile 删除云端文件
func (r *rapidDriver) DeleteFile(ctx context.Context, fileID string) error {
	drv, err := r.mgr.Get(ctx, r.accountID)
	if err != nil || drv == nil {
		return fmt.Errorf("驱动不可用: %w", err)
	}
	del, ok := drv.(driver.Deleter)
	if !ok {
		return fmt.Errorf("驱动 %s 不支持删除", r.sourceType)
	}
	return del.DeleteFiles(ctx, []string{fileID})
}

// UploadTextFile CAS 写回 .cas 清单。LitePan 驱动无通用文本上传入口，留空（DB 已有 casContent 双保险）。
func (r *rapidDriver) UploadTextFile(ctx context.Context, parentID, fileName, content string) (string, error) {
	return "", nil
}

// ReplayRapid 重放秒传请求原文。优先按 sourceType 用五哈希走 MultiHasher；
// 秒传请求原文里的 params 反解出哈希再调 RapidUploadByHashes。
func (r *rapidDriver) ReplayRapid(ctx context.Context, payload, targetFolderID string) (string, error) {
	drv, err := r.mgr.Get(ctx, r.accountID)
	if err != nil || drv == nil {
		return "", fmt.Errorf("驱动不可用: %w", err)
	}
	mh, ok := drv.(driver.MultiHasher)
	if !ok {
		return "", fmt.Errorf("驱动 %s 未实现 MultiHasher 秒传", r.sourceType)
	}
	req, err := rapidRequestFromPayload(payload)
	if err != nil {
		return "", err
	}
	res, err := mh.RapidUploadByHashes(ctx, driver.RapidUploadByHashesRequest{
		ParentID: targetFolderID,
		FileName: req.Name,
		Size:     req.Size,
		Hashes:   req.Hashes,
	})
	if err != nil {
		return "", err
	}
	if res == nil || !res.Reuse {
		return "", fmt.Errorf("秒传未命中（云端已无该哈希文件）")
	}
	return res.FileID, nil
}

// rapidRequest 反解秒传请求原文（cloud-auto-save-x 同款 JSON）
type rapidRequest struct {
	DriveType string                  `json:"drive_type"`
	Name      string                  `json:"name"`
	Size      int64                   `json:"size"`
	Params    map[string]any          `json:"params"`
	Hashes    map[domain.HashType]string `json:"-"`
}

func rapidRequestFromPayload(payload string) (*rapidRequest, error) {
	var rr rapidRequest
	if err := jsonUnmarshal([]byte(payload), &rr); err != nil {
		return nil, fmt.Errorf("秒传请求原文解析失败: %w", err)
	}
	if strings.TrimSpace(rr.Name) == "" {
		return nil, fmt.Errorf("秒传请求原文缺少 name")
	}
	rr.Hashes = map[domain.HashType]string{}
	p := rr.Params
	put := func(key string, ht domain.HashType) {
		if v, ok := p[key].(string); ok && strings.TrimSpace(v) != "" {
			rr.Hashes[ht] = strings.ToLower(strings.TrimSpace(v))
		}
	}
	put("fileMd5", domain.HashMD5)
	put("sliceMd5", domain.HashSliceMD5)
	put("file_md5", domain.HashMD5)
	put("contentHash", domain.HashSHA256)
	put("sha1", domain.HashSHA1)
	put("gcid", domain.HashGcid)
	return &rr, nil
}

func hashSetFromFileItem(item *domain.FileItem) cloud189.HashSet {
	var hs cloud189.HashSet
	if item == nil || item.Hash == nil {
		return hs
	}
	get := func(ht domain.HashType) string { return strings.ToLower(strings.TrimSpace(item.Hash[ht])) }
	hs.FileMd5 = get(domain.HashMD5)
	hs.SliceMd5 = get(domain.HashSliceMD5)
	hs.Sha1 = get(domain.HashSHA1)
	hs.Sha256 = get(domain.HashSHA256)
	hs.PreHash = get(domain.HashPreHash)
	hs.Gcid = get(domain.HashGcid)
	return hs
}

func hashSetEmpty(hs cloud189.HashSet) bool {
	return hs.FileMd5 == "" && hs.SliceMd5 == "" && hs.Sha1 == "" &&
		hs.Sha256 == "" && hs.PreHash == "" && hs.Gcid == ""
}
