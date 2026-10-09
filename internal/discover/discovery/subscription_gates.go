package discovery

import (
	"fmt"
	"log"
	"strings"

	"litepan/internal/discover/connector"
	"litepan/internal/mediaorganize/rules"
	"litepan/internal/settings"
)

// T05 · 订阅侧的画质/特效/体积闸门 + 搜索源归一化。
//
// 放在这里而不是 connector 包，是因为这三件事的判定口径是**订阅**的：
// 用户在订阅上写「至少 4K」「不要 HDR」「单个不超过 50G」，指的是「这条订阅要什么」，
// 而不是「某个连接器怎么解析标题」。connector 负责把源返回的原始字段解析成
// 结构化的 Item（画质、编码、体积、特效），闸门负责拿订阅配置去比对 Item。
//
// 两条不能含糊的语义：
//
//	1. 画质是**下限**不是精确匹配。「至少 4K」不能写成「等于 4K」——
//	   那会把 2160p 的 HDR 版直接拒掉，而用户要的是「别给我 1080p」。
//	2. 闸门一律**放行未知**。源没报体积、没标分辨率的候选不能因为「查不到信息」
//	   就被筛掉，否则任何一家源改了字段名，整条订阅就静默地什么都不转了。
//	   未知只记日志，让用户能看见「这批候选其实是靠默认放行的」。

// 支持的画质下限取值。空串 = 不限制。
var subscriptionResolutionLevels = []int{0, 480, 720, 1080, 2160}

// 支持的特效取值（含 sdr 这个反向语义）。
var subscriptionEffects = []string{"dolby vision", "dolby atmos", "hdr", "hdr10+", "sdr"}

// joinWarnings 把多条界面提示拼起来（已有的在前，追加的在后，空的忽略）。
func joinWarnings(existing, extra string) string {
	existing = strings.TrimSpace(existing)
	extra = strings.TrimSpace(extra)
	switch {
	case extra == "":
		return existing
	case existing == "":
		return extra
	}
	return existing + "；" + extra
}

// normalizeSubscriptionGates 校验并归一化订阅的搜索源与画质闸门。
//
// 返回的 warnings 是给界面看的提示（不是错误）：搜索源里写了未装配的 key 时提示，
// 因为连接器是按装配情况动态就绪的，写错 key 的后果是「这条订阅少搜一个源」而不是报错。
func normalizeSubscriptionGates(payload *SubscriptionUpsertPayload) (warnings string, err error) {
	if payload.SearchSources != nil {
		normalized, unknown := normalizeSearchSourceKeys(*payload.SearchSources)
		*payload.SearchSources = normalized
		if len(unknown) > 0 {
			warnings = fmt.Sprintf("以下搜索源当前未注册（可能是版本较旧或拼写有误），已原样保留，运行时若不可用会被跳过：%s",
				strings.Join(unknown, "、"))
		}
	}

	res := strings.ToLower(strings.TrimSpace(payload.Resolution))
	if res != "" {
		level, ok := parseResolutionLevel(res)
		if !ok {
			return "", fmt.Errorf("画质下限无效：%s（可选 480/720/1080/2160，或留空不限制）", res)
		}
		res = fmt.Sprintf("%d", level)
	}
	payload.Resolution = res

	effect := strings.ToLower(strings.TrimSpace(payload.Effect))
	if effect != "" && !isSupportedEffect(effect) {
		return "", fmt.Errorf("特效要求无效：%s（可选 dolby vision/dolby atmos/hdr/hdr10+/sdr，或留空不限制）", effect)
	}
	payload.Effect = effect

	minMB, maxMB := 0, 0
	if payload.MinFileSizeMB != nil {
		minMB = *payload.MinFileSizeMB
	}
	if payload.MaxFileSizeMB != nil {
		maxMB = *payload.MaxFileSizeMB
	}
	if minMB < 0 || maxMB < 0 {
		return "", fmt.Errorf("体积闸门不能为负数")
	}
	if minMB > 0 && maxMB > 0 && minMB > maxMB {
		return "", fmt.Errorf("体积下限 %dMB 大于上限 %dMB", minMB, maxMB)
	}
	return warnings, nil
}

// normalizeSearchSourceKeys 归一化搜索源列表：去重、去空白、转小写、保持用户顺序。
// 返回归一化后的逗号串与其中未装配的 key（顺序即用户书写顺序，便于提示）。
func normalizeSearchSourceKeys(raw string) (normalized string, unknown []string) {
	keys := connector.ParseKeys(raw)
	seen := map[string]bool{}
	kept := make([]string, 0, len(keys))
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		kept = append(kept, k)
		if _, ok := ConnectorFor(k); !ok {
			unknown = append(unknown, k)
		}
	}
	return strings.Join(kept, ","), unknown
}

// parseResolutionLevel 解析画质下限。"2160"/"4k"/"uhd" → 2160，"1080" → 1080。
func parseResolutionLevel(raw string) (int, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.TrimSuffix(strings.TrimPrefix(normalized, "p"), "p")
	switch normalized {
	case "sd", "480p":
		return 480, true
	case "hd", "720p", "720i":
		return 720, true
	case "fhd", "1080p", "1080i":
		return 1080, true
	case "4k", "uhd", "2160p", "2160":
		return 2160, true
	}
	var level int
	if _, e := fmt.Sscanf(normalized, "%d", &level); e == nil {
		for _, v := range subscriptionResolutionLevels {
			if v == level {
				return level, true
			}
		}
	}
	return 0, false
}

func isSupportedEffect(raw string) bool {
	for _, v := range subscriptionEffects {
		if v == raw {
			return true
		}
	}
	return false
}

// subscriptionGate 一条订阅上的闸门配置（已解析成可直接比对的数值）。
type subscriptionGate struct {
	// ResolutionLevel 画质下限（像素高）。0 = 不限制。
	ResolutionLevel int
	// Effect 必须具备的特效，"sdr" 表示「必须不含 HDR/DV」。空串 = 不限制。
	Effect   string
	MinBytes int64
	MaxBytes int64
}

// Enabled 是否存在任何闸门（用于日志里区分「没配」和「配了但全放行」）。
func (g subscriptionGate) Enabled() bool {
	return g.ResolutionLevel > 0 || g.Effect != "" || g.MinBytes > 0 || g.MaxBytes > 0
}

// gateForSubscription 从订阅行解析闸门配置。解析不了的输入按「不限制」处理并记日志 ——
// 闸门写坏了不该让整条订阅停摆，最坏结果是不过滤。
func gateForSubscription(sub *DiscoverySubscription) subscriptionGate {
	g := subscriptionGate{}
	if level, ok := parseResolutionLevel(sub.Resolution); ok {
		g.ResolutionLevel = level
	} else if strings.TrimSpace(sub.Resolution) != "" {
		log.Printf("[discovery] subscription_gate_invalid sub_id=%d field=resolution value=%q 忽略该画质下限",
			sub.ID, sub.Resolution)
	}
	effect := strings.ToLower(strings.TrimSpace(sub.Effect))
	if effect == "" {
		// 不限制
	} else if isSupportedEffect(effect) {
		g.Effect = effect
	} else {
		log.Printf("[discovery] subscription_gate_invalid sub_id=%d field=effect value=%q 忽略该特效要求",
			sub.ID, sub.Effect)
	}
	if sub.MinFileSizeMB > 0 {
		g.MinBytes = int64(sub.MinFileSizeMB) * 1024 * 1024
	}
	if sub.MaxFileSizeMB > 0 {
		g.MaxBytes = int64(sub.MaxFileSizeMB) * 1024 * 1024
	}
	if g.MinBytes > 0 && g.MaxBytes > 0 && g.MinBytes > g.MaxBytes {
		log.Printf("[discovery] subscription_gate_invalid sub_id=%d min=%dMB max=%dMB 体积区间非法，忽略体积闸门",
			sub.ID, sub.MinFileSizeMB, sub.MaxFileSizeMB)
		g.MinBytes, g.MaxBytes = 0, 0
	}
	return g
}

// gateVerdict 单个候选的闸门判定结果。
type gateVerdict struct {
	Pass bool
	// Reason 过滤原因，写进订阅项的跳过原因里（人话）。
	Reason string
	// Unknown 标记「闸门因为拿不到信息而放行」，只进日志不进跳过原因 ——
	// 用户看到「因为体积放行」会以为闸门没生效，得让他在日志里才看得见。
	Unknown bool
}

// passGate 判定单个候选是否通过闸门。未知一律放行并标 Unknown。
func passGate(g subscriptionGate, it connector.Item) gateVerdict {
	v := gateVerdict{Pass: true}

	if g.ResolutionLevel > 0 && it.Resolution > 0 && it.Resolution < g.ResolutionLevel {
		v.Pass = false
		v.Reason = fmt.Sprintf("画质 %dp 低于要求的 %dp", it.Resolution, g.ResolutionLevel)
		return v
	}
	if g.ResolutionLevel > 0 && it.Resolution <= 0 {
		v.Unknown = true
	}

	if g.Effect != "" {
		has := effectPresent(g.Effect, it)
		switch {
		case g.Effect == "sdr" && (it.HasHDR || hasAtmosDV(it)):
			v.Pass = false
			v.Reason = "要求 SDR，但该候选带 HDR/DV"
			return v
		case g.Effect != "sdr" && !has:
			// 到了这里说明这一批候选里**有**其它候选带特效信息（inertEffectIfUnreportable
			// 已经确认过了），所以本条缺标记就是真的没有，而不是源没报。
			v.Pass = false
			v.Reason = fmt.Sprintf("要求 %s，但该候选没有该特效标记", g.Effect)
			return v
		}
	}

	if (g.MinBytes > 0 || g.MaxBytes > 0) && it.SizeBytes > 0 {
		if g.MinBytes > 0 && it.SizeBytes < g.MinBytes {
			v.Pass = false
			v.Reason = fmt.Sprintf("体积 %s 小于下限 %d MB", formatBytes(it.SizeBytes), g.MinBytes/1024/1024)
			return v
		}
		if g.MaxBytes > 0 && it.SizeBytes > g.MaxBytes {
			v.Pass = false
			v.Reason = fmt.Sprintf("体积 %s 超过上限 %d MB", formatBytes(it.SizeBytes), g.MaxBytes/1024/1024)
			return v
		}
	} else if g.MinBytes > 0 || g.MaxBytes > 0 {
		v.Unknown = true
	}

	return v
}

// effectPresent 判断候选是否具备要求的特效。
// 判不出来（没有可依据的字段）时返回 false，调用方负责把它当「未知」而不是「没有」。
func effectPresent(effect string, it connector.Item) bool {
	switch effect {
	case "dolby vision", "dv":
		return hasDV(it)
	case "dolby atmos", "atmos":
		return it.HasAtmos
	case "hdr":
		// DV 也是 HDR10 编码，要求 HDR 时不该把 DV 版挡掉。
		return it.HasHDR || hasDV(it)
	case "hdr10+":
		return it.HasHDR
	case "sdr":
		return !it.HasHDR && !hasAtmosDV(it)
	}
	return false
}

// hasDV 是否有杜比视界。connector 层已把 HDR 归一到 HasHDR，
// DV 需要从标题/标签里再看一眼，因为它是 HDR 的子集。
//
// T06 起走公共词表 + 词边界匹配。改动前这里是 strings.Contains(hay, "dv")，
// 而 hay 里的 "DVDRip" 已被 normalizeTagText 归一成 "dvdrip" ——
// 于是一批 DVD 片源全被误判成杜比视界，而 dv / sdr 两条特效闸门都建在它上面。
// 词边界匹配下 "dvdrip" 是完整一词，与 "dv" 不相等。
func hasDV(it connector.Item) bool {
	hay := it.Title + " " + it.Remark + " " + strings.Join(it.SpecTags, " ")
	return rules.HasDVText(hay)
}

// normalizeTagText 把发布名里的分隔符统一成空格再转小写。
// 资源名里 Dolby Vision 写成 "Dolby.Vision"/"Dolby-Vision"/"Dolby_Vision" 三种都有，
// 不归一就只有三分之一的 DV 片源能被 sdr 闸门认出来。
func normalizeTagText(raw string) string {
	replacer := strings.NewReplacer(".", " ", "_", " ", "-", " ", "+", " ")
	return strings.ToLower(replacer.Replace(raw))
}

func hasAtmosDV(it connector.Item) bool {
	return it.HasAtmos || hasDV(it)
}

// filterByGate 按闸门过滤候选，返回通过的和被过滤掉的（带原因，用于记日志与订阅项状态）。
// gatedOutItem 被闸门筛掉的候选 + 筛它的理由。
//
// 为什么要把理由带出来而不只是打日志：用户在订阅运行详情里问「为什么这部没转」，
// 「被画质下限筛掉」和「被体积上限筛掉」是两个完全不同的答案，
// 只剩一个「筛掉 N 条」的计数时没法回答。
type gatedOutItem struct {
	Item   connector.Item
	Reason string
}

func filterByGate(g subscriptionGate, items []connector.Item) (kept []connector.Item, dropped []gatedOutItem) {
	g = g.inertEffectIfUnreportable(items)
	for _, it := range items {
		v := passGate(g, it)
		if v.Pass {
			kept = append(kept, it)
			if v.Unknown {
				log.Printf("[discovery] subscription_gate_passed_unknown sub_item=%s title=%s 来源信息不全，闸门放行",
					it.SourceKey, it.Title)
			}
			continue
		}
		dropped = append(dropped, gatedOutItem{Item: it, Reason: v.Reason})
		log.Printf("[discovery] subscription_gate_filtered source=%s title=%s reason=%s",
			it.SourceKey, it.Title, v.Reason)
	}
	return kept, dropped
}

// inertEffectIfUnreportable 整批候选都读不出特效信息时，本轮关掉特效闸门。
//
// 为什么需要这一步：sdr 是「排掉带 HDR/DV 的」，有正向证据就能筛；
// 而 hdr/dv/atmos 是「只要带这个的」，缺正向证据就放行。所以 sdr 天然比
// hdr 严格得多 —— 一个源如果压根不在发布名里带特效标签（很多磁力源就是这样），
// 「要求 HDR」会把这一批候选**全部**筛掉，订阅表现成「什么都转不到」，
// 而日志里每条都写着「要求 HDR 但该候选不是 HDR」，看不出其实是源没信息。
//
// 这里不改成逐条判断（那等于取消正向特效闸门），而是只在「整批一条特效信息都没有」
// 时才判定为源不支持，按未配置处理 —— 有信息的源该筛还是筛。
func (g subscriptionGate) inertEffectIfUnreportable(items []connector.Item) subscriptionGate {
	if g.Effect == "" || len(items) == 0 {
		return g
	}
	for _, it := range items {
		if it.HasHDR || it.HasAtmos || it.HasDTS || hasDV(it) {
			return g
		}
	}
	log.Printf("[discovery] subscription_gate_inert field=effect effect=%q 本轮 %d 条候选都读不出特效信息，按未配置处理",
		g.Effect, len(items))
	g.Effect = ""
	return g
}

// subscriptionSearchSources 取一条订阅实际要用的连接器 key 列表。
// 订阅行上没配（或历史数据是空串）时回落到全局默认 —— 这是存量订阅拿到
// 'tgto123' 的那一条路径，也是「默认只有 tgto123」这条验收的实现。
func subscriptionSearchSources(sub *DiscoverySubscription) []string {
	keys := connector.ParseKeys(sub.SearchSources)
	if len(keys) == 0 {
		keys = connector.ParseKeys(SubscriptionSearchSourcesSetting())
	}
	return keys
}

// SubscriptionSearchSourcesSetting 读全局默认搜索源配置。
// 与 registry 里的默认值常量同源，避免两处各写一个字符串。
//
// ⚠️ 走 currentSettings()（settings.Service）而不是本包的 SettingString：
// mo_* 系列的键由 internal/settings 的注册表服务持有（管理界面写的就是那边），
// 而 SettingString 读的是 discovery 自己的 discovery_settings 表，
// 它的 DefaultSettings() 白名单里没有这个键 ⇒ 用错通道会永远读到默认值，
// 用户在界面上改了搜索源也不生效。T06 补这条时才发现，已一并修正。
func SubscriptionSearchSourcesSetting() string {
	return guardrailString(settings.KeyMOSubscriptionSearchSources, settings.KeyMOSubscriptionSearchSourcesDefault)
}

// formatBytes 人类可读的体积。只用于日志与跳过原因，不参与判定。
// formatBytes 字节数 → 人类可读。
// 0 表示「源没报体积」（connector.Item.SizeBytes 的零值语义），
// 这里返回空串而不是 "0 B" —— 界面上显示 "0 B" 会让人以为这份资源真的是空的，
// 而把 0 写成候选体积又会被体积闸门当成「小于任何下限」而误筛。
func formatBytes(n int64) string {
	const unit = 1024
	if n <= 0 {
		return ""
	}
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
