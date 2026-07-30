package services

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
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

const aiTaggingPromptSchemaVersion = "ai-tagging-v5-agent-evidence"

const (
	aiTaggingFrameMaxWidth       = 512
	aiTaggingFrameQuality        = 8
	aiTaggingFrameMinimumCount   = 10
	aiTaggingFramePolicyVersion  = "one-per-minute-v1"
	aiTaggingFrameEdgeAvoidRatio = 0.05
)

type AITaggingEvidence struct {
	FileName             string            `json:"file_name"`
	Path                 string            `json:"path"`
	Directory            string            `json:"directory"`
	SubtitleText         string            `json:"subtitle_text,omitempty"`
	SubtitleTemporary    bool              `json:"-"`
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
	MimeType string  `json:"mime_type"`
	DataURL  string  `json:"data_url"`
	Index    int     `json:"index"`
	Position float64 `json:"position"`
}

type AITaggingExtractor struct{}

func NewAITaggingExtractor() *AITaggingExtractor {
	return &AITaggingExtractor{}
}

func (e *AITaggingExtractor) Collect(ctx context.Context, video models.Video, config AITaggingConfig) AITaggingEvidence {
	frameCount := planAITaggingFrameCount(video.Duration)
	evidence := AITaggingEvidence{
		FileName:            video.Name,
		Path:                video.Path,
		Directory:           video.Directory,
		FrameSamplingConfig: formatAITaggingFrameSamplingConfig(video.Duration, frameCount),
		PromptSchemaVersion: aiTaggingPromptSchemaVersion,
	}
	e.collectSubtitle(video, config, &evidence)
	e.collectFrames(ctx, video, &evidence)
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

func (e *AITaggingExtractor) collectFrames(ctx context.Context, video models.Video, evidence *AITaggingEvidence) {
	if strings.TrimSpace(video.Path) == "" {
		return
	}
	count := planAITaggingFrameCount(video.Duration)
	if count <= 0 {
		return
	}
	positions := planAITaggingFramePositions(video.Duration, count)
	frames, warnings := sampleAITaggingFrames(ctx, video.Path, positions, 1)
	evidence.Frames = append(evidence.Frames, frames...)
	evidence.Warnings = append(evidence.Warnings, warnings...)
	minAccepted := count / 2
	if minAccepted < 3 {
		minAccepted = 3
	}
	if len(evidence.Frames) > 0 && len(evidence.Frames) < minAccepted {
		evidence.Warnings = append(evidence.Warnings, fmt.Sprintf("frame sampling sparse: got=%d want>=%d", len(evidence.Frames), minAccepted))
	}
}

func (e *AITaggingExtractor) CollectAdditionalFrames(ctx context.Context, video models.Video, existing []AITaggingFrame, count int) ([]AITaggingFrame, []string) {
	if count <= 0 {
		return nil, []string{"additional frame count must be positive"}
	}
	existingPositions := make([]float64, 0, len(existing))
	for _, frame := range existing {
		existingPositions = append(existingPositions, frame.Position)
	}
	positions := planAdditionalAITaggingFramePositions(video.Duration, existingPositions, count)
	return sampleAITaggingFrames(ctx, video.Path, positions, len(existing)+1)
}

func sampleAITaggingFrames(ctx context.Context, videoPath string, positions []float64, startIndex int) ([]AITaggingFrame, []string) {
	if strings.TrimSpace(videoPath) == "" || len(positions) == 0 {
		return nil, nil
	}
	ffmpegBin := findMediaBinary("ffmpeg")
	if ffmpegBin == "" {
		return nil, []string{"ffmpeg unavailable for frame sampling"}
	}
	if _, err := os.Stat(videoPath); err != nil {
		return nil, []string{fmt.Sprintf("video file unavailable for frame sampling: %v", err)}
	}
	tmpDir, err := os.MkdirTemp("", "cineinsight-ai-frames-*")
	if err != nil {
		return nil, []string{fmt.Sprintf("frame temp dir failed: %v", err)}
	}
	defer os.RemoveAll(tmpDir)

	frames := make([]AITaggingFrame, 0, len(positions))
	warnings := make([]string, 0)
	for offset, position := range positions {
		select {
		case <-ctx.Done():
			return frames, append(warnings, "frame sampling cancelled")
		default:
		}
		outPath := filepath.Join(tmpDir, fmt.Sprintf("frame-%d.jpg", offset))
		cmd := exec.CommandContext(ctx, ffmpegBin,
			"-y",
			"-ss", strconv.FormatFloat(position, 'f', 2, 64),
			"-i", videoPath,
			"-frames:v", "1",
			"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", aiTaggingFrameMaxWidth),
			"-q:v", strconv.Itoa(aiTaggingFrameQuality),
			outPath,
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			warnings = append(warnings, fmt.Sprintf("frame sample %d failed: %v %s", offset+1, err, truncateLogSnippet(string(output), 160)))
			continue
		}
		data, err := os.ReadFile(outPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("frame read %d failed: %v", offset+1, err))
			continue
		}
		frames = append(frames, AITaggingFrame{
			MimeType: "image/jpeg",
			DataURL:  "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data),
			Index:    startIndex + len(frames),
			Position: position,
		})
	}
	return frames, warnings
}

func formatAITaggingFrameSamplingConfig(duration float64, resolvedCount int) string {
	return fmt.Sprintf("policy=%s,duration=%.2f,count=%d,max_width=%d,quality=%d,edge_avoid=%.2f",
		aiTaggingFramePolicyVersion,
		duration,
		resolvedCount,
		aiTaggingFrameMaxWidth,
		aiTaggingFrameQuality,
		aiTaggingFrameEdgeAvoidRatio,
	)
}

func planAITaggingFrameCount(duration float64) int {
	if duration <= 0 {
		return aiTaggingFrameMinimumCount
	}
	count := int(math.Ceil(duration / 60))
	if count < aiTaggingFrameMinimumCount {
		return aiTaggingFrameMinimumCount
	}
	return count
}

func planAITaggingFramePositions(duration float64, count int) []float64 {
	if count <= 0 {
		return nil
	}
	if duration <= 0 {
		duration = float64(count + 1)
	}
	start := duration * aiTaggingFrameEdgeAvoidRatio
	end := duration * (1 - aiTaggingFrameEdgeAvoidRatio)
	if end <= start {
		start = 0
		end = duration
	}
	span := end - start
	positions := make([]float64, 0, count)
	for i := 0; i < count; i++ {
		var position float64
		if count == 1 {
			position = start + span/2
		} else {
			position = start + span*float64(i)/float64(count-1)
		}
		if position < 0 {
			position = 0
		}
		if duration > 0 && position > duration {
			position = duration
		}
		positions = append(positions, position)
	}
	return positions
}

func planAdditionalAITaggingFramePositions(duration float64, existing []float64, count int) []float64 {
	if count <= 0 {
		return nil
	}
	if duration <= 0 {
		duration = float64(len(existing) + count + 1)
	}
	start := duration * aiTaggingFrameEdgeAvoidRatio
	end := duration * (1 - aiTaggingFrameEdgeAvoidRatio)
	if end <= start {
		start, end = 0, duration
	}
	points := make([]float64, 0, len(existing)+count+2)
	points = append(points, start, end)
	for _, position := range existing {
		if position > start && position < end {
			points = append(points, position)
		}
	}
	result := make([]float64, 0, count)
	for len(result) < count {
		sort.Float64s(points)
		bestStart, bestEnd := points[0], points[1]
		for index := 1; index < len(points)-1; index++ {
			if points[index+1]-points[index] > bestEnd-bestStart {
				bestStart, bestEnd = points[index], points[index+1]
			}
		}
		position := bestStart + (bestEnd-bestStart)/2
		if bestEnd-bestStart < 0.001 {
			break
		}
		points = append(points, position)
		result = append(result, position)
	}
	sort.Float64s(result)
	return result
}

func (e AITaggingEvidence) SummaryJSON() string {
	summary := e
	if summary.SubtitleTemporary {
		summary.SubtitleText = ""
		properties := make(map[string]string, len(summary.AdditionalProperties)+1)
		for key, value := range summary.AdditionalProperties {
			properties[key] = value
		}
		properties["temporary_transcript"] = "used_not_persisted"
		summary.AdditionalProperties = properties
	}
	if len(summary.Frames) > 0 {
		summary.Frames = make([]AITaggingFrame, len(e.Frames))
		for i, frame := range e.Frames {
			summary.Frames[i] = AITaggingFrame{MimeType: frame.MimeType, DataURL: fmt.Sprintf("<%d bytes>", len(frame.DataURL)), Index: frame.Index, Position: frame.Position}
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
