package cloud189

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 登录实现（对齐 CloudAuthClient）
// ---------------------------------------------------------------------------

type loginFormCache struct {
	CaptchaToken string
	Lt           string
	ParamID      string
	ReqID        string
	PublicKey    string
	PreKey       string
	RsaUserName  string
	RsaPassword  string
}

// getEncrypt 获取登录公钥
func (c *Client) getEncrypt(ctx context.Context) (pubKey, pre string, err error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, AuthURL+"/api/logbox/config/encryptConf.do", nil)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			PubKey string `json:"pubKey"`
			Pre    string `json:"pre"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	return out.Data.PubKey, out.Data.Pre, nil
}

// getLoginForm 获取登录表单参数
func (c *Client) getLoginForm(ctx context.Context) (*loginFormCache, error) {
	u := fmt.Sprintf("%s/api/portal/unifyLoginForPC.action?appId=%s&clientType=%s&returnURL=%s&timeStamp=%d",
		WebURL, AppID, ClientType, url.QueryEscape(ReturnURL), time.Now().UnixMilli())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	text := string(body)
	extract := func(re string) (string, error) {
		m := regexp.MustCompile(re).FindStringSubmatch(text)
		if len(m) < 2 {
			return "", fmt.Errorf("登录页参数提取失败: %s", re)
		}
		return m[1], nil
	}
	ct, err := extract(`'captchaToken' value='(.+?)'`)
	if err != nil {
		return nil, err
	}
	lt, err := extract(`lt = "(.+?)"`)
	if err != nil {
		return nil, err
	}
	pid, err := extract(`paramId = "(.+?)"`)
	if err != nil {
		return nil, err
	}
	rid, err := extract(`reqId = "(.+?)"`)
	if err != nil {
		return nil, err
	}
	return &loginFormCache{CaptchaToken: ct, Lt: lt, ParamID: pid, ReqID: rid}, nil
}

// checkValidateCode 判断是否需要验证码（返回图片或空）
func (c *Client) checkValidateCode(ctx context.Context, fc *loginFormCache) (needCaptcha bool, captchaImage string, err error) {
	form := url.Values{
		"appKey":      {AppID},
		"accountType": {AccountType},
		"userName":    {fc.RsaUserName},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, AuthURL+"/api/logbox/oauth2/needcaptcha.do", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("REQID", fc.ReqID)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if strings.TrimSpace(string(body)) != "1" {
		return false, "", nil
	}
	// 拉验证码图
	imgURL := fmt.Sprintf("%s/api/logbox/oauth2/picCaptcha.do?token=%s&REQID=%s&rnd=%d",
		AuthURL, fc.CaptchaToken, fc.ReqID, time.Now().UnixMilli())
	imgReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	imgReq.Header.Set("User-Agent", UserAgent)
	imgResp, err := c.http.Do(imgReq)
	if err != nil {
		return true, "", fmt.Errorf("获取验证码失败: %w", err)
	}
	defer imgResp.Body.Close()
	img, err := io.ReadAll(io.LimitReader(imgResp.Body, 1<<20))
	if err != nil || len(img) < 20 {
		return true, "", fmt.Errorf("获取验证码失败")
	}
	return true, "data:image/png;base64," + base64EncodeString(img), nil
}

func base64EncodeString(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// LoginByPassword 密码登录（返回 NeedCaptcha 时 CaptchaImage 非空，需带 validateCode 重试）
func (c *Client) LoginByPassword(ctx context.Context, username, password, validateCode string) (*LoginResult, error) {
	fc, err := c.getLoginForm(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取登录参数失败: %w", err)
	}
	pubKey, pre, err := c.getEncrypt(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取登录公钥失败: %w", err)
	}
	fc.PublicKey = fmt.Sprintf("-----BEGIN PUBLIC KEY-----\n%s\n-----END PUBLIC KEY-----", pubKey)
	fc.PreKey = pre

	userEnc, err := rsaEncryptHex(fc.PublicKey, username)
	if err != nil {
		return nil, err
	}
	passEnc, err := rsaEncryptHex(fc.PublicKey, password)
	if err != nil {
		return nil, err
	}
	fc.RsaUserName = pre + userEnc
	fc.RsaPassword = pre + passEnc

	if validateCode == "" {
		need, img, err := c.checkValidateCode(ctx, fc)
		if err != nil {
			return &LoginResult{Success: false, NeedCaptcha: need, CaptchaImage: img, Message: err.Error()}, nil
		}
		if need {
			return &LoginResult{Success: false, NeedCaptcha: true, CaptchaImage: img, Message: "需要验证码"}, nil
		}
	}

	form := url.Values{
		"appKey":       {AppID},
		"accountType":  {AccountType},
		"validateCode": {validateCode},
		"captchaToken": {fc.CaptchaToken},
		"dynamicCheck": {"FALSE"},
		"clientType":   {"1"},
		"cb_SaveName":  {"3"},
		"isOauth2":     {"false"},
		"returnUrl":    {ReturnURL},
		"paramId":      {fc.ParamID},
		"userName":     {fc.RsaUserName},
		"password":     {fc.RsaPassword},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, AuthURL+"/api/logbox/oauth2/loginSubmit.do", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", AuthURL)
	req.Header.Set("lt", fc.Lt)
	req.Header.Set("REQID", fc.ReqID)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var loginRes struct {
		Result int    `json:"result"`
		Msg    string `json:"msg"`
		ToURL  string `json:"toUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&loginRes); err != nil {
		return nil, err
	}
	if loginRes.ToURL == "" {
		return &LoginResult{Success: false, Message: loginRes.Msg}, nil
	}

	sess, err := c.LoginByRedirectURL(ctx, loginRes.ToURL)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Success: true, Session: sess}, nil
}

// loginByAccessToken accessToken 换 session
