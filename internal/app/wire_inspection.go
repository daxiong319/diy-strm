package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"litepan/internal/domain"
	"litepan/internal/file"
	"litepan/internal/inspection"
	"litepan/internal/mediaorganize"
	"litepan/internal/mediaorganize/tmdb"
)

// inspectionFileLister 把 file.Service 的清单能力接到巡检的 TreeLister 上。
//
// 三处适配值得写下来，因为它们各自对应一个「接了等于没接」的坑：
//
//  1. 先查 SupportsFullList。清单模式不可用时**必须**返回错误，不能返回空清单 ——
//     空清单会被六个检查器一致解读成「一切正常」，而实际上什么都没查。
//  2. 115 的清单模式不返回文件夹（show_dir=0），条目只有 pid。目录路径靠
//     strm_dir_cache 的 pid→路径 缓存翻译，缺失时用 ResolveDirPath 单条补。
//  3. 清单模式给不出目录节点本身，所以巡检树里的目录全部由「某个文件的父目录」
//     反推：path 上没有出现在任何一个文件父目录里的中间目录说明它真的为空。
//     第 3 条正是「空目录检查」的来源，也是它能工作的原因。
type inspectionFileLister struct {
	files *file.Service
	dirs  domain.StrmDirCacheRepository
}

func (l *inspectionFileLister) Tree(ctx context.Context, accountID int64, rootID, rootPath string) ([]inspection.NetdiskEntry, error) {
	ok, err := l.files.SupportsFullList(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.Errorf(domain.CodeNotImplement, "该网盘驱动不支持清单模式，目录树类巡检无法进行")
	}
	raw, err := l.files.ListAllFiles(ctx, accountID, rootID)
	if err != nil {
		return nil, err
	}
	dirIDs := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, e := range raw {
		if e.ParentID == "" || seen[e.ParentID] {
			continue
		}
		seen[e.ParentID] = true
		dirIDs = append(dirIDs, e.ParentID)
	}
	paths, err := l.dirPaths(ctx, accountID, dirIDs)
	if err != nil {
		return nil, err
	}
	out := make([]inspection.NetdiskEntry, 0, len(raw)*2)
	// 目录节点：按路径去重。同一路径可能被多个 pid 命中（缓存脏了），
	// 保留第一个 —— 重复目录会让空目录检查把同一个空目录报两次。
	emitted := map[string]bool{}
	for id, p := range paths {
		clean := strings.Trim(p, "/")
		if clean == "" || emitted[clean] {
			continue
		}
		emitted[clean] = true
		out = append(out, inspection.NetdiskEntry{
			ID:           "dir:" + id,
			ParentID:     "dir:" + dirParentID(ctx, l, paths, id),
			RelativePath: clean,
			IsDir:        true,
		})
	}
	for _, e := range raw {
		dirPath, known := paths[e.ParentID]
		if !known {
			// 拿不到父目录路径就不登记这一条：既不猜，也不伪造相对路径。
			// 少报一条比报一条位置错的强 —— 巡检的所有修复动作都以路径为键，
			// 路径错了会指向另一个目录。
			continue
		}
		out = append(out, inspection.NetdiskEntry{
			ID:           e.FileID,
			ParentID:     "dir:" + e.ParentID,
			Name:         e.Name,
			RelativePath: joinNetdiskPath(dirPath, e.Name),
			Size:         e.Size,
			Sha1:         e.Sha1,
			IsDir:        false,
		})
	}
	return out, nil
}

// dirParentID 从目录路径里取出上一级目录 ID。
//
// 清单模式不返回目录节点，pid→路径是唯一映射，所以「谁是我爹」只能从路径反推。
// 中间层目录没在 paths 里（它不含文件）时返回空 —— 这类目录会被判成孤儿候选，
// 而孤儿检查本来就该把它报出来。
func dirParentID(ctx context.Context, l *inspectionFileLister, paths map[string]string, id string) string {
	byPath := make(map[string]string, len(paths))
	for pid, p := range paths {
		byPath[strings.Trim(p, "/")] = pid
	}
	cur := strings.Trim(paths[id], "/")
	for {
		idx := strings.LastIndex(cur, "/")
		if idx <= 0 {
			return ""
		}
		cur = cur[:idx]
		if pid, ok := byPath[cur]; ok {
			return pid
		}
	}
}

// dirPaths 批量把 pid 翻成远端路径：先查缓存，缺失的单条反查并回写缓存。
//
// 逐条反查而不是直接放弃：缓存冷启动是最常见的情形（刚同步完第一次），
// 一次巡检如果因为缓存空就什么都不查，用户会以为巡检坏了。
func (l *inspectionFileLister) dirPaths(ctx context.Context, accountID int64, dirIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(dirIDs) == 0 {
		return out, nil
	}
	var missing []string
	if l.dirs != nil {
		hit, err := l.dirs.GetBatch(ctx, accountID, dirIDs)
		if err != nil {
			return nil, err
		}
		for id, p := range hit {
			out[id] = p
		}
	}
	for _, id := range dirIDs {
		if _, ok := out[id]; !ok {
			missing = append(missing, id)
		}
	}
	var fresh []domain.StrmDirCacheEntry
	for _, id := range missing {
		p, err := l.files.ResolveDirPath(ctx, accountID, id)
		if err != nil {
			// 拿不到就当这条路径未知，不猜。
			continue
		}
		out[id] = p
		fresh = append(fresh, domain.StrmDirCacheEntry{
			AccountID: accountID, DirID: id, DirPath: p, LastSeenAt: time.Now(),
		})
	}
	if len(fresh) > 0 && l.dirs != nil {
		if err := l.dirs.UpsertBatch(ctx, fresh); err != nil {
			return out, nil // 回写失败不影响本次结论，只是下次慢一点。
		}
	}
	return out, nil
}

// inspectionDirChecker 把 file.Service 的目录列举与删除接到巡检修复器上。
type inspectionDirChecker struct {
	files *file.Service
}

func (c *inspectionDirChecker) DirItems(ctx context.Context, accountID int64, dirID string) ([]inspection.DirItem, error) {
	items, err := c.files.List(ctx, accountID, dirID, true)
	if err != nil {
		return nil, err
	}
	out := make([]inspection.DirItem, 0, len(items))
	for _, it := range items {
		out = append(out, inspection.DirItem{ID: it.ID, Name: it.Name, IsDir: it.IsDir})
	}
	return out, nil
}

func (c *inspectionDirChecker) DeleteDir(ctx context.Context, accountID int64, dirID string) error {
	return c.files.DeleteFiles(ctx, accountID, []string{dirID}, "")
}

func joinNetdiskPath(dirPath, name string) string {
	dirPath = strings.Trim(dirPath, "/")
	if dirPath == "" {
		return name
	}
	return path.Join(dirPath, name)
}

// inspectionRoots 从整理任务配置里读出巡检范围。
//
// 只认 target_root（路径）而不是 target_root_id（网盘目录 ID）：巡检的
// 孤儿目录、目录树清理、重复排查都要按路径比较，ID 还得再翻一次驱动。
// 代价是任务没填路径时这个根不参与巡检 —— 方向是漏扫而不是乱扫。
//
// 同一个路径登记一次：巡检会把根下的目录翻两遍，第二遍报的东西和第一遍
// 完全一样，用户会以为有重复项。
func inspectionRoots(ctx context.Context, tasks []*domain.MediaOrganizeTask) []inspection.Root {
	seen := map[string]bool{}
	out := make([]inspection.Root, 0, len(tasks))
	for _, task := range tasks {
		if task == nil || len(task.Config) == 0 || task.AccountID <= 0 {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal(task.Config, &cfg); err != nil {
			continue
		}
		root := strings.TrimSpace(anyToString(cfg["target_root"]))
		if root == "" {
			continue
		}
		root = path.Clean("/" + root)
		if seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, inspection.Root{
			AccountID: task.AccountID,
			ID:        strings.TrimSpace(anyToString(cfg["target_root_id"])),
			Path:      root,
			Label:     strings.TrimSpace(task.TaskName),
		})
	}
	return out
}

func anyToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.0f", x), "0"), ".")
	case bool:
		return ""
	default:
		return ""
	}
}

// wireInspection 组装巡检套件（六个检查器 + 两类修复器）。
//
// Roots 传函数不传切片：整理任务配置是运行期可改的，接线时冻结切片会让
// 用户新建的整理根在巡检里永远不出现 —— 而巡检页面上不会有任何提示说
// 「你少配了一个根」，只会少报一堆问题。
func wireInspection(files *file.Service, st *storeBundle) *inspection.Service {
	source := inspection.NewNetdiskTreeSource(&inspectionFileLister{
		files: files,
		dirs:  st.store.StrmDirCache,
	})
	roots := func(ctx context.Context) ([]inspection.Root, error) {
		tasks, err := st.store.MediaOrganizeTasks.List(ctx)
		if err != nil {
			return nil, err
		}
		return inspectionRoots(ctx, tasks), nil
	}

	reg := inspection.NewRegistry()
	reg.RegisterRepairer(inspection.RepairDeleteDir, &inspection.DeleteDirRepairer{
		Files: &inspectionDirChecker{files: files},
	})
	reg.RegisterRepairer(inspection.RepairDropIndexRow, &inspection.DropIndexRowRepairer{
		Emby: st.store.EmbyIndex,
	})
	reg.Register(&inspection.IndexAbnormalChecker{
		KeyName:   inspection.KeyIndexAbnormal,
		LabelText: "115 索引异常文件",
		Roots:     roots,
		Source:    source,
		IndexRows: func(ctx context.Context) ([]inspection.IndexRow, error) {
			return inspectionIndexRows(ctx, st.store.EmbyIndex)
		},
	})
	reg.Register(&inspection.OrphanDirChecker{
		KeyName:   inspection.KeyOrphanDir,
		LabelText: "孤儿目录清理",
		Roots:     roots,
		Source:    source,
	})
	reg.Register(&inspection.EmptyDirChecker{
		KeyName:   inspection.KeyEmptyDir,
		LabelText: "目录树清理",
		Roots:     roots,
		Source:    source,
	})
	reg.Register(&inspection.DuplicateChecker{
		KeyName:   inspection.KeyDuplicate,
		LabelText: "重复排查",
		Roots:     roots,
		Source:    source,
	})
	reg.Register(&inspection.TMDBChecker{
		KeyName:   inspection.KeyTMDB,
		LabelText: "TMDB 检查与修正",
		Items:     inspectionTMDBItems(st),
		Client:    inspectionTMDBLookup(st),
	})
	reg.Register(&inspection.MissingChecker{
		KeyName:   inspection.KeyMissing,
		LabelText: "查漏补缺",
		Roots:     roots,
		Source:    source,
		Expect:    inspectionExpectSeasons(st),
	})
	return inspection.NewService(inspection.ServiceOptions{
		Repo:     st.store.Inspection,
		Registry: reg,
	})
}

// inspectionIndexRows 把索引仓储的关联行转成巡检需要的形状。
func inspectionIndexRows(ctx context.Context, repo domain.EmbyIndexRepository) ([]inspection.IndexRow, error) {
	rows, err := repo.ListMediaSyncFiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]inspection.IndexRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, inspection.IndexRow{
			ID:           r.ID,
			EmbyItemID:   r.EmbyItemID,
			AccountID:    r.AccountID,
			RootID:       r.RootID,
			RelativePath: r.RelativePath,
			FileName:     r.FileName,
		})
	}
	return out, nil
}

// inspectionTMDBItems 从整理历史里取带 TMDB 编号的作品。
//
// 整理历史是目前唯一同时有 title / year / tmdb_id / media_type 的本地表：
// media_requests 是求片单，没入库的东西也在里面；emby_media_items 根本没有
// 编号列。取最近 500 条而不是全表：编号失效是长尾，翻太旧的历史只是
// 反复报告用户早就放弃的作品。
func inspectionTMDBItems(st *storeBundle) func(ctx context.Context) ([]inspection.TMDBCheckItem, error) {
	return func(ctx context.Context) ([]inspection.TMDBCheckItem, error) {
		hist, err := st.store.MoviePilot.ListOrganizeHistory(ctx, 500)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		out := make([]inspection.TMDBCheckItem, 0, len(hist))
		for _, h := range hist {
			if h.TmdbId <= 0 {
				continue
			}
			key := fmt.Sprintf("%d|%s", h.TmdbId, h.MediaType)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, inspection.TMDBCheckItem{
				Kind:       inspectionTMDBKind(h.MediaType),
				TMDBID:     h.TmdbId,
				LocalName:  h.Title,
				LocalPath:  h.TargetPath,
				RefID:      h.ID,
			})
		}
		return out, nil
	}
}

// inspectionTMDBLookup 构造巡检用的 TMDB 查询端口。
//
// 没配 API Key 时返回 nil 而不是空实现：TMDBChecker 见到 nil 会把每一项
// 报成「跳过（未配置 TMDB）」，而不是拿一个必然失败的客户端去制造 500 条
// 「编号失效」。
func inspectionTMDBLookup(st *storeBundle) inspection.TMDBLookup {
	if inspectionTMDBClient(st) == nil {
		return nil
	}
	return &tmdbInspectorLookup{newClient: func() *tmdb.Client { return inspectionTMDBClient(st) }}
}

// inspectionTMDBClient 构造巡检用的 TMDB 客户端，没配 API Key 时返回 nil。
//
// 返回 nil 而不是空实现：调用方据此把整个检查项报成「跳过（未配置 TMDB）」，
// 而不是拿一个必然失败的客户端去制造 500 条「编号失效」——
// 那会让巡检页面上最刺眼的红色数字全是假的。
func inspectionTMDBClient(st *storeBundle) *tmdb.Client {
	opts := func() tmdb.Options {
		ps := mediaorganize.EnrichPlannerSettings(st.settings, nil)
		return tmdb.Options{
			APIKey:      mediaorganize.PlannerTMDBAPIKey(ps),
			Language:    mediaorganize.PlannerTMDBLanguage(ps),
			ProxyURL:    tmdb.BuildProxyURL(mediaorganize.TmdbProxyFromSettings(ps)),
			APIBaseHost: mediaorganize.PlannerTMDBAPIHost(ps),
		}
	}
	if strings.TrimSpace(opts().APIKey) == "" {
		return nil
	}
	return tmdb.NewClient(opts())
}

// tmdbInspectorLookup 把 tmdb.Client 的 json 响应翻译成巡检的元数据子集。
//
// 404 必须翻译成 ErrTMDBNotFound 而不是普通错误 —— 检查器靠这个区分
// 「这个编号没了」（该报）和「TMDB 打不开」（不该报）。直接透传错误串会让
// 一次网络抖动变成 N 条「编号失效」的假报告。
type tmdbInspectorLookup struct {
	newClient func() *tmdb.Client
}

func (l *tmdbInspectorLookup) Lookup(ctx context.Context, tmdbID int64, mediaType string) (inspection.TMDBMeta, error) {
	c := l.newClient()
	raw, err := c.Lookup(ctx, fmt.Sprintf("%d", tmdbID), mediaType)
	if err != nil {
		if strings.Contains(err.Error(), "http status 404") {
			return inspection.TMDBMeta{}, inspection.ErrTMDBNotFound
		}
		return inspection.TMDBMeta{}, err
	}
	return parseTMDBMeta(raw, mediaType)
}

// inspectionExpectSeasons 构造「这一季应该有多少集」的端口。
//
// 签名是按剧名而不是按 TMDB 编号：目录树里只有文件名，而从文件名反推编号
// 本身就是巡检不该做的事（同名剧太多，反推错就会去查另一部剧的集数，
// 然后报一堆根本不存在的「缺集」）。所以这里先搜、拿到编号再查集数，
// 搜不到就返回空集合 —— MissingChecker 见到空集合会跳过这一季，
// 宁可漏报不可错报。
//
// 与 TMDB 校验共用同一份凭据：查作品和查季集数分开配置，迟早会出现
// 「能校验编号却查不到集数」的半残状态。
func inspectionExpectSeasons(st *storeBundle) func(ctx context.Context, seriesTitle string, season int) ([]int, error) {
	client := inspectionTMDBClient(st)
	if client == nil {
		return nil
	}
	return func(ctx context.Context, seriesTitle string, season int) ([]int, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		raw, err := client.Search(ctx, seriesTitle, nil, "tv")
		if err != nil {
			return nil, err
		}
		id, ok := firstTMDBID(raw)
		if !ok {
			return nil, nil
		}
		info, err := client.Lookup(ctx, id, "tv")
		if err != nil {
			return nil, err
		}
		meta, err := parseTMDBMeta(info, "tv")
		if err != nil {
			return nil, err
		}
		for _, s := range meta.Seasons {
			if s.SeasonNumber != season || s.EpisodeCount <= 0 {
				continue
			}
			out := make([]int, 0, s.EpisodeCount)
			for i := 1; i <= s.EpisodeCount; i++ {
				out = append(out, i)
			}
			return out, nil
		}
		return nil, nil
	}
}

// firstTMDBID 从 TMDB 搜索结果里取第一个编号。
//
// 取第一个而不是「最匹配的」：搜索接口不返回可靠的匹配分字段，
// 而巡检在这一步只需要一个「第 n 季有多少集」的参照系。取错的后果是
// 这一季被跳过（空集合），不会报出错误的缺集结论。
func firstTMDBID(results []json.RawMessage) (string, bool) {
	for _, raw := range results {
		var hit struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(raw, &hit); err != nil || hit.ID <= 0 {
			continue
		}
		return strconv.FormatInt(hit.ID, 10), true
	}
	return "", false
}

// inspectionTMDBKind 把整理历史里的 media_type 归一成 TMDB 接口认的词。
//
// 非 tv 一律当 movie：TMDB 只认真值，auto 在 Lookup 上会因为多一次探测请求
// 而失败，而历史里的 media_type 本来也只有那两个来源。
func inspectionTMDBKind(mediaType string) string {
	if strings.EqualFold(strings.TrimSpace(mediaType), "tv") {
		return "tv"
	}
	return "movie"
}

// parseTMDBMeta 只取巡检要用的那几个字段，丢掉整份 API 响应。
//
// 只挑 SeasonNumber / EpisodeCount 是因为「这一季有多少集」是唯一需要比对
// 的数字；集名 map 留空：改名预览对剧集不做（依赖用户季集模板），
// 建一张永远读不到的 map 只是把 TMDB 的响应原样抄一遍进快照。
func parseTMDBMeta(raw json.RawMessage, mediaType string) (inspection.TMDBMeta, error) {
	var payload struct {
		Title       string `json:"title"`
		Name        string `json:"name"`
		ReleaseDate string `json:"release_date"`
		FirstAir    string `json:"first_air_date"`
		Seasons     []struct {
			SeasonNumber int `json:"season_number"`
			EpisodeCount int `json:"episode_count"`
		} `json:"seasons"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return inspection.TMDBMeta{}, err
	}
	meta := inspection.TMDBMeta{Title: payload.Title, MediaType: mediaType}
	if meta.Title == "" {
		meta.Title = payload.Name
	}
	date := payload.ReleaseDate
	if date == "" {
		date = payload.FirstAir
	}
	if len(date) >= 4 {
		if year, err := strconv.Atoi(date[:4]); err == nil {
			meta.Year = year
		}
	}
	for _, s := range payload.Seasons {
		meta.Seasons = append(meta.Seasons, inspection.TMDBSeasonMeta{
			SeasonNumber: s.SeasonNumber,
			EpisodeCount: s.EpisodeCount,
			EpisodeNames: map[int]string{},
		})
	}
	return meta, nil
}
