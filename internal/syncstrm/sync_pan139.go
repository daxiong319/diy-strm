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

// 中国移动云盘（139）增量同步：目录级剪枝。
//
// 正确性设计（目录 updatedAt 的服务端语义无法完全确认，剪枝只做加速、不赌正确性）：
//  1. 当天首次同步强制全量（对齐百度网盘策略）——即使 UTime 语义有偏差，漏扫窗口最多到当天首次全量；
//  2. 只剪「上次同步见过子项」的目录（memSyncCache 预载上次的 sync_files 记录做背书），新目录绝不剪；
//  3. 目录 UTime 晚于 上次同步时间-安全窗 → 必须扫描；等于/早于才允许跳过；
//  4. 被剪目录的子项记录已在预载缓存中，后续 compare 阶段不会误删其本地 STRM/元数据。
const pan139PruneSafetyWindow = int64(10 * 60) // 10 分钟重叠带，吸收时钟偏差与同步期间的边界变化

func (s *SyncStrm) StartPan139Sync() {
	if !s.TmpSyncPath {
		// 当天第一次同步执行全量（与百度网盘策略一致）
		sync := models.GetTodayFirstSyncByPathId(s.SyncPathId)
		if sync == nil {
			s.FullSync = true
		}
	}
	if s.FullSync || s.LastSyncAt == 0 {
		s.Sync.Logger.Infof("执行中国移动云盘全量同步")
		s.StartOther()
		return
	}
	s.Sync.Logger.Infof("从修改时间 %s 开始中国移动云盘增量同步（目录剪枝）", fmt.Sprintf("%d", s.LastSyncAt))
	// 预载上次同步的全部记录：被剪目录的本地文件依赖这些记录在对比阶段存活
	s.LoadSyncFileToCache()
	if err := s.startPan139Incremental(); err != nil {
		s.Sync.Logger.Errorf("中国移动云盘增量同步失败：%v", err)
		select {
		case s.PathErrChan <- err:
		default:
		}
	}
}

// canPrunePan139Dir 判断目录是否可剪枝跳过
func (s *SyncStrm) canPrunePan139Dir(item pathQueueItem) bool {
	if s.Account.SourceType != models.SourceTypePan139 {
		return false
	}
	if item.PathId == "" || item.PathId == s.SourcePathId {
		return false // 入口目录永远不剪
	}
	if item.Mtime <= 0 || s.LastSyncAt <= 0 {
		return false
	}
	// 修改时间落在 (上次同步-安全窗, +∞) 区间 → 可能变化，必须扫描
	if item.Mtime > s.LastSyncAt-pan139PruneSafetyWindow {
		return false
	}
	// 上次同步见过该目录的子项才允许剪（新目录没有数据背书）
	children, err := s.memSyncCache.GetByParentId(item.PathId)
	return err == nil && len(children) > 0
}

// startPan139Incremental 带目录剪枝的遍历（结构对齐 StartOther，仅在目录入队前做剪枝判定）
func (s *SyncStrm) startPan139Incremental() error {
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
		// 剪枝判定：目录自上次同步后无变化（且有上次数据背书）→ 整棵子树跳过
		if s.canPrunePan139Dir(pathItem) {
			atomic.AddInt64(&prunedDirs, 1)
			s.Sync.Logger.Infof("目录 %s 自上次同步后无变化(UTime=%d <= 上次同步)，剪枝跳过其子树", pathItem.Path, pathItem.Mtime)
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
					Mtime: fileItem.MTime,
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
		return err
	}
	s.Sync.Logger.Infof("增量遍历完成：剪枝跳过 %d 个未变化目录，实际扫描 %d 个", atomic.LoadInt64(&prunedDirs), s.memSyncCache.Count())
	return nil
}
