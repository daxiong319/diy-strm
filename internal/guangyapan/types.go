package guangyapan

import "time"

// File 光鸭云盘文件信息（逆向 API 字段）
type File struct {
	FileID   string `json:"fileId"`
	ParentID string `json:"parentId"`
	FileName string `json:"fileName"`
	FileSize int64  `json:"fileSize"`
	ResType  int    `json:"resType"` // 2=目录
	CTime    int64  `json:"ctime"`
	UTime    int64  `json:"utime"`
}

// IsDir 是否为目录
func (f File) IsDir() bool {
	return f.ResType == 2
}

// GetID 返回文件 ID
func (f File) GetID() string {
	return f.FileID
}

// GetName 返回文件名称
func (f File) GetName() string {
	return f.FileName
}

// GetSize 返回文件大小
func (f File) GetSize() int64 {
	return f.FileSize
}

// GetMTime 返回修改时间（秒级时间戳）
func (f File) GetMTime() int64 {
	return f.UTime
}

// GetMTimeTime 返回修改时间
func (f File) GetMTimeTime() time.Time {
	if f.UTime <= 0 {
		return time.Time{}
	}
	return time.Unix(f.UTime, 0)
}

// Files 文件列表响应
type Files struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Total int    `json:"total"`
		List  []File `json:"list"`
	} `json:"data"`
}

// DownloadURLResp 下载直链响应
type DownloadURLResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		SignedURL   string `json:"signedURL"`
		DownloadURL string `json:"downloadUrl"`
	} `json:"data"`
}

// TokenResp 令牌刷新/登录响应
type TokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Sub          string `json:"sub"`
	Error        string `json:"error"`
	ErrorCode    int    `json:"error_code"`
	ErrorDesc    string `json:"error_description"`
}

// UserMeResp 用户信息响应
type UserMeResp struct {
	Sub string `json:"sub"`
}

// CommonResp 通用响应外壳（创建目录/删除等）
type CommonResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		FileID string `json:"fileId"`
		TaskID string `json:"taskId"`
	} `json:"data"`
}

// TaskStatusResp 异步任务状态响应
type TaskStatusResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Status int `json:"status"`
	} `json:"data"`
}
