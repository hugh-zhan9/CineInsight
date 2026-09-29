package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"video-master/models"
	"video-master/services/subtitleparser"
)

type SubtitleWorkbenchErrorCode string

const (
	SubtitleWorkbenchErrorValidation    SubtitleWorkbenchErrorCode = "subtitle_validation_failed"
	SubtitleWorkbenchErrorConflict      SubtitleWorkbenchErrorCode = "subtitle_conflict"
	SubtitleWorkbenchErrorReplaceFailed SubtitleWorkbenchErrorCode = "subtitle_replace_failed"
)

type SubtitleSaveStatus string

const (
	SubtitleSaveStatusSaved        SubtitleSaveStatus = "saved"
	SubtitleSaveStatusIndexPending SubtitleSaveStatus = "saved_index_pending"
	SubtitleSaveStatusRejected     SubtitleSaveStatus = "rejected"
)

// SubtitleRetranslateMode 是工作台重译的作用范围（D-PC16）。
type SubtitleRetranslateMode string

const (
	// SubtitleRetranslateModeWholeEntry 把整条文本送去翻译并整条替换（默认，兼容旧请求）。
	SubtitleRetranslateModeWholeEntry SubtitleRetranslateMode = "whole_entry"
	// SubtitleRetranslateModeTranslationLine 只把第一行（原文）送去翻译，只替换第二行（译文）。
	SubtitleRetranslateModeTranslationLine SubtitleRetranslateMode = "translation_line"
)

type SubtitleFingerprint struct {
	Size      int64  `json:"size"`
	ModTimeNS int64  `json:"mod_time_ns"`
	SHA256    string `json:"sha256"`
}

type SubtitleEditDocument struct {
	VideoID     uint                           `json:"video_id"`
	Fingerprint SubtitleFingerprint            `json:"fingerprint"`
	Entries     []subtitleparser.EditorSegment `json:"entries"`
	// Issues 是打开时发现的格式问题（零时长、结束早于开始、重叠）。它们不阻止打开，
	// 但会阻止保存，由工作台的「下一个问题」与「一键修复」处理（D-PC15）。
	Issues []subtitleparser.DocumentIssue `json:"issues"`
	// 以下三个字段只由 App 层在遇到带错误码的失败时填写（G-3）；服务层返回 *SubtitleCodedError。
	ErrorCode        string `json:"error_code,omitempty"`
	Message          string `json:"message,omitempty"`
	DetectedEncoding string `json:"detected_encoding,omitempty"`
	// Candidates 是编码歧义时的候选（含各自前 3 条字幕预览），用户选定后把编码传给 ConvertSubtitleToUTF8。
	Candidates []subtitleparser.EncodingCandidate `json:"candidates,omitempty"`
}

type SubtitleValidationResult struct {
	Valid  bool                                   `json:"valid"`
	Issues []subtitleparser.EditorValidationIssue `json:"issues"`
}

type SubtitleSaveRequest struct {
	VideoID     uint                           `json:"video_id"`
	Fingerprint SubtitleFingerprint            `json:"fingerprint"`
	Entries     []subtitleparser.EditorSegment `json:"entries"`
}

type SubtitleSaveResult struct {
	Status      SubtitleSaveStatus                     `json:"status"`
	Fingerprint *SubtitleFingerprint                   `json:"fingerprint,omitempty"`
	Issues      []subtitleparser.EditorValidationIssue `json:"issues,omitempty"`
	// FirstIssueEntryIndex / FirstIssueClientID 指向第一个问题所在的条目（从 1 起），
	// 让前端直接跳过去；校验被拒绝时才有值（D-PC15）。
	FirstIssueEntryIndex int                        `json:"first_issue_entry_index,omitempty"`
	FirstIssueClientID   string                     `json:"first_issue_client_id,omitempty"`
	ErrorCode            SubtitleWorkbenchErrorCode `json:"error_code,omitempty"`
	Message              string                     `json:"message,omitempty"`
	// BackupID 是被覆盖前的旧字幕备份；新建文件时为空（D-PC13）。
	BackupID string `json:"backup_id,omitempty"`
}

type SubtitleRetranslateEntry struct {
	ClientID string `json:"client_id"`
	Text     string `json:"text"`
}

type SubtitleRetranslateRequest struct {
	VideoID    uint                       `json:"video_id"`
	SourceLang string                     `json:"source_lang"`
	TargetLang string                     `json:"target_lang"`
	Mode       SubtitleRetranslateMode    `json:"mode"`
	Entries    []SubtitleRetranslateEntry `json:"entries"`
}

type SubtitleRetranslateResult struct {
	Entries []SubtitleRetranslateEntry `json:"entries"`
	// Warnings 带空译文回退的告知（沿用 subtitleFallbackWarning 的口径）。
	Warnings []string `json:"warnings,omitempty"`
}

// SubtitleErrorEncodingAmbiguous：GB18030 与 Big5 都能干净解码、调用方又没指定编码时，
// 转换拒绝执行并带回候选（MEDIA-02），由用户看预览选定后再转。
const SubtitleErrorEncodingAmbiguous = "subtitle_encoding_ambiguous"

// SubtitleConvertResult 是编码转换的结果。
type SubtitleConvertResult struct {
	Encoding    string               `json:"encoding"`
	BackupID    string               `json:"backup_id,omitempty"`
	Fingerprint *SubtitleFingerprint `json:"fingerprint,omitempty"`
	Warnings    []string             `json:"warnings,omitempty"`
	// 以下字段只由 App 层在遇到带错误码的失败时填写（G-3），此时文件未被改动；服务层返回 *SubtitleCodedError。
	ErrorCode        string                             `json:"error_code,omitempty"`
	Message          string                             `json:"message,omitempty"`
	DetectedEncoding string                             `json:"detected_encoding,omitempty"`
	Candidates       []subtitleparser.EncodingCandidate `json:"candidates,omitempty"`
}

type SubtitleWorkbenchService struct {
	subtitleService   *SubtitleService
	replaceFile       func(string, string) error
	translatorFactory func(SubtitleTranslationConfig) (SubtitleTranslator, error)
	glossaryResolver  func(videoID uint, targetLanguage string) ([]GlossaryTerm, error)
	// dataDir 是没有 subtitleService 时写入器使用的应用数据目录（备份根）；
	// 有 subtitleService 时以它的 BaseDir 为准。
	dataDir string
}

func NewSubtitleWorkbenchService(subtitleService *SubtitleService) *SubtitleWorkbenchService {
	service := &SubtitleWorkbenchService{
		subtitleService:  subtitleService,
		replaceFile:      replaceSubtitleFileAtomically,
		glossaryResolver: NewTranslationGlossaryService().ResolveForVideo,
	}
	service.translatorFactory = func(config SubtitleTranslationConfig) (SubtitleTranslator, error) {
		if service.subtitleService == nil {
			return nil, fmt.Errorf("字幕服务不可用")
		}
		provider := normalizeSubtitleTranslationProvider(config.Provider)
		return service.subtitleService.subtitleTranslator(provider, config)
	}
	return service
}

func (s *SubtitleWorkbenchService) writer() *SubtitleFileWriter {
	dataDir := s.dataDir
	if s.subtitleService != nil {
		dataDir = s.subtitleService.BaseDir
	}
	writer := NewSubtitleFileWriter(dataDir)
	if s.replaceFile != nil {
		writer.replaceFile = s.replaceFile
	}
	return writer
}

// GetDocument 打开视频的同名 .srt。失败时返回带错误码的 *SubtitleCodedError：
// subtitle_missing（没有字幕，可用 NewBlankDocument 以空文档打开）、
// subtitle_not_sidecar_srt、subtitle_encoding_not_utf8。
func (s *SubtitleWorkbenchService) GetDocument(video models.Video) (*SubtitleEditDocument, error) {
	if video.ID == 0 || strings.TrimSpace(video.Path) == "" {
		return nil, errors.New("视频信息无效")
	}
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	content, fingerprint, _, err := readSubtitleForEditing(srtPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, missingSidecarError(video.ID, video.Path)
		}
		return nil, err
	}
	// 0 字节或只有空白（含 BOM）的旧 .srt 当作空白文档打开：指纹取现有文件，保存时正常备份后替换。
	if len(bytes.TrimSpace(bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF}))) == 0 {
		return &SubtitleEditDocument{
			VideoID: video.ID, Fingerprint: fingerprint, Entries: []subtitleparser.EditorSegment{},
			Issues: []subtitleparser.DocumentIssue{},
		}, nil
	}
	if err := ensureUTF8SubtitleContent(content); err != nil {
		return nil, err
	}
	entries, err := subtitleparser.ParseStrict(content)
	if err != nil {
		return nil, err
	}
	return &SubtitleEditDocument{
		VideoID: video.ID, Fingerprint: fingerprint, Entries: entries,
		Issues: subtitleparser.DetectDocumentIssues(entries),
	}, nil
}

// NewBlankDocument 以空文档打开还没有字幕的视频；保存时经写入器创建文件（D-PC15）。
// 空文档的指纹是零值：保存时文件必须仍然不存在，否则按外部改动处理。
func (s *SubtitleWorkbenchService) NewBlankDocument(video models.Video) (*SubtitleEditDocument, error) {
	if video.ID == 0 || strings.TrimSpace(video.Path) == "" {
		return nil, errors.New("视频信息无效")
	}
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	if _, err := os.Lstat(srtPath); err == nil {
		return nil, errors.New("同名字幕已经存在，请重新打开字幕")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取字幕文件状态失败: %s", subtitleIOReason(err))
	}
	return &SubtitleEditDocument{
		VideoID: video.ID, Entries: []subtitleparser.EditorSegment{},
		Issues: []subtitleparser.DocumentIssue{},
	}, nil
}

func (s *SubtitleWorkbenchService) Validate(entries []subtitleparser.EditorSegment) SubtitleValidationResult {
	issues := subtitleparser.ValidateEditorSegments(entries)
	if len(issues) == 0 {
		_, issues = subtitleparser.SerializeEditorSegments(entries)
	}
	if issues == nil {
		issues = []subtitleparser.EditorValidationIssue{}
	}
	return SubtitleValidationResult{Valid: len(issues) == 0, Issues: issues}
}

func (s *SubtitleWorkbenchService) SaveDocument(video models.Video, request SubtitleSaveRequest) (*SubtitleSaveResult, error) {
	if request.VideoID == 0 || request.VideoID != video.ID {
		return nil, errors.New("保存请求的视频与当前视频不一致")
	}
	serialized, issues := subtitleparser.SerializeEditorSegments(request.Entries)
	if len(issues) != 0 {
		result := &SubtitleSaveResult{
			Status: SubtitleSaveStatusRejected, ErrorCode: SubtitleWorkbenchErrorValidation,
			Message: "字幕校验未通过，请先修正标出的问题", Issues: issues,
		}
		for _, issue := range issues {
			if issue.EntryIndex > 0 {
				result.FirstIssueEntryIndex = issue.EntryIndex
				result.FirstIssueClientID = issue.ClientID
				break
			}
		}
		return result, nil
	}

	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	unlock := lockSubtitleFile(srtPath)
	defer unlock()

	_, currentFingerprint, _, err := readSubtitleForEditing(srtPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// 文件不存在：只有以空文档打开（零指纹）时才允许创建。
		currentFingerprint = SubtitleFingerprint{}
	}
	if currentFingerprint != request.Fingerprint {
		return rejectedSubtitleSave(SubtitleWorkbenchErrorConflict, "字幕在编辑器之外被修改，请重新加载后再保存"), nil
	}

	writeResult, err := s.writer().Replace(context.Background(), video.ID, srtPath, serialized)
	if err != nil {
		return rejectedSubtitleSave(SubtitleWorkbenchErrorReplaceFailed, err.Error()), nil
	}

	// 文件已经换好，此后无论如何都不能报告「未保存」。
	_, savedFingerprint, _, err := readSubtitleForEditing(srtPath)
	if err != nil {
		return &SubtitleSaveResult{
			Status:   SubtitleSaveStatusIndexPending,
			Message:  "字幕已保存，但无法重新读取，索引稍后刷新",
			BackupID: writeResult.BackupID,
		}, nil
	}
	segments := make([]subtitleparser.Segment, 0, len(request.Entries))
	for index, entry := range request.Entries {
		text := strings.ReplaceAll(entry.Text, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		text = strings.Trim(text, "\n")
		segments = append(segments, subtitleparser.Segment{
			Index: index + 1, StartTimeMs: entry.StartTimeMs, EndTimeMs: entry.EndTimeMs,
			Text: text, Lines: strings.Split(text, "\n"),
		})
	}
	if err := replaceSubtitleIndex(video, srtPath, segments); err != nil {
		log.Printf("[Subtitle] workbench index refresh failed video_id=%d err=%v", video.ID, err)
		return &SubtitleSaveResult{
			Status: SubtitleSaveStatusIndexPending, Fingerprint: &savedFingerprint,
			Message: "字幕已保存，但搜索索引刷新失败", BackupID: writeResult.BackupID,
		}, nil
	}
	return &SubtitleSaveResult{Status: SubtitleSaveStatusSaved, Fingerprint: &savedFingerprint, BackupID: writeResult.BackupID}, nil
}

// ConvertToUTF8 把非 UTF-8 字幕转成 UTF-8 并经写入器写回（会先备份，可恢复）。
// fromEncoding 是前端从 subtitle_encoding_not_utf8 里拿到的检测结果，非空时必须与当前一致。
// 检测有歧义（多个候选）时 fromEncoding 必填：为空返回 subtitle_encoding_ambiguous 与候选，
// 不按主推测静默转换——主推测错了，写回的就是一整份乱码（虽有备份，但用户未必察觉）。
func (s *SubtitleWorkbenchService) ConvertToUTF8(video models.Video, fromEncoding string) (*SubtitleConvertResult, error) {
	if video.ID == 0 || strings.TrimSpace(video.Path) == "" {
		return nil, errors.New("视频信息无效")
	}
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	unlock := lockSubtitleFile(srtPath)
	defer unlock()

	content, _, _, err := readSubtitleForEditing(srtPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, missingSidecarError(video.ID, video.Path)
		}
		return nil, err
	}
	detection, err := subtitleparser.DetectSubtitleEncoding(content)
	if err != nil {
		return nil, &SubtitleCodedError{Code: SubtitleErrorEncodingNotUTF8, Message: "字幕文件的编码无法识别，无法自动转换", DetectedEncoding: "unknown"}
	}
	text, detected := detection.Text, detection.Encoding
	if detected == subtitleparser.EncodingUTF8 {
		return nil, errors.New("字幕已经是 UTF-8 编码，无需转换")
	}
	requested := strings.ToLower(strings.TrimSpace(fromEncoding))
	if requested == "" && len(detection.Candidates) > 1 {
		return nil, &SubtitleCodedError{
			Code:             SubtitleErrorEncodingAmbiguous,
			Message:          "无法确定字幕的编码，请先在候选中选择预览正确的一项再转换",
			DetectedEncoding: detected,
			Candidates:       detection.Candidates,
		}
	}
	// 用户在歧义候选里选定的编码必须真的按它解码（I-2）：只允许选主推测或候选里的编码，
	// 其余一律视为与打开时的检测不一致。
	if requested != "" && requested != detected {
		allowed := false
		for _, candidate := range detection.Candidates {
			if candidate.Encoding == requested {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, errors.New("字幕编码与之前检测的结果不一致，请重新打开字幕")
		}
		decoded, decodeErr := subtitleparser.DecodeSubtitleBytesAs(content, requested)
		if decodeErr != nil {
			return nil, errors.New("字幕无法按所选编码解码，请重新选择")
		}
		text, detected = decoded, requested
	}

	writeResult, err := s.writer().Replace(context.Background(), video.ID, srtPath, []byte(text))
	if err != nil {
		return nil, err
	}
	result := &SubtitleConvertResult{Encoding: detected, BackupID: writeResult.BackupID}
	if _, fingerprint, _, readErr := readSubtitleForEditing(srtPath); readErr == nil {
		result.Fingerprint = &fingerprint
	}
	if err := indexSubtitleFileForVideoID(video.ID, srtPath); err != nil {
		log.Printf("[Subtitle] index converted subtitle failed video_id=%d err=%v", video.ID, err)
		result.Warnings = append(result.Warnings, "字幕已转换，但搜索索引刷新失败")
	}
	return result, nil
}

func normalizeSubtitleRetranslateMode(mode SubtitleRetranslateMode) (SubtitleRetranslateMode, error) {
	switch SubtitleRetranslateMode(strings.TrimSpace(string(mode))) {
	case "", SubtitleRetranslateModeWholeEntry:
		return SubtitleRetranslateModeWholeEntry, nil
	case SubtitleRetranslateModeTranslationLine:
		return SubtitleRetranslateModeTranslationLine, nil
	default:
		return "", fmt.Errorf("不支持的重译范围: %q", string(mode))
	}
}

// translationLineSource 取一条字幕的第一行（原文）。
func translationLineSource(text string) string {
	normalized := strings.Trim(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	return strings.TrimSpace(strings.SplitN(normalized, "\n", 2)[0])
}

// replaceTranslationLine 只替换第二行；单行条目把译文补成第二行，第三行及以后原样保留。
func replaceTranslationLine(text, translation string) string {
	normalized := strings.Trim(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) < 2 {
		return lines[0] + "\n" + translation
	}
	lines[1] = translation
	return strings.Join(lines, "\n")
}

func (s *SubtitleWorkbenchService) Retranslate(ctx context.Context, request SubtitleRetranslateRequest, config SubtitleTranslationConfig) (*SubtitleRetranslateResult, error) {
	if len(request.Entries) == 0 {
		return nil, errors.New("请先选择要重译的字幕")
	}
	if strings.TrimSpace(request.TargetLang) == "" {
		return nil, errors.New("请选择重译的目标语言")
	}
	mode, err := normalizeSubtitleRetranslateMode(request.Mode)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(request.Entries))
	totalTextBytes := 0
	if len(request.Entries) > subtitleparser.MaxEditorSegments {
		return nil, fmt.Errorf("重译选区不能超过 %d 条", subtitleparser.MaxEditorSegments)
	}
	for _, entry := range request.Entries {
		id := strings.TrimSpace(entry.ClientID)
		if id == "" {
			return nil, errors.New("重译条目缺少标识")
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("重译条目标识 %q 重复", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(entry.Text) == "" {
			return nil, fmt.Errorf("重译条目 %q 的文本为空", id)
		}
		totalTextBytes += len([]byte(entry.Text))
		if totalTextBytes > subtitleparser.MaxEditorFileBytes {
			return nil, fmt.Errorf("重译选区的文本超过 %d 字节", subtitleparser.MaxEditorFileBytes)
		}
	}
	translator, err := s.translatorFactory(config)
	if err != nil {
		return nil, err
	}
	if translator == nil {
		return nil, errors.New("字幕翻译器不可用")
	}
	// 术语生效集按视频与目标语言解析一次，整次选区重译共用（D-033、D-PC16）。只有能吃下术语表的
	// 翻译器才去查：DeepL 用不上，也就不该因为一次库读失败而多出一条失败路径（D-034）。
	contextual, injectable := translator.(ContextualTranslator)
	var glossary []GlossaryTerm
	if injectable {
		resolved, err := s.glossaryResolver(request.VideoID, normalizeSubtitleLanguageCode(request.TargetLang))
		if err != nil {
			return nil, fmt.Errorf("读取术语表失败: %w", err)
		}
		glossary = resolved
	}

	sources := make([]string, len(request.Entries))
	for index, entry := range request.Entries {
		if mode == SubtitleRetranslateModeTranslationLine {
			sources[index] = translationLineSource(entry.Text)
		} else {
			sources[index] = entry.Text
		}
	}

	translatedEntries := make([]SubtitleRetranslateEntry, 0, len(request.Entries))
	fallbackCount := 0
	// 滑动窗口：上一批尾部若干条 (原文, 译文) 作为只读上文，首批为空（D-035）。
	var preceding []ContextPair
	const batchSize = 50
	for start := 0; start < len(request.Entries); start += batchSize {
		end := start + batchSize
		if end > len(request.Entries) {
			end = len(request.Entries)
		}
		texts := sources[start:end]
		translations, err := translateSubtitleBatch(ctx, translator, contextual, TranslationRequest{
			Texts:            texts,
			SourceLang:       request.SourceLang,
			TargetLang:       request.TargetLang,
			Glossary:         glossary,
			PrecedingContext: preceding,
		})
		if err != nil {
			return nil, err
		}
		if len(translations) != len(texts) {
			return nil, fmt.Errorf("字幕翻译返回 %d 条，期望 %d 条", len(translations), len(texts))
		}
		// 空译文回退（与生成、文件翻译共用），且必须先于上文窗口，免得「原文 → 空」进入下一批提示词。
		fallbackTexts, batchFallbacks := applyTranslationFallback(texts, translations)
		fallbackCount += batchFallbacks
		preceding = trailingContextPairs(texts, fallbackTexts, subtitleTranslationContextWindow)
		for index := range translations {
			entry := request.Entries[start+index]
			text := fallbackTexts[index]
			if mode == SubtitleRetranslateModeTranslationLine {
				if strings.TrimSpace(translations[index]) == "" {
					// 没有译文：条目保持原样，不把原文当译文叠成两遍。
					text = entry.Text
				} else {
					text = replaceTranslationLine(entry.Text, text)
				}
			}
			translatedEntries = append(translatedEntries, SubtitleRetranslateEntry{ClientID: entry.ClientID, Text: text})
		}
	}
	result := &SubtitleRetranslateResult{Entries: translatedEntries}
	if fallbackCount > 0 {
		result.Warnings = append(result.Warnings, subtitleFallbackWarning(fallbackCount))
	}
	return result, nil
}

func readSubtitleForEditing(path string) ([]byte, SubtitleFingerprint, os.FileMode, error) {
	// Lstat, not Stat: an atomic replace would destroy a symlink and write the edit
	// to the wrong location, leaving the real subtitle file behind with stale content.
	if linkInfo, err := os.Lstat(path); err != nil {
		return nil, SubtitleFingerprint{}, 0, wrapSubtitleReadError(err)
	} else if !linkInfo.Mode().IsRegular() {
		return nil, SubtitleFingerprint{}, 0, errors.New("字幕文件不是普通文件（可能是符号链接），已拒绝编辑")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, SubtitleFingerprint{}, 0, wrapSubtitleReadError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, SubtitleFingerprint{}, 0, wrapSubtitleReadError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, SubtitleFingerprint{}, 0, errors.New("字幕文件不是普通文件，已拒绝编辑")
	}
	if info.Size() > subtitleparser.MaxEditorFileBytes {
		return nil, SubtitleFingerprint{}, 0, fmt.Errorf("字幕文件超过编辑器 %d 字节的上限", subtitleparser.MaxEditorFileBytes)
	}
	content, err := io.ReadAll(io.LimitReader(file, subtitleparser.MaxEditorFileBytes+1))
	if err != nil {
		return nil, SubtitleFingerprint{}, 0, wrapSubtitleReadError(err)
	}
	if len(content) > subtitleparser.MaxEditorFileBytes {
		return nil, SubtitleFingerprint{}, 0, fmt.Errorf("字幕文件超过编辑器 %d 字节的上限", subtitleparser.MaxEditorFileBytes)
	}
	finalInfo, err := file.Stat()
	if err != nil {
		return nil, SubtitleFingerprint{}, 0, wrapSubtitleReadError(err)
	}
	if finalInfo.Size() != info.Size() || finalInfo.ModTime() != info.ModTime() {
		return nil, SubtitleFingerprint{}, 0, errors.New("读取字幕时文件发生了变化，请重试")
	}
	digest := sha256.Sum256(content)
	return content, SubtitleFingerprint{
		Size: info.Size(), ModTimeNS: info.ModTime().UnixNano(), SHA256: fmt.Sprintf("%x", digest),
	}, info.Mode(), nil
}

// subtitleReadError 是不含路径的中文读取错误，Unwrap 保留原因，
// 调用方用 errors.Is(err, os.ErrNotExist) 分流「文件不存在」。
type subtitleReadError struct {
	message string
	cause   error
}

func (e *subtitleReadError) Error() string { return e.message }
func (e *subtitleReadError) Unwrap() error { return e.cause }

func wrapSubtitleReadError(err error) error {
	return &subtitleReadError{message: "读取字幕文件失败: " + subtitleIOReason(err), cause: err}
}

func rejectedSubtitleSave(code SubtitleWorkbenchErrorCode, message string) *SubtitleSaveResult {
	return &SubtitleSaveResult{Status: SubtitleSaveStatusRejected, ErrorCode: code, Message: message}
}
