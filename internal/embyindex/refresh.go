package embyindex

import (
	"context"
	"strings"
)

// 刷新目标类型，取值与 embyrefresh 的常量保持一致。
//
// 本包不导入 embyrefresh（那会让 embyindex 依赖自动化链路，
// 而自动化链路又要在扫描完成后回调本包，容易形成环），
// 因此这里用等值的字符串常量描述目标，由调用方负责翻译。
const (
	// RefreshTargetLibrary 表示库级刷新。
	RefreshTargetLibrary = "library"
	// RefreshTargetItem 表示条目级刷新。
	RefreshTargetItem = "item"
)

// RefreshIntent 描述一次「Emby 需要感知某个条目/媒体库已变化」的意图。
//
// 它刻意只保留刷新队列（embyrefresh.RequestRefreshParams）需要的字段，
// 这样 embyindex 既不需要导入 embyrefresh，调用方也能零成本地完成翻译。
type RefreshIntent struct {
	// TargetType 为 RefreshTargetLibrary 或 RefreshTargetItem。
	TargetType string
	// LibraryID 是条目所属媒体库 ID；库级刷新必填。
	LibraryID string
	// LibraryName 是媒体库名称，仅用于任务展示。
	LibraryName string
	// ItemID 是 Emby 条目 ID；条目级刷新必填。
	ItemID string
}

// RefreshIntentSink 是刷新意图的投递出口。
//
// 由 internal/app 用 automation.Service.RegisterRefreshIntent 适配后注入。
// 之所以在 embyindex 侧声明接口而不是直接依赖 automation：
//   - embyindex 是低层索引能力，不应反向依赖自动化服务；
//   - 该接口只有 Receiving 一侧需要，测试也能用极小的假实现覆盖。
type RefreshIntentSink interface {
	// RegisterRefreshIntent 登记一次刷新意图；实现方负责防抖与合并。
	RegisterRefreshIntent(ctx context.Context, intent RefreshIntent) error
}

// refreshAggregator 把一轮扫描里发生变化的条目聚合成刷新意图。
//
// 设计要点：
//   - 条目在 10 个以内时按条目逐个登记，交给刷新队列自己按 10 条阈值合并
//     （老版 EmbyRefreshItemAggregationThreshold=10 的行为由队列负责，本包不重复实现）；
//   - 超过阈值时直接登记库级刷新，避免往队列里灌入成百上千条任务；
//   - 没有库 ID 的条目无法降级刷新，直接丢弃而不是登记一条必定失败的任务。
type refreshAggregator struct {
	sink RefreshIntentSink
	// threshold 是「改为库级刷新」的条目数阈值，<=0 时使用默认值。
	threshold int
	// libraryNames 记录本次扫描中 库ID → 库名，便于库级意图带上名称。
	libraryNames map[string]string
	// items 按库收集变化的条目 ID（保持出现顺序，便于测试断言）。
	items map[string][]string
	// order 记录库 ID 的首次出现顺序，保证输出稳定。
	order []string
}

// defaultRefreshAggregationThreshold 与老版 embyrefresh 的条目合并阈值一致。
//
// 注意：这是本包「超过多少个就改走库级刷新」的本地阈值，
// 与刷新队列内部 EmbyRefreshItemAggregationThreshold 的语义是同名不同层：
// 队列负责「不足 10 条也要凑一起刷」，本包负责「实在太多就别逐条登记了」。
const defaultRefreshAggregationThreshold = 10

// newRefreshAggregator 构造聚合器；sink 为 nil 时返回 nil，表示不产生任何刷新意图。
func newRefreshAggregator(sink RefreshIntentSink, threshold int) *refreshAggregator {
	if sink == nil {
		return nil
	}
	if threshold <= 0 {
		threshold = defaultRefreshAggregationThreshold
	}
	return &refreshAggregator{
		sink:         sink,
		threshold:    threshold,
		libraryNames: map[string]string{},
		items:        map[string][]string{},
	}
}

// add 记录一个确实发生变化的条目。
//
// libraryID 为空表示无法确定归属（例如单条同步解析到多个候选库），
// 此时不登记条目级意图：刷新队列对条目刷新的降级依赖库 ID，
// 缺库 ID 的任务只会失败，登记它是纯粹的噪音。
func (a *refreshAggregator) add(libraryID, libraryName, itemID string) {
	if a == nil {
		return
	}
	libraryID = strings.TrimSpace(libraryID)
	itemID = strings.TrimSpace(itemID)
	if libraryID == "" || itemID == "" {
		return
	}
	if _, seen := a.items[libraryID]; !seen {
		a.order = append(a.order, libraryID)
	}
	a.items[libraryID] = append(a.items[libraryID], itemID)
	if name := strings.TrimSpace(libraryName); name != "" {
		a.libraryNames[libraryID] = name
	}
}

// flush 把聚合结果登记到刷新队列。
//
// 返回成功登记的次数。任何单次登记失败都只记录不中断：
// 刷新意图是「尽力而为」的副作用，绝不能因为刷新队列暂时不可用
// 而让已经写入本地索引的扫描结果被回滚。
func (a *refreshAggregator) flush(ctx context.Context, log func(msg string, args ...any)) int {
	if a == nil || len(a.order) == 0 {
		return 0
	}
	registered := 0
	for _, libraryID := range a.order {
		itemIDs := a.items[libraryID]
		if len(itemIDs) == 0 {
			continue
		}
		libraryName := a.libraryNames[libraryID]
		if len(itemIDs) > a.threshold {
			// 变化太多，逐条登记只会让队列堆积；库级刷新一次覆盖。
			if err := a.sink.RegisterRefreshIntent(ctx, RefreshIntent{
				TargetType:  RefreshTargetLibrary,
				LibraryID:   libraryID,
				LibraryName: libraryName,
			}); err != nil {
				if log != nil {
					log("登记 Emby 媒体库刷新意图失败", "library_id", libraryID, "err", err)
				}
				continue
			}
			registered++
			continue
		}
		for _, itemID := range itemIDs {
			if err := a.sink.RegisterRefreshIntent(ctx, RefreshIntent{
				TargetType:  RefreshTargetItem,
				LibraryID:   libraryID,
				LibraryName: libraryName,
				ItemID:      itemID,
			}); err != nil {
				if log != nil {
					log("登记 Emby 条目刷新意图失败", "item_id", itemID, "library_id", libraryID, "err", err)
				}
				continue
			}
			registered++
		}
	}
	return registered
}
