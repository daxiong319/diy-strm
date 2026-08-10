package pan139

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ShareAPIBase 分享接口域名（固定，无需 mcloud-sign）
const ShareAPIBase = "https://share-kd-njs.yun.139.com"

// shareHeaders 分享接口请求头（参考云盘 Web 端）
func shareHeaders() map[string]string {
	return map[string]string{
		"Accept":         "application/json, text/plain, */*",
		"Content-Type":   "application/json",
		"Caller":         "web",
		"x-m4c-caller":   "PC",
		"Mcloud-Client":  "10701",
		"Mcloud-Version": "7.17.2",
		"Mcloud-Channel": "1000101",
		"Origin":         "https://yun.139.com",
		"Referer":        "https://yun.139.com/",
	}
}

// ShareItem 分享链接中的文件或目录项
type ShareItem struct {
	CatalogID  string `json:"catalogID"` // 目录 ID（caLst 项）
	CaName     string `json:"caName"`    // 目录名
	ContentID  string `json:"contentID"` // 文件 ID（coLst 项）
	CoName     string `json:"coName"`    // 文件名
	CoSize     int64  `json:"coSize"`    // 文件大小
	UpdateTime string `json:"updateTime"`
	Path       string `json:"path"` // 转存用 path（parentID/fileID）
}

// IsDir 判断是否为目录
func (s ShareItem) IsDir() bool {
	return s.CatalogID != ""
}

// ShareInfo 分享链接信息
type ShareInfo struct {
	NodNum     int         `json:"nodNum"`
	LinkName   string      `json:"lkName"`
	ExpireTime string      `json:"expireTime"`
	FileList   []ShareItem `json:"coLst"`
	FolderList []ShareItem `json:"caLst"`
}

// sharePost 发送分享接口请求
func (c *Client) sharePost(ctx context.Context, path string, body map[string]interface{}) (map[string]interface{}, error) {
	req := c.client.R().
		SetContext(ctx).
		SetHeaders(shareHeaders()).
		SetBody(body)
	var raw map[string]interface{}
	res, err := req.SetResult(&raw).Post(ShareAPIBase + path)
	if err != nil {
		return nil, fmt.Errorf("中国移动云盘分享接口请求失败 %s：%v", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode() >= 400 {
		bodyText, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("中国移动云盘分享接口请求失败 %s：status=%d body=%s", path, res.StatusCode(), string(bodyText))
	}
	code := fmt.Sprintf("%v", raw["code"])
	if code != "" && code != "0" {
		return nil, fmt.Errorf("中国移动云盘分享接口错误 [%s]：%v", code, raw["desc"])
	}
	data, _ := raw["data"].(map[string]interface{})
	if data == nil {
		data = raw
	}
	return data, nil
}

func toShareItems(list []interface{}) []ShareItem {
	items := make([]ShareItem, 0, len(list))
	for _, raw := range list {
		b, _ := json.Marshal(raw)
		var item ShareItem
		if err := json.Unmarshal(b, &item); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items
}

// GetShareInfo 获取分享链接指定目录的信息（分页）
// pCaID 为目录 ID（根目录传 "root"）；startNum/endNum 为 1 起始闭区间
func (c *Client) GetShareInfo(ctx context.Context, linkID, passwd, pCaID string, startNum, endNum int) (*ShareInfo, error) {
	if strings.TrimSpace(pCaID) == "" {
		pCaID = "root"
	}
	account := c.GetAccount()
	data, err := c.sharePost(ctx, "/yun-share/richlifeApp/devapp/IOutLink/getOutLinkInfoV6", map[string]interface{}{
		"getOutLinkInfoReq": map[string]interface{}{
			"account": account,
			"linkID":  strings.TrimSpace(linkID),
			"passwd":  strings.TrimSpace(passwd),
			"pCaID":   pCaID,
			"caSrt":   0,
			"coSrt":   0,
			"srtDr":   1,
			"bNum":    startNum,
			"eNum":    endNum,
		},
	})
	if err != nil {
		return nil, err
	}
	info := &ShareInfo{
		NodNum:     int(toInt64(data["nodNum"])),
		LinkName:   fmt.Sprintf("%v", data["lkName"]),
		ExpireTime: fmt.Sprintf("%v", data["expireTime"]),
	}
	if list, ok := data["coLst"].([]interface{}); ok {
		info.FileList = toShareItems(list)
	}
	if list, ok := data["caLst"].([]interface{}); ok {
		info.FolderList = toShareItems(list)
	}
	return info, nil
}

func toInt64(v interface{}) int64 {
	switch value := v.(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case string:
		parsed, _ := strconv.ParseInt(value, 10, 64)
		return parsed
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	}
	return 0
}

// ListShareDir 列出分享目录（文件+文件夹）
func (c *Client) ListShareDir(ctx context.Context, linkID, passwd, pCaID string) (*ShareInfo, error) {
	return c.GetShareInfo(ctx, linkID, passwd, pCaID, 1, 200)
}

// SaveShareFiles 把分享链接中的文件/目录转存到目标目录，返回转存任务 ID
// coPathLst/caPathLst 为分享项列表接口返回的 path（parentID/fileID）
func (c *Client) SaveShareFiles(ctx context.Context, linkID, passwd, targetCatalogID string, coPathLst, caPathLst []string) (string, error) {
	if strings.TrimSpace(linkID) == "" {
		return "", fmt.Errorf("分享链接 ID 为空")
	}
	data, err := c.sharePost(ctx, "/yun-share/richlifeApp/devapp/IBatchOprTask/createOuterLinkBatchOprTask", map[string]interface{}{
		"createOuterLinkBatchOprTaskReq": map[string]interface{}{
			"msisdn":       c.GetAccount(),
			"ownerAccount": "",
			"taskType":     1,
			"linkID":       strings.TrimSpace(linkID),
			"needPassword": strings.TrimSpace(passwd) != "",
			"taskInfo": map[string]interface{}{
				"linkID":          strings.TrimSpace(linkID),
				"needPassword":    strings.TrimSpace(passwd) != "",
				"contentInfoList": coPathLst,
				"catalogInfoList": caPathLst,
				"newCatalogID":    strings.TrimSpace(targetCatalogID),
			},
		},
	})
	if err != nil {
		return "", err
	}
	taskID := fmt.Sprintf("%v", data["taskID"])
	if taskID == "" || taskID == "<nil>" {
		if nested, ok := data["createOuterLinkBatchOprTaskRes"].(map[string]interface{}); ok {
			taskID = fmt.Sprintf("%v", nested["taskID"])
		}
	}
	if taskID == "" || taskID == "<nil>" {
		return "", fmt.Errorf("中国移动云盘转存成功但未返回任务 ID")
	}
	return taskID, nil
}

// WaitShareFilesVisible 轮询目标目录，等待转存文件出现（转存任务完成后端无状态查询接口，以此确认完成）
// 最多等待 timeout（默认 30 秒），返回可见文件数
func (c *Client) WaitShareFilesVisible(ctx context.Context, targetCatalogID string, expectedNames []string, timeout time.Duration) (visible int, missing []string, err error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	expect := make(map[string]bool)
	for _, name := range expectedNames {
		if strings.TrimSpace(name) != "" {
			expect[name] = true
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		default:
		}
		files, err := c.GetFiles(ctx, targetCatalogID)
		if err == nil {
			seen := 0
			for _, file := range files {
				if expect[file.FileName] {
					seen++
				}
			}
			if seen >= len(expect) {
				return seen, nil, nil
			}
			if time.Now().After(deadline) {
				missingNames := make([]string, 0)
				for name := range expect {
					found := false
					for _, file := range files {
						if file.FileName == name {
							found = true
							break
						}
					}
					if !found {
						missingNames = append(missingNames, name)
					}
				}
				return seen, missingNames, nil
			}
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return 0, nil, nil
}
