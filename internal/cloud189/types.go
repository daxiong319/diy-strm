package cloud189

import "time"

// TokenSession 登录/刷新后的会话令牌（sessionKey + accessToken 双轨）
type TokenSession struct {
	SessionKey   string `json:"sessionKey"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// tokenStoreData 令牌持久化（账号表 token/refresh_token/token_expiries_time 复用）
type tokenStoreData struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// FileInfo 天翼云盘文件条目（listFiles 响应规整）
type FileInfo struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	IsDir      bool      `json:"is_dir"`
	MD5        string    `json:"md5,omitempty"`
	SliceMD5   string    `json:"slice_md5,omitempty"`
	ParentID   string    `json:"parent_id"`
	CreateDate time.Time `json:"create_date"`
	LastOpTime time.Time `json:"last_op_time"`
}

// FamilyInfo 家庭云信息
type FamilyInfo struct {
	FamilyID     int64  `json:"familyId"`
	RootFolderID string `json:"rootFolderId"`
	UserRole     int    `json:"userRole"`
}

// LoginResult 登录结果
type LoginResult struct {
	Success      bool
	NeedCaptcha  bool
	CaptchaImage string // data:image/png;base64,...
	Message      string
	Session      *TokenSession
}
