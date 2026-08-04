package services

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrLocalMetadataNFOInvalid 表示现有 NFO 或待写字段不满足安全边界。
	ErrLocalMetadataNFOInvalid = errors.New("local_metadata_nfo_invalid")
	// ErrLocalMetadataNFOSymlink 表示视频或目标 NFO 是符号链接。
	ErrLocalMetadataNFOSymlink = errors.New("local_metadata_nfo_symlink")
	// ErrLocalMetadataNFOConflict 表示生成期间目标 NFO 被其他进程修改。
	ErrLocalMetadataNFOConflict = errors.New("local_metadata_nfo_conflict")
)

// LocalMetadataNFOExportInput 是应用负责维护的 Kodi movie NFO 字段。
type LocalMetadataNFOExportInput struct {
	DisplayTitle   string   `json:"display_title"`
	PersonalRating *float64 `json:"personal_rating"`
	Tags           []string `json:"tags"`
	People         []string `json:"people"`
	Collection     string   `json:"collection"`
}

// LocalMetadataNFOExportResult 描述一次成功的 NFO 原子发布。
type LocalMetadataNFOExportResult struct {
	NFOPath  string   `json:"nfo_path"`
	Created  bool     `json:"created"`
	Size     int64    `json:"size"`
	Warnings []string `json:"warnings"`
}

type localMetadataXMLPart struct {
	element *localMetadataXMLElement
	text    []byte
	comment []byte
}

type localMetadataXMLElement struct {
	name     xml.Name
	attrs    []xml.Attr
	children []localMetadataXMLPart
}

// localMetadataXMLDocument 保存 movie 根元素以及根元素之外的顶层注释
// （例如 tinyMediaManager 写在 <movie> 之前的 "created by" 注释），
// 合并写回时这些注释原样保留。
type localMetadataXMLDocument struct {
	leading  [][]byte
	root     *localMetadataXMLElement
	trailing [][]byte
}

// ExportLocalMetadataNFO 将应用管理字段写入视频同名 NFO，并保留其他 XML 字段。
func ExportLocalMetadataNFO(ctx context.Context, videoPath string, input LocalMetadataNFOExportInput) (*LocalMetadataNFOExportResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nfoPath, err := localMetadataExportPath(videoPath)
	if err != nil {
		return nil, err
	}
	input, err = validateLocalMetadataNFOExportInput(videoPath, input)
	if err != nil {
		return nil, err
	}

	original, existed, mode, err := readLocalMetadataNFOForExport(nfoPath)
	if err != nil {
		return nil, err
	}
	document := &localMetadataXMLDocument{root: newLocalMetadataMovieElement()}
	if existed {
		if _, err := parseLocalMovieNFO(original); err != nil {
			return nil, fmt.Errorf("%w: existing NFO cannot be parsed: %v", ErrLocalMetadataNFOInvalid, err)
		}
		document, err = parseLocalMetadataXMLTree(original)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrLocalMetadataNFOInvalid, err)
		}
	}
	mergeLocalMetadataManagedFields(document.root, input)
	content, err := marshalLocalMetadataXMLTree(document)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > localMetadataNFOMaxBytes {
		return nil, fmt.Errorf("%w: NFO exceeds %d byte limit", ErrLocalMetadataNFOInvalid, localMetadataNFOMaxBytes)
	}
	if _, err := parseLocalMovieNFO(content); err != nil {
		return nil, fmt.Errorf("%w: generated NFO cannot be read back: %v", ErrLocalMetadataNFOInvalid, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	removeStaleLocalMetadataTempFiles(nfoPath, time.Now())
	temporary, err := os.CreateTemp(filepath.Dir(nfoPath), "."+filepath.Base(nfoPath)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create NFO temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode.Perm()); err != nil {
		temporary.Close()
		return nil, fmt.Errorf("set NFO temporary permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return nil, fmt.Errorf("write NFO temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return nil, fmt.Errorf("sync NFO temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, fmt.Errorf("close NFO temporary file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 冲突检测是尽力而为：这里在 rename 前一刻重读并字节比对，把竞争窗口
	// 压缩到比对与下方 rename 之间的极短间隙；但 rename 语义下无法完全消除
	// 该窗口——若其他进程恰在此间隙写入同名 NFO，其内容会被本次 rename 覆盖。
	current, currentExists, _, err := readLocalMetadataNFOForExport(nfoPath)
	if err != nil {
		return nil, err
	}
	if existed != currentExists || !bytes.Equal(original, current) {
		return nil, ErrLocalMetadataNFOConflict
	}
	if err := replaceSubtitleFileAtomically(temporaryPath, nfoPath); err != nil {
		return nil, fmt.Errorf("replace NFO atomically: %w", err)
	}
	_ = syncSubtitleParentDirectory(filepath.Dir(nfoPath))
	return &LocalMetadataNFOExportResult{NFOPath: nfoPath, Created: !existed, Size: int64(len(content))}, nil
}

func localMetadataExportPath(videoPath string) (string, error) {
	if strings.TrimSpace(videoPath) == "" {
		return "", fmt.Errorf("%w: video path is required", ErrLocalMetadataNFOInvalid)
	}
	cleaned := filepath.Clean(videoPath)
	info, err := os.Lstat(cleaned)
	if err != nil {
		return "", fmt.Errorf("inspect video path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", ErrLocalMetadataNFOSymlink
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: video path is not a regular file", ErrLocalMetadataNFOInvalid)
	}
	extension := filepath.Ext(cleaned)
	if extension == "" || strings.EqualFold(extension, ".nfo") {
		return "", fmt.Errorf("%w: video path must have a non-NFO extension", ErrLocalMetadataNFOInvalid)
	}
	return strings.TrimSuffix(cleaned, extension) + ".nfo", nil
}

func validateLocalMetadataNFOExportInput(videoPath string, input LocalMetadataNFOExportInput) (LocalMetadataNFOExportInput, error) {
	input.DisplayTitle = strings.TrimSpace(input.DisplayTitle)
	if input.DisplayTitle == "" {
		base := filepath.Base(videoPath)
		input.DisplayTitle = strings.TrimSuffix(base, filepath.Ext(base))
	}
	input.Collection = strings.TrimSpace(input.Collection)
	input.Tags = normalizeLocalMetadataExportNames(input.Tags)
	input.People = normalizeLocalMetadataExportNames(input.People)
	if len(input.People) > localMetadataActorMaxCount {
		return LocalMetadataNFOExportInput{}, fmt.Errorf("%w: NFO exceeds %d actor limit", ErrLocalMetadataNFOInvalid, localMetadataActorMaxCount)
	}
	fields := make([]struct{ name, value string }, 0, 2+len(input.Tags)+len(input.People))
	fields = append(fields, struct{ name, value string }{"标题", input.DisplayTitle}, struct{ name, value string }{"作品集", input.Collection})
	for _, tag := range input.Tags {
		fields = append(fields, struct{ name, value string }{"标签", tag})
	}
	for _, person := range input.People {
		fields = append(fields, struct{ name, value string }{"演员", person})
	}
	for _, field := range fields {
		if len(field.value) > localMetadataTextMaxBytes {
			return LocalMetadataNFOExportInput{}, fmt.Errorf("%w: NFO text field exceeds %d byte limit", ErrLocalMetadataNFOInvalid, localMetadataTextMaxBytes)
		}
		if err := validateLocalMetadataXMLText(field.name, field.value); err != nil {
			return LocalMetadataNFOExportInput{}, err
		}
	}
	if input.PersonalRating != nil {
		rating := *input.PersonalRating
		if math.IsNaN(rating) || math.IsInf(rating, 0) || rating < 0 || rating > 10 || math.Abs(rating*2-math.Round(rating*2)) > 1e-9 {
			return LocalMetadataNFOExportInput{}, fmt.Errorf("%w: personal rating must be a half-step from 0 to 10", ErrLocalMetadataNFOInvalid)
		}
	}
	return input, nil
}

// validateLocalMetadataXMLText 拒绝含 XML 1.0 不支持字符（如 0x08 等控制字符）
// 或非法 UTF-8 的应用侧字段：Go 的 XML 编码器会把这类字符静默替换为 U+FFFD，
// 导致写出的值与库内值不一致，因此这里改为提前失败并指明字段。
func validateLocalMetadataXMLText(fieldName, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s不是合法的 UTF-8 文本，无法写入 NFO", ErrLocalMetadataNFOInvalid, fieldName)
	}
	for _, r := range value {
		if !isLocalMetadataXMLChar(r) {
			return fmt.Errorf("%w: %s包含 XML 不支持的字符 U+%04X，无法写入 NFO", ErrLocalMetadataNFOInvalid, fieldName, r)
		}
	}
	return nil
}

// isLocalMetadataXMLChar 判断字符是否属于 XML 1.0 的合法字符范围。
func isLocalMetadataXMLChar(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

// localMetadataTempMaxAge 是崩溃遗留临时文件的最短保留时间，
// 早于该时长的同名临时文件视为垃圾。
const localMetadataTempMaxAge = time.Hour

// removeStaleLocalMetadataTempFiles 清理早前崩溃在 CreateTemp 与 rename 之间
// 遗留的临时文件。只匹配该 NFO 自己的临时文件前缀，且只删除修改时间早于
// localMetadataTempMaxAge 的文件；清理是尽力而为，失败不阻塞本次写出。
func removeStaleLocalMetadataTempFiles(nfoPath string, now time.Time) {
	directory := filepath.Dir(nfoPath)
	prefix := "." + filepath.Base(nfoPath) + ".tmp-"
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < localMetadataTempMaxAge {
			continue
		}
		_ = os.Remove(filepath.Join(directory, entry.Name()))
	}
}

func normalizeLocalMetadataExportNames(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		normalized := normalizeLocalMetadataName(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, value)
	}
	return result
}

func readLocalMetadataNFOForExport(path string) ([]byte, bool, os.FileMode, error) {
	linkInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, 0644, nil
	}
	if err != nil {
		return nil, false, 0, fmt.Errorf("inspect NFO: %w", err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, false, 0, ErrLocalMetadataNFOSymlink
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, false, 0, fmt.Errorf("%w: NFO target is not a regular file", ErrLocalMetadataNFOInvalid)
	}
	if linkInfo.Size() > localMetadataNFOMaxBytes {
		return nil, false, 0, fmt.Errorf("%w: NFO exceeds %d byte limit", ErrLocalMetadataNFOInvalid, localMetadataNFOMaxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, 0, fmt.Errorf("open NFO: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, false, 0, fmt.Errorf("inspect opened NFO: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(linkInfo, openedInfo) {
		return nil, false, 0, ErrLocalMetadataNFOSymlink
	}
	content, err := io.ReadAll(io.LimitReader(file, localMetadataNFOMaxBytes+1))
	if err != nil {
		return nil, false, 0, fmt.Errorf("read NFO: %w", err)
	}
	if int64(len(content)) > localMetadataNFOMaxBytes {
		return nil, false, 0, fmt.Errorf("%w: NFO exceeds %d byte limit", ErrLocalMetadataNFOInvalid, localMetadataNFOMaxBytes)
	}
	finalInfo, err := os.Lstat(path)
	if err != nil || finalInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(linkInfo, finalInfo) {
		return nil, false, 0, ErrLocalMetadataNFOConflict
	}
	return content, true, linkInfo.Mode(), nil
}

func newLocalMetadataMovieElement() *localMetadataXMLElement {
	return &localMetadataXMLElement{name: xml.Name{Local: "movie"}}
}

func parseLocalMetadataXMLTree(content []byte) (*localMetadataXMLDocument, error) {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	decoder.Strict = true
	document := &localMetadataXMLDocument{}
	stack := make([]*localMetadataXMLElement, 0, 8)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse NFO XML: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			if len(stack)+1 > localMetadataXMLMaxDepth {
				return nil, fmt.Errorf("NFO XML exceeds depth %d", localMetadataXMLMaxDepth)
			}
			element := &localMetadataXMLElement{name: value.Name, attrs: append([]xml.Attr(nil), value.Attr...)}
			if len(stack) == 0 {
				if document.root != nil {
					return nil, errors.New("NFO XML has multiple roots")
				}
				document.root = element
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, localMetadataXMLPart{element: element})
			}
			stack = append(stack, element)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("NFO XML has an unmatched end element")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) != 0 {
				current := stack[len(stack)-1]
				current.children = append(current.children, localMetadataXMLPart{text: append([]byte(nil), value...)})
			} else if strings.TrimSpace(string(value)) != "" {
				return nil, errors.New("NFO XML has text outside the movie root")
			}
		case xml.Comment:
			if len(stack) != 0 {
				current := stack[len(stack)-1]
				current.children = append(current.children, localMetadataXMLPart{comment: append([]byte(nil), value...)})
			} else if document.root == nil {
				document.leading = append(document.leading, append([]byte(nil), value...))
			} else {
				document.trailing = append(document.trailing, append([]byte(nil), value...))
			}
		case xml.Directive:
			return nil, errors.New("NFO XML directives are not allowed")
		case xml.ProcInst:
			if !strings.EqualFold(value.Target, "xml") || document.root != nil {
				return nil, errors.New("NFO XML processing instructions are not allowed")
			}
		}
	}
	if document.root == nil || !strings.EqualFold(document.root.name.Local, "movie") {
		return nil, errors.New("NFO root must be movie")
	}
	return document, nil
}

// mergeLocalMetadataManagedFields 写出 title/userrating/tag/actor/set。
// 注意不对称：导出的 <tag>/<userrating> 仅供第三方消费（Kodi/Jellyfin），
// 应用自身的 NFO 解析器（parseLocalMovieNFO）按既定导入字段设计不读取
// 这两个字段，应用内的往返只覆盖 title/actor/set。
func mergeLocalMetadataManagedFields(root *localMetadataXMLElement, input LocalMetadataNFOExportInput) {
	// A managed field with no app-side value means "the app has nothing to
	// say"; the existing NFO elements for that field are preserved verbatim
	// instead of being deleted.
	writeRating := input.PersonalRating != nil
	writeTags := len(input.Tags) > 0
	writePeople := len(input.People) > 0
	writeSet := input.Collection != ""

	var titleTemplate, ratingTemplate, setTemplate *localMetadataXMLElement
	actorTemplates := make(map[string]*localMetadataXMLElement)
	tagTemplates := make(map[string]*localMetadataXMLElement)
	retained := make([]localMetadataXMLPart, 0, len(root.children))
	for _, part := range root.children {
		if part.element == nil {
			retained = append(retained, part)
			continue
		}
		switch strings.ToLower(part.element.name.Local) {
		case "title":
			if titleTemplate == nil {
				titleTemplate = part.element
			}
		case "userrating":
			if !writeRating {
				retained = append(retained, part)
				break
			}
			if ratingTemplate == nil {
				ratingTemplate = part.element
			}
		case "tag":
			if !writeTags {
				retained = append(retained, part)
				break
			}
			tagTemplates[normalizeLocalMetadataName(localMetadataElementText(part.element))] = part.element
		case "actor":
			if !writePeople {
				retained = append(retained, part)
				break
			}
			actorTemplates[normalizeLocalMetadataName(localMetadataChildText(part.element, "name"))] = part.element
		case "set":
			if !writeSet {
				retained = append(retained, part)
				break
			}
			if setTemplate == nil {
				setTemplate = part.element
			}
		default:
			retained = append(retained, part)
		}
	}
	root.children = retained

	titleTemplate = localMetadataSetElementText(titleTemplate, "title", input.DisplayTitle)
	root.children = append(root.children, localMetadataXMLPart{element: titleTemplate})
	if input.PersonalRating != nil {
		ratingTemplate = localMetadataSetElementText(ratingTemplate, "userrating", formatLocalMetadataRating(*input.PersonalRating))
		setLocalMetadataAttribute(ratingTemplate, "max", "10")
		root.children = append(root.children, localMetadataXMLPart{element: ratingTemplate})
	}
	for _, tag := range input.Tags {
		template := tagTemplates[normalizeLocalMetadataName(tag)]
		root.children = append(root.children, localMetadataXMLPart{element: localMetadataSetElementText(template, "tag", tag)})
	}
	if input.Collection != "" {
		if setTemplate == nil {
			setTemplate = &localMetadataXMLElement{name: xml.Name{Local: "set"}}
		}
		localMetadataSetChildText(setTemplate, "name", input.Collection)
		root.children = append(root.children, localMetadataXMLPart{element: setTemplate})
	}
	for _, person := range input.People {
		actor := actorTemplates[normalizeLocalMetadataName(person)]
		if actor == nil {
			actor = &localMetadataXMLElement{name: xml.Name{Local: "actor"}}
		}
		localMetadataSetChildText(actor, "name", person)
		root.children = append(root.children, localMetadataXMLPart{element: actor})
	}
}

func formatLocalMetadataRating(rating float64) string {
	if rating == math.Trunc(rating) {
		return fmt.Sprintf("%.0f", rating)
	}
	return fmt.Sprintf("%.1f", rating)
}

func localMetadataSetElementText(element *localMetadataXMLElement, name, value string) *localMetadataXMLElement {
	if element == nil {
		element = &localMetadataXMLElement{name: xml.Name{Local: name}}
	}
	element.name.Local = name
	element.children = []localMetadataXMLPart{{text: []byte(value)}}
	return element
}

func localMetadataSetChildText(element *localMetadataXMLElement, name, value string) {
	for _, part := range element.children {
		if part.element != nil && strings.EqualFold(part.element.name.Local, name) {
			localMetadataSetElementText(part.element, name, value)
			return
		}
	}
	element.children = append(element.children, localMetadataXMLPart{element: localMetadataSetElementText(nil, name, value)})
}

func setLocalMetadataAttribute(element *localMetadataXMLElement, name, value string) {
	for index := range element.attrs {
		if strings.EqualFold(element.attrs[index].Name.Local, name) {
			element.attrs[index].Value = value
			return
		}
	}
	element.attrs = append(element.attrs, xml.Attr{Name: xml.Name{Local: name}, Value: value})
}

func localMetadataChildText(element *localMetadataXMLElement, name string) string {
	for _, part := range element.children {
		if part.element != nil && strings.EqualFold(part.element.name.Local, name) {
			return localMetadataElementText(part.element)
		}
	}
	return ""
}

func localMetadataElementText(element *localMetadataXMLElement) string {
	var builder strings.Builder
	for _, part := range element.children {
		if part.element == nil {
			builder.Write(part.text)
		}
	}
	return strings.TrimSpace(builder.String())
}

func marshalLocalMetadataXMLTree(document *localMetadataXMLDocument) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString(xml.Header)
	encoder := xml.NewEncoder(&output)
	for _, comment := range document.leading {
		if err := encoder.EncodeToken(xml.Comment(comment)); err != nil {
			return nil, fmt.Errorf("encode NFO XML: %w", err)
		}
		if err := encoder.Flush(); err != nil {
			return nil, fmt.Errorf("flush NFO XML: %w", err)
		}
		output.WriteString("\n")
	}
	if err := encodeLocalMetadataXMLElement(encoder, document.root); err != nil {
		return nil, fmt.Errorf("encode NFO XML: %w", err)
	}
	if err := encoder.Flush(); err != nil {
		return nil, fmt.Errorf("flush NFO XML: %w", err)
	}
	for _, comment := range document.trailing {
		output.WriteString("\n")
		if err := encoder.EncodeToken(xml.Comment(comment)); err != nil {
			return nil, fmt.Errorf("encode NFO XML: %w", err)
		}
		if err := encoder.Flush(); err != nil {
			return nil, fmt.Errorf("flush NFO XML: %w", err)
		}
	}
	return output.Bytes(), nil
}

func encodeLocalMetadataXMLElement(encoder *xml.Encoder, element *localMetadataXMLElement) error {
	start := xml.StartElement{Name: element.name, Attr: element.attrs}
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	for _, part := range element.children {
		switch {
		case part.element != nil:
			if err := encodeLocalMetadataXMLElement(encoder, part.element); err != nil {
				return err
			}
		case part.comment != nil:
			if err := encoder.EncodeToken(xml.Comment(part.comment)); err != nil {
				return err
			}
		default:
			if err := encoder.EncodeToken(xml.CharData(part.text)); err != nil {
				return err
			}
		}
	}
	return encoder.EncodeToken(start.End())
}
