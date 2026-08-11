package pan139

import (
	"context"
	"fmt"
)

// PartSize 中国移动云盘分片大小（100MB）
const PartSize = 100 * 1024 * 1024

// personalUploadResp /file/create 响应（与 alist 139Yun 驱动协议一致）
type personalUploadResp struct {
	Code string `json:"code"`
	Data struct {
		FileId    string `json:"fileId"`
		FileName  string `json:"fileName"`
		PartInfos []struct {
			PartNumber int    `json:"partNumber"`
			UploadUrl  string `json:"uploadUrl"`
		} `json:"partInfos"`
		Exist       bool   `json:"exist"`
		RapidUpload bool   `json:"rapidUpload"`
		UploadId    string `json:"uploadId"`
	} `json:"data"`
}

// personalUploadUrlResp /file/getUploadUrl 响应
type personalUploadUrlResp struct {
	Code string `json:"code"`
	Data struct {
		FileId    string `json:"fileId"`
		UploadId  string `json:"uploadId"`
		PartInfos []struct {
			PartNumber int    `json:"partNumber"`
			UploadUrl  string `json:"uploadUrl"`
		} `json:"partInfos"`
	} `json:"data"`
}

// personalCompleteResp /file/complete 响应
type personalCompleteResp struct {
	Code string `json:"code"`
	Data struct {
		FileId    string `json:"fileId"`
		FileIdNew string `json:"fileIdNew"`
		FileName  string `json:"fileName"`
	} `json:"data"`
}

// buildPartInfos 按 100MB 分片构造 partInfos
func buildPartInfos(size int64) []map[string]interface{} {
	count := (size + PartSize - 1) / PartSize
	if count < 1 {
		count = 1
	}
	infos := make([]map[string]interface{}, 0, count)
	for i := int64(1); i <= count; i++ {
		var partSize int64 = PartSize
		if i == count && size%PartSize != 0 {
			partSize = size % PartSize
		}
		infos = append(infos, map[string]interface{}{"partNumber": i, "partSize": partSize})
	}
	return infos
}

func checkUploadCode(code string) error {
	if code != "" && code != "0" && code != "0000" {
		return fmt.Errorf("code=%s", code)
	}
	return nil
}

// findFileIDByName 在目录中按文件名查找文件 ID（秒传命中但未返回 fileId 时兜底）
func (c *Client) findFileIDByName(ctx context.Context, parentFileID, name string) string {
	files, err := c.GetFiles(ctx, parentFileID)
	if err != nil {
		return ""
	}
	for _, file := range files {
		if file.FileName == name {
			return file.FileID
		}
	}
	return ""
}
