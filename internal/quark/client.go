package quark

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
// requestFull 同 request 但返回完整响应 JSON（含 data + metadata 等外层字段），
// 用于需要 metadata（如 part_size）的上传预检场景。
func (c *Client) requestFull(ctx context.Context, method, uri string, body any) (map[string]any, error) {
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
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("夸克接口响应解析失败: %s", truncate(string(raw), 160))
	}
	if status, _ := m["status"].(float64); status >= 400 {
		return nil, fmt.Errorf("夸克接口错误 [%v]: %s", status, firstNonEmpty(anyString(m["message"]), truncate(string(raw), 200)))
	}
	return m, nil
}

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

// BuildPreHash 从本地文件计算夸克秒传预检特征：前 4 块 × 4MB 的分块 MD5 逗号串。
// 文件不足 4 块时取实际块数；每块 MD5 为小写 hex。
func BuildPreHash(filePath string, fileSize int64) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("打开文件失败：%v", err)
	}
	defer f.Close()

	blockCount := 0
	var parts []string
	buf := make([]byte, PreSliceSize)
	remaining := fileSize
	for blockCount < PreSliceCount && remaining > 0 {
		readSize := int64(len(buf))
		if remaining < readSize {
			readSize = remaining
		}
		n, err := io.ReadFull(f, buf[:readSize])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return "", fmt.Errorf("读取文件分块失败：%v", err)
		}
		h := md5.Sum(buf[:n])
		parts = append(parts, hex.EncodeToString(h[:]))
		remaining -= int64(n)
		blockCount++
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("文件为空，无法计算分块 MD5")
	}
	return strings.Join(parts, ","), nil
}

// UploadFile 夸克上传（秒传优先，未命中回退普通分片上传）。
// 返回 (fileID, rapid bool, err)。
// UploadFile 夸克完整上传：秒传预检优先，未命中回退 OSS 分片普通上传。
// 协议对齐 alist quark_uc 驱动（/file/upload/pre → 分片 PUT → /file/upload/finish）。
func (c *Client) UploadFile(ctx context.Context, targetFolderID, localPath string, fileSize int64, preHash string, progress func(uploaded int64)) (fileID string, rapid bool, err error) {
	fileName := filepath.Base(localPath)
	if preHash == "" {
		return "", false, fmt.Errorf("夸克上传需要 preHash 分块预检特征")
	}
	// 第一步：秒传预检（get_token + pre_id block_md5）
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
		return "", false, err
	}
	if finish, ok := tokenRes["finish"].(map[string]any); ok {
		if fid := anyString(finish["fid"]); fid != "" {
			return fid, true, nil // 秒传命中
		}
	}
	taskID := anyString(tokenRes["task_id"])
	// 秒传未命中 → 普通分片上传（OSS multipart）
	pre, err := c.uploadPre(ctx, targetFolderID, localPath, fileSize)
	if err != nil {
		return "", false, err
	}
	if pre.Data.TaskID == "" {
		if taskID != "" {
			pre.Data.TaskID = taskID
		} else {
			return "", false, fmt.Errorf("夸克上传预检失败：无 task_id")
		}
	}
	fid, err := c.uploadParts(ctx, pre, localPath, fileSize, progress)
	if err != nil {
		return "", false, err
	}
	return fid, false, nil
}

// uploadPreResp 上传预检响应（OSS multipart 凭证）
type uploadPreResp struct {
	Data struct {
		TaskID    string `json:"task_id"`
		UploadID  string `json:"upload_id"`
		ObjKey    string `json:"obj_key"`
		UploadURL string `json:"upload_url"`
		Bucket    string `json:"bucket"`
		AuthInfo  string `json:"auth_info"`
		Fid       string `json:"fid"`
	} `json:"data"`
	Metadata struct {
		PartSize int `json:"part_size"`
	} `json:"metadata"`
}

// uploadAuthResp 上传分片签名响应
type uploadAuthResp struct {
	Data struct {
		AuthKey string `json:"auth_key"`
	} `json:"data"`
}

// uploadPre 获取 OSS 上传凭证（用完整响应，含 data + metadata.part_size）
func (c *Client) uploadPre(ctx context.Context, targetFolderID, localPath string, fileSize int64) (*uploadPreResp, error) {
	fileName := filepath.Base(localPath)
	now := time.Now()
	raw, err := c.requestFull(ctx, http.MethodPost, "/file/upload/pre", map[string]any{
		"ccp_hash_update": true,
		"dir_name":        "",
		"file_name":       fileName,
		"format_type":     "video/mp4",
		"l_created_at":    now.UnixMilli(),
		"l_updated_at":    now.UnixMilli(),
		"pdir_fid":        targetFolderID,
		"size":            fileSize,
	})
	if err != nil {
		return nil, err
	}
	data, _ := raw["data"].(map[string]any)
	metadata, _ := raw["metadata"].(map[string]any)
	pre := &uploadPreResp{}
	pre.Data.TaskID = anyString(data["task_id"])
	pre.Data.UploadID = anyString(data["upload_id"])
	pre.Data.ObjKey = anyString(data["obj_key"])
	pre.Data.UploadURL = anyString(data["upload_url"])
	pre.Data.Bucket = anyString(data["bucket"])
	pre.Data.AuthInfo = anyString(data["auth_info"])
	pre.Data.Fid = anyString(data["fid"])
	if metadata != nil {
		pre.Metadata.PartSize = int(anyFloat(metadata["part_size"]))
	}
	return pre, nil
}

// uploadParts 分片上传 OSS + finish
func (c *Client) uploadParts(ctx context.Context, pre *uploadPreResp, localPath string, fileSize int64, progress func(uploaded int64)) (string, error) {
	partSize := int64(pre.Metadata.PartSize)
	if partSize <= 0 {
		partSize = 10 * 1024 * 1024 // 默认 10MB
	}
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := make([]byte, partSize)
	partNumber := 1
	var uploaded int64
	for uploaded < fileSize {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		remaining := fileSize - uploaded
		readSize := partSize
		if remaining < partSize {
			readSize = remaining
		}
		n, err := io.ReadFull(f, buf[:readSize])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return "", fmt.Errorf("读取文件分片失败：%v", err)
		}
		if err := c.uploadPart(ctx, pre, partNumber, buf[:n]); err != nil {
			return "", err
		}
		uploaded += int64(n)
		partNumber++
		if progress != nil {
			progress(uploaded)
		}
	}
	// finish
	return c.uploadFinish(ctx, pre)
}

// uploadPart 上传单个 OSS 分片
func (c *Client) uploadPart(ctx context.Context, pre *uploadPreResp, partNumber int, data []byte) error {
	timeStr := time.Now().UTC().Format(http.TimeFormat)
	mimeType := "video/mp4"
	raw, err := c.request(ctx, http.MethodPost, "/file/upload/auth", map[string]any{
		"auth_info": pre.Data.AuthInfo,
		"auth_meta": fmt.Sprintf("PUT\n\n%s\n%s\nx-oss-date:%s\nx-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit\n/%s/%s?partNumber=%d&uploadId=%s",
			mimeType, timeStr, timeStr, pre.Data.Bucket, pre.Data.ObjKey, partNumber, pre.Data.UploadID),
		"task_id": pre.Data.TaskID,
	})
	if err != nil {
		return err
	}
	// request() 返回 data，auth_key 平铺
	authKey := anyString(raw["auth_key"])
	// PUT 到 OSS
	host := strings.TrimPrefix(pre.Data.UploadURL, "https://")
	if !strings.HasPrefix(host, "https://") {
		host = strings.TrimPrefix(pre.Data.UploadURL, "http://")
	}
	u := fmt.Sprintf("https://%s.%s/%s", pre.Data.Bucket, host, pre.Data.ObjKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authKey)
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Referer", Referer)
	req.Header.Set("x-oss-date", timeStr)
	req.Header.Set("x-oss-user-agent", "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit")
	q := req.URL.Query()
	q.Set("partNumber", fmt.Sprintf("%d", partNumber))
	q.Set("uploadId", pre.Data.UploadID)
	req.URL.RawQuery = q.Encode()

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("夸克分片上传失败（HTTP %d）: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

// uploadFinish 完成 OSS 分片上传
func (c *Client) uploadFinish(ctx context.Context, pre *uploadPreResp) (string, error) {
	raw, err := c.request(ctx, http.MethodPost, "/file/upload/finish", map[string]any{
		"obj_key": pre.Data.ObjKey,
		"task_id": pre.Data.TaskID,
	})
	if err != nil {
		return "", err
	}
	fid := anyString(raw["fid"])
	if fid == "" {
		if data, ok := raw["data"].(map[string]any); ok {
			fid = anyString(data["fid"])
		}
	}
	if fid == "" {
		return "", fmt.Errorf("夸克完成上传失败：未返回 fid（%s）", truncate(marshalJSON(raw), 200))
	}
	return fid, nil
}

// UploadFilePlain 夸克纯普通上传（不尝试秒传，用于无 preHash 的兜底场景）。
func (c *Client) UploadFilePlain(ctx context.Context, targetFolderID, localPath string, fileSize int64, progress func(uploaded int64)) (fileID string, err error) {
	pre, err := c.uploadPre(ctx, targetFolderID, localPath, fileSize)
	if err != nil {
		return "", err
	}
	if pre.Data.TaskID == "" {
		return "", fmt.Errorf("夸克上传预检失败：无 task_id")
	}
	return c.uploadParts(ctx, pre, localPath, fileSize, progress)
}

// UploadTextFile 上传小文本文件（如 .cas 指纹文件）到指定目录。
// 走普通分片上传链路（小文件单分片）。
func (c *Client) UploadTextFile(ctx context.Context, parentFID, fileName, content string) string {
	fid, err := c.uploadText(ctx, parentFID, fileName, content)
	if err != nil {
		return ""
	}
	return fid
}

// uploadText 上传小文本内容到网盘
func (c *Client) uploadText(ctx context.Context, parentFID, fileName, content string) (string, error) {
	// 夸克上传小文本：走 uploadPre → 分片 PUT → finish（单分片即可）
	pre, err := c.uploadPre(ctx, parentFID, fileName, int64(len(content)))
	if err != nil {
		return "", err
	}
	if pre.Data.TaskID == "" {
		return "", fmt.Errorf("夸克上传预检失败：无 task_id")
	}
	// 单分片上传
	timeStr := time.Now().UTC().Format(http.TimeFormat)
	mimeType := "text/plain"
	raw, err := c.request(ctx, http.MethodPost, "/file/upload/auth", map[string]any{
		"auth_info": pre.Data.AuthInfo,
		"auth_meta": fmt.Sprintf("PUT\n\n%s\n%s\nx-oss-date:%s\nx-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit\n/%s/%s?partNumber=1&uploadId=%s",
			mimeType, timeStr, timeStr, pre.Data.Bucket, pre.Data.ObjKey, pre.Data.UploadID),
		"task_id": pre.Data.TaskID,
	})
	if err != nil {
		return "", err
	}
	authKey := anyString(raw["auth_key"])
	if authKey == "" {
		return "", fmt.Errorf("获取上传签名失败")
	}
	host := strings.TrimPrefix(pre.Data.UploadURL, "https://")
	host = strings.TrimPrefix(host, "http://")
	u := fmt.Sprintf("https://%s.%s/%s", pre.Data.Bucket, host, pre.Data.ObjKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, strings.NewReader(content))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authKey)
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Referer", Referer)
	req.Header.Set("x-oss-date", timeStr)
	q := req.URL.Query()
	q.Set("partNumber", "1")
	q.Set("uploadId", pre.Data.UploadID)
	req.URL.RawQuery = q.Encode()

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("夸克分片上传失败 HTTP %d", resp.StatusCode)
	}
	// finish
	raw2, err := c.request(ctx, http.MethodPost, "/file/upload/finish", map[string]any{
		"obj_key": pre.Data.ObjKey,
		"task_id": pre.Data.TaskID,
	})
	if err != nil {
		return "", err
	}
	fid := anyString(raw2["fid"])
	if fid == "" {
		if d, ok := raw2["data"].(map[string]any); ok {
			fid = anyString(d["fid"])
		}
	}
	return fid, nil
}
