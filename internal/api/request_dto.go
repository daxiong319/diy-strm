package api

import (
	"time"

	"litepan/internal/mediarequest"
)

// 求片相关的对外 JSON 契约。
//
// 为什么不直接把 mediarequest.Request 序列化出去：那个结构体是**存储层**的形状
// （PascalCase 字段、无 json tag、时间是 Go time.Time、Status 是命名 string 类型）。
// 直接吐出去等于把存储结构变成公开 API：以后给表加一列就会悄悄改掉前端契约，
// 而且字段名大小写一变前端就静默拿到 undefined。
//
// 这里显式定义 DTO 的另一个理由：**两张页面看到的求片单长得不一样**。
// 求片站（手机）只需要「片名/季/状态/我的标签」，审核台（桌面）需要审核人、
// 驳回理由、订阅 ID。把后者也发给家人，等于把审核流程内部状态透给不需要看的人。

// requestStatusText 求片单状态的中文。
//
// 前端不该自己拿英文状态码拼中文（两个页面的措辞要一致），
// 但**前端仍然要按状态分支**（待审显示"等待审核"、被拒显示理由），
// 所以这里同时给出机器可判的 status 和给人看的 status_text。
var requestStatusText = map[mediarequest.Status]string{
	mediarequest.StatusPending:   "等待审核",
	mediarequest.StatusApproved:  "已通过",
	mediarequest.StatusRejected:  "已驳回",
	mediarequest.StatusFulfilled: "已转存",
}

// requestDTO 求片单（求片站视角：自己的单，只给必要信息）。
type requestDTO struct {
	ID         int64    `json:"id"`
	TMDBID     int64    `json:"tmdb_id"`
	Title      string   `json:"title"`
	MediaType  string   `json:"media_type"`
	Season     int      `json:"season"`
	Status     string   `json:"status"`
	StatusText string   `json:"status_text"`
	Notes      string   `json:"notes,omitempty"`
	Tags       []string `json:"tags"`
	// RejectReason 只在被驳回时有值 —— 它是家人唯一能行动的信息。
	RejectReason string `json:"reject_reason,omitempty"`
	// SubscriptionLinked 表示这条求片已经建好订阅（求片站要知道"还在盯着"）。
	SubscriptionLinked bool   `json:"subscription_linked"`
	CreatedAt          string `json:"created_at"`
}

// requestReviewDTO 求片单（审核台视角：多出审核痕迹与订阅指针）。
type requestReviewDTO struct {
	requestDTO
	RequesterID   int64  `json:"requester_id"`
	RequesterName string `json:"requester_name"`
	Year          int    `json:"year"`
	PosterURL     string `json:"poster_url,omitempty"`
	ReviewedAt    string `json:"reviewed_at,omitempty"`
	ReviewerName  string `json:"reviewer_name,omitempty"`
	ReviewedNote  string `json:"reviewed_note,omitempty"`
	// SubscriptionID 指向 discovery_subscriptions.id；0 表示还没建。
	// 审核台靠它判断"点了通过但什么都没发生"这类异常。
	SubscriptionID int64 `json:"subscription_id"`
}

// requestRuleDTO 求片规则。
type requestRuleDTO struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	MediaType       string `json:"media_type"`
	DailyLimit      int    `json:"daily_limit"`
	PendingLimit    int    `json:"pending_limit"`
	AutoApprove     bool   `json:"auto_approve"`
	AppliesToUserID int64  `json:"applies_to_user_id"`
	Enabled         bool   `json:"enabled"`
	Priority        int    `json:"priority"`
}

// requestStatsDTO 一行统计（一天 × 一个人 × 一种类型）。
type requestStatsDTO struct {
	Day            string `json:"day"`
	RequesterID    int64  `json:"requester_id"`
	MediaType      string `json:"media_type"`
	SubmittedCount int    `json:"submitted_count"`
	ApprovedCount  int    `json:"approved_count"`
	RejectedCount  int    `json:"rejected_count"`
	FulfilledCount int    `json:"fulfilled_count"`
}

// requestDTOOf 转求片站视角。
func requestDTOOf(r mediarequest.Request) requestDTO {
	text, ok := requestStatusText[r.Status]
	if !ok {
		text = string(r.Status)
	}
	return requestDTO{
		ID:                 r.ID,
		TMDBID:             r.TMDBID,
		Title:              r.Title,
		MediaType:          r.MediaType,
		Season:             r.Season,
		Status:             string(r.Status),
		StatusText:         text,
		Notes:              r.Notes,
		Tags:               nonNilStrings(r.Tags),
		RejectReason:       r.RejectReason,
		SubscriptionLinked: r.SubscriptionID > 0,
		CreatedAt:          r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// requestReviewDTOOf 转审核台视角。
func requestReviewDTOOf(r mediarequest.Request) requestReviewDTO {
	return requestReviewDTO{
		requestDTO:     requestDTOOf(r),
		RequesterID:    r.RequesterID,
		RequesterName:  r.RequesterName,
		Year:           r.Year,
		PosterURL:      r.PosterURL,
		ReviewedNote:   r.ReviewedNote,
		SubscriptionID: r.SubscriptionID,
		ReviewerName:   r.ReviewerName,
		ReviewedAt:     tsOrEmpty(r.ReviewedAt),
	}
}

// requestRuleInput 是 PUT /media-request/rules 的入参。
//
// 为什么不让前端直接传 mediarequest.Rule：那个结构体没有 json tag，
// 序列化出来是 PascalCase（MediaType/AutoApprove），而读接口出去的
// requestRuleDTO 是 snake_case。前端「读回来改一项再存回去」就会因为
// 字段名对不上被严格模式的 decodeJSON 打回 400 —— 规则页整个不可用。
// 入参必须和出参用同一套字段名，这层转换就是为此存在。
type requestRuleInput struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	MediaType       string `json:"media_type"`
	DailyLimit      int    `json:"daily_limit"`
	PendingLimit    int    `json:"pending_limit"`
	AutoApprove     bool   `json:"auto_approve"`
	AppliesToUserID int64  `json:"applies_to_user_id"`
	Enabled         bool   `json:"enabled"`
	Priority        int    `json:"priority"`
}

func (in requestRuleInput) toRule() mediarequest.Rule {
	return mediarequest.Rule{
		ID:              in.ID,
		Name:            in.Name,
		MediaType:       in.MediaType,
		DailyLimit:      in.DailyLimit,
		PendingLimit:    in.PendingLimit,
		AutoApprove:     in.AutoApprove,
		AppliesToUserID: in.AppliesToUserID,
		Enabled:         in.Enabled,
		Priority:        in.Priority,
	}
}

func requestRuleDTOOf(r mediarequest.Rule) requestRuleDTO {
	return requestRuleDTO{
		ID:              r.ID,
		Name:            r.Name,
		MediaType:       r.MediaType,
		DailyLimit:      r.DailyLimit,
		PendingLimit:    r.PendingLimit,
		AutoApprove:     r.AutoApprove,
		AppliesToUserID: r.AppliesToUserID,
		Enabled:         r.Enabled,
		Priority:        r.Priority,
	}
}

func requestStatsDTOOf(r mediarequest.AnalyticsRow) requestStatsDTO {
	return requestStatsDTO{
		Day:            r.Day,
		RequesterID:    r.RequesterID,
		MediaType:      r.MediaType,
		SubmittedCount: r.SubmittedCount,
		ApprovedCount:  r.ApprovedCount,
		RejectedCount:  r.RejectedCount,
		FulfilledCount: r.FulfilledCount,
	}
}

func requestDTOList(list []mediarequest.Request) []requestDTO {
	out := make([]requestDTO, 0, len(list))
	for _, r := range list {
		out = append(out, requestDTOOf(r))
	}
	return out
}

func requestReviewDTOList(list []mediarequest.Request) []requestReviewDTO {
	out := make([]requestReviewDTO, 0, len(list))
	for _, r := range list {
		out = append(out, requestReviewDTOOf(r))
	}
	return out
}

func requestRuleDTOList(list []mediarequest.Rule) []requestRuleDTO {
	out := make([]requestRuleDTO, 0, len(list))
	for _, r := range list {
		out = append(out, requestRuleDTOOf(r))
	}
	return out
}

// nonNilStrings 把 nil 切片变成空切片。
//
// JSON 里 nil 会变成 null，前端拿到就是 `tags === null` 而不是数组 ——
// 模板里直接 `tags.length` 会抛。前端自己判空当然也行，
// 但让契约永远是数组可以少一类「忘了判 null」的线上错误。
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func tsOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
