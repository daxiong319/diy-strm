package pan123

import (
	"strconv"
	"time"
)

// File 123 云盘文件信息（逆向 web API 字段）
type File struct {
	FileName    string    `json:"FileName"`
	Size        int64     `json:"Size"`
	UpdateAt    time.Time `json:"UpdateAt"`
	FileId      int64     `json:"FileId"`
	Type        int       `json:"Type"` // 1=目录
	Etag        string    `json:"Etag"`
	S3KeyFlag   string    `json:"S3KeyFlag"`
	DownloadUrl string    `json:"DownloadUrl"`
	IsLock      bool      `json:"IsLock"`
}

// IsDir 是否为目录
func (f File) IsDir() bool {
	return f.Type == 1
}

// GetID 返回文件 ID 字符串
func (f File) GetID() string {
	return strconv.FormatInt(f.FileId, 10)
}

// Files 文件列表响应
type Files struct {
	Data struct {
		Next     string `json:"Next"`
		Total    int    `json:"Total"`
		InfoList []File `json:"InfoList"`
	} `json:"data"`
}

// UploadResp 上传请求响应
type UploadResp struct {
	Data struct {
		AccessKeyId     string `json:"AccessKeyId"`
		Bucket          string `json:"Bucket"`
		Key             string `json:"Key"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
		FileId          int64  `json:"FileId"`
		Reuse           bool   `json:"Reuse"`
		EndPoint        string `json:"EndPoint"`
		StorageNode     string `json:"StorageNode"`
		UploadId        string `json:"UploadId"`
	} `json:"data"`
}

// S3PreSignedURLs 预签名上传 URL 响应
type S3PreSignedURLs struct {
	Data struct {
		PreSignedUrls map[string]string `json:"presignedUrls"`
	} `json:"data"`
}

// DownloadInfoResp 下载信息响应
type DownloadInfoResp struct {
	Data struct {
		DownloadUrl string `json:"DownloadUrl"`
	} `json:"data"`
}

// UserInfoResp 用户信息响应
type UserInfoResp struct {
	Data struct {
		UserId   int64  `json:"userId"`
		Username string `json:"userName"`
		Nickname string `json:"nickName"`
	} `json:"data"`
}

// BaseResp 统一响应外壳
type BaseResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
