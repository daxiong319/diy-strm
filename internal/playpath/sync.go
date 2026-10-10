package playpath

// Config 是「从配置里读到的一份开关 + 规则原文」。
type Config struct {
	// Enabled 为 false 时 Map 直接原样返回 —— 映射是静默改写路径的功能，
	// 关掉就必须彻底 no-op，而不是「规则还在但没人看开关」。
	Enabled bool
	// Rules 是 Encode 出来的原文（`{"rules":[...]}`）。
	Rules string
}

// Sync 用配置刷新开关与规则表。
//
// 幂等：规则原文没变就什么都不做 —— 这是刻意的。Map 每次播放都会命中，
// 如果每次都重建规则表，命中统计会被同值的 SetRules 抹成 0，
// 于是「这条规则生效过没有」这个唯一的诊断依据永远显示「从未命中」。
//
// 关掉开关时保留规则表与统计：用户临时关一下再打开，
// 「最近一次命中」不该凭空消失。
func (s *Service) Sync(cfg Config) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled == cfg.Enabled && s.raw == cfg.Rules {
		return
	}
	s.enabled = cfg.Enabled
	s.raw = cfg.Rules
	if !cfg.Enabled {
		return
	}
	s.rules = Normalize(Decode(cfg.Rules))
	live := make(map[string]struct{}, len(s.rules))
	for _, r := range s.rules {
		live[r.ID] = struct{}{}
		if _, ok := s.stats[r.ID]; !ok {
			s.stats[r.ID] = &Stats{}
		}
	}
	// 规则没了统计也要没：ID 复用时否则会继承上一条规则的历史命中数，
	// 让「这条规则生效过」指向一条已经不存在的规则。
	for id := range s.stats {
		if _, ok := live[id]; !ok {
			delete(s.stats, id)
		}
	}
}

// Enabled 回报当前开关状态，供 API 直接回显。
func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// TestPath 是给「测试路径」按钮用的：走同一套 Map，
// 所以按钮给出的结果和真实播放一定一致 —— 如果测试另写一份匹配逻辑，
// 它就会在最需要说真话的时候说假话。
//
// Matched=false 就是「不命中任何规则、路径不会改写」，页面要如实这么写。
func (s *Service) TestPath(p string) Result {
	return s.Map(p)
}

// PlainRules 返回去掉统计的规则列表（保持匹配顺序）。
// 配置页要把它提交回服务、Encode 进设置表，而 Report 里混着统计字段，
// 直接拿 Report 当 Rule 用会让统计跟着一起提交回去。
func (s *Service) PlainRules() []Rule {
	if s == nil {
		return nil
	}
	reports := s.Rules()
	out := make([]Rule, 0, len(reports))
	for _, rep := range reports {
		out = append(out, rep.Rule)
	}
	return out
}

// ConflictsOf 返回当前规则表的同源冲突，供 API 一行调用。
func ConflictsOf(s *Service) []Conflict {
	if s == nil {
		return nil
	}
	return Conflicts(s.PlainRules())
}
