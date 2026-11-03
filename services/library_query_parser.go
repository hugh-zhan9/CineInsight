package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrLibraryQueryInvalid = errors.New("library_query_invalid: 查询或条件无效")
var ErrTextAIOutput = errors.New("ai_output_invalid: 模型返回的条件无法校验")
var ErrLibraryQueryExpired = errors.New("query_draft_expired: 查询草稿已过期，请重新解析")

// LibraryQueryCondition is a reviewed condition; entity facts are resolved locally.
type LibraryQueryCondition struct {
	ID                         string `json:"condition_id"`
	Kind                       string `json:"kind"`
	Operator                   string `json:"operator"`
	Value                      string `json:"value"`
	SourceText                 string `json:"source_text"`
	Label                      string `json:"label"`
	EntityID                   uint   `json:"entity_id"`
	RequiresRelationAcceptance bool   `json:"requires_relation_acceptance"`
}
type LibraryQueryCandidate struct {
	ID             uint   `json:"id"`
	Label          string `json:"label"`
	Disambiguation string `json:"disambiguation"`
}
type LibraryQueryIssue struct {
	ID         string                  `json:"id"`
	SourceText string                  `json:"source_text"`
	Reason     string                  `json:"reason"`
	Candidates []LibraryQueryCandidate `json:"candidates"`
}
type parsedLibraryQuery struct {
	Conditions  []LibraryQueryCondition
	Unsupported []LibraryQueryIssue
}
type queryModelCondition struct {
	Kind       string `json:"kind"`
	Operator   string `json:"operator"`
	Value      string `json:"value"`
	SourceText string `json:"source_text"`
}
type queryModelOutput struct {
	Conditions  []queryModelCondition `json:"conditions"`
	Unsupported []string              `json:"unsupported"`
}

var queryEntityRE = regexp.MustCompile(`(人物|标签|出演|主演|导演|编剧|配音)\s+([^\s,，、;；+＋]+)`)

const queryNumberPattern = `(?:[0-9]+个半|[0-9]+(?:\.[0-9]+)?|[零〇一二两三四五六七八九十百千]+个半|[零〇一二两三四五六七八九十百千]+|半)`

var queryDurationRE = regexp.MustCompile(`(` + queryNumberPattern + `)\s*个?\s*(分钟|小时)\s*(及以下|以内|以下|之内|内)`)
var queryDurationPrefixRE = regexp.MustCompile(`(?:不超过|不要超过|最多|≤|<=)\s*(` + queryNumberPattern + `)\s*个?\s*(分钟|小时)`)
var queryDurationMinimumRE = regexp.MustCompile(`(?:至少|不少于|大于等于|≥|>=)\s*(` + queryNumberPattern + `)\s*个?\s*(分钟|小时)`)
var queryDurationMinimumSuffixRE = regexp.MustCompile(`(` + queryNumberPattern + `)\s*个?\s*(分钟|小时)\s*(及以上|以上)`)
var queryNeutralRE = regexp.MustCompile(`我想看|我想找|帮我找|帮我|给我|想看|想找|推荐|影片|电影|视频|一些|一下|有关|关于|请|我|的|还|过|看|找|只要|只`)

var queryWatchRE = regexp.MustCompile(`未看过|没有看过|没看过|未看|没看|已看过|看过|已看|收藏`)
var queryConnectorRE = regexp.MustCompile(`[\s,，、;；+＋]+|并且|同时|且|和`)
var queryUnsupportedRE = regexp.MustCompile(`(?i)或者|或|\bOR\b|排除|除了|不含|不要人物|不是人物`)
var queryRoleRE = regexp.MustCompile(`出演|主演|导演|编剧|配音|客串|制片`)

func normalizeLibraryQueryText(text string) (string, error) {
	text = strings.TrimSpace(text)
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 500 {
		return "", ErrLibraryQueryInvalid
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return "", ErrLibraryQueryInvalid
		}
	}
	return text, nil
}
func validQueryCondition(c LibraryQueryCondition) bool {
	switch c.Kind {
	case "watch":
		return c.Operator == "eq" && (c.Value == "watched" || c.Value == "unwatched")
	case "favorite":
		return c.Operator == "eq" && c.Value == "true"
	case "duration":
		n, err := strconv.Atoi(c.Value)
		return err == nil && strconv.Itoa(n) == c.Value && n > 0 && n <= 86400 && (c.Operator == "lte" || c.Operator == "gte")
	case "person", "tag":
		for _, r := range c.Value {
			if unicode.IsControl(r) {
				return false
			}
		}
		return (c.Operator == "and" || c.Operator == "eq") && strings.TrimSpace(c.Value) == c.Value && c.Value != "" && utf8.RuneCountInString(c.Value) <= 200
	}
	return false
}
func queryConditionLabel(c LibraryQueryCondition) string {
	switch c.Kind {
	case "watch":
		if c.Value == "watched" {
			return "已看"
		}
		return "未看"
	case "favorite":
		return "仅收藏"
	case "duration":
		if c.Operator == "gte" {
			return "时长至少 " + c.Value + " 秒"
		}
		return "时长不超过 " + c.Value + " 秒"
	case "person":
		return "人物关联：" + c.Value
	case "tag":
		return "标签：" + c.Value
	}
	return c.SourceText
}
func finishParsedQuery(result *parsedLibraryQuery, text string) {
	for i := range result.Conditions {
		c := &result.Conditions[i]
		c.ID = fmt.Sprintf("condition-%d", i+1)
		c.Label = queryConditionLabel(*c)
		c.RequiresRelationAcceptance = c.Kind == "person" && queryRoleRE.MatchString(text)
	}
	for _, word := range queryUnsupportedRE.FindAllString(text, -1) {
		covered := false
		for _, issue := range result.Unsupported {
			if strings.Contains(issue.SourceText, word) {
				covered = true
				break
			}
		}
		if !covered {
			result.Unsupported = append(result.Unsupported, LibraryQueryIssue{SourceText: word, Reason: "暂不支持这个组合或否定条件", Candidates: []LibraryQueryCandidate{}})
		}
	}
	for i := range result.Unsupported {
		result.Unsupported[i].ID = fmt.Sprintf("unsupported-%d", i+1)
	}
}
func queryRemainders(text string, covered []bool) []string {
	parts := []string{}
	start := -1
	for i := 0; i <= len(text); i++ {
		if i < len(text) && !covered[i] {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			piece := strings.TrimSpace(text[start:i])
			if queryConnectorRE.ReplaceAllString(piece, "") != "" {
				parts = append(parts, piece)
			}
			start = -1
		}
	}
	return parts
}
func queryDurationSeconds(number, unit string) (string, bool) {
	value, ok := queryNumber(number)
	if !ok {
		return "", false
	}
	factor := int64(60)
	if unit == "小时" {
		factor = 3600
	}
	value.Mul(value, big.NewRat(factor, 1))
	if !value.IsInt() || !value.Num().IsInt64() {
		return "", false
	}
	seconds := value.Num().Int64()
	if seconds < 1 || seconds > 86400 {
		return "", false
	}
	return strconv.FormatInt(seconds, 10), true
}
func parseLocalLibraryQuery(text string) (*parsedLibraryQuery, error) {
	text, err := normalizeLibraryQueryText(text)
	if err != nil {
		return nil, err
	}
	result := &parsedLibraryQuery{Conditions: []LibraryQueryCondition{}, Unsupported: []LibraryQueryIssue{}}
	covered := make([]bool, len(text))
	type found struct {
		start     int
		condition LibraryQueryCondition
	}
	matches := []found{}
	add := func(start, end int, c LibraryQueryCondition) bool {
		for _, used := range covered[start:end] {
			if used {
				return false
			}
		}
		if !validQueryCondition(c) {
			return false
		}
		for i := start; i < end; i++ {
			covered[i] = true
		}
		matches = append(matches, found{start, c})
		return true
	}
	for _, m := range queryEntityRE.FindAllStringSubmatchIndex(text, -1) {
		kind := "person"
		if text[m[2]:m[3]] == "标签" {
			kind = "tag"
		}
		add(m[0], m[1], LibraryQueryCondition{Kind: kind, Operator: "and", Value: text[m[4]:m[5]], SourceText: text[m[0]:m[1]]})
	}
	for _, rule := range []struct {
		pattern  *regexp.Regexp
		operator string
	}{{queryDurationRE, "lte"}, {queryDurationPrefixRE, "lte"}, {queryDurationMinimumRE, "gte"}, {queryDurationMinimumSuffixRE, "gte"}} {
		for _, m := range rule.pattern.FindAllStringSubmatchIndex(text, -1) {
			if m[0] > 0 && text[m[0]-1] == '-' {
				continue
			}
			value, ok := queryDurationSeconds(text[m[2]:m[3]], text[m[4]:m[5]])
			if ok {
				add(m[0], m[1], LibraryQueryCondition{Kind: "duration", Operator: rule.operator, Value: value, SourceText: text[m[0]:m[1]]})
			}
		}
	}
	for _, m := range queryWatchRE.FindAllStringIndex(text, -1) {
		word := text[m[0]:m[1]]
		kind, value := "watch", "watched"
		if strings.HasPrefix(word, "未") || strings.HasPrefix(word, "没") {
			value = "unwatched"
		}
		if word == "收藏" {
			kind, value = "favorite", "true"
		}
		add(m[0], m[1], LibraryQueryCondition{Kind: kind, Operator: "eq", Value: value, SourceText: word})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
	for _, match := range matches {
		result.Conditions = append(result.Conditions, match.condition)
	}
	for _, part := range queryRemainders(text, covered) {
		result.Unsupported = append(result.Unsupported, LibraryQueryIssue{SourceText: part, Reason: "本地语法尚不能解释，请改写或明确移除", Candidates: []LibraryQueryCandidate{}})
	}
	finishParsedQuery(result, text)
	if len(result.Conditions) > 32 || len(result.Unsupported) > 32 {
		return nil, ErrLibraryQueryInvalid
	}
	return result, nil
}
func parseExternalLibraryQuery(text, body string) (*parsedLibraryQuery, error) {
	text, err := normalizeLibraryQueryText(text)
	if err != nil {
		return nil, err
	}
	var output queryModelOutput
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return nil, ErrTextAIOutput
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, ErrTextAIOutput
	}
	if output.Conditions == nil || output.Unsupported == nil || len(output.Conditions)+len(output.Unsupported) == 0 || len(output.Conditions) > 32 || len(output.Unsupported) > 32 {
		return nil, ErrTextAIOutput
	}
	result := &parsedLibraryQuery{Conditions: []LibraryQueryCondition{}, Unsupported: []LibraryQueryIssue{}}
	mark := func(fragment string) bool {
		return strings.TrimSpace(fragment) != "" && strings.Contains(text, fragment)
	}
	for _, item := range output.Conditions {
		c := LibraryQueryCondition{Kind: item.Kind, Operator: item.Operator, Value: item.Value, SourceText: item.SourceText}
		if !validQueryCondition(c) || !mark(c.SourceText) {
			return nil, ErrTextAIOutput
		}
		result.Conditions = append(result.Conditions, c)
	}
	for _, fragment := range output.Unsupported {
		if !mark(fragment) {
			return nil, ErrTextAIOutput
		}
		result.Unsupported = append(result.Unsupported, LibraryQueryIssue{SourceText: fragment, Reason: "暂不支持这个条件，请改写或明确移除", Candidates: []LibraryQueryCandidate{}})
	}
	if !queryKnownConstraintsPreserved(text, result) {
		return nil, ErrTextAIOutput
	}
	groundExternalQuery(text, result)
	finishParsedQuery(result, text)
	if len(result.Unsupported) > 32 {
		return nil, ErrTextAIOutput
	}
	return result, nil
}

// Coverage alone is not semantic evidence: a model cannot attach a whole sentence
// to one condition and thereby erase other predicates we can parse deterministically.
func queryKnownConstraintsPreserved(text string, result *parsedLibraryQuery) bool {
	known, err := parseLocalLibraryQuery(text)
	if err != nil {
		return false
	}

	for _, atom := range known.Conditions {
		accounted := false
		for _, condition := range result.Conditions {
			if sameQueryAtom(atom, condition) {
				accounted = true
				break
			}
		}
		if !accounted {
			for _, issue := range result.Unsupported {
				if strings.Contains(issue.SourceText, atom.SourceText) {
					accounted = true
					break
				}
			}
		}
		if !accounted {
			return false
		}
	}
	for _, condition := range result.Conditions {
		source, err := parseLocalLibraryQuery(condition.SourceText)
		if err != nil {
			return false
		}
		hasKind, matches := false, false
		for _, atom := range source.Conditions {
			if atom.Kind == condition.Kind {
				hasKind = true
				if sameQueryAtom(atom, condition) {
					matches = true
				}
			}
		}
		if hasKind && !matches {
			return false
		}
	}
	return true
}

func queryNumber(number string) (*big.Rat, bool) {
	if strings.HasSuffix(number, "个半") {
		base, ok := queryNumber(strings.TrimSuffix(number, "个半"))
		if !ok {
			return nil, false
		}
		return base.Add(base, big.NewRat(1, 2)), true
	}
	if number == "半" {
		return big.NewRat(1, 2), true
	}
	if value, ok := new(big.Rat).SetString(number); ok {
		return value, true
	}
	digits := map[rune]int64{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	units := map[rune]int64{'十': 10, '百': 100, '千': 1000}
	total, current, lastUnit := int64(0), int64(0), int64(10000)
	haveDigit := false
	for _, r := range number {
		if digit, ok := digits[r]; ok {
			if haveDigit && current != 0 {
				return nil, false
			}
			current = digit
			haveDigit = true
			continue
		}
		unit, ok := units[r]
		if !ok || unit >= lastUnit {
			return nil, false
		}
		if !haveDigit {
			current = 1
		}
		total += current * unit
		current = 0
		haveDigit = false
		lastUnit = unit
	}
	if number == "" {
		return nil, false
	}
	return big.NewRat(total+current, 1), true
}
func sameQueryAtom(a, b LibraryQueryCondition) bool {
	if a.Kind != b.Kind || !strings.EqualFold(a.Value, b.Value) {
		return false
	}
	return a.Kind == "person" || a.Kind == "tag" || a.Operator == b.Operator
}

// Only validated predicate spans count as explained. A large source_text cannot
// consume unknown wording; remaining requirements are surfaced for explicit review.
func groundExternalQuery(text string, result *parsedLibraryQuery) {
	covered := make([]bool, len(text))
	markAt := func(start, end int) {
		for i := start; i < end; i++ {
			covered[i] = true
		}
	}
	markFragment := func(fragment string) {
		for start := 0; start < len(text); {
			offset := strings.Index(text[start:], fragment)
			if offset < 0 {
				return
			}
			start += offset
			markAt(start, start+len(fragment))
			start += len(fragment)
		}
	}
	for _, issue := range result.Unsupported {
		markFragment(issue.SourceText)
	}
	retained := []LibraryQueryCondition{}
	for _, condition := range result.Conditions {
		local, _ := parseLocalLibraryQuery(condition.SourceText)
		fragments := []string{}
		if local != nil {
			for _, atom := range local.Conditions {
				if sameQueryAtom(atom, condition) {
					fragments = append(fragments, atom.SourceText)
				}
			}
		}
		if len(fragments) == 0 && (condition.Kind == "person" || condition.Kind == "tag") {
			prefix, suffix := `(?:标签|类型|题材)?\s*`, ``
			if condition.Kind == "person" {
				prefix = `(?:人物|演员|出演|主演|导演|编剧|配音)?\s*`
				suffix = `(?:出演|主演|导演|编剧|配音)?`
			}
			pattern := regexp.MustCompile(`(?i)` + prefix + regexp.QuoteMeta(condition.Value) + suffix)
			fragments = pattern.FindAllString(condition.SourceText, -1)
		}
		if len(fragments) == 0 {
			result.Unsupported = append(result.Unsupported, LibraryQueryIssue{SourceText: condition.SourceText, Reason: "模型给出的条件无法从这段文字核对，请改写或明确移除", Candidates: []LibraryQueryCandidate{}})
			markFragment(condition.SourceText)
			continue
		}
		retained = append(retained, condition)
		for _, fragment := range fragments {
			markFragment(fragment)
		}
	}
	result.Conditions = retained
	for _, fragment := range queryRemainders(text, covered) {
		if queryNeutralRE.ReplaceAllString(queryConnectorRE.ReplaceAllString(fragment, ""), "") == "" {
			continue
		}
		result.Unsupported = append(result.Unsupported, LibraryQueryIssue{SourceText: fragment, Reason: "这部分要求尚未转换为可核对的条件，请改写或明确移除", Candidates: []LibraryQueryCandidate{}})
	}
}
