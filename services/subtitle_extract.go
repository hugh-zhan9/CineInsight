package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"
)

type ExtractableSubtitleTrack struct {
	Index     int    `json:"index"`
	Language  string `json:"language"`
	Title     string `json:"title"`
	Codec     string `json:"codec"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason"`
}
type ExtractableSubtitleTracks struct {
	SourceVersion string                     `json:"source_version"`
	Tracks        []ExtractableSubtitleTrack `json:"tracks"`
}
type SubtitleExtractRequest struct {
	VideoID       uint   `json:"video_id"`
	SourceVersion string `json:"source_version"`
	TrackIndex    int    `json:"track_index"`
}
type SubtitleExtractPreview struct {
	Token             string                         `json:"token"`
	VideoID           uint                           `json:"video_id"`
	SourceVersion     string                         `json:"source_version"`
	TrackIndex        int                            `json:"track_index"`
	TargetFingerprint SubtitleFingerprint            `json:"target_fingerprint"`
	Entries           []subtitleparser.EditorSegment `json:"entries"`
	ExpiresAt         time.Time                      `json:"expires_at" ts_type:"string"`
}
type SubtitleExtractConfirm struct {
	Token             string              `json:"token"`
	VideoID           uint                `json:"video_id"`
	SourceVersion     string              `json:"source_version"`
	TrackIndex        int                 `json:"track_index"`
	TargetFingerprint SubtitleFingerprint `json:"target_fingerprint"`
}
type extractionArtifact struct {
	preview     SubtitleExtractPreview
	path        string
	fingerprint SubtitleFingerprint
}
type subtitleExtractionState struct {
	mu      sync.Mutex
	pending map[string]extractionArtifact
	running map[uint]context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
}

func extractionVideo(ctx context.Context, id uint, expected string) (models.Video, string, error) {
	if id == 0 {
		return models.Video{}, "", errors.New("视频信息无效")
	}
	var video models.Video
	var version string
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		query, err := applyScanScope(db.Model(&models.Video{}), true)
		if err != nil {
			return err
		}
		if err = query.Where("videos.is_stale = ?", false).First(&video, id).Error; err != nil {
			return err
		}
		info, err := os.Stat(video.Path)
		if err != nil {
			return fmt.Errorf("视频当前不可读取: %s", subtitleIOReason(err))
		}
		if !info.Mode().IsRegular() {
			return ErrQueueSourceChanged
		}
		version = videoSourceVersion(video.ID, info.Size(), info.ModTime().UnixNano())
		if expected != "" && version != expected {
			return ErrQueueSourceChanged
		}
		return nil
	})
	return video, version, err
}
func subtitleTrackSupport(codec string) (bool, string) {
	switch codec {
	case "subrip", "srt", "mov_text", "ass", "ssa", "webvtt", "text":
		if codec == "ass" || codec == "ssa" {
			return true, "转换为 SRT 会丢失字体、位置和其他样式"
		}
		return true, ""
	default:
		return false, "此字幕格式暂不支持文字提取（图片字幕不支持）"
	}
}
func (s *SubtitleWorkbenchService) ListExtractableSubtitleTracks(ctx context.Context, videoID uint) (*ExtractableSubtitleTracks, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	video, version, err := extractionVideo(ctx, videoID, "")
	if err != nil {
		return nil, err
	}
	data, _, err := runLocalFFProbe(ctx, video.Path)
	if err != nil {
		return nil, fmt.Errorf("读取字幕轨失败: %w", err)
	}
	var payload mediaProbePayload
	if err = json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if _, _, err = extractionVideo(ctx, videoID, version); err != nil {
		return nil, err
	}
	result := &ExtractableSubtitleTracks{SourceVersion: version, Tracks: []ExtractableSubtitleTrack{}}
	for _, stream := range payload.Streams {
		if stream.CodecType != "subtitle" {
			continue
		}
		supported, reason := subtitleTrackSupport(stream.CodecName)
		result.Tracks = append(result.Tracks, ExtractableSubtitleTrack{stream.Index, stream.Tags.Language, stream.Tags.Title, stream.CodecName, supported, reason})
	}
	return result, nil
}
func (s *SubtitleWorkbenchService) PreviewSubtitleExtraction(parent context.Context, request SubtitleExtractRequest) (*SubtitleExtractPreview, error) {
	if request.SourceVersion == "" || request.TrackIndex < 0 {
		return nil, errors.New("字幕提取参数无效")
	}
	state := &s.extraction
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		cancel()
		return nil, database.ErrMaintenance
	}
	if state.running == nil {
		state.running = map[uint]context.CancelFunc{}
	}
	if _, ok := state.running[request.VideoID]; ok {
		state.mu.Unlock()
		cancel()
		return nil, errors.New("该视频正在提取字幕")
	}
	state.running[request.VideoID] = cancel
	state.wg.Add(1)
	state.mu.Unlock()
	defer func() {
		cancel()
		state.mu.Lock()
		delete(state.running, request.VideoID)
		state.mu.Unlock()
		state.wg.Done()
	}()
	video, _, err := extractionVideo(ctx, request.VideoID, request.SourceVersion)
	if err != nil {
		return nil, err
	}
	tracks, err := s.ListExtractableSubtitleTracks(ctx, request.VideoID)
	if err != nil {
		return nil, err
	}
	if tracks.SourceVersion != request.SourceVersion {
		return nil, ErrQueueSourceChanged
	}
	supported := false
	for _, track := range tracks.Tracks {
		if track.Index == request.TrackIndex {
			supported = track.Supported
		}
	}
	if !supported {
		return nil, errors.New("所选字幕轨不支持文字提取")
	}
	target := subtitleparser.SRTPathForVideo(video.Path)
	_, fingerprint, _, err := readSubtitleForEditing(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ffmpeg, err := findThumbnailFFmpeg()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "error", "-i", video.Path, "-map", "0:"+strconv.Itoa(request.TrackIndex), "-c:s", "srt", "-f", "srt", "pipe:1")
	output := &boundedCapture{limit: subtitleparser.MaxEditorFileBytes}
	diagnostic := &boundedCapture{limit: 4096}
	cmd.Stdout = output
	cmd.Stderr = diagnostic
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("文字字幕提取失败，请检查所选轨道")
	}
	if output.overflow {
		return nil, errors.New("字幕提取结果超过大小上限")
	}
	if err = ensureUTF8SubtitleContent(output.buf.Bytes()); err != nil {
		return nil, err
	}
	entries, err := subtitleparser.ParseStrict(output.buf.Bytes())
	if err != nil {
		return nil, err
	}
	if !s.Validate(entries).Valid {
		return nil, errors.New("提取的字幕校验失败")
	}
	if _, _, err = extractionVideo(ctx, request.VideoID, request.SourceVersion); err != nil {
		return nil, err
	}
	token, err := newRandomRerollToken()
	if err != nil {
		return nil, err
	}
	temporary, err := writeSubtitleTemporary(target, output.buf.Bytes(), 0600)
	if err != nil {
		return nil, err
	}
	preview := SubtitleExtractPreview{token, request.VideoID, request.SourceVersion, request.TrackIndex, fingerprint, entries, time.Now().Add(10 * time.Minute)}
	state.mu.Lock()
	defer state.mu.Unlock()
	if ctx.Err() != nil || state.closed {
		os.Remove(temporary)
		return nil, context.Canceled
	}
	if state.pending == nil {
		state.pending = map[string]extractionArtifact{}
	}
	for key, artifact := range state.pending {
		if artifact.preview.VideoID == request.VideoID || time.Now().After(artifact.preview.ExpiresAt) {
			os.Remove(artifact.path)
			delete(state.pending, key)
		}
	}
	if len(state.pending) >= 2 {
		os.Remove(temporary)
		return nil, errors.New("最多保留两份字幕提取预览，请先确认或取消")
	}
	_, artifactFingerprint, _, readErr := readSubtitleForEditing(temporary)
	if readErr != nil {
		os.Remove(temporary)
		return nil, readErr
	}
	state.pending[token] = extractionArtifact{preview: preview, path: temporary, fingerprint: artifactFingerprint}
	return &preview, nil
}
func (s *SubtitleWorkbenchService) ConfirmSubtitleExtraction(ctx context.Context, request SubtitleExtractConfirm) (*SubtitleSaveResult, error) {
	state := &s.extraction
	state.mu.Lock()
	artifact, ok := state.pending[request.Token]
	if state.closed || !ok {
		state.mu.Unlock()
		return nil, errors.New("字幕提取预览已失效")
	}
	p := artifact.preview
	if request.VideoID != p.VideoID || request.SourceVersion != p.SourceVersion || request.TrackIndex != p.TrackIndex || request.TargetFingerprint != p.TargetFingerprint {
		state.mu.Unlock()
		return nil, errors.New("字幕提取预览身份不一致")
	}
	if _, running := state.running[p.VideoID]; running {
		state.mu.Unlock()
		return nil, errors.New("该视频正在处理字幕")
	}
	ctx, cancel := context.WithCancel(ctx)
	if state.running == nil {
		state.running = map[uint]context.CancelFunc{}
	}
	state.running[p.VideoID] = cancel
	delete(state.pending, request.Token)
	state.wg.Add(1)
	state.mu.Unlock()
	defer func() {
		cancel()
		state.mu.Lock()
		delete(state.running, p.VideoID)
		state.mu.Unlock()
		state.wg.Done()
	}()
	defer os.Remove(artifact.path)
	if time.Now().After(p.ExpiresAt) {
		return nil, errors.New("字幕提取预览已过期，请重新提取")
	}
	unlock, err := rLockLibraryPathsContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	video, _, err := extractionVideo(ctx, p.VideoID, p.SourceVersion)
	if err != nil {
		return nil, err
	}
	content, fingerprint, _, err := readSubtitleForEditing(artifact.path)
	if err != nil {
		return nil, err
	}
	if fingerprint != artifact.fingerprint {
		return nil, errors.New("提取预览文件已变化，请重新提取")
	}
	entries, err := subtitleparser.ParseStrict(content)
	if err != nil {
		return nil, err
	}
	// SaveDocument serializes validation, target CAS and atomic backup replacement under the shared path lock.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.saveDocument(ctx, video, SubtitleSaveRequest{VideoID: p.VideoID, Fingerprint: p.TargetFingerprint, Entries: entries}, func() error { _, _, err := extractionVideo(ctx, p.VideoID, p.SourceVersion); return err })
}
func (s *SubtitleWorkbenchService) CancelSubtitleExtraction(videoID uint) {
	state := &s.extraction
	state.mu.Lock()
	defer state.mu.Unlock()
	if cancel := state.running[videoID]; cancel != nil {
		cancel()
	}
	for token, artifact := range state.pending {
		if artifact.preview.VideoID == videoID {
			os.Remove(artifact.path)
			delete(state.pending, token)
		}
	}
}

// QuiesceSubtitleExtraction stops subprocesses and waits before database maintenance or shutdown.
func (s *SubtitleWorkbenchService) QuiesceSubtitleExtraction() {
	state := &s.extraction
	state.mu.Lock()
	state.closed = true
	for _, cancel := range state.running {
		cancel()
	}
	for token, artifact := range state.pending {
		os.Remove(artifact.path)
		delete(state.pending, token)
	}
	state.mu.Unlock()
	state.wg.Wait()
}
func (s *SubtitleWorkbenchService) ResumeSubtitleExtraction() {
	s.extraction.mu.Lock()
	s.extraction.closed = false
	s.extraction.mu.Unlock()
}
