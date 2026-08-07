package pan139

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"resty.dev/v3"
)

// 扫码登录常量（逆向自 yun.139.com 网页 thirdlogin 接口，协议参考 LitePan 139Cloud 驱动）
const (
	qrThirdLoginURL = "https://user-njs.yun.139.com/user/thirdlogin"
	qrPagePrefix    = "https://yun.139.com/w/#/qrcLogin"

	qrClientType = 670
	qrCPID       = 292
	qrPinType    = 21
	qrAppVersion = "mCloud_4.3.0_536"

	// 与官网 web 包 mcloud-version 对齐；过旧版本扫码轮询会返回 9101
	qrWebClientVersion = "7.17.9"

	qrCodeTimeoutSec = 300

	// 网页 thirdlogin 传输层 AES-256-CBC 密钥（请求/响应外壳）
	qrTransportKey = "UqEZkrjCKfa02pP6jntzFmkzOz86zHUC"
	// 成功时 data 字段若为 hex 密文，用 AES-128-ECB 再解一层
	qrDataKey = "qPqDw263XgFgL3u8"
)

// QRStatus 扫码登录状态
type QRStatus string

const (
	QRWaiting   QRStatus = "waiting"   // 未扫码/已扫码待确认
	QRSuccess   QRStatus = "success"   // 扫码并确认成功
	QRExpired   QRStatus = "expired"   // 二维码已过期
	QRFailed    QRStatus = "failed"    // 扫码登录失败
	QRCancelled QRStatus = "cancelled" // 用户取消
)

// QRStartResult 扫码会话启动结果（二维码内容由前端渲染）
type QRStartResult struct {
	Token     string `json:"token"`      // 轮询会话令牌（base64url 编码）
	QRURL     string `json:"qr_url"`     // 二维码内容（URL）
	ExpiresIn int    `json:"expires_in"` // 有效秒数
}

// QRPollResult 扫码轮询结果
type QRPollResult struct {
	Status        QRStatus `json:"status"`
	Message       string   `json:"message,omitempty"`
	Authorization string   `json:"authorization,omitempty"` // 成功时返回
	Username      string   `json:"username,omitempty"`      // 成功时返回
	ExpiresAt     int64    `json:"expires_at,omitempty"`    // 成功时返回（毫秒时间戳）
}

// qrSession 扫码会话（opaque token 内容，base64url 编码）
type qrSession struct {
	Created   int64  `json:"ts"`
	SessionID string `json:"sid"`
	VisitorID string `json:"vid"`
}

// qrLoginEnvelope thirdlogin 响应外壳（AES-256-CBC 解密后）
type qrLoginEnvelope struct {
	Success    bool
	Code       string
	Message    string
	StatusCode string
	Data       json.RawMessage
}

// qrLoginData thirdlogin 登录数据（data 字段 AES-128-ECB 解密后）
type qrLoginData struct {
	Account         string
	Token           string
	AuthToken       string
	ExpireTime      json.Number
	EncryptAccount  string
	SimplifyAccount string
	ResultCode      string
	ResultDesc      string
}

// 与网页 qrCodeErrArr / queryQrcLoginResult 对齐的状态码
var (
	qrCodeExpired = map[string]bool{"200059542": true}
	qrCodeCancel  = map[string]bool{"200059549": true}
	qrCodeFail    = map[string]bool{
		"200059543": true, "200059545": true, "200059546": true, "200059547": true,
	}
	qrCodeWaiting = map[string]bool{
		"200059541": true, // 未扫码
		"200059548": true, // 已扫码待确认
	}
)

// qrRestClient 扫码接口专用 HTTP 客户端（单例复用连接池）
var qrRestClient *resty.Client

func init() {
	qrRestClient = resty.New()
	qrRestClient.SetTimeout(30 * time.Second)
	qrRestClient.SetRetryCount(0)
}

// StartQRLogin 生成扫码登录会话与二维码内容
// 二维码内容为 yun.139.com 扫码页 URL，由前端渲染成二维码图片；轮询时携带返回的 Token
func StartQRLogin() (*QRStartResult, error) {
	sessionID := qrRandomString(16)
	visitorID := qrRandomString(32)
	qrURL := fmt.Sprintf("%s?sID=%s&dID=%s&cType=9", qrPagePrefix, sessionID, visitorID)
	opaque, err := encodeQRSession(qrSession{
		Created:   time.Now().Unix(),
		SessionID: sessionID,
		VisitorID: visitorID,
	})
	if err != nil {
		return nil, fmt.Errorf("中国移动云盘扫码会话生成失败：%v", err)
	}
	return &QRStartResult{
		Token:     opaque,
		QRURL:     qrURL,
		ExpiresIn: qrCodeTimeoutSec,
	}, nil
}

// PollQRLogin 轮询扫码登录状态；成功时返回 Authorization 凭据（可直接用于账号登录）
func PollQRLogin(token string) (*QRPollResult, error) {
	sess, err := decodeQRSession(token)
	if err != nil || sess.SessionID == "" {
		return nil, fmt.Errorf("扫码会话无效，请重新获取二维码")
	}
	elapsed := time.Now().Unix() - sess.Created
	if elapsed > qrCodeTimeoutSec {
		return &QRPollResult{Status: QRExpired, Message: "二维码已过期，请重新获取"}, nil
	}

	env, bodyLen, err := qrThirdLogin(context.Background(), sess.SessionID, sess.VisitorID)
	if err != nil {
		// 成功包通常 >1KB；解密/解析失败时不要静默 waiting，否则二维码会一直停着
		if bodyLen > 1000 {
			return &QRPollResult{Status: QRFailed, Message: "扫码成功但解析登录响应失败，请重试：" + err.Error()}, nil
		}
		return &QRPollResult{Status: QRWaiting, Message: "请用中国移动云盘 App「扫一扫」扫码并确认登录"}, nil
	}
	data, dataErr := parseQRLoginData(env.Data)
	switch mapQRPollStatus(env, data, elapsed) {
	case QRSuccess:
		if dataErr != nil {
			return &QRPollResult{Status: QRFailed, Message: "扫码成功但解析登录数据失败，请重试"}, nil
		}
		return buildQRAuthorization(data)
	case QRExpired:
		return &QRPollResult{Status: QRExpired, Message: "二维码已过期，请重新获取"}, nil
	case QRCancelled:
		return &QRPollResult{Status: QRCancelled, Message: "已取消，请重新扫码"}, nil
	case QRFailed:
		msg := firstNonEmpty(data.ResultDesc, env.Message, "扫码登录失败")
		if qrCodeCancel[qrPollCode(env, data)] {
			msg = "已取消，请重新扫码"
		}
		return &QRPollResult{Status: QRFailed, Message: msg}, nil
	default:
		msg := firstNonEmpty(env.Message, "请用中国移动云盘 App「扫一扫」扫码，并在手机上确认登录")
		if qrCodeWaiting[qrPollCode(env, data)] {
			if strings.Contains(msg, "未找到") || msg == "参数为空" {
				msg = "请用中国移动云盘 App「扫一扫」扫码，并在手机上确认登录"
			}
		}
		return &QRPollResult{Status: QRWaiting, Message: msg}, nil
	}
}

// qrPollCode 业务等待/失败码优先看外层；仅当外层是 0/空时才回落到 data.result
func qrPollCode(env qrLoginEnvelope, data qrLoginData) string {
	code := strings.TrimSpace(env.Code)
	if code != "" && code != "0" && code != "0000" {
		return code
	}
	if rc := strings.TrimSpace(data.ResultCode); rc != "" {
		return rc
	}
	return code
}

// mapQRPollStatus 映射扫码轮询状态
func mapQRPollStatus(env qrLoginEnvelope, data qrLoginData, elapsed int64) QRStatus {
	// 等待态也可能有 resultCode=0，必须结合凭证、success、statusCode 或外层 code 判断成功
	if qrHasCredentials(data) || env.Success || qrCodeOK(env.Code) || env.StatusCode == "200" {
		return QRSuccess
	}
	code := qrPollCode(env, data)
	switch {
	case qrCodeExpired[code] || elapsed > qrCodeTimeoutSec:
		return QRExpired
	case qrCodeCancel[code]:
		return QRCancelled
	case qrCodeFail[code]:
		return QRFailed
	case qrCodeWaiting[code] || code == "" || code == "9999":
		return QRWaiting
	case code == "01000001" || code == "9101":
		// 请求头/签名/体不对；继续 waiting 只会假死
		return QRFailed
	default:
		// 未知业务码不再默默 waiting，避免扫完码后弹窗假死
		return QRFailed
	}
}

func qrCodeOK(code string) bool {
	switch strings.TrimSpace(code) {
	case "0", "0000":
		return true
	default:
		return false
	}
}

func qrHasCredentials(data qrLoginData) bool {
	account := strings.TrimSpace(data.Account)
	if account == "" && strings.TrimSpace(data.EncryptAccount) != "" {
		account = "x"
	}
	token := strings.TrimSpace(firstNonEmpty(data.Token, data.AuthToken))
	return account != "" && token != ""
}

// qrThirdLogin 调用网页扫码登录接口查询状态
// 返回（响应外壳, 响应体长度, 错误）；响应体长度用于区分"等待中"与"扫码成功但解析失败"
func qrThirdLogin(ctx context.Context, sessionID, visitorID string) (qrLoginEnvelope, int, error) {
	var out qrLoginEnvelope
	body := map[string]any{
		"msisdn":     "",
		"random":     "",
		"dycpwd":     sessionID,
		"cpid":       qrCPID,
		"clienttype": qrClientType,
		"version":    qrAppVersion,
		"pintype":    qrPinType,
		"secinfo":    qrSecInfo(sessionID),
		"loginMode":  "0",
		"extInfo":    map[string]any{},
	}
	// 官网先对明文算 mcloud-sign，再 AES 加密请求体
	plainJSON, err := json.Marshal(body)
	if err != nil {
		return out, 0, fmt.Errorf("中国移动云盘扫码参数序列化失败：%v", err)
	}
	payload, err := qrEncryptTransport(body)
	if err != nil {
		return out, 0, err
	}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return out, 0, fmt.Errorf("中国移动云盘扫码请求体封装失败：%v", err)
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	randStr := randomString(16)
	sign := calSign(string(plainJSON), ts, randStr)

	req := qrRestClient.R().
		SetContext(ctx).
		SetHeaders(qrSignHeaders(ts, randStr, sign, visitorID)).
		SetBody(reqBody)
	res, err := req.Post(qrThirdLoginURL)
	if err != nil {
		return out, 0, fmt.Errorf("中国移动云盘扫码请求失败：%v", err)
	}
	defer res.Body.Close()
	data := res.Bytes()
	bodyLen := len(data)
	if res.StatusCode() != 200 {
		return out, bodyLen, fmt.Errorf("中国移动云盘扫码接口 HTTP %d: %s", res.StatusCode(), truncate(data, 200))
	}
	plain, err := qrDecryptTransport(data)
	if err != nil {
		// 偶发明文 JSON（例如网关错误）
		if env, ok := parseQRLoginEnvelope(data); ok {
			return env, bodyLen, nil
		}
		return out, bodyLen, err
	}
	env, ok := parseQRLoginEnvelope(plain)
	if !ok {
		return out, bodyLen, fmt.Errorf("中国移动云盘扫码响应无法解析")
	}
	return env, bodyLen, nil
}

// qrSignHeaders 扫码接口请求头（基于个人盘签名头，去掉 Authorization，替换 web 客户端版本与设备信息）
func qrSignHeaders(ts, randStr, sign, visitorID string) map[string]string {
	h := personalHeaders("", ts, randStr, sign)
	delete(h, "Authorization")
	h["hcy-cool-flag"] = "1"
	h["Mcloud-Version"] = qrWebClientVersion
	h["x-DeviceInfo"] = qrDeviceInfo(visitorID)
	h["X-Yun-Client-Info"] = qrClientInfo(visitorID)
	return h
}

func qrDeviceInfo(visitorID string) string {
	return fmt.Sprintf("||9|%s|chrome|120.0.0.0|%s||windows 10||zh-CN|||", qrWebClientVersion, strings.TrimSpace(visitorID))
}

func qrClientInfo(visitorID string) string {
	return fmt.Sprintf("||9|%s|chrome|120.0.0.0|%s||windows 10||zh-CN|||dW5kZWZpbmVk||", qrWebClientVersion, strings.TrimSpace(visitorID))
}

// parseQRLoginEnvelope 解析响应外壳（解密后的明文 JSON）
func parseQRLoginEnvelope(raw []byte) (qrLoginEnvelope, bool) {
	var out qrLoginEnvelope
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return out, false
	}
	out.Success = anyTruthy(top["success"])
	out.Code = anyToString(top["code"])
	out.StatusCode = anyToString(top["statusCode"])
	out.Message = anyToString(top["message"])
	if out.Message == "" {
		out.Message = anyToString(top["msg"])
	}
	if data, ok := top["data"]; ok && data != nil {
		switch v := data.(type) {
		case string:
			b, _ := json.Marshal(v)
			out.Data = b
		default:
			b, err := json.Marshal(v)
			if err == nil {
				out.Data = b
			}
		}
	}
	return out, true
}

// parseQRLoginData 解析登录数据（data 字段，可能为 hex/base64 密文）
func parseQRLoginData(raw json.RawMessage) (qrLoginData, error) {
	var out qrLoginData
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil && strings.TrimSpace(asString) != "" {
		plain, err := qrDecryptDataField(asString)
		if err != nil {
			// 兼容偶发已是明文 JSON 字符串的情况
			if strings.HasPrefix(strings.TrimSpace(asString), "{") {
				return fillQRLoginData([]byte(asString))
			}
			return out, err
		}
		return fillQRLoginData(plain)
	}
	return fillQRLoginData(raw)
}

func fillQRLoginData(raw []byte) (qrLoginData, error) {
	var out qrLoginData
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return out, fmt.Errorf("中国移动云盘扫码登录数据解析失败：%v", err)
	}
	out.Account = firstNonEmpty(
		anyToString(top["account"]),
		anyToString(top["msisdn"]),
		anyToString(top["phoneNumber"]),
		findStringByKeys(top, "account", "msisdn", "phoneNumber"),
	)
	out.Token = firstNonEmpty(
		anyToString(top["token"]),
		anyToString(top["authToken"]),
		anyToString(top["accessToken"]),
		findStringByKeys(top, "authToken", "token", "accessToken"),
	)
	out.AuthToken = firstNonEmpty(anyToString(top["authToken"]), anyToString(top["token"]), out.Token)
	out.EncryptAccount = firstNonEmpty(anyToString(top["encryptAccount"]), findStringByKeys(top, "encryptAccount"))
	out.SimplifyAccount = anyToString(top["simplifyAccount"])
	if exp := firstNonEmpty(anyToString(top["expireTime"]), findStringByKeys(top, "expireTime")); exp != "" {
		out.ExpireTime = json.Number(exp)
	}
	if result, ok := top["result"].(map[string]any); ok {
		out.ResultCode = anyToString(result["resultCode"])
		out.ResultDesc = anyToString(result["resultDesc"])
	}
	return out, nil
}

// buildQRAuthorization 由扫码登录数据组装 Authorization 凭据（base64(pc:account:token|xxx|xxx|expire)）
func buildQRAuthorization(data qrLoginData) (*QRPollResult, error) {
	account := strings.TrimSpace(data.Account)
	if account == "" && strings.TrimSpace(data.EncryptAccount) != "" {
		if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data.EncryptAccount)); err == nil {
			account = strings.TrimSpace(string(decoded))
		}
	}
	token := strings.TrimSpace(data.Token)
	if token == "" {
		token = strings.TrimSpace(data.AuthToken)
	}
	if account == "" || token == "" {
		return nil, fmt.Errorf("扫码成功但未返回账号或令牌")
	}
	authorization := base64.StdEncoding.EncodeToString([]byte("pc:" + account + ":" + token))
	info, err := parseAuthorizationValue(authorization)
	if err != nil {
		// 个别响应 token 不含标准过期段时，用返回的过期时间补齐格式
		exp, expErr := data.ExpireTime.Int64()
		if expErr != nil || exp <= 0 {
			return nil, fmt.Errorf("扫码成功但凭据格式异常：%v", err)
		}
		millis := exp
		if exp < 1e12 {
			millis = exp * 1000
		}
		authorization = base64.StdEncoding.EncodeToString([]byte("pc:" + account + ":" + token + "|||" + strconv.FormatInt(millis, 10)))
		info, err = parseAuthorizationValue(authorization)
		if err != nil {
			return nil, fmt.Errorf("扫码成功但凭据格式异常：%v", err)
		}
	}
	return &QRPollResult{
		Status:        QRSuccess,
		Message:       "扫码登录成功",
		Authorization: info.authorization,
		Username:      info.account,
		ExpiresAt:     info.expiration,
	}, nil
}

// qrSecInfo secinfo 字段：sha1(fetion.com.cn:dycpwd) 大写 hex
func qrSecInfo(dycPwd string) string {
	sum := sha1.Sum([]byte("fetion.com.cn:" + dycPwd))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// qrEncryptTransport 请求体 AES-256-CBC 加密外壳：base64(IV + PKCS7(明文))
func qrEncryptTransport(body any) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("中国移动云盘扫码请求体序列化失败：%v", err)
	}
	block, err := aes.NewCipher([]byte(qrTransportKey))
	if err != nil {
		return "", fmt.Errorf("中国移动云盘扫码加密初始化失败：%v", err)
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", fmt.Errorf("中国移动云盘扫码加密初始化失败：%v", err)
	}
	padded := pkcs7Pad(raw, aes.BlockSize)
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, padded)
	return base64.StdEncoding.EncodeToString(append(iv, encrypted...)), nil
}

// qrDecryptTransport 响应体 AES-256-CBC 解密外壳
func qrDecryptTransport(data []byte) ([]byte, error) {
	s := strings.TrimSpace(string(data))
	if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") {
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("中国移动云盘扫码响应解析失败：%v", err)
		}
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("中国移动云盘扫码响应解码失败：%v", err)
	}
	if len(raw) < aes.BlockSize || len(raw)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("中国移动云盘扫码响应密文长度异常")
	}
	block, err := aes.NewCipher([]byte(qrTransportKey))
	if err != nil {
		return nil, fmt.Errorf("中国移动云盘扫码解密初始化失败：%v", err)
	}
	iv, ciphertext := raw[:aes.BlockSize], raw[aes.BlockSize:]
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	if out, err := pkcs7Unpad(plain); err == nil {
		return out, nil
	}
	// 个别成功包 padding 异常时，尽量截取 JSON 对象
	if i := bytes.IndexByte(plain, '{'); i >= 0 {
		if j := bytes.LastIndexByte(plain, '}'); j > i {
			return bytes.TrimSpace(plain[i : j+1]), nil
		}
	}
	return nil, fmt.Errorf("中国移动云盘扫码响应解密失败")
}

// qrDecryptDataField 解密登录数据字段（hex/base64 密文，AES-128-ECB）
func qrDecryptDataField(cipherText string) ([]byte, error) {
	cipherText = strings.TrimSpace(cipherText)
	raw, err := hex.DecodeString(cipherText)
	if err != nil {
		// 兼容偶发 base64 内层密文
		raw, err = base64.StdEncoding.DecodeString(cipherText)
		if err != nil {
			return nil, fmt.Errorf("中国移动云盘扫码数据解码失败：%v", err)
		}
	}
	block, err := aes.NewCipher([]byte(qrDataKey))
	if err != nil {
		return nil, fmt.Errorf("中国移动云盘扫码数据解密初始化失败：%v", err)
	}
	if len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("中国移动云盘扫码数据密文长度异常")
	}
	plain := make([]byte, len(raw))
	for i := 0; i < len(raw); i += aes.BlockSize {
		block.Decrypt(plain[i:i+aes.BlockSize], raw[i:i+aes.BlockSize])
	}
	if out, err := pkcs7Unpad(plain); err == nil {
		return out, nil
	}
	if i := bytes.IndexByte(plain, '{'); i >= 0 {
		if j := bytes.LastIndexByte(plain, '}'); j > i {
			return bytes.TrimSpace(plain[i : j+1]), nil
		}
	}
	return nil, fmt.Errorf("中国移动云盘扫码数据解密失败")
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padLen := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+padLen)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(padLen)
	}
	return out
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("中国移动云盘扫码数据解密失败")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > len(data) || padLen > aes.BlockSize {
		return nil, fmt.Errorf("中国移动云盘扫码数据解密失败")
	}
	for i := 0; i < padLen; i++ {
		if data[len(data)-1-i] != byte(padLen) {
			return nil, fmt.Errorf("中国移动云盘扫码数据解密失败")
		}
	}
	return data[:len(data)-padLen], nil
}

// qrRandomString 生成随机字母数字字符串
func qrRandomString(n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		// 极端情况下退化为时间戳片段，保证流程可继续
		fallback := fmt.Sprintf("%d", time.Now().UnixNano())
		for len(fallback) < n {
			fallback += fallback
		}
		return fallback[:n]
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

func encodeQRSession(sess qrSession) (string, error) {
	b, err := json.Marshal(sess)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeQRSession(token string) (qrSession, error) {
	var sess qrSession
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return sess, err
	}
	return sess, json.Unmarshal(raw, &sess)
}

// findStringByKeys 深度优先查找任意层级的指定键
func findStringByKeys(root map[string]any, keys ...string) string {
	want := map[string]bool{}
	for _, k := range keys {
		want[strings.ToLower(k)] = true
	}
	var walk func(any) string
	walk = func(v any) string {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				if want[strings.ToLower(k)] {
					if s := anyToString(child); s != "" {
						return s
					}
				}
			}
			for _, child := range t {
				if s := walk(child); s != "" {
					return s
				}
			}
		case []any:
			for _, child := range t {
				if s := walk(child); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(root)
}

func anyTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return t != 0
	case json.Number:
		i, err := t.Int64()
		return err == nil && i != 0
	default:
		return false
	}
}

func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return strings.TrimSpace(fmt.Sprint(t))
	case json.Number:
		return strings.TrimSpace(t.String())
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		s := strings.TrimSpace(string(b))
		return strings.Trim(s, `"`)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// truncate 截断响应内容用于错误提示
func truncate(data []byte, maxLen int) string {
	if len(data) <= maxLen {
		return string(data)
	}
	return string(data[:maxLen])
}
