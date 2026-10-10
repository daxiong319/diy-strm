package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/playback"
	"litepan/internal/playbackfallback"
	"litepan/internal/settings"
	"litepan/internal/strm"
)

func (h *Handler) strmPlay(w http.ResponseWriter, r *http.Request) {
	if h.strm == nil || h.playback == nil {
		writeErr(w, domain.Errf(domain.CodeNotImplement))
		return
	}
	h.logSTRMPlayEntry(r, "strm_play")
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
		h.logSTRMPlayDenied(r, "strm_play", err)
		writeErr(w, err)
		return
	}
	fileName, _ := url.PathUnescape(chi.URLParam(r, "filename"))
	// 跨账户转移放在鉴权之后、真正取流之前：鉴权没过就没有转存的必要
	// （否则任何人都能拿一个合法 token 反复触发转存），而取流之前才谈得上
	// 「换一条路再试」。这一步只读不写，副作用只有可能的后台转存。
	req := playback.Request{AccountID: accountID, FileID: fileID}
	if h.applyCrossAccountFallback(w, r, req, fileName) {
		return
	}
	if err := h.playback.ServeHTTP(w, r, req, h.playIntent(fileName)); err != nil {
		h.noteCrossAccountFailure(r.Context(), accountID, err)
		writeErr(w, err)
		return
	}
	h.noteCrossAccountSuccess(accountID)
}

func (h *Handler) strmPathPlay(w http.ResponseWriter, r *http.Request) {
	if h.strm == nil || h.playback == nil || h.files == nil {
		writeErr(w, domain.Errf(domain.CodeNotImplement))
		return
	}
	h.logSTRMPlayEntry(r, "strm_path_play")
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
		h.logSTRMPlayDenied(r, "strm_path_play", err)
		writeErr(w, err)
		return
	}
	// T17 播放路径映射：改写**本地挂载路径**之后再去网盘上找这个文件。
	// 顺序不能反 —— 规则的用户脑子里是「我挂载在哪」，不是「网盘上有什么」；
	// 先解析再映射等于拿挂载路径去网盘里找，永远找不到。
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
	req := playback.Request{AccountID: accountID, FileID: item.ID}
	if h.applyCrossAccountFallback(w, r, req, fileName) {
		return
	}
	if err := h.playback.ServeHTTP(w, r, req, h.playIntent(fileName)); err != nil {
		h.noteCrossAccountFailure(r.Context(), accountID, err)
		writeErr(w, err)
		return
	}
	h.noteCrossAccountSuccess(accountID)
}

// mapPlayPath 按用户配置的规则改写本地挂载路径。
//
// 没配映射服务时原样返回：映射功能默认关闭（mo_play_path_mapping_enabled），
// 关闭状态必须与「一条规则都没有」完全等价，否则关掉开关反而会改变行为。
func (h *Handler) mapPlayPath(relativePath string) string {
	if h.playPath == nil {
		return relativePath
	}
	return h.playPath.Map(relativePath).Path
}

// playIntent 依据「播放模式」与「是否允许 302 直连」两个设置拼出本次播放意图。
//
// 两个开关都默认关闭，即默认行为与 T17 之前逐字一致（流代理、计入监控）。
func (h *Handler) playIntent(fileName string) playback.Intent {
	intent := playback.Intent{FileName: fileName}
	if h.settings == nil {
		return intent
	}
	// mo_play_mode=redirect 是显式选择；mo_strm_redirect_enabled 是「即使驱动
	// 判成代理也允许 302」的额外许可。两个都开才给 ForceRedirect。
	if h.settings.String(settings.KeyMOPlayMode) == settings.KeyMOPlayModeRedirect &&
		h.settings.Bool(settings.KeyMOStrmRedirectEnabled) {
		intent.ForceRedirect = true
	}
	return intent
}

// applyCrossAccountFallback 在真正取流前决定要不要换账号。
// 返回 true 表示已经答复了客户端，调用方不该再取流。
func (h *Handler) applyCrossAccountFallback(w http.ResponseWriter, r *http.Request, req playback.Request, fileName string) bool {
	fb := h.crossAccount
	if fb == nil {
		return false
	}
	d := fb.Resolve(r.Context(), playbackfallback.Request{
		AccountID: req.AccountID,
		FileID:    req.FileID,
		FileName:  fileName,
	})
	if !d.Switched {
		return false
	}
	if !d.Preparing {
		// 理论上 Resolve 不会返回 Switched 且 Preparing=false 的组合；
		// 真出现了也只能照常播，绝不能拿一个空账号去取流。
		return false
	}
	if d.FileID == "" {
		// 目标文件还在上传队列里。此时**不能**拿空 fileID 去取流
		// （会是一个语焉不详的 404），也不该把「正在准备」当成错误 ——
		// 播放器会自己重试，返 503 会让一部分播放器直接放弃。
		w.Header().Set("Retry-After", "10")
		writeJSON(w, http.StatusServiceUnavailable, Resp{
			Success:   false,
			Message:   d.Reason,
			ErrorType: string(domain.CodeValidation),
		})
		return true
	}
	// 秒传命中：目标文件已经在目标账号里，可以直接改走目标账号取流。
	req = playback.Request{AccountID: d.AccountID, FileID: d.FileID}
	if err := h.playback.ServeHTTP(w, r, req, h.playIntent(fileName)); err != nil {
		writeErr(w, err)
		return true
	}
	return true
}

// noteCrossAccountFailure 把一次取流失败喂给跨账户转移服务，
// 让它据此维护「连续不可用」的起点。
func (h *Handler) noteCrossAccountFailure(ctx context.Context, accountID int64, err error) {
	if h.crossAccount == nil {
		return
	}
	h.crossAccount.Observe(accountID, false, err.Error())
}

// noteCrossAccountSuccess 成功一次就清掉观察，否则「坏过一次」的账号
// 会在下次刚出故障时立刻被判定为长期失效。
func (h *Handler) noteCrossAccountSuccess(accountID int64) {
	if h.crossAccount != nil {
		h.crossAccount.Forget(accountID)
	}
}

// logSTRMPlayEntry 在 debug 级别记录 STRM 播放入口请求。
// 这是「客户端回来取流」的必经入口，且日志打在 token/签名校验之前，
// 因此能把「压根没来取」和「来了但被鉴权挡掉」分开——排查直读类播放问题时这是关键分界。
// 只记 token/签名的有无，不记它们的值（凭据不能进日志）。
func (h *Handler) logSTRMPlayEntry(r *http.Request, route string) {
	if h.log == nil {
		return
	}
	h.log.Debug("STRM 播放入口",
		"route", route,
		"user_agent", r.UserAgent(),
		"has_token", strings.TrimSpace(chi.URLParam(r, "token")) != "",
		"has_signature", strings.TrimSpace(chi.URLParam(r, "signature")) != "",
		"signature_required", h.strm != nil && h.strm.SignatureEnabled(),
	)
}

// logSTRMPlayDenied 记录鉴权失败的取流请求，用来区分「回来了但被拒」和「压根没回来」。
func (h *Handler) logSTRMPlayDenied(r *http.Request, route string, err error) {
	if h.log == nil {
		return
	}
	h.log.Debug("STRM 播放鉴权失败", "route", route, "user_agent", r.UserAgent(), "error", err)
}

func (h *Handler) authorizeSTRMPlay(r *http.Request) error {
	ok, err := h.strm.MatchToken(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		return err
	}
	if !ok {
		return domain.Errf(domain.CodePermissionDenied)
	}
	signature := chi.URLParam(r, "signature")
	if h.strm.SignatureEnabled() {
		if signature == "" {
			return domain.Errf(domain.CodePermissionDenied)
		}
		unsignedPath := strings.TrimSuffix(r.URL.EscapedPath(), "/s/"+signature)
		if !h.strm.VerifySignature(unsignedPath, signature) {
			return domain.Errf(domain.CodePermissionDenied)
		}
	}
	return nil
}
