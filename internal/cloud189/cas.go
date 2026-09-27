package cloud189

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// CAS 清单（对齐 CasFileService：V1/V2/管道符/Base64/协议头全格式）
// ---------------------------------------------------------------------------

// CasManifest V1 清单
type CasManifest struct {
	FileName   string `json:"fileName"`
	FileSize   int64  `json:"fileSize"`
	FileMd5    string `json:"fileMd5"`
	SliceMd5   string `json:"sliceMd5"`
	UploadTime string `json:"uploadTime,omitempty"`
}

// HashSet 统一五哈希指纹（对齐 cloud-auto-save-x 的多网盘多维特征模型）。
// 一份清单同时服务多盘秒传：139 用 Sha256、夸克用 FileMd5+PreHash、天翼用 SliceMd5、115 用 Sha1/Gcid。
type HashSet struct {
	Sha1     string `json:"sha1,omitempty"`     // 115 秒传
	Sha256   string `json:"sha256,omitempty"`   // 移动云盘(139) 秒传
	FileMd5  string `json:"fileMd5,omitempty"`  // 天翼/123/夸克 全量 MD5
	SliceMd5 string `json:"sliceMd5,omitempty"` // 天翼秒传切片 MD5（139 兼容占位）
	PreHash  string `json:"preHash,omitempty"`  // 夸克 4×4MB 分块 MD5 预检串
	Gcid     string `json:"gcid,omitempty"`     // 光鸭秒传 GCID
	Sha115   string `json:"sha115,omitempty"`   // 115 专用 SHA1（保留兼容）
}

// NonEmpty 返回非空的哈希字段名列表（用于 rapid_drive_types 能力推导）
func (h HashSet) NonEmpty() map[string]string {
	out := map[string]string{}
	if h.Sha1 != "" {
		out["sha1"] = h.Sha1
	}
	if h.Sha256 != "" {
		out["sha256"] = h.Sha256
	}
	if h.FileMd5 != "" {
		out["fileMd5"] = h.FileMd5
	}
	if h.SliceMd5 != "" {
		out["sliceMd5"] = h.SliceMd5
	}
	if h.PreHash != "" {
		out["preHash"] = h.PreHash
	}
	if h.Gcid != "" {
		out["gcid"] = h.Gcid
	}
	return out
}

// CasManifestV2 V2 多网盘多维特征清单
type CasManifestV2 struct {
	Version     int     `json:"version"`
	FileName    string  `json:"fileName"`
	FileSize    int64   `json:"fileSize"`
	Hashes      HashSet `json:"hashes"`
	SourceDrive string  `json:"sourceDrive,omitempty"`
	CreatedAt   string  `json:"createdAt,omitempty"`
}

// ParseManifest 解析 V1 清单
func ParseManifest(data map[string]any) (CasManifest, error) {
	fileObj := data
	if f, ok := data["file"].(map[string]any); ok {
		fileObj = f
	} else if d, ok := data["data"].(map[string]any); ok {
		fileObj = d
	}
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := fileObj[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	m := CasManifest{
		FileName:   get("fileName", "name", "filename"),
		FileMd5:    strings.ToUpper(get("fileMd5", "md5", "file_md5")),
		SliceMd5:   strings.ToUpper(get("sliceMd5", "slice_md5")),
		UploadTime: get("uploadTime", "lastOpTime"),
	}
	if m.UploadTime == "" {
		m.UploadTime = time.Now().Format(time.RFC3339)
	}
	sizeStr := get("fileSize", "size", "length")
	if sizeStr != "" {
		m.FileSize, _ = strconv.ParseInt(sizeStr, 10, 64)
	} else if v, ok := fileObj["fileSize"].(float64); ok {
		m.FileSize = int64(v)
	} else if v, ok := fileObj["size"].(float64); ok {
		m.FileSize = int64(v)
	}
	if m.FileName == "" || m.FileSize <= 0 || m.FileMd5 == "" || m.SliceMd5 == "" {
		return m, fmt.Errorf("CAS 文件内容不完整，缺少 name/size/md5/sliceMd5")
	}
	return m, nil
}

// ParseManifestText 解析 CAS 文本（JSON 或 Base64）
func ParseManifestText(text string) (CasManifest, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return CasManifest{}, fmt.Errorf("CAS 清单内容为空")
	}
	if strings.HasPrefix(trimmed, "{") {
		var data map[string]any
		if err := json.Unmarshal([]byte(trimmed), &data); err != nil {
			return CasManifest{}, fmt.Errorf("解析 CAS JSON 失败: %w", err)
		}
		return ParseManifest(data)
	}
	// Base64
	if decoded, err := base64.StdEncoding.DecodeString(compactBase64(trimmed)); err == nil {
		s := strings.TrimSpace(string(decoded))
		if strings.HasPrefix(s, "{") {
			return ParseManifestText(s)
		}
	}
	return CasManifest{}, fmt.Errorf("无法识别的 CAS 文件内容格式")
}

// ParseManifestV2 解析 V2 清单（管道符/Base64/cloud189:// 协议头/V1 自动升级/cloud-auto-save-x 扁平格式）
func ParseManifestV2(text string) (CasManifestV2, error) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return CasManifestV2{}, fmt.Errorf("CAS 清单内容为空")
	}

	// 1. 管道符格式：文件名|文件大小|MD5|SliceMD5
	if strings.Contains(raw, "|") {
		parts := strings.Split(raw, "|")
		if len(parts) >= 4 {
			name := strings.TrimSpace(parts[0])
			size, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
			md5 := strings.TrimSpace(parts[2])
			sliceMd5 := strings.TrimSpace(parts[3])
			if name != "" && err == nil && isMd5(md5) && isMd5(sliceMd5) {
				m := CasManifestV2{
					Version:     2,
					FileName:    name,
					FileSize:    size,
					SourceDrive: "cloud189",
					CreatedAt:   time.Now().Format(time.RFC3339),
				}
				m.Hashes.FileMd5 = strings.ToUpper(md5)
				m.Hashes.SliceMd5 = strings.ToUpper(sliceMd5)
				return m, nil
			}
		}
	}

	// 2. 去协议头 + Base64
	clean := raw
	if strings.HasPrefix(strings.ToLower(clean), "cloud189://") {
		clean = strings.TrimSpace(clean[len("cloud189://"):])
	}
	jsonText := clean
	if !strings.HasPrefix(clean, "{") && !strings.HasPrefix(clean, "[") {
		if decoded, err := base64.StdEncoding.DecodeString(compactBase64(clean)); err == nil {
			s := string(decoded)
			s = strings.TrimPrefix(s, "\ufeff") // 去 UTF-8 BOM
			s = strings.TrimSpace(s)
			if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
				jsonText = s
			} else if strings.Contains(s, "|") {
				return ParseManifestV2(s)
			}
		}
	}

	var data any
	if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
		return CasManifestV2{}, fmt.Errorf("解析 CAS 失败: 既非有效 JSON 也非合法 Base64/管道符格式 (%v)", err)
	}
	// 数组取第一条
	if arr, ok := data.([]any); ok {
		if len(arr) == 0 {
			return CasManifestV2{}, fmt.Errorf("CAS JSON 数组为空")
		}
		data = arr[0]
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return CasManifestV2{}, fmt.Errorf("CAS JSON 结构非法")
	}

	// 3. cloud-auto-save-x 扁平格式：顶层 content_hash/hash_algorithm/sha256/sliceMd5/schema_version
	if isFlatManifest(obj) {
		return parseFlatManifestV2(obj), nil
	}

	// 4. V2 嵌套结构 {version:2, hashes:{...}}
	if ver, _ := obj["version"].(float64); ver == 2 {
		if hashes, ok := obj["hashes"].(map[string]any); ok {
			m := CasManifestV2{
				Version:     2,
				FileName:    anyString(obj["fileName"]),
				FileSize:    int64(anyFloat(obj["fileSize"])),
				SourceDrive: anyString(obj["sourceDrive"]),
				CreatedAt:   anyString(obj["createdAt"]),
			}
			if m.FileName == "" {
				m.FileName = anyString(obj["name"])
			}
			if m.CreatedAt == "" {
				m.CreatedAt = time.Now().Format(time.RFC3339)
			}
			m.Hashes.FileMd5 = strings.ToUpper(firstNonEmpty(anyString(hashes["fileMd5"]), anyString(hashes["md5"])))
			m.Hashes.SliceMd5 = strings.ToUpper(anyString(hashes["sliceMd5"]))
			m.Hashes.Sha1 = strings.ToUpper(anyString(hashes["sha1"]))
			m.Hashes.Sha256 = strings.ToUpper(anyString(hashes["sha256"]))
			m.Hashes.PreHash = strings.ToUpper(anyString(hashes["preHash"]))
			m.Hashes.Gcid = anyString(hashes["gcid"])
			m.Hashes.Sha115 = strings.ToUpper(firstNonEmpty(anyString(hashes["sha115"]), anyString(hashes["sha1_115"])))
			if m.FileName == "" || m.FileSize <= 0 {
				return m, fmt.Errorf("CAS V2 清单缺少 fileName/fileSize")
			}
			return m, nil
		}
	}

	// V1 升级
	v1, err := ParseManifest(obj)
	if err != nil {
		return CasManifestV2{}, err
	}
	return UpgradeToV2(v1), nil
}

// isFlatManifest 判断是否 cloud-auto-save-x 扁平格式。
// 特征：顶层含 content_hash/hash_algorithm/schema_version，或 content_* 扁平哈希字段
// （content_file_md5/content_pre_hash/content_sha256/content_sha1/content_slice_md5）。
func isFlatManifest(obj map[string]any) bool {
	if _, ok := obj["content_hash"]; ok {
		return true
	}
	if _, ok := obj["hash_algorithm"]; ok {
		return true
	}
	if _, ok := obj["schema_version"]; ok {
		return true
	}
	for k := range obj {
		if strings.HasPrefix(k, "content_") {
			return true
		}
	}
	return false
}

// parseFlatManifestV2 解析 cloud-auto-save-x 扁平格式：
// {"content_hash":"...","hash_algorithm":"SHA256","sha256":"...","sliceMd5":"...","schema_version":2,"name","size"}
func parseFlatManifestV2(obj map[string]any) CasManifestV2 {
	name := anyString(obj["name"])
	if name == "" {
		name = anyString(obj["fileName"])
	}
	size := int64(anyFloat(obj["size"]))
	if size == 0 {
		size = int64(anyFloat(obj["fileSize"]))
	}
	m := CasManifestV2{
		Version:     2,
		FileName:    name,
		FileSize:    size,
		SourceDrive: anyString(obj["drive_type"]),
		CreatedAt:   anyString(obj["create_time"]),
	}
	if m.CreatedAt == "" {
		m.CreatedAt = anyString(obj["createdAt"])
	}
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().Format(time.RFC3339)
	}
	// 顶层扁平哈希字段（小写 + 驼峰双兼容）
	m.Hashes.Sha1 = strings.ToUpper(firstNonEmpty(anyString(obj["sha1"]), anyString(obj["content_sha1"])))
	m.Hashes.Sha256 = strings.ToUpper(firstNonEmpty(anyString(obj["sha256"]), anyString(obj["content_sha256"])))
	m.Hashes.FileMd5 = strings.ToUpper(firstNonEmpty(
		anyString(obj["content_file_md5"]), anyString(obj["file_md5"]), anyString(obj["md5"]), anyString(obj["content_md5"])))
	m.Hashes.SliceMd5 = strings.ToUpper(firstNonEmpty(anyString(obj["sliceMd5"]), anyString(obj["content_slice_md5"])))
	m.Hashes.PreHash = strings.ToUpper(firstNonEmpty(anyString(obj["preHash"]), anyString(obj["content_pre_hash"])))
	m.Hashes.Gcid = anyString(obj["gcid"])
	// content_hash 兜底：无 hash_algorithm 时按长度推断
	if ch := anyString(obj["content_hash"]); ch != "" {
		algo := strings.ToUpper(anyString(obj["hash_algorithm"]))
		switch algo {
		case "SHA256":
			if m.Hashes.Sha256 == "" {
				m.Hashes.Sha256 = strings.ToUpper(ch)
			}
		case "MD5":
			if m.Hashes.FileMd5 == "" {
				m.Hashes.FileMd5 = strings.ToUpper(ch)
			}
		case "SHA1":
			if m.Hashes.Sha1 == "" {
				m.Hashes.Sha1 = strings.ToUpper(ch)
			}
		default:
			// 按长度推断兜底
			switch len(ch) {
			case 32:
				if m.Hashes.FileMd5 == "" {
					m.Hashes.FileMd5 = strings.ToUpper(ch)
				}
			case 40:
				if m.Hashes.Sha1 == "" {
					m.Hashes.Sha1 = strings.ToUpper(ch)
				}
			case 64:
				if m.Hashes.Sha256 == "" {
					m.Hashes.Sha256 = strings.ToUpper(ch)
				}
			}
		}
	}
	return m
}

// UpgradeToV2 V1 升级 V2
func UpgradeToV2(m CasManifest) CasManifestV2 {
	v := CasManifestV2{
		Version:     2,
		FileName:    m.FileName,
		FileSize:    m.FileSize,
		SourceDrive: "cloud189",
		CreatedAt:   m.UploadTime,
	}
	if v.CreatedAt == "" {
		v.CreatedAt = time.Now().Format(time.RFC3339)
	}
	v.Hashes.FileMd5 = strings.ToUpper(m.FileMd5)
	v.Hashes.SliceMd5 = strings.ToUpper(m.SliceMd5)
	return v
}

// EncodeManifestV1 编码 V1 JSON
func EncodeManifestV1(m CasManifest) string {
	if m.UploadTime == "" {
		m.UploadTime = time.Now().Format(time.RFC3339)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return string(b)
}

// EncodeManifestV2 编码 V2 JSON
func EncodeManifestV2(m CasManifestV2) string {
	b, _ := json.MarshalIndent(m, "", "  ")
	return string(b)
}

// IsCasFileName 判断 .cas 文件名
func IsCasFileName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".cas")
}

// VirtualFileName 去掉 .cas 后缀
func VirtualFileName(casName string) string {
	if IsCasFileName(casName) {
		return casName[:len(casName)-4]
	}
	return casName
}

func compactBase64(s string) string {
	s = strings.Join(strings.Fields(s), "")
	if pad := (4 - len(s)%4) % 4; pad > 0 {
		s += strings.Repeat("=", pad)
	}
	return s
}

var md5Re = regexp.MustCompile(`^[A-Fa-f0-9]{32}$`)

func isMd5(s string) bool {
	return md5Re.MatchString(s)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// anyFloat 宽容数值提取（float64/int/string）
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
