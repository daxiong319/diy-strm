package tgchannel

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
)

// 频道资源帖加密分享链接支持。
// tgto123 原版把 123FSLink/123FLCP 等资源暗号按
// [ENCRYPTED_LINK_START]<urlsafe_base64(iv+ciphertext)>[ENCRYPTED_LINK_END]
// 嵌在帖子里；本包按同款算法（AES-256-CBC + PKCS7，默认密钥 "123456" 补 ASCII '0' 至 32B）
// 解密后照常提取网盘链接。密钥可用环境变量 TG_CHANNEL_LINK_ENC_KEY 覆盖。

var (
	reEncryptedLink = regexp.MustCompile(`(?is)\[ENCRYPTED_LINK_START\](.*?)\[ENCRYPTED_LINK_END\]`)
	reFastShare     = regexp.MustCompile(`(?i)(123FSLinkV[12]|123FLCPV[12])`)

	linkEncKeyOnce sync.Once
	linkEncKey     []byte
)

// encryptedLinkKey 返回 AES 密钥：env TG_CHANNEL_LINK_ENC_KEY，空则默认 "123456"
// （与 tgto123 decrypt_tool 一致：长度不足右侧补 ASCII '0'，超过截断到 32 字节）。
func encryptedLinkKey() []byte {
	linkEncKeyOnce.Do(func() {
		key := os.Getenv("TG_CHANNEL_LINK_ENC_KEY")
		if strings.TrimSpace(key) == "" {
			key = "123456"
		}
		b := []byte(key)
		switch {
		case len(b) < 32:
			padded := make([]byte, 32)
			copy(padded, b)
			for i := len(b); i < 32; i++ {
				padded[i] = '0'
			}
			b = padded
		case len(b) > 32:
			b = b[:32]
		}
		linkEncKey = b
	})
	return linkEncKey
}

// DecryptEncryptedLinks 解密文本中全部 [ENCRYPTED_LINK_START]...[ENCRYPTED_LINK_END] 片段，
// 替换回明文；解密失败保留原文。返回处理后的文本。
func DecryptEncryptedLinks(text string) string {
	matches := reEncryptedLink.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return text
	}
	key := encryptedLinkKey()
	for _, m := range matches {
		plain, err := decryptLinkCipher(m[1], key)
		if err != nil {
			log.Printf("[tgchannel] 解密加密分享链接失败（保留原文）：%v", err)
			continue
		}
		text = strings.ReplaceAll(text, m[0], plain)
	}
	return text
}

// HasFastShareMarker 文本是否含 123 秒传/批量暗号标记（123FSLinkV1/V2、123FLCPV1/V2）。
// 这类暗号需 123 秒传 API 转存，目前引擎识别并留痕提示、不自动转存。
func HasFastShareMarker(text string) bool {
	return reFastShare.MatchString(text)
}

// decryptLinkCipher AES-256-CBC 解密单个暗号片段（url-safe base64，去 '='，补回后解码）
func decryptLinkCipher(ciphertext string, key []byte) (string, error) {
	s := strings.TrimSpace(ciphertext)
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	if len(raw) < aes.BlockSize {
		return "", fmt.Errorf("密文过短（%d 字节）", len(raw))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	if len(raw)%aes.BlockSize != 0 {
		return "", fmt.Errorf("密文长度非块对齐")
	}
	iv := raw[:aes.BlockSize]
	data := raw[aes.BlockSize:]
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(data, data)
	// 去 PKCS7 填充
	padLen := int(data[len(data)-1])
	if padLen <= 0 || padLen > aes.BlockSize || padLen > len(data) {
		return "", fmt.Errorf("非法 PKCS7 填充")
	}
	for _, b := range data[len(data)-padLen:] {
		if int(b) != padLen {
			return "", fmt.Errorf("非法 PKCS7 填充")
		}
	}
	return string(data[:len(data)-padLen]), nil
}
