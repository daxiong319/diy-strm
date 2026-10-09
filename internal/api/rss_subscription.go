package api

// RSS 订阅源管理接口（T16）。
//
// 路由挂在 /api/admin/rss-* —— 必须在 /admin 子树**里面**（router.go 里注册）。
// T10 的 library-share 就是在 /admin 之外挂的，前端按 /admin/ 写，
// 五个操作全部 404。这里不重复再挂一份免得同一功能两套路径。
//
// ⚠️ RSS 源是**用户自己贴的外部地址**，所以本文件比别的管理接口多两件事：
//
//  1. 预览（preview）：不落库就能看 feed 抓回来什么。抓外部地址是有副作用的
//     （真的发了一个 HTTP 请求），所以「先预览再保存」是必须有的路径，
//     不能让用户靠「保存 → 立即同步 → 看报错」来验证 URL 写没写错。
//  2. 解析失败时的提示必须具体：自研解析器只支持 UTF-8，一个 GBK 源如果只
//     报「解析失败」，用户会去反复重贴 URL，而正确做法是换源或等支持 ——
//     这两件事需要他在界面上看到**不同**的文字。
//
// 同步逻辑本体在 internal/discover/discovery/rss_service.go（包级函数 +
// 包级注入变量，本文件与 discover_channels.go 等一样只做 HTTP 转接）。

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"litepan/internal/discover/discovery"
	"litepan/internal/discover/rss"
	"litepan/internal/domain"
)

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

// rssSourceDTO 源行的响应体。
//
// 单列 DTO 而不是直接暴露 domain.RSSSource：decodeJSON 开了
// DisallowUnknownFields，前端多传一个字段就会 400；而且 domain 模型的
// 时间字段序列化形态（RFC3339 vs "2006-01-02 15:04:05"）不该由前端猜。
type rssSourceDTO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	RssURL       string `json:"rss_url"`
	TargetPath   string `json:"target_path"`
	Storage      string `json:"storage"`
	MediaServer  string `json:"media_server"`
	PosterURL    string `json:"poster_url"`
	IncludeRegex string `json:"include_regex"`
	ExcludeRegex string `json:"exclude_regex"`
	MediaType    string `json:"media_type"`
	Action       string `json:"action"`
	Enabled      bool   `json:"enabled"`
}

type rssSourceUpsertDTO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	RssURL       string `json:"rss_url"`
	TargetPath   string `json:"target_path"`
	Storage      string `json:"storage"`
	MediaServer  string `json:"media_server"`
	PosterURL    string `json:"poster_url"`
	IncludeRegex string `json:"include_regex"`
	ExcludeRegex string `json:"exclude_regex"`
	MediaType    string `json:"media_type"`
	Action       string `json:"action"`
	// Enabled 指针：nil = 不改（PATCH 语义）。
	//
	// ⚠️ 用指针而不是 bool：普通 bool 分不清「用户传了 false」和「用户没传这个
	// 字段」。源列表页有个「只显示启用/只显示停用」的筛选，前端会把整个源对象
	// 回传，普通 bool 会让停用的源因为一次编辑被静默重新启用。
	Enabled *bool `json:"enabled"`
}

type rssPreviewDTO struct {
	RssURL       string `json:"rss_url"`
	IncludeRegex string `json:"include_regex"`
	ExcludeRegex string `json:"exclude_regex"`
}

// rssItemDTO 一条 feed 条目的展示体（预览与同步明细共用）。
type rssItemDTO struct {
	Title  string `json:"title"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Link   string `json:"link,omitempty"`
}

// rssPreviewResult 预览响应体。
type rssPreviewResult struct {
	FeedTitle   string       `json:"feed_title"`
	Items       []rssItemDTO `json:"items"`
	Total       int          `json:"total"`
	Filtered    int          `json:"filtered"`
	NoResource  int          `json:"no_resource"`
	ContentType string       `json:"content_type"`
	Bytes       int64        `json:"bytes"`
	Charsets    []string     `json:"supported_charsets"`
}

func toRSSSourceDTO(row domain.RSSSource) rssSourceDTO {
	return rssSourceDTO{
		ID: row.ID, Name: row.Name, RssURL: row.RssURL, TargetPath: row.TargetPath,
		Storage: row.Storage, MediaServer: row.MediaServer, PosterURL: row.PosterURL,
		IncludeRegex: row.IncludeRegex, ExcludeRegex: row.ExcludeRegex,
		MediaType: row.MediaType, Action: row.Action, Enabled: row.Enabled,
	}
}

func toRSSItemDTO(row discovery.RSSItemResult) rssItemDTO {
	return rssItemDTO{Title: row.Title, Status: row.Status, Reason: row.Reason, Link: row.Link}
}

// ---------------------------------------------------------------------------
// 仓储访问
// ---------------------------------------------------------------------------

// rssStores 两套仓储的注入点。
//
// 单列一层而不是让本文件直接读 discovery.RSSSourceStore：本包的其它
// discovery 转接（discover_channels.go 等）读的是 discovery 包里**自己的**
// 数据层函数，而 RSS 的仓储接口住在 internal/domain。走独立的 Bind 函数后，
// api 的单测可以只替换这两个接口，不必装上整个发现栈。
var rssStores struct {
	source  domain.RSSSourceRepository
	history domain.RSSHistoryRepository
}

// BindRSSStores 由 internal/app/wire_http.go 调用（与 discovery.RSSSourceStore
// 同一处注入）。未调用时所有端点返回「未就绪」而不是 panic。
func BindRSSStores(source domain.RSSSourceRepository, history domain.RSSHistoryRepository) {
	rssStores.source = source
	rssStores.history = history
}

func rssSourceStore() domain.RSSSourceRepository   { return rssStores.source }
func rssHistoryStore() domain.RSSHistoryRepository { return rssStores.history }

// rssPreviewTimeout 预览抓取的 HTTP 超时。
//
// 刻意独立于 mo_rss_http_timeout_seconds：预览是人在等着的交互，20 秒偏短
// （大 feed 首次冷启就可能慢），而轮询是后台任务，短超时是为了不影响别的源。
const rssPreviewTimeout = 60 * time.Second

// ---------------------------------------------------------------------------
// 源 CRUD
// ---------------------------------------------------------------------------

// rssSources 源列表。GET /api/admin/rss-sources?keyword=&enabled=&limit=
func (h *Handler) rssSources(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, rssSourceStore() != nil) {
		return
	}
	q := domain.RSSSourceQuery{
		Keyword: strings.TrimSpace(r.URL.Query().Get("keyword")),
		Limit:   queryInt(r, "limit", 0),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("enabled")); raw != "" {
		enabled := raw == "1" || strings.EqualFold(raw, "true")
		q.Enabled = &enabled
	}
	rows, err := rssSourceStore().List(r.Context(), q)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]rssSourceDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, toRSSSourceDTO(row))
	}
	writeOK(w, map[string]any{"items": items, "total": len(items)})
}

// rssSourceCreate 新增源。POST /api/admin/rss-sources
//
// 重复 URL 的处理：**报错并指出已存在那条**，而不是静默返回 200。
// 同一地址订阅两次会让两轮轮询都处理同一批条目（虽然有 guid 去重兜底，
// 但两条源各自有独立的历史，用户会在「历史」里看到同一份记录出现两遍）。
// 错误文案里带上已有源的 id 与名字，前端可以直接提示「已存在（#3「番剧源」）」。
func (h *Handler) rssSourceCreate(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, rssSourceStore() != nil) {
		return
	}
	var in rssSourceUpsertDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := validateRSSSourceDTO(&in, true); err != nil {
		writeErr(w, err)
		return
	}
	existing, exists, err := rssSourceStore().GetByURL(r.Context(), in.RssURL)
	if err != nil {
		writeErr(w, err)
		return
	}
	if exists {
		writeErr(w, domain.Errorf(domain.CodeValidation,
			"这个 RSS 地址已经存在（订阅源 #%d「%s」），不会重复添加", existing.ID, existing.Name))
		return
	}
	row, err := rssSourceStore().Upsert(r.Context(), toRSSUpsertPayload(domain.RSSSource{}, in, 0))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"source": toRSSSourceDTO(row)})
}

// rssSourceDetail 单个源。GET /api/admin/rss-sources/{id}
//
// 单独开一个 GET：前端编辑弹窗打开时只想拉这一条（带出完整配置），
// 不该为了这个去拉整个列表再在本地 find。
func (h *Handler) rssSourceDetail(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, rssSourceStore() != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	row, ok, err := rssSourceStore().Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, domain.Errorf(domain.CodeNotFound, "RSS 订阅源 #%d 不存在", id))
		return
	}
	writeOK(w, map[string]any{"source": toRSSSourceDTO(row)})
}

// rssSourceUpdate 修改源。PUT /api/admin/rss-sources/{id}
//
// 语义：**部分更新**。空串字段沿用原值，enabled 缺省沿用原值。
// 理由是前端表格里有行内开关（只传 id + enabled 一个字段），
// 真正的全量覆盖得让用户手工带齐十几个字段才能改个名字。
func (h *Handler) rssSourceUpdate(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, rssSourceStore() != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	current, ok, err := rssSourceStore().Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, domain.Errorf(domain.CodeNotFound, "RSS 订阅源 #%d 不存在", id))
		return
	}
	var in rssSourceUpsertDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := validateRSSSourceDTO(&in, false); err != nil {
		writeErr(w, err)
		return
	}
	// 只有**改了地址**才查重复：改 A 成 B 而 B 已存在才算冲突。
	// 不改地址时同一行自己撞自己（existing.ID == id）是正常的。
	if in.RssURL != "" && in.RssURL != current.RssURL {
		existing, exists, err := rssSourceStore().GetByURL(r.Context(), in.RssURL)
		if err != nil {
			writeErr(w, err)
			return
		}
		if exists && existing.ID != id {
			writeErr(w, domain.Errorf(domain.CodeValidation,
				"这个 RSS 地址已经被订阅源 #%d「%s」占用了", existing.ID, existing.Name))
			return
		}
	}
	row, err := rssSourceStore().Upsert(r.Context(), toRSSUpsertPayload(current, in, id))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"source": toRSSSourceDTO(row)})
}

// toRSSUpsertPayload 组装写入载荷。
//
// 空串回填原值：name 与 rss_url 在迁移 0044 里是 NOT NULL，空串会直接写坏
// 数据，而且症状是「编辑一下名字，源的地址没了」。
func toRSSUpsertPayload(current domain.RSSSource, in rssSourceUpsertDTO, id int64) domain.RSSUpsertPayload {
	name := in.Name
	if name == "" {
		name = current.Name
	}
	url := in.RssURL
	if url == "" {
		url = current.RssURL
	}
	return domain.RSSUpsertPayload{
		ID:           id,
		Name:         name,
		RssURL:       url,
		TargetPath:   strings.TrimSpace(in.TargetPath),
		Storage:      strings.TrimSpace(in.Storage),
		MediaServer:  strings.TrimSpace(in.MediaServer),
		PosterURL:    strings.TrimSpace(in.PosterURL),
		IncludeRegex: strings.TrimSpace(in.IncludeRegex),
		ExcludeRegex: strings.TrimSpace(in.ExcludeRegex),
		MediaType:    normalizeRSSMediaType(in.MediaType),
		Action:       normalizeRSSAction(in.Action),
		Enabled:      in.Enabled,
	}
}

// rssSourceDelete 删除源。DELETE /api/admin/rss-sources/{id}?delete_history=1
//
// 连带删历史要显式传参且默认不删：删了 guid 就从唯一约束里消失，
// 同一批条目下一轮会被当成新的再提交一遍离线下载。
func (h *Handler) rssSourceDelete(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, rssSourceStore() != nil && rssHistoryStore() != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	_, ok, err := rssSourceStore().Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, domain.Errorf(domain.CodeNotFound, "RSS 订阅源 #%d 不存在", id))
		return
	}
	deleteHistory := r.URL.Query().Get("delete_history") == "1"
	if err := rssSourceStore().Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	var removed int64
	if deleteHistory {
		if removed, err = rssHistoryStore().DeleteBySource(r.Context(), id); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeOK(w, map[string]any{"deleted": true, "history_removed": removed})
}

// ---------------------------------------------------------------------------
// 同步与预览
// ---------------------------------------------------------------------------

// rssSourceSync 立即同步一个源。POST /api/admin/rss-sources/{id}/sync
//
// 同步是长耗时动作（一个源可能几十条、每条一次提交），给独立的超时预算
// 而不是复用默认的 90s。
func (h *Handler) rssSourceSync(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, rssSourceStore() != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	src, ok, err := rssSourceStore().Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeErr(w, domain.Errorf(domain.CodeNotFound, "RSS 订阅源 #%d 不存在", id))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	writeOK(w, toRSSSyncDTO(discovery.SyncRSSSource(ctx, src)))
}

// rssSyncAll 立即同步全部启用的源。POST /api/admin/rss-sync
func (h *Handler) rssSyncAll(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, rssSourceStore() != nil) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	results := discovery.SyncAllRSSSources(ctx)
	items := make([]rssItemSyncDTO, 0, len(results))
	var added, skipped, failed int
	for _, res := range results {
		items = append(items, toRSSSyncDTO(res))
		added += res.Counters.Added
		skipped += res.Counters.Skipped
		failed += res.Counters.Failed
	}
	writeOK(w, map[string]any{
		"items": items,
		"counters": map[string]int{
			"added": added, "skipped": skipped, "failed": failed,
		},
	})
}

// rssItemSyncDTO 一轮同步的响应体。
//
// 直接用 discovery.RSSSyncResult 会把 domain.RSSSource 整条原样吐出去
// （含存储层的时间字符串格式），前端要自己再翻译一遍；这里显式列字段。
type rssItemSyncDTO struct {
	Source    rssSourceDTO           `json:"source"`
	Counters  domain.RSSSyncCounters `json:"counters"`
	Status    string                 `json:"status"`
	Message   string                 `json:"message"`
	Catchup   bool                   `json:"catchup"`
	Total     int                    `json:"total"`
	FetchedAt string                 `json:"fetched_at"`
	Detail    []rssItemDTO           `json:"detail,omitempty"`
	Notices   []string               `json:"notices,omitempty"`
}

func toRSSSyncDTO(res discovery.RSSSyncResult) rssItemSyncDTO {
	dto := rssItemSyncDTO{
		Source:   toRSSSourceDTO(res.Source),
		Counters: res.Counters,
		Status:   res.Status,
		Message:  res.Message,
		Catchup:  res.Catchup,
		Total:    res.Total,
		Notices:  res.Notices,
	}
	if !res.FetchedAt.IsZero() {
		dto.FetchedAt = res.FetchedAt.Format(time.RFC3339)
	}
	for _, d := range res.Detail {
		dto.Detail = append(dto.Detail, toRSSItemDTO(d))
	}
	return dto
}

// rssPreview 预览一个 feed，不落库。POST /api/admin/rss-preview
//
// 抓外部地址有副作用（真发了一个请求），所以「先看看抓回来什么」必须在保存
// 之前就能做 —— 让用户靠「保存 → 立即同步 → 看报错」来验证 URL，是把三次
// 错误尝试当成正常使用流程。
//
// 预览**套用**用户填的包含/排除正则（取 DTO 里传来的那两个），这样用户能在
// 保存前就看到过滤后剩下什么。
func (h *Handler) rssPreview(w http.ResponseWriter, r *http.Request) {
	var in rssPreviewDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	in.RssURL = strings.TrimSpace(in.RssURL)
	if err := rss.ValidateFeedURL(in.RssURL); err != nil {
		writeErr(w, err)
		return
	}
	include, err := compileRSSPreviewRegex(in.IncludeRegex, "包含")
	if err != nil {
		writeErr(w, err)
		return
	}
	exclude, err := compileRSSPreviewRegex(in.ExcludeRegex, "排除")
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), rssPreviewTimeout)
	defer cancel()
	// 超时给 60s 而不复用 mo_rss_http_timeout_seconds：预览是人在等着的交互，
	// 20 秒偏短（大 feed 冷启就可能慢）；轮询才是后台任务，短超时是为了
	// 不让一个坏源拖慢整轮。
	fetched, err := rss.NewFetcherWithOptions(
		&http.Client{Timeout: rssPreviewTimeout},
		rss.FetchOptions{MaxBytes: rss.MaxFeedBytes},
	).Fetch(ctx, in.RssURL)
	if err != nil {
		// 解析/抓取失败时给**可行动**的提示，而不是一句「解析失败」。
		// 错误文案是用户唯一的排查依据：「解析失败，请检查链接」对一个贴了
		// GBK 源的用户毫无用处，他会去反复重贴同一个 URL。
		writeErr(w, rssPreviewError(err))
		return
	}
	out := rssPreviewResult{
		Items:       []rssItemDTO{},
		ContentType: fetched.ContentType,
		Bytes:       fetched.Bytes,
		Charsets:    rss.SupportedCharsetsForUI(),
	}
	if fetched.Feed != nil {
		out.FeedTitle = fetched.Feed.Title
		out.Total = len(fetched.Feed.Items)
	}
	for _, it := range feedItems(fetched) {
		if include != nil && !include.MatchString(it.Title) {
			out.Filtered++
			continue
		}
		if exclude != nil && exclude.MatchString(it.Title) {
			out.Filtered++
			continue
		}
		if it.Kind == rss.KindNone {
			out.NoResource++
			continue
		}
		out.Items = append(out.Items, rssItemDTO{Title: it.Title, Status: "preview", Link: it.ResourceURL})
	}
	writeOK(w, out)
}

// feedItems 取条目，容忍 nil feed。
func feedItems(res *rss.FetchResult) []rss.Item {
	if res == nil || res.Feed == nil {
		return nil
	}
	return res.Feed.Items
}

// rssPreviewError 把解析/抓取错误翻译成用户能照着做的提示。
func rssPreviewError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, rss.ErrUnsupportedEncoding.Error()):
		return domain.Errorf(domain.CodeValidation,
			"这个 feed 不是 UTF-8 编码，自研解析器暂不支持（GBK/Big5 等）。"+
				"请换一个 UTF-8 的源，或换用支持该编码的客户端。")
	case strings.Contains(msg, rss.ErrNoFeed.Error()):
		return domain.Errorf(domain.CodeValidation,
			"这个地址返回的内容不是 RSS 2.0、也不是 Atom 1.0。"+
				"多半是贴了网页地址而不是 feed 地址 —— 请找带 .xml 结尾、或页面里带 <rss> / <feed> 的那个地址。")
	case strings.Contains(msg, rss.ErrFeedTooLarge.Error()):
		return domain.Errorf(domain.CodeValidation,
			"这个 feed 太大（超过 4 MiB 上限），已放弃抓取以免解析出错误的条目。")
	case strings.Contains(msg, "unsupported feed variant"):
		return domain.Errorf(domain.CodeValidation,
			"这个 feed 用到了自研解析器不支持的变体（%+v）。"+
				"支持范围：RSS 2.0、Atom 1.0、RDF / RSS 1.0，且必须是 UTF-8。", msg)
	default:
		return domain.Errorf(domain.CodeValidation, "抓取失败：%s", msg)
	}
}

// ---------------------------------------------------------------------------
// 历史
// ---------------------------------------------------------------------------

// rssHistory 历史记录。GET /api/admin/rss-history?source_id=&status=&since=&limit=
func (h *Handler) rssHistory(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, rssHistoryStore() != nil) {
		return
	}
	q := domain.RSSHistoryQuery{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Limit:  queryInt(r, "limit", 200),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("source_id")); raw != "" {
		id, err := parseQueryInt64(r, "source_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		q.SourceID = id
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeErr(w, domain.Errorf(domain.CodeValidation, "非法 since（要 RFC3339）：%s", raw))
			return
		}
		q.Since = since
	}
	rows, err := rssHistoryStore().List(r.Context(), q)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]domain.RSSHistory, 0, len(rows))
	items = append(items, rows...)
	writeOK(w, map[string]any{"items": items, "total": len(items)})
}

// rssHistoryDelete 删一条历史。DELETE /api/admin/rss-history/{id}
//
// 语义是**把这个条目加入豁免**：删掉记录后它的 guid 从唯一约束里消失，
// 下一轮同步会重新处理它。UI 上要把这一点写在按钮旁边 —— 用户以为在
// 「清历史」，实际是在「让它再来一次」。
func (h *Handler) rssHistoryDelete(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, rssHistoryStore() != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := rssHistoryStore().DeleteByID(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": true, "id": id})
}

// ---------------------------------------------------------------------------
// 选项
// ---------------------------------------------------------------------------

// rssOptions 解析器能力清单。GET /api/admin/rss-options
//
// 解析器的支持范围**由后端下发**，前端不硬编码一份文案。两份文案必然漂移：
// 解析器加了 Atom，前端那句「只支持 RSS 2.0」还挂在页面上，用户会以为
// Atom 源不被支持。
func (h *Handler) rssOptions(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, map[string]any{
		"charsets": rss.SupportedCharsetsForUI(),
		"formats":  []string{"RSS 2.0", "Atom 1.0", "RDF / RSS 1.0"},
		"kinds": []map[string]string{
			{"value": string(rss.KindMagnet), "label": "磁力链接"},
			{"value": string(rss.KindEd2k), "label": "ed2k 链接"},
			{"value": string(rss.KindTorrent), "label": "种子直链"},
			{"value": string(rss.KindDirect), "label": "HTTP 直链"},
			{"value": string(rss.KindShare), "label": "网盘分享链接"},
		},
		"media_types": []map[string]string{
			{"value": "tv", "label": "剧集 / 番剧"},
			{"value": "movie", "label": "电影"},
		},
		"actions": []map[string]string{
			{"value": domain.RSSActionTransfer, "label": "转存（分享链接）"},
			{"value": domain.RSSActionOffline, "label": "离线下载（磁力 / ed2k）"},
		},
		"media_servers": []map[string]string{
			{"value": "", "label": "不判定（全部媒体库）"},
			{"value": domain.RSSMediaServerNone, "label": "跳过媒体库判定"},
		},
		"limits": map[string]any{
			"max_feed_bytes": rss.MaxFeedBytes,
			"notice":         "解析器为自研实现，只支持 UTF-8；非 UTF-8 feed 会明确报「不支持」，不会静默失败。",
		},
	})
}

// ---------------------------------------------------------------------------
// 校验与归一
// ---------------------------------------------------------------------------

// validateRSSSourceDTO 校验源表单。
//
// requireNameAndURL=false 时（更新）**只校验格式、不校验必填**：空串会在
// toRSSUpsertPayload 里回填原值。
//
// 两个正则**必须在这里编译**而不是等同步时才报错：一个打错的正则会让过滤
// 完全不生效，用户以为自己在筛 1080p、实际每条都下，而那个输入框看起来
// 完全正常。保存时拦下来，错误文案直接说「哪个框、正则为什么非法」。
func validateRSSSourceDTO(in *rssSourceUpsertDTO, requireNameAndURL bool) error {
	in.Name = strings.TrimSpace(in.Name)
	in.RssURL = strings.TrimSpace(in.RssURL)
	if requireNameAndURL {
		if in.Name == "" {
			return domain.Errorf(domain.CodeValidation, "请填写订阅名称")
		}
		if in.RssURL == "" {
			return domain.Errorf(domain.CodeValidation, "请填写 RSS 地址")
		}
		if err := rss.ValidateFeedURL(in.RssURL); err != nil {
			return domain.Errorf(domain.CodeValidation, "RSS 地址无效：%v", err)
		}
	}
	if in.IncludeRegex != "" {
		if _, err := regexp.Compile(strings.TrimSpace(in.IncludeRegex)); err != nil {
			return domain.Errorf(domain.CodeValidation, "包含过滤正则无效：%v", err)
		}
	}
	if in.ExcludeRegex != "" {
		if _, err := regexp.Compile(strings.TrimSpace(in.ExcludeRegex)); err != nil {
			return domain.Errorf(domain.CodeValidation, "排除过滤正则无效：%v", err)
		}
	}
	return nil
}

func compileRSSPreviewRegex(pattern, label string) (*regexp.Regexp, error) {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return nil, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, domain.Errorf(domain.CodeValidation, "%s过滤正则无效：%v", label, err)
	}
	return re, nil
}

// normalizeRSSMediaType 归一媒体类型。
//
// 空 → tv：BT RSS 绝大多数是番剧源，兜底 movie 会让每一条 tv 资源
// 都撞「资源类型与订阅类型不一致」被挡下。
func normalizeRSSMediaType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "movie", "电影":
		return "movie"
	case "tv", "series", "剧集":
		return "tv"
	default:
		return "tv"
	}
}

func normalizeRSSAction(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case domain.RSSActionOffline:
		return domain.RSSActionOffline
	default:
		return domain.RSSActionTransfer
	}
}
