package moviepilot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
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

// localDirFingerprint 本地目录文件集指纹（relPath+size，含未完成临时文件，用于判定下载是否仍在写入）
func localDirFingerprint(root string) (string, error) {
	var parts []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
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

// filterClaimedFiles 剔除已被其他 MoviePilot 批次占用的本地文件（返回未被占用的文件）。
// 季包补种目录：整季种子先上传了 E01~E10，新集 E11 由另一 hash 下载进同目录时，
// E01~E10 已属于既有批次不应重传，只有 E11 是待上传缺口。
// excludeMpTaskID：排除自身批次 ID（为当前批次新建文件任务时，避免把自己刚建的记录误算为"其它批次"）。
func filterClaimedFiles(files []LocalFile, excludeMpTaskID uint) ([]LocalFile, error) {
	if len(files) == 0 {
		return files, nil
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.AbsPath)
	}
	claimed := models.MoviePilotOccupiedByLocalPaths(paths, excludeMpTaskID)
	if len(claimed) == 0 {
		return files, nil
	}
	out := make([]LocalFile, 0, len(files))
	for _, f := range files {
		if _, ok := claimed[filepath.Clean(f.AbsPath)]; ok {
			helpers.AppLogger.Debugf("MoviePilot 文件已被其他批次占用，跳过：%s", f.AbsPath)
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// CreateMoviePilotUploadTasks 为本地目录生成文件级上传任务（走系统统一上传队列）
// remoteRootPath 为目标网盘上传根目录路径，remoteRootId 为根目录 ID（各网盘语义：ID 或路径）
// 返回创建的文件任务数
func CreateMoviePilotUploadTasks(ctx context.Context, account *models.Account, moviePilotTaskId uint, localRoot, remoteRootPath, remoteRootId string) (int, error) {
	files, err := CollectLocalFiles(localRoot)
	if err != nil {
		return 0, err
	}
	// 只上传未被其他批次占用的文件缺口：防止季包补种目录把已上传过的旧文件重复入队
	files, err = filterClaimedFiles(files, moviePilotTaskId)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("源目录 %s 中没有待上传的新文件（文件均已被其他批次处理）", localRoot)
	}
	return createUploadTasksForFiles(ctx, account, moviePilotTaskId, remoteRootPath, remoteRootId, files)
}

// createUploadTasksForFiles 为指定文件集创建上传任务（不校验本地目录，直接按文件列表建任务）
func createUploadTasksForFiles(ctx context.Context, account *models.Account, moviePilotTaskId uint, remoteRootPath, remoteRootId string, files []LocalFile) (int, error) {
	if account == nil {
		return 0, fmt.Errorf("上传账号为空")
	}

	baseDirId := strings.TrimSpace(remoteRootId)
	if baseDirId == "" {
		var err error
		baseDirId, err = EnsureRemoteDir(ctx, account, remoteRootPath)
		if err != nil {
			return 0, fmt.Errorf("定位上传根目录失败：%v", err)
		}
	}

	dirIDCache := map[string]string{}
	created := 0
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return created, err
		}
		dir := filepath.ToSlash(filepath.Dir(f.RelPath))
		parentID := baseDirId
		if dir != "." && dir != "" {
			relDir := strings.TrimRight(remoteRootPath, "/") + "/" + dir
			if id, ok := dirIDCache[dir]; ok {
				parentID = id
			} else {
				id, err := EnsureRemoteDir(ctx, account, relDir)
				if err != nil {
					helpers.AppLogger.Errorf("MoviePilot 创建上传子目录 %s 失败：%v", relDir, err)
					continue
				}
				dirIDCache[dir] = id
				parentID = id
			}
		}
		task := &models.DbUploadTask{
			AccountId:        account.ID,
			SourceType:       account.SourceType,
			MoviePilotTaskId: moviePilotTaskId,
			LocalFullPath:    f.AbsPath,
			RelativePath:     f.RelPath,
			RemoteFileId:     strings.TrimRight(remoteRootPath, "/") + "/" + f.RelPath,
			RemotePathId:     parentID,
			FileName:         filepath.Base(f.RelPath),
			Source:           models.UploadSourceMoviePilot,
			Status:           models.UploadStatusPending,
			FileSize:         f.Size,
		}
		if err := models.CreateUploadTask(task); err != nil {
			helpers.AppLogger.Errorf("MoviePilot 创建文件上传任务失败：%s：%v", f.AbsPath, err)
			continue
		}
		created++
	}
	return created, nil
}

// createMissingUploadTasks 批次增量补传：扫描本地目录中尚未纳入批次的新文件并创建上传任务。
// 用于下载任务被判定为"完成"后仍有文件陆续落盘的场景（如快照发布大文件后到）。
func createMissingUploadTasks(ctx context.Context, task *models.MoviePilotUploadTask, account *models.Account) (int, error) {
	files, err := CollectLocalFiles(task.LocalPath)
	if err != nil {
		return 0, err
	}
	// 先剔除已被其他批次占用的文件（同目录多 hash 场景：他任务已传的文件不算缺口）
	files, err = filterClaimedFiles(files, task.ID)
	if err != nil {
		return 0, err
	}
	var existing []string
	db.Db.Model(&models.DbUploadTask{}).
		Where("movie_pilot_task_id = ?", task.ID).
		Pluck("local_full_path", &existing)
	have := make(map[string]struct{}, len(existing))
	for _, p := range existing {
		have[filepath.Clean(p)] = struct{}{}
	}
	missing := make([]LocalFile, 0, len(files))
	for _, f := range files {
		if _, ok := have[filepath.Clean(f.AbsPath)]; !ok {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return 0, nil
	}
	baseDirID, err := EnsureRemoteDir(ctx, account, task.RemotePath)
	if err != nil {
		return 0, fmt.Errorf("定位网盘上传目录失败：%v", err)
	}
	created, err := createUploadTasksForFiles(ctx, account, task.ID, task.RemotePath, baseDirID, missing)
	if err != nil {
		return created, err
	}
	helpers.AppLogger.Infof("MoviePilot 批次 %s 增量补传 %d 个新文件", task.Title, created)
	return created, nil
}

// moviePilotUploadTaskIdsByDbTask 通过 db_upload_tasks 反查指定批次的任务
func moviePilotDbTasks(mpTaskId uint) []*models.DbUploadTask {
	var tasks []*models.DbUploadTask
	db.Db.Where("movie_pilot_task_id = ?", mpTaskId).Order("id asc").Find(&tasks)
	return tasks
}

// moviePilotDbTasksFinished 批次是否全部进入终态
func moviePilotDbTasksFinished(mpTaskId uint) bool {
	var total int64
	db.Db.Model(&models.DbUploadTask{}).
		Where("movie_pilot_task_id = ?", mpTaskId).
		Count(&total)
	if total == 0 {
		return false
	}
	var unfinished int64
	db.Db.Model(&models.DbUploadTask{}).
		Where("movie_pilot_task_id = ? AND status NOT IN ?", mpTaskId, []models.UploadStatus{
			models.UploadStatusCompleted, models.UploadStatusFailed, models.UploadStatusCancelled,
		}).
		Count(&unfinished)
	return unfinished == 0
}

// moviePilotDbTasksAllSuccess 批次是否全部上传成功
func moviePilotDbTasksAllSuccess(mpTaskId uint) bool {
	var failed int64
	db.Db.Model(&models.DbUploadTask{}).
		Where("movie_pilot_task_id = ? AND status IN ?", mpTaskId, []models.UploadStatus{
			models.UploadStatusFailed, models.UploadStatusCancelled,
		}).
		Count(&failed)
	return failed == 0
}

// moviePilotDbTaskProgress 汇总批次的上传进度
func moviePilotDbTaskProgress(mpTaskId uint) (totalBytes, uploadedBytes int64, totalFiles, uploadedFiles int64) {
	db.Db.Model(&models.DbUploadTask{}).
		Where("movie_pilot_task_id = ?", mpTaskId).
		Select("COALESCE(SUM(file_size),0), COALESCE(SUM(uploaded_bytes),0)").
		Row().Scan(&totalBytes, &uploadedBytes)
	db.Db.Model(&models.DbUploadTask{}).
		Where("movie_pilot_task_id = ?", mpTaskId).
		Count(&totalFiles)
	db.Db.Model(&models.DbUploadTask{}).
		Where("movie_pilot_task_id = ? AND status = ?", mpTaskId, models.UploadStatusCompleted).
		Count(&uploadedFiles)
	return
}