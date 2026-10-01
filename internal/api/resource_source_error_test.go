package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestClassifyResourceSourceError 覆盖 D7 的核心诉求：三类性质完全不同的情况
// 必须落到不同的 code 上，且「未配置」绝不能被当成搜索失败。
func TestClassifyResourceSourceError(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		err      error
		wantSkip bool
		wantCode string
		// 提示里必须出现的关键信息（可执行性要求）
		wantContains []string
	}{
		{
			name:     "SeedHub 未配置应被跳过而不是报错",
			source:   "seedhub",
			err:      errSourceNotConfigured("SeedHub", "请在发现-基础配置中填写 API 地址与令牌"),
			wantSkip: true,
			wantCode: CodeResourceSourceUnconfigured,
		},
		{
			name:     "观影未启用应被跳过",
			source:   "guanying",
			err:      errSourceNotConfigured("观影", "请在发现-基础配置中开启观影"),
			wantSkip: true,
			wantCode: CodeResourceSourceUnconfigured,
		},
		{
			name:     "TG 未配置频道应被跳过",
			source:   "tg",
			err:      errSourceNotConfigured("TG 频道", "请在发现-基础配置中填写公开 TG 资源检索频道"),
			wantSkip: true,
			wantCode: CodeResourceSourceUnconfigured,
		},
		{
			name:     "文案兜底：老式未配置错误也应被跳过",
			source:   "seedhub",
			err:      errors.New("SeedHub 未配置：请先在发现-基础配置中填写 API 地址与令牌"),
			wantSkip: true,
			wantCode: CodeResourceSourceUnconfigured,
		},
		{
			name:     "文案兜底：未授权也算未配置",
			source:   "guanying",
			err:      errors.New("观影未授权，请先登录"),
			wantSkip: true,
			wantCode: CodeResourceSourceUnconfigured,
		},
		{
			name:   "RE0 反代连不上是真故障，提示要说清是哪个服务、在哪配",
			source: "re0",
			err: fmt.Errorf("tgto123 反代不可用：%w",
				errors.New(`Post "http://127.0.0.1:12366/api/login": dial tcp 127.0.0.1:12366: connect: connection refused`)),
			wantSkip:     false,
			wantCode:     CodeResourceSourceUnavailable,
			wantContains: []string{"tgto123", "127.0.0.1:12366", "基础配置", "连接被拒绝"},
		},
		{
			name:         "TG 查不到 t.me 归为不可达",
			source:       "tg",
			err:          errors.New("公开频道查询失败：Get \"https://t.me/s/x\": dial tcp: lookup t.me: no such host"),
			wantCode:     CodeResourceSourceUnavailable,
			wantContains: []string{"t.me", "域名无法解析"},
		},
		{
			name:         "超时单独归类",
			source:       "seedhub",
			err:          errors.New("请求 SeedHub 超时"),
			wantCode:     CodeResourceSourceTimeout,
			wantContains: []string{"SeedHub", "超时"},
		},
		{
			name:         "上游业务错误保留原文并归为真实失败",
			source:       "guanying",
			err:          errors.New("观影返回 500：upstream error"),
			wantCode:     CodeResourceSourceError,
			wantContains: []string{"观影返回 500"},
		},
		{
			name:   "nil 错误不产生任何归类",
			source: "re0",
			err:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyResourceSourceError(tc.source, tc.err)
			if tc.err == nil {
				if got.Skip || got.Code != "" || got.Message != "" {
					t.Fatalf("nil 错误应返回零值，got %+v", got)
				}
				return
			}
			if got.Skip != tc.wantSkip {
				t.Fatalf("Skip 不符：want %v got %v（code=%s msg=%s）", tc.wantSkip, got.Skip, got.Code, got.Message)
			}
			if got.Code != tc.wantCode {
				t.Fatalf("code 不符：want %q got %q（msg=%s）", tc.wantCode, got.Code, got.Message)
			}
			if got.Skip && got.Code == CodeResourceSourceError {
				t.Fatal("未配置的来源绝不能带 RESOURCE_SOURCE_ERROR 码")
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got.Message, want) {
					t.Fatalf("提示缺少 %q：%s", want, got.Message)
				}
			}
		})
	}
}

// TestUnconfiguredIsNeverAnError 明确钉住用户报的那两个 case：
// SeedHub（没配）不该出现在 errors 里，RE0（依赖挂了）必须出现且带可执行提示。
func TestUnconfiguredIsNeverAnError(t *testing.T) {
	seedhub := classifyResourceSourceError("seedhub",
		errors.New("SeedHub 未配置：请先在发现-基础配置中填写 API 地址与令牌"))
	if !seedhub.Skip {
		t.Fatalf("SeedHub 未配置不应算失败，got %+v", seedhub)
	}
	if seedhub.Code == "RESOURCE_SOURCE_ERROR" {
		t.Fatal("SeedHub 未配置不应是 RESOURCE_SOURCE_ERROR")
	}

	re0 := classifyResourceSourceError("re0",
		fmt.Errorf("tgto123 反代不可用：%w",
			errors.New(`Post "http://127.0.0.1:12366/api/login": dial tcp 127.0.0.1:12366: connect: connection refused`)))
	if re0.Skip {
		t.Fatal("RE0 反代连接失败是真故障，不应被跳过")
	}
	if re0.Code != CodeResourceSourceUnavailable {
		t.Fatalf("RE0 应归为 UNAVAILABLE，got %s", re0.Code)
	}
	// 提示必须是可执行的：既说清哪个服务不通，也说清去哪儿配
	for _, want := range []string{"tgto123", "127.0.0.1:12366", "基础配置"} {
		if !strings.Contains(re0.Message, want) {
			t.Fatalf("提示不可执行，缺 %q：%s", want, re0.Message)
		}
	}
	// 不应把整条原始 Go 网络错误原样丢给用户
	if strings.Contains(re0.Message, `Post "http://`) {
		t.Fatalf("提示不应原样包含 Go 的 http.Post 错误文本：%s", re0.Message)
	}
}

// TestSummarizeDialError 裸错误摘要（前端展示的是人话，不是 dial tcp 原文）
func TestSummarizeDialError(t *testing.T) {
	cases := map[string]string{
		`dial tcp 127.0.0.1:12366: connect: connection refused`: "连接被拒绝",
		`dial tcp: lookup t.me: no such host`:                   "域名无法解析",
		`dial tcp 10.0.0.1:80: i/o timeout`:                     "连接超时",
		`dial tcp: network is unreachable`:                      "网络不可达",
	}
	for raw, want := range cases {
		if got := summarizeDialError(raw); got != want {
			t.Fatalf("summarizeDialError(%q) = %q, want %q", raw, got, want)
		}
	}
	// 兜底路径要截断，避免超长原文糊满前端
	long := strings.Repeat("错", 300)
	if got := summarizeDialError(long); len([]rune(got)) > 121 {
		t.Fatalf("超长错误应被截断，got %d runes", len([]rune(got)))
	}
}

// TestSourceName 来源中文名（用于拼提示语）
func TestSourceName(t *testing.T) {
	cases := map[string]string{
		"re0": "RE0", "guanying": "观影", "seedhub": "SeedHub", "tg": "TG 频道", "unknown": "unknown",
	}
	for in, want := range cases {
		if got := sourceName(in); got != want {
			t.Fatalf("sourceName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestErrorCodeConstantsAreDistinct 钉住对外契约：四个码互不相同且非空。
func TestErrorCodeConstantsAreDistinct(t *testing.T) {
	all := []string{
		CodeResourceSourceUnconfigured,
		CodeResourceSourceUnavailable,
		CodeResourceSourceTimeout,
		CodeResourceSourceError,
	}
	seen := map[string]bool{}
	for _, c := range all {
		if c == "" {
			t.Fatal("错误码不能为空")
		}
		if seen[c] {
			t.Fatalf("错误码重复：%s", c)
		}
		seen[c] = true
	}
}
