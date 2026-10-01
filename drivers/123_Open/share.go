package pan123open

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/driver"
)

// 123 开放平台分享相关端点。
// 说明：分享链接的「分享信息」接口需要 123 侧的分享链接 ID（shareKey），
// 由分享 URL（https://www.123pan.com/s/xxxx 或 https://123pan.com/s/xxxx 等）
// 提取；提取码通过 /api/v1/share/info 校验后由 /api/v1/share/save 落盘。
const (
	pathShareInfo = "/api/v1/share/info"
	pathShareSave = "/api/v1/share/save"
)

// fetchShareInfo 拉取分享信息（校验提取码 + 取分享名与顶级文件列表）。
// 抽出来供转存与「聚合帖防误转」的标题探测共用，避免两处各写一份请求拼装。
func (d *Driver) fetchShareInfo(ctx context.Context, shareKey, sharePwd, target string) (*shareInfoResp, error) {
	infoParams := url.Values{}
	infoParams.Set("shareKey", shareKey)
	if sharePwd != "" {
		infoParams.Set("sharePwd", sharePwd)
	}
	infoParams.Set("parentFileId", "0")
	if target != "" {
		infoParams.Set("targetParentId", target)
	}
	var info shareInfoResp
	if err := d.apiCall(ctx, http.MethodGet, pathShareInfo, infoParams, nil, &info); err != nil {
		return nil, domain.Wrap(domain.CodeDriverError, err)
	}
	if info.ShareKey == "" {
		info.ShareKey = shareKey
	}
	return &info, nil
}

// ProbeShareTitle 实现 driver.ShareDirProbe：只读地取分享内第一个顶级条目名。
//
// 供 TG 频道订阅的「聚合帖防误转」复核使用。刻意不返回错误之外的任何副作用——
// 探测阶段绝不调用 share/save，否则复核动作本身就把内容转存了。
// 取不到名称时返回空串与 nil（调用方按「无法确定即放行」降级，宁放勿拦）。
func (d *Driver) ProbeShareTitle(ctx context.Context, req driver.ShareLinkSaveRequest) (string, error) {
	shareKey, sharePwd := parse123ShareURL(req.ShareURL, req.SharePwd)
	if shareKey == "" {
		return "", nil
	}
	info, err := d.fetchShareInfo(ctx, shareKey, sharePwd, "")
	if err != nil {
		return "", err
	}
	// 优先用分享名（聚合帖场景下它就是分享的顶级目录名，最能反映内容主题）；
	// 分享名缺失时退回分享内第一个条目的文件名。
	if name := strings.TrimSpace(info.ShareName); name != "" {
		return name, nil
	}
	if names := info.fileNames(); len(names) > 0 {
		return names[0], nil
	}
	return "", nil
}

// ShareLinkSaver 实现 driver.ShareLinkSaver：分享链接一键转存。
func (d *Driver) SaveShareLink(ctx context.Context, req driver.ShareLinkSaveRequest) (*driver.ShareLinkSaveResult, error) {
	shareKey, sharePwd := parse123ShareURL(req.ShareURL, req.SharePwd)
	if shareKey == "" {
		return nil, domain.Errorf(domain.CodeValidation, "无法从分享链接解析出 123 分享标识：%s", req.ShareURL)
	}
	target := d.normalizeParent(req.TargetParentID)

	// 1) 校验分享并取分享内根目录文件列表
	info, err := d.fetchShareInfo(ctx, shareKey, sharePwd, target)
	if err != nil {
		return nil, err
	}

	// 2) 转存到目标目录
	body := map[string]any{
		"shareKey":       firstNonEmpty123(info.ShareKey, shareKey),
		"sharePwd":       firstNonEmpty123(sharePwd, info.SharePwd),
		"targetParentId": target,
		"fileIds":        info.fileIDs(),
	}
	var save shareSaveResp
	err = d.apiCall(ctx, http.MethodPost, pathShareSave, nil, body, &save)
	if err != nil {
		// 兼容 field 命名差异（targetParentID / targetDirId）
		body["targetParentID"] = target
		delete(body, "targetParentId")
		body["targetDirId"] = target
		err = d.apiCall(ctx, http.MethodPost, pathShareSave, nil, body, &save)
		if err != nil {
			return nil, domain.Wrap(domain.CodeDriverError, err)
		}
	}

	title := firstNonEmpty123(save.ShareName, info.ShareName, shareKey)
	total := len(info.fileIDs())
	if total == 0 {
		total = save.SavedCount
	}
	if total == 0 {
		total = 1
	}
	return &driver.ShareLinkSaveResult{
		Title:    title,
		Total:    total,
		ParentID: target,
		Message:  firstNonEmpty123(save.Message, "转存成功"),
	}, nil
}

// parse123ShareURL 从 123 分享链接解析 shareKey 与提取码（返回覆盖请求中显式提供的提取码）。
func parse123ShareURL(rawURL, explicitPwd string) (shareKey, sharePwd string) {
	sharePwd = strings.TrimSpace(explicitPwd)
	link := strings.TrimSpace(rawURL)
	if link == "" {
		return "", sharePwd
	}
	// 兼容「链接 提取码:xxxx」整体文本
	if idx := strings.Index(link, "http"); idx > 0 {
		link = link[idx:]
	}
	// 提取码写法：?pwd=xxxx / 提取码：xxxx / 密码 xxxx
	if sharePwd == "" {
		if u, err := url.Parse(link); err == nil {
			q := u.Query()
			if p := firstNonEmpty123(q.Get("pwd"), q.Get("password"), q.Get("code")); p != "" {
				sharePwd = p
			}
		}
	}
	if sharePwd == "" {
		for _, marker := range []string{"提取码", "密码", "pwd", "Pwd", "访问码"} {
			if idx := strings.Index(link, marker); idx >= 0 {
				rest := link[idx+len(marker):]
				rest = strings.TrimLeft(rest, "：:，, 	=")
				fields := strings.FieldsFunc(rest, func(r rune) bool {
					return r == ' ' || r == '\t' || r == '\n' || r == '，' || r == ','
				})
				if len(fields) > 0 {
					sharePwd = strings.TrimSpace(fields[0])
				}
				link = link[:idx]
				break
			}
		}
	}
	// shareKey：路径最后一段（/s/xxxx 或 /s/xxxx.html）
	link = strings.TrimSpace(link)
	link = strings.TrimSuffix(link, "/")
	link = strings.TrimSuffix(link, ".html")
	if idx := strings.IndexAny(link, "?#"); idx >= 0 {
		link = link[:idx]
	}
	// URL 可能被中文/散文包裹（「分享给你 https://… 快去」），在首个空白或中文标点处截断，
	// 避免把尾部文字粘进 shareKey。
	if idx := strings.IndexFunc(link, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' ||
			r == '，' || r == ',' || r == '。' || r == '；' || r == ';' ||
			r == '！' || r == '!' || r == '、' || r == '）' || r == '】'
	}); idx >= 0 {
		link = link[:idx]
	}
	parts := strings.Split(link, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		seg := strings.TrimSpace(parts[i])
		if seg == "" || strings.EqualFold(seg, "s") || strings.Contains(seg, ":") {
			continue
		}
		if strings.Contains(seg, ".") && !strings.HasPrefix(seg, "s") {
			continue
		}
		shareKey = seg
		break
	}
	return shareKey, sharePwd
}

// shareInfoResp /api/v1/share/info 响应
type shareInfoResp struct {
	ShareKey  string `json:"shareKey"`
	ShareName string `json:"shareName"`
	SharePwd  string `json:"sharePwd"`
	FileList  []struct {
		FileID   json.Number `json:"fileId"`
		FileID2  json.Number `json:"fileID"`
		Filename string      `json:"filename"`
		Type     int         `json:"type"`
	} `json:"fileList"`
	Info struct {
		FileList []struct {
			FileID   json.Number `json:"fileId"`
			FileID2  json.Number `json:"fileID"`
			Filename string      `json:"filename"`
			Type     int         `json:"type"`
		} `json:"fileList"`
	} `json:"info"`
}

// fileIDs 分享内文件 ID 列表（兼容 fileList / info.fileList 两种外壳）
func (r shareInfoResp) fileIDs() []string {
	entries := r.FileList
	if len(entries) == 0 {
		entries = r.Info.FileList
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		id := e.FileID2.String()
		if id == "" || id == "0" {
			id = e.FileID.String()
		}
		if id != "" && id != "0" {
			out = append(out, id)
		}
	}
	return out
}

// fileNames 分享内顶级条目名称列表（兼容 fileList / info.fileList 两种外壳）。
// 与 fileIDs 同样兼容外壳差异：123 在不同接口版本上把列表放在这两个位置之一。
func (r shareInfoResp) fileNames() []string {
	entries := r.FileList
	if len(entries) == 0 {
		entries = r.Info.FileList
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if name := strings.TrimSpace(e.Filename); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// shareSaveResp /api/v1/share/save 响应
type shareSaveResp struct {
	ShareName  string `json:"shareName"`
	SavedCount int    `json:"savedCount"`
	Message    string `json:"message"`
}

// firstNonEmpty123 首个非空字符串
func firstNonEmpty123(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
