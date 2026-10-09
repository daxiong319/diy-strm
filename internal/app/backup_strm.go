package app

import (
	"context"
	"strings"

	"litepan/internal/backuprestore"
	"litepan/internal/domain"
	"litepan/internal/driver"
	"litepan/internal/file"
)

// backupSTRMAdapter 把 backuprestore 需要的上传能力接到网盘驱动上（T14）。
//
// 放这里而不是 backuprestore 包内，是因为 backuprestore 至今不依赖网盘驱动：
// 引入驱动栈只为恢复几百个 .strm，会把「备份能不能用」和「网盘能不能连上」
// 缠在一起 —— 网盘凭据过期时，本地恢复也不该跟着废掉。
type backupSTRMAdapter struct {
	files *file.Service
}

func (a backupSTRMAdapter) UploadSTRMFile(ctx context.Context, accountID int64, req backuprestore.STRMUploadRequest) (*backuprestore.STRMUploadResult, error) {
	if a.files == nil {
		return nil, domain.Errorf(domain.CodeInternal, "网盘上传未就绪")
	}
	policy := strings.TrimSpace(req.ConflictPolicy)
	if policy == "" {
		policy = "overwrite"
	}
	res, err := a.files.UploadLocal(ctx, accountID, driver.LocalUploadRequest{
		LocalPath:      req.LocalPath,
		FileName:       req.FileName,
		ParentID:       req.ParentID,
		ConflictPolicy: policy,
		ModTime:        req.ModTime,
	})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	return &backuprestore.STRMUploadResult{
		FileID:   res.FileID,
		ParentID: res.ParentID,
		FileName: res.FileName,
		Size:     res.Size,
		Skipped:  res.Skipped,
	}, nil
}

// EnsureSTRMDirectory 恢复时目录已存在就复用，绝不覆盖。
//
// 这里的「复用」是必须的而不是优化：网盘目录 ID 由服务端生成，
// 恢复时若对已存在的同名目录做覆盖/改名，同一份备份恢复两次就会多出一层
// 「未识别 (1)」，用户看到的目录树和备份时完全不是一回事。
func (a backupSTRMAdapter) EnsureSTRMDirectory(ctx context.Context, accountID int64, parentID, name string) (string, error) {
	if a.files == nil {
		return "", domain.Errorf(domain.CodeInternal, "网盘上传未就绪")
	}
	existing, err := a.files.List(ctx, accountID, parentID, false)
	if err == nil {
		for _, item := range existing {
			if item.IsDir && item.Name == name {
				return item.ID, nil
			}
		}
	}
	created, err := a.files.CreateFolder(ctx, accountID, parentID, name)
	if err != nil {
		return "", err
	}
	if created == nil || created.ID == "" {
		return "", domain.Errorf(domain.CodeInternal, "创建网盘目录失败：%s", name)
	}
	return created.ID, nil
}
