package services

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 按文件夹建议作品集（用户 2026-10-14 修订 D-023：文件夹结构与文件名一起判断）。
//
// 纯函数，不碰数据库、不碰磁盘：输入是一个扫描根下的视频（路径 + 文件名解析结果）
// 与人物名集合，输出是按文件夹成组的候选。结果只是建议，不建任何作品集。

const (
	// folderSuggestionKeyPrefix 写在 normalized_series 前面，区分「按文件夹」与「按文件名」
	// 两种来源；忽略记忆键沿用 sha256(扫描根 + "\n" + normalized_series)，因此按文件夹记忆。
	folderSuggestionKeyPrefix = "dir:"
	// folderSuggestionKeyMaxBytes 对应 normalized_series / series_name 的 size:200。
	// 按字节截断比按字符更严，SQLite 与 Postgres 两边都放得下。
	folderSuggestionKeyMaxBytes = 200
	// folderSuggestionMinMembers：成员不足 2 个不成候选（与按文件名同一口径）。
	folderSuggestionMinMembers = 2
	// folderSuggestionMaxLooseMembers：既不像同一系列、也没有章节子目录的文件夹，
	// 只有不超过这么多个视频时才凭「名字像片名」成候选；再多更像一个大分类。
	folderSuggestionMaxLooseMembers = 100
	// folderSuggestionContainerChildren：有这么多个（非章节）子文件夹各自成候选时，
	// 这个文件夹是分类容器，自己不成候选。
	folderSuggestionContainerChildren = 2
	// folderSuggestionCommonPrefixMinRunes：全部文件名共有的前缀至少这么长才算同一系列。
	folderSuggestionCommonPrefixMinRunes = 3
)

// folderSuggestionMeaninglessNames 是不成候选、也不拿来当名字的文件夹名（比较前转小写、
// 去掉末尾的副本编号「 (2)」「 2」）。后半是通用分类名：它们装的是一类片子，不是一部作品。
var folderSuggestionMeaninglessNames = map[string]struct{}{
	"新建文件夹": {}, "new folder": {}, "untitled folder": {}, "untitled": {},
	"trash": {}, ".trash": {}, "回收站": {}, "归档": {}, "archive": {}, "archives": {},
	"temp": {}, "tmp": {}, "下载": {}, "downloads": {}, "download": {}, "未分类": {},
	"misc": {}, "其他": {}, "其它": {}, "sample": {}, "samples": {},
	"电影": {}, "视频": {}, "影视": {}, "电视剧": {}, "剧集": {}, "动漫": {}, "动画": {},
	"综艺": {}, "纪录片": {}, "直播": {}, "录屏": {}, "movies": {}, "movie": {},
	"videos": {}, "video": {}, "films": {}, "tv": {}, "tv shows": {}, "shows": {}, "anime": {},
}

// folderSuggestionJunkNames 里的单视频子文件夹不当发布包装上提：sample 片段不是正片。
var folderSuggestionJunkNames = map[string]struct{}{
	"sample": {}, "samples": {}, "trash": {}, ".trash": {}, "回收站": {},
}

var (
	// folderSuggestionCopySuffixRe 去掉「新建文件夹 (2)」「New Folder 3」的副本编号。
	folderSuggestionCopySuffixRe = regexp.MustCompile(`\s*(?:\(\d+\)|（\d+）|\d+)$`)
	// folderSuggestionDateLikeRe：纯日期/数字（`2021`、`2021-12-04`、`20211204`、`2021年12月`）。
	folderSuggestionDateLikeRe = regexp.MustCompile(`^[\d\s._\-:/年月日号]+$`)
	// folderSuggestionGeneratedRe：自动生成的名字（`From-Telegram-20211204T015145791Z`）。
	folderSuggestionGeneratedRe = regexp.MustCompile(`(?i)\d{8}T\d{6}|^from[-_ ]telegram`)
	// folderSuggestionChineseChapterRe：第N周/章/季/部/期/节/课/篇/卷/单元/模块（另含讲/天/阶段），
	// N 可以是数字或中文数字，后面允许接说明文字（「第十三周Go语言实践Runtime」）。
	folderSuggestionChineseChapterRe = regexp.MustCompile(`^第\s*([0-9]+|[零〇一二两三四五六七八九十百]+)\s*(?:周|章|季|部|期|节|節|课|課|篇|卷|单元|單元|模块|模塊|讲|講|天|阶段|階段)`)
	// folderSuggestionLatinChapterRe：Season N / Part N / Disc N / CD N / Vol N 等，名字以它开头。
	folderSuggestionLatinChapterRe = regexp.MustCompile(`(?i)^(?:season|part|disc|disk|cd|vol(?:ume)?|chapter|week|lesson|module|unit)[\s._\-]*(\d{1,3})(?:$|[^\d])`)
	// folderSuggestionShortSeasonRe：整名以 `S01` 开头（`S01`、`S01 1080p`），`S01E01` 不算。
	folderSuggestionShortSeasonRe = regexp.MustCompile(`(?i)^s(\d{1,2})(?:$|[\s._\-\]])`)
	// folderSuggestionNumberOnlyRe：纯序号子文件夹（`01`、`2`）当章节。
	folderSuggestionNumberOnlyRe = regexp.MustCompile(`^(\d{1,3})$`)
	// folderSuggestionNumberedChapterRe：序号 + 说明（`01-基础篇`、`02 进阶`、`3.高级`）。单个这样的名字可能就是片名
	// （`3 Idiots`），所以只在扫描根以外、同一层至少 folderSuggestionNumberedChapterMin 个兄弟都这样命名时才当章节。
	folderSuggestionNumberedChapterRe = regexp.MustCompile(`^(\d{1,3})[\s._\-、．·]+\S`)
)

// folderSuggestionNumberedChapterMin：同一层至少这么多个「序号 + 说明」的子文件夹，才把它们当章节。
const folderSuggestionNumberedChapterMin = 2

var (
	// folderSuggestionBracketRe 是文件夹名里的来源/组名括号（`[字幕组]`、`【公众号】`）。
	folderSuggestionBracketRe = regexp.MustCompile(`\[[^\[\]]*\]|【[^【】]*】`)
	// folderSuggestionCutRes：名字在这些位置截断（`SxxExx`/`Sxx`/`Season N`/`第N季`/年份），
	// 第一个质量标签另由 suggestionQualityTagRe 给出。取最靠前、且前面还剩名字的那一处。
	folderSuggestionCutRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bS\d{1,2}(?:E\d{1,3})?\b`),
		regexp.MustCompile(`(?i)\bSeason[\s._\-]*\d{1,2}\b`),
		regexp.MustCompile(`第\s*(?:[0-9]+|[零〇一二两三四五六七八九十百]+)\s*季`),
		regexp.MustCompile(`\b(?:19|20)\d{2}\b`),
		suggestionQualityTagRe,
	}
	// folderSuggestionSeasonMarkRe 从文件夹名里取季号，用于同名文件夹合并后的先后。
	folderSuggestionSeasonMarkRe = regexp.MustCompile(`(?i)\bS(\d{1,2})\b|\bSeason[\s._\-]*(\d{1,2})\b|第\s*([0-9]+|[零〇一二两三四五六七八九十百]+)\s*季`)
	folderSuggestionTrimChars    = " \t-–—_.([{（【「·,，:："
)

// parseChineseNumeral 解析「十四」「二十」「一百零五」这类中文数字；遇到别的字符返回 false。
func parseChineseNumeral(text string) (int, bool) {
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, current := 0, 0
	for _, char := range text {
		switch {
		case char == '十':
			if current == 0 {
				current = 1
			}
			total += current * 10
			current = 0
		case char == '百':
			if current == 0 {
				current = 1
			}
			total += current * 100
			current = 0
		default:
			value, ok := digits[char]
			if !ok {
				return 0, false
			}
			current = value
		}
	}
	total += current
	return total, text != ""
}

// parseFolderNumber 接收阿拉伯数字或中文数字。
func parseFolderNumber(text string) (int, bool) {
	if value, err := strconv.Atoi(text); err == nil {
		return value, true
	}
	return parseChineseNumeral(text)
}

// folderChapterNumber 判断文件夹名是不是章节（第一周、Season 2、S01、CD1、01），是则返回编号。
func folderChapterNumber(name string) (int, bool) {
	name = strings.TrimSpace(suggestionFullWidthDigits.Replace(name))
	for _, re := range []*regexp.Regexp{folderSuggestionChineseChapterRe, folderSuggestionLatinChapterRe, folderSuggestionShortSeasonRe, folderSuggestionNumberOnlyRe} {
		if match := re.FindStringSubmatch(name); match != nil {
			return parseFolderNumber(match[1])
		}
	}
	return 0, false
}

// numberedChapterChildren 找出 node 下「序号 + 说明」命名的子文件夹（不含已按 folderChapterNumber 认出的）；
// 扫描根下不认（根下按序号排的往往是各自独立的系列），不足 folderSuggestionNumberedChapterMin 个也不认。
func numberedChapterChildren(node *folderSuggestionNode, isRoot bool) map[string]int {
	if isRoot {
		return nil
	}
	found := make(map[string]int)
	for name := range node.children {
		if _, chapter := folderChapterNumber(name); chapter {
			continue
		}
		match := folderSuggestionNumberedChapterRe.FindStringSubmatch(strings.TrimSpace(suggestionFullWidthDigits.Replace(name)))
		if match == nil {
			continue
		}
		if number, ok := parseFolderNumber(match[1]); ok {
			found[name] = number
		}
	}
	if len(found) < folderSuggestionNumberedChapterMin {
		return nil
	}
	return found
}

// folderSeasonMark 从文件夹名里取季号（`Show.S02.1080p` → 2），没有返回 0。
func folderSeasonMark(name string) int {
	match := folderSuggestionSeasonMarkRe.FindStringSubmatch(suggestionFullWidthDigits.Replace(name))
	if match == nil {
		return 0
	}
	for _, group := range match[1:] {
		if value, ok := parseFolderNumber(group); ok {
			return value
		}
	}
	return 0
}

// cleanFolderSuggestionName 把文件夹名收成候选名：去来源括号（整个名字都在括号里时保留内容）、
// 在 SxxExx/Sxx/Season/第N季/年份/第一个质量标签处截断、`.` `_` 换空格。
// `Immortal.Doctor.In.Modern.City.S01.1080p.iQIYI.WEB-DL.AAC2.0.H.264-MWeb` → `Immortal Doctor In Modern City`。
func cleanFolderSuggestionName(name string) string {
	name = strings.TrimSpace(suggestionFullWidthDigits.Replace(name))
	stripped := folderSuggestionBracketRe.ReplaceAllString(name, " ")
	if tidyFolderName(stripped) == "" {
		stripped = strings.NewReplacer("[", " ", "]", " ", "【", " ", "】", " ").Replace(name)
	}
	cut := len(stripped)
	for _, re := range folderSuggestionCutRes {
		for _, loc := range re.FindAllStringIndex(stripped, -1) {
			if loc[0] >= cut {
				break
			}
			if tidyFolderName(stripped[:loc[0]]) != "" {
				cut = loc[0]
				break
			}
		}
	}
	result := tidyFolderName(stripped[:cut])
	if result == "" {
		result = tidyFolderName(suggestionQualityTagRe.ReplaceAllString(stripped, " "))
	}
	return truncateUTF8(result, folderSuggestionKeyMaxBytes)
}

func tidyFolderName(text string) string {
	text = suggestionWordSeparatorRe.ReplaceAllString(text, " ")
	text = strings.TrimSpace(suggestionSpaceRe.ReplaceAllString(text, " "))
	return strings.Trim(text, folderSuggestionTrimChars)
}

// truncateUTF8 按字节截断到 maxBytes 以内，不切断多字节字符。
func truncateUTF8(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut])
}

// folderNameMeaningless：系统/隐藏目录、固定清单里的名字（含副本编号）、纯日期数字、
// 自动生成的名字，以及收拾之后什么都不剩的名字，一律不成候选、不当名字。
func folderNameMeaningless(raw, cleaned string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" || strings.ContainsAny(lower[:1], ".@$#") {
		return true
	}
	for _, candidate := range []string{lower, strings.TrimSpace(folderSuggestionCopySuffixRe.ReplaceAllString(lower, "")), strings.ToLower(cleaned)} {
		if _, listed := folderSuggestionMeaninglessNames[candidate]; listed {
			return true
		}
	}
	if folderSuggestionGeneratedRe.MatchString(raw) || folderSuggestionDateLikeRe.MatchString(lower) {
		return true
	}
	return cleaned == "" || folderSuggestionDateLikeRe.MatchString(cleaned)
}

// folderNameJunk：这类子文件夹里的单个视频不当发布包装上提（sample 片段、回收站）。
func folderNameJunk(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" || strings.ContainsAny(lower[:1], ".@$#") {
		return true
	}
	_, junk := folderSuggestionJunkNames[lower]
	return junk
}

// folderNameIsPerson：文件夹名（原样或收拾后）与某个人物的名字/原名相同，就不当作品集。
func folderNameIsPerson(raw, cleaned string, persons map[string]struct{}) bool {
	for _, candidate := range []string{raw, cleaned} {
		if _, person := persons[normalizeLocalMetadataName(candidate)]; person && candidate != "" {
			return true
		}
	}
	return false
}

// folderSuggestionVideo 是扫描根下的一个视频与它的文件名解析结果（ParseEpisodeFromFileName）。
type folderSuggestionVideo struct {
	ID     uint
	Name   string
	Path   string
	Parsed ParsedEpisode
}

// folderSuggestionMember 是文件夹候选的一个成员。Season 在有章节子目录时是章节号；
// Position 是稠密排名，只有真正的同集多版本（文件名解析出同一系列同一集）才共用。
type folderSuggestionMember struct {
	VideoID  uint
	Season   *int
	Episode  *int
	Position int
}

// folderSuggestionGroup 是一个按文件夹成组的候选。
type folderSuggestionGroup struct {
	// Folders 是成组的文件夹（相对扫描根、`/` 分隔）；同名文件夹合并时有多个，按季排好。
	Folders []string
	Name    string
	// NormalizedKey = "dir:" + 小写的第一个文件夹路径（过长时截断并接哈希），写进 normalized_series。
	NormalizedKey string
	Members       []folderSuggestionMember
}

type folderSuggestionNode struct {
	name     string
	rel      string
	children map[string]*folderSuggestionNode
	files    []folderSuggestionEntry
}

// folderSuggestionEntry 是向上汇集时的一个视频：chapter 非 nil 表示经章节子目录并入，
// seasonMark 是同名文件夹合并时所在文件夹名里的季号。
type folderSuggestionEntry struct {
	video      folderSuggestionVideo
	rel        string
	chapter    *int
	seasonMark int
}

// buildFolderSuggestionTree 按视频路径搭出扫描根下的目录树；不在根下的路径跳过。
func buildFolderSuggestionTree(scanRoot string, videos []folderSuggestionVideo) *folderSuggestionNode {
	root := &folderSuggestionNode{children: map[string]*folderSuggestionNode{}}
	for _, video := range videos {
		rel, err := filepath.Rel(scanRoot, video.Path)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		parts := strings.Split(rel, "/")
		node := root
		for _, part := range parts[:len(parts)-1] {
			child, exists := node.children[part]
			if !exists {
				childRel := part
				if node.rel != "" {
					childRel = node.rel + "/" + part
				}
				child = &folderSuggestionNode{name: part, rel: childRel, children: map[string]*folderSuggestionNode{}}
				node.children[part] = child
			}
			node = child
		}
		node.files = append(node.files, folderSuggestionEntry{video: video, rel: rel})
	}
	return root
}

func sortedFolderChildNames(node *folderSuggestionNode) []string {
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return naturalSuggestionLess(names[i], names[j]) })
	return names
}

func collectFolderSubtree(node *folderSuggestionNode) []folderSuggestionEntry {
	entries := append([]folderSuggestionEntry(nil), node.files...)
	for _, name := range sortedFolderChildNames(node) {
		entries = append(entries, collectFolderSubtree(node.children[name])...)
	}
	return entries
}

// folderSuggestionDraft 是判定成候选、尚未合并与编号的文件夹。
type folderSuggestionDraft struct {
	node    *folderSuggestionNode
	name    string
	entries []folderSuggestionEntry
}

type folderSuggestionBuilder struct {
	persons map[string]struct{}
	drafts  []*folderSuggestionDraft
}

// buildFolderSuggestionGroups 是按文件夹成组的入口：自底向上判定每个文件夹，再把同名文件夹合并、排序、编号。
// persons 是人物名与原名经 normalizeLocalMetadataName 后的集合。
func buildFolderSuggestionGroups(scanRoot string, videos []folderSuggestionVideo, persons map[string]struct{}) []folderSuggestionGroup {
	builder := &folderSuggestionBuilder{persons: persons}
	builder.evaluate(buildFolderSuggestionTree(scanRoot, videos), true)
	return finishFolderSuggestionGroups(builder.drafts)
}

// evaluate 判定 node，返回整棵子树的视频与 node 是否成了候选。
//
//   - 章节子目录（第N周、Season 2、CD1、01…）不单独判定，整棵子树并入本层并带上章节号；
//   - 其余子目录先各自判定：整棵子树只有一个视频的是发布包装，那个视频上提为本层成员；
//     成了候选的子目录记一次，它的视频归它自己；
//   - 本层成候选的条件：不是扫描根、名字有意义且不是人物、成员 ≥2，且 (a) 有章节子目录，
//     或 (b) 文件看起来是同一系列，或 (c) 没有子候选且成员不超过 folderSuggestionMaxLooseMembers；
//     有 ≥folderSuggestionContainerChildren 个子候选的是分类容器，不成候选。
func (b *folderSuggestionBuilder) evaluate(node *folderSuggestionNode, isRoot bool) ([]folderSuggestionEntry, bool) {
	all := append([]folderSuggestionEntry(nil), node.files...)
	pool := append([]folderSuggestionEntry(nil), node.files...)
	childCandidates, hasChapters := 0, false
	numbered := numberedChapterChildren(node, isRoot)
	for _, name := range sortedFolderChildNames(node) {
		child := node.children[name]
		number, chapter := folderChapterNumber(name)
		if !chapter {
			number, chapter = numbered[name]
		}
		if chapter {
			hasChapters = true
			for _, entry := range collectFolderSubtree(child) {
				chapterNumber := number
				entry.chapter = &chapterNumber
				all = append(all, entry)
				pool = append(pool, entry)
			}
			continue
		}
		subtree, candidate := b.evaluate(child, false)
		all = append(all, subtree...)
		switch {
		case candidate:
			childCandidates++
		case len(subtree) == 1 && !folderNameJunk(name):
			pool = append(pool, subtree[0])
		}
	}
	if isRoot || childCandidates >= folderSuggestionContainerChildren || len(pool) < folderSuggestionMinMembers {
		return all, false
	}
	cleaned := cleanFolderSuggestionName(node.name)
	if folderNameMeaningless(node.name, cleaned) || folderNameIsPerson(node.name, cleaned, b.persons) {
		return all, false
	}
	if !hasChapters && !folderEntriesLookLikeSeries(pool) &&
		(childCandidates > 0 || len(pool) > folderSuggestionMaxLooseMembers) {
		return all, false
	}
	b.drafts = append(b.drafts, &folderSuggestionDraft{node: node, name: cleaned, entries: pool})
	return all, true
}

var (
	// folderSuggestionLeadingNumberRe：文件名开头的 1–3 位序号（`01丨开篇`、`[03] xx`、`1.2 xx`）。
	folderSuggestionLeadingNumberRe = regexp.MustCompile(`^[\s\[(【（#]*(\d{1,3})(?:\D|$)`)
	// folderSuggestionChineseOrdinalRe：自然排序前把「第十一」换成「第11」。
	folderSuggestionChineseOrdinalRe = regexp.MustCompile(`第([零〇一二两三四五六七八九十百]+)`)
)

// folderEntriesLookLikeSeries：过半文件解析成同一系列；或过半文件以序号开头且序号不止一个；
// 或全部文件名有至少 folderSuggestionCommonPrefixMinRunes 个字的公共前缀。
func folderEntriesLookLikeSeries(entries []folderSuggestionEntry) bool {
	seriesCount := map[string]int{}
	numbered, numbers := 0, map[int]struct{}{}
	for _, entry := range entries {
		if entry.video.Parsed.OK {
			seriesCount[entry.video.Parsed.NormalizedSeries]++
		}
		if number, ok := leadingSuggestionNumber(entry.video.Name); ok {
			numbered++
			numbers[number] = struct{}{}
		}
	}
	for _, count := range seriesCount {
		if count*2 > len(entries) {
			return true
		}
	}
	if numbered*2 > len(entries) && len(numbers) >= 2 {
		return true
	}
	return utf8.RuneCountInString(commonSuggestionPrefix(entries)) >= folderSuggestionCommonPrefixMinRunes
}

// commonSuggestionPrefix 是全部文件名（去扩展名、转小写）的公共前缀，去掉末尾的分隔符与数字。
func commonSuggestionPrefix(entries []folderSuggestionEntry) string {
	var prefix []rune
	for index, entry := range entries {
		stem := []rune(strings.ToLower(tidyFolderName(fileNameStem(entry.video.Name))))
		if index == 0 {
			prefix = stem
			continue
		}
		length := 0
		for length < len(prefix) && length < len(stem) && prefix[length] == stem[length] {
			length++
		}
		prefix = prefix[:length]
	}
	return strings.TrimRight(string(prefix), " -_.0123456789")
}

func leadingSuggestionNumber(name string) (int, bool) {
	match := folderSuggestionLeadingNumberRe.FindStringSubmatch(suggestionFullWidthDigits.Replace(fileNameStem(name)))
	if match == nil {
		return 0, false
	}
	return mustParseEpisodeNumber(match[1]), true
}

// naturalSuggestionLess 是自然排序：数字按数值比，「第十一」当 11，其余按小写字符比。
func naturalSuggestionLess(a, b string) bool {
	if order := compareNaturalSuggestion(naturalSuggestionKey(a), naturalSuggestionKey(b)); order != 0 {
		return order < 0
	}
	return a < b
}

func naturalSuggestionKey(text string) string {
	text = strings.ToLower(suggestionFullWidthDigits.Replace(text))
	return folderSuggestionChineseOrdinalRe.ReplaceAllStringFunc(text, func(match string) string {
		if value, ok := parseChineseNumeral(strings.TrimPrefix(match, "第")); ok {
			return "第" + strconv.Itoa(value)
		}
		return match
	})
}

func compareNaturalSuggestion(a, b string) int {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isASCIIDigit(a[i]) && isASCIIDigit(b[j]) {
			startA, startB := i, j
			for i < len(a) && isASCIIDigit(a[i]) {
				i++
			}
			for j < len(b) && isASCIIDigit(b[j]) {
				j++
			}
			numberA, numberB := strings.TrimLeft(a[startA:i], "0"), strings.TrimLeft(b[startB:j], "0")
			if len(numberA) != len(numberB) {
				return cmp.Compare(len(numberA), len(numberB))
			}
			if numberA != numberB {
				return strings.Compare(numberA, numberB)
			}
			continue
		}
		runeA, sizeA := utf8.DecodeRuneInString(a[i:])
		runeB, sizeB := utf8.DecodeRuneInString(b[j:])
		if runeA != runeB {
			return cmp.Compare(int(runeA), int(runeB))
		}
		i, j = i+sizeA, j+sizeB
	}
	return cmp.Compare(len(a)-i, len(b)-j)
}

func isASCIIDigit(char byte) bool { return char >= '0' && char <= '9' }

// finishFolderSuggestionGroups 合并同名文件夹、排序、编号并算出记忆键。
//
// 收拾后名字相同（忽略大小写）的候选文件夹，同在一个父目录下、或名字里都带季号
// （`Show.S01…` 与 `Show.S02…`）时合并成一组，按季号排先后；只是名字相同、散在不同
// 位置又没有季号的（`A/Extras` 与 `B/Extras`）各自成组。
func finishFolderSuggestionGroups(drafts []*folderSuggestionDraft) []folderSuggestionGroup {
	var clusters [][]*folderSuggestionDraft
	for _, draft := range drafts {
		joined := false
		for index, cluster := range clusters {
			for _, other := range cluster {
				if canMergeFolderDrafts(other, draft) {
					clusters[index] = append(cluster, draft)
					joined = true
					break
				}
			}
			if joined {
				break
			}
		}
		if !joined {
			clusters = append(clusters, []*folderSuggestionDraft{draft})
		}
	}
	groups := make([]folderSuggestionGroup, 0, len(clusters))
	for _, cluster := range clusters {
		groups = append(groups, buildFolderSuggestionGroup(cluster))
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].NormalizedKey < groups[j].NormalizedKey })
	return groups
}

func canMergeFolderDrafts(a, b *folderSuggestionDraft) bool {
	if !strings.EqualFold(a.name, b.name) {
		return false
	}
	if folderParentRel(a.node.rel) == folderParentRel(b.node.rel) {
		return true
	}
	return folderSeasonMark(a.node.name) > 0 && folderSeasonMark(b.node.name) > 0
}

func folderParentRel(rel string) string {
	if index := strings.LastIndex(rel, "/"); index >= 0 {
		return rel[:index]
	}
	return ""
}

// folderSuggestionKey = "dir:" + 小写相对路径；超过 size:200 时截断并接 16 位哈希，结果确定。
func folderSuggestionKey(rel string) string {
	key := folderSuggestionKeyPrefix + strings.ToLower(rel)
	if len(key) <= folderSuggestionKeyMaxBytes {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	suffix := "#" + hex.EncodeToString(sum[:8])
	return truncateUTF8(key, folderSuggestionKeyMaxBytes-len(suffix)) + suffix
}

// buildFolderSuggestionGroup 排序并编号。顺序：文件夹季号 → 章节号（本层散放的文件在前）
// → 文件名解析出的季/集 → 相对路径的自然排序。
func buildFolderSuggestionGroup(cluster []*folderSuggestionDraft) folderSuggestionGroup {
	merged := len(cluster) > 1
	sort.SliceStable(cluster, func(i, j int) bool {
		left, right := folderSeasonMark(cluster[i].node.name), folderSeasonMark(cluster[j].node.name)
		if left != right {
			return left < right
		}
		return naturalSuggestionLess(cluster[i].node.rel, cluster[j].node.rel)
	})
	folders := make([]string, 0, len(cluster))
	var entries []folderSuggestionEntry
	for _, draft := range cluster {
		folders = append(folders, draft.node.rel)
		mark := 0
		if merged {
			mark = folderSeasonMark(draft.node.name)
		}
		for _, entry := range draft.entries {
			entry.seasonMark = mark
			entries = append(entries, entry)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return folderEntryLess(entries[i], entries[j]) })
	members := make([]folderSuggestionMember, 0, len(entries))
	for index, entry := range entries {
		position := 1
		if index > 0 {
			position = members[index-1].Position
			if !sameFolderSuggestionEpisode(entries[index-1], entry) {
				position++
			}
		}
		members = append(members, folderSuggestionMember{
			VideoID: entry.video.ID, Season: folderEntrySeason(entry), Episode: folderEntryEpisode(entry), Position: position,
		})
	}
	return folderSuggestionGroup{
		Folders: folders, Name: cluster[0].name, NormalizedKey: folderSuggestionKey(cluster[0].node.rel), Members: members,
	}
}

func folderEntryLess(a, b folderSuggestionEntry) bool {
	keysA, keysB := folderEntrySortKeys(a), folderEntrySortKeys(b)
	for index := range keysA {
		if keysA[index] != keysB[index] {
			return keysA[index] < keysB[index]
		}
	}
	return naturalSuggestionLess(a.rel, b.rel)
}

// folderEntrySortKeys：没有章节号（本层散放）记 -1 排在章节前；没解析出剧集的季/集记 0。
func folderEntrySortKeys(entry folderSuggestionEntry) [4]int {
	chapter := -1
	if entry.chapter != nil {
		chapter = *entry.chapter
	}
	season, episode := 0, 0
	if entry.video.Parsed.OK {
		season, episode = intValueOrZero(entry.video.Parsed.Season), intValueOrZero(entry.video.Parsed.Episode)
	}
	return [4]int{entry.seasonMark, chapter, season, episode}
}

// sameFolderSuggestionEpisode：只有文件名解析出同一系列的同一集才算同集多版本；
// 只靠开头序号认出的集号不共用位置（`1.1 xx` 与 `1.2 xx` 不是同一集）。
func sameFolderSuggestionEpisode(a, b folderSuggestionEntry) bool {
	if !a.video.Parsed.OK || !b.video.Parsed.OK || a.video.Parsed.NormalizedSeries != b.video.Parsed.NormalizedSeries {
		return false
	}
	return folderEntrySortKeys(a) == folderEntrySortKeys(b)
}

func folderEntrySeason(entry folderSuggestionEntry) *int {
	switch {
	case entry.chapter != nil:
		return intPtrValue(*entry.chapter)
	case entry.video.Parsed.OK && entry.video.Parsed.Season != nil:
		return intPtrValue(*entry.video.Parsed.Season)
	case entry.seasonMark > 0:
		return intPtrValue(entry.seasonMark)
	}
	return nil
}

func folderEntryEpisode(entry folderSuggestionEntry) *int {
	if entry.video.Parsed.OK && entry.video.Parsed.Episode != nil {
		return intPtrValue(*entry.video.Parsed.Episode)
	}
	if number, ok := leadingSuggestionNumber(entry.video.Name); ok {
		return intPtrValue(number)
	}
	return nil
}

func intPtrValue(value int) *int { return &value }
