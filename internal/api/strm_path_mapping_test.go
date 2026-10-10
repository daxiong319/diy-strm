package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"litepan/internal/playpath"
)

// 这组用例守的是 T17 里最容易被「补一句文档」糊弄过去的一条：
// **播放路径映射是静默改写路径的**。规则匹配不上不报错、写反了也不报错，
// 用户看到的现象永远是「我配了规则但没生效」。
// 于是页面上必须有两处能说真话的地方：命中统计与「测试路径」。

func newPlayPathHandler(t *testing.T, enabled bool, rules ...playpath.Rule) (*Handler, *playpath.Service) {
	t.Helper()
	svc := playpath.New(rules, time.Now)
	svc.Sync(playpath.Config{Enabled: enabled})
	return &Handler{playPath: svc}, svc
}

// pathOrRoot 兜住不需要真实路径的调用点：httptest.NewRequest 不接受空 URL。
func pathOrRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func doJSON(t *testing.T, h *Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败：%v", err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, pathOrRoot(path), rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	switch method {
	case http.MethodGet:
		h.strmPathMappingRules(rec, req)
	case http.MethodPut:
		h.strmPathMappingSave(rec, req)
	default:
		h.strmPathMappingTest(rec, req)
	}
	return rec
}

func decodeOK[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    T      `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v（body=%s）", err, rec.Body.String())
	}
	if !resp.Success {
		t.Fatalf("期望成功，得到 message=%q（body=%s）", resp.Message, rec.Body.String())
	}
	return resp.Data
}

// TestPathMappingTestButtonTellsTruth 是验收 ①③④ 的核心用例。
//
// 三条语义在这一条里被同时钉住：
//   - 两条源都能匹配 → 用第一条（顺序语义，不是最精确优先）；
//   - 大小写不同 → 不匹配；
//   - 源写反（目标当源用）→ 从未命中，且提示要说清原因。
//
// 三条都是「用户改完规则看不出效果」时最可能踩的坑，而它们的症状完全一样。
func TestPathMappingTestButtonTellsTruth(t *testing.T) {
	h, _ := newPlayPathHandler(t, true,
		playpath.Rule{ID: "r1", Source: "/media", Target: "/A"},
		playpath.Rule{ID: "r2", Source: "/media/4k", Target: "/B"},
	)

	t.Run("两条都匹配时用第一条并点出被遮蔽的", func(t *testing.T) {
		got := decodeOK[struct {
			Matched    bool   `json:"matched"`
			RuleID     string `json:"rule_id"`
			Result     string `json:"result"`
			Message    string `json:"message"`
			Candidates []struct {
				ID string `json:"id"`
			} `json:"candidates"`
		}](t, doJSON(t, h, http.MethodPost, "/test", map[string]any{"path": "/media/4k/x.mkv"}))
		if !got.Matched || got.RuleID != "r1" {
			t.Fatalf("应命中第一条 r1，得到 %+v", got)
		}
		if got.Result != "/A/4k/x.mkv" {
			t.Fatalf("改写结果 = %q，期望 /A/4k/x.mkv", got.Result)
		}
		if len(got.Candidates) != 1 || got.Candidates[0].ID != "r2" {
			t.Fatalf("应点出被遮蔽的 r2，得到 %+v", got.Candidates)
		}
		if !bytes.Contains([]byte(got.Message), []byte("r2")) {
			t.Fatalf("提示必须点名被遮蔽的规则，否则用户只看到「命中了」却不知道另一条是死的：%q", got.Message)
		}
	})

	t.Run("大小写不同不匹配", func(t *testing.T) {
		got := decodeOK[struct {
			Matched bool   `json:"matched"`
			Result  string `json:"result"`
			Message string `json:"message"`
		}](t, doJSON(t, h, http.MethodPost, "/test", map[string]any{"path": "/Media/4k/x.mkv"}))
		if got.Matched {
			t.Fatalf("/Media 不应匹配源 /media，得到 %+v", got)
		}
		if got.Result != "/Media/4k/x.mkv" {
			t.Fatalf("未命中时路径必须原样返回，得到 %q", got.Result)
		}
	})

	t.Run("源写反时立刻看出", func(t *testing.T) {
		// 用户把源和目标填反了：源写成 /A。
		h2, _ := newPlayPathHandler(t, true, playpath.Rule{ID: "r1", Source: "/A", Target: "/media"})
		got := decodeOK[struct {
			Matched bool   `json:"matched"`
			Result  string `json:"result"`
			Message string `json:"message"`
		}](t, doJSON(t, h2, http.MethodPost, "/test", map[string]any{"path": "/media/4k/x.mkv"}))
		if got.Matched || got.Result != "/media/4k/x.mkv" {
			t.Fatalf("写反的规则不该命中，得到 %+v", got)
		}
		if got.Message == "" {
			t.Fatal("从未命中必须给出一句可显示的说明，否则页面是空白，用户无从判断")
		}
	})
}

// TestPathMappingTestUsesSameLogicAsPlayback 断言「测试路径」走的是真实 Map。
//
// 如果按钮另写一份匹配逻辑（哪怕只是把顺序反过来成最精确优先），
// 它就会在用户最需要说真话的时候说假话 —— 而绿色对勾正是他唯一的依据。
// 这里用「服务先跑一次真实播放映射（产生统计），再点按钮」证明二者共用状态。
func TestPathMappingTestUsesSameLogicAsPlayback(t *testing.T) {
	svc := playpath.New([]playpath.Rule{{ID: "r1", Source: "/media", Target: "/A"}}, time.Now)
	svc.Sync(playpath.Config{Enabled: true})
	h := &Handler{playPath: svc}

	svc.Map("/media/x.mkv") // 真实播放路径先命中一次

	state := decodeOK[struct {
		Rules []playpath.Report `json:"rules"`
	}](t, doJSON(t, h, http.MethodGet, "", nil))
	if len(state.Rules) != 1 || state.Rules[0].Hits != 1 {
		t.Fatalf("真实播放的命中必须被统计到，页面才能据此判断规则生不生效，得到 %+v", state.Rules)
	}

	got := decodeOK[struct {
		Matched bool `json:"matched"`
	}](t, doJSON(t, h, http.MethodPost, "/test", map[string]any{"path": "/media/y.mkv"}))
	if !got.Matched {
		t.Fatal("按钮应给出与真实播放一致的命中结论")
	}
	if st := svc.Stat("r1"); st.Hits != 2 {
		t.Fatalf("按钮用的应是真��� Map（命中数应为 2），实际 %d", st.Hits)
	}
}

// TestPathMappingSaveWarnsOnDuplicateSourceInsteadOfBlocking 是「只警告不阻止」的守卫。
//
// 顺序匹配下同源规则是**合法**配置（后一条就是永远命不到的死规则），
// 但用户多半正在写后一条而没意识到前一条已经覆盖了它。
// 断言保存成功 + conflicts 非空：改成拒绝保存，用户会以为「冲突是错误」，
// 反而把正确的规则删掉。
func TestPathMappingSaveWarnsOnDuplicateSourceInsteadOfBlocking(t *testing.T) {
	h, svc := newPlayPathHandler(t, true)
	rec := doJSON(t, h, http.MethodPut, "", map[string]any{
		"enabled": true,
		"rules": []map[string]any{
			{"id": "r1", "source": "/media", "target": "/A"},
			{"id": "r2", "source": "/media", "target": "/B"},
		},
	})
	got := decodeOK[struct {
		Saved     int                 `json:"saved"`
		Conflicts []playpath.Conflict `json:"conflicts"`
	}](t, rec)
	if got.Saved != 2 {
		t.Fatalf("同源冲突不应阻止保存，saved=%d", got.Saved)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0].ID != "r2" {
		t.Fatalf("应警告后一条被遮蔽，得到 %+v", got.Conflicts)
	}
	if res := svc.Map("/media/x"); res.RuleID != "r1" {
		t.Fatalf("实际匹配仍应是第一条，得到 %+v", res)
	}
}

// TestPathMappingSaveKeepsEnabledOff 守卫「关掉映射」不被偷偷打开。
//
// SetRules 恒把开关打开，而这里用户提交的是 enabled=false。
// 一旦保存把开关变成 true，用户在页面上明明关了它，播放却仍在改写路径 ——
// 而且没有任何报错。
func TestPathMappingSaveKeepsEnabledOff(t *testing.T) {
	h, svc := newPlayPathHandler(t, true)
	rec := doJSON(t, h, http.MethodPut, "", map[string]any{
		"enabled": false,
		"rules":   []map[string]any{{"id": "r1", "source": "/media", "target": "/A"}},
	})
	got := decodeOK[struct {
		Enabled bool `json:"enabled"`
	}](t, rec)
	if got.Enabled {
		t.Fatal("保存 enabled=false 之后开关必须仍为 false")
	}
	if svc.Enabled() {
		t.Fatal("服务内开关也被打开 —— 用户关了映射但播放仍在改写路径")
	}
	if res := svc.Map("/media/x.mkv"); res.Path != "/media/x.mkv" {
		t.Fatalf("关闭状态下映射必须是彻底 no-op，得到 %q", res.Path)
	}
}

// TestPathMappingEndpointsRejectWhenUnwired 未装配时端点要报「不支持」，
// 而不是 500 或静默返回空列表 —— 空列表会被页面渲染成「一条规则都没有」，
// 用户会把没装配误读成没配过。
func TestPathMappingEndpointsRejectWhenUnwired(t *testing.T) {
	h := &Handler{}
	for _, tc := range []struct {
		name string
		call func(*httptest.ResponseRecorder)
	}{
		{"rules", func(rec *httptest.ResponseRecorder) {
			h.strmPathMappingRules(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		}},
		{"save", func(rec *httptest.ResponseRecorder) {
			h.strmPathMappingRules(rec, httptest.NewRequest(http.MethodPut, "/x", bytes.NewReader(nil)))
		}},
		{"test", func(rec *httptest.ResponseRecorder) {
			h.strmPathMappingTest(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(nil)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec)
			if rec.Code == http.StatusOK {
				t.Fatalf("未装配时应报「该操作不支持」，得到 200：%s", rec.Body.String())
			}
		})
	}
}
