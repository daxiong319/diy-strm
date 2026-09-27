package cloud189

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
)

// sortParameter 参数排序拼接（对齐 SDK sortParameter：key=value 按字典序 & 连接）
func sortParameter(data map[string]string) string {
	if len(data) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(data))
	for k, v := range data {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// getSignature 签名（MD5(排序后参数串)，对齐 SDK getSignature）
func getSignature(data map[string]string) string {
	sum := md5.Sum([]byte(sortParameter(data)))
	return hex.EncodeToString(sum[:])
}

// parseRSAPublicKey 解析 PEM 公钥
func parseRSAPublicKey(publicKeyPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("RSA 公钥 PEM 解析失败")
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("RSA 公钥解析失败: %w", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("非 RSA 公钥")
	}
	return pub, nil
}

// rsaEncryptHex RSA PKCS1 加密并返回大写 hex（对齐 SDK rsaEncrypt）
func rsaEncryptHex(publicKeyPEM, origData string) (string, error) {
	pub, err := parseRSAPublicKey(publicKeyPEM)
	if err != nil {
		return "", err
	}
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(origData))
	if err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(enc)), nil
}

// rsaEncryptBase64 RSA PKCS1 加密并返回 base64（对齐 RapidUpload rsaEncryptBase64）
func rsaEncryptBase64(publicKeyPEM, text string) (string, error) {
	pub, err := parseRSAPublicKey(publicKeyPEM)
	if err != nil {
		return "", err
	}
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, pub, []byte(text))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

// hmacSHA1 HMAC-SHA1 hex（对齐 RapidUpload hmacSha1：key=value 排序 & 连接）
func hmacSHA1(obj map[string]string, key string) string {
	pairs := make([]string, 0, len(obj))
	for k, v := range obj {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	data := strings.Join(pairs, "&")
	mac := hmac.New(sha1.New, []byte(key))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// sha256Hex SHA-256 hex
func sha256Hex(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}
