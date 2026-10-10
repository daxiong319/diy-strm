// Package playpath 实现「播放路径映射」。
//
// 这个功能最大的特点是**静默失败是常态**：规则配错了（源写反、目标写错、
// 大小写不符、顺序被前面一条遮住）都不会报错，播放照样能进行，只是
// 拿到的不是你想要的那条路径。用户从头到尾看不到任何提示。
//
// 所以本包把「四件本来会静默的事」变成可观测的：
//  1. 每条规则记录命中次数与最近命中时间，配置页据此显示「从未命中」；
//  2. 提供 Test：贴一条真实路径进去，立刻看到会映射成什么、不命中会怎样；
//  3. 匹配一律**按顺序取第一条命中**，不做「最精确优先」——与 muvyo 一致，
//     因为「更精确优先」会让用户改了顺序却看不出效果，反而更难排查；
//  4. 匹配大小写敏感，且要求源与路径的边界对齐。
package playpath

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// Rule 一条映射规则。Source 为空或 Target 为空视为未配置，由 Save 剔除。
type Rule struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Note   string `json:"note,omitempty"`
}

// Stats 单条规则的命中统计。
type Stats struct {
	Hits     int       `json:"hits"`
	LastHit  time.Time `json:"last_hit,omitempty"`
	LastFrom string    `json:"last_from,omitempty"`
	LastTo   string    `json:"last_to,omitempty"`
}

// Service 路径映射服务。零值不可用，请用 New。
//
// 命中统计**只存在内存里**，重启归零。这不是省事：统计的唯一用途是让
// 用户判断「这条规则到底有没有生效」，把它写进 DB 反而会让人误以为
// 那是权威记录；而写统计本身要落库、要和扫描/整理任务抢写通道，
// 收益远不抵风险。
type Service struct {
	mu    sync.RWMutex
	rules []Rule
	stats map[string]*Stats
	now   func() time.Time
	// enabled 是「播放路径映射」总开关。New 构造出来的服务默认打开，
	// 这样单独用本包的人不必知道有个开关；由 Sync 从配置刷新的实例才是准的。
	enabled bool
	// raw 是最近一次 Sync 进来的规则原文，用来判断配置有没有真的变。
	raw string
	// onLog 用于把每次命中打成 debug 日志。muvyo 那边靠搜
	// "Mount path replaced" 定位问题，这里保留同样的可检索文本，
	// 否则出问题时用户和我都只能靠猜。
	onLog func(source, from, to string)
}

func New(rules []Rule, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	s := &Service{rules: Normalize(rules), stats: map[string]*Stats{}, now: now, enabled: true}
	// 预置空统计，让配置页在「从未命中」时也拿得到一条记录，
	// 而不是把 0 次当成「规则不存在」。
	for _, r := range s.rules {
		s.stats[r.ID] = &Stats{}
	}
	return s
}

func (s *Service) SetLogger(fn func(source, from, to string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onLog = fn
}

// Normalize 规整规则列表：补 ID、去空、去重（按源去重保留第一条）、
// 源统一补前导斜杠。顺序即匹配顺序，**不排序**。
func Normalize(in []Rule) []Rule {
	out := make([]Rule, 0, len(in))
	seen := map[string]bool{}
	for i, r := range in {
		r.Source = normalizePath(r.Source)
		r.Target = normalizePath(r.Target)
		r.ID = strings.TrimSpace(r.ID)
		if r.ID == "" {
			r.ID = "rule-" + itoa(i+1)
		}
		if r.Source == "" || r.Target == "" {
			continue
		}
		if seen[r.Source] {
			// 同源重复：第一条生效，后面的永远命中不到。
			// 保留第一条而不是报错，因为顺序匹配下重复是合法配置
			// （用户可能就想让后面的兜底），但在 Save 时会提示。
			continue
		}
		seen[r.Source] = true
		out = append(out, r)
	}
	return out
}

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// 去掉尾部斜杠："/a/" 与 "/a" 应当是同一条规则，
	// 否则用户随手加个斜杠就多出一条永远命中不到的重复规则。
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	return p
}

// Result 一次映射的结果。
type Result struct {
	Path    string // 映射后的路径；未命中时与 Input 相同
	Matched bool
	RuleID  string
	Source  string
	From    string
	To      string
	// Candidates 列出所有「能匹配但排在后面」的规则。
	// 它是「为什么我改了规则没生效」的唯一答案：用户改的那条在第二条，
	// 第一条先命中了。静默失败的最大来源就是顺序遮蔽。
	Candidates []ShadowedRule
}

// ShadowedRule 一条被前面的规则遮蔽的规则。
type ShadowedRule struct {
	ID     string
	Source string
	Target string
}

// Map 对一条路径做映射。未命中时返回原路径，Matched=false，**不报错**。
//
// 边界对齐是刻意的：源 "/media" 不匹配 "/media2/x"。
// 纯前缀匹配会让一条为 /media 写的规则悄悄吃掉 /media-old/... 下的
// 全部请求，而这正是本功能「静默失败」的典型形态。
func (s *Service) Map(p string) Result {
	if s == nil {
		return Result{Path: p}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := Result{Path: p}
	// 开关关掉 = 彻底 no-op：规则表保留（统计不丢），但一个字节都不改写。
	if p == "" || !s.enabled {
		return res
	}
	for i := range s.rules {
		r := s.rules[i]
		if !prefixMatches(p, r.Source) {
			continue
		}
		if !res.Matched {
			res.Matched = true
			res.RuleID = r.ID
			res.Source = r.Source
			res.From = p
			res.To = r.Target + strings.TrimPrefix(p, r.Source)
			res.Path = res.To
		} else {
			// 后面还有能匹配的：记下来，这解释了「我改了没生效」。
			res.Candidates = append(res.Candidates, ShadowedRule{ID: r.ID, Source: r.Source, Target: r.Target})
			continue
		}
		s.record(r, res.From, res.To)
	}
	return res
}

func (s *Service) record(r Rule, from, to string) {
	st := s.stats[r.ID]
	if st == nil {
		st = &Stats{}
		s.stats[r.ID] = st
	}
	st.Hits++
	st.LastHit = s.now()
	st.LastFrom = from
	st.LastTo = to
	if s.onLog != nil {
		// 保留 muvyo 的可检索文本，故障排查时两边能对上。
		s.onLog(r.Source, from, to)
	}
}

// prefixMatches 源与路径前缀一致，且边界对齐（其后是 '/' 或恰好结束）。
// 大小写敏感：/Media/ 与 /media/ 是两条不同的路径。
func prefixMatches(p, source string) bool {
	if !strings.HasPrefix(p, source) {
		return false
	}
	rest := p[len(source):]
	return rest == "" || strings.HasPrefix(rest, "/")
}

// Stat 取单条规则的统计。
func (s *Service) Stat(id string) Stats {
	if s == nil {
		return Stats{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.stats[id]; ok && st != nil {
		return *st
	}
	return Stats{}
}

// Report 一条规则 + 它的统计，配置页一行一条。
type Report struct {
	Rule
	Stats
}

// Rules 返回带统计的规则列表（保持匹配顺序）。
func (s *Service) Rules() []Report {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Report, 0, len(s.rules))
	for _, r := range s.rules {
		st := Stats{}
		if v, ok := s.stats[r.ID]; ok && v != nil {
			st = *v
		}
		out = append(out, Report{Rule: r, Stats: st})
	}
	return out
}

// SetRules 热替换规则列表（配置保存后调用）。统计按规则 ID 保留：
// 改了目标路径不该把「这条规则生效过」这个事实也抹掉。
func (s *Service) SetRules(in []Rule) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = Normalize(in)
	s.enabled = true
	for _, r := range s.rules {
		if _, ok := s.stats[r.ID]; !ok {
			s.stats[r.ID] = &Stats{}
		}
	}
	// 删掉已不存在规则的统计，否则 ID 复用时会继承上一条规则的历史。
	live := map[string]bool{}
	for _, r := range s.rules {
		live[r.ID] = true
	}
	for id := range s.stats {
		if !live[id] {
			delete(s.stats, id)
		}
	}
}

// Conflict 指出与已有规则同源的条目。**只警告不阻止**——
// 顺序匹配下同源规则是合法配置（用户可能就想让后面的兜底），
// 但它也是「我改了没生效」的头号原因，必须说出来。
type Conflict struct {
	ID     string
	Source string
	Reason string
}

// Conflicts 返回配置列表里的同源冲突。
func Conflicts(in []Rule) []Conflict {
	var out []Conflict
	first := map[string]string{}
	for i, r := range in {
		src := normalizePath(r.Source)
		if src == "" {
			continue
		}
		if prev, ok := first[src]; ok {
			out = append(out, Conflict{
				ID:     r.ID,
				Source: src,
				Reason: "与「" + prev + "」源相同，只有一条会生效（按顺序取第一条）",
			})
			continue
		}
		first[src] = ruleLabel(r, i)
	}
	return out
}

func ruleLabel(r Rule, i int) string {
	if strings.TrimSpace(r.ID) != "" {
		return r.ID
	}
	return "第 " + itoa(i+1) + " 条"
}

// Encode 把规则列表序列化成设置值。
func Encode(rules []Rule) (string, error) {
	norm := Normalize(rules)
	if len(norm) == 0 {
		return "", nil
	}
	data, err := json.Marshal(map[string]any{"rules": norm})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Decode 解析设置值。解析失败返回空列表而不是错误：
// 设置值是用户手填的 JSON，一个坏括号不该让整个播放链路 500。
func Decode(raw string) []Rule {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var wrapper struct {
		Rules []Rule `json:"rules"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err == nil && wrapper.Rules != nil {
		return Normalize(wrapper.Rules)
	}
	var flat []Rule
	if err := json.Unmarshal([]byte(raw), &flat); err == nil {
		return Normalize(flat)
	}
	return nil
}

// UnusedIDs 给配置页标出「仍在库里但已无对应规则」的 ID。
func UnusedIDs(raw string, rules []Rule) []string {
	known := map[string]bool{}
	for _, r := range Normalize(rules) {
		known[r.ID] = true
	}
	var out []string
	for _, r := range Decode(raw) {
		if !known[r.ID] {
			out = append(out, r.ID)
		}
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
