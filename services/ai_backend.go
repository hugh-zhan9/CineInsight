package services

import "strings"

// AIBackendMode controls which AI backend is allowed to run.
type AIBackendMode string

const (
	AIBackendModeAPI   AIBackendMode = "api"
	AIBackendModeLocal AIBackendMode = "local"
	AIBackendModeOff   AIBackendMode = "off"
)

const (
	defaultLocalMLModel     = "xlm-roberta-base-ViT-B-32::laion5b_s13b_b90k"
	defaultLocalMLDevice    = "auto"
	legacyBuiltinLocalModel = "builtin-local-ml"
	legacyOpenAILocalModel  = "ViT-B-32::openai"
)

func normalizeAIBackendMode(value string) AIBackendMode {
	switch AIBackendMode(stringsTrimLower(value)) {
	case AIBackendModeLocal:
		return AIBackendModeLocal
	case AIBackendModeOff:
		return AIBackendModeOff
	default:
		return AIBackendModeAPI
	}
}

func stringsTrimLower(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeLocalMLDevice(value string) string {
	switch stringsTrimLower(value) {
	case "cpu", "cuda", "mps":
		return stringsTrimLower(value)
	default:
		return defaultLocalMLDevice
	}
}
