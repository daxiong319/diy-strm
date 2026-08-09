package guangyapan

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DeveloperAPIBase 光鸭官方开发者接口地址
const DeveloperAPIBase = "https://dapi.guangyapan.com"

// 开发者接口业务码
const (
	DeveloperCodeOK            = 0
	DeveloperCodeInvalidToken  = 18001 // TOKEN 不存在
	DeveloperCodeTokenBound    = 18002 // TOKEN 已绑定其他上传者
	DeveloperCodeSameAccount   = 18003 // 上传者与接收者相同
	DeveloperCodeNotOwner      = 18006 // 文件不属于当前开发者
	DeveloperCodeNoSpace       = 18007 // 接收空间不足
	DeveloperCodeDirMissing    = 18008 // 目标目录不存在
	DeveloperCodeRateLimited   = 18010 // 频率限制
	DeveloperCodeNeedPreAudit  = 18011 // 暂无已通过预审的文件
	DeveloperCodeTooManyItems  = 18012 // 超过 20 项
	DeveloperCodeBusy          = 18013 // 服务繁忙
	DeveloperCodeAlreadySent   = 18014 // 文件已传过
	DeveloperCodeBadCredential = 18020 // 凭据无效
	DeveloperCodeSignFailed    = 18021 // 签名失败
	DeveloperCodeSignExpired   = 18022 // 签名过期
	DeveloperCodeNonceReused   = 18023 // nonce 重用
	DeveloperCodeNotGranted    = 18025 // 接口未授权
	DeveloperCodeRestricted    = 18026 // 开发者受限
)

// DeveloperMaxItems 单次小号秒传最大文件数
const DeveloperMaxItems = 20

// DeveloperAPIError 开发者接口业务错误
type DeveloperAPIError struct {
	Code   int
	Status int
	Msg    string
}

func (e *DeveloperAPIError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("开发者接口失败（业务码 %d）：%s", e.Code, e.Msg)
	}
	return fmt.Sprintf("开发者接口失败（HTTP %d）：%s", e.Status, e.Msg)
}

// retryable 判断是否可重试
func (e *DeveloperAPIError) retryable() bool {
	return e.Code == DeveloperCodeRateLimited || e.Code == DeveloperCodeBusy ||
		e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Retryable 判断是否可重试（跨包使用）
func (e *DeveloperAPIError) Retryable() bool {
	return e.retryable()
}

// DeveloperResponse 开发者接口统一响应
type DeveloperResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// DeveloperClient 光鸭官方开发者接口客户端（client_id/client_secret 签名）
type DeveloperClient struct {
	clientID     string
	clientSecret string
	apiBase      string
	httpClient   *http.Client
}

// NewDeveloperClient 创建开发者客户端
func NewDeveloperClient(clientID, clientSecret string) *DeveloperClient {
	return &DeveloperClient{
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		apiBase:      DeveloperAPIBase,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// SetBaseURL 注入 API 地址（测试用）
func (c *DeveloperClient) SetBaseURL(apiBase string) {
	c.apiBase = strings.TrimRight(apiBase, "/")
}

// buildHeaders 构造签名请求头：MD5(client_id&client_secret&nonce&timestamp) 再 SHA512 十六进制
func (c *DeveloperClient) buildHeaders(nonce, timestamp string) map[string]string {
	source := fmt.Sprintf("client_id=%s&client_secret=%s&nonce=%s&timestamp=%s", c.clientID, c.clientSecret, nonce, timestamp)
	md5Sum := md5.Sum([]byte(source))
	sign := sha512.Sum512(md5Sum[:])
	return map[string]string{
		"content-type": "application/json",
		"client_id":    c.clientID,
		"nonce":        nonce,
		"timestamp":    timestamp,
		"sign":         hex.EncodeToString(sign[:]),
	}
}

func randomNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano()%999999999999999)
	}
	return hex.EncodeToString(b)
}

// post 发送签名 POST 请求并统一处理业务错误。
// 返回原始响应与 data 原始字节。
func (c *DeveloperClient) post(ctx context.Context, endpoint string, body map[string]interface{}) (*DeveloperResponse, error) {
	if c.clientID == "" || c.clientSecret == "" {
		return nil, errors.New("请先配置光鸭开发者 client_id 和 client_secret")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+endpoint, strings.NewReader(string(payload)))
		if err != nil {
			return nil, err
		}
		nonce := randomNonce()
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		for k, v := range c.buildHeaders(nonce, timestamp) {
			req.Header.Set(k, v)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("无法连接开发者接口 %s：%v", endpoint, err)
		}
		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("读取开发者接口响应失败：%v", readErr)
		}
		var out DeveloperResponse
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &out); err != nil {
				return nil, fmt.Errorf("开发者接口 %s 返回非 JSON 响应：%v", endpoint, err)
			}
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && out.Code == DeveloperCodeOK {
			return &out, nil
		}
		apiErr := &DeveloperAPIError{
			Code:   out.Code,
			Status: resp.StatusCode,
			Msg:    out.Msg,
		}
		if apiErr.retryable() && attempt < 2 {
			delay := 2 * time.Second * time.Duration(attempt+1)
			if apiErr.Code == DeveloperCodeRateLimited {
				delay = 60 * time.Second
			}
			lastErr = apiErr
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		return nil, apiErr
	}
	return nil, lastErr
}

func extractTaskID(data json.RawMessage) string {
	var fields map[string]interface{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return ""
	}
	for _, key := range []string{"task_id", "taskId"} {
		if value, ok := fields[key]; ok {
			return strings.TrimSpace(fmt.Sprintf("%v", value))
		}
	}
	return ""
}

// UploadByFileID 提交小号秒传：把当前开发者账号的文件秒传到接收 TOKEN 授权目录。
// 返回上传任务 ID；文件未过审返回 DeveloperCodeNeedPreAudit。
func (c *DeveloperClient) UploadByFileID(ctx context.Context, tokenID string, fileIDs []string) (string, error) {
	if len(fileIDs) == 0 {
		return "", errors.New("未选择文件")
	}
	if len(fileIDs) > DeveloperMaxItems {
		return "", fmt.Errorf("单次最多 %d 个文件", DeveloperMaxItems)
	}
	resp, err := c.post(ctx, "/developer/v1/upload_by_fileid", map[string]interface{}{
		"token_id": tokenID,
		"file_ids": fileIDs,
	})
	if err != nil {
		return "", err
	}
	taskID := extractTaskID(resp.Data)
	if taskID == "" {
		return "", errors.New("开发者接口没有返回秒传任务 ID")
	}
	return taskID, nil
}

// PreUpload 提交文件预审（未过审文件需要预审后才能秒传）。
func (c *DeveloperClient) PreUpload(ctx context.Context, tokenID string, fileIDs []string) (string, error) {
	resp, err := c.post(ctx, "/developer/v1/pre_upload", map[string]interface{}{
		"token_id": tokenID,
		"file_ids": fileIDs,
	})
	if err != nil {
		return "", err
	}
	taskID := extractTaskID(resp.Data)
	if taskID == "" {
		return "", errors.New("开发者接口没有返回预审任务 ID")
	}
	return taskID, nil
}

// UploadStatusData upload_status 查询结果
type UploadStatusData struct {
	Status        string // success / failed / 其他
	TotalCount    int
	PassedCount   int
	RejectedCount int
	PendingCount  int
	SuccessCount  int
	SkippedCount  int
	FailedCount   int
}

// UploadStatus 查询小号秒传任务状态。
func (c *DeveloperClient) UploadStatus(ctx context.Context, taskID string) (*UploadStatusData, error) {
	resp, err := c.post(ctx, "/developer/v1/upload_status", map[string]interface{}{
		"task_id": taskID,
	})
	if err != nil {
		return nil, err
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(resp.Data, &fields); err != nil {
		return nil, fmt.Errorf("解析上传状态失败：%v", err)
	}
	out := &UploadStatusData{}
	toInt := func(keys ...string) int {
		for _, key := range keys {
			if value, ok := fields[key]; ok && value != nil && value != "" {
				if parsed, err := strconv.Atoi(fmt.Sprintf("%v", value)); err == nil && parsed > 0 {
					return parsed
				}
			}
		}
		return 0
	}
	out.Status = strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(fields, "status"))))
	out.TotalCount = toInt("total_count", "totalCount")
	out.PassedCount = toInt("passed_count", "passedCount")
	out.RejectedCount = toInt("rejected_count", "rejectedCount")
	out.PendingCount = toInt("pending_count", "pendingCount")
	out.SuccessCount = toInt("success_count", "successCount", "use_count", "useCount")
	out.SkippedCount = toInt("skipped_count", "skippedCount")
	out.FailedCount = toInt("failed_count", "failedCount", "rejected_count", "rejectedCount")
	return out, nil
}

func firstNonNil(m map[string]interface{}, key string) interface{} {
	if value, ok := m[key]; ok {
		return value
	}
	return ""
}

// PreUploadStatus 查询预审状态：0 未开始 / 1 提交中 / 2 审核中 / 3 完成 / 4 失败。
func (c *DeveloperClient) PreUploadStatus(ctx context.Context, taskID string) (int, string, error) {
	resp, err := c.post(ctx, "/developer/v1/pre_upload_status", map[string]interface{}{
		"task_id": taskID,
	})
	if err != nil {
		return 0, "", err
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(resp.Data, &fields); err != nil {
		return 0, "", fmt.Errorf("解析预审状态失败：%v", err)
	}
	status := 0
	if value, ok := firstNonNil(fields, "status").(float64); ok {
		status = int(value)
	} else if parsed, err := strconv.Atoi(fmt.Sprintf("%v", firstNonNil(fields, "status"))); err == nil {
		status = parsed
	}
	msg := strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(fields, "message")))
	return status, msg, nil
}
