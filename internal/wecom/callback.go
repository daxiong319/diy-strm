package wecom

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// ---- 企微回调加解密 ----
//
// 企微智能机器人的回调是**密文**的：明文包在 URLQuery 的 encrypt 字段里，
// 解开后是一个 AES-CBC 包，前面 16 字节是随机数，尾部 4 字节是 PKCS#7 填充长度。
// 这套算法官方叫「消息加解密」，Telegram 那边完全没有对应物 ——
// 所以它住在企微包里，不进 internal/inboundbot 的共用内核。
//
// AES 长度不是 32 字节而是 43 个字符（去掉末尾 "="）：企微给的 EncodingAESKey
// 是这个长度，直接 base64 解会失败，所以要先补 "=" 再解。
const (
	// aesKeyBytes 是解密后真正使用的密钥长度。
	aesKeyBytes = 32
	// randomPrefixBytes 是包头那段 16 字节随机数。
	// 它不参与校验，只用于让同一段明文每次密文不同。
	randomPrefixBytes = 16
)

// DecodeAESKey 把企微给的 EncodingAESKey 转成 32 字节密钥。
func DecodeAESKey(encoded string) ([]byte, error) {
	raw := strings.TrimSpace(encoded)
	if raw == "" {
		return nil, fmt.Errorf("企业微信 EncodingAESKey 未配置")
	}
	// 企微给的 key 常常是 43 个字符（省掉了末尾的一个 "="）。
	if pad := len(raw) % 4; pad != 0 {
		raw += strings.Repeat("=", 4-pad)
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("企业微信 EncodingAESKey 不是合法的 base64: %w", err)
	}
	if len(key) != aesKeyBytes {
		return nil, fmt.Errorf("企业微信 EncodingAESKey 解出来是 %d 字节，应为 %d 字节", len(key), aesKeyBytes)
	}
	return key, nil
}

// DecryptMessage 解开企微的密文包。
//
// 格式：[16 字节随机数][明文][4 字节填充长度大端]。
//
// 尾部那 4 字节必须校验：没有它的话，一个被改过 1 位的密文包有 1/256 的概率
// 解出「长度合法」的明文，然后被当成一条真实的用户消息执行。
// Bot 的入口直接连着转存和跑整理，1/256 的误放行不能接受。
func DecryptMessage(encodedAESKey, encrypted string) ([]byte, error) {
	key, err := DecodeAESKey(encodedAESKey)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encrypted))
	if err != nil {
		return nil, fmt.Errorf("企业微信密文不是合法的 base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(raw)%aes.BlockSize != 0 || len(raw) <= randomPrefixBytes+4 {
		return nil, fmt.Errorf("企业微信密文长度非法（%d 字节）", len(raw))
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(out, raw)

	padding := int(binary.BigEndian.Uint32(out[len(out)-4:]))
	if padding < 1 || padding > aes.BlockSize || padding > len(out)-randomPrefixBytes-4 {
		return nil, fmt.Errorf("企业微信密文的填充长度非法（%d）", padding)
	}
	// 填充字节本身也要逐个核对：只看长度的话，被改动的密文仍然可能通过。
	body := out[randomPrefixBytes : len(out)-4-padding]
	for i := len(out) - 4 - padding; i < len(out)-4; i++ {
		if int(out[i]) != padding {
			return nil, fmt.Errorf("企业微信密文的填充字节不一致")
		}
	}
	return body, nil
}

// EncryptMessage 加密一条消息（回包时用）。
func EncryptMessage(encodedAESKey, plaintext string) (string, error) {
	key, err := DecodeAESKey(encodedAESKey)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	// 填充要按**整个包**（16 字节随机数 + 明文 + 4 字节长度）算，不是只按明文算：
	// 只按明文算的话，总长度常常不是 16 的倍数，CBC 直接 panic
	//（crypto/cipher: input not full blocks）。
	body := randomPrefixBytes + len(plaintext) + 4
	pad := aes.BlockSize - body%aes.BlockSize
	if pad == 0 {
		// PKCS#7 恒定补 1..BlockSize 字节，补 0 的实现会让某些解密端解不出来。
		pad = aes.BlockSize
	}
	buf := make([]byte, 0, body+pad)
	buf = append(buf, randomBytes(randomPrefixBytes)...)
	buf = append(buf, plaintext...)
	buf = append(buf, bytesRepeat(byte(pad), pad)...)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(pad))
	buf = append(buf, length[:]...)

	out := make([]byte, len(buf))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(out, buf)
	return base64.StdEncoding.EncodeToString(out), nil
}

// ComputeSignature 算回调签名。
//
// 官方算法是 sha1(sort(token, timestamp, nonce, encrypt))：
// 四个字符串按字典序排好、拼在一起、取 sha1 的 hex。
// **拼错了这个式子，签名会永远对不上**，而企微只会回一句「签名错误」，
// 从它那侧看不出是排序还是拼接的问题。
func ComputeSignature(token, timestamp, nonce, encrypt string) string {
	parts := []string{token, timestamp, nonce, encrypt}
	// 只四五个元素，插入排序比引入 sort.Slice 清楚。
	for i := 1; i < len(parts); i++ {
		for j := i; j > 0 && parts[j] < parts[j-1]; j-- {
			parts[j], parts[j-1] = parts[j-1], parts[j]
		}
	}
	h := sha1.New()
	io.WriteString(h, strings.Join(parts, ""))
	return hexLower(h.Sum(nil))
}

// VerifySignature 比对签名。**比较必须无视大小写**：这里的 hex 由本函数生成，
// 而调用方也可能从别处算出大写形式；用 != 比较会让一个完全正确的请求被拒。
func VerifySignature(token, timestamp, nonce, encrypt, want string) bool {
	got := ComputeSignature(token, timestamp, nonce, encrypt)
	return strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want))
}

func randomBytes(n int) []byte {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败时全零也能工作：包头本来就不参与校验。
		// 返回错误反而会让一条正常消息因为「随机数取不到」而失败。
		return make([]byte, n)
	}
	return buf
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func hexLower(raw []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(raw)*2)
	for i, b := range raw {
		out[i*2] = digits[b>>4]
		out[i*2+1] = digits[b&0x0f]
	}
	return string(out)
}
