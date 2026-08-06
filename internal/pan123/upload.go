package pan123

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"resty.dev/v3"
)

// UploadProgress 上传进度回调（done/total 单位字节）
type UploadProgress func(done, total int64)

// MD5File 计算文件 MD5（流式计算）
func MD5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// UploadFile 上传文件到指定目录
// 流程：upload_request 获取凭证 → 秒传判断 → S3 直传或预签名分片上传 → 完成
// 返回上传结果（fileId 等）
func (c *Client) UploadFile(ctx context.Context, filePath string, parentFileId string, progress UploadProgress) (*UploadResp, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("获取文件信息失败：%w", err)
	}
	size := fileInfo.Size()
	if size <= 0 {
		return nil, fmt.Errorf("文件大小为 0，无法上传：%s", filePath)
	}

	etag, err := MD5File(filePath)
	if err != nil {
		return nil, fmt.Errorf("计算文件 MD5 失败：%w", err)
	}

	data := map[string]interface{}{
		"driveId":      0,
		"duplicate":    2, // 2->覆盖 1->重命名 0->默认
		"etag":         etag,
		"fileName":     fileInfo.Name(),
		"parentFileId": parentFileId,
		"size":         size,
		"type":         0,
	}
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	var uploadResp UploadResp
	respBody, err := c.Request(ctx, c.api("/file/upload_request"), http.MethodPost, func(req *resty.Request) {
		req.SetBody(body).SetContext(ctx)
	})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(respBody, &uploadResp); err != nil {
		return nil, fmt.Errorf("解析上传请求响应失败：%w", err)
	}

	// 秒传成功或无需上传
	if uploadResp.Data.Reuse || uploadResp.Data.Key == "" {
		if progress != nil {
			progress(size, size)
		}
		return &uploadResp, nil
	}

	// 有 S3 凭证走 AWS SDK 直传
	if uploadResp.Data.AccessKeyId != "" && uploadResp.Data.SecretAccessKey != "" && uploadResp.Data.SessionToken != "" {
		if err := c.uploadViaS3(ctx, &uploadResp, filePath, size, progress); err != nil {
			return nil, err
		}
		// 完成上传（v1）
		completeData := map[string]interface{}{
			"fileId": uploadResp.Data.FileId,
		}
		completeBody, err := json.Marshal(completeData)
		if err != nil {
			return nil, err
		}
		if _, err := c.Request(ctx, c.api("/file/upload_complete"), http.MethodPost, func(req *resty.Request) {
			req.SetBody(completeBody).SetContext(ctx)
		}); err != nil {
			return nil, err
		}
		return &uploadResp, nil
	}

	// 预签名分片上传
	if err := c.uploadViaPreSignedURLs(ctx, &uploadResp, filePath, size, progress); err != nil {
		return nil, err
	}
	return &uploadResp, nil
}

// uploadViaS3 使用 AWS S3 SDK 直传
func (c *Client) uploadViaS3(ctx context.Context, upReq *UploadResp, filePath string, size int64, progress UploadProgress) error {
	cfg := &aws.Config{
		Credentials:      credentials.NewStaticCredentials(upReq.Data.AccessKeyId, upReq.Data.SecretAccessKey, upReq.Data.SessionToken),
		Region:           aws.String("123pan"),
		Endpoint:         aws.String(upReq.Data.EndPoint),
		S3ForcePathStyle: aws.Bool(true),
	}
	s, err := session.NewSession(cfg)
	if err != nil {
		return err
	}
	uploader := s3manager.NewUploader(s)
	if size > s3manager.MaxUploadParts*s3manager.DefaultUploadPartSize {
		uploader.PartSize = size / (s3manager.MaxUploadParts - 1)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	var reader io.Reader = f
	if progress != nil {
		reader = &progressReader{reader: f, progress: progress, total: size}
	}

	_, err = uploader.UploadWithContext(ctx, &s3manager.UploadInput{
		Bucket: aws.String(upReq.Data.Bucket),
		Key:    aws.String(upReq.Data.Key),
		Body:   reader,
	})
	return err
}

// progressReader 包装读取器，上报进度
type progressReader struct {
	reader   io.Reader
	progress UploadProgress
	total    int64
	done     int64
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.reader.Read(buf)
	p.done += int64(n)
	if p.progress != nil {
		p.progress(p.done, p.total)
	}
	return n, err
}

// uploadViaPreSignedURLs 预签名分片上传
// 分片大小 16MB；单分片使用 S3Auth，多分片使用 S3PreSignedUrls（每批 10 个）
func (c *Client) uploadViaPreSignedURLs(ctx context.Context, upReq *UploadResp, filePath string, size int64, progress UploadProgress) error {
	const chunkSize = 16 * 1024 * 1024 // 16MB

	tmpF, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer tmpF.Close()

	chunkCount := int(size / chunkSize)
	lastChunkSize := size % chunkSize
	if lastChunkSize > 0 {
		chunkCount++
	} else {
		lastChunkSize = chunkSize
	}

	getS3UploadUrl := c.getS3Auth
	batchSize := 1
	if chunkCount > 1 {
		batchSize = 10
		getS3UploadUrl = c.getS3PreSignedUrls
	}

	for i := 1; i <= chunkCount; i += batchSize {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		start := i
		end := min(i+batchSize, chunkCount+1)
		s3PreSignedUrls, err := getS3UploadUrl(ctx, upReq, start, end)
		if err != nil {
			return err
		}
		for j := start; j < end; j++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			curSize := int64(chunkSize)
			if j == chunkCount {
				curSize = lastChunkSize
			}
			// 每个分片从文件对应偏移读取
			sectionReader := io.NewSectionReader(tmpF, chunkSize*int64(j-1), curSize)
			if err := c.uploadPreSignedChunk(ctx, upReq, s3PreSignedUrls, j, end, sectionReader, curSize, false, getS3UploadUrl); err != nil {
				return err
			}
			if progress != nil {
				progress(int64(j)*chunkSize, size)
			}
		}
	}

	// 完成上传（v2）
	completeData := map[string]interface{}{
		"StorageNode": upReq.Data.StorageNode,
		"bucket":      upReq.Data.Bucket,
		"fileId":      upReq.Data.FileId,
		"fileSize":    size,
		"isMultipart": chunkCount > 1,
		"key":         upReq.Data.Key,
		"uploadId":    upReq.Data.UploadId,
	}
	completeBody, err := json.Marshal(completeData)
	if err != nil {
		return err
	}
	_, err = c.Request(ctx, c.api("/file/upload_complete/v2"), http.MethodPost, func(req *resty.Request) {
		req.SetBody(completeBody).SetContext(ctx)
	})
	return err
}

// getS3PreSignedUrls 获取多分片预签名上传 URL（每批最多 10 个）
func (c *Client) getS3PreSignedUrls(ctx context.Context, upReq *UploadResp, start, end int) (*S3PreSignedURLs, error) {
	data := map[string]interface{}{
		"bucket":          upReq.Data.Bucket,
		"key":             upReq.Data.Key,
		"partNumberEnd":   end,
		"partNumberStart": start,
		"uploadId":        upReq.Data.UploadId,
		"StorageNode":     upReq.Data.StorageNode,
	}
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var s3PreSignedUrls S3PreSignedURLs
	respBody, err := c.Request(ctx, c.api("/file/s3_repare_upload_parts_batch"), http.MethodPost, func(req *resty.Request) {
		req.SetBody(body).SetContext(ctx)
	})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(respBody, &s3PreSignedUrls); err != nil {
		return nil, err
	}
	return &s3PreSignedUrls, nil
}

// getS3Auth 获取单分片预签名上传 URL
func (c *Client) getS3Auth(ctx context.Context, upReq *UploadResp, start, end int) (*S3PreSignedURLs, error) {
	data := map[string]interface{}{
		"StorageNode":     upReq.Data.StorageNode,
		"bucket":          upReq.Data.Bucket,
		"key":             upReq.Data.Key,
		"partNumberEnd":   end,
		"partNumberStart": start,
		"uploadId":        upReq.Data.UploadId,
	}
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var s3PreSignedUrls S3PreSignedURLs
	respBody, err := c.Request(ctx, c.api("/file/s3_upload_object/auth"), http.MethodPost, func(req *resty.Request) {
		req.SetBody(body).SetContext(ctx)
	})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(respBody, &s3PreSignedUrls); err != nil {
		return nil, err
	}
	return &s3PreSignedUrls, nil
}

// uploadPreSignedChunk 上传单个分片到预签名 URL；403 时刷新签名重试一次
// reader 必须是按分片偏移构造的 SectionReader
func (c *Client) uploadPreSignedChunk(ctx context.Context, upReq *UploadResp, s3PreSignedUrls *S3PreSignedURLs, cur, end int, reader io.Reader, curSize int64, retry bool, getS3UploadUrl func(ctx context.Context, upReq *UploadResp, start, end int) (*S3PreSignedURLs, error)) error {
	uploadUrl := s3PreSignedUrls.Data.PreSignedUrls[strconv.Itoa(cur)]
	if uploadUrl == "" {
		return fmt.Errorf("上传地址为空，S3 预签名 URL 列表：%+v", s3PreSignedUrls)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadUrl, reader)
	if err != nil {
		return err
	}
	req.ContentLength = curSize

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusForbidden {
		if retry {
			return fmt.Errorf("上传分片 %d 失败，状态码：%d", cur, res.StatusCode)
		}
		// 刷新预签名 URL 后重试
		newS3PreSignedUrls, err := getS3UploadUrl(ctx, upReq, cur, end)
		if err != nil {
			return err
		}
		s3PreSignedUrls.Data.PreSignedUrls = newS3PreSignedUrls.Data.PreSignedUrls
		return c.uploadPreSignedChunk(ctx, upReq, s3PreSignedUrls, cur, end, reader, curSize, true, getS3UploadUrl)
	}
	if res.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(res.Body)
		return fmt.Errorf("上传分片 %d 失败，状态码：%d，响应：%s", cur, res.StatusCode, respBody)
	}
	return nil
}
