package domain

import (
	"sort"
	"strings"
)

// 通知场景（scene）是对外暴露的订阅维度：用户可以对每个渠道单独勾选
// 「我关心哪些场景」，而不是只能按通知分类（category）这种内部标识过滤。
//
// ⚠️ 场景清单是**按 参考实现 文档推断**的，不是逆向出来的完整名单。
// 参考实现 的 notify_scenes 模块在 so_modules/ 里的 .so 中，具体 scene 名单
// 没完全逆出来；文档自称「6 大场景」却列举了 7 个口径（见
// prompts/12-notify-scenes.md）。这里按文档列举的 7 个落地，不自行增删。
//
// 另有一条重要口径：场景开关**只作用于已归类的通知分类**。
// 未归类的分类（见 UnmappedCategories）不受场景开关影响，永远送达 ——
// 这样「关闭所有场景」不会连带把账号认证失效这类告警也静音掉，
// 同时保证存量渠道（配置里没有 scenes 键）行为逐条不变。
type NotificationScene string

const (
	// SceneUpload 上传完成。
	SceneUpload NotificationScene = "upload"
	// SceneStrm STRM 生成 / 失效。
	SceneStrm NotificationScene = "strm"
	// SceneOrganize 整理（识别 / 改名 / 移动）。
	SceneOrganize NotificationScene = "organize"
	// SceneSubscription 订阅（命中 / 转存 / 失败）。
	SceneSubscription NotificationScene = "subscription"
	// SceneDownload 下载完成。
	SceneDownload NotificationScene = "download"
	// SceneSync 同步。
	SceneSync NotificationScene = "sync"
	// SceneBackup 备份。
	SceneBackup NotificationScene = "backup"
)

// NotificationSceneInfo 单个场景的下发给前端的元数据。
type NotificationSceneInfo struct {
	Scene   NotificationScene `json:"scene"`
	Label   string            `json:"label"`
	Trigger string            `json:"trigger"`
	// Categories 归入该场景的通知分类；空数组表示该场景在 litepan 里
	// 还没有任何通知产出（见 ChangeNote 字段）。
	Categories []string `json:"categories"`
}

// notificationSceneCatalog 是场景清单的唯一真相来源。
//
// 归类规则：**只按 参考实现 文档自己写的「触发时机」列去匹配**，不为了凑覆盖率
// 硬塞。匹配不上的分类宁可留在 UnmappedCategories 里永远送达，
// 也不要挂到一个名不副实的场景下让用户以为「关掉同步就不再收 STRM 告警」。
var notificationSceneCatalog = []NotificationSceneInfo{
	{
		Scene:      SceneUpload,
		Label:      "上传",
		Trigger:    "上传完成",
		Categories: []string{"upload"},
	},
	{
		Scene:      SceneStrm,
		Label:      "STRM",
		Trigger:    "STRM 生成 / 失效",
		Categories: []string{"strm_scan_warn", "strm_scrape_warn"},
	},
	{
		Scene:      SceneOrganize,
		Label:      "整理",
		Trigger:    "整理（识别 / 改名 / 移动）",
		Categories: nil,
	},
	{
		Scene:      SceneSubscription,
		Label:      "订阅",
		Trigger:    "订阅（命中 / 转存 / 失败）",
		Categories: []string{"cas"},
	},
	{
		Scene:      SceneDownload,
		Label:      "下载",
		Trigger:    "下载完成",
		Categories: nil,
	},
	{
		Scene:      SceneSync,
		Label:      "同步",
		Trigger:    "同步",
		Categories: nil,
	},
	{
		Scene:      SceneBackup,
		Label:      "备份",
		Trigger:    "备份",
		Categories: nil,
	},
}

// unmappedCategories 明确列出「暂时不属于任何场景」的分类，写进代码而不是
// 靠「遍历场景找没找到」隐式推导 —— 前端要显示这份清单让用户知道
// 关掉全部场景之后这些仍然会收到，否则用户会以为告警被静音了。
var unmappedCategories = map[string]bool{
	// 存储账号认证失效 / 恢复：账号维度的告警，与任何一个业务场景都无关。
	NotificationCategoryAuth: true,
	// 事件总线兜底分类（onCreated 里 category 为空时使用）。
	NotificationCategorySystem: true,
	// 自动化动作产出的通用通知，具体属于 strm / organize 还是 emby 取决于
	// 规则配置，按 category 无法判定，留在场景外最安全。
	NotificationCategoryAutomation: true,
	// 媒体服务器（Emby）侧事件。
	NotificationCategoryEmbyIngest:   true,
	NotificationCategoryEmbyPlayback: true,
	NotificationCategoryEmbyDeleted:  true,
	// 基础设施告警。
	NotificationCategoryCacheScopeWarn: true,
	NotificationCategoryFuseMountWarn:  true,
	NotificationCategoryQuarkTVWarn:    true,
}

// 未归类分类的中文名，供前端「不受场景开关影响」清单展示。
var unmappedCategoryLabels = map[string]string{
	NotificationCategoryAuth:           "账号认证失效 / 恢复",
	NotificationCategorySystem:         "系统兜底通知",
	NotificationCategoryAutomation:     "自动化动作通知",
	NotificationCategoryEmbyIngest:     "Emby 入库",
	NotificationCategoryEmbyPlayback:   "Emby 播放",
	NotificationCategoryEmbyDeleted:    "Emby 删除",
	NotificationCategoryCacheScopeWarn: "缓存保持范围告警",
	NotificationCategoryFuseMountWarn:  "网盘挂载告警",
	NotificationCategoryQuarkTVWarn:    "夸克 TV 凭证告警",
}

// NotificationScenes 返回按展示顺序排列的场景清单（前端渲染勾选列表）。
func NotificationScenes() []NotificationSceneInfo {
	out := make([]NotificationSceneInfo, 0, len(notificationSceneCatalog))
	for _, s := range notificationSceneCatalog {
		cp := s
		cp.Categories = append([]string(nil), s.Categories...)
		out = append(out, cp)
	}
	return out
}

// UnmappedCategories 返回不受场景开关影响的分类清单。
//
// 返回的顺序跟着 notificationSceneCatalog 里出现过的顺序走，
// 再补上纯未知分类（历史脏数据），保证前端展示稳定不跳序。
func UnmappedCategories() []NotificationSceneInfo {
	out := make([]NotificationSceneInfo, 0, len(unmappedCategories))
	seen := map[string]bool{}
	for cat := range unmappedCategories {
		seen[cat] = true
	}
	// catalog 里已归类的分类不能出现在未归类清单中（防止以后有人手改
	// catalog 时把同一个分类塞进两个场景，两边都会命中、语义打架）。
	for _, s := range notificationSceneCatalog {
		for _, cat := range s.Categories {
			delete(seen, cat)
		}
	}
	names := make([]string, 0, len(seen))
	for cat := range seen {
		names = append(names, cat)
	}
	sort.Strings(names)
	for _, cat := range names {
		out = append(out, NotificationSceneInfo{
			Scene:   "",
			Label:   unmappedCategoryLabels[cat],
			Trigger: cat,
		})
	}
	return out
}

// SceneLabel 返回场景的中文名；未知场景返回 ("", false)。
func SceneLabel(scene NotificationScene) (string, bool) {
	for _, s := range notificationSceneCatalog {
		if s.Scene == scene {
			return s.Label, true
		}
	}
	return "", false
}

// SceneOf 返回某通知分类所属的场景；未归类时返回 ("", false)。
func SceneOf(category string) (NotificationScene, bool) {
	for _, s := range notificationSceneCatalog {
		for _, cat := range s.Categories {
			if cat == category {
				return s.Scene, true
			}
		}
	}
	return "", false
}

// SceneOf2 是 SceneOf 的「总会返回一个值」版本：未归类时返回空串。
//
// 供补发队列存 event_scene 用 —— 未归类的通知同样可能投递失败需要补发，
// 那种情况下场景字段只能留空，**不能因为「归不出类」就把这条补发记录丢掉**。
func SceneOf2(category string) NotificationScene {
	scene, _ := SceneOf(category)
	return scene
}

// ParseSceneSubscriptions 解析渠道配置里的 scenes 键（英文逗号分隔的场景 ID）。
//
// 空串 / 全空白 = **全订阅**，这是存量渠道的默认状态（它们的配置里根本没有
// scenes 这个键）。调用方必须把「解析不出任何场景」当成放行而不是拦下，
// 否则升级后老渠道会一条通知都收不到 —— 这正是硬约束「存量配置必须能加载」
// 要防的事故。
func ParseSceneSubscriptions(raw string) ([]NotificationScene, bool) {
	out := make([]NotificationScene, 0, len(notificationSceneCatalog))
	seen := map[NotificationScene]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, s := range notificationSceneCatalog {
			if string(s.Scene) == part && !seen[s.Scene] {
				seen[s.Scene] = true
				out = append(out, s.Scene)
				break
			}
		}
	}
	return out, len(out) > 0
}

// EncodeSceneSubscriptions 把订阅集合序列化回配置值；空集合返回空串（= 全订阅）。
func EncodeSceneSubscriptions(scenes []NotificationScene) string {
	valid := map[NotificationScene]bool{}
	for _, s := range notificationSceneCatalog {
		valid[s.Scene] = true
	}
	seen := map[NotificationScene]bool{}
	parts := make([]string, 0, len(scenes))
	for _, sc := range scenes {
		if !valid[sc] || seen[sc] {
			continue
		}
		seen[sc] = true
		parts = append(parts, string(sc))
	}
	return strings.Join(parts, ",")
}
