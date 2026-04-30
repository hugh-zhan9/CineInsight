package services

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"video-master/models"
	"video-master/services/subtitleparser"
)

const aiTaggingPromptSchemaVersion = "ai-tagging-v1"

type AITaggingEvidence struct {
	FileName             string            `json:"file_name"`
	Path                 string            `json:"path"`
	Directory            string            `json:"directory"`
	SubtitleText         string            `json:"subtitle_text,omitempty"`
	SubtitlePath         string            `json:"subtitle_path,omitempty"`
	SubtitleModTime      int64             `json:"subtitle_mod_time,omitempty"`
	SubtitleSize         int64             `json:"subtitle_size,omitempty"`
	Frames               []AITaggingFrame  `json:"frames,omitempty"`
	FrameSamplingConfig  string            `json:"frame_sampling_config"`
	PromptSchemaVersion  string            `json:"prompt_schema_version"`
	Warnings             []string          `json:"warnings,omitempty"`
	AdditionalProperties map[string]string `json:"additional_properties,omitempty"`
}

type AITaggingFrame struct {
	MimeType string `json:"mime_type"`
	DataURL  string `json:"data_url"`
}

type AITaggingExtractor struct{}

func NewAITaggingExtractor() *AITaggingExtractor {
	return &AITaggingExtractor{}
}

func (e *AITaggingExtractor) Collect(ctx context.Context, video models.Video, config AITaggingConfig) AITaggingEvidence {
	evidence := AITaggingEvidence{
		FileName:            video.Name,
		Path:                video.Path,
		Directory:           video.Directory,
		FrameSamplingConfig: fmt.Sprintf("count=%d", config.FrameCount),
		PromptSchemaVersion: aiTaggingPromptSchemaVersion,
	}
	e.collectSubtitle(video, config, &evidence)
	e.collectFrames(ctx, video, config, &evidence)
	return evidence
}

func (e *AITaggingExtractor) collectSubtitle(video models.Video, config AITaggingConfig, evidence *AITaggingEvidence) {
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	info, err := os.Stat(srtPath)
	if err != nil {
		if !os.IsNotExist(err) {
			evidence.Warnings = append(evidence.Warnings, fmt.Sprintf("subtitle stat failed: %v", err))
		}
		return
	}
	segments, err := subtitleparser.ParseFile(srtPath)
	if err != nil {
		evidence.Warnings = append(evidence.Warnings, fmt.Sprintf("subtitle parse failed: %v", err))
		return
	}
	var builder strings.Builder
	for _, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(text)
		if config.SubtitleCharLimit > 0 && builder.Len() >= config.SubtitleCharLimit {
			break
		}
	}
	subtitleText := builder.String()
	if config.SubtitleCharLimit > 0 && len([]rune(subtitleText)) > config.SubtitleCharLimit {
		runes := []rune(subtitleText)
		subtitleText = string(runes[:config.SubtitleCharLimit])
	}
	evidence.SubtitleText = subtitleText
	evidence.SubtitlePath = srtPath
	evidence.SubtitleModTime = info.ModTime().Unix()
	evidence.SubtitleSize = info.Size()
}

func (e *AITaggingExtractor) collectFrames(ctx context.Context, video models.Video, config AITaggingConfig, evidence *AITaggingEvidence) {
	if config.FrameCount <= 0 || strings.TrimSpace(video.Path) == "" {
		return
	}
	ffmpegBin := findMediaBinary("ffmpeg")
	if ffmpegBin == "" {
		evidence.Warnings = append(evidence.Warnings, "ffmpeg unavailable for frame sampling")
		return
	}
	if _, err := os.Stat(video.Path); err != nil {
		evidence.Warnings = append(evidence.Warnings, fmt.Sprintf("video file unavailable for frame sampling: %v", err))
		return
	}
	tmpDir, err := os.MkdirTemp("", "cineinsight-ai-frames-*")
	if err != nil {
		evidence.Warnings = append(evidence.Warnings, fmt.Sprintf("frame temp dir failed: %v", err))
		return
	}
	defer os.RemoveAll(tmpDir)

	count := config.FrameCount
	if count > 4 {
		count = 4
	}
	duration := video.Duration
	if duration <= 0 {
		duration = float64(count + 1)
	}
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			evidence.Warnings = append(evidence.Warnings, "frame sampling cancelled")
			return
		default:
		}
		position := duration * float64(i+1) / float64(count+1)
		outPath := filepath.Join(tmpDir, fmt.Sprintf("frame-%d.jpg", i))
		cmd := exec.CommandContext(ctx, ffmpegBin, "-y", "-ss", strconv.FormatFloat(position, 'f', 2, 64), "-i", video.Path, "-frames:v", "1", "-q:v", "4", outPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			evidence.Warnings = append(evidence.Warnings, fmt.Sprintf("frame sample %d failed: %v %s", i+1, err, truncateLogSnippet(string(output), 160)))
			continue
		}
		data, err := os.ReadFile(outPath)
		if err != nil {
			evidence.Warnings = append(evidence.Warnings, fmt.Sprintf("frame read %d failed: %v", i+1, err))
			continue
		}
		evidence.Frames = append(evidence.Frames, AITaggingFrame{
			MimeType: "image/jpeg",
			DataURL:  "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data),
		})
	}
}

func (e AITaggingEvidence) SummaryJSON() string {
	summary := e
	if len(summary.Frames) > 0 {
		summary.Frames = make([]AITaggingFrame, len(e.Frames))
		for i, frame := range e.Frames {
			summary.Frames[i] = AITaggingFrame{MimeType: frame.MimeType, DataURL: fmt.Sprintf("<%d bytes>", len(frame.DataURL))}
		}
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func buildEvidenceFingerprint(video models.Video, tags []models.Tag, evidence AITaggingEvidence) string {
	payload := map[string]interface{}{
		"video_id":              video.ID,
		"path":                  video.Path,
		"name":                  video.Name,
		"tag_library_hash":      tagLibraryHash(tags),
		"subtitle_path":         evidence.SubtitlePath,
		"subtitle_mod_time":     evidence.SubtitleModTime,
		"subtitle_size":         evidence.SubtitleSize,
		"frame_sampling_config": evidence.FrameSamplingConfig,
		"prompt_schema_version": evidence.PromptSchemaVersion,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func tagLibraryHash(tags []models.Tag) string {
	items := make([]string, 0, len(tags))
	for _, tag := range tags {
		items = append(items, fmt.Sprintf("%d:%s:%s", tag.ID, tag.Name, tag.UpdatedAt.UTC().Format(time.RFC3339Nano)))
	}
	sort.Strings(items)
	sum := sha256.Sum256([]byte(strings.Join(items, "\n")))
	return hex.EncodeToString(sum[:])
}

func findMediaBinary(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	if runtime.GOOS == "darwin" {
		for _, path := range []string{filepath.Join("/opt/homebrew/bin", name), filepath.Join("/usr/local/bin", name)} {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	return ""
}
