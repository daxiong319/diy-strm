package embyindex_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/embyindex"
	"litepan/internal/settings"
	"litepan/internal/store"
)

// TestTriggerIsCallableFromOutsidePackage 是本次接线修复的回归测试。
//
// 修复前：PerformEmbySync 等方法是导出的，但参数类型 configSnapshot 未导出，
// 包外代码能看到方法名却造不出参数，导致这些方法永远不可能被调用
// （vet 报：cannot use nil as embyindex.configSnapshot value in argument）。
// 本测试位于 package embyindex_test，即真正的包外视角，
// 只要有人再把配置类型改回未导出，这里就会编译失败。
func TestTriggerIsCallableFromOutsidePackage(t *testing.T) {
	cfg := embyindex.BuildConfig("cfg-1", "测试", "http://emby.local:8096/", " key ", nil, false)
	var svc *embyindex.Service
	// 仅验证签名可被包外构造与调用，不真正发请求。
	_, _ = svc.PerformEmbySync(context.Background(), cfg)
	_, _ = svc.PerformEmbyIncrementalSync(context.Background(), cfg)
	_, _ = svc.SyncEmbyItemByID(context.Background(), cfg, "item-1")
	svc.RequestScan()
	svc.SetConfigLoader(func(context.Context) (embyindex.Config, bool) { return cfg, true })
}

func TestBuildConfigNormalizesAndMarksAllSelected(t *testing.T) {
	tests := []struct {
		name        string
		configID    string
		embyURL     string
		apiKey      string
		libraryIDs  []string
		allSelected bool
		wantURL     string
		wantKey     string
		wantLibs    int
		wantAll     bool
	}{
		{
			name:     "去除地址尾部斜杠与密钥空白",
			embyURL:  "  http://emby.local:8096///  ",
			apiKey:   "  abc123  ",
			wantURL:  "http://emby.local:8096",
			wantKey:  "abc123",
			wantLibs: 0,
			wantAll:  true,
		},
		{
			name:        "显式全选",
			embyURL:     "http://a",
			apiKey:      "k",
			libraryIDs:  []string{"lib-1"},
			allSelected: true,
			wantURL:     "http://a",
			wantKey:     "k",
			wantLibs:    1,
			wantAll:     true,
		},
		{
			name:       "未选任何库等同于全选",
			embyURL:    "http://a",
			apiKey:     "k",
			libraryIDs: []string{"  ", ""},
			wantURL:    "http://a",
			wantKey:    "k",
			wantLibs:   0,
			wantAll:    true,
		},
		{
			name:       "只勾选部分库时不标记全选",
			embyURL:    "http://a",
			apiKey:     "k",
			libraryIDs: []string{" lib-1 ", "lib-2"},
			wantURL:    "http://a",
			wantKey:    "k",
			wantLibs:   2,
			wantAll:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := embyindex.BuildConfig("id-x", "名字", tc.embyURL, tc.apiKey, tc.libraryIDs, tc.allSelected)
			if got.EmbyURL != tc.wantURL {
				t.Errorf("EmbyURL = %q, want %q", got.EmbyURL, tc.wantURL)
			}
			if got.APIKey != tc.wantKey {
				t.Errorf("APIKey = %q, want %q", got.APIKey, tc.wantKey)
			}
			if len(got.LibraryIDs) != tc.wantLibs {
				t.Errorf("len(LibraryIDs) = %d, want %d", len(got.LibraryIDs), tc.wantLibs)
			}
			if got.AllSelected != tc.wantAll {
				t.Errorf("AllSelected = %v, want %v", got.AllSelected, tc.wantAll)
			}
		})
	}
}

func TestConfigUsableRequiresURLAndKey(t *testing.T) {
	tests := []struct {
		name string
		cfg  embyindex.Config
		want bool
	}{
		{name: "齐备", cfg: embyindex.Config{EmbyURL: "http://a", APIKey: "k"}, want: true},
		{name: "缺地址", cfg: embyindex.Config{APIKey: "k"}, want: false},
		{name: "缺密钥", cfg: embyindex.Config{EmbyURL: "http://a"}, want: false},
		{name: "全空", cfg: embyindex.Config{}, want: false},
		{name: "空白地址", cfg: embyindex.Config{EmbyURL: "   ", APIKey: "k"}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Usable(); got != tc.want {
				t.Errorf("Usable() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestScanOnceSkipsWithoutConfig 覆盖「未配置 Emby 时不空转」的约定。
func TestScanOnceSkipsWithoutConfig(t *testing.T) {
	tests := []struct {
		name        string
		loader      embyindex.ConfigLoader
		enabled     string
		wantOK      bool
		wantNoIndex bool
	}{
		{
			name:   "未注入配置读取器则跳过",
			loader: nil,
			wantOK: false,
		},
		{
			name: "配置不可用则跳过",
			loader: func(context.Context) (embyindex.Config, bool) {
				return embyindex.Config{}, false
			},
			wantOK: false,
		},
		{
			name: "配置缺密钥视作不可用",
			loader: func(context.Context) (embyindex.Config, bool) {
				return embyindex.Config{EmbyURL: "http://a"}, true
			},
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := newTestRepo(t)
			svc := embyindex.New(embyindex.Options{
				Index:    repo,
				Settings: newTestSettings(t, map[string]string{settings.KeyEmbyEnabled: "true"}),
			})
			if svc == nil {
				t.Fatal("New 返回 nil，仓储已提供")
			}
			if tc.loader != nil {
				svc.SetConfigLoader(tc.loader)
			}
			if _, ok := svc.ScanOnce(context.Background()); ok != tc.wantOK {
				t.Errorf("ScanOnce ok = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}

// TestScanOnceSkipsWhenDisabled 确认功能开关关闭时不扫描。
func TestScanOnceSkipsWhenDisabled(t *testing.T) {
	_, repo := newTestRepo(t)
	svc := embyindex.New(embyindex.Options{
		Index:    repo,
		Settings: newTestSettings(t, map[string]string{settings.KeyEmbyEnabled: "false"}),
	})
	if svc == nil {
		t.Fatal("New 返回 nil")
	}
	svc.SetConfigLoader(func(context.Context) (embyindex.Config, bool) {
		return embyindex.BuildConfig("c", "n", "http://a", "k", nil, true), true
	})
	if _, ok := svc.ScanOnce(context.Background()); ok {
		t.Error("功能关闭时 ScanOnce 应返回 ok=false")
	}
}

// TestNilServiceIsSafeNoOp 覆盖 nil 服务必须处处安全的约定。
func TestNilServiceIsSafeNoOp(t *testing.T) {
	var svc *embyindex.Service
	ctx := context.Background()
	svc.Start(ctx)
	svc.Stop(ctx)
	svc.RequestScan()
	svc.SetConfigLoader(nil)
	if _, ok := svc.ScanOnce(ctx); ok {
		t.Error("nil 服务 ScanOnce 应返回 ok=false")
	}
	if svc.SyncEnabled() {
		t.Error("nil 服务 SyncEnabled 应为 false")
	}
	if svc.DeleteNetdiskEnabled() {
		t.Error("nil 服务 DeleteNetdiskEnabled 应为 false")
	}
	// domain.Errf 每次构造新错误实例，不能用 errors.Is 比较具体值，应按错误码判定。
	if _, err := svc.PerformEmbySync(ctx, embyindex.Config{}); err == nil {
		t.Error("nil 服务 PerformEmbySync 应返回未实现错误")
	} else {
		var appErr *domain.AppError
		if !errors.As(err, &appErr) || appErr.Code != domain.CodeNotImplement {
			t.Errorf("nil 服务 PerformEmbySync 应返回 NOT_IMPLEMENT，实际 %v", err)
		}
	}
}

// TestStartIsIdempotentAndStops 确认重复 Start 不会起多个循环，Stop 能退出。
func TestStartIsIdempotentAndStops(t *testing.T) {
	_, repo := newTestRepo(t)
	svc := embyindex.New(embyindex.Options{
		Index:        repo,
		Settings:     newTestSettings(t, map[string]string{settings.KeyEmbyEnabled: "false"}),
		ScanInterval: 10 * time.Millisecond,
	})
	if svc == nil {
		t.Fatal("New 返回 nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	svc.Start(ctx) // 重复调用应为空操作
	svc.Start(ctx)
	time.Sleep(30 * time.Millisecond)
	svc.Stop(ctx)
	cancel()
	// 停止后能再次启动，说明 loopRunning 已被正确复位。
	svc.Start(context.Background())
	svc.Stop(context.Background())
}

// TestRequestScanIsNonBlocking 确认扫描请求信号可合并且从不阻塞调用方。
func TestRequestScanIsNonBlocking(t *testing.T) {
	_, repo := newTestRepo(t)
	svc := embyindex.New(embyindex.Options{
		Index:    repo,
		Settings: newTestSettings(t, map[string]string{settings.KeyEmbyEnabled: "false"}),
	})
	if svc == nil {
		t.Fatal("New 返回 nil")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			svc.RequestScan()
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RequestScan 阻塞了调用方")
	}
}

// testIndexRepo 打开一个内存仓储供外部测试构造 Service。
//
// 这里刻意不依赖包内 helper：本文件属于 package embyindex_test，
// 走的是与生产装配完全相同的公开 API 路径。
func testIndexRepo(t *testing.T) domain.EmbyIndexRepository {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(db).EmbyIndex
}
