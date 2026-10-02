package embywebhook

import (
	"strings"
	"sync"
	"time"
)

// seriesBuffer 是同一部剧在合并窗口内累积的季集信息。
type seriesBuffer struct {
	// SeriesName 冗余保存剧名，用于渲染通知标题。
	SeriesName string
	// Seasons 是「季号 → 集号列表」。季号为 0 时按第 1 季处理。
	Seasons map[int][]int
	// firstSeen 是该条目第一次进入缓冲的时间，用于判断窗口是否已过。
	firstSeen time.Time
}

// SeriesFlush 是缓冲区到期后回调的载荷。
type SeriesFlush struct {
	// Key 是缓冲区键（SeriesID，取不到时退化为 SeriesName）。
	Key string
	// SeriesName 是剧名。
	SeriesName string
	// Seasons 是合并后的季集映射。
	Seasons map[int][]int
	// Deleted 表示这是一条删除事件（library.deleted）。
	Deleted bool
}

// Buffer 把短时间内逐个到达的同一部剧的单集事件合并成一次通知。
// 老版用包级全局变量 + 一次性 ticker goroutine 实现，这里改成可实例化、
// 可显式启停的结构，便于测试与优雅关闭。
type Buffer struct {
	flusher func(SeriesFlush)

	mu      sync.Mutex
	added   map[string]*seriesBuffer
	deleted map[string]*seriesBuffer

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}

	// window / tick 是快照，构造时从包级变量读取，避免运行期被测试改动影响。
	window time.Duration
	tick   time.Duration
}

// NewBuffer 创建缓冲区。flusher 在窗口到期后按剧调用，必须自行处理通知发送。
func NewBuffer(flusher func(SeriesFlush)) *Buffer {
	return &Buffer{
		flusher: flusher,
		added:   map[string]*seriesBuffer{},
		deleted: map[string]*seriesBuffer{},
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
		window:  MergeWindow,
		tick:    tickInterval(MergeWindow),
	}
}

// Start 启动后台 ticker。重复调用只生效一次。
func (b *Buffer) Start() {
	if b == nil {
		return
	}
	go b.loop()
}

// Stop 停止 ticker 并等待其退出；可重复调用。
func (b *Buffer) Stop() {
	if b == nil {
		return
	}
	b.stopOnce.Do(func() {
		close(b.stopCh)
	})
	<-b.doneCh
}

// loop 是 ticker 主循环，收到停止信号后退出并关闭 doneCh。
func (b *Buffer) loop() {
	defer close(b.doneCh)
	ticker := time.NewTicker(b.tick)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.flushExpired()
		}
	}
}

// AddItem 记录一条 library.new 的单集事件。
func (b *Buffer) AddItem(ev EmbyEvent) {
	b.add(b.added, ev)
}

// AddDeletedItem 记录一条 library.deleted 的单集事件。
func (b *Buffer) AddDeletedItem(ev EmbyEvent) {
	b.add(b.deleted, ev)
}

// add 把事件并入对应剧的缓冲。
func (b *Buffer) add(target map[string]*seriesBuffer, ev EmbyEvent) {
	if b == nil {
		return
	}
	key := seriesKey(ev)
	if key == "" {
		return
	}
	season := ev.Item.ParentIndexNumber
	if season <= 0 {
		// 老版把季号 0 归到第 1 季，避免出现 S0。
		season = 1
	}
	episode := ev.Item.IndexNumber

	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := target[key]
	if !ok {
		entry = &seriesBuffer{
			SeriesName: strings.TrimSpace(ev.Item.SeriesName),
			Seasons:    map[int][]int{},
			firstSeen:  Now(),
		}
		target[key] = entry
	}
	if entry.SeriesName == "" {
		entry.SeriesName = strings.TrimSpace(ev.Item.SeriesName)
	}
	if episode > 0 {
		entry.Seasons[season] = append(entry.Seasons[season], episode)
	} else if _, exists := entry.Seasons[season]; !exists {
		entry.Seasons[season] = nil
	}
}

// flushExpired 把两个缓冲里超过合并窗口的条目取出来回调。
// 条目在回调之前就从 map 里删除，避免慢回调导致重复通知。
func (b *Buffer) flushExpired() {
	if b == nil {
		return
	}
	now := Now()
	for _, flush := range []struct {
		target  map[string]*seriesBuffer
		deleted bool
	}{
		{target: b.added, deleted: false},
		{target: b.deleted, deleted: true},
	} {
		b.mu.Lock()
		expired := make([]SeriesFlush, 0, len(flush.target))
		for key, entry := range flush.target {
			if now.Sub(entry.firstSeen) < b.window {
				continue
			}
			expired = append(expired, SeriesFlush{
				Key:        key,
				SeriesName: entry.SeriesName,
				Seasons:    entry.Seasons,
				Deleted:    flush.deleted,
			})
			// 先删再回调：即使回调很慢，这一批也不会被下一个 tick 重复取出。
			delete(flush.target, key)
		}
		b.mu.Unlock()

		for _, item := range expired {
			if b.flusher != nil {
				b.flusher(item)
			}
		}
	}
}

// seriesKey 取缓冲键：优先 SeriesID，缺失时退化为剧名。
func seriesKey(ev EmbyEvent) string {
	if id := strings.TrimSpace(ev.Item.SeriesID); id != "" {
		return id
	}
	return strings.TrimSpace(ev.Item.SeriesName)
}
