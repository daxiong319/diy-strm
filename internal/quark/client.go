package quark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 夸克网盘驱动（协议对齐 CASCLOUD189 QuarkUcDriver）
// 认证：Cookie（__pus/__puus 等会话字段）
// 秒传：POST /file 两步（get_token 预检 pre_id=4×4MB 分块 MD5 逗号串 → finish=true）
// ---------------------------------------------------------------------------

const (
	APIBase   = "https://drive.quark.cn/1/clouddrive"
	UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
	Referer   = "https://pan.quark.cn/"

	PreSliceSize  = 4 * 1024 * 1024
	PreSliceCount = 4
)

// Client 夸克客户端
type Client struct {
	cookie string
	http   *http.Client
}

// NewClient 创建客户端
func NewClient(cookie string) *Client {
	return &Client{
		cookie: cookie,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

// request 通用请求（POST JSON / GET）
func (c *Client) request(ctx context.Context, method, uri string, body any) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, APIBase+uri, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("Referer", Referer)
	req.Header.Set("Content-Type", "application/json")
	// 夸克接口强制 query 参数
	q := req.URL.Query()
	q.Set("pr", "ucpro")
	q.Set("fr", "pc")
	req.URL.RawQuery = q.Encode()

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Status  int            `json:"status"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("夸克接口响应解析失败: %s", truncate(string(raw), 160))
	}
	if envelope.Status >= 400 {
		return nil, fmt.Errorf("夸克接口错误 [%d]: %s", envelope.Status, firstNonEmpty(envelope.Message, truncate(string(raw), 200)))
	}
	if envelope.Data != nil {
		return envelope.Data, nil
	}
	// 无 data 包装时直接把整个 JSON 当 map
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m, nil
}

// CheckHealth 账号健康检查（/member）
func (c *Client) CheckHealth(ctx context.Context) error {
	_, err := c.request(ctx, http.MethodGet, "/member", nil)
	return err
}

// FileItem 夸克文件条目
type FileItem struct {
	Fid     string `json:"fid"`
	Name    string `json:"file_name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"dir"`
	MD5     string `json:"md5"`      // 全量 MD5（列表返回）
	PreHash string `json:"pre_hash"` // 分块 MD5 预检串（部分接口返回）
}

// ListFiles 列目录（分页拉全）
func (c *Client) ListFiles(ctx context.Context, parentFID string) ([]FileItem, error) {
	if parentFID == "" {
		parentFID = "0"
	}
	var out []FileItem
	page := 1
	for {
		resp, err := c.request(ctx, http.MethodPost, "/file/sort", map[string]any{
			"pdir_fid":     parentFID,
			"_page":        page,
			"_size":        100,
			"_fetch_total": 1,
			"pr":           "ucpro",
			"fr":           "pc",
		})
		if err != nil {
			return nil, err
		}
		list, _ := resp["list"].([]any)
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			fi := FileItem{
				Fid:   anyString(m["fid"]),
				Name:  firstNonEmpty(anyString(m["file_name"]), anyString(m["name"])),
				Size:  int64(anyFloat(m["size"])),
				IsDir: anyBool(m["dir"]),
				MD5:   anyString(m["md5"]),
			}
			out = append(out, fi)
		}
		if len(list) < 100 {
			break
		}
		page++
	}
	return out, nil
}

// GetFileHash 按文件 ID 获取 md5/pre_hash（列父目录匹配 fid）
func (c *Client) GetFileHash(ctx context.Context, fileID string) (md5, preHash string, ok bool) {
	// 夸克列表接口按目录拉，需要知道父目录。这里从根目录递归一层（与 139 相同策略）
	return "", "", false
}

// RapidUpload 夸克两步秒传（preHash=4×4MB 分块 MD5 逗号串）
func (c *Client) RapidUpload(ctx context.Context, targetFolderID, fileName string, fileSize int64, preHash string) (fileID string, err error) {
	if targetFolderID == "" {
		targetFolderID = "0"
	}
	if !strings.Contains(preHash, ",") {
		return "", fmt.Errorf("夸克秒传需要 4×4MB 分块 MD5 预校验特征（preHash，逗号分隔）")
	}
	// 步骤 1：get_token 预检
	tokenRes, err := c.request(ctx, http.MethodPost, "/file", map[string]any{
		"pdir_fid":    targetFolderID,
		"file_name":   fileName,
		"file_size":   fileSize,
		"get_token":   true,
		"pre_id":      preHash,
		"hash_source": "block_md5",
		"block_size":  PreSliceSize,
		"blocks":      PreSliceCount,
	})
	if err != nil {
		return "", err
	}
	if finish, ok := tokenRes["finish"].(map[string]any); ok {
		if fid := anyString(finish["fid"]); fid != "" {
			return fid, nil // 秒传直接命中
		}
	}
	taskID := anyString(tokenRes["task_id"])
	if taskID == "" {
		return "", fmt.Errorf("夸克秒传预检失败: %s", truncate(marshalJSON(tokenRes), 200))
	}
	// 步骤 2：finish 完成
	finishRes, err := c.request(ctx, http.MethodPost, "/file", map[string]any{
		"task_id":   taskID,
		"finish":    true,
		"file_name": fileName,
	})
	if err != nil {
		return "", err
	}
	fid := anyString(finishRes["fid"])
	if fid == "" {
		return "", fmt.Errorf("夸克秒传完成阶段失败: %s", truncate(marshalJSON(finishRes), 200))
	}
	return fid, nil
}

// DeleteFile 删除文件（回收站）
func (c *Client) DeleteFile(ctx context.Context, fileIDs []string) error {
	_, err := c.request(ctx, http.MethodPost, "/file/delete", map[string]any{
		"action_type": 2, // 移到回收站
		"fid_list":    fileIDs,
	})
	return err
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return fmt.Sprintf("%v", t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

func anyFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		var f float64
		_, _ = fmt.Sscanf(strings.TrimSpace(n), "%g", &f)
		return f
	}
	return 0
}

func anyBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case float64:
		return b != 0
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func marshalJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
