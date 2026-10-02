package cas

import (
	"context"
	"fmt"
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
// SaveDir 回填实际使用的目标目录，供调用方日志/通知复用，避免各自重算而出现分歧。
type AutoSaveResult struct {
	Saved         bool   `json:"saved"`
	Skipped       bool   `json:"skipped"`
	Reason        string `json:"reason,omitempty"`
	DriveType     string `json:"drive_type,omitempty"`
	AccountID     int64  `json:"account_id,omitempty"`
	SavedFileName string `json:"saved_file_name,omitempty"`
	SaveDir       string `json:"save_dir,omitempty"`
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

// DriveDisplayName 网盘编码转中文展示名，供通知文案与日志复用。
//
// 用户需要一眼看出「转存到了哪个网盘」，光给 cloud189 这样的内部编码
// 不直观。注意 cloud139 是中国移动（和彩云），cloud189 才是中国电信天翼，
// 两者不能都叫「天翼云盘」。
// 未知编码原样返回，既不丢信息也不伪造名称。
func DriveDisplayName(driveType string) string {
	switch NormalizeDriveType(driveType) {
	case "cloud189":
		return "天翼云盘"
	case "cloud139":
		return "移动云盘"
	case "quark":
		return "夸克网盘"
	case "115_open":
		return "115 网盘"
	case "123_open":
		return "123 网盘"
	case "guangya":
		return "光鸭云盘"
	case "baidu_open":
		return "百度网盘"
	case "onedrive":
		return "OneDrive"
	case "webdav":
		return "WebDAV"
	case "openlist":
		return "OpenList"
	case "localfs":
		return "本地目录"
	}
	return driveType
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
		res.SaveDir = saveDir
		outName := name
		if !strings.HasSuffix(strings.ToLower(outName), ".cas") {
			outName += ".cas"
		}
		fileID, err := autoSaveSaver(ctx, acc.ID, saveDir, AutoSaveSourceFile{FileName: outName, Content: f.Content})
		if err != nil {
			// Reason 只放底层错误原文：调用方（接收侧通知）会自行补「未转存：」前缀，
			// 这里再写一次「转存失败：」会拼出「未转存：转存失败：…」的重复文案。
			res.Skipped, res.Reason = true, err.Error()
			results = append(results, res)
			dutil.AppLogger.Warnf("CAS 自动转存：%s → %s %s 失败：%v",
				outName, DriveDisplayName(driveType), "/"+saveDir, err)
			continue
		}
		res.Saved, res.SavedFileName = true, outName
		results = append(results, res)
		dutil.AppLogger.Infof("CAS 自动转存：%s → %s %s（账号 %d，文件 %s）",
			outName, DriveDisplayName(driveType), "/"+saveDir, acc.ID, fileID)
		notifyAutoSave(ctx, acc.ID, driveType, outName, saveDir)
	}
	return results
}

// DisplaySaveDir 把保存目录归一成可用于展示的形式：空目录显示为「根目录」，
// 其余补上前导斜杠。与 notifyAutoSave 的文案保持一致。
func DisplaySaveDir(saveDir string) string {
	trimmed := strings.Trim(strings.TrimSpace(saveDir), "/")
	if trimmed == "" {
		return "根目录"
	}
	return "/" + trimmed
}

// notifyAutoSave 转存成功后向通知中心写入一条消息（未注入通知器时静默跳过）。
//
// 文案必须让用户一眼看清「转存了什么文件 → 到哪个网盘 → 哪个目录 → 成功还是失败」，
// 否则只写「自动转存成功 / 账号 2」等于没说。
func notifyAutoSave(ctx context.Context, accountID int64, driveType, fileName, saveDir string) {
	if autoSaveNotifier == nil {
		return
	}
	drive := DriveDisplayName(driveType)
	where := DisplaySaveDir(saveDir)
	autoSaveNotifier(ctx, "success", "cas", "CAS 自动转存成功",
		fmt.Sprintf("已转存「%s」→ %s %s（账号 %d），转存成功。", fileName, drive, where, accountID),
		accountID, 0)
}

// NotifyAutoSaveFailure 供接收侧在下载/判定/落盘失败时提示用户（未注入通知器时静默跳过）。
// driveType 为空时表示尚未判定出网盘，文案会略去网盘与目录，只说明失败原因。
func NotifyAutoSaveFailure(ctx context.Context, level, fileName, driveType, saveDir, message string) {
	if autoSaveNotifier == nil {
		return
	}
	if level == "" {
		level = "warn"
	}
	subject := "收到「" + fileName + "」"
	if d := strings.TrimSpace(driveType); d != "" {
		subject += "，识别为 " + DriveDisplayName(d)
		if where := strings.Trim(strings.TrimSpace(saveDir), "/"); where != "" {
			subject += " /" + where
		}
	}
	autoSaveNotifier(ctx, level, "cas", "CAS 自动转存未完成", subject+"，"+message+"。", 0, 0)
}
