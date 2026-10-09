package app

import (
	"context"
	"fmt"

	"litepan/internal/auth"
	"litepan/internal/cache"
	"litepan/internal/config"
	"litepan/internal/core/driverexec"
	"litepan/internal/driver"
	"litepan/internal/eventbus"
	"litepan/internal/guardrail"
	"litepan/internal/logx"
	"litepan/internal/settings"
	"litepan/pkg/secretkey"
)

type coreBundle struct {
	bus      *eventbus.Bus
	cache    *cache.Service
	drivers  *driver.Manager
	auth     *auth.Service
	sched    *auth.Scheduler
	exec     *driverexec.Executor
	listHits *cache.HitTracker
	// breaker 是风控熔断器（T15）。放在 core 层而不是 services 层：
	// 它要在 file/mediaorganize 之前就接进 driver.Manager，装配顺序不能反。
	breaker *guardrail.Breaker
	secret  []byte
}

func wireCore(ctx context.Context, cfg config.Config, logs *logx.Manager, st *storeBundle) (*coreBundle, error) {
	bus := eventbus.New(logs.For(logx.ModuleSystem))
	cacheSvc := cache.NewService(cache.Options{
		MaxItems: st.settings.Int(settings.KeyCacheMaxItems),
		MemLimit: int64(st.settings.Int(settings.KeyCacheMemoryLimitMB)) * 1024 * 1024,
		Log:      logs.For(logx.ModuleCache),
	})
	cache.NewCleaner(cacheSvc, logs.For(logx.ModuleCache)).Register(bus)

	mgr := driver.NewManager(st.store.Accounts, st.store.AuthStates, st.store.Configs, logs.For(logx.ModuleDriver))
	authSvc := auth.NewService(auth.Options{
		Accounts:   st.store.Accounts,
		AuthStates: st.store.AuthStates,
		Drivers:    mgr,
		Bus:        bus,
		Log:        logs.For(logx.ModuleAuth),
		ActiveEnabled: func() bool {
			return st.settings.Bool(settings.KeyAuthActiveRefresh)
		},
	})
	if err := authSvc.LoadManagedAccounts(ctx); err != nil {
		return nil, fmt.Errorf("load auth accounts: %w", err)
	}

	initCachePersistence(cacheSvc, st.settings, cfg.DataDir)

	secret, err := secretkey.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("load secret key: %w", err)
	}

	// 风控熔断（T15）必须在 drivers 建好之后、file 服务之前接进去：
	// 熔断靠 driver.Manager 的调用观察者计数，接晚了就漏掉前面的调用。
	breaker := wireGuardrail(st, mgr, logs)

	return &coreBundle{
		bus:      bus,
		cache:    cacheSvc,
		drivers:  mgr,
		auth:     authSvc,
		sched:    auth.NewScheduler(authSvc, logs.For(logx.ModuleAuth)),
		exec:     driverexec.New(mgr, authSvc.Gate()),
		listHits: cache.NewHitTracker(),
		breaker:  breaker,
		secret:   secret,
	}, nil
}
