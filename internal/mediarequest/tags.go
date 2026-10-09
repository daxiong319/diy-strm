package mediarequest

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// 入库标签的默认上限，与 参考实现 一致（见 registry.go 里
// mo_media_request_tag_max_per_user / mo_media_request_tag_max_length 的说明）。
const (
	DefaultTagMaxPerUser = 20
	DefaultTagMaxLength  = 100
)

// NormalizeTags 清洗用户输入的标签：去空白、丢空的、去重、保持首次出现的顺序。
//
// 保持输入顺序而不是排序：用户是照着自己想的顺序勾的（先「4K」再「中字」），
// 入库文件名里也就按这个顺序拼。排序会让每次显示的顺序都变，勾选框会「跳」。
//
// 去重按**去空白之后**的原文比较，但不折叠大小写：'HDR' 和 'hdr' 在
// different 文件系统（大小写敏感的 vs 不敏感的）上会产生两个文件名，
// 而它们显然是同一个意思。调用方（Service）会把大小写差异的项合并后再走这里。
func NormalizeTags(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, t := range raw {
		t = strings.TrimSpace(t)
		// 标签里出现换行/制表符会让入库文件名在各种工具里显示成两行，
		// 属于「存进去才发现没法看」的一类坑，在这里直接拍平。
		t = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			return r
		}, t)
		t = strings.Join(strings.Fields(t), " ")
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out
}

// ParseTagsLine 解析逗号分隔的一行标签（参考实现 的输入形态就是逗号分隔）。
//
// 同时接受中英文逗号：手机上切换输入法很容易打出全角「，」，
// 而一个全角逗号会让「4K，中字」变成一个 6 字的单个标签 —— 不报错、只是标签废了，
// 是最难被发现的一类输入问题。
func ParseTagsLine(line string) []string {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == '，' || r == '、' || r == ';' || r == '；'
	})
	return NormalizeTags(fields)
}

// ValidateTags 校验标签是否超限。
//
// maxPerUser / maxLength 为 0 时表示不限。返回的 error 是给用户看的，
// 所以必须写清楚「超了多少、限是多少」—— 只说「标签太多」的话，
// 家人根本不知道自己该怎么改。
func ValidateTags(tags []string, maxPerUser, maxLength int) error {
	if maxPerUser > 0 && len(tags) > maxPerUser {
		return &LimitError{Kind: LimitKindTagCount, Limit: maxPerUser, Actual: len(tags)}
	}
	for _, t := range tags {
		if maxLength > 0 && utf8.RuneCountInString(t) > maxLength {
			return &LimitError{Kind: LimitKindTagLength, Limit: maxLength, Actual: utf8.RuneCountInString(t), Subject: t}
		}
	}
	return nil
}

// LimitError 上限类错误。上限必须报错而不是静默截断 ——
// 静默丢掉第 21 个标签，家人看到「提交成功」却没等到带该标签的文件，
// 排查起来要比当场一句「超了」难得多。
// LimitKind 是 LimitError.Kind 的取值集合。
//
// 用常量而不是裸中文串：API 层要按 Kind 决定返回 429 还是 400，
// 「靠字符串比较中文文案来判断是不是超了每天上限」在文案微调时就会错。
const (
	// LimitKindDaily 今天可求的片数用完了。
	LimitKindDaily = "今天可求的片数"
	// LimitKindPending 同时待审的片数用完了。
	LimitKindPending = "同时待审的片数"
	// LimitKindTagCount 标签个数超了。
	LimitKindTagCount = "标签数"
	// LimitKindTagLength 单个标签太长。
	LimitKindTagLength = "单个标签长度"
)

type LimitError struct {
	// Kind 取值见 LimitKind* 常量。
	Kind    string
	Limit   int    // 上限
	Actual  int    // 实际
	Subject string // 具体是哪个超了（只有一个标签超长时才有）
}

func (e *LimitError) Error() string {
	if e.Subject != "" {
		return fmt.Sprintf("%s超限：%q 已经 %d 个字，上限 %d 个字", e.Kind, e.Subject, e.Actual, e.Limit)
	}
	return fmt.Sprintf("%s超限：本次 %d 个，上限 %d 个", e.Kind, e.Actual, e.Limit)
}

// MergeTags 把新提交的标签并进已有集合，返回合并后的结果。
//
// 语义是「替换」：结果 = 新提交的那一份（去重后），已有集合只用来在**新增会超限**时
// 提前拦住。替换而不是追加的理由写在 Store.UpsertTags 上 —— 没有删除入口的标签
// 集合，额度用完就等于永久锁死这部作品。
func MergeTags(existing, incoming []string, maxPerUser, maxLength int) ([]string, error) {
	next := NormalizeTags(incoming)
	if err := ValidateTags(next, maxPerUser, maxLength); err != nil {
		return nil, err
	}
	return next, nil
}
