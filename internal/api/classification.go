package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"litepan/internal/classifyorganize"
	"litepan/internal/domain"
)

func (h *Handler) getClassificationConfig(w http.ResponseWriter, _ *http.Request) {
	if h.classifyOrganize == nil {
		writeOK(w, classifyorganize.DefaultConfig())
		return
	}
	writeOK(w, h.classifyOrganize.Config())
}

func (h *Handler) updateClassificationConfig(w http.ResponseWriter, r *http.Request) {
	var in classifyorganize.Config
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if h.classifyOrganize == nil {
		writeOK(w, classifyorganize.DefaultConfig())
		return
	}
	out, err := h.classifyOrganize.Update(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, out)
}

// listClassificationCategories 是 C-8 的只读出口。
//
// 返回值直接沿用 classifyorganize.Category（它已经是纯数据视图，没有
// gorm.Model 之类的东西挂上来），但**不要**在这里补字段：分类目录一旦
// 加工成"前端专用形状"，WashUp 之外的第四个消费者就得反过来解析前端 DTO。
//
// 错误语义在服务层已经定好了：配置读不出来返回错误（清单错了消费者的筛选
// 就是错的），投影写不进去只记日志（投影是加速手段不是真相来源）。这里
// 原样透传，不再翻译成"空列表"——那正是让洗版扫遍整个媒体库的写法。
func (h *Handler) listClassificationCategories(w http.ResponseWriter, r *http.Request) {
	if h.classifyOrganize == nil {
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "分类服务未初始化"))
		return
	}
	cats, err := h.classifyOrganize.ListActiveCategories(r.Context())
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "读取分类目录失败：%v", err))
		return
	}
	if cats == nil {
		// 前端 v-for 一个 nil 与空数组行为相同，但 JSON 里的 null 会让
		// `data || []` 这种兜底之外的写法炸掉。返回 [] 保持形状稳定。
		cats = []classifyorganize.Category{}
	}
	writeOK(w, map[string]any{
		"items": cats,
		// levels 让前端不必自己数层级，也不必假设"一定有三层"——
		// 一级表化（C-3）放开之后层级是可配的。
		"levels": []int{1, 2, 3},
	})
}

type classificationTMDBDetailDTO struct {
	TMDBID    string `json:"tmdb_id"`
	MediaType string `json:"media_type"`
}

func (h *Handler) lookupClassificationTMDBDetail(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.mediaOrganize != nil) {
		return
	}
	var in classificationTMDBDetailDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	in.TMDBID = strings.TrimSpace(in.TMDBID)
	in.MediaType = strings.ToLower(strings.TrimSpace(in.MediaType))
	raw, err := h.mediaOrganize.LookupTMDBDetail(r.Context(), in.TMDBID, in.MediaType, "")
	if err != nil {
		writeErr(w, err)
		return
	}
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "解析 TMDB 详情失败"))
		return
	}
	detail["media_type"] = in.MediaType
	writeOK(w, detail)
}
