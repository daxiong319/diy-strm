package discovery

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	"litepan/internal/discover/ddb"
	"litepan/internal/discover/dmodels"
	"litepan/internal/settings"
)

// fakeConfigRepo 内存配置仓库（settings.ConfigRepository 的最小实现），
// 用来把「全局 Emby 反代实例」真正接进 dmodels.GetEmbyConfig 的可读路径。
type fakeConfigRepo struct{ vals map[string]string }

func (r *fakeConfigRepo) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := r.vals[key]
	return v, ok, nil
}

func (r *fakeConfigRepo) Set(_ context.Context, key, value string) error {
	r.vals[key] = value
	return nil
}

func (r *fakeConfigRepo) All(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.vals))
	for k, v := range r.vals {
		out[k] = v
	}
	return out, nil
}

// bindFakeGlobalEmby 把 emby_proxy_instances 注入 dmodels 的 settings 服务，
// 使 dmodels.GetEmbyConfig() 返回一个「全局 Emby」配置——也就是修复前会被
// MediaEmbyConfig 静默继承的那份配置。测试结束恢复为 nil，避免污染其它用例。
func bindFakeGlobalEmby(t *testing.T, embyURL, apiKey string) {
	t.Helper()
	repo := &fakeConfigRepo{vals: map[string]string{}}
	if embyURL != "" || apiKey != "" {
		blob, err := json.Marshal([]map[string]string{{"emby_url": embyURL, "api_key": apiKey}})
		if err != nil {
			t.Fatalf("marshal emby_proxy_instances: %v", err)
		}
		repo.vals[settings.KeyEmbyProxyInstances] = string(blob)
	}
	svc, err := settings.New(context.Background(), repo)
	if err != nil {
		t.Fatalf("构造 settings 服务失败：%v", err)
	}
	dmodels.BindSettings(svc)
	t.Cleanup(func() { dmodels.BindSettings(nil) })

	// 前置断言：确认全局 Emby 确实可读，否则这个回归用例是空转的。
	if _, err := dmodels.GetEmbyConfig(); err != nil {
		t.Fatalf("GetEmbyConfig 报错：%v", err)
	}
}

// setupEmbyConfigTestDB 初始化发现板块库表（ddb.Init 是 once 语义，同进程只生效一次），
// 每个用例前清空 discovery_settings，避免用例之间互相污染。
func setupEmbyConfigTestDB(t *testing.T) {
	t.Helper()
	if ddb.Db == nil {
		if err := ddb.Init(filepath.Join(t.TempDir(), "test.db"), slog.Default()); err != nil {
			t.Fatalf("初始化测试数据库失败：%v", err)
		}
	}
	if err := ddb.Db.Exec("DROP TABLE IF EXISTS discovery_settings").Error; err != nil {
		t.Fatalf("清理 discovery_settings 失败：%v", err)
	}
	if err := ddb.Db.AutoMigrate(&DiscoverySetting{}); err != nil {
		t.Fatalf("建表失败：%v", err)
	}
}

// setMediaEmbySetting 写 media_emby 设置项。
func setMediaEmbySetting(t *testing.T, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal setting: %v", err)
	}
	row := DiscoverySetting{Key: SettingMediaEmby, Value: string(raw)}
	if err := ddb.Db.Save(&row).Error; err != nil {
		t.Fatalf("save setting: %v", err)
	}
}

// TestMediaEmbyConfigNotInheritsGlobalEmby 是 D6 的核心回归用例：
// 缺集补档的配置必须留空，绝不能回退到全局 Emby 配置。
//
// 背景：修复前 MediaEmbyConfig 在 server_url 或 api_key 为空时会调用
// dmodels.GetEmbyConfig() 回退，把别的功能里的 Emby 服务器地址/Key 预填进
// 缺集补档，用户看到的是别人的配置。这里把全局 Emby 真正接上，确保旧实现
// 会命中回退分支——否则用例是空转的。
func TestMediaEmbyConfigNotInheritsGlobalEmby(t *testing.T) {
	setupEmbyConfigTestDB(t)
	// 全局 Emby 配置齐全：修复前这里会被静默继承。
	bindFakeGlobalEmby(t, "http://global-emby:8096", "global-key")
	if cfg, err := dmodels.GetEmbyConfig(); err != nil || cfg == nil || cfg.EmbyUrl == "" {
		t.Fatalf("用例前置条件不满足：全局 Emby 未生效（cfg=%+v err=%v）", cfg, err)
	}

	// 场景 1：media_emby 完全未写入 → 必须全空，不得继承全局
	enabled, url, key := MediaEmbyConfig()
	if enabled || url != "" || key != "" {
		t.Fatalf("未配置时不应继承全局 Emby：got enabled=%v url=%q key=%q", enabled, url, key)
	}

	// 场景 2：map 存在但没有字段 → 同样全空
	setMediaEmbySetting(t, map[string]any{})
	enabled, url, key = MediaEmbyConfig()
	if enabled || url != "" || key != "" {
		t.Fatalf("空 map 不应继承全局 Emby：got enabled=%v url=%q key=%q", enabled, url, key)
	}

	// 场景 3：只填地址、缺 Key → 视为未配置，且不得借全局 Key 补全
	setMediaEmbySetting(t, map[string]any{"enabled": true, "server_url": "http://my-emby:8096", "api_key": ""})
	enabled, url, key = MediaEmbyConfig()
	if enabled {
		t.Fatalf("只有地址时不应判定为已配置：got enabled=%v url=%q key=%q", enabled, url, key)
	}
	if key != "" {
		t.Fatalf("缺 Key 时不得继承全局 Key：got key=%q", key)
	}
	if url != "http://my-emby:8096" {
		t.Fatalf("已填写的地址应原样返回（供前端回填）：got url=%q", url)
	}

	// 场景 4：只填 Key、缺地址 → 视为未配置，地址不得继承全局地址
	setMediaEmbySetting(t, map[string]any{"enabled": true, "server_url": "", "api_key": "my-key"})
	enabled, url, key = MediaEmbyConfig()
	if enabled {
		t.Fatalf("只有 Key 时不应判定为已配置：got enabled=%v url=%q key=%q", enabled, url, key)
	}
	if url != "" {
		t.Fatalf("缺地址时不得继承全局地址：got url=%q", url)
	}
	if key != "my-key" {
		t.Fatalf("已填写的 Key 应原样返回：got key=%q", key)
	}

	// 场景 5：地址与 Key 齐全但 enabled=false → 不算已配置（需用户显式启用）
	setMediaEmbySetting(t, map[string]any{"enabled": false, "server_url": "http://my-emby:8096", "api_key": "my-key"})
	enabled, url, key = MediaEmbyConfig()
	if enabled {
		t.Fatalf("enabled=false 时不应判定为已配置：got enabled=%v url=%q key=%q", enabled, url, key)
	}
	if url != "http://my-emby:8096" || key != "my-key" {
		t.Fatalf("已填写的值应原样返回：got url=%q key=%q", url, key)
	}

	// 场景 6：齐全且启用 → 正常返回（值去空格）
	setMediaEmbySetting(t, map[string]any{"enabled": true, "server_url": "  http://my-emby:8096  ", "api_key": "  my-key  "})
	enabled, url, key = MediaEmbyConfig()
	if !enabled || url != "http://my-emby:8096" || key != "my-key" {
		t.Fatalf("齐全且启用时应返回去空格后的配置：got enabled=%v url=%q key=%q", enabled, url, key)
	}
}

// TestMediaEmbyClientRejectsUnconfigured 未配置时客户端构造必须失败，
// 以便调用方返回「请先配置」而不是拿空地址去请求。
func TestMediaEmbyClientRejectsUnconfigured(t *testing.T) {
	setupEmbyConfigTestDB(t)
	bindFakeGlobalEmby(t, "http://global-emby:8096", "global-key")

	if _, err := mediaEmbyClient(); err == nil {
		t.Fatal("未配置时 mediaEmbyClient 应返回错误")
	}

	// 全局 Emby 存在也不能让本功能「可用」
	setMediaEmbySetting(t, map[string]any{"enabled": true, "server_url": "", "api_key": ""})
	if _, err := mediaEmbyClient(); err == nil {
		t.Fatal("仅有全局 Emby 时 mediaEmbyClient 不应可用")
	}
}
