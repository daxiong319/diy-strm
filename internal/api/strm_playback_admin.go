package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/playback"
	"litepan/internal/playpath"
	"litepan/internal/settings"
	"litepan/internal/strm"
)

// strmRedirectPlay 是「只要直链」的播放入口：解析出网盘直链后 302 给播放器，
// 自己一个字节都不吐。
//
// 它与 /strm/play 的差别不是「快一点」，而是**流量记在哪**：本端点吐 302，
// 播放监控只建一个会话、流量恒为 0；/strm/play 转发字节，外网时计入本站计费。
// 鉴权与 strmPlay 完全一致 —— 直链一样是凭据，只是不经本站转发。
func (h *Handler) strmRedirectPlay(w http.ResponseWriter, r *http.Request) {
	if h.strm == nil || h.playback == nil {
		writeErr(w, domain.Errf(domain.CodeNotImplement))
		return
	}
	h.logSTRMPlayEntry(r, "strm_redirect_play")
	accountID, err := parsePathInt64(r, "account_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	fileID, err := strm.DecodeFileKey(chi.URLParam(r, "file_key"))
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法 file_key"))
		return
	}
	if err := h.authorizeSTRMPlay(r); err != nil {
		h.logSTRMPlayDenied(r, "strm_redirect_play", err)
		writeErr(w, err)
		return
	}
	fileName, _ := url.PathUnescape(chi.URLParam(r, "filename"))
	if err := h.playback.ServeRedirect(w, r, playback.Request{
		AccountID: accountID,
		FileID:    fileID,
	}, playback.Intent{FileName: fileName}); err != nil {
		writeErr(w, err)
	}
}

// strmPathRedirectPlay 是 strmRedirectPlay 的按路径版本，同样先过播放路径映射。
func (h *Handler) strmPathRedirectPlay(w http.ResponseWriter, r *http.Request) {
	if h.strm == nil || h.playback == nil || h.files == nil {
		writeErr(w, domain.Errf(domain.CodeNotImplement))
		return
	}
	h.logSTRMPlayEntry(r, "strm_path_redirect_play")
	accountID, err := parsePathInt64(r, "account_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	rootID, err := strm.DecodePathKey(chi.URLParam(r, "root_key"))
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法 root_key"))
		return
	}
	relativePath, err := strm.DecodePathKey(chi.URLParam(r, "path_key"))
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法 path_key"))
		return
	}
	if err := h.authorizeSTRMPlay(r); err != nil {
		h.logSTRMPlayDenied(r, "strm_path_redirect_play", err)
		writeErr(w, err)
		return
	}
	relativePath = h.mapPlayPath(relativePath)
	item, err := h.files.ResolvePath(r.Context(), accountID, rootID, relativePath)
	if err != nil {
		writeErr(w, err)
		return
	}
	fileName, _ := url.PathUnescape(chi.URLParam(r, "filename"))
	if fileName == "" {
		fileName = item.Name
	}
	if err := h.playback.ServeRedirect(w, r, playback.Request{
		AccountID: accountID,
		FileID:    item.ID,
	}, playback.Intent{FileName: fileName}); err != nil {
		writeErr(w, err)
	}
}

// strmPathMappingRules 返回播放路径映射的规则与命中统计。
//
// 命中统计是这套「静默失败」配置唯一的自查依据：规则匹配不上不报错，
// 页面若不显示「最近一次是否命中、命中了几次」，用户就只能靠猜。
func (h *Handler) strmPathMappingRules(w http.ResponseWriter, r *http.Request) {
	if h.playPath == nil {
		writeErr(w, domain.Errf(domain.CodeNotImplement))
		return
	}
	writeOK(w, map[string]any{
		"enabled":   h.playPath.Enabled(),
		"rules":     h.playPath.Rules(),
		"conflicts": playpath.ConflictsOf(h.playPath),
	})
}

// strmPathMappingSave 保存播放路径映射规则。
//
// 同源冲突**只警告不阻止**：顺序匹配下两条同源规则是合法配置
// （后一条就是永远命不到的死规则），但用户多半正在写后一条而没意识到
// 前一条已经覆盖了它。直接拒绝保存会让用户以为「冲突是错误」，
// 反而把正确的规则删掉；只警告并把被遮蔽的条目列出来，页面才能让他自己判断。
//
// 保存走 Sync 而不是 SetRules：Sync 认开关，SetRules 恒把开关打开，
// 那样「关掉映射并改规则」会被悄悄变成「打开映射」。
func (h *Handler) strmPathMappingSave(w http.ResponseWriter, r *http.Request) {
	if h.playPath == nil {
		writeErr(w, domain.Errf(domain.CodeNotImplement))
		return
	}
	var in struct {
		Enabled bool            `json:"enabled"`
		Rules   []playpath.Rule `json:"rules"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	// 冲突警告必须基于**用户提交的原始列表**：Normalize 已经把同源的后一条丢掉了，
	// 拿归一化后的规则去找冲突，结果永远是空 —— 恰好把最需要提示的那种情况说成没问题。
	conflicts := playpath.Conflicts(in.Rules)
	// 先按「旧规则 → 新规则」的对应关系保住统计：Sync 在原文变化时重建规则表，
	// 同一批规则只改顺序/内容时统计要跟着走。
	h.playPath.SetRules(in.Rules)
	h.playPath.Sync(playpath.Config{Enabled: in.Enabled})
	raw, err := playpath.Encode(h.playPath.PlainRules())
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "规则无法保存：%v", err))
		return
	}
	if h.settings != nil {
		if err := h.settings.Update(r.Context(), map[string]string{
			settings.KeyMOPlayPathMappingEnabled: strconv.FormatBool(in.Enabled),
			settings.KeyMOPathMappingRules:       raw,
		}); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeOK(w, map[string]any{
		"enabled":   h.playPath.Enabled(),
		"rules":     h.playPath.Rules(),
		"conflicts": conflicts,
		"saved":     len(in.Rules),
	})
}

// strmPathMappingTest 用**与真实播放完全相同**的映射逻辑试一条路径。
//
// 不另写一份匹配代码是刻意的：另写一份就可能在「用户最需要说真话的时候」
// 说假话（比如把顺序悄悄改成最精确优先），而页面上的绿色对勾正是用户
// 判断「规则到底生不生效」的唯一依据。
func (h *Handler) strmPathMappingTest(w http.ResponseWriter, r *http.Request) {
	if h.playPath == nil {
		writeErr(w, domain.Errf(domain.CodeNotImplement))
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	res := h.playPath.TestPath(in.Path)
	writeOK(w, map[string]any{
		"matched":    res.Matched,
		"rule_id":    res.RuleID,
		"source":     res.Source,
		"from":       res.From,
		"to":         res.To,
		"result":     res.Path,
		"candidates": res.Candidates,
		"message":    playpathVerdict(res),
	})
}

// playpathVerdict 把一次测试结果说成人话。三态必须区分得开：
// 命中 / 命中但被后面的规则遮蔽 / 从未命中 ——
// 「被遮蔽」最容易被用户误判成「我这条规则写错了」。
func playpathVerdict(res playpath.Result) string {
	switch {
	case res.Matched:
		if len(res.Candidates) > 0 {
			return "已命中规则 " + res.RuleID + "；另有 " + strconv.Itoa(len(res.Candidates)) +
				" 条规则也能匹配但排在它后面，属于永远不会生效的死规则：" + playpathRuleIDs(res.Candidates)
		}
		return "已命中规则 " + res.RuleID + "，路径已改写"
	case len(res.Candidates) > 0:
		return "已命中规则 " + res.Candidates[0].ID + "，路径已改写"
	default:
		return "从未命中：路径原样放行。若期望它被改写，请检查源前缀是否写对（区分大小写、需带前导斜杠），或是否有别的规则排在前面先命中了"
	}
}

// playpathRuleIDs 把被遮蔽规则的 ID 拼出来。
// 只说「另有 1 条」用户还得自己回去数是哪一条；直接点名才可能发现「原来我第二条白写了」。
func playpathRuleIDs(cands []playpath.ShadowedRule) string {
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, "、")
}
