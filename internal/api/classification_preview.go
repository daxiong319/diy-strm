package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize"
	"litepan/internal/mediaorganize/classification"
)

// classificationPreviewReq 是预览端点的入参（C-7）。
//
// 字段名必须与 web/src/api/cloudTools.ts 里 classificationApi.preview 的
// 字面量逐字一致 —— decodeJSON 开了 DisallowUnknownFields，名字对不上是 400。
type classificationPreviewReq struct {
	MediaType string         `json:"media_type"`
	TMDBID    string         `json:"tmdb_id"`
	Title     string         `json:"title"`
	Year      int            `json:"year"`
	Raw       map[string]any `json:"raw"`
}

// classificationPreviewResp 是预览端点的出参。
type classificationPreviewResp struct {
	Path           string         `json:"path"`
	Segments       []string       `json:"segments"`
	MatchedRule    string         `json:"matched_rule"`
	DegradedReason string         `json:"degraded_reason"`
	Template       string         `json:"template"`
	Category       string         `json:"category"`
	Applied        bool           `json:"applied"`
	Matched        bool           `json:"matched"`
	Evidence       map[string]any `json:"evidence,omitempty"`
}

// previewClassification 只算不写：拿一份影片元数据跑一次分类，返回会被拼出来的
// 相对目录段，让用户在改规则前就能看到「这条规则会把文件放到哪」。
//
// 刻意不落库、不写目录、不调任何网盘接口 —— 这是一个纯函数式端点，
// 界面上每敲一个字都会调它。
func (h *Handler) previewClassification(w http.ResponseWriter, r *http.Request) {
	if h.classifyOrganize == nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "分类整理未初始化"))
		return
	}
	var req classificationPreviewReq
	if !decodeJSONBody(w, r, &req) {
		return
	}
	mediaType := strings.TrimSpace(req.MediaType)
	switch mediaType {
	case "movie", "tv":
	case "":
		writeErr(w, domain.Errorf(domain.CodeValidation, "请填写媒体类型"))
		return
	default:
		// 不静默回落成 movie：媒体类型填错时分类结果整个是错的，
		// 用户看到的是一个看起来正常的目录，不会怀疑自己填错了。
		writeErr(w, domain.Errorf(domain.CodeValidation, "媒体类型只能是 movie 或 tv"))
		return
	}
	year := req.Year
	if year < 0 || year > 3000 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "年份超出合理范围"))
		return
	}
	raw := make(map[string]any, len(req.Raw)+1)
	for k, v := range req.Raw {
		raw[k] = v
	}

	// Loader 必须挂上，否则预览填了 TMDB ID 也查不到详情，
	// 而真实执行路径（planner.classifyGroup）是把 Planner 自己当 Loader 传进去的。
	// 不挂的症状特别有欺骗性：预览里只显示到「电影/国产」就停住，
	// 三级年份目录看上去像没配好，实际上真实执行会走到 2000-2009。
	// 这是预览与执行不一致的头号来源，所以宁可 nil 报错也不要悄悄降级。
	var loader classification.DetailLoader
	if tmdbID := strings.TrimSpace(req.TMDBID); tmdbID != "" {
		if h.mediaOrganize == nil {
			writeErr(w, domain.Errorf(domain.CodeInternal, "TMDB 查询未初始化，无法按 TMDB ID 预览"))
			return
		}
		loader = previewDetailLoader{svc: h.mediaOrganize}
	}

	decision, err := h.classifyOrganize.Classify(r.Context(), classification.Request{
		MediaType: mediaType,
		TMDBID:    strings.TrimSpace(req.TMDBID),
		Title:     strings.TrimSpace(req.Title),
		Year:      year,
		Raw:       raw,
		Loader:    loader,
	})
	switch {
	case err == nil:
	case errors.Is(err, classification.ErrUnavailable):
		// 与 classifyorganize.Available() 的其他入口一致：功能没开就说没开，
		// 不要伪装成「没匹配到规则」。
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "分类整理未启用"))
		return
	default:
		writeErr(w, wrapClassificationErr(err))
		return
	}

	writeOK(w, classificationPreviewResp{
		Path:           classificationPathLabel(decision.RelativeSegments),
		Segments:       decision.RelativeSegments,
		MatchedRule:    matchedRuleLabel(decision),
		DegradedReason: decision.DegradedReason,
		Template:       decision.Template,
		Category:       decision.Category,
		Applied:        decision.Applied,
		Matched:        decision.Matched,
		Evidence:       decision.Evidence,
	})
}

// classificationPathLabel 把相对段拼成可读路径。
//
// 末尾补一个 "/" 是刻意的：它表达的是「目录」而不是「文件名」，
// 少了它用户会把结果当成最终文件名，而命名模板那一层还没渲染上去。
func classificationPathLabel(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	cleaned := make([]string, 0, len(segments))
	for _, seg := range segments {
		if name := strings.TrimSpace(seg); name != "" {
			cleaned = append(cleaned, name)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	return strings.Join(cleaned, "/") + "/"
}

// matchedRuleLabel 描述命中的那一层规则。
//
// 返回目录名而不是规则 ID：预览的用户是设置规则的人，他要知道的是
// 「命中了哪一层」，而不是一个要回表里查的编号。
func matchedRuleLabel(decision classification.Decision) string {
	if !decision.Applied || !decision.Matched || len(decision.RelativeSegments) == 0 {
		return ""
	}
	return decision.RelativeSegments[len(decision.RelativeSegments)-1]
}

// wrapClassificationErr 把包内错误翻成 API 错误码。
//
// 分类包自己就是用 domain.Errorf 抛错的（配置校验都在 config.go 里），
// 所以这里不需要识别任何自有错误类型：writeErr 已经认 domain.AsAppError。
// 保留这个函数是为了以后分类包改用裸 error 时不用改 handler。
func wrapClassificationErr(err error) error {
	return domain.Wrap(domain.CodeInternal, err)
}

// previewDetailLoader 让预览端点能像 planner 一样按 TMDB ID 拉详情。
//
// 复用 mediaorganize.Service.LookupTMDBDetail 而不是自己再写一个客户端：
// 密钥、超时、错误类型都只在这一处定义过一份，第二份必然在某次改动里漂移。
type previewDetailLoader struct {
	svc *mediaorganize.Service
}

func (l previewDetailLoader) Lookup(ctx context.Context, tmdbID, mediaType string) (json.RawMessage, error) {
	return l.svc.LookupTMDBDetail(ctx, tmdbID, mediaType, "")
}
