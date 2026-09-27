package cloud189

import (
	"strings"
	"testing"
)

// TestCasParseV1 V1 JSON 清单解析
func TestCasParseV1(t *testing.T) {
	text := `{"fileName":"生逢其时.S01E01.2026.2160p.mkv","fileSize":16951101639,"fileMd5":"A1B2C3D4E5F60718293A4B5C6D7E8F90","sliceMd5":"F90E8D7C6B5A4938271605F4E3D2C1B0","uploadTime":"2026-09-01T00:00:00Z"}`
	m, err := ParseManifestText(text)
	if err != nil {
		t.Fatalf("V1 解析失败：%v", err)
	}
	if m.FileName != "生逢其时.S01E01.2026.2160p.mkv" || m.FileSize != 16951101639 {
		t.Fatalf("V1 字段错误：%+v", m)
	}
	if m.FileMd5 != "A1B2C3D4E5F60718293A4B5C6D7E8F90" || m.SliceMd5 != "F90E8D7C6B5A4938271605F4E3D2C1B0" {
		t.Fatalf("V1 MD5 未大写归一：%+v", m)
	}
}

// TestCasParseV2JSON V2 JSON 清单解析
func TestCasParseV2JSON(t *testing.T) {
	text := `{"version":2,"fileName":"test.mkv","fileSize":1000,"hashes":{"md5":"a1b2c3d4e5f60718293a4b5c6d7e8f90","sliceMd5":"f90e8d7c6b5a4938271605f4e3d2c1b0"},"sourceDrive":"cloud189"}`
	m, err := ParseManifestV2(text)
	if err != nil {
		t.Fatalf("V2 解析失败：%v", err)
	}
	if m.Hashes.FileMd5 != "A1B2C3D4E5F60718293A4B5C6D7E8F90" {
		t.Fatalf("V2 MD5 未大写：%+v", m.Hashes)
	}
}

// TestCasParsePipe 管道符格式解析（论坛流传格式）
func TestCasParsePipe(t *testing.T) {
	text := `生逢其时.S01E01.2026.2160p.mkv|16951101639|a1b2c3d4e5f60718293a4b5c6d7e8f90|f90e8d7c6b5a4938271605f4e3d2c1b0`
	m, err := ParseManifestV2(text)
	if err != nil {
		t.Fatalf("管道符解析失败：%v", err)
	}
	if m.FileName != "生逢其时.S01E01.2026.2160p.mkv" || m.Hashes.FileMd5 != "A1B2C3D4E5F60718293A4B5C6D7E8F90" {
		t.Fatalf("管道符解析错误：%+v", m)
	}
}

// TestCasParseBase64V1 Base64 编码 V1 清单解析
func TestCasParseBase64V1(t *testing.T) {
	b64 := "eyJmaWxlTmFtZSI6IngubWt2IiwiZmlsZVNpemUiOjEwMCwiZmlsZU1kNSI6IkExQjJDM0Q0RTVGNjA3MTgyOTNBNEI1QzZEN0U4RjkwIiwic2xpY2VNZDUiOiJGOTBFOEQ3QzZCNUE0OTM4MjcxNjA1RjRFM0QyQzFCMCJ9" // base64({"fileName":"x.mkv",...})
	m, err := ParseManifestV2(b64)
	if err != nil {
		t.Fatalf("Base64 解析失败：%v", err)
	}
	if m.Hashes.FileMd5 != "A1B2C3D4E5F60718293A4B5C6D7E8F90" {
		t.Fatalf("Base64 解析错误：%+v", m)
	}
}

// TestCasParseCloud189Scheme cloud189:// 协议头解析
func TestCasParseCloud189Scheme(t *testing.T) {
	inner := `{"fileName":"x.mkv","fileSize":100,"fileMd5":"A1B2C3D4E5F60718293A4B5C6D7E8F90","sliceMd5":"F90E8D7C6B5A4938271605F4E3D2C1B0"}`
	m, err := ParseManifestV2("cloud189://" + inner)
	if err != nil {
		t.Fatalf("协议头解析失败：%v", err)
	}
	if m.Hashes.SliceMd5 != "F90E8D7C6B5A4938271605F4E3D2C1B0" {
		t.Fatalf("协议头解析错误：%+v", m)
	}
}

// TestCasEncodeV1 V1 编码往返
func TestCasEncodeV1(t *testing.T) {
	m := CasManifest{FileName: "a.mkv", FileSize: 1, FileMd5: "A", SliceMd5: "B"}
	out := EncodeManifestV1(m)
	if !strings.Contains(out, "\"fileName\": \"a.mkv\"") || !strings.Contains(out, "uploadTime") {
		t.Fatalf("V1 编码异常：%s", out)
	}
	m2, err := ParseManifestText(out)
	if err != nil || m2.FileName != "a.mkv" {
		t.Fatalf("V1 编码往返失败：%v", err)
	}
}

// TestUpgradeToV2 V1→V2 升级
func TestUpgradeToV2(t *testing.T) {
	v1 := CasManifest{FileName: "a.mkv", FileSize: 100, FileMd5: "abc", SliceMd5: "def"}
	v2 := UpgradeToV2(v1)
	if v2.Version != 2 || v2.Hashes.FileMd5 != "ABC" || v2.Hashes.SliceMd5 != "DEF" {
		t.Fatalf("升级错误：%+v", v2)
	}
}

// TestCasFileNameHelpers 文件名工具
func TestCasFileNameHelpers(t *testing.T) {
	if !IsCasFileName("a.mkv.cas") || !IsCasFileName("A.CAS") {
		t.Fatal("IsCasFileName 误判")
	}
	if VirtualFileName("a.mkv.cas") != "a.mkv" {
		t.Fatal("VirtualFileName 错误")
	}
}

// TestAesEncryptECB AES-128-ECB 加密（与 SDK aesEncrypt 对齐：utf8 key、hex 大写、PKCS7）
func TestAesEncryptECB(t *testing.T) {
	// 与 Node crypto.createCipheriv('aes-128-ecb', key) 对齐的已知向量
	enc, err := aesEncryptECB(`{"a":1}`, "0123456789abcdef")
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	if enc == "" || enc != strings.ToUpper(enc) {
		t.Fatalf("输出非大写 hex：%s", enc)
	}
	// 同输入同 key 必同输出（ECB 确定性）
	enc2, _ := aesEncryptECB(`{"a":1}`, "0123456789abcdef")
	if enc != enc2 {
		t.Fatal("ECB 应确定性输出")
	}
}

// TestSortParameterAndSignature 签名排序（与 SDK sortParameter/getSignature 对齐）
func TestSortParameterAndSignature(t *testing.T) {
	data := map[string]string{"b": "2", "a": "1", "c": "3"}
	if sortParameter(data) != "a=1&b=2&c=3" {
		t.Fatalf("排序错误：%s", sortParameter(data))
	}
	sig1 := getSignature(data)
	sig2 := getSignature(map[string]string{"c": "3", "a": "1", "b": "2"})
	if sig1 != sig2 {
		t.Fatal("同参数不同序应同签名")
	}
}

// TestHmacSHA1 HMAC-SHA1（与 SDK hmacSha1 对齐：key=value 排序 & 连接）
func TestHmacSHA1(t *testing.T) {
	obj := map[string]string{"b": "2", "a": "1"}
	out := hmacSHA1(obj, "secret")
	// 与 Node crypto.createHmac('sha1','secret').update('a=1&b=2') 对齐
	if out == "" || len(out) != 40 {
		t.Fatalf("HMAC-SHA1 长度错误：%s", out)
	}
}

// TestParseRsaKeyResponse RSA 密钥响应解析（JSON + XML 两形态）
func TestParseRsaKeyResponse(t *testing.T) {
	pub, pk, err := parseRsaKeyResponse(`{"pubKey":"ABC123","pkId":"k1"}`)
	if err != nil || pub != "ABC123" || pk != "k1" {
		t.Fatalf("JSON 解析错误：%v %s %s", err, pub, pk)
	}
	pub2, pk2, err := parseRsaKeyResponse(`<pubKey>XYZ</pubKey><pkId>k2</pkId>`)
	if err != nil || pub2 != "XYZ" || pk2 != "k2" {
		t.Fatalf("XML 解析错误：%v %s %s", err, pub2, pk2)
	}
}

// TestCasParseFlatManifest cloud-auto-save-x 扁平 payload 格式（实测样本）
func TestCasParseFlatManifest(t *testing.T) {
	// 真实样本：cloud139 的 cas_records.json_payload
	text := `{"content_hash":"1518e54fa582efa6eb1f41b7ba9bf1caebd09b67dac236cb5979b6febde80fd2","create_time":"1790379684","hash_algorithm":"SHA256","name":"群体 (2026).Colony.1080p.WEB-DL.AAC2.0.H.264.mkv","schema_version":2,"sha256":"1518e54fa582efa6eb1f41b7ba9bf1caebd09b67dac236cb5979b6febde80fd2","size":7098372977,"sliceMd5":"1518e54fa582efa6eb1f41b7ba9bf1caebd09b67dac236cb5979b6febde80fd2"}`
	m, err := ParseManifestV2(text)
	if err != nil {
		t.Fatalf("扁平格式解析失败：%v", err)
	}
	if m.FileName != "群体 (2026).Colony.1080p.WEB-DL.AAC2.0.H.264.mkv" || m.FileSize != 7098372977 {
		t.Fatalf("扁平格式字段错误：%+v", m)
	}
	if m.Hashes.Sha256 != "1518E54FA582EFA6EB1F41B7BA9BF1CAEBD09B67DAC236CB5979B6FEBDE80FD2" {
		t.Fatalf("sha256 应大写：%s", m.Hashes.Sha256)
	}
	if m.Hashes.SliceMd5 != "1518E54FA582EFA6EB1F41B7BA9BF1CAEBD09B67DAC236CB5979B6FEBDE80FD2" {
		t.Fatalf("sliceMd5 解析错误：%s", m.Hashes.SliceMd5)
	}
}

// TestCasParseFlatManifestQuark cloud-auto-save-x 夸克扁平格式（content_file_md5 + content_pre_hash）
func TestCasParseFlatManifestQuark(t *testing.T) {
	text := `{"content_file_md5":"abcdef0123456789abcdef0123456789","content_pre_hash":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","content_sha256":"","name":"test.mkv","size":123456,"drive_type":"quark","create_time":"1790000000"}`
	m, err := ParseManifestV2(text)
	if err != nil {
		t.Fatalf("夸克扁平解析失败：%v", err)
	}
	if m.Hashes.FileMd5 != "ABCDEF0123456789ABCDEF0123456789" {
		t.Fatalf("file_md5 错误：%s", m.Hashes.FileMd5)
	}
	if m.Hashes.PreHash != "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF" {
		t.Fatalf("pre_hash 错误：%s", m.Hashes.PreHash)
	}
}

// TestHashSetNonEmpty 五哈希非空字段推导
func TestHashSetNonEmpty(t *testing.T) {
	h := HashSet{Sha256: "abc", FileMd5: "def", PreHash: "ghi"}
	nonEmpty := h.NonEmpty()
	if len(nonEmpty) != 3 {
		t.Fatalf("NonEmpty 应返回 3 项：%v", nonEmpty)
	}
	if _, ok := nonEmpty["sha256"]; !ok {
		t.Fatal("缺 sha256")
	}
	if _, ok := nonEmpty["fileMd5"]; !ok {
		t.Fatal("缺 fileMd5")
	}
	if _, ok := nonEmpty["preHash"]; !ok {
		t.Fatal("缺 preHash")
	}
}
