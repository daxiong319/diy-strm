package moviepilot

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 上传任务「片源是否已在云盘」校验：
// 以任务本地下载目录中的片源清单为基准，与云盘「待整理」目录（任务上传目标）
// 和「已整理」目录（整理历史记录 + 目标目录实时列取）做搜索比对：
//   名称完全一致 → 集号（SxxExx）一致 → 字节大小一致 → 内容指纹（首块哈希）兜底。
// 覆盖「已上传但被整理重命名」「名称对不上」等场景，结果按任务缓存 10 分钟。
// ---------------------------------------------------------------------------

// CloudCheckFile 单个片源校验结果
type CloudCheckFile struct {
	File     string `json:"file"`
	Size     int64  `json:"size"`
	Uploaded bool   `json:"uploaded"`
	Where    string `json:"where,omitempty"` // 待整理 / 已整理 / 整理记录
	By       string `json:"by,omitempty"`    // name / episode / size / hash / record
}

// CloudCheckResult 任务级校验结果
type CloudCheckResult struct {
	Status    string           `json:"status"` // all / partial / none / unknown
	Total     int              `json:"total"`
	Uploaded  int              `json:"uploaded"`
	Source    string           `json:"source"` // 片源清单来源：本地下载目录 / 上传批次记录
	Files     []CloudCheckFile `json:"files"`
	CheckedAt int64            `json:"checked_at"`
	Note      string           `json:"note,omitempty"`
}

var (
	cloudCheckMu    sync.Mutex
	cloudCheckCache = map[uint]*cloudCheckEntry{}
)

type cloudCheckEntry struct {
	res *CloudCheckResult
	at  time.Time
}

const cloudCheckTTL = 10 * time.Minute

// CheckTaskCloudUploaded 校验任务片源是否已上传云盘（结果缓存，force 跳过缓存）
func CheckTaskCloudUploaded(ctx context.Context, taskID uint, force bool) (*CloudCheckResult, error) {
	if !force {
		cloudCheckMu.Lock()
		if e, ok := cloudCheckCache[taskID]; ok && time.Since(e.at) < cloudCheckTTL {
			cloudCheckMu.Unlock()
			return e.res, nil
		}
		cloudCheckMu.Unlock()
	}
	res := &CloudCheckResult{Status: "unknown", CheckedAt: time.Now().Unix()}
	task := models.GetMoviePilotUploadTask(taskID)
	if task == nil {
		return nil, fmt.Errorf("上传任务不存在：%d", taskID)
	}

	// 1. 期望片源清单：优先本地下载目录（真实文件），目录不可用时回退批次记录
	srcs, sourceDesc := collectTaskSourceFiles(task)
	if len(srcs) == 0 {
		res.Note = "本地下载目录已清理且无上传批次记录，无法校验"
		cacheCloudCheck(taskID, res)
		return res, nil
	}
	res.Source = sourceDesc

	// 2. 云盘账号
	cfg := models.LoadMoviePilotConfig()
	var account models.Account
	if cfg.UploadAccountId == 0 {
		res.Note = "未配置上传网盘账号，无法校验"
		cacheCloudCheck(taskID, res)
		return res, nil
	}
	if err := db.Db.First(&account, cfg.UploadAccountId).Error; err != nil {
		return nil, fmt.Errorf("上传账号不存在（ID=%d）", cfg.UploadAccountId)
	}

	// 3. 待整理侧：任务上传目标目录子树
	var pending []organizeEntry
	if dirID, ok := resolveRemoteDirID(ctx, &account, cfg, task.RemotePath); ok && dirID != "" {
		counter := 0
		if err := collectOrganizeEntries(ctx, &account, dirID, "", &pending, &counter, 0); err != nil {
			helpers.AppLogger.Warnf("MoviePilot 云盘校验列取待整理目录失败：%v", err)
		}
	}

	// 4. 已整理侧：整理历史记录（本任务上传目录内文件 → 目标路径）+ 目标目录实时列取
	type recT struct{ original, target string }
	var recs []recT
	organizedDirs := map[string]bool{}
	var hist []models.OrganizeHistoryRecord
	like := task.RemotePath + "%"
	if err := db.Db.Where("status IN ? AND source = ? AND source_path LIKE ?",
		[]string{models.OrganizeStatusSuccess, models.OrganizeStatusReplace}, models.SourceDisplayName(account.SourceType), like).
		Order("id desc").Limit(500).Find(&hist).Error; err == nil {
		for _, h := range hist {
			if h.OriginalFileName != "" && h.TargetPath != "" {
				recs = append(recs, recT{h.OriginalFileName, h.TargetPath})
				if d := filepath.Dir(h.TargetPath); !organizedDirs[d] {
					organizedDirs[d] = true
				}
			}
		}
	}
	var organized []organizeEntry
	dirCount := 0
	for d := range organizedDirs {
		if dirCount >= 8 || len(organized) >= 300 {
			break
		}
		dirCount++
		if dirID, ok := resolveRemoteDirID(ctx, &account, cfg, d); ok && dirID != "" {
			files, err := listNetDirByID(ctx, &account, dirID)
			if err == nil {
				for _, f := range files {
					if !f.IsDir && len(organized) < 300 {
						organized = append(organized, f)
					}
				}
			}
		}
	}

	// 5. 逐片源比对
	res.Total = len(srcs)
	cloudAll := append(append([]organizeEntry{}, pending...), organized...)
	for _, s := range srcs {
		cf := CloudCheckFile{File: s.name, Size: s.size}
		// a) 名称完全一致（待整理/已整理任意一侧）
		if e, where := findByName(s.name, pending, "待整理"); e != nil {
			cf.Uploaded, cf.Where, cf.By = true, where, "name"
		} else if e, where := findByName(s.name, organized, "已整理"); e != nil {
			cf.Uploaded, cf.Where, cf.By = true, where, "name"
		}
		// b) 集号一致（整理重命名后仍保留 SxxExx 段）
		if !cf.Uploaded {
			if key, ok := episodeKey(s.name); ok {
				if e, where := findByEpisode(key, pending, "待整理"); e != nil {
					cf.Uploaded, cf.Where, cf.By = true, where, "episode"
				} else if e, where := findByEpisode(key, organized, "已整理"); e != nil {
					cf.Uploaded, cf.Where, cf.By = true, where, "episode"
				}
			}
		}
		// c) 字节大小完全一致（重命名后的大小指纹）
		if !cf.Uploaded && s.size > 0 {
			if e, where := findBySize(s.size, pending, "待整理"); e != nil {
				cf.Uploaded, cf.Where, cf.By = true, where, "size"
			} else if e, where := findBySize(s.size, organized, "已整理"); e != nil {
				cf.Uploaded, cf.Where, cf.By = true, where, "size"
			}
		}
		// d) 整理历史记录（上传后被整理重命名，目标文件已不在两侧列表时可回退记录证据）
		if !cf.Uploaded {
			for _, r := range recs {
				if r.original == s.name {
					cf.Uploaded, cf.Where, cf.By = true, "已整理", "record"
					break
				}
			}
		}
		// e) 内容指纹兜底：名称/集号/大小都对不上时，比对本地文件与候选云文件的
		//    首块哈希（每任务候选上限受控，避免大流量下载）
		if !cf.Uploaded && s.full != "" && len(cloudAll) > 0 {
			if fingerprintMatch(ctx, &account, s, cloudAll) {
				cf.Uploaded, cf.Where, cf.By = true, "云盘", "hash"
			}
		}
		if cf.Uploaded {
			res.Uploaded++
		}
		res.Files = append(res.Files, cf)
	}
	switch {
	case res.Uploaded == res.Total:
		res.Status = "all"
	case res.Uploaded == 0:
		res.Status = "none"
	default:
		res.Status = "partial"
	}
	if len(pending) == 0 && len(organized) == 0 && res.Status == "none" {
		res.Note = "云盘未列取到文件（目录不存在或账号不可用），结果仅供参考"
	}
	cacheCloudCheck(taskID, res)
	return res, nil
}

func cacheCloudCheck(taskID uint, res *CloudCheckResult) {
	cloudCheckMu.Lock()
	cloudCheckCache[taskID] = &cloudCheckEntry{res: res, at: time.Now()}
	cloudCheckMu.Unlock()
}

// taskSourceFile 期望片源
type taskSourceFile struct {
	name string // 文件名
	size int64  // 字节大小
	full string // 本地完整路径（批次记录来源时为空）
}

// collectTaskSourceFiles 采集任务片源清单：本地目录优先，回退批次记录
func collectTaskSourceFiles(task *models.MoviePilotUploadTask) ([]taskSourceFile, string) {
	var out []taskSourceFile
	if task.LocalPath != "" {
		if st, err := os.Stat(task.LocalPath); err == nil && st.IsDir() {
			_ = filepath.WalkDir(task.LocalPath, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				name := d.Name()
				if !mediaparse.IsVideoExt(name) {
					return nil
				}
				if fi, e2 := d.Info(); e2 == nil {
					out = append(out, taskSourceFile{name: name, size: fi.Size(), full: p})
				}
				return nil
			})
			if len(out) > 0 {
				return out, "本地下载目录"
			}
		}
	}
	// 回退：批次文件记录（源目录已清理/种子保留到期删除时）
	var rows []models.DbUploadTask
	if err := db.Db.Where("movie_pilot_task_id = ? AND status NOT IN ?", task.ID,
		[]models.UploadStatus{models.UploadStatusFailed, models.UploadStatusCancelled}).
		Order("id asc").Find(&rows).Error; err != nil {
		return nil, ""
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.FileName == "" || seen[r.FileName] {
			continue
		}
		seen[r.FileName] = true
		out = append(out, taskSourceFile{name: r.FileName, size: r.FileSize})
	}
	return out, "上传批次记录"
}

// findByName 名称完全一致匹配（忽略扩展名大小写差异）
func findByName(name string, entries []organizeEntry, where string) (*organizeEntry, string) {
	for i := range entries {
		if !entries[i].IsDir && strings.EqualFold(entries[i].Name, name) {
			return &entries[i], where
		}
	}
	return nil, ""
}

// episodeKey 提取 SxxExx 集号键（无法解析返回 false）
func episodeKey(name string) (string, bool) {
	ep, ok := mediaparse.ParseEpisode(name)
	if !ok || ep.Episode <= 0 {
		return "", false
	}
	s := ep.Season
	if s <= 0 {
		s = 1
	}
	return fmt.Sprintf("S%02dE%02d", s, ep.Episode), true
}

// findByEpisode 集号一致匹配（整理重命名后集号段仍保留）
func findByEpisode(key string, entries []organizeEntry, where string) (*organizeEntry, string) {
	for i := range entries {
		if entries[i].IsDir {
			continue
		}
		if k, ok := episodeKey(entries[i].Name); ok && k == key {
			return &entries[i], where
		}
	}
	return nil, ""
}

// findBySize 字节大小完全一致匹配（重命名/转码名不同但内容相同的兜底）
func findBySize(size int64, entries []organizeEntry, where string) (*organizeEntry, string) {
	if size <= 0 {
		return nil, ""
	}
	for i := range entries {
		if !entries[i].IsDir && entries[i].Size == size {
			return &entries[i], where
		}
	}
	return nil, ""
}

// fingerprintChunkSize 内容指纹比对的首块大小
const fingerprintChunkSize = 4 << 20

// fingerprintMaxCandidates 每任务最多下载比对的云文件数（防大流量）
const fingerprintMaxCandidates = 12

var fingerprintHTTP = &http.Client{Timeout: 120 * time.Second}

// fingerprintMatch 内容指纹兜底：本地文件首块哈希 vs 云文件首块哈希（Range 下载）。
// 候选限制在合理规模内；账号类型不支持下载直链时直接返回 false。
func fingerprintMatch(ctx context.Context, account *models.Account, s taskSourceFile, candidates []organizeEntry) bool {
	local, err := localFirstChunkMD5(s.full, fingerprintChunkSize)
	if err != nil {
		return false
	}
	tried := 0
	for i := range candidates {
		c := &candidates[i]
		if c.IsDir || c.Size <= 0 {
			continue
		}
		// 大小差异过大的不比对（同内容必同大小；差异大说明不是同一文件）
		diff := c.Size - s.size
		if diff < 0 {
			diff = -diff
		}
		if s.size > 0 && diff > s.size/10 {
			continue
		}
		if tried >= fingerprintMaxCandidates {
			break
		}
		tried++
		remote, err := remoteFirstChunkMD5(ctx, account, c)
		if err == nil && remote != "" && remote == local {
			return true
		}
	}
	return false
}

// localFirstChunkMD5 本地文件首块 MD5
func localFirstChunkMD5(path string, n int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, n)
	cnt, err := f.Read(buf)
	if cnt <= 0 {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("空文件")
	}
	sum := md5.Sum(buf[:cnt])
	return hex.EncodeToString(sum[:]), nil
}

// remoteFirstChunkMD5 云文件首块 MD5（下载直链 + Range 请求）
func remoteFirstChunkMD5(ctx context.Context, account *models.Account, e *organizeEntry) (string, error) {
	var (
		dlURL string
		err   error
	)
	switch account.SourceType {
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return "", fmt.Errorf("139 客户端不可用")
		}
		dlURL, err = client.GetDownloadURL(ctx, e.ID)
	case models.SourceTypeGuangYaPan:
		client := account.GetGuangYaPanClient()
		if client == nil {
			return "", fmt.Errorf("光鸭客户端不可用")
		}
		dlURL, err = client.GetDownloadURL(ctx, e.ID)
	default:
		return "", fmt.Errorf("该网盘类型不支持指纹比对")
	}
	if err != nil || dlURL == "" {
		return "", fmt.Errorf("获取下载直链失败：%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", fingerprintChunkSize-1))
	resp, err := fingerprintHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// 206=支持 Range；200=不支持（可能整文件回流，直接放弃避免大流量下载）
	if resp.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("云盘直链不支持 Range 读取（HTTP %d）", resp.StatusCode)
	}
	buf := make([]byte, fingerprintChunkSize)
	cnt := 0
	for cnt < len(buf) {
		n, err := resp.Body.Read(buf[cnt:])
		cnt += n
		if err != nil {
			if err == io.EOF {
				break
			}
			if cnt == 0 {
				return "", err
			}
			break
		}
	}
	if cnt <= 0 {
		return "", fmt.Errorf("首块读取为空")
	}
	sum := md5.Sum(buf[:cnt])
	return hex.EncodeToString(sum[:]), nil
}

// resolveRemoteDirID 按网盘路径解析目录 ID（只查不建）。
// 139/光鸭/123 用原生路径解析；百度/OpenList 路径语义即 ID；115 逐级列取遍历。
func resolveRemoteDirID(ctx context.Context, account *models.Account, cfg *models.MoviePilotConfig, p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	switch account.SourceType {
	case models.SourceTypePan139:
		if c := account.GetPan139Client(); c != nil {
			if id, err := c.GetPathIdByPath(ctx, p); err == nil && id != "" {
				return id, true
			}
		}
		return "", false
	case models.SourceTypeGuangYaPan:
		if c := account.GetGuangYaPanClient(); c != nil {
			if id, err := c.GetPathIdByPath(ctx, p); err == nil && id != "" {
				return id, true
			}
		}
		return "", false
	case models.SourceType123:
		if c := account.Get123Client(); c != nil {
			if id, err := c.GetPathIdByPath(ctx, p); err == nil && id != "" {
				return id, true
			}
		}
		return "", false
	case models.SourceTypeBaiduPan, models.SourceTypeOpenList:
		return normalizeOpenListPath(p), true
	case models.SourceType115:
		return walkRemoteDirFrom(ctx, account, p, "0")
	default:
		return "", false
	}
}

// walkRemoteDirFrom 从根目录逐级列取解析目录 ID（无原生路径解析的类型兜底）
func walkRemoteDirFrom(ctx context.Context, account *models.Account, p, rootID string) (string, bool) {
	rel := strings.Trim(p, "/")
	cur := rootID
	if rel == "" {
		return cur, true
	}
	// 上传根目录 ID 可作为起点缩短遍历
	seg := strings.Split(rel, "/")
	cfg := models.LoadMoviePilotConfig()
	if root := strings.Trim(cfg.UploadRoot, "/"); root != "" && rel == root {
		return cfg.UploadRootId, true
	} else if root != "" && strings.HasPrefix(rel, root+"/") && cfg.UploadRootId != "" {
		cur = cfg.UploadRootId
		seg = strings.Split(strings.TrimPrefix(rel, root+"/"), "/")
	}
	for _, name := range seg {
		if name == "" {
			continue
		}
		entries, err := listNetDirByID(ctx, account, cur)
		if err != nil {
			return "", false
		}
		found := ""
		for i := range entries {
			if entries[i].IsDir && entries[i].Name == name {
				found = entries[i].ID
				break
			}
		}
		if found == "" {
			return "", false
		}
		cur = found
	}
	return cur, true
}

// purgeCloudCheckCache 任务重试/取消后清缓存（状态已变，旧校验结果失效）
func purgeCloudCheckCache(taskID uint) {
	cloudCheckMu.Lock()
	delete(cloudCheckCache, taskID)
	cloudCheckMu.Unlock()
}
