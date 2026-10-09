package backuprestore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Uploader 是恢复 STRM 目录到网盘所需的最小能力（T14）。
//
// 刻意只声明这一个方法，而不是依赖 internal/file 或 internal/driver：
// backuprestore 目前是个不依赖网盘驱动的包，引入整个驱动栈只为恢复几个
// .strm 文件会把「备份能不能用」和「网盘能不能连上」绑在一起 —— 网盘挂了
// 就连本地恢复也做不了。装配层（internal/app）负责把真实实现塞进来。
type Uploader interface {
	// UploadSTRMFile 把本地文件上传到网盘指定目录。
	// 返回值里的 FileID 是新文件在网盘上的 ID，调用方用它在下一轮里往下建子目录。
	UploadSTRMFile(ctx context.Context, accountID int64, req STRMUploadRequest) (*STRMUploadResult, error)
	// EnsureSTRMDirectory 确保目录存在，返回目录 ID（已存在时返回原 ID）。
	EnsureSTRMDirectory(ctx context.Context, accountID int64, parentID, name string) (string, error)
}

// STRMUploadRequest 描述一次 STRM 文件上传。
type STRMUploadRequest struct {
	LocalPath      string
	FileName       string
	ParentID       string
	ConflictPolicy string
	ModTime        *time.Time
}

// STRMUploadResult 是一次上传的结果。
type STRMUploadResult struct {
	FileID   string
	ParentID string
	FileName string
	Size     int64
	Skipped  bool
}

// RestoreTarget 说清「恢复到哪里」。
//
// 两种目标不是二选一的对立关系：网盘恢复需要先把文件放到本地暂存区
// （网盘驱动只接受本地文件上传，这是既有约束），所以 local 永远是中间步骤。
// 用户选「网盘」时本地那份会在上传成功后清掉。
type RestoreTarget struct {
	// Local=true 时恢复完就结束，STRM 目录留在本地。
	Local bool `json:"local"`
	// AccountID / ParentID 是网盘目标；两者任一为空表示网盘恢复未配置。
	AccountID int64  `json:"account_id,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
	// AccountName 只用于给用户看的提示。
	AccountName string `json:"account_name,omitempty"`
}

// CloudConfigured 报告网盘恢复是否可用。
func (t RestoreTarget) CloudConfigured() bool {
	return !t.Local && t.AccountID > 0 && strings.TrimSpace(t.ParentID) != ""
}

// uploadSTRMToCloud 把 STRM 文件逐个上传到网盘，目录结构保持不变。
//
// 先建目录再传文件，且目录 ID 用上传返回值继续往下传 —— 网盘目录 ID
// 是服务端生成的，本地路径串不能直接当 ID 用（同一层出现两次同名目录时
// 尤其容易错位）。
//
// 单个文件失败不中断整体：恢复几十个剧集时因为一个改名冲突就全盘失败，
// 用户还得从头再来。失败的条目记在返回值里，由上层如实告诉用户。
func (s *Service) uploadSTRMToCloud(ctx context.Context, target RestoreTarget, files map[string][]byte, stagingDir string) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if s.uploader == nil {
		return nil, fmt.Errorf("网盘恢复未配置")
	}
	// 先把内容落到本地暂存区：驱动只接受「本地文件 → 网盘」这一个方向。
	staged, err := stageSTRMFiles(files, stagingDir)
	if err != nil {
		return nil, err
	}
	// 暂存区用完即删：它和 STRM 目录等大，留着就是一份用户看不见、
	// 又永远不会再被读到的副本。失败了也不要留 —— 上传失败的条目已经
	// 在返回值里列出来，重试要走重新做一次恢复，而不是复用这份暂存。
	defer func() { _ = os.RemoveAll(staged) }()
	dirIDs := map[string]string{"": target.ParentID}
	// 先声明再赋值：闭包要递归调用自己，:= 声明的变量在初始化时还不存在。
	var ensureDir func(rel string) (string, error)
	ensureDir = func(rel string) (string, error) {
		if id, ok := dirIDs[rel]; ok {
			return id, nil
		}
		parent := ""
		if idx := strings.LastIndex(rel, "/"); idx >= 0 {
			parent = rel[:idx]
		}
		parentID, err := ensureDir(parent)
		if err != nil {
			return "", err
		}
		name := rel
		if idx := strings.LastIndex(rel, "/"); idx >= 0 {
			name = rel[idx+1:]
		}
		id, err := s.uploader.EnsureSTRMDirectory(ctx, target.AccountID, parentID, name)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(id) == "" {
			return "", fmt.Errorf("网盘未返回目录 ID：%s", name)
		}
		dirIDs[rel] = id
		return id, nil
	}

	var failed []string
	for _, name := range sortedKeys(files) {
		parent := ""
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			parent = name[:idx]
		}
		parentID, err := ensureDir(parent)
		if err != nil {
			failed = append(failed, name)
			s.log.Warn("恢复 STRM 目录到网盘时建目录失败", "path", parent, "error", err)
			continue
		}
		local := filepath.Join(staged, filepath.FromSlash(name))
		if _, err := s.uploader.UploadSTRMFile(ctx, target.AccountID, STRMUploadRequest{
			LocalPath:      local,
			FileName:       filepath.Base(name),
			ParentID:       parentID,
			ConflictPolicy: "rename",
		}); err != nil {
			failed = append(failed, name)
			s.log.Warn("恢复 STRM 目录到网盘时上传失败", "path", name, "error", err)
		}
	}
	return failed, nil
}

// stageSTRMFiles 把 STRM 内容写到本地暂存目录。
func stageSTRMFiles(files map[string][]byte, stagingDir string) (string, error) {
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return "", err
	}
	for name, body := range files {
		target := filepath.Join(stagingDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(target, body, 0o600); err != nil {
			return "", err
		}
	}
	return stagingDir, nil
}

func sortedKeys(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for name := range files {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
