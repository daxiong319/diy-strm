// 订阅转存前的身份校验闸门。
//
// 参考实现 的不变式是「**未通过身份校验，一律不转存**」。litepan 订阅追新的链路是
// 「搜索 → 候选 → 直接转存」，中间没有任何拦截，候选若是另一部剧/另一季/另一集，
// 或者是一个混拼了多部作品的分享，错误内容就会进网盘并可能覆盖已有文件 ——
// 这是 litepan 唯一一条会损坏用户已有资产的路径。本文件把闸门插在
// planAndTransferRuleCandidates 里、发起转存之前。
//
// 关于「拿不到文件清单怎么办」：参考实现 还能展开分享的文件清单、并在不合时用隔离区
// （quarantine）兜底；litepan 做不到 —— tgto123 反代只有
// /api/login、/api/media/resources/search、/api/media/resources/transfer、
// /api/re0/authorize 四个端点，转存调用把「解锁 + 转存」压成一次，落盘目录由
// 服务端决定，转存完也没法枚举核对（见 internal/discover/discovery/tgto123_proxy.go）。
// 所以本期不做隔离区（总纲「本期不动」第 6 条），改用等效简化：
//
//	**拿不到任何可验证的证据，就不转存。**
//
// 磁力/ed2k 候选只有链接、没有文件清单，正好落在这个档位上。
//
// 证据分三档（见 identity.Evidence）：
//   - none     ：磁力/ed2k 等只有链接的来源 → IDENTITY_MANIFEST_UNAVAILABLE → 不转存
//   - metadata ：网盘来源（123/115/观影/139 等），有资源标题、备注、正则可解析的季集证据
//   - manifest ：逐文件清单。本期还没有来源产出，预留给未来的分享清单接口
//
// 关于日志：每条判定都打一行，reason_code 与订阅 ID / TMDB ID / 候选标识 / 解析出的
// 标题·年份·季集·纯度 一起输出，可以直接 grep reason_code 反查上下文。
package discovery

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"litepan/internal/discover/ddb"
	"litepan/internal/discover/identity"
	"litepan/internal/settings"
)

// ---------------------------------------------------------------------------
// 设置注入
// ---------------------------------------------------------------------------

var (
	identitySettingsMu sync.RWMutex
	identitySettings   *settings.Service
)

// BindSettings 注入全局设置服务。
//
// discovery 包本来拿不到全局设置，参照 dmodels.BindSettings
// （internal/discover/dmodels/scrape.go）加同样的注入点，由 internal/app/wire_http.go
// 在启动时装配一次。读的是 settings.Service 的内存副本（String/Bool 都是查 map），
// 所以每次判定都实时读，改设置不需要重启。
func BindSettings(svc *settings.Service) {
	identitySettingsMu.Lock()
	defer identitySettingsMu.Unlock()
	identitySettings = svc
}

func currentSettings() *settings.Service {
	identitySettingsMu.RLock()
	defer identitySettingsMu.RUnlock()
	return identitySettings
}

// identityGateOptions 读取身份校验配置。
//
// ⚠️ PurityRatio 的默认值 0.8 是**推测值，不是从 参考实现 逆向确认的数值**：
// 参考实现 把 dominant_ratio 阈值编译进了 Cython 的 .so，只能读到符号名，读不到字面量。
// 0.8 是工程上的起点（允许 20% 的辅助文件），上线后应按实际误转存率校准。
// AllowUnavailable 是保留的逃生阀，打开会破坏不变式，管理界面已标红警告。
func identityGateOptions() (enabled bool, opts identity.Options) {
	svc := currentSettings()
	opts.PurityRatio = identity.DefaultPurityRatio
	if svc == nil {
		// 没装配设置服务时保持保守：校验开着、用默认阈值。宁可少转也不要转错。
		return true, opts
	}
	// settings.Service 的 getter 自己会回落到 registry 里的 Default，
	// 所以这里不用再传默认值。
	enabled = svc.Bool(settings.KeyMOSubscriptionIdentityEnabled)
	opts.PurityRatio = purityRatioFromSettings(svc)
	opts.AllowManifestUnavailable = svc.Bool(settings.KeyMOSubscriptionIdentityAllowUnavailable)
	return enabled, opts
}

func purityRatioFromSettings(svc *settings.Service) float64 {
	raw := strings.TrimSpace(svc.String(settings.KeyMOSubscriptionIdentityPurityRatio))
	if raw == "" {
		return identity.DefaultPurityRatio
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 || v > 1 {
		return identity.DefaultPurityRatio
	}
	return v
}

// ---------------------------------------------------------------------------
// 记录模型
// ---------------------------------------------------------------------------

// DiscoverySubscriptionIdentityCheck 一次身份校验的判定记录（可查历史）。
// 与 internal/store/migrations/0034_subscription_identity.sql 的建表语句保持一致：
// 两边同时存在，谁先跑都能建出同一张表。
type DiscoverySubscriptionIdentityCheck struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	SubscriptionID uint      `gorm:"index" json:"subscription_id"`
	RunID          uint      `gorm:"index" json:"run_id"`
	RuleID         uint      `gorm:"index" json:"rule_id"`
	ItemKey        string    `gorm:"size:255;index" json:"item_key"`
	Source         string    `gorm:"size:16" json:"source"`
	Provider       string    `gorm:"size:16" json:"provider"`
	Slug           string    `gorm:"size:255" json:"slug"`
	TMDBID         int64     `gorm:"index" json:"tmdb_id"`
	ExpectedTitle  string    `gorm:"size:255" json:"expected_title"`
	CandidateTitle string    `gorm:"size:255" json:"candidate_title"`
	Passed         bool      `gorm:"index" json:"passed"`
	ReasonCode     string    `gorm:"size:48;index" json:"reason_code"`
	Dimension      string    `gorm:"size:16" json:"dimension"`
	EvidenceKind   string    `gorm:"size:16" json:"evidence_kind"`
	MediaType      string    `gorm:"size:16" json:"media_type"`
	Year           int       `json:"year"`
	Season         int       `json:"season"`
	Episode        int       `json:"episode"`
	PurityRatio    float64   `json:"purity_ratio"`
	Detail         string    `gorm:"type:text" json:"-"`
	CreatedAt      time.Time `json:"created_at"`
}

func (DiscoverySubscriptionIdentityCheck) TableName() string {
	return "discovery_subscription_identity_checks"
}

// ---------------------------------------------------------------------------
// 证据抽取
// ---------------------------------------------------------------------------

// manifestForCandidate 把候选资源抽取成身份校验可用的证据。
//
// ⚠️ 目前没有任何来源能提供逐文件清单：tgto123 反代层没有 share-detail 端点，
// hdhive.Resource 结构体也没有文件列表字段。所以除 none 档外一律落到 metadata 档，
// 靠资源标题、备注和正则解析出来的季集证据做校验。manifest 档留给未来的清单来源。
func manifestForCandidate(cand resourceCandidate) identity.Manifest {
	linkType := strings.ToLower(strings.TrimSpace(cand.LinkType))
	if linkType == "magnet" || linkType == "ed2k" {
		// 只有链接，没有任何可用于确认身份的信息 → 一律不转存。
		return identity.Manifest{Kind: identity.EvidenceNone}
	}

	titles := nonEmpty([]string{cand.Title, cand.Remark})
	if len(titles) == 0 {
		// 标题和备注都空，等于什么都没有。
		return identity.Manifest{Kind: identity.EvidenceNone}
	}

	m := identity.Manifest{
		Kind:      identity.EvidenceMetadata,
		Titles:    titles,
		MediaType: cand.MediaType,
	}
	if cand.Episode != nil {
		m.Season = intOrZero(cand.Episode.SeasonNum)
		m.Episode = intOrZero(cand.Episode.EpisodeNum)
		m.EndEpisode = intOrZero(cand.Episode.EndEpisodeNum)
	}
	return m
}

func intOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// identityRequestForSubscription 组装订阅期望身份。
//
// 注意：DiscoverySubscription **没有 year 字段**（只有 TMDBID/MediaType/Title/
// OriginalTitle），所以 ExpectedYear 目前恒为 0 = 不校验年份。保留字段是为了以后
// 补上年份后无需改校验逻辑。
//
// 标题用 Title + OriginalTitle 两份：中文 TMDB 标题与外文原名指向同一部作品，而
// 网盘资源的标题两者皆可能出现，只比对其中一份会把正确资源误判掉。
//
// 季集期望统一为 0（不校验）：仓库里没有「订阅只要第几季第几集」这一说，
// 凭空猜会把正确资源挡掉，等以后订阅偏好真加了季集字段再接上。
func identityRequestForSubscription(sub *DiscoverySubscription) identity.Request {
	if sub == nil {
		// 没有订阅实体（RSS 投递）：不给任何期望值。
		//
		// ⚠️ 刻意**不**在这里退化成「用候选自己的标题当期望标题」——
		// 那等于让候选自己证明自己正确，六维校验全部退化成空转。
		// 期望为空时 identity.Validate 的标题/类型维度会因为 len(expectedKeys)==0
		// 而跳过，只剩下「证据本身能不能解析出媒体」这一条。
		// RSS 目前不走这道闸门（磁力/ed2k 无清单，恒判 unavailable），
		// 但真走到这里时必须是保守语义而不是宽松语义。
		return identity.Request{}
	}
	req := identity.Request{
		ExpectedTitle:  sub.Title,
		ExpectedTMDBID: strconv.FormatInt(sub.TMDBID, 10),
		ExpectedType:   strings.ToLower(strings.TrimSpace(sub.MediaType)),
	}
	if original := strings.TrimSpace(sub.OriginalTitle); original != "" && original != sub.Title {
		req.ExpectedAltTitles = []string{original}
	}
	return req
}

// ---------------------------------------------------------------------------
// 闸门
// ---------------------------------------------------------------------------

// checkCandidateIdentity 对单条候选做身份校验，返回校验结果；调用方按
// identitySkipReason(res) 是否为空决定是否发起转存。
//
// 副作用只有两个：打一行日志、往 discovery_subscription_identity_checks 插一条记录。
// 两者失败都不影响判定 —— 判定的正确性不应该依赖日志和数据库是否可用。
func checkCandidateIdentity(sub *DiscoverySubscription, rule DiscoverySubscriptionRule, cand resourceCandidate, runID uint) identity.Result {
	enabled, opts := identityGateOptions()
	if !enabled {
		return identity.Pass(map[string]any{"identity_gate": "disabled"})
	}

	req := identityRequestForSubscription(sub)
	m := manifestForCandidate(cand)
	res := identity.Validate(req, m, opts)

	logIdentityDecision(sub, rule, cand, runID, req, m, res)
	persistIdentityDecision(sub, rule, cand, runID, req, m, res)
	return res
}

// identitySkipReason 把结果转成人能读的跳过原因；通过时返回空串。
func identitySkipReason(res identity.Result) string {
	if res.Passed {
		return ""
	}
	msg, _ := res.Detail["message"].(string)
	prefix := string(res.Reason)
	if msg == "" {
		return prefix
	}
	return prefix + "：" + msg
}

// logIdentityDecision 打一行可 grep 的判定日志：reason_code 与订阅 ID / TMDB ID /
// 候选标识 / 解析出的标题·年份·季集·纯度 出现在同一行，方便直接 grep。
//
// sub 允许为 nil：RSS 投递（rss_delivery.go）没有订阅实体。nil 时 sub_id/tmdb_id
// 打 0，日志形态不变 —— 统一成一种格式才能 grep。
func logIdentityDecision(sub *DiscoverySubscription, rule DiscoverySubscriptionRule, cand resourceCandidate, runID uint, req identity.Request, m identity.Manifest, res identity.Result) {
	log.Printf("[discovery] identity_check reason=%s dimension=%s passed=%v sub_id=%d tmdb_id=%d rule_id=%d run_id=%d "+
		"candidate=%s source=%s provider=%s link_type=%s evidence=%s "+
		"expected_title=%q expected_type=%q expected_season=%d expected_episode=%d expected_year=%d | "+
		"candidate_title=%q candidate_type=%q candidate_season=%d candidate_episode=%d purity=%v detail=%s",
		res.Reason, orEmpty(res.Dimension, "-"), res.Passed, subIDOf(sub), tmdbIDOf(sub), rule.ID, runID,
		orEmpty(cand.ItemKey, cand.Slug, cand.ShareURL), cand.Source, cand.Provider, cand.LinkType, m.Kind,
		req.ExpectedTitle, req.ExpectedType, req.ExpectedSeason, req.ExpectedEpisode, req.ExpectedYear,
		orEmpty(cand.Title, cand.ChannelTitle), orEmpty(cand.MediaType, "-"),
		m.Season, m.Episode, detailRatio(res), identityDetailJSON(res))
}

// persistIdentityDecision 落库一条判定记录，便于事后查「为什么这条没转」。
func persistIdentityDecision(sub *DiscoverySubscription, rule DiscoverySubscriptionRule, cand resourceCandidate, runID uint, req identity.Request, m identity.Manifest, res identity.Result) {
	if ddb.Db == nil {
		return
	}
	detail, err := json.Marshal(res.Detail)
	if err != nil {
		detail = []byte("{}")
	}
	rec := &DiscoverySubscriptionIdentityCheck{
		SubscriptionID: subIDOf(sub),
		RunID:          runID,
		RuleID:         rule.ID,
		ItemKey:        cand.ItemKey,
		Source:         cand.Source,
		Provider:       cand.Provider,
		Slug:           firstNonEmptyStr(cand.Slug, cand.ShareURL),
		TMDBID:         tmdbIDOf(sub),
		ExpectedTitle:  req.ExpectedTitle,
		CandidateTitle: cand.Title,
		Passed:         res.Passed,
		ReasonCode:     string(res.Reason),
		Dimension:      res.Dimension,
		EvidenceKind:   string(m.Kind),
		MediaType:      cand.MediaType,
		Season:         m.Season,
		Episode:        m.Episode,
		PurityRatio:    detailRatio(res),
		Detail:         string(detail),
	}
	if db := ddb.Db.Create(rec); db.Error != nil {
		log.Printf("[discovery] 身份校验记录落库失败（不影响判定）：%v", db.Error)
	}
}

func detailRatio(res identity.Result) float64 {
	v, _ := res.Detail["dominant_ratio"].(float64)
	return v
}

func identityDetailJSON(res identity.Result) string {
	raw, err := json.Marshal(res.Detail)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func orEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
