package services

import (
	"regexp"
	"strconv"
	"strings"
)

// 剧集文件名解析（D-023）。纯函数，不碰数据库、不碰磁盘：
// 解析规则是行为契约，表驱动测试直接钉这几个正则与优先级。
//
// 只看文件名（不含扩展名），不看目录名——目录层级在不同库里含义完全不同，
// 拿它猜系列名的误判率比收益高得多（D-023 边界）。

// suggestionFullWidthDigits 把全角数字规范化成半角。中文剧集命名里
// 「第０３集」与「第03集」是同一部片子的同一集，不能因为字形不同分成两组。
var suggestionFullWidthDigits = strings.NewReplacer(
	"０", "0", "１", "1", "２", "2", "３", "3", "４", "4",
	"５", "5", "６", "6", "７", "7", "８", "8", "９", "9",
)

// suggestionBracketGroupRe 匹配方括号组名/来源标记（`[字幕组]`、`[WEB-DL]`）。
// 纯数字的方括号是集号（`[03]`），由 suggestionBracketEpisodeRe 负责，去噪时必须留着。
var suggestionBracketGroupRe = regexp.MustCompile(`\[[^\[\]]*\]`)

// suggestionBracketDigitsRe 判定一个方括号内容是不是纯集号。
var suggestionBracketDigitsRe = regexp.MustCompile(`^\[(\d{1,3})\]$`)

// suggestionQualityTagRe 是质量/编码/来源标签的固定清单。去噪只为了让系列名不
// 带这些尾巴，清单之外的词一律保留——宁可系列名多一个词（同系列的文件都会多同
// 一个词，照样分到一组），也不要把真实片名当噪音删掉。
var suggestionQualityTagRe = regexp.MustCompile(`(?i)\b(2160p|1080[pi]|720p|576p|480p|4k|8k|x26[45]|h\.?26[45]|hevc|avc|web-?dl|web-?rip|bluray|blu-ray|bd-?rip|dvd-?rip|hdtv|remux|10bit|8bit|hdr10\+?|hdr|aac(?:\d(?:\.\d)?)?|flac|dts(?:-hd)?|truehd|ddp?\d(?:\.\d)?)\b`)

// 五种剧集模式，按优先级从高到低（D-023）。第一个命中的模式决定季/集与系列名。
var (
	suggestionSeasonEpisodeRe  = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,3})`)
	suggestionChineseEpisodeRe = regexp.MustCompile(`第\s*(\d{1,4})\s*[集话話]`)
	suggestionEPrefixEpisodeRe = regexp.MustCompile(`(?i)\bEP?\s*(\d{1,3})\b`)
	suggestionBracketEpisodeRe = regexp.MustCompile(`\[(\d{1,3})\]`)
	suggestionDashEpisodeRe    = regexp.MustCompile(`\s-\s(\d{1,3})(?:[vV]\d)?$`)
)

// suggestionEpisodePatterns 是只给出集号的四条模式（`SxxExx` 另算，它还给季号），
// 顺序即优先级。bracketTitle 标记那条方括号集号模式：它命中而系列名为空时，
// 还有"片名也在方括号里"这一种救法（D-023 修订）。
var suggestionEpisodePatterns = []struct {
	re           *regexp.Regexp
	bracketTitle bool
}{
	{re: suggestionChineseEpisodeRe},
	{re: suggestionEPrefixEpisodeRe},
	{re: suggestionBracketEpisodeRe, bracketTitle: true},
	{re: suggestionDashEpisodeRe},
}

var (
	// suggestionSeparatorRe 收系列名时用：`.` `_` `-` 都是分隔符。
	suggestionSeparatorRe = regexp.MustCompile(`[._\-]+`)
	// suggestionWordSeparatorRe 只在匹配前把 `.` 与 `_` 换成空格。
	//
	// 必须在匹配之前做：`\bEP?\s*\d` 与 `\b` 用的是 ASCII 词边界，而下划线是词
	// 字符——`Dark_Matter_EP08` 里 `_E` 之间没有边界，不先换掉的话 `EP` 这条
	// 模式在下划线命名上永远不命中。`-` 不能一起换：`\s-\s(\d{1,3})$` 这条模式
	// 就是靠短横线本身认集号的。
	suggestionWordSeparatorRe = regexp.MustCompile(`[._]+`)
	suggestionSpaceRe         = regexp.MustCompile(`\s+`)
)

// ParsedEpisode 是一次文件名解析的结果。Season 为 nil 表示文件名里没有季信息
// （只有 `SxxExx` 给得出季），Episode 恒非 nil（OK 为 true 时）。
type ParsedEpisode struct {
	Series           string
	NormalizedSeries string
	Season           *int
	Episode          *int
	OK               bool
}

// ParseEpisodeFromFileName 解析不含扩展名的文件名。任何模式都不命中、或者命中
// 位置之前没剩下系列名时返回 OK=false：没有名字的候选没法命名，也没法给用户看。
func ParseEpisodeFromFileName(stem string) ParsedEpisode {
	normalized := suggestionFullWidthDigits.Replace(stem)
	denoised := denoiseEpisodeFileName(normalized)

	if match := suggestionSeasonEpisodeRe.FindStringSubmatchIndex(denoised); match != nil {
		season := mustParseEpisodeNumber(denoised[match[2]:match[3]])
		episode := mustParseEpisodeNumber(denoised[match[4]:match[5]])
		return buildParsedEpisode(denoised[:match[0]], &season, &episode)
	}
	for _, pattern := range suggestionEpisodePatterns {
		match := pattern.re.FindStringSubmatchIndex(denoised)
		if match == nil {
			continue
		}
		episode := mustParseEpisodeNumber(denoised[match[2]:match[3]])
		parsed := buildParsedEpisode(denoised[:match[0]], nil, &episode)
		if parsed.OK || !pattern.bracketTitle {
			return parsed
		}
		// 命中位置之前什么都没剩下，说明片名本身也在方括号里（番剧常见的
		// `[组名][片名][05][1080p]`）。回到原串按方括号取片名（D-023 修订）。
		return parseBracketTitleEpisode(normalized)
	}
	return ParsedEpisode{}
}

// parseBracketTitleEpisode 处理"片名整个在方括号里"的命名（D-023 修订）。
//
// 只在第 4 条模式 `[NN]` 命中、且去掉方括号组名后系列名为空时才走到这里。取的是
// 紧邻集号括号之前、内容既不是纯数字也不是质量/编码/来源标签的那个方括号组。
//
// 它必须不是**第一个**方括号组：第一个括号按惯例是发布组名，`[组名][05]` 里没有
// 片名可取，硬拿组名当片名会把不同番剧凑成一组。只有集号一个括号时更没得取。
func parseBracketTitleEpisode(name string) ParsedEpisode {
	groups := suggestionBracketGroupRe.FindAllString(name, -1)
	episodeIndex, episode := -1, 0
	for index, group := range groups {
		digits := suggestionBracketDigitsRe.FindStringSubmatch(group)
		if digits == nil {
			continue
		}
		episodeIndex, episode = index, mustParseEpisodeNumber(digits[1])
		break
	}
	if episodeIndex < 0 {
		return ParsedEpisode{}
	}
	titleIndex := -1
	for index, group := range groups[:episodeIndex] {
		content := bracketGroupContent(group)
		if isNumericBracketContent(content) || isQualityOnlyBracketContent(content) {
			continue
		}
		if cleanSeriesName(content) == "" {
			continue
		}
		titleIndex = index
	}
	if titleIndex < 1 {
		return ParsedEpisode{}
	}
	series := cleanSeriesName(bracketGroupContent(groups[titleIndex]))
	return ParsedEpisode{
		Series:           series,
		NormalizedSeries: strings.ToLower(series),
		Episode:          &episode,
		OK:               true,
	}
}

func bracketGroupContent(group string) string {
	return strings.TrimSuffix(strings.TrimPrefix(group, "["), "]")
}

// isNumericBracketContent 把 `[2019]`、`[05]`、`[]` 都判为不能当片名。
func isNumericBracketContent(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true
	}
	for _, char := range trimmed {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// isQualityOnlyBracketContent 报告括号内容是否只由质量/编码/来源标签组成
// （`[1080p]`、`[BDRip x265]`）。清单之外的词一律当片名候选保留。
func isQualityOnlyBracketContent(content string) bool {
	stripped := suggestionQualityTagRe.ReplaceAllString(content, " ")
	stripped = suggestionSeparatorRe.ReplaceAllString(stripped, " ")
	return strings.TrimSpace(stripped) == ""
}

func buildParsedEpisode(prefix string, season, episode *int) ParsedEpisode {
	series := cleanSeriesName(prefix)
	if series == "" {
		return ParsedEpisode{}
	}
	return ParsedEpisode{
		Series:           series,
		NormalizedSeries: strings.ToLower(series),
		Season:           season,
		Episode:          episode,
		OK:               true,
	}
}

// denoiseEpisodeFileName 去掉方括号组名与质量标签，纯数字方括号（集号）留着，
// 最后把 `.` 与 `_` 换成空格。质量标签必须在换分隔符之前删：`h.264` 这类写法
// 靠点号连着，换成空格就认不出来了。
func denoiseEpisodeFileName(name string) string {
	name = suggestionBracketGroupRe.ReplaceAllStringFunc(name, func(group string) string {
		if suggestionBracketDigitsRe.MatchString(group) {
			return group
		}
		return " "
	})
	name = suggestionQualityTagRe.ReplaceAllString(name, " ")
	return suggestionWordSeparatorRe.ReplaceAllString(name, " ")
}

// cleanSeriesName 把命中位置之前的部分收成系列名：分隔符换空格、折叠空白。
func cleanSeriesName(prefix string) string {
	prefix = suggestionSeparatorRe.ReplaceAllString(prefix, " ")
	return strings.TrimSpace(suggestionSpaceRe.ReplaceAllString(prefix, " "))
}

// mustParseEpisodeNumber 只接收正则里 `\d{1,4}` 捕获到的内容，必然可解析。
func mustParseEpisodeNumber(digits string) int {
	value, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return value
}
