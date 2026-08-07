package pan139

import "time"

// File 中国移动云盘（139）文件信息（新个人盘 API 字段）
type File struct {
	FileID   string `json:"fileId"`
	FileName string `json:"name"`
	FileSize int64  `json:"size"`
	Type     string `json:"type"` // "folder"=目录
	CTime    int64  `json:"-"`    // 创建时间（Unix 秒，由 createdAt 解析）
	UTime    int64  `json:"-"`    // 修改时间（Unix 秒，由 updatedAt 解析）
}

// IsDir 是否为目录
func (f File) IsDir() bool {
	return f.Type == "folder"
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

// fileItem 新个人盘列表原始条目（时间字段为字符串，需转换）
type fileItem struct {
	FileId    string `json:"fileId"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Type      string `json:"type"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// toFile 转换原始条目为 File
func (i fileItem) toFile() File {
	return File{
		FileID:   i.FileId,
		FileName: i.Name,
		FileSize: i.Size,
		Type:     i.Type,
		CTime:    parseTime(i.CreatedAt),
		UTime:    parseTime(i.UpdatedAt),
	}
}

// BaseResp 新个人盘接口通用响应
type BaseResp struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ListResp 文件列表响应
type ListResp struct {
	BaseResp
	Data struct {
		Items          []fileItem `json:"items"`
		NextPageCursor string     `json:"nextPageCursor"`
	} `json:"data"`
}

// DownloadURLResp 下载直链响应
type DownloadURLResp struct {
	BaseResp
	Data struct {
		CdnURL string `json:"cdnUrl"`
		URL    string `json:"url"`
	} `json:"data"`
}

// CreateResp 创建目录响应
type CreateResp struct {
	BaseResp
	Data struct {
		FileId   string `json:"fileId"`
		FileName string `json:"fileName"`
	} `json:"data"`
}

// RefreshTokenResp 令牌刷新响应（XML 格式）
type RefreshTokenResp struct {
	Return string `xml:"return"`
	Token  string `xml:"token"`
	Desc   string `xml:"desc"`
}

// RoutePolicyResp 路由策略响应
type RoutePolicyResp struct {
	BaseResp
	Data struct {
		RoutePolicyList []struct {
			ModName string `json:"modName"`
			HttpUrl string `json:"httpUrl"`
			HttpsUrl string `json:"httpsUrl"`
		} `json:"routePolicyList"`
	} `json:"data"`
}
