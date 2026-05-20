package services

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"video-master/models"
)

type LocalHeuristicAITaggingClient struct{}

func NewLocalHeuristicAITaggingClient() AITaggingAIClient {
	return LocalHeuristicAITaggingClient{}
}

func (LocalHeuristicAITaggingClient) AnalyzeTags(ctx context.Context, req AITaggingRequest) ([]AITagSuggestion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	analyzer := localTagAnalyzer{existingTags: req.ExistingTags}
	return analyzer.analyze(req.Video, req.Evidence), nil
}

type localTagAnalyzer struct {
	existingTags []models.Tag
	suggestions  []AITagSuggestion
	seen         map[string]struct{}
}

func (a *localTagAnalyzer) analyze(video models.Video, evidence AITaggingEvidence) []AITagSuggestion {
	a.seen = make(map[string]struct{})
	text := strings.Join([]string{
		video.Name,
		video.Path,
		evidence.SubtitleText,
	}, "\n")
	lowerText := strings.ToLower(text)

	if evidence.DetectedFaceCount > 0 {
		a.add("人物", models.AITagConfidenceHigh, fmt.Sprintf("本地分析：已检测到 %d 张人脸", evidence.DetectedFaceCount))
	}

	if video.Width > 0 && video.Height > 0 {
		if video.Width >= 3840 || video.Height >= 2160 {
			a.add("4K", models.AITagConfidenceHigh, "本地分析：视频分辨率达到 4K 级别")
		} else if video.Width >= 1920 || video.Height >= 1080 {
			a.add("高清", models.AITagConfidenceMedium, "本地分析：视频分辨率达到 1080p 级别")
		}
		if video.Height > video.Width && video.Height >= 720 {
			a.add("竖屏", models.AITagConfidenceHigh, "本地分析：视频高度大于宽度，符合竖屏视频特征")
		}
	}

	rules := []struct {
		label      string
		confidence string
		patterns   []string
	}{
		{label: "家庭", confidence: models.AITagConfidenceMedium, patterns: []string{"family", "home", "亲子", "家庭", "孩子", "宝宝"}},
		{label: "舞台", confidence: models.AITagConfidenceMedium, patterns: []string{"stage", "show", "concert", "live", "演出", "舞台", "演唱会"}},
		{label: "旅行", confidence: models.AITagConfidenceMedium, patterns: []string{"travel", "trip", "旅行", "旅游", "自驾"}},
		{label: "运动", confidence: models.AITagConfidenceMedium, patterns: []string{"sport", "workout", "运动", "健身", "跑步"}},
		{label: "教学", confidence: models.AITagConfidenceMedium, patterns: []string{"tutorial", "lesson", "course", "教程", "教学", "课程"}},
	}
	for _, rule := range rules {
		for _, pattern := range rule.patterns {
			if strings.Contains(lowerText, strings.ToLower(pattern)) {
				a.add(rule.label, rule.confidence, fmt.Sprintf("本地分析：名称、路径或字幕包含 %q", pattern))
				break
			}
		}
	}

	if maybeScreenRecording(video.Name, evidence.Directory) {
		a.add("录屏", models.AITagConfidenceMedium, "本地分析：文件名或路径呈现录屏特征")
	}

	return a.suggestions
}

func (a *localTagAnalyzer) add(label string, confidence string, reasoning string) {
	label = strings.TrimSpace(label)
	if label == "" {
		return
	}
	normalized := normalizeAITagName(label)
	if _, ok := a.seen[normalized]; ok {
		return
	}
	a.seen[normalized] = struct{}{}

	matchedName := ""
	for _, tag := range a.existingTags {
		if normalizeAITagName(tag.Name) == normalized {
			matchedName = tag.Name
			label = tag.Name
			break
		}
	}

	a.suggestions = append(a.suggestions, AITagSuggestion{
		Label:               label,
		Confidence:          confidence,
		MatchType:           localMatchType(matchedName),
		MatchedExistingName: matchedName,
		Reasoning:           reasoning,
	})
}

func localMatchType(matchedName string) string {
	if matchedName != "" {
		return "existing_exact"
	}
	return "new_candidate"
}

var screenRecordingPattern = regexp.MustCompile(`(?i)(screen\s*record|screen[-_ ]?capture|录屏|屏幕录制|recording)`)

func maybeScreenRecording(name string, dir string) bool {
	return screenRecordingPattern.MatchString(name) || screenRecordingPattern.MatchString(filepath.Base(dir))
}
