package services

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type SubtitleTranslationProvider string

const SubtitleTranslationProviderApple SubtitleTranslationProvider = "apple"

// Historical database settings are never read by the system translator.
type SubtitleTranslationConfig struct {
	Provider    string
	DeepLAPIKey string
	BaseURL     string
	APIKey      string
	Model       string
}
type SubtitleTranslator interface {
	Translate(context.Context, []string, string, string) ([]string, error)
}

const subtitleTranslationRequestTimeout = 5 * time.Minute

type appleSubtitleTranslator struct{ run appleAIRunner }

func (s *SubtitleService) subtitleTranslator(_ SubtitleTranslationProvider, config SubtitleTranslationConfig) (SubtitleTranslator, error) {
	if s.translatorFactory != nil {
		return s.translatorFactory(config)
	}
	return &appleSubtitleTranslator{run: s.callAppleAI}, nil
}
func (t *appleSubtitleTranslator) Translate(ctx context.Context, texts []string, source, target string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return []string{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, subtitleTranslationRequestTimeout)
	defer cancel()
	response, err := t.run(ctx, appleAIRequest{Operation: "translate", Texts: texts, Source: source, Target: target})
	if err != nil {
		return nil, err
	}
	if len(response.Translations) != len(texts) {
		return nil, fmt.Errorf("字幕翻译返回 %d 条，期望 %d 条", len(response.Translations), len(texts))
	}
	return response.Translations, nil
}
func normalizeSubtitleTranslationProvider(string) SubtitleTranslationProvider {
	return SubtitleTranslationProviderApple
}
func subtitleTranslationProgressMessage(SubtitleTranslationProvider) string {
	return "通过苹果系统翻译字幕..."
}
func normalizeSubtitleLanguageCode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "chinese":
		return "zh"
	case "english":
		return "en"
	case "japanese":
		return "ja"
	case "korean":
		return "ko"
	case "french":
		return "fr"
	case "german":
		return "de"
	case "spanish":
		return "es"
	case "portuguese":
		return "pt"
	case "russian":
		return "ru"
	case "italian":
		return "it"
	default:
		return strings.TrimSpace(strings.ToLower(value))
	}
}
