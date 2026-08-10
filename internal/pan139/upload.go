package pan139

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// UploadFile 上传文件到指定目录
// sha256Hex 为文件完整 SHA256（十六进制小写）；云端已存在同哈希文件时秒传命中（rapid=true，无需读取 reader）
// reader 为文件内容读取器；progress 可选，回调已上传字节数
// 返回云端文件 ID、最终文件名（可能被 auto_rename 改名为 name(1) 等）
func (c *Client) UploadFile(ctx context.Context, parentFileID, name string, size int64, sha256Hex string, reader io.Reader, progress func(uploaded int64)) (fileID, finalName string, rapid bool, err error) {
	if strings.TrimSpace(name) == "" {
		return "", "", false, fmt.Errorf("中国移动云盘上传文件名不能为空")
	}
	if strings.TrimSpace(parentFileID) == "" {
		parentFileID = "/"
	}
	partInfos := buildPartInfos(size)

	var createResp personalUploadResp
	if err := c.Request(ctx, "/file/create", map[string]interface{}{
		"contentHash":          sha256Hex,
		"contentHashAlgorithm": "SHA256",
		"contentType":          "application/octet-stream",
		"parallelUpload":       false,
		"partInfos":            partInfos,
		"size":                 size,
		"parentFileId":         parentFileID,
		"name":                 name,
		"type":                 "file",
		"fileRenameMode":       "auto_rename",
	}, &createResp); err != nil {
		return "", "", false, err
	}
	if err := checkUploadCode(createResp.Code); err != nil {
		return "", "", false, fmt.Errorf("中国移动云盘创建上传任务失败：%v", err)
	}
	data := createResp.Data

	// 云端已存在同哈希同名文件，无需上传（秒传命中）
	if data.Exist {
		fileID = data.FileId
		if fileID == "" {
			fileID = c.findFileIDByName(ctx, parentFileID, name)
		}
		if fileID == "" {
			return "", data.FileName, true, fmt.Errorf("中国移动云盘检测到文件已存在，但未返回文件 ID")
		}
		return fileID, data.FileName, true, nil
	}

	// 无分片上传地址：仅当 RapidUpload 命中（秒传成功）时合法
	if len(data.PartInfos) == 0 {
		if data.RapidUpload {
			fileID = data.FileId
			if fileID == "" {
				fileID = c.findFileIDByName(ctx, parentFileID, name)
			}
			return fileID, data.FileName, true, nil
		}
		return "", "", false, fmt.Errorf("中国移动云盘创建上传任务失败：未返回分片上传地址")
	}

	// 分片数量超过首批发出的 100 片时，分批获取后续上传地址
	uploadPartInfos := data.PartInfos
	for start := 100; start < len(partInfos); start += 100 {
		end := start + 100
		if end > len(partInfos) {
			end = len(partInfos)
		}
		var urlResp personalUploadUrlResp
		if err := c.Request(ctx, "/file/getUploadUrl", map[string]interface{}{
			"fileId":    data.FileId,
			"uploadId":  data.UploadId,
			"partInfos": partInfos[start:end],
			"commonAccountInfo": map[string]interface{}{
				"account":     c.GetAccount(),
				"accountType": 1,
			},
		}, &urlResp); err != nil {
			return "", "", false, err
		}
		if err := checkUploadCode(urlResp.Code); err != nil {
			return "", "", false, fmt.Errorf("中国移动云盘获取分片上传地址失败：%v", err)
		}
		uploadPartInfos = append(uploadPartInfos, urlResp.Data.PartInfos...)
	}

	// 逐片上传
	var uploaded int64
	if progress != nil {
		progress(0)
	}
	for _, part := range uploadPartInfos {
		if err := ctx.Err(); err != nil {
			return "", "", false, err
		}
		index := part.PartNumber - 1
		if index < 0 || index >= len(partInfos) {
			return "", "", false, fmt.Errorf("中国移动云盘分片序号异常：partNumber=%d", part.PartNumber)
		}
		var partSize int64
		if size < PartSize {
			partSize = size
		} else if index == len(partInfos)-1 {
			partSize = size - PartSize*int64(len(partInfos)-1)
		} else {
			partSize = PartSize
		}
		partReader := io.LimitReader(reader, partSize)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, part.UploadUrl, partReader)
		if err != nil {
			return "", "", false, fmt.Errorf("中国移动云盘构造分片上传请求失败：%v", err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Content-Length", fmt.Sprint(partSize))
		req.Header.Set("Origin", "https://yun.139.com")
		req.Header.Set("Referer", "https://yun.139.com/")
		res, err := c.client.Client().Do(req)
		if err != nil {
			return "", "", false, fmt.Errorf("中国移动云盘上传分片 %d 失败：%v", part.PartNumber, err)
		}
		bodyText, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return "", "", false, fmt.Errorf("中国移动云盘上传分片 %d 失败：status=%d body=%s", part.PartNumber, res.StatusCode, string(bodyText))
		}
		uploaded += partSize
		if progress != nil {
			progress(uploaded)
		}
	}

	// 完成上传
	var completeResp personalCompleteResp
	if err := c.Request(ctx, "/file/complete", map[string]interface{}{
		"contentHash":          sha256Hex,
		"contentHashAlgorithm": "SHA256",
		"fileId":               data.FileId,
		"uploadId":             data.UploadId,
	}, &completeResp); err != nil {
		return "", "", false, err
	}
	if err := checkUploadCode(completeResp.Code); err != nil {
		return "", "", false, fmt.Errorf("中国移动云盘完成上传失败：%v", err)
	}
	fileID = completeResp.Data.FileIdNew
	if fileID == "" {
		fileID = completeResp.Data.FileId
	}
	finalName = completeResp.Data.FileName
	if finalName == "" {
		finalName = name
	}
	return fileID, finalName, false, nil
}
