package notifychannel

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"litepan/internal/domain"
)

// Service 对外提供通知渠道的配置管理（CRUD）与单渠道测试发送。
// 投递由 Dispatcher 负责；这里只做配置持久化与触发刷新。
type Service struct {
	repo  domain.NotifyChannelRepository
	disp  *Dispatcher
	log   *slog.Logger
}

func NewService(repo domain.NotifyChannelRepository, disp *Dispatcher, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{repo: repo, disp: disp, log: log}
}

// Meta 返回全部渠道元数据（前端据此渲染配置表单）。
func (s *Service) Meta() []ChannelMeta {
	return ChannelsMeta()
}

// List 返回全部渠道（含禁用的），config 原样返回。
func (s *Service) List(ctx context.Context) ([]*domain.NotifyChannel, error) {
	return s.repo.List(ctx)
}

// TestPayload 测试发送请求体。
type TestPayload struct {
	Type   string            `json:"type"`
	Config map[string]string `json:"config"`
	Title  string            `json:"title"`
	Content string           `json:"content"`
}

// Test 用给定（未必已保存的）配置试发一条通知，返回错误便于前端提示。
func (s *Service) Test(ctx context.Context, p TestPayload) error {
	if strings.TrimSpace(p.Type) == "" {
		return domain.Errorf(domain.CodeValidation, "缺少渠道类型")
	}
	cfg := withDefaults(p.Type, p.Config)
	title := p.Title
	if title == "" {
		title = "diy-strm 测试通知"
	}
	content := p.Content
	if content == "" {
		content = "这是一条测试消息，收到说明渠道配置正确。"
	}
	msg := Message{Title: title, Content: content, Tone: "info"}
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return Send(sendCtx, p.Type, cfg, msg)
}

type ChannelInput struct {
	Type    string            `json:"type"`
	Name    string            `json:"name"`
	Config  map[string]string `json:"config"`
	Enabled *bool             `json:"enabled"`
}

// Create 新建渠道并刷新 dispatcher 缓存。
func (s *Service) Create(ctx context.Context, in ChannelInput) (*domain.NotifyChannel, error) {
	if strings.TrimSpace(in.Type) == "" {
		return nil, domain.Errorf(domain.CodeValidation, "缺少渠道类型")
	}
	if _, ok := Registry[in.Type]; !ok {
		return nil, domain.Errorf(domain.CodeValidation, "未知的渠道类型: %s", in.Type)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	c := &domain.NotifyChannel{
		Type:    in.Type,
		Name:    strings.TrimSpace(in.Name),
		Config:  encodeConfig(in.Config),
		Enabled: enabled,
	}
	id, err := s.repo.Create(ctx, c)
	if err != nil {
		return nil, err
	}
	c.ID = id
	s.refresh(ctx)
	return c, nil
}

// Update 更新渠道。
func (s *Service) Update(ctx context.Context, id int64, in ChannelInput) (*domain.NotifyChannel, error) {
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Type) != "" {
		if _, ok := Registry[in.Type]; !ok {
			return nil, domain.Errorf(domain.CodeValidation, "未知的渠道类型: %s", in.Type)
		}
		c.Type = in.Type
	}
	if in.Name != "" {
		c.Name = strings.TrimSpace(in.Name)
	}
	if in.Config != nil {
		c.Config = encodeConfig(in.Config)
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if err := s.repo.Update(ctx, c); err != nil {
		return nil, err
	}
	s.refresh(ctx)
	return c, nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.refresh(ctx)
	return nil
}

func (s *Service) refresh(ctx context.Context) {
	if s.disp != nil {
		s.disp.Refresh(ctx)
	}
}
