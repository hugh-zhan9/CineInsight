package services

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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

type SubtitleFingerprint struct {
	Size      int64  `json:"size"`
	ModTimeNS int64  `json:"mod_time_ns"`
	SHA256    string `json:"sha256"`
}

type SubtitleEditDocument struct {
	VideoID     uint                           `json:"video_id"`
	Fingerprint SubtitleFingerprint            `json:"fingerprint"`
	Entries     []subtitleparser.EditorSegment `json:"entries"`
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
	ErrorCode   SubtitleWorkbenchErrorCode             `json:"error_code,omitempty"`
	Message     string                                 `json:"message,omitempty"`
}

type SubtitleRetranslateEntry struct {
	ClientID string `json:"client_id"`
	Text     string `json:"text"`
}

type SubtitleRetranslateRequest struct {
	VideoID    uint                       `json:"video_id"`
	SourceLang string                     `json:"source_lang"`
	TargetLang string                     `json:"target_lang"`
	Entries    []SubtitleRetranslateEntry `json:"entries"`
}

type SubtitleRetranslateResult struct {
	Entries []SubtitleRetranslateEntry `json:"entries"`
}

type SubtitleWorkbenchService struct {
	subtitleService   *SubtitleService
	replaceFile       func(string, string) error
	translatorFactory func(SubtitleTranslationConfig) (SubtitleTranslator, error)
}

func NewSubtitleWorkbenchService(subtitleService *SubtitleService) *SubtitleWorkbenchService {
	service := &SubtitleWorkbenchService{
		subtitleService: subtitleService,
		replaceFile:     replaceSubtitleFileAtomically,
	}
	service.translatorFactory = func(config SubtitleTranslationConfig) (SubtitleTranslator, error) {
		if service.subtitleService == nil {
			return nil, fmt.Errorf("subtitle service unavailable")
		}
		provider := normalizeSubtitleTranslationProvider(config.Provider)
		return service.subtitleService.subtitleTranslator(provider, config)
	}
	return service
}

func (s *SubtitleWorkbenchService) GetDocument(video models.Video) (*SubtitleEditDocument, error) {
	if video.ID == 0 || strings.TrimSpace(video.Path) == "" {
		return nil, fmt.Errorf("video is invalid")
	}
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	content, fingerprint, _, err := readSubtitleForEditing(srtPath)
	if err != nil {
		return nil, err
	}
	entries, err := subtitleparser.ParseStrict(content)
	if err != nil {
		return nil, err
	}
	return &SubtitleEditDocument{VideoID: video.ID, Fingerprint: fingerprint, Entries: entries}, nil
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
		return nil, fmt.Errorf("subtitle save video ID does not match")
	}
	serialized, issues := subtitleparser.SerializeEditorSegments(request.Entries)
	if len(issues) != 0 {
		return &SubtitleSaveResult{
			Status: SubtitleSaveStatusRejected, ErrorCode: SubtitleWorkbenchErrorValidation,
			Message: "subtitle validation failed", Issues: issues,
		}, nil
	}

	unlock := lockSubtitleFile(video.ID)
	defer unlock()

	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	_, currentFingerprint, sourceMode, err := readSubtitleForEditing(srtPath)
	if err != nil {
		return nil, err
	}
	if currentFingerprint != request.Fingerprint {
		return rejectedSubtitleSave(SubtitleWorkbenchErrorConflict, "subtitle changed outside the editor; reload before saving"), nil
	}

	temporary, err := os.CreateTemp(filepath.Dir(srtPath), "."+filepath.Base(srtPath)+".tmp-*")
	if err != nil {
		return rejectedSubtitleSave(SubtitleWorkbenchErrorReplaceFailed, fmt.Sprintf("create subtitle temporary file: %s", subtitleIOReason(err))), nil
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(sourceMode.Perm()); err != nil {
		return rejectedSubtitleSave(SubtitleWorkbenchErrorReplaceFailed, fmt.Sprintf("set subtitle permissions: %s", subtitleIOReason(err))), nil
	}
	if _, err := temporary.Write(serialized); err != nil {
		return rejectedSubtitleSave(SubtitleWorkbenchErrorReplaceFailed, fmt.Sprintf("write subtitle temporary file: %s", subtitleIOReason(err))), nil
	}
	if err := temporary.Sync(); err != nil {
		return rejectedSubtitleSave(SubtitleWorkbenchErrorReplaceFailed, fmt.Sprintf("sync subtitle temporary file: %s", subtitleIOReason(err))), nil
	}
	if err := temporary.Close(); err != nil {
		return rejectedSubtitleSave(SubtitleWorkbenchErrorReplaceFailed, fmt.Sprintf("close subtitle temporary file: %s", subtitleIOReason(err))), nil
	}

	_, finalFingerprint, _, err := readSubtitleForEditing(srtPath)
	if err != nil {
		return nil, err
	}
	if finalFingerprint != request.Fingerprint {
		return rejectedSubtitleSave(SubtitleWorkbenchErrorConflict, "subtitle changed outside the editor; reload before saving"), nil
	}
	if err := s.replaceFile(temporaryPath, srtPath); err != nil {
		return rejectedSubtitleSave(SubtitleWorkbenchErrorReplaceFailed, fmt.Sprintf("replace subtitle file: %s", subtitleIOReason(err))), nil
	}
	removeTemporary = false
	_ = syncSubtitleParentDirectory(filepath.Dir(srtPath))

	// The file is already replaced; never report this as "not modified".
	_, savedFingerprint, _, err := readSubtitleForEditing(srtPath)
	if err != nil {
		return &SubtitleSaveResult{
			Status:  SubtitleSaveStatusIndexPending,
			Message: "subtitle saved but could not be re-read; index refresh is pending",
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
		return &SubtitleSaveResult{
			Status: SubtitleSaveStatusIndexPending, Fingerprint: &savedFingerprint,
			Message: fmt.Sprintf("subtitle saved but index refresh failed: %v", err),
		}, nil
	}
	return &SubtitleSaveResult{Status: SubtitleSaveStatusSaved, Fingerprint: &savedFingerprint}, nil
}

func (s *SubtitleWorkbenchService) Retranslate(ctx context.Context, request SubtitleRetranslateRequest, config SubtitleTranslationConfig) (*SubtitleRetranslateResult, error) {
	if len(request.Entries) == 0 {
		return nil, fmt.Errorf("subtitle translation selection is empty")
	}
	if strings.TrimSpace(request.TargetLang) == "" {
		return nil, fmt.Errorf("subtitle translation target language is required")
	}
	seen := make(map[string]struct{}, len(request.Entries))
	totalTextBytes := 0
	if len(request.Entries) > subtitleparser.MaxEditorSegments {
		return nil, fmt.Errorf("subtitle translation selection exceeds %d entries", subtitleparser.MaxEditorSegments)
	}
	for _, entry := range request.Entries {
		id := strings.TrimSpace(entry.ClientID)
		if id == "" {
			return nil, fmt.Errorf("subtitle translation entry client ID is required")
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("subtitle translation entry client ID %q is duplicated", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(entry.Text) == "" {
			return nil, fmt.Errorf("subtitle translation entry %q is empty", id)
		}
		totalTextBytes += len([]byte(entry.Text))
		if totalTextBytes > subtitleparser.MaxEditorFileBytes {
			return nil, fmt.Errorf("subtitle translation selection exceeds %d bytes", subtitleparser.MaxEditorFileBytes)
		}
	}
	translator, err := s.translatorFactory(config)
	if err != nil {
		return nil, err
	}
	if translator == nil {
		return nil, fmt.Errorf("subtitle translator unavailable")
	}
	translatedEntries := make([]SubtitleRetranslateEntry, 0, len(request.Entries))
	const batchSize = 50
	for start := 0; start < len(request.Entries); start += batchSize {
		end := start + batchSize
		if end > len(request.Entries) {
			end = len(request.Entries)
		}
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = request.Entries[index].Text
		}
		translations, err := translator.Translate(ctx, texts, request.SourceLang, request.TargetLang)
		if err != nil {
			return nil, err
		}
		if len(translations) != len(texts) {
			return nil, fmt.Errorf("subtitle translation returned %d entries for %d inputs", len(translations), len(texts))
		}
		for index, translation := range translations {
			translatedEntries = append(translatedEntries, SubtitleRetranslateEntry{
				ClientID: request.Entries[start+index].ClientID,
				Text:     translation,
			})
		}
	}
	return &SubtitleRetranslateResult{Entries: translatedEntries}, nil
}

func readSubtitleForEditing(path string) ([]byte, SubtitleFingerprint, os.FileMode, error) {
	// Lstat, not Stat: an atomic replace would destroy a symlink and write the edit
	// to the wrong location, leaving the real subtitle file behind with stale content.
	if linkInfo, err := os.Lstat(path); err != nil {
		return nil, SubtitleFingerprint{}, 0, err
	} else if !linkInfo.Mode().IsRegular() {
		return nil, SubtitleFingerprint{}, 0, fmt.Errorf("subtitle source is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, SubtitleFingerprint{}, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, SubtitleFingerprint{}, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, SubtitleFingerprint{}, 0, fmt.Errorf("subtitle source is not a regular file")
	}
	if info.Size() > subtitleparser.MaxEditorFileBytes {
		return nil, SubtitleFingerprint{}, 0, fmt.Errorf("subtitle exceeds %d byte editor limit", subtitleparser.MaxEditorFileBytes)
	}
	content, err := io.ReadAll(io.LimitReader(file, subtitleparser.MaxEditorFileBytes+1))
	if err != nil {
		return nil, SubtitleFingerprint{}, 0, err
	}
	if len(content) > subtitleparser.MaxEditorFileBytes {
		return nil, SubtitleFingerprint{}, 0, fmt.Errorf("subtitle exceeds %d byte editor limit", subtitleparser.MaxEditorFileBytes)
	}
	finalInfo, err := file.Stat()
	if err != nil {
		return nil, SubtitleFingerprint{}, 0, err
	}
	if finalInfo.Size() != info.Size() || finalInfo.ModTime() != info.ModTime() {
		return nil, SubtitleFingerprint{}, 0, fmt.Errorf("subtitle changed while it was being read")
	}
	digest := sha256.Sum256(content)
	return content, SubtitleFingerprint{
		Size: info.Size(), ModTimeNS: info.ModTime().UnixNano(), SHA256: fmt.Sprintf("%x", digest),
	}, info.Mode(), nil
}

func rejectedSubtitleSave(code SubtitleWorkbenchErrorCode, message string) *SubtitleSaveResult {
	return &SubtitleSaveResult{Status: SubtitleSaveStatusRejected, ErrorCode: code, Message: message}
}

// subtitleIOReason strips the local path out of filesystem errors so save failures can be
// reported to the WebView and the log without disclosing where the media library lives.
func subtitleIOReason(err error) string {
	if err == nil {
		return ""
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return fmt.Sprintf("%s: %v", pathErr.Op, pathErr.Err)
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return fmt.Sprintf("%s: %v", linkErr.Op, linkErr.Err)
	}
	return err.Error()
}
