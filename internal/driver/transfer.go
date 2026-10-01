package driver

import (
	"context"
	"errors"
	"strings"

	"litepan/internal/domain"
)

type RapidUploadRequest struct {
	ParentID  string
	FileName  string
	Method    string
	Hash      string
	Size      int64
	Duplicate int
}

type RapidUploadResult struct {
	Reuse    bool
	FileID   string
	ParentID string
	Message  string
}

type RapidUploader interface {
	RapidUploadByHash(ctx context.Context, req RapidUploadRequest) (*RapidUploadResult, error)
}

// RapidUploadByHashesRequest CAS 五哈希秒传请求（多网盘多维指纹模型）。
type RapidUploadByHashesRequest struct {
	ParentID string
	FileName string
	Size     int64
	// Hashes 五哈希指纹（domain.HashType → 值），驱动按自身秒传特征取用。
	Hashes map[domain.HashType]string
}

// MultiHasher CAS 秒传：按五哈希指纹秒传（189:fileMd5+sliceMd5 / 139:sha256 / 夸克:md5+preHash）。
type MultiHasher interface {
	RapidUploadByHashes(ctx context.Context, req RapidUploadByHashesRequest) (*RapidUploadResult, error)
}

type RapidUploadProber interface {
	SupportsRapidUploadProbe(method string) bool
	ProbeRapidUploadByHash(ctx context.Context, req RapidUploadRequest) (*RapidUploadResult, error)
}

// ShareLinkSaveRequest 分享链接转存请求（TG 频道订阅 / 资源一键转存共用）。
type ShareLinkSaveRequest struct {
	// ShareURL 分享页地址或分享标识（驱动自解析 key/id）。
	ShareURL string
	// SharePwd 提取码/访问码（无则空）。
	SharePwd string
	// TargetParentID 目标父目录 ID（空表示根目录）。
	TargetParentID string
}

// ShareLinkSaveResult 分享转存结果。
type ShareLinkSaveResult struct {
	Title    string
	Total    int
	ParentID string
	Message  string
}

// ShareLinkSaver 分享链接转存能力（支持分享链接一键转存的驱动实现）。
type ShareLinkSaver interface {
	SaveShareLink(ctx context.Context, req ShareLinkSaveRequest) (*ShareLinkSaveResult, error)
}

// ShareDirProbe 分享标题探测能力（只读，不落盘）。
//
// 存在的原因：TG 频道聚合帖常把多部影片塞进一条帖子，若只按关键词命中就转存，
// 会把无关剧集整包搬进用户目录（旧仓库「无上神帝」误转事故）。转存前先取分享内
// 顶级文件名复核，是成本最低的兜底。单独抽成接口而非复用 SaveShareLink：
// 探测必须无副作用，否则复核本身就产生了误转。
type ShareDirProbe interface {
	// ProbeShareTitle 返回分享内第一个顶级条目的名称（目录名或文件名）。
	ProbeShareTitle(ctx context.Context, req ShareLinkSaveRequest) (string, error)
}

type rapidProbeTerminalError struct{ err error }

func (e *rapidProbeTerminalError) Error() string { return e.err.Error() }
func (e *rapidProbeTerminalError) Unwrap() error { return e.err }

func StopRapidProbe(err error) error {
	if err == nil || IsRapidProbeTerminal(err) {
		return err
	}
	return &rapidProbeTerminalError{err: err}
}

func IsRapidProbeTerminal(err error) bool {
	var target *rapidProbeTerminalError
	return errors.As(err, &target)
}

type TransferHashResolver interface {
	ResolveTransferHash(ctx context.Context, item *domain.FileItem, method string, allowStream bool) (string, error)
}

func NormalizeTransferHash(method, value string) string {
	text := strings.ToLower(strings.TrimSpace(value))
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "md5":
		if len(text) != 32 {
			return ""
		}
		for _, ch := range text {
			if ch < '0' || (ch > '9' && ch < 'a') || ch > 'f' {
				return ""
			}
		}
		return text
	case "sha1":
		if len(text) != 40 {
			return ""
		}
		for _, ch := range text {
			if ch < '0' || (ch > '9' && ch < 'a') || ch > 'f' {
				return ""
			}
		}
		return text
	default:
		return ""
	}
}

func HashFromItem(item *domain.FileItem, method string) string {
	if item == nil || item.Hash == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "md5":
		return NormalizeTransferHash("md5", item.Hash[domain.HashMD5])
	case "sha1":
		return NormalizeTransferHash("sha1", item.Hash[domain.HashSHA1])
	default:
		return ""
	}
}
