package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"litepan/internal/discover/discovery"
	"litepan/internal/logx"
	"litepan/internal/mediarequest"
)

// reconcileInterval 求片对账周期。
//
// 15 分钟是权衡出来的：对账只做两类事 ——
// ①已审核通过、但订阅没建成的补建（失败通常是当时没配保存目录）；
// ②订阅已转存、但状态还停在 approved 的标 fulfilled。
// 两件都是「补事实」不是「做决策」，慢一点没代价；
// 跑得更勤反而会在「订阅刚建好、转存还没发生」的窗口里反复空转。
const reconcileInterval = 15 * time.Minute

// requestCenterBundle 求片中心装配结果（T09）。
type requestCenterBundle struct {
	svc *mediarequest.Service
}

// Enabled 求片中心这次是否真的装上了。
func (b *requestCenterBundle) Enabled() bool { return b != nil && b.svc != nil }

// maybeService 取服务，nil 接收者返回 nil ——
// 让装配层可以无脑写 reqCenter.maybeService()，不用先判空。
func (b *requestCenterBundle) maybeService() *mediarequest.Service {
	if b == nil {
		return nil
	}
	return b.svc
}

// wireRequestCenter 构造求片中心服务（参考实现 移植⑨）。
//
// 返回 nil 时接口层一律按「功能未启用」处理，理由同 wireRBAC：
// 一个没装上的功能没有任何理由变成 500。
func wireRequestCenter(st *storeBundle, logs *logx.Manager) *requestCenterBundle {
	if st == nil || st.settings == nil || st.store == nil || st.store.DB == nil {
		return nil
	}
	var write, read *sql.DB
	if st.store.DB.WriteHandle() != nil {
		write = st.store.DB.WriteHandle()
	}
	if st.store.DB.ReadHandle() != nil {
		read = st.store.DB.ReadHandle()
	}
	if write == nil {
		// 与 wireRBAC 同一条纪律：没有写句柄就不给。
		// 求片站一开就是「能让外人往订阅表里写东西」的能力，
		// 开一个写不进去的半成品比不开危险得多。
		return nil
	}
	return &requestCenterBundle{
		svc: mediarequest.NewService(mediarequest.Params{
			Store: mediarequest.NewStore(write, read),
			Cfg:   st.settings,
			// 三个适配器都只转发给 discovery 的包级函数 —— 订阅表只有那一份，
			// 求片中心和发现引擎共用它，identity_key、账本、多源自动全都自动生效。
			// 刻意不走 discovery 的 Service：那边要构造一整套依赖，
			// 而求片只会用到这三个函数，硬依赖整个 Service 只是为了拿三个函数。
			Subs:   subscriptionSaver{},
			Items:  subscriptionItemLister{},
			Search: mediaSearcher{},
			Log:    mediarequest.NewSlogLogger(logs.For(logx.ModuleAPI)),
		}),
	}
}

// Start 起后台对账协程（求片站自己的端口由 mediarequest.Listener 管，不在这里）。
//
// 只在启用时起：功能关着还每 15 分钟扫一次表是纯浪费。
// 挂在调用方给的 ctx 上与应用同生共死 —— 单独起 context 会留下
// 「应用已关、对账还在跑」的僵尸协程。
func (b *requestCenterBundle) Start(ctx context.Context, logs *logx.Manager) {
	if b == nil || b.svc == nil || !b.svc.Enabled() {
		return
	}
	logs.For(logx.ModuleAPI).Info("求片对账已启动", "interval", reconcileInterval)
	go func() {
		ticker := time.NewTicker(reconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := b.svc.Reconcile(ctx)
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						logs.For(logx.ModuleAPI).Warn("求片对账失败", "err", err)
					}
					continue
				}
				if n > 0 {
					logs.For(logx.ModuleAPI).Info("求片对账完成", "handled", n)
				}
			}
		}
	}()
}

// subscriptionSaver 转发 discovery.SaveSubscription。
type subscriptionSaver struct{}

func (subscriptionSaver) SaveSubscription(payload *discovery.SubscriptionUpsertPayload) (*discovery.DiscoverySubscription, string, error) {
	return discovery.SaveSubscription(payload)
}

// subscriptionItemLister 转发 discovery.ListSubscriptionItems。
type subscriptionItemLister struct{}

func (subscriptionItemLister) ListSubscriptionItems(id uint, status string, limit int) ([]discovery.DiscoverySubscriptionItem, error) {
	return discovery.ListSubscriptionItems(id, status, limit)
}

// mediaSearcher 转发 discovery.SearchMedia。
type mediaSearcher struct{}

// force 固定传 false：求片站的搜索是「随手查一下」，
// 传 true 会让它去刷新演员资料（联网、可能几十秒），
// 手机端一次输入框回车不该等这么久。
func (mediaSearcher) SearchMedia(ctx context.Context, q, mediaType string, page int, force bool) (*discovery.ActorsPage, error) {
	return discovery.SearchMedia(ctx, q, mediaType, page, false)
}
