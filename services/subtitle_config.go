package services

import "strings"

func normalizeSubtitleWhisperXModel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tiny", "base", "small", "medium", "large-v2", "large-v3":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return defaultSubtitleWhisperXModel
	}
}

func normalizeSubtitleWhisperXBatchSize(value int) int {
	if value <= 0 {
		return defaultSubtitleWhisperXBatchSize
	}
	if value > maxSubtitleWhisperXBatchSize {
		return maxSubtitleWhisperXBatchSize
	}
	return value
}

func normalizeSubtitleWhisperXComputeType(value string) string {
	// The bundled runtime runs on CPU. Keep compute type internal and stable.
	return defaultSubtitleWhisperXComputeType
}
