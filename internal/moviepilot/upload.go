package moviepilot

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/upload"
)

// uploadSourceType 本服务创建的上传任务来源类型。
// 复用既有 server-local 通道（与离线交棒同一条链路），不新写上传器。
const uploadSourceType = upload.SourceTypeOfflineHandoff

// FolderCreator 创建网盘目录所需的最小能力（由 file.Service 实现）。
type FolderCreator interface {
	CreateFolder(ctx context.Context, accountID int64, parentID, name string) (*domain.FileItem, error)
}

// UploadEnqueuer 创建服务器本地文件上传任务所需的最小能力（由 upload.Manager 实现）。
type UploadEnqueuer interface {
	CreateServerLocalTasks(ctx context.Context, params []upload.ServerLocalCreateParams) ([]*upload.Task, error)
}

// UploadHandoffOptions 交棒上传所需的依赖与目标位置。
type UploadHandoffOptions struct {
	// AccountID 目标网盘账号。
	AccountID int64
	// AccountName 账号名称（展示用）。
	AccountName string
	// DriverType 驱动类型。
	DriverType string
	// TargetParentID 目标根目录 ID（网盘语义的目录标识）。
	TargetParentID string
	// TargetDisplayPath 目标显示路径（日志与前端展示）。
	TargetDisplayPath string
	// ClientTaskIDPrefix 客户端任务 ID 前缀，用于幂等去重。
	ClientTaskIDPrefix string
}

// moviePilotClientTaskID 生成上传任务的幂等客户端 ID。
// 同一 MoviePilot 任务 + 同一目标相对路径始终映射到同一 ID，重复交棒不会重复上传。
func moviePilotClientTaskID(prefix string, mpTaskID int64, relPath string) string {
	return fmt.Sprintf("%s:%d:%s", prefix, mpTaskID, filepath.ToSlash(relPath))
}

// HandoffLocalTreeToUpload 把一个本地目录树交棒给既有上传链路：
// 按目录层级在网盘建目录（复用 file.Service.CreateFolder），再为每个文件建一个上传任务
// （upload.Manager.CreateServerLocalTasks 不支持目录，必须逐文件建任务）。
//
// targets 为「相对目标根目录的路径 → 本地绝对路径」的计划列表；
// 传入前调用方已完成整理（重命名/移动到整理目录），这里只负责投递。
func (s *Service) HandoffLocalTreeToUpload(ctx context.Context, opts UploadHandoffOptions, files []LocalFile) (int, error) {
	if s.uploads == nil {
		return 0, domain.Errorf(domain.CodeNotImplement, "上传服务未就绪")
	}
	if s.folders == nil {
		return 0, domain.Errorf(domain.CodeNotImplement, "文件服务未就绪，无法创建网盘目录")
	}
	if opts.AccountID <= 0 {
		return 0, domain.Errorf(domain.CodeValidation, "未配置上传账号，请在 MoviePilot 设置中选择")
	}
	if len(files) == 0 {
		return 0, &errEmptySource{msg: "没有可上传的文件", wait: true}
	}

	// 收集所有需要的目录（相对路径，不含文件名），按层级浅→深创建
	dirs := map[string]struct{}{}
	for _, f := range files {
		dir := path.Dir(filepath.ToSlash(f.RelPath))
		for dir != "." && dir != "/" && dir != "" {
			dirs[dir] = struct{}{}
			dir = path.Dir(dir)
		}
	}
	dirList := make([]string, 0, len(dirs))
	for d := range dirs {
		dirList = append(dirList, d)
	}
	sort.Slice(dirList, func(i, j int) bool {
		li, lj := strings.Count(dirList[i], "/"), strings.Count(dirList[j], "/")
		if li != lj {
			return li < lj
		}
		return dirList[i] < dirList[j]
	})

	// 根目录 ID：TargetParentID 由调用方保证已存在（整理根已在网盘建好）
	dirIDs := map[string]string{"": opts.TargetParentID}
	for _, relDir := range dirList {
		parentRel := path.Dir(relDir)
		if parentRel == "." || parentRel == "/" {
			parentRel = ""
		}
		parentID, ok := dirIDs[parentRel]
		if !ok {
			return 0, fmt.Errorf("目标目录 %s 的父目录 ID 缺失", relDir)
		}
		item, err := s.folders.CreateFolder(ctx, opts.AccountID, parentID, path.Base(relDir))
		if err != nil {
			return 0, fmt.Errorf("创建网盘目录 %s 失败：%w", relDir, err)
		}
		if item == nil || strings.TrimSpace(item.ID) == "" {
			return 0, fmt.Errorf("网盘目录 %s 创建后未返回有效 ID", relDir)
		}
		dirIDs[relDir] = item.ID
	}

	prefix := opts.ClientTaskIDPrefix
	if prefix == "" {
		prefix = "moviepilot"
	}
	params := make([]upload.ServerLocalCreateParams, 0, len(files))
	for _, f := range files {
		rel := filepath.ToSlash(f.RelPath)
		parentRel := path.Dir(rel)
		if parentRel == "." || parentRel == "/" {
			parentRel = ""
		}
		parentID, ok := dirIDs[parentRel]
		if !ok {
			return 0, fmt.Errorf("文件 %s 的目标目录 ID 缺失", rel)
		}
		displayPath := opts.TargetDisplayPath
		if parentRel != "" {
			displayPath = path.Join(opts.TargetDisplayPath, parentRel)
		}
		params = append(params, upload.ServerLocalCreateParams{
			ClientTaskID:      moviePilotClientTaskID(prefix, 0, rel),
			BatchName:         opts.TargetDisplayPath,
			AccountID:         opts.AccountID,
			AccountName:       opts.AccountName,
			DriverType:        opts.DriverType,
			FileName:          filepath.Base(f.AbsPath),
			DisplayName:       filepath.Base(f.AbsPath),
			SourceType:        uploadSourceType,
			RelPath:           rel,
			TargetPath:        parentID,
			TargetDisplayPath: displayPath,
			LocalPath:         f.AbsPath,
			TotalBytes:        f.Size,
			ConflictPolicy:    "overwrite",
		})
	}
	created, err := s.uploads.CreateServerLocalTasks(ctx, params)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, t := range created {
		if t != nil {
			count++
		}
	}
	return count, nil
}

// EnsureRemoteDir 在网盘上按路径逐级创建目录，返回末级目录 ID。
// relativeTo 为已存在的起始目录 ID（如上传根），relDir 为其下的相对路径。
func (s *Service) EnsureRemoteDir(ctx context.Context, accountID int64, relativeTo, relDir string) (string, error) {
	if s.folders == nil {
		return "", domain.Errorf(domain.CodeNotImplement, "文件服务未就绪")
	}
	relDir = strings.Trim(strings.TrimSpace(relDir), "/")
	if relDir == "" {
		return relativeTo, nil
	}
	current := relativeTo
	segs := strings.Split(relDir, "/")
	for _, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		item, err := s.folders.CreateFolder(ctx, accountID, current, seg)
		if err != nil {
			// 同名目录已存在时创建会失败：调用方可先列出目录再复用，这里交由上层容错
			return "", fmt.Errorf("创建网盘目录 %s 失败：%w", seg, err)
		}
		if item != nil && strings.TrimSpace(item.ID) != "" {
			current = item.ID
		}
	}
	return current, nil
}

// localFilesForUpload 把整理后的本地文件列表换算为相对整理根的上传计划。
// organizeRoot 为整理根（本地），relDir 为该条目的目标相对目录。
func localFilesForUpload(localDir, relDir string) ([]LocalFile, error) {
	files, err := CollectLocalFiles(localDir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, &errEmptySource{msg: fmt.Sprintf("源目录 %s 中没有可上传的文件（等待文件落盘）", localDir), wait: true}
	}
	out := make([]LocalFile, 0, len(files))
	for _, f := range files {
		out = append(out, LocalFile{
			AbsPath: f.AbsPath,
			RelPath: path.Join(relDir, f.RelPath),
			Size:    f.Size,
		})
	}
	return out, nil
}

// isEmptyDir 判断目录是否不存在或为空。
func isEmptyDir(p string) bool {
	entries, err := os.ReadDir(p)
	return err != nil || len(entries) == 0
}
