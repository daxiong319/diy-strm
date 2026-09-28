package cloud189

import (
	"context"
	"crypto/aes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 三步秒传（对齐 Cloud189RapidUploadService）
// initMultiUpload → checkTransSecond → commitMultiUploadFile
// ---------------------------------------------------------------------------

type rsaKeyEntry struct {
	PubKey   string
	PkID     string
	ExpireAt time.Time
}

var (
	rsaKeyCache = map[string]*rsaKeyEntry{}
	rsaKeyMu    sync.Mutex
)

// aesEncryptECB AES-128-ECB 加密（对齐 SDK aesEncrypt：utf8 key、hex 大写输出）
func aesEncryptECB(plainText, keyHex string) (string, error) {
	key := []byte(keyHex)
	if len(key) != 16 {
		return "", fmt.Errorf("AES key 必须 16 字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	// ECB 模式逐块加密（PKCS7 填充）
	data := pkcs7Pad([]byte(plainText), aes.BlockSize)
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
	}
	return strings.ToUpper(hex.EncodeToString(out)), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	if pad == 0 {
		pad = blockSize
	}
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

// generateRsaKey 获取上传 RSA 密钥（30 分钟缓存）
func generateRsaKey(ctx context.Context, cacheKey string, client *http.Client) (*rsaKeyEntry, error) {
	rsaKeyMu.Lock()
	defer rsaKeyMu.Unlock()
	if e, ok := rsaKeyCache[cacheKey]; ok && time.Now().Before(e.ExpireAt) {
		return e, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, WebURL+"/api/security/generateRsaKey.action", nil)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	pubKey, pkID, err := parseRsaKeyResponse(string(body))
	if err != nil {
		return nil, err
	}
	e := &rsaKeyEntry{
		PubKey:   fmt.Sprintf("-----BEGIN PUBLIC KEY-----\n%s\n-----END PUBLIC KEY-----", pubKey),
		PkID:     pkID,
		ExpireAt: time.Now().Add(30 * time.Minute),
	}
	rsaKeyCache[cacheKey] = e
	return e, nil
}

func parseRsaKeyResponse(s string) (pubKey, pkID string, err error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") {
		var j struct {
			PubKey string `json:"pubKey"`
			PkID   string `json:"pkId"`
		}
		if err := json.Unmarshal([]byte(s), &j); err == nil && j.PubKey != "" {
			clean := strings.NewReplacer("-----BEGIN PUBLIC KEY-----", "", "-----END PUBLIC KEY-----", "", "\n", "", "\r", "", " ", "").Replace(j.PubKey)
			return clean, j.PkID, nil
		}
	}
	// XML 格式
	extract := func(tag string) string {
		start := strings.Index(s, "<"+tag+">")
		end := strings.Index(s, "</"+tag+">")
		if start < 0 || end < 0 || end < start {
			return ""
		}
		return s[start+len(tag)+2 : end]
	}
	pubKey = extract("pubKey")
	pkID = extract("pkId")
	if pubKey == "" || pkID == "" {
		return "", "", fmt.Errorf("获取天翼云 RSA 密钥失败: 未知响应格式: %s", truncateStr(s, 100))
	}
	clean := strings.NewReplacer("-----BEGIN PUBLIC KEY-----", "", "-----END PUBLIC KEY-----", "", "\n", "", "\r", "", " ", "").Replace(pubKey)
	return clean, pkID, nil
}

// buildUploadRequest 构造秒传签名请求（对齐 buildUploadRequest）
func (c *Client) buildUploadRequest(ctx context.Context, requestURI string, params map[string]any, cacheKey string) (url.Values, http.Header, error) {
	sessionKey, err := c.GetSessionKey(ctx)
	if err != nil {
		return nil, nil, err
	}
	rsaKey, err := generateRsaKey(ctx, cacheKey, c.http)
	if err != nil {
		return nil, nil, err
	}

	requestID := randomUUID()
	requestDate := fmt.Sprintf("%d", time.Now().UnixMilli())
	randomAesKey := randomHex8()

	paramsJSON, _ := json.Marshal(params)
	encryptedParams, err := aesEncryptECB(string(paramsJSON), randomAesKey)
	if err != nil {
		return nil, nil, err
	}
	rsaEncryptedKey, err := rsaEncryptBase64(rsaKey.PubKey, randomAesKey)
	if err != nil {
		return nil, nil, err
	}

	hmacData := map[string]string{
		"SessionKey": sessionKey,
		"Operate":    "GET",
		"RequestURI": requestURI,
		"Date":       requestDate,
		"params":     encryptedParams,
	}
	signature := hmacSHA1(hmacData, randomAesKey)

	headers := http.Header{
		"X-Request-Date":       {requestDate},
		"X-Request-ID":         {requestID},
		"EncryptionTextLength": {fmt.Sprintf("%d", len(encryptedParams))},
		"EncryptionTextSha256": {sha256Hex(encryptedParams)},
		"PkId":                 {rsaKey.PkID},
		"Signature":            {signature},
		"User-Agent":           {UserAgent},
	}
	query := url.Values{
		"params":         {encryptedParams},
		"EncryptionText": {rsaEncryptedKey},
	}
	return query, headers, nil
}

// executeUploadRequest 执行秒传接口调用（SessionKey 失效自动重试一次）
func (c *Client) executeUploadRequest(ctx context.Context, requestURI string, params map[string]any, cacheKey string) (map[string]any, error) {
	for attempt := 0; attempt < 2; attempt++ {
		query, headers, err := c.buildUploadRequest(ctx, requestURI, params, cacheKey)
		if err != nil {
			return nil, err
		}
		u := UploadBaseURL + requestURI + "?" + query.Encode()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		for k, vs := range headers {
			for _, v := range vs {
				req.Header.Set(k, v)
			}
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("秒传接口响应解析失败: %s", truncateStr(string(body), 160))
		}
		// 成功判定
		if out["code"] == "SUCCESS" || out["res_code"] == float64(0) || out["resultCode"] == float64(0) || out["data"] != nil || out["fileId"] != nil || out["uploadFileId"] != nil {
			return out, nil
		}
		if msg, _ := out["res_message"].(string); msg != "" {
			if attempt == 0 && (strings.Contains(msg, "SessionKey") || strings.Contains(msg, "KeyInvalid") || strings.Contains(msg, "INVALID_SESSION")) {
				// SessionKey 失效：清缓存重试一次（新架构：清 sessionKey/secret 后 apiRequest 会自动 doRefresh）
				c.mu.Lock()
				c.sessionKey = ""
				c.sessionSecret = ""
				c.mu.Unlock()
				continue
			}
			return nil, fmt.Errorf("天翼云秒传接口错误: %s (%v)", msg, out["res_code"])
		}
		return out, nil
	}
	return nil, fmt.Errorf("秒传请求失败")
}

// RapidUpload 三步秒传（familyId 非空走家庭云）
func (c *Client) RapidUpload(ctx context.Context, parentFolderID, fileName string, fileSize int64, fileMd5, sliceMd5, familyID string) (fileID string, err error) {
	fileMd5 = strings.ToUpper(strings.TrimSpace(fileMd5))
	sliceMd5 = strings.ToUpper(strings.TrimSpace(sliceMd5))
	isFamily := familyID != ""
	cacheKey := "personal"
	if isFamily {
		cacheKey = "family:" + familyID
	}

	// Step 1: initMultiUpload
	initParams := map[string]any{
		"parentFolderId": parentFolderID,
		"fileName":       url.QueryEscape(fileName),
		"fileSize":       fileSize,
		"sliceSize":      10485760, // 10MB
	}
	if isFamily {
		initParams["familyId"] = familyID
	}
	initURI := "/person/initMultiUpload"
	if isFamily {
		initURI = "/family/initMultiUpload"
	}
	initRes, err := c.executeUploadRequest(ctx, initURI, initParams, cacheKey)
	if err != nil {
		return "", fmt.Errorf("初始化秒传失败: %w", err)
	}
	uploadFileID := anyString(initRes["uploadFileId"])
	if uploadFileID == "" {
		if data, ok := initRes["data"].(map[string]any); ok {
			uploadFileID = anyString(data["uploadFileId"])
		}
	}
	if uploadFileID == "" {
		return "", fmt.Errorf("初始化秒传失败: uploadFileId 缺失 (响应: %s)", truncateStr(jsonString(initRes), 200))
	}

	// Step 2: checkTransSecond
	checkParams := map[string]any{
		"fileMd5":      fileMd5,
		"sliceMd5":     sliceMd5,
		"uploadFileId": uploadFileID,
	}
	if isFamily {
		checkParams["familyId"] = familyID
	}
	checkURI := "/person/checkTransSecond"
	if isFamily {
		checkURI = "/family/checkTransSecond"
	}
	checkRes, err := c.executeUploadRequest(ctx, checkURI, checkParams, cacheKey)
	if err != nil {
		return "", fmt.Errorf("秒传特征校验失败: %w", err)
	}
	exists := false
	if v, ok := checkRes["fileDataExists"]; ok {
		exists = v == float64(1) || v == true
	}
	if !exists {
		if data, ok := checkRes["data"].(map[string]any); ok {
			if v, ok2 := data["fileDataExists"]; ok2 {
				exists = v == float64(1) || v == true
			}
		}
	}
	if !exists {
		return "", fmt.Errorf("秒传失败: 云端未命中该文件特征或文件无法秒传 (checkTransSecond=%s)", truncateStr(jsonString(checkRes), 200))
	}

	// Step 3: commitMultiUploadFile
	commitParams := map[string]any{
		"uploadFileId": uploadFileID,
		"fileMd5":      fileMd5,
		"sliceMd5":     sliceMd5,
	}
	if isFamily {
		commitParams["familyId"] = familyID
	}
	commitURI := "/person/commitMultiUploadFile"
	if isFamily {
		commitURI = "/family/commitMultiUploadFile"
	}
	commitRes, err := c.executeUploadRequest(ctx, commitURI, commitParams, cacheKey)
	if err != nil {
		return "", fmt.Errorf("提交秒传失败: %w", err)
	}
	fileID = anyString(commitRes["fileId"])
	if fileID == "" {
		if data, ok := commitRes["data"].(map[string]any); ok {
			if f, ok2 := data["file"].(map[string]any); ok2 {
				fileID = anyString(f["userFileId"])
			}
			if fileID == "" {
				fileID = anyString(data["userFileId"])
			}
		}
	}
	if fileID == "" {
		fileID = uploadFileID
	}
	return fileID, nil
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

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func randomHex8() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
