package syncstrm

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"diy-strm/internal/models"
	"diy-strm/internal/v115open"

	"golang.org/x/sync/errgroup"
)

// 中国移动云盘（139）同步：目录级增量（持久缓存折中版）。
//
// 每个目录的列表决策：
//  1. pan139_dir_caches 指纹命中（TTL 内 UTime 未变）→ 整棵子树免 API 列表，子项数据
//     由 LoadSyncFileToCache 预载的上次 sync_files 记录背书，对比阶段不会误删本地文件；
//  2. 未命中（新目录 / UTime 变了 / 快照超 TTL）→ 真实列表，成功后 UpsertPan139DirCache 刷新指纹；
//  3. TTL 是最终兜底：即使 139 目录 updatedAt 不随内容变化（语义未核实），漏检窗口 ≤ TTL。
//
// 全量同步同样在列表成功后刷新指纹，为下一轮增量积累缓存。
const pan139CacheTTLSeconds = int64(24 * 3600) // 指纹快照有效期 24 小时：语义失效时最坏漏检窗口与旧「每日全量」一致
const pan139PruneSafetyWindow = int64(10 * 60) // UTime 恰好等于上次同步时间附近的重叠带，吸收时钟偏差

func (s *SyncStrm) StartPan139Sync() {
	if s.FullSync || s.LastSyncAt == 0 {
		s.Sync.Logger.Infof("执行中国移动云盘全量同步")
		s.startPan139Walk(true)
		return
	}
	s.Sync.Logger.Infof("从修改时间 %s 开始中国移动云盘增量同步（目录指纹剪枝）", fmt.Sprintf("%d", s.LastSyncAt))
	// 预载上次同步的全部记录：被剪目录的本地文件依赖这些记录在对比阶段存活
	s.LoadSyncFileToCache()
	s.startPan139Walk(false)
}

// pruneStaleCacheChildren 增量重列后，把预载缓存中该目录下不在最新列表里的子项清掉
// （云端已删除的文件；预载记录来自上次 sync_files，最新列表是权威）
func (s *SyncStrm) pruneStaleCacheChildren(parentId string, fresh []*SyncFileCache) {
	children, err := s.memSyncCache.GetByParentId(parentId)
	if err != nil || len(children) == 0 {
		return
	}
	// 注意：GetByParentId 返回的切片包含刚 Insert 的新列表条目（同一内存对象），
	// 因此用「新列表 FileId 集合」判断：不在集合里的预载记录 = 云端已删除
	current := make(map[string]bool, len(fresh))
	for _, f := range fresh {
		current[f.GetFileId()] = true
	}
	for _, child := range children {
		if current[child.GetFileId()] {
			continue
		}
		// 上次见过但这次列表没有 → 云端已删除，清掉本地记录背书
		if child.ParentId == parentId && child.FileId != "" {
			s.Sync.Logger.Infof("云端已删除的子项 %s（%s），从同步缓存清除其记录背书", child.GetFullRemotePath(), child.FileId)
			s.memSyncCache.DeleteByFileId(child.FileId)
		}
	}
}

// canSkipPan139Dir 增量同步时判断目录是否可跳过（指纹命中）
func (s *SyncStrm) canSkipPan139Dir(item pathQueueItem) bool {
	if s.Account.SourceType != models.SourceTypePan139 {
		return false
	}
	if item.PathId == "" || item.PathId == s.SourcePathId {
		return false // 入口目录永远不跳
	}
	if item.Mtime <= 0 || s.LastSyncAt <= 0 {
		return false // UTime 不可信，必须扫描
	}
	// 修改时间落在 (上次同步-安全窗, +∞) → 可能变化，必须扫描
	if item.Mtime > s.LastSyncAt-pan139PruneSafetyWindow {
		return false
	}
	// 上次同步见过该目录的子项才允许跳过（新目录没有数据背书）
	children, err := s.memSyncCache.GetByParentId(item.PathId)
	if err != nil || len(children) == 0 {
		return false
	}
	// 指纹快照：TTL 内且 UTime 未变 → 跳过；超 TTL → 强制重列一次
	cache := models.FindPan139DirCache(s.Account.ID, item.PathId)
	return cache.Pan139DirCacheFresh(item.Mtime, pan139CacheTTLSeconds)
}

// startPan139Walk 带目录跳过判定的遍历（结构对齐 StartOther）
// fullSync=true 时不做跳过（所有目录真实列表并刷新指纹），false 时指纹命中即剪枝
func (s *SyncStrm) startPan139Walk(fullSync bool) {
	s.Sync.UpdateSubStatus(models.SyncSubStatusProcessNetFileList)

	eg, ctx := errgroup.WithContext(s.Context)
	workerCount := int(s.PathWorkerMax) + 3
	if workerCount < 1 {
		workerCount = 1
	}
	type pathQueue struct {
		mu     sync.Mutex
		cond   *sync.Cond
		items  []pathQueueItem
		closed bool
	}
	q := &pathQueue{}
	q.cond = sync.NewCond(&q.mu)
	var closeOnce sync.Once
	closeQueue := func() {
		closeOnce.Do(func() {
			q.mu.Lock()
			q.closed = true
			q.mu.Unlock()
			q.cond.Broadcast()
		})
	}
	var pending int64
	var prunedDirs int64
	enqueue := func(item pathQueueItem) bool {
		if ctx.Err() != nil {
			return false
		}
		atomic.AddInt64(&pending, 1)
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			if atomic.AddInt64(&pending, -1) == 0 {
				closeQueue()
			}
			return false
		}
		q.items = append(q.items, item)
		q.mu.Unlock()
		q.cond.Signal()
		return true
	}
	dequeue := func() (pathQueueItem, bool) {
		q.mu.Lock()
		for len(q.items) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.items) == 0 && q.closed {
			q.mu.Unlock()
			return pathQueueItem{}, false
		}
		item := q.items[0]
		q.items = q.items[1:]
		q.mu.Unlock()
		return item, true
	}
	go func() {
		<-ctx.Done()
		closeQueue()
	}()

	var processPath func(pathQueueItem) error
	processPath = func(pathItem pathQueueItem) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// 指纹命中：目录自上次同步后无变化 → 整棵子树跳过（不消耗 API 配额）
		if !fullSync && s.canSkipPan139Dir(pathItem) {
			atomic.AddInt64(&prunedDirs, 1)
			s.Sync.Logger.Infof("目录 %s 指纹命中（UTime 未变且快照未过期），剪枝跳过其子树", pathItem.Path)
			return nil
		}
		s.Sync.Logger.Infof("正在处理目录 %s 下的文件列表", pathItem.Path)
		if s.IsExcludeName(filepath.Base(pathItem.Path)) {
			s.Sync.Logger.Warnf("目录 %s 被排除，跳过它和旗下所有内容", pathItem.Path)
			return nil
		}
		retryCount := 0
		var fileItems []*SyncFileCache
		var err error
	apiloop:
		for {
			fileItems, err = s.SyncDriver.GetNetFileFiles(ctx, pathItem.Path, pathItem.PathId)
			if err != nil {
				if retryCount >= models.SettingsGlobal.OpenlistRetry {
					s.Sync.Logger.Errorf("重试 %d 次后，获取目录 %s 下的文件列表失败：%v", models.SettingsGlobal.OpenlistRetry, pathItem.Path, err)
					select {
					case s.PathErrChan <- err:
					default:
					}
					return err
				} else {
					retryCount++
					s.Sync.Logger.Warnf("获取目录 %s 下的文件列表失败，休息 %d 秒后重试第 %d 次：%v", pathItem.Path, models.SettingsGlobal.OpenlistRetryDelay, retryCount, err)
					time.Sleep(time.Duration(models.SettingsGlobal.OpenlistRetryDelay) * time.Second)
					continue apiloop
				}
			}
			break apiloop
		}
		// 列表成功即刷新指纹（全量/增量都刷，为下轮积累缓存）
		models.UpsertPan139DirCache(s.Account.ID, pathItem.PathId, pathItem.Path, pathItem.Mtime)
		// 增量模式下重列了「上次见过子项」的目录：云端已删除的子项必须从预载缓存清掉，
		// 否则其本地 STRM 会因缓存背书在对比阶段被保留，形成孤儿文件
		if !fullSync {
			s.pruneStaleCacheChildren(pathItem.PathId, fileItems)
		}
		if len(fileItems) == 0 {
			s.Sync.Logger.Infof("请求完成，目录 %s 下没有文件，跳过", pathItem.Path)
			return nil
		}
		s.Sync.Logger.Infof("请求完成，目录 %s 下共有 %d 个文件和子目录", pathItem.Path, len(fileItems))
		for _, fileItem := range fileItems {
			if s.IsExcludeName(filepath.Base(fileItem.FileName)) {
				s.Sync.Logger.Warnf("文件 %s 被排除，跳过它和其下所有内容", fileItem.FileName)
				continue
			}
			if fileItem.FileType == v115open.TypeDir {
				fileItem.GetLocalFilePath(s.TargetPath, s.SourcePath)
				s.memSyncCache.Insert(fileItem)
				subPath := pathQueueItem{
					Path:   fileItem.GetFullRemotePath(),
					PathId: fileItem.GetFileId(),
					Mtime:  fileItem.MTime,
				}
				enqueue(subPath)
			} else {
				if !s.ValidFile(fileItem) {
					continue
				}
				fileItem.GetLocalFilePath(s.TargetPath, s.SourcePath)
				s.memSyncCache.Insert(fileItem)
				s.processNetFile(fileItem)
			}
		}
		return nil
	}

	for i := 0; i < workerCount; i++ {
		eg.Go(func() error {
			for {
				item, ok := dequeue()
				if !ok {
					return nil
				}
				if ctx.Err() != nil {
					if atomic.AddInt64(&pending, -1) == 0 {
						closeQueue()
					}
					return nil
				}
				if err := processPath(item); err != nil {
					if atomic.AddInt64(&pending, -1) == 0 {
						closeQueue()
					}
					return err
				}
				if atomic.AddInt64(&pending, -1) == 0 {
					closeQueue()
				}
			}
		})
	}

	enqueue(pathQueueItem{
		Path:   s.SourcePath,
		PathId: s.SourcePathId,
	})

	if err := eg.Wait(); err != nil {
		s.Sync.Logger.Errorf("路径处理失败：%v", err)
		return
	}
	if fullSync {
		s.Sync.Logger.Infof("全量遍历完成，共扫描 %d 个条目，目录指纹已刷新", s.memSyncCache.Count())
	} else {
		s.Sync.Logger.Infof("增量遍历完成：指纹剪枝跳过 %d 个未变化目录", atomic.LoadInt64(&prunedDirs))
	}
}
