package services

type SubtitleEngine string

const (
	SubtitleEngineWhisperX SubtitleEngine = "whisperx"
	SubtitleEngineQwen     SubtitleEngine = "qwen"
)

const (
	defaultSubtitleWhisperXModel       = "medium"
	defaultSubtitleWhisperXBatchSize   = 8
	defaultSubtitleWhisperXComputeType = "int8"
	maxSubtitleWhisperXBatchSize       = 16
)

type SubtitlePrepareMode string

const (
	SubtitlePrepareModeManaged      SubtitlePrepareMode = "managed"
	SubtitlePrepareModeManualPrereq SubtitlePrepareMode = "manual_prereq"
	SubtitlePrepareModeUnsupported  SubtitlePrepareMode = "unsupported"
	SubtitlePrepareModeNone         SubtitlePrepareMode = "none"
)

type SubtitleStatusReasonCode string

const (
	SubtitleReasonReady               SubtitleStatusReasonCode = "ready"
	SubtitleReasonUnsupportedPlatform SubtitleStatusReasonCode = "unsupported_platform"
	SubtitleReasonMissingFFmpeg       SubtitleStatusReasonCode = "missing_ffmpeg"
	SubtitleReasonMissingRuntime      SubtitleStatusReasonCode = "missing_runtime"
	SubtitleReasonMissingModel        SubtitleStatusReasonCode = "missing_model"
	SubtitleReasonManualPrereq        SubtitleStatusReasonCode = "manual_prereq_required"
)

type SubtitleSourceLangMode string

const (
	SubtitleSourceLangModeShared     SubtitleSourceLangMode = "shared"
	SubtitleSourceLangModeEngineOnly SubtitleSourceLangMode = "engine_only"
	SubtitleSourceLangModeIgnored    SubtitleSourceLangMode = "ignored"
)

type SubtitleEngineStatus struct {
	Engine         SubtitleEngine           `json:"engine"`
	DisplayName    string                   `json:"display_name"`
	Supported      bool                     `json:"supported"`
	Available      bool                     `json:"available"`
	NeedsPrepare   bool                     `json:"needs_prepare"`
	PrepareMode    SubtitlePrepareMode      `json:"prepare_mode"`
	ReasonCode     SubtitleStatusReasonCode `json:"reason_code"`
	SourceLangMode SubtitleSourceLangMode   `json:"source_lang_mode"`
	ReasonMessage  string                   `json:"reason_message"`
	PrepareHint    string                   `json:"prepare_hint"`
}

type SubtitleGenerateRequest struct {
	VideoID    uint           `json:"video_id"`
	VideoName  string         `json:"video_name,omitempty"`
	Engine     SubtitleEngine `json:"engine"`
	SourceLang string         `json:"source_lang"`
}

type SubtitleRecognitionConfig struct {
	WhisperXModel       string
	WhisperXBatchSize   int
	WhisperXComputeType string
}

type SubtitleGenerateOptions struct {
	BilingualEnabled  bool
	BilingualLang     string
	TranslationConfig SubtitleTranslationConfig
	RecognitionConfig SubtitleRecognitionConfig
	ForceGenerate     bool
}

type SubtitleGenerateResultStatus string

const (
	SubtitleResultStatusSuccess          SubtitleGenerateResultStatus = "success"
	SubtitleResultStatusCancelled        SubtitleGenerateResultStatus = "cancelled"
	SubtitleResultStatusValidationFailed SubtitleGenerateResultStatus = "validation_failed"
)

type SubtitleValidationCode string

const (
	SubtitleValidationCodeHallucinationDetected SubtitleValidationCode = "hallucination_detected"
)

type SubtitleGenerateResult struct {
	Status            SubtitleGenerateResultStatus `json:"status"`
	VideoID           uint                         `json:"video_id"`
	Path              string                       `json:"path,omitempty"`
	Message           string                       `json:"message,omitempty"`
	ValidationCode    SubtitleValidationCode       `json:"validation_code,omitempty"`
	ForceEligible     bool                         `json:"force_eligible,omitempty"`
	Engine            SubtitleEngine               `json:"engine,omitempty"`
	SourceLang        string                       `json:"source_lang,omitempty"`
	Warnings          []string                     `json:"warnings,omitempty"`
	TranslationStatus string                       `json:"translation_status,omitempty"`
}

type SubtitleValidationError struct {
	Code          SubtitleValidationCode
	Message       string
	ForceEligible bool
}

func (e *SubtitleValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
