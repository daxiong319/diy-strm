package pan123

import (
	"context"
	"log"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"resty.dev/v3"
)

// API 地址常量（逆向自 yun.123pan.com web 端）
const (
	LoginApi         = "https://login.123pan.com/api"
	BApi             = "https://yun.123pan.com/b/api"
	MainApi          = BApi
	SignIn           = LoginApi + "/user/sign_in"
	Logout           = MainApi + "/user/logout"
	UserInfo         = MainApi + "/user/info"
	FileList         = MainApi + "/file/list/new"
	DownloadInfo     = MainApi + "/file/download_info"
	Mkdir            = MainApi + "/file/upload_request"
	Move             = MainApi + "/file/mod_pid"
	Rename           = MainApi + "/file/rename"
	Trash            = MainApi + "/file/trash"
	UploadRequest    = MainApi + "/file/upload_request"
	UploadComplete   = MainApi + "/file/upload_complete"
	S3PreSignedUrls  = MainApi + "/file/s3_repare_upload_parts_batch"
	S3Auth           = MainApi + "/file/s3_upload_object/auth"
	UploadCompleteV2 = MainApi + "/file/upload_complete/v2"
	S3Complete       = MainApi + "/file/s3_complete_multipart_upload"
	SafeBoxUnlock    = MainApi + "/restful/goapi/v1/file/safe_box/auth/unlockbox"

	// 请求头常量
	HeaderPlatform  = "platform"
	HeaderAppVer    = "app-version"
	HeaderAuth      = "authorization"
	HeaderReferer   = "referer"
	HeaderOrigin    = "origin"
	// WebUA 123 云盘 Web 端 User-Agent（下载/上传分片时使用）
	WebUA           = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)"
	defaultPlatform = "web"
	defaultAppVer   = "3"
	webReferer      = "https://yun.123pan.com/"
)

// signPath 生成 123 云盘 API 请求签名参数
// 返回 (签名 key, 签名 value)，格式 key=timeSign, value=timestamp-random-dataSign
func signPath(path string, os string, version string) (k string, v string) {
	table := []byte{'a', 'd', 'e', 'f', 'g', 'h', 'l', 'm', 'y', 'i', 'j', 'n', 'o', 'p', 'k', 'q', 'r', 's', 't', 'u', 'b', 'c', 'v', 'w', 's', 'z'}
	random := fmt.Sprintf("%.f", math.Round(1e7*rand.Float64()))
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	timestamp := fmt.Sprint(now.Unix())
	nowStr := []byte(now.Format("200601021504"))
	for i := 0; i < len(nowStr); i++ {
		nowStr[i] = table[nowStr[i]-48]
	}
	timeSign := fmt.Sprint(crc32.ChecksumIEEE(nowStr))
	data := strings.Join([]string{timestamp, random, path, os, version, timeSign}, "|")
	dataSign := fmt.Sprint(crc32.ChecksumIEEE([]byte(data)))
	return timeSign, strings.Join([]string{timestamp, random, dataSign}, "-")
}

// GetApi 为原始 URL 附加签名参数
func GetApi(rawUrl string) string {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return rawUrl
	}
	query := u.Query()
	query.Add(signPath(u.Path, "web", "3"))
	u.RawQuery = query.Encode()
	return u.String()
}

// IsEmailFormat 判断是否为邮箱账号
func IsEmailFormat(username string) bool {
	return strings.Contains(username, "@")
}

// Login 登录 123 云盘，获取访问令牌
// 使用 loginMu 串行化，避免并发 401 重登录时重复发起登录请求
func (c *Client) Login(ctx context.Context) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	body := map[string]interface{}{}
	if IsEmailFormat(c.username) {
		body = map[string]interface{}{
			"mail":     c.username,
			"password": c.password,
			"type":     2,
		}
	} else {
		body = map[string]interface{}{
			"passport": c.username,
			"password": c.password,
			"remember": true,
		}
	}

	res, err := c.client.R().
		SetContext(ctx).
		SetHeaders(map[string]string{
			HeaderOrigin:   "https://yun.123pan.com",
			HeaderReferer:  webReferer,
			HeaderPlatform: defaultPlatform,
			HeaderAppVer:   defaultAppVer,
			"user-agent":   WebUA,
		}).
		SetBody(body).
		Post(c.loginBase + "/user/sign_in")
	if err != nil {
		return fmt.Errorf("123 云盘登录请求失败：%w", err)
	}
	defer res.Body.Close()

	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Bytes(), &resp); err != nil {
		return fmt.Errorf("123 云盘登录响应解析失败：%w", err)
	}
	if resp.Code != 200 {
		if resp.Message == "" {
			resp.Message = "登录失败"
		}
		return fmt.Errorf("123 云盘登录失败：%s", resp.Message)
	}
	if resp.Data.Token == "" {
		return errors.New("123 云盘登录成功但未返回令牌")
	}

	c.tokenMu.Lock()
	oldToken := c.accessToken
	c.accessToken = resp.Data.Token
	c.tokenMu.Unlock()
	if resp.Data.Token != oldToken {
		c.notifyAuthChanged(resp.Data.Token)
	}
	return nil
}

// GetUserInfo 获取当前登录用户信息（用于验证令牌有效性）
func (c *Client) GetUserInfo(ctx context.Context) (*UserInfoResp, error) {
	body, err := c.Request(ctx, UserInfo, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	var userInfo UserInfoResp
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("解析用户信息失败：%w", err)
	}
	return &userInfo, nil
}

// api 拼接主 API 地址
func (c *Client) api(path string) string {
	return c.mainBase + path
}

// Request 统一请求方法：附加签名、鉴权、401 自动重新登录
// 返回响应体原始字节
func (c *Client) Request(ctx context.Context, url string, method string, callback func(req *resty.Request)) ([]byte, error) {
	isRetry := false
do:
	req := c.client.R()
	req.SetContext(ctx)
	req.SetHeaders(map[string]string{
		HeaderOrigin:   "https://yun.123pan.com",
		HeaderReferer:  webReferer,
		HeaderAuth:     "Bearer " + c.GetAccessToken(),
		"user-agent":   WebUA,
		HeaderPlatform: defaultPlatform,
		HeaderAppVer:   defaultAppVer,
	})
	if callback != nil {
		callback(req)
	}
	res, err := req.Execute(method, GetApi(url))
	if err != nil {
		return nil, fmt.Errorf("123 云盘请求失败 %s：%w", url, err)
	}
	defer res.Body.Close()

	body := res.Bytes()
	var resp BaseResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("123 云盘响应解析失败：%w", err)
	}
	if resp.Code != 0 {
		if !isRetry && resp.Code == 401 {
			log.Printf("pan123 401 raw url=%s status=%d body=%s", url, res.StatusCode(), string(body))
			if err := c.Login(ctx); err != nil {
				return nil, err
			}
			isRetry = true
			goto do
		}
		return nil, fmt.Errorf("123 云盘接口错误：%s", resp.Message)
	}
	return body, nil
}
