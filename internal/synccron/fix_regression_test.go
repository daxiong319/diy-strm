package synccron

import (
	"testing"

	"diy-strm/internal/models"
)

// 回归（三批修复）：CancelTask 不再提前清 currentTask —— 提前置 nil 会让 AddTask 查重
// 认为同任务不在执行而重复入队，导致同一任务并发执行；currentTask 由执行协程收尾统一清理。
// （异步 processor 拾取窗口为微秒级且依赖 DB，无法稳定轮询到 running，直接摆状态断言契约）
func TestCancelRunningTaskKeepsCurrentTask(t *testing.T) {
	q := NewQueuePerType(models.SourceType115)
	defer q.Stop()

	// 模拟任务正在执行（STRM）
	q.mutex.Lock()
	q.currentTask = &NewSyncTask{ID: 91, TaskType: SyncTaskTypeStrm}
	q.mutex.Unlock()

	if err := q.CancelTask(91, SyncTaskTypeStrm); err != nil {
		t.Fatalf("取消执行中任务失败：%v", err)
	}
	q.mutex.RLock()
	stillCurrent := q.currentTask != nil
	q.mutex.RUnlock()
	if !stillCurrent {
		t.Fatal("CancelTask 不应提前清 currentTask（会导致同任务重复入队并发执行）")
	}

	// 刮削任务同样契约
	q2 := NewQueuePerType(models.SourceType115)
	defer q2.Stop()
	q2.mutex.Lock()
	q2.currentTask = &NewSyncTask{ID: 52, TaskType: SyncTaskTypeScrape}
	q2.mutex.Unlock()
	if err := q2.CancelTask(52, SyncTaskTypeScrape); err != nil {
		t.Fatalf("取消刮削任务失败：%v", err)
	}
	q2.mutex.RLock()
	stillCurrent2 := q2.currentTask != nil
	q2.mutex.RUnlock()
	if !stillCurrent2 {
		t.Fatal("刮削任务 CancelTask 同样不应提前清 currentTask")
	}
}

// 回归（三批修复）：Stop 之后 AddTask 不得 panic（close(channel) 与入队以 processorStartMu 串行化）。
func TestAddTaskAfterStopDoesNotPanic(t *testing.T) {
	q := NewQueuePerType(models.SourceType115)
	q.Stop()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Stop 后 AddTask 发生 panic（send on closed channel 防护失效）：%v", r)
		}
	}()
	// Stop 后 status=stopped，AddTask 走暂停分支仅入 waitingQueue，不应触发 channel 发送
	if err := q.AddTask(&NewSyncTask{ID: 7, TaskType: SyncTaskTypeStrm}); err != nil {
		t.Fatalf("Stop 后 AddTask 不应报错：%v", err)
	}
}

// 回归（三批修复）：ID=0 任务去重键按源路径区分——不同目录的同类型任务不再互撞。
func TestZeroIDTaskKeyDistinguishesSourcePath(t *testing.T) {
	q := NewQueuePerType(models.SourceType123)

	a := &NewSyncTask{SourcePath: "/media/a", TaskType: SyncTaskTypeStrm}
	b := &NewSyncTask{SourcePath: "/media/b", TaskType: SyncTaskTypeStrm}

	if a.Key() == b.Key() {
		t.Fatalf("不同源路径的无 ID 任务键不应相同：%s", a.Key())
	}
	if err := q.AddTask(a); err != nil {
		t.Fatalf("添加任务 A 失败：%v", err)
	}
	if err := q.AddTask(b); err != nil {
		t.Fatalf("不同源路径的任务 B 不应被去重拦截：%v", err)
	}
	// 同键任务仍要被去重拦截
	if err := q.AddTask(&NewSyncTask{SourcePath: "/media/a", TaskType: SyncTaskTypeStrm}); err == nil {
		t.Fatal("同源路径同类型任务应被『任务已存在』去重拦截")
	}
	q.Stop()
}
