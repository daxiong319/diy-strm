package rbac

import (
	"testing"
)

// TestCanDenyTruthTable 是验收 ② 的核心用例：deny 的优先级真值表。
//
// 这张表穷举「用户级覆盖 × 组效果」的��部组合（3 态 × 4 态）。
// 三态是 继承/允许/拒绝；组效果的 4 态是 无组允许/一组允许/一组拒绝/多组混合。
func TestCanDenyTruthTable(t *testing.T) {
	const (
		inherit = ""
		allow   = "allow"
		deny    = "deny"
	)
	cases := []struct {
		name    string
		user    string
		groups  map[string]string
		want    bool
		wantSrc string
	}{
		// --- 任务书点名的三例 ---
		{
			name:    "组允许+用户拒绝=拒绝",
			user:    deny,
			groups:  map[string]string{PermSystemManage: allow},
			want:    false,
			wantSrc: SourceUserDeny,
		},
		{
			// 来源记为 group_allow：decide 里组允许先于用户允许判定，
			// 两者同为允许时先命中的那个就是归因来源。判定结果不受影响。
			name:    "组允许+用户允许=允许",
			user:    allow,
			groups:  map[string]string{PermSystemManage: allow},
			want:    true,
			wantSrc: SourceGroupAllw,
		},
		{
			name:    "组拒绝+用户允许=拒绝（deny 最高）",
			user:    allow,
			groups:  map[string]string{PermSystemManage: deny},
			want:    false,
			wantSrc: SourceGroupDeny,
		},
		// --- 其余组合 ---
		{
			name:    "两边都拒绝=拒绝（用户级先判）",
			user:    deny,
			groups:  map[string]string{PermSystemManage: deny},
			want:    false,
			wantSrc: SourceUserDeny,
		},
		{
			name:    "用户继承+组允许=允许",
			user:    inherit,
			groups:  map[string]string{PermSystemManage: allow},
			want:    true,
			wantSrc: SourceGroupAllw,
		},
		{
			name:    "用户继承+组拒绝=拒绝",
			user:    inherit,
			groups:  map[string]string{PermSystemManage: deny},
			want:    false,
			wantSrc: SourceGroupDeny,
		},
		{
			name:    "用户允许+无组=允许",
			user:    allow,
			groups:  nil,
			want:    true,
			wantSrc: SourceUserAllw,
		},
		{
			name:    "用户拒绝+无组=拒绝",
			user:    deny,
			groups:  nil,
			want:    false,
			wantSrc: SourceUserDeny,
		},
		{
			name:    "两边都继承=拒绝（默认无权限）",
			user:    inherit,
			groups:  nil,
			want:    false,
			wantSrc: SourceNone,
		},
		{
			name:    "用户允许但别的权限项上有组拒绝不影响本项",
			user:    inherit,
			groups:  map[string]string{PermToolManage: deny},
			want:    false,
			wantSrc: SourceNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Principal{UserID: 7, Username: "u", GroupEffects: map[string]Effect{}}
			for k, v := range tc.groups {
				if v == inherit {
					continue
				}
				p.GroupEffects[k] = Effect(v)
			}
			if tc.user != inherit {
				p.Overrides = map[string]Effect{PermSystemManage: Effect(tc.user)}
			}
			got := p.Can(PermSystemManage)
			if got != tc.want {
				t.Fatalf("Can(%s) = %v, 期望 %v（user=%q groups=%v）",
					PermSystemManage, got, tc.want, tc.user, tc.groups)
			}
			ex := p.Explain(PermSystemManage)
			if ex.Source != tc.wantSrc {
				t.Fatalf("Explain.Source = %q, 期望 %q", ex.Source, tc.wantSrc)
			}
			if ex.Allowed != tc.want {
				t.Fatalf("Explain.Allowed = %v, 期望 %v", ex.Allowed, tc.want)
			}
		})
	}
}

// TestMultiGroupMergePutsDenyOnTop 验证多组合并时 deny 压过 allow。
//
// 场景：用户同时属于「运维组」（允许 system.manage）和「审计组」（拒绝 system.manage）。
// 期望拒绝 —— 任何一个组说不行就是不行。
func TestMultiGroupMergePutsDenyOnTop(t *testing.T) {
	p := Principal{
		UserID: 7,
		GroupEffects: map[string]Effect{
			PermSystemManage: EffectDeny, // 合并结果应当已经是 deny
			PermToolManage:   EffectAllow,
		},
	}
	if p.Can(PermSystemManage) {
		t.Fatal("组里同时有 allow 与 deny 时必须拒绝")
	}
	if !p.Can(PermToolManage) {
		t.Fatal("只有一个 allow 的权限项应当放行")
	}
	// 用户级允许不能盖掉组级拒绝。
	p.Overrides = map[string]Effect{PermSystemManage: EffectAllow}
	if p.Can(PermSystemManage) {
		t.Fatal("用户级允许不得越过组级拒绝")
	}
}

// TestSuperBypassesEverything 超管绕过一切，包括超管专属项与未知权限项。
func TestSuperBypassesEverything(t *testing.T) {
	p := NewSuperPrincipal("admin")
	for _, perm := range []string{
		PermSystemManage,
		PermSubscriptionCreate,
		PermOfflineDownloadRun,
		"完全不存在的权限项",
	} {
		if !p.Can(perm) {
			t.Fatalf("超管应当可以 %q", perm)
		}
	}
	if !p.Can("") {
		t.Fatal("超管对空权限项也应放行")
	}
	// 显式 deny 也拦不住超管。
	p.Overrides = map[string]Effect{PermSystemManage: EffectDeny}
	p.GroupEffects = map[string]Effect{PermSystemManage: EffectDeny}
	if !p.Can(PermSystemManage) {
		t.Fatal("超管不应被 deny 拦住")
	}
}

// TestSuperOnlyPermissionsAreDeniedToEveryoneElse 是验收 ③：
// 加订阅与离线下载对非超管永远拒绝，哪怕数据里配了允许。
func TestSuperOnlyPermissionsAreDeniedToEveryoneElse(t *testing.T) {
	p := Principal{
		UserID:   7,
		Username: "u",
		// 数据里显式给了允许（正常路径写不进来，这里就是要验证写了也没用）。
		GroupEffects: map[string]Effect{
			PermSubscriptionCreate: EffectAllow,
			PermOfflineDownloadRun: EffectAllow,
		},
		// 用户级也给了允许。
		Overrides: map[string]Effect{
			PermSubscriptionCreate: EffectAllow,
			PermOfflineDownloadRun: EffectAllow,
		},
	}
	for _, perm := range []string{PermSubscriptionCreate, PermOfflineDownloadRun} {
		if p.Can(perm) {
			t.Fatalf("非超管不应获得 %q", perm)
		}
		if src := p.Explain(perm).Source; src != SourceSuperOnly {
			t.Fatalf("%q 的拒绝来源 = %q, 期望 %q", perm, src, SourceSuperOnly)
		}
	}
	// 即便是"什么权限都没有但有 system.manage"的人也一样不行。
	p2 := Principal{UserID: 8, GroupEffects: map[string]Effect{PermSystemManage: EffectAllow}}
	if p2.Can(PermSubscriptionCreate) {
		t.Fatal("拥有 system.manage 也不该拿到 subscription.create")
	}
}

// TestUnknownPermissionFailsClosed 未知权限项一律拒绝。
//
// 这是「判定顺序」里 super 之后的第一条：新加一个权限项但忘了在
// IsKnownPermission 里登记时，判定应该是保守而不是放行。
func TestUnknownPermissionFailsClosed(t *testing.T) {
	p := Principal{
		UserID:       7,
		GroupEffects: map[string]Effect{"随便什么": EffectAllow},
		Overrides:    map[string]Effect{"随便什么": EffectAllow},
	}
	if p.Can("随便什么") {
		t.Fatal("未知权限项应当拒绝")
	}
	if p.Can("") {
		t.Fatal("空权限项应当拒绝")
	}
	if src := p.Explain("随便什么").Source; src != SourceUnknown {
		t.Fatalf("未知权限项来源 = %q, 期望 %q", src, SourceUnknown)
	}
}

// TestSuperOnlySetMatchesCatalog 是漂移防护：
// catalog 里标了 SuperOnly 的项，必须与 superOnlyPermissions 这个硬编码集合一致。
//
// 判定只读 superOnlyPermissions（见 permissions.go 注释），
// 所以两处一旦不一致，界面显示和真实判定就会对不上 —�� 这个测试就是防这个。
func TestSuperOnlySetMatchesCatalog(t *testing.T) {
	inCode := map[string]struct{}{}
	for k := range superOnlyPermissions {
		inCode[k] = struct{}{}
	}
	inCatalog := map[string]struct{}{}
	for _, meta := range catalog {
		if meta.SuperOnly {
			inCatalog[meta.Key] = struct{}{}
		}
	}
	for k := range inCode {
		if _, ok := inCatalog[k]; !ok {
			t.Errorf("superOnlyPermissions 里有 %q，但 catalog 没标 SuperOnly", k)
		}
	}
	for k := range inCatalog {
		if _, ok := inCode[k]; !ok {
			t.Errorf("catalog 里 %q 标了 SuperOnly，但 superOnlyPermissions 里没有 —— 界面会显示成可下放，实际判定是拒绝", k)
		}
	}
	// 反向也确认一下：这两个必须确实是超管专属。
	for _, want := range []string{PermSubscriptionCreate, PermOfflineDownloadRun} {
		if !IsSuperOnly(want) {
			t.Errorf("%q 应当是超管专属", want)
		}
	}
}

// TestCatalogIsWellFormed 检查权限清单本身：key 不重复、SortOrder 不重复、分类合法。
func TestCatalogIsWellFormed(t *testing.T) {
	seenKey := map[string]int{}
	seenOrder := map[int]string{}
	validCat := map[string]bool{
		CategoryConsole:  true,
		CategoryRequest:  true,
		CategoryResource: true,
		CategoryIdentity: true,
	}
	for i, meta := range catalog {
		if prev, dup := seenKey[meta.Key]; dup {
			t.Errorf("权限 key 重复：%q（catalog[%d] 与 catalog[%d]）", meta.Key, prev, i)
		}
		seenKey[meta.Key] = i
		if meta.Label == "" {
			t.Errorf("权限 %q 没有中文标签，界面上会显示成裸 key", meta.Key)
		}
		if !validCat[meta.Category] {
			t.Errorf("权限 %q 的分类 %q 不在已定义分类里", meta.Key, meta.Category)
		}
		if prev, dup := seenOrder[meta.SortOrder]; dup {
			t.Errorf("SortOrder %d 重复：%q 与 %q，界面排序会不稳定", meta.SortOrder, prev, meta.Key)
		}
		seenOrder[meta.SortOrder] = meta.Key
	}
	for _, meta := range catalog {
		if !IsKnownPermission(meta.Key) {
			t.Errorf("权限 %q 不被 IsKnownPermission 认可", meta.Key)
		}
	}
}
