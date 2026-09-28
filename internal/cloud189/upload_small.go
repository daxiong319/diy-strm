package cloud189

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// UploadSmallText 上传小文本文件（.cas 指纹文件）到指定目录。
// 走天翼 web 端 writeFile 链路：createUploadFile → patch 上传内容 → commit。
// 内容上限 10MB（.cas 只有几百字节，绰绰有余）。
func (c *Client) UploadSmallText(ctx context.Context, parentFolderID, fileName, content string) (string, error) {
	if len(content) > 10<<20 {
		return "", fmt.Errorf("文件过大")
	}
	// 1. createUploadFile 申请上传
	body, err := c.signedPost(ctx, APIURL, "/createUploadFile.action", url.Values{
		"parentFolderID": {parentFolderID},
		"fileName":       {fileName},
		"size":           {fmt.Sprintf("%d", len(content))},
	}, nil)
	if err != nil {
		return "", err
	}
	var create struct {
		Data struct {
			FileDataExists int    `json:"fileDataExists"`
			UploadFileID   string `json:"uploadFileId"`
			FileID         string `json:"fileId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &create); err != nil {
		return "", err
	}
	// 秒传命中（同内容文件已在云端）
	if create.Data.FileDataExists == 1 && create.Data.FileID != "" {
		return create.Data.FileID, nil
	}
	if create.Data.UploadFileID == "" {
		return "", fmt.Errorf("createUploadFile 未返回 uploadFileId")
	}
	// 2. patch 上传内容（小文件单分片，sliceMd5=全量 MD5）
	md5Hex := md5HexOf(content)
	sliceSize := int64(len(content))
	body2, err := c.signedPost(ctx, UploadBaseURL, "/person/patch", url.Values{
		"uploadFileId": {create.Data.UploadFileID},
		"sliceMd5":     {md5Hex},
		"sliceSize":    {fmt.Sprintf("%d", sliceSize)},
		"fileSliceMd5": {md5Hex},
	}, strings.NewReader(content))
	_ = body2
	if err != nil {
		return "", fmt.Errorf("上传内容失败：%w", err)
	}
	// 3. commit 提交
	body3, err := c.signedPost(ctx, APIURL, "/commitMultiUploadFile.action", url.Values{
		"uploadFileId": {create.Data.UploadFileID},
		"isLog":        {"0"},
		"opertype":     {"1"},
	}, nil)
	if err != nil {
		return "", err
	}
	var commit struct {
		Data struct {
			FileID string `json:"fileId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body3, &commit); err != nil {
		return "", err
	}
	if commit.Data.FileID == "" {
		return "", fmt.Errorf("commit 未返回 fileID")
	}
	return commit.Data.FileID, nil
}

// md5HexOf 内容 MD5（十六进制大写）
func md5HexOf(content string) string {
	sum := md5.Sum([]byte(content))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// randomHex 随机串（备用）
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var _ = time.Now
