package cloud139

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/driver"
)

// 中国移动云盘分享接口（逆向自 Web 端分享页，独立于个人云主机）。
const (
	shareAPIBase    = "https://share-kd-njs.yun.139.com"
	pathShareInfo   = "/yun-share/richlifeApp/devapp/IOutLink/getOutLinkInfoV6"
	pathShareSave   = "/yun-share/richlifeApp/devapp/IBatchOprTask/createOuterLinkBatchOprTask"
	sharePageEndNum = 200
)

// SaveShareLink 实现 driver.ShareLinkSaver：移动云盘分享链接一键转存。
func (d *Driver) SaveShareLink(ctx context.Context, req driver.ShareLinkSaveRequest) (*driver.ShareLinkSaveResult, error) {
	linkID, passwd := parse139ShareURL(req.ShareURL, req.SharePwd)
	if linkID == "" {
		return nil, domain.Errorf(domain.CodeValidation, "无法从分享链接解析出移动云盘分享标识：%s", req.ShareURL)
	}
	targetCatalogID := d.normalizeParent(req.TargetParentID)

	// 1) 分享根目录内容（文件 + 目录）
	info, err := d.shareRequest(ctx, pathShareInfo, map[string]any{
		"getOutLinkInfoReq": map[string]any{
			"account": d.currentAccount(),
			"linkID":  linkID,
			"passwd":  passwd,
			"pCaID":   "root",
			"caSrt":   0,
			"coSrt":   0,
			"srtDr":   1,
			"bNum":    1,
			"eNum":    sharePageEndNum,
		},
	})
	if err != nil {
		return nil, err
	}
	coPaths, caPaths := share139Paths(info)
	if len(coPaths) == 0 && len(caPaths) == 0 {
		return nil, domain.Errorf(domain.CodeDriverError, "分享链接内容为空")
	}

	// 2) 创建转存任务
	_, err = d.shareRequest(ctx, pathShareSave, map[string]any{
		"createOuterLinkBatchOprTaskReq": map[string]any{
			"msisdn":       share139Msisdn(d.currentAccount()),
			"ownerAccount": "",
			"taskType":     1,
			"linkID":       linkID,
			"needPassword": passwd != "",
			"taskInfo": map[string]any{
				"linkID":          linkID,
				"needPassword":    passwd != "",
				"contentInfoList": coPaths,
				"catalogInfoList": caPaths,
				"newCatalogID":    targetCatalogID,
			},
		},
	})
	if err != nil {
		return nil, err
	}

	title := strings.TrimSpace(stringValue139(info["lkName"]))
	if title == "" {
		title = linkID
	}
	total := len(coPaths) + len(caPaths)
	return &driver.ShareLinkSaveResult{
		Title:    title,
		Total:    total,
		ParentID: targetCatalogID,
		Message:  "转存任务已提交",
	}, nil
}

// shareRequest 调用分享接口（复用驱动签名，指向分享服务主机）。
// 分享接口的响应外壳与个人云不同（数据直接挂在顶层），故直接解码为 map。
func (d *Driver) shareRequest(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	var raw map[string]any
	if err := d.signedRequest(ctx, shareAPIBase+path, body, &raw); err != nil {
		return nil, domain.Wrap(domain.CodeDriverError, err)
	}
	return raw, nil
}

// share139Paths 从分享信息里取出文件/目录的转存 path。
func share139Paths(info map[string]any) (coPaths, caPaths []string) {
	collect := func(key string) []string {
		list, ok := info[key].([]any)
		if !ok {
			return nil
		}
		out := make([]string, 0, len(list))
		for _, entry := range list {
			item, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if p := strings.TrimSpace(stringValue139(item["path"])); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return collect("coLst"), collect("caLst")
}

// parse139ShareURL 解析移动云盘分享链接：linkID + 提取码。
// 支持 https://yun.139.com/shareweb/#/w/i/{linkID}、...?pwd=xxxx、短链等写法。
func parse139ShareURL(rawURL, explicitPwd string) (linkID, passwd string) {
	passwd = strings.TrimSpace(explicitPwd)
	link := strings.TrimSpace(rawURL)
	if link == "" {
		return "", passwd
	}
	if idx := strings.Index(link, "http"); idx > 0 {
		link = link[idx:]
	}
	if passwd == "" {
		for _, marker := range []string{"提取码", "密码", "访问码", "pwd", "Pwd"} {
			idx := strings.Index(link, marker)
			if idx < 0 {
				continue
			}
			rest := strings.TrimLeft(link[idx+len(marker):], "：:，, 	=")
			fields := strings.FieldsFunc(rest, func(r rune) bool {
				return r == ' ' || r == '\t' || r == '\n' || r == '，' || r == ','
			})
			if len(fields) > 0 {
				passwd = strings.TrimSpace(fields[0])
			}
			link = link[:idx]
			break
		}
	}
	// 兼容 #/w/i/{id} 形式：先截掉 ? 之前的部分再取末段
	if idx := strings.Index(link, "?"); idx >= 0 {
		if passwd == "" {
			if u, err := url.Parse(link); err == nil {
				q := u.Query()
				passwd = firstNonEmpty139(q.Get("pwd"), q.Get("password"), q.Get("code"))
			}
		}
		link = link[:idx]
	}
	// URL 可能被散文包裹（「分享给你 https://… 快去」），先截到链接本体。
	if idx := strings.IndexFunc(link, share139URLBoundary); idx >= 0 {
		link = link[:idx]
	}
	if idx := strings.Index(link, "#"); idx >= 0 {
		link = link[idx+1:]
	}
	// 标准形式：{host}/shareweb/#/w/i/{linkID} 或 {host}/w/i/{linkID}
	// linkID 位于 "w/i/" 之后，按该标记取后续段，避免误取路径里的 "i"。
	if idx := strings.LastIndex(link, "w/i/"); idx >= 0 {
		seg := strings.TrimSpace(link[idx+len("w/i/"):])
		seg = strings.TrimSuffix(strings.TrimSuffix(seg, "/"), ".html")
		for _, sep := range []string{"/", "?", "&", "="} {
			if p := strings.Index(seg, sep); p >= 0 {
				seg = seg[:p]
			}
		}
		if seg = strings.TrimSpace(seg); seg != "" {
			return seg, passwd
		}
	}
	for _, sep := range []string{"/", "="} {
		parts := strings.Split(link, sep)
		for i := len(parts) - 1; i >= 0; i-- {
			seg := strings.TrimSpace(parts[i])
			if seg == "" || strings.HasPrefix(seg, "http") {
				continue
			}
			linkID = seg
			break
		}
		if linkID != "" {
			break
		}
	}
	return linkID, passwd
}

// share139Msisdn 账号数字形式（服务端接受数字或字符串）
func share139Msisdn(account string) any {
	account = strings.TrimSpace(account)
	if account == "" {
		return ""
	}
	if n, err := strconv.ParseInt(account, 10, 64); err == nil {
		return n
	}
	return account
}

// stringValue139 宽松字符串取值
func stringValue139(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case bool:
		if value {
			return "true"
		}
		return "false"
	}
	return ""
}

// firstNonEmpty139 首个非空字符串
func firstNonEmpty139(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// share139URLBoundary 判定 URL 结束的边界字符（空白/中文标点），
// 用于剥掉分享文本里粘在链接尾部的说明文字。
func share139URLBoundary(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '，', ',', '。', '；', ';', '！', '!', '、', '）', '】':
		return true
	}
	return false
}
