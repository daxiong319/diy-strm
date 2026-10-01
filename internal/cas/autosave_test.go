package cas

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"litepan/internal/cas/cloud189"
	"litepan/internal/domain"
)

// withConfig 临时替换包级配置存储，运行 fn 后恢复。
func withConfig(t *testing.T, cfg CasConfig) {
	t.Helper()
	store := map[string]string{}
	raw, _ := json.Marshal(cfg)
	store[configKey] = string(raw)
	prevGet, prevSet := configStore, configSetter
	BindConfigStore(
		func(key string) (string, bool) { v, ok := store[key]; return v, ok },
		func(key, value string) { store[key] = value },
	)
	t.Cleanup(func() {
		configStore, configSetter = prevGet, prevSet
	})
}

// withAutoSave 临时注入转存器/账号表/通知器，结束后恢复。
func withAutoSave(t *testing.T, saver AutoSaveSaver, accounts AccountLister) *[]string {
	t.Helper()
	prevSaver, prevAccounts, prevNotifier := autoSaveSaver, autoSaveAccounts, autoSaveNotifier
	notified := &[]string{}
	autoSaveSaver, autoSaveNotifier = saver, nil
	if accounts != nil {
		autoSaveAccounts = accounts
	}
	BindAutoSaveNotifier(func(_ context.Context, level, _, title, message string, _, _ int64) {
		*notified = append(*notified, level+"|"+title+"|"+message)
	})
	t.Cleanup(func() {
		autoSaveSaver, autoSaveAccounts, autoSaveNotifier = prevSaver, prevAccounts, prevNotifier
	})
	return notified
}

func TestNormalizeDriveType(t *testing.T) {
	cases := map[string]string{
		"189_cloud": "cloud189",
		"189cloud":  "cloud189",
		"天翼云盘":      "cloud189",
		"139_cloud": "cloud139",
		"pan139":    "cloud139",
		"移动云盘":      "cloud139",
		"quark":     "quark",
		"夸克":        "quark",
		"115_open":  "115_open",
		"123pan":    "123_open",
		"guangya":   "guangya",
		"":          "",
	}
	for in, want := range cases {
		if got := NormalizeDriveType(in); got != want {
			t.Errorf("NormalizeDriveType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveDriveTypePrefersSourceDrive(t *testing.T) {
	m := cloud189.CasManifestV2{SourceDrive: "189_cloud", Hashes: cloud189.HashSet{Sha256: "x"}}
	if got := ResolveDriveType(m); got != "cloud189" {
		t.Errorf("应优先采用 sourceDrive，got %q", got)
	}
	// sourceDrive 缺失时按哈希推断
	m2 := cloud189.CasManifestV2{Hashes: cloud189.HashSet{Sha256: "x"}}
	if got := ResolveDriveType(m2); got != "cloud139" {
		t.Errorf("Sha256 应推断为 cloud139，got %q", got)
	}
	m3 := cloud189.CasManifestV2{Hashes: cloud189.HashSet{FileMd5: "a", PreHash: "p"}}
	if got := ResolveDriveType(m3); got != "quark" {
		t.Errorf("PreHash 应优先推断为 quark，got %q", got)
	}
	m4 := cloud189.CasManifestV2{Hashes: cloud189.HashSet{FileMd5: "a"}}
	if got := ResolveDriveType(m4); got != "cloud189" {
		t.Errorf("FileMd5 应推断为 cloud189，got %q", got)
	}
	if got := ResolveDriveType(cloud189.CasManifestV2{}); got != "" {
		t.Errorf("无信息应返回空，got %q", got)
	}
}

func TestPickAutoSaveAccount(t *testing.T) {
	accounts := []*domain.Account{
		{ID: 1, DriverType: "139_cloud", IsActive: true},
		{ID: 2, DriverType: "189_cloud", IsActive: false}, // 未启用应跳过
		{ID: 3, DriverType: "189_cloud", IsActive: true},
		{ID: 4, DriverType: "189_cloud", IsActive: true},
		nil,
	}
	if got := pickAutoSaveAccount(accounts, "cloud189", 0); got == nil || got.ID != 3 {
		t.Errorf("wantID=0 应取第一个启用匹配账号(ID=3)，got %+v", got)
	}
	if got := pickAutoSaveAccount(accounts, "cloud189", 4); got == nil || got.ID != 4 {
		t.Errorf("wantID=4 应精确匹配，got %+v", got)
	}
	if got := pickAutoSaveAccount(accounts, "cloud189", 999); got == nil || got.ID != 3 {
		t.Errorf("wantID 未命中应回落到首个匹配，got %+v", got)
	}
	if got := pickAutoSaveAccount(accounts, "quark", 0); got != nil {
		t.Errorf("无匹配网盘应返回 nil，got %+v", got)
	}
}

func TestResolveSaveDir(t *testing.T) {
	cfg := CasConfig{
		CASNotifyAutoSaveDir: "CAS",
		CASNotifyAutoSaveDrives: map[string]AutoSaveDriveConfig{
			"cloud189": {SaveDir: " CAS/天翼 "},
			"quark":    {SaveDir: ""},
		},
	}
	if got := resolveSaveDir(cfg, "cloud189"); got != "CAS/天翼" {
		t.Errorf("账号级目录优先且去空白，got %q", got)
	}
	if got := resolveSaveDir(cfg, "quark"); got != "CAS" {
		t.Errorf("空账号级目录应回落全局，got %q", got)
	}
	if got := resolveSaveDir(cfg, "cloud139"); got != "CAS" {
		t.Errorf("未配置网盘应回落全局，got %q", got)
	}
}

func TestAutoSaveCASFilesSuccess(t *testing.T) {
	withConfig(t, CasConfig{
		CASNotifyAutoSave:       true,
		CASNotifyAutoSaveDir:    "CAS",
		CASNotifyAutoSaveDrives: map[string]AutoSaveDriveConfig{},
	})
	var gotAccount int64
	var gotDir, gotName, gotContent string
	notified := withAutoSave(t,
		func(_ context.Context, accountID int64, saveDir string, file AutoSaveSourceFile) (string, error) {
			gotAccount, gotDir, gotName, gotContent = accountID, saveDir, file.FileName, file.Content
			return "file-1", nil
		},
		func(context.Context) ([]*domain.Account, error) {
			return []*domain.Account{{ID: 7, DriverType: "189_cloud", IsActive: true}}, nil
		},
	)

	content := cloud189.EncodeManifestV2(cloud189.CasManifestV2{
		Version: 2, FileName: "a.mkv", FileSize: 10,
		Hashes: cloud189.HashSet{FileMd5: "m", SliceMd5: "s"},
	})
	res := AutoSaveCASFiles(context.Background(), []AutoSaveSourceFile{{FileName: "a.cas", Content: content}})
	if len(res) != 1 || !res[0].Saved {
		t.Fatalf("应当转存成功，got %+v", res)
	}
	if res[0].DriveType != "cloud189" || res[0].AccountID != 7 || res[0].SavedFileName != "a.cas" {
		t.Errorf("结果字段不符：%+v", res[0])
	}
	if gotAccount != 7 || gotDir != "CAS" || gotName != "a.cas" || gotContent == "" {
		t.Errorf("落盘参数不符：account=%d dir=%q name=%q content=%q", gotAccount, gotDir, gotName, gotContent)
	}
	if len(*notified) != 1 || !strings.Contains((*notified)[0], "已自动转存") {
		t.Errorf("应发出成功通知，got %v", *notified)
	}
}

func TestAutoSaveCASFilesSkippedReasons(t *testing.T) {
	valid := cloud189.EncodeManifestV2(cloud189.CasManifestV2{
		Version: 2, FileName: "a.mkv", FileSize: 10,
		Hashes: cloud189.HashSet{FileMd5: "m"},
	})

	// 1) 全局关闭
	withConfig(t, CasConfig{CASNotifyAutoSave: false, CASNotifyAutoSaveDir: "CAS"})
	withAutoSave(t, func(context.Context, int64, string, AutoSaveSourceFile) (string, error) { return "", nil },
		func(context.Context) ([]*domain.Account, error) { return nil, nil })
	res := AutoSaveCASFiles(context.Background(), []AutoSaveSourceFile{{FileName: "a.cas", Content: valid}})
	if !res[0].Skipped || !strings.Contains(res[0].Reason, "未开启") {
		t.Errorf("应因未开启跳过，got %+v", res[0])
	}

	// 2) 内容非法
	withConfig(t, CasConfig{CASNotifyAutoSave: true, CASNotifyAutoSaveDir: "CAS"})
	res = AutoSaveCASFiles(context.Background(), []AutoSaveSourceFile{{FileName: "a.cas", Content: "not-a-manifest"}})
	if !res[0].Skipped || !strings.Contains(res[0].Reason, "解析 .cas 失败") {
		t.Errorf("应因解析失败跳过，got %+v", res[0])
	}

	// 3) 无法判定网盘
	res = AutoSaveCASFiles(context.Background(), []AutoSaveSourceFile{{
		FileName: "a.cas",
		Content:  cloud189.EncodeManifestV2(cloud189.CasManifestV2{Version: 2, FileName: "a.mkv", FileSize: 10}),
	}})
	if !res[0].Skipped || !strings.Contains(res[0].Reason, "无法判定") {
		t.Errorf("应因无法判定网盘跳过，got %+v", res[0])
	}

	// 4) 该网盘已关闭
	withConfig(t, CasConfig{
		CASNotifyAutoSave:    true,
		CASNotifyAutoSaveDir: "CAS",
		CASNotifyAutoSaveDrives: map[string]AutoSaveDriveConfig{
			"cloud189": {Enabled: false},
		},
	})
	res = AutoSaveCASFiles(context.Background(), []AutoSaveSourceFile{{FileName: "a.cas", Content: valid}})
	if !res[0].Skipped || !strings.Contains(res[0].Reason, "已关闭") {
		t.Errorf("应因网盘关闭跳过，got %+v", res[0])
	}

	// 5) 未找到可用账号
	withConfig(t, CasConfig{CASNotifyAutoSave: true, CASNotifyAutoSaveDir: "CAS"})
	withAutoSave(t, func(context.Context, int64, string, AutoSaveSourceFile) (string, error) { return "", nil },
		func(context.Context) ([]*domain.Account, error) { return nil, nil })
	res = AutoSaveCASFiles(context.Background(), []AutoSaveSourceFile{{FileName: "a.cas", Content: valid}})
	if !res[0].Skipped || !strings.Contains(res[0].Reason, "未找到") {
		t.Errorf("应因无账号跳过，got %+v", res[0])
	}

	// 6) 空输入
	if got := AutoSaveCASFiles(context.Background(), nil); len(got) != 0 {
		t.Errorf("空输入应返回空结果，got %+v", got)
	}
}

func TestAutoSaveCASFilesUnnamedFallback(t *testing.T) {
	withConfig(t, CasConfig{CASNotifyAutoSave: true, CASNotifyAutoSaveDir: "CAS"})
	var gotName string
	withAutoSave(t,
		func(_ context.Context, _ int64, _ string, file AutoSaveSourceFile) (string, error) {
			gotName = file.FileName
			return "id", nil
		},
		func(context.Context) ([]*domain.Account, error) {
			return []*domain.Account{{ID: 1, DriverType: "189_cloud", IsActive: true}}, nil
		},
	)
	content := cloud189.EncodeManifestV2(cloud189.CasManifestV2{
		Version: 2, FileName: "a.mkv", FileSize: 10, Hashes: cloud189.HashSet{FileMd5: "m"},
	})
	res := AutoSaveCASFiles(context.Background(), []AutoSaveSourceFile{{FileName: "  ", Content: content}})
	if !res[0].Saved {
		t.Fatalf("应转存成功，got %+v", res[0])
	}
	if gotName != "未命名.cas" {
		t.Errorf("空文件名应回落 未命名.cas，got %q", gotName)
	}
}

func TestNormalizeAutoSaveOnSave(t *testing.T) {
	store := map[string]string{}
	prevGet, prevSet := configStore, configSetter
	BindConfigStore(
		func(key string) (string, bool) { v, ok := store[key]; return v, ok },
		func(key, value string) { store[key] = value },
	)
	t.Cleanup(func() { configStore, configSetter = prevGet, prevSet })

	SaveConfig(CasConfig{
		Enabled:              true,
		CASNotifyAutoSaveDir: "/CAS/",
		CASNotifyAutoSaveDrives: map[string]AutoSaveDriveConfig{
			"189_cloud": {Enabled: true, SaveDir: "/天翼/"},
		},
	})
	got := getConfig()
	if got.CASNotifyAutoSaveDir != "CAS" {
		t.Errorf("目录应去掉首尾斜杠，got %q", got.CASNotifyAutoSaveDir)
	}
	dc, ok := got.CASNotifyAutoSaveDrives["cloud189"]
	if !ok {
		t.Fatalf("驱动名应被归一为 cloud189，got %+v", got.CASNotifyAutoSaveDrives)
	}
	if dc.SaveDir != "天翼" {
		t.Errorf("账号级目录应归一，got %q", dc.SaveDir)
	}
}
