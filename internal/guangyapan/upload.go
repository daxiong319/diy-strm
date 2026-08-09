package guangyapan

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"diy-strm/internal/helpers"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

// 光鸭云盘上传相关 API 路径常量（逆向自 Web 端，协议参考 alist GuangYaPan 驱动）
const (
	APIGetResCenterToken   = "/userres/v1/get_res_center_token"
	APICheckCanFlashUpload = "/userres/v1/check_can_flash_upload"
	APIGetInfoByTaskID     = "/userres/v1/file/get_info_by_task_id"

	// FlashUploadMinSize 计算 GCID 并尝试秒传的最小文件大小（小文件直接带 MD5）
	FlashUploadMinSize = 1024 * 1024
	// rapidUploadCode get_res_center_token 返回 156 表示小文件秒传命中
	rapidUploadCode = 156
	// cloudTaskPendingCode get_info_by_task_id 返回 147 表示云端仍在入库
	cloudTaskPendingCode = 147
)

// uploadTokenData get_res_center_token 返回的上传凭证
type uploadTokenData struct {
	TaskID string `json:"taskId"`
	Creds  struct {
		AccessKeyID     string `json:"accessKeyID"`
		SecretAccessKey string `json:"secretAccessKey"`
		AccessKeySecret string `json:"accessKeySecret"`
		SessionToken    string `json:"sessionToken"`
	} `json:"creds"`
	Region       string `json:"region"`
	BucketName   string `json:"bucketName"`
	FullEndpoint string `json:"fullEndPoint"`
	Endpoint     string `json:"endPoint"`
	ObjectPath   string `json:"objectPath"`
	N            string `json:"n"`
	Provider     string `json:"provider"`
}

type getResCenterTokenResp struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data uploadTokenData `json:"data"`
}

type checkCanFlashUploadResp struct {
	Code int `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		CanFlashUpload bool   `json:"canFlashUpload"`
		TaskID         string `json:"taskId"`
	} `json:"data"`
}

type getInfoByTaskIDResp struct {
	Code int `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		FileID string `json:"fileId"`
	} `json:"data"`
}

// GetResCenterToken 申请上传凭证，返回凭证与小文件秒传是否命中（code 156）。
func (c *Client) GetResCenterToken(ctx context.Context, parentID, name string, size int64, fileMD5 string) (*uploadTokenData, bool, error) {
	res := map[string]interface{}{
		"fileSize": size,
	}
	if strings.TrimSpace(fileMD5) != "" {
		res["md5"] = fileMD5
	}
	var out getResCenterTokenResp
	if err := c.Request(ctx, APIGetResCenterToken, map[string]interface{}{
		"capacity": 2,
		"name":     name,
		"res":      res,
		"parentId": parentID,
	}, &out); err != nil {
		return nil, false, err
	}
	if !isSuccess(out.Code, out.Msg) && out.Code != rapidUploadCode {
		return nil, false, fmt.Errorf("光鸭云盘申请上传凭证失败：code=%d msg=%s", out.Code, out.Msg)
	}
	if strings.TrimSpace(out.Data.TaskID) == "" {
		return nil, false, errors.New("光鸭云盘申请上传凭证失败：未返回任务 ID")
	}
	return &out.Data, out.Code == rapidUploadCode, nil
}

// CheckCanFlashUpload 检查大文件 GCID 是否可以秒传。
// 命中时返回 canFlash=true，并可能返回替换后的任务 ID。
func (c *Client) CheckCanFlashUpload(ctx context.Context, taskID, gcid string) (bool, string, error) {
	var out checkCanFlashUploadResp
	if err := c.Request(ctx, APICheckCanFlashUpload, map[string]interface{}{
		"taskId": taskID,
		"gcid":   gcid,
	}, &out); err != nil {
		return false, "", err
	}
	if !isSuccess(out.Code, out.Msg) {
		return false, "", fmt.Errorf("光鸭云盘检查秒传失败：code=%d msg=%s", out.Code, out.Msg)
	}
	return out.Data.CanFlashUpload, strings.TrimSpace(out.Data.TaskID), nil
}

// gcidChunkSize 计算 GCID 分块大小（与官方一致：文件越大分块越大）
func gcidChunkSize(size int64) int64 {
	switch {
	case size <= 0x08000000:
		return 256 * 1024
	case size <= 0x10000000:
		return 512 * 1024
	case size <= 0x20000000:
		return 1024 * 1024
	default:
		return 2 * 1024 * 1024
	}
}

// CalculateGCID 计算光鸭秒传指纹：分块 SHA1 后再对块哈希整体 SHA1，大写十六进制。
func CalculateGCID(filePath string, size int64) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("打开文件失败：%v", err)
	}
	defer file.Close()
	chunkSize := gcidChunkSize(size)
	buf := make([]byte, chunkSize)
	outer := sha1.New()
	var position int64
	for position < size {
		length := chunkSize
		if remaining := size - position; remaining < chunkSize {
			length = remaining
		}
		n, err := file.ReadAt(buf[:length], position)
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("读取文件分片失败：%v", err)
		}
		inner := sha1.Sum(buf[:n])
		outer.Write(inner[:])
		position += int64(n)
	}
	return strings.ToUpper(hex.EncodeToString(outer.Sum(nil))), nil
}

// uploadToOSS 使用短期 STS 凭证把本地文件分片上传到 OSS。
func (c *Client) uploadToOSS(ctx context.Context, token *uploadTokenData, localPath string, progress func(done, total int64)) error {
	endpoint := strings.TrimSpace(token.FullEndpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(token.Endpoint)
	}
	if endpoint == "" || strings.TrimSpace(token.BucketName) == "" || strings.TrimSpace(token.ObjectPath) == "" {
		return errors.New("光鸭云盘上传凭证不完整")
	}
	accessKeySecret := strings.TrimSpace(token.Creds.SecretAccessKey)
	if accessKeySecret == "" {
		accessKeySecret = strings.TrimSpace(token.Creds.AccessKeySecret)
	}
	if accessKeySecret == "" {
		return errors.New("光鸭云盘上传凭证缺少密钥")
	}
	fileInfo, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("读取待上传文件信息失败：%v", err)
	}
	size := fileInfo.Size()
	if size <= 0 {
		return errors.New("待上传文件为空")
	}

	cfg := oss.LoadDefaultConfig().
		WithCredentialsProvider(credentials.NewStaticCredentialsProvider(token.Creds.AccessKeyID, accessKeySecret, token.Creds.SessionToken)).
		WithRegion(token.Region).
		WithEndpoint(endpoint)
	client := oss.NewClient(cfg)

	bucket := strings.TrimSpace(token.BucketName)
	objectKey := strings.TrimSpace(token.ObjectPath)

	// 小文件直接用 PutObject，大文件走 multipart
	if size <= FlashUploadMinSize {
		file, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("打开待上传文件失败：%v", err)
		}
		defer file.Close()
		_, err = client.PutObject(ctx, &oss.PutObjectRequest{
			Bucket:        oss.Ptr(bucket),
			Key:           oss.Ptr(objectKey),
			Body:          file,
			ContentLength: oss.Ptr(size),
		})
		if err != nil {
			return fmt.Errorf("OSS 上传失败：%v", err)
		}
		if progress != nil {
			progress(size, size)
		}
		return nil
	}

	partSize := int64(32 * 1024 * 1024)
	totalParts := int((size + partSize - 1) / partSize)
	if totalParts == 0 {
		totalParts = 1
	}
	initResult, err := client.InitiateMultipartUpload(ctx, &oss.InitiateMultipartUploadRequest{
		Bucket: oss.Ptr(bucket),
		Key:    oss.Ptr(objectKey),
	})
	if err != nil {
		return fmt.Errorf("初始化 OSS multipart 失败：%v", err)
	}
	if initResult.UploadId == nil || *initResult.UploadId == "" {
		return errors.New("初始化 OSS multipart 返回空 upload_id")
	}
	uploadID := *initResult.UploadId

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开待上传文件失败：%v", err)
	}
	defer file.Close()

	parts := make([]oss.UploadPart, 0, totalParts)
	var uploadedBytes int64
	for partNumber := 1; partNumber <= totalParts; partNumber++ {
		offset := int64(partNumber-1) * partSize
		length := min(partSize, size-offset)
		reader := io.NewSectionReader(file, offset, length)
		result, err := client.UploadPart(ctx, &oss.UploadPartRequest{
			Bucket:        oss.Ptr(bucket),
			Key:           oss.Ptr(objectKey),
			PartNumber:    int32(partNumber),
			UploadId:      oss.Ptr(uploadID),
			Body:          reader,
			ContentLength: oss.Ptr(length),
		})
		if err != nil {
			return fmt.Errorf("OSS 分片 %d 上传失败：%v", partNumber, err)
		}
		if result.ETag == nil || *result.ETag == "" {
			return fmt.Errorf("OSS 分片 %d 返回空 ETag", partNumber)
		}
		parts = append(parts, oss.UploadPart{
			PartNumber: int32(partNumber),
			ETag:       result.ETag,
		})
		uploadedBytes += length
		if progress != nil {
			progress(uploadedBytes, size)
		}
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})
	if _, err := client.CompleteMultipartUpload(ctx, &oss.CompleteMultipartUploadRequest{
		Bucket:   oss.Ptr(bucket),
		Key:      oss.Ptr(objectKey),
		UploadId: oss.Ptr(uploadID),
		CompleteMultipartUpload: &oss.CompleteMultipartUpload{
			Parts: parts,
		},
	}); err != nil {
		return fmt.Errorf("完成 OSS multipart 失败：%v", err)
	}
	return nil
}

// waitFileUploaded 轮询云端入库结果直到返回 fileId。
func (c *Client) waitFileUploaded(ctx context.Context, taskID string) (string, error) {
	const (
		pollInterval = 1500 * time.Millisecond
		maxAttempt   = 600
	)
	for attempt := 0; attempt < maxAttempt; attempt++ {
		var out getInfoByTaskIDResp
		if err := c.Request(ctx, APIGetInfoByTaskID, map[string]interface{}{
			"taskId": taskID,
		}, &out); err != nil {
			return "", err
		}
		if fileID := strings.TrimSpace(out.Data.FileID); fileID != "" {
			return fileID, nil
		}
		if out.Code != cloudTaskPendingCode {
			return "", fmt.Errorf("光鸭云盘查询上传结果失败：code=%d msg=%s", out.Code, out.Msg)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return "", errors.New("光鸭云盘上传云端入库超时")
}

// UploadFile 上传本地文件到光鸭云盘指定目录。
// 内部自动尝试秒传：<1MiB 带 MD5 直传命中，≥1MiB 计算 GCID 检查 flash 秒传，未命中回退 OSS 分片上传。
// 返回远端文件 ID 与是否秒传命中。
func (c *Client) UploadFile(ctx context.Context, localPath, parentID string, progress func(done, total int64)) (fileID string, rapid bool, err error) {
	fileInfo, err := os.Stat(localPath)
	if err != nil {
		return "", false, fmt.Errorf("读取待上传文件信息失败：%v", err)
	}
	size := fileInfo.Size()
	if size <= 0 {
		return "", false, errors.New("待上传文件为空")
	}
	var fileMD5 string
	if size < FlashUploadMinSize {
		fileMD5, err = helpers.CalculateFileMD5(localPath)
		if err != nil {
			return "", false, err
		}
	}
	token, smallRapid, err := c.GetResCenterToken(ctx, parentID, filepath.Base(localPath), size, fileMD5)
	if err != nil {
		return "", false, err
	}
	taskID := token.TaskID
	flashHit := smallRapid
	if !flashHit && size >= FlashUploadMinSize {
		gcid, gcidErr := CalculateGCID(localPath, size)
		if gcidErr != nil {
			helpers.AppLogger.Warnf("[上传] 计算光鸭 GCID 失败，继续普通上传：%v", gcidErr)
		} else {
			canFlash, newTaskID, flashErr := c.CheckCanFlashUpload(ctx, taskID, gcid)
			if flashErr != nil {
				helpers.AppLogger.Warnf("[上传] 光鸭秒传校验失败，继续普通上传：%v", flashErr)
			} else if canFlash {
				flashHit = true
				if newTaskID != "" {
					taskID = newTaskID
				}
			}
		}
	}
	if flashHit {
		if progress != nil {
			progress(size, size)
		}
		fileID, err = c.waitFileUploaded(ctx, taskID)
		return fileID, true, err
	}
	if err := c.uploadToOSS(ctx, token, localPath, progress); err != nil {
		return "", false, err
	}
	fileID, err = c.waitFileUploaded(ctx, taskID)
	return fileID, false, err
}
