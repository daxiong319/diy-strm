package cas

import (
	"context"
	"strings"

	"litepan/internal/cas/cloud189"
	"litepan/internal/discover/dutil"
	"litepan/internal/domain"
)

// ---------------------------------------------------------------------------
// CAS 自动转存：通知渠道收到用户发来的 .cas 文件后，
// 判定清单归属网盘 → 保存到用户在前端配置的保存目录。
// ---------------------------------------------------------------------------

// AutoSaveDriveConfig 单个网盘的自动转存配置。
// AccountID 为 0 时按网盘类型自动挑一个启用账号；SaveDir 为空时回落到全局目录。
type AutoSaveDriveConfig struct {
	Enabled   bool   `json:"enabled"`
	AccountID int64  `json:"account_id"`
	SaveDir   string `json:"save_dir"`
}

// AutoSaveSourceFile 待转存的 .cas 文件。
type AutoSaveSourceFile struct {
	FileName string `json:"file_name"`
	Content  string `json:"content"`
}

// AutoSaveResult 单个 .cas 的转存结果。
type AutoSaveResult struct {
	Saved         bool   `json:"saved"`
	Skipped       bool   `json:"skipped"`
	Reason        string `json:"reason,omitempty"`
	DriveType     string `json:"drive_type,omitempty"`
	AccountID     int64  `json:"account_id,omitempty"`
	SavedFileName string `json:"saved_file_name,omitempty"`
}

// AutoSaveSaver 由装配层注入：把 .cas 文本落到目标网盘目录，返回文件 ID。
type AutoSaveSaver func(ctx context.Context, accountID int64, saveDir string, file AutoSaveSourceFile) (string, error)

// AccountLister 由装配层注入：列出全部网盘账号（用于按网盘类型匹配账号）。
type AccountLister func(ctx context.Context) ([]*domain.Account, error)

// AutoSaveNotifier 由装配层注入：写入通知中心消息。
type AutoSaveNotifier func(ctx context.Context, level, category, title, message string, accountID, refID int64)

var (
	autoSaveSaver    AutoSaveSaver
	autoSaveAccounts AccountLister
	autoSaveNotifier AutoSaveNotifier
)

// BindAutoSaveSaver 注入 .cas 落盘器。
func BindAutoSaveSaver(s AutoSaveSaver) {
	if s != nil {
		autoSaveSaver = s
	}
}

// BindAutoSaveAccounts 注入账号列表读取器。
func BindAutoSaveAccounts(l AccountLister) {
	if l != nil {
		autoSaveAccounts = l
	}
}

// BindAutoSaveNotifier 注入通知写入器。
func BindAutoSaveNotifier(n AutoSaveNotifier) {
	if n != nil {
		autoSaveNotifier = n
	}
}

// NormalizeDriveType 把各处五花八门的网盘标识归一到统一类型。
// 兼容 cas 内部别名（cloud189/189cloud、cloud139/pan139/139cloud）与
// LitePan 驱动名（189_cloud/139_cloud/quark/115_open/guangya/123_open）。
func NormalizeDriveType(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	v = strings.ReplaceAll(v, "-", "_")
	switch v {
	case "cloud189", "189cloud", "189_cloud", "189", "tianyi", "天翼", "天翼云盘":
		return "cloud189"
	case "cloud139", "pan139", "139cloud", "139_cloud", "139", "yidong", "移动", "移动云盘", "中国移动云盘":
		return "cloud139"
	case "quark", "夸克", "夸克网盘":
		return "quark"
	case "115", "115_open", "115open":
		return "115_open"
	case "123", "123_open", "123open", "123pan":
		return "123_open"
	case "guangya", "guangyapan", "光鸭", "光鸭云盘":
		return "guangya"
	case "baidu", "baidu_open", "baiduopen", "百度", "百度网盘":
		return "baidu_open"
	case "onedrive":
		return "onedrive"
	case "webdav":
		return "webdav"
	case "openlist", "alist":
		return "openlist"
	case "localfs", "local":
		return "localfs"
	}
	return v
}

// detectDriveTypeFromHashes 与老 diy-strm 一致：按清单哈希推断归属网盘。
// Sha256 → 移动(139)；FileMd5+SliceMd5 → 天翼(189)；PreHash → 夸克。
func detectDriveTypeFromHashes(hs cloud189.HashSet) string {
	switch {
	case hs.PreHash != "":
		return "quark"
	case hs.Sha256 != "":
		return "cloud139"
	case hs.FileMd5 != "" || hs.SliceMd5 != "":
		return "cloud189"
	}
	return ""
}

// ResolveDriveType 优先采用清单自报的网盘，缺失时按哈希推断。
func ResolveDriveType(m cloud189.CasManifestV2) string {
	if d := NormalizeDriveType(m.SourceDrive); d != "" {
		return d
	}
	return detectDriveTypeFromHashes(m.Hashes)
}

// pickAutoSaveAccount 按网盘类型挑账号：优先配置指定且启用的，否则取第一个启用的匹配账号。
func pickAutoSaveAccount(accounts []*domain.Account, driveType string, wantID int64) *domain.Account {
	var first *domain.Account
	for _, a := range accounts {
		if a == nil || !a.IsActive {
			continue
		}
		if NormalizeDriveType(a.DriverType) != driveType {
			continue
		}
		if wantID != 0 && a.ID == wantID {
			return a
		}
		if first == nil {
			first = a
		}
	}
	return first
}

// resolveSaveDir 返回该网盘的保存目录：账号级配置优先，其次全局默认。
func resolveSaveDir(cfg CasConfig, driveType string) string {
	if dc, ok := cfg.CASNotifyAutoSaveDrives[driveType]; ok {
		if v := strings.Trim(strings.TrimSpace(dc.SaveDir), "/"); v != "" {
			return v
		}
	}
	return strings.Trim(strings.TrimSpace(cfg.CASNotifyAutoSaveDir), "/")
}

// AutoSaveCASFiles 处理一批收到的 .cas 文件（通知渠道回调入口）。
// 只对开启的网盘执行转存，未匹配到账号/目录/落盘器时记录原因并跳过，不影响其它文件。
func AutoSaveCASFiles(ctx context.Context, files []AutoSaveSourceFile) []AutoSaveResult {
	results := make([]AutoSaveResult, 0, len(files))
	if len(files) == 0 {
		return results
	}
	cfg := getConfig()
	for _, f := range files {
		res := AutoSaveResult{}
		name := strings.TrimSpace(f.FileName)
		if name == "" {
			name = "未命名.cas"
		}
		if !cfg.CASNotifyAutoSave {
			res.Skipped, res.Reason = true, "CAS 自动转存未开启"
			results = append(results, res)
			continue
		}
		manifest, err := cloud189.ParseManifestV2(f.Content)
		if err != nil {
			res.Skipped, res.Reason = true, "解析 .cas 失败："+err.Error()
			results = append(results, res)
			continue
		}
		driveType := ResolveDriveType(manifest)
		if driveType == "" {
			res.Skipped, res.Reason = true, "无法判定 .cas 归属网盘（清单无 sourceDrive 且缺少可识别哈希）"
			results = append(results, res)
			continue
		}
		res.DriveType = driveType

		if dc, ok := cfg.CASNotifyAutoSaveDrives[driveType]; ok && !dc.Enabled {
			res.Skipped, res.Reason = true, "该网盘已关闭自动转存"
			results = append(results, res)
			continue
		}
		if autoSaveSaver == nil {
			res.Skipped, res.Reason = true, "转存器未就绪"
			results = append(results, res)
			continue
		}
		if autoSaveAccounts == nil {
			res.Skipped, res.Reason = true, "账号服务未就绪"
			results = append(results, res)
			continue
		}

		accounts, err := autoSaveAccounts(ctx)
		if err != nil {
			res.Skipped, res.Reason = true, "读取账号失败："+err.Error()
			results = append(results, res)
			continue
		}
		var wantID int64
		if dc, ok := cfg.CASNotifyAutoSaveDrives[driveType]; ok {
			wantID = dc.AccountID
		}
		acc := pickAutoSaveAccount(accounts, driveType, wantID)
		if acc == nil {
			res.Skipped, res.Reason = true, "未找到可用的"+driveType+"账号"
			results = append(results, res)
			continue
		}
		res.AccountID = acc.ID

		saveDir := resolveSaveDir(cfg, driveType)
		outName := name
		if !strings.HasSuffix(strings.ToLower(outName), ".cas") {
			outName += ".cas"
		}
		fileID, err := autoSaveSaver(ctx, acc.ID, saveDir, AutoSaveSourceFile{FileName: outName, Content: f.Content})
		if err != nil {
			res.Skipped, res.Reason = true, "转存失败："+err.Error()
			results = append(results, res)
			dutil.AppLogger.Warnf("CAS 自动转存：%s 转存到 %s 失败：%v", outName, driveType, err)
			continue
		}
		res.Saved, res.SavedFileName = true, outName
		results = append(results, res)
		dutil.AppLogger.Infof("CAS 自动转存：%s → %s（账号 %d，目录 %s，文件 %s）", outName, driveType, acc.ID, saveDir, fileID)
		notifyAutoSave(ctx, acc.ID, driveType, outName, saveDir)
	}
	return results
}

// notifyAutoSave 转存成功后向通知中心写入一条消息（未注入通知器时静默跳过）。
func notifyAutoSave(ctx context.Context, accountID int64, driveType, fileName, saveDir string) {
	if autoSaveNotifier == nil {
		return
	}
	dir := saveDir
	if dir == "" {
		dir = "根目录"
	}
	autoSaveNotifier(ctx, "success", "cas", "CAS 清单已自动转存",
		"已收到 "+fileName+"，识别为「"+driveType+"」，已保存到 "+dir+"/"+fileName+"。", accountID, 0)
}

// NotifyAutoSaveFailure 供接收侧在下载/判定/落盘失败时提示用户（未注入通知器时静默跳过）。
func NotifyAutoSaveFailure(ctx context.Context, level, fileName, message string) {
	if autoSaveNotifier == nil {
		return
	}
	if level == "" {
		level = "warn"
	}
	autoSaveNotifier(ctx, level, "cas", "CAS 清单自动转存未完成",
		"收到 "+fileName+"，"+message+"。", 0, 0)
}
