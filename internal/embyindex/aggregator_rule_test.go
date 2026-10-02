package embyindex

import (
	"context"
	"sync"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/settings"
	"litepan/internal/store"
)

// 本文件用包内视角覆盖刷新聚合规则。
//
// 聚合器 refreshAggregator 与 Service.refreshSink 都是未导出的实现细节，
// 不适合为了测试把它们导出；但它们承载了「超过阈值升级为库级刷新」这条
// 与老版 EmbyRefreshItemAggregationThreshold 对齐的关键规则，
// 因此用包内测试直接覆盖，外部行为则由 schedule_test.go 覆盖公开 API。

// accountingSink 记录收到的刷新意图。
type accountingSink struct {
	mu      sync.Mutex
	intents []RefreshIntent
	err     error
}

func (s *accountingSink) RegisterRefreshIntent(_ context.Context, intent RefreshIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.intents = append(s.intents, intent)
	return nil
}

func (s *accountingSink) snapshot() []RefreshIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RefreshIntent, len(s.intents))
	copy(out, s.intents)
	return out
}

// newAggregatorServiceRule 构造一个只依赖刷新投递的服务，不触碰网络与数据库。
func newAggregatorServiceRule(t *testing.T, sink RefreshIntentSink, threshold int) *Service {
	t.Helper()
	svc := New(Options{
		Index:                       testIndexRepoInternal(t),
		Settings:                    newTestSettingsInternal(t),
		RefreshSink:                 sink,
		RefreshAggregationThreshold: threshold,
	})
	if svc == nil {
		t.Fatal("New 返回 nil，仓储已提供")
	}
	return svc
}

// TestRefreshAggregatorGroupsByLibrary 覆盖「超过阈值改登记库级刷新」的聚合规则。
//
// 阈值语义：条目数 > 阈值 才升级为库级，等于阈值仍逐条登记。
// 这与老版 EmbyRefreshItemAggregationThreshold=10 的用法保持一致。
func TestRefreshAggregatorGroupsByLibrary(t *testing.T) {
	tests := []struct {
		name          string
		threshold     int
		items         int
		wantLibrary   int
		wantItemLevel int
	}{
		{name: "低于阈值逐条登记", threshold: 10, items: 3, wantLibrary: 0, wantItemLevel: 3},
		{name: "恰好等于阈值仍逐条登记", threshold: 10, items: 10, wantLibrary: 0, wantItemLevel: 10},
		{name: "超过阈值升级为库级", threshold: 10, items: 11, wantLibrary: 1, wantItemLevel: 0},
		{name: "阈值取默认值", threshold: 0, items: 11, wantLibrary: 1, wantItemLevel: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := &accountingSink{}
			svc := newAggregatorServiceRule(t, sink, tc.threshold)
			// 直接驱动聚合器，避免依赖 Emby 网络：通过导出入口不可行，
			// 因此这里用一个包内可见的辅助（见 aggregator_test.go 的说明）。
			registerTestIntents(t, svc, "lib-1", "电影库", tc.items)

			var library, itemLevel int
			for _, in := range sink.snapshot() {
				switch in.TargetType {
				case RefreshTargetLibrary:
					library++
					if in.LibraryID != "lib-1" {
						t.Errorf("库级意图 LibraryID = %q, want lib-1", in.LibraryID)
					}
					if in.LibraryName != "电影库" {
						t.Errorf("库级意图 LibraryName = %q, want 电影库", in.LibraryName)
					}
				case RefreshTargetItem:
					itemLevel++
					if in.ItemID == "" {
						t.Error("条目级意图 ItemID 不应为空")
					}
				}
			}
			if library != tc.wantLibrary {
				t.Errorf("库级意图 = %d, want %d", library, tc.wantLibrary)
			}
			if itemLevel != tc.wantItemLevel {
				t.Errorf("条目级意图 = %d, want %d", itemLevel, tc.wantItemLevel)
			}
		})
	}
}

// TestRefreshAggregatorNilSinkIsSilent 确认未装配刷新队列时聚合器退化为空操作。
func TestRefreshAggregatorNilSinkIsSilent(t *testing.T) {
	svc := newAggregatorServiceRule(t, nil, 0)
	// 不 panic 即为通过。
	registerTestIntents(t, svc, "lib-1", "电影库", 3)
}

// accountingSink 记录收到的刷新意图。

// newTestSettingsInternal 构造开启 Emby 功能的设置服务。
func newTestSettingsInternal(t *testing.T) *settings.Service {
	t.Helper()
	svc, err := settings.New(context.Background(), &aggConfigRepo{values: map[string]string{settings.KeyEmbyEnabled: "true"}})
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}
	return svc
}

// aggConfigRepo 是本包内的最小设置仓储，避免依赖外部测试包的 stub。
type aggConfigRepo struct {
	values map[string]string
}

func (r *aggConfigRepo) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := r.values[key]
	return v, ok, nil
}

func (r *aggConfigRepo) Set(_ context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *aggConfigRepo) All(context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}

// testIndexRepoInternal 取一个内存仓储供聚合测试构造 Service。
func testIndexRepoInternal(t *testing.T) domain.EmbyIndexRepository {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(db).EmbyIndex
}
