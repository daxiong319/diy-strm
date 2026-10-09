package discovery

import (
	"context"
	"log"
	"sync"

	"litepan/internal/discover/connector"
	"litepan/internal/discover/hdhive"
)

// T05 · 连接器注册表 —— 订阅执行器与「有哪些搜索源」之间唯一的耦合点。
//
// 为什么在 discovery 包里而不在 connector 包里：连接器的**实现**必然调用
// discovery 里的检索函数（Tgto123SearchResources 等），若注册表也放在
// connector 包就形成 discovery → connector → discovery 的循环依赖。
// 所以接口与纯类型留在 internal/discover/connector（不含 discovery 依赖），
// 实现与注册表留在本包。
//
// 注册表是「惰性装配 + 显式就绪」的：连接器在 InitConnectors 里注册，
// 是否真的能搜由各自的 Ready() 决定（配置了地址、装了通道工厂…），
// 订阅执行时按 Ready() 过滤，未就绪的源跳过并记日志而不是让整轮失败。

var (
	connectorsMu sync.RWMutex
	connectorReg *connector.Registry
)

// InitConnectors 幂等地重建连接器注册表（装配层启动时调用，测试里反复调用）。
func InitConnectors() {
	reg := connector.NewRegistry()
	// tgto123：目前唯一在订阅流水线上真正跑通的源。第一个注册 ⇒
	// SearchAll 按注册顺序串行执行 ⇒ 默认单源场景下候选顺序与改动前完全一致。
	reg.Register(NewTgto123Connector())
	// hdhive：第二源，用来验证「多源」这条链路本身成立（超时、预算、
	// 合并去重、跨源排序）。它是否就绪取决于 RE0 官方通道是否装配，
	// 见 connector_hdhive.go 顶部说明。
	reg.Register(NewHdHiveConnector())

	connectorsMu.Lock()
	connectorReg = reg
	connectorsMu.Unlock()
}

// ConnectorFor 按 key 取连接器。注册表未初始化时惰性初始化一次。
func ConnectorFor(key string) (connector.Connector, bool) {
	connectorsMu.RLock()
	reg := connectorReg
	connectorsMu.RUnlock()
	if reg == nil {
		InitConnectors()
		connectorsMu.RLock()
		reg = connectorReg
		connectorsMu.RUnlock()
	}
	return reg.Get(key)
}

// AllConnectors 返回全部已注册连接器（按注册顺序）。
func AllConnectors() []connector.Connector {
	connectorsMu.RLock()
	defer connectorsMu.RUnlock()
	if connectorReg == nil {
		return nil
	}
	return connectorReg.All()
}

// ConnectorKeys 返回全部已注册 key，供设置页渲染下拉。
func ConnectorKeys() []string {
	connectorsMu.RLock()
	defer connectorsMu.RUnlock()
	if connectorReg == nil {
		return nil
	}
	return connectorReg.Keys()
}

// SubscriptionConnectors 取一条订阅要用的连接器，返回已就绪的与被跳过的。
// 被跳过的源带上原因（未注册 / 未就绪），调用方负责记日志 —— 「这条订阅只搜到了
// 一个源」必须在运行日志里看得见，否则用户会以为订阅坏了。
func SubscriptionConnectors(keys []string) (ready []connector.Connector, skipped []connector.SkipReason) {
	connectorsMu.RLock()
	reg := connectorReg
	connectorsMu.RUnlock()
	if reg == nil {
		InitConnectors()
		connectorsMu.RLock()
		reg = connectorReg
		connectorsMu.RUnlock()
	}
	if reg == nil {
		return nil, nil
	}
	return reg.Resolve(keys)
}

// connectorPurposeSubscriptions 订阅检索场景。
const connectorPurposeSubscriptions = connector.PurposeSubscriptions

// hiveResourceRawKey connector.Item.Raw 里存放原始 hdhive.Resource 的键。
// 约定：只要连接器返回的候选 Raw 里带了这个键，订阅引擎就复用既有的
// resourceFromHive 转回候选，从而与接口化之前逐字节一致；不带则走通用转换。
const hiveResourceRawKey = "hive_resource"

// hiveResourceOf 从 Item.Raw 取原始 hdhive.Resource。
func hiveResourceOf(it connector.Item) (hdhive.Resource, bool) {
	if it.Raw == nil {
		return hdhive.Resource{}, false
	}
	r, ok := it.Raw[hiveResourceRawKey].(hdhive.Resource)
	return r, ok
}

// init 保证任何一次订阅执行都不依赖调用方记得初始化注册表。
func init() {
	InitConnectors()
}

// logConnectorSkips 把被跳过的搜索源写进订阅运行日志。
func logConnectorSkips(sub *DiscoverySubscription, skipped []connector.SkipReason) {
	for _, s := range skipped {
		log.Printf("[discovery] connector_skipped sub_id=%d key=%s reason=%s",
			sub.ID, s.Key, s.Reason)
	}
}

// searchWithConnectors 按订阅配置的多源执行检索，返回合并去重后的候选与被跳过的源。
//
// 这是 T05 之后订阅检索的唯一入口：单一数据源走它与改动前行为一致
// （SearchAll 对单源只是加了一层超时与错误包装，不改候选内容与顺序）。
func searchWithConnectors(ctx context.Context, sub *DiscoverySubscription, q connector.Query) ([]connector.Item, []connector.SkipReason) {
	keys := subscriptionSearchSources(sub)
	ready, skipped := SubscriptionConnectors(keys)
	logConnectorSkips(sub, skipped)

	outcomes := connector.SearchAll(ctx, ready, q,
		ConnectorTimeout(), ConnectorSearchBudget())
	for _, o := range outcomes {
		if o.Skipped {
			log.Printf("[discovery] connector_skipped sub_id=%d key=%s reason=搜索预算耗尽（已用 %s）",
				sub.ID, o.Key, o.Elapsed.Round(1e9))
			continue
		}
		if o.Err != nil {
			log.Printf("[discovery] connector_failed sub_id=%d key=%s elapsed=%s err=%v",
				sub.ID, o.Key, o.Elapsed.Round(1e9), o.Err)
		}
	}

	merged, dup := connector.Merge(outcomes)
	for _, d := range dup {
		log.Printf("[discovery] connector_dedup sub_id=%d key=%s dedup_key=%s title=%s 与来源 %s 重复，丢弃",
			sub.ID, d.Key, d.DedupKey, d.Title, d.KeptFrom)
	}
	return merged, skipped
}
