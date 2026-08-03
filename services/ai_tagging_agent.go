package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
)

const aiTagAgentMaxDecisions = 4

func (s *AITaggingService) runAgentEvidenceLoop(
	ctx context.Context,
	video models.Video,
	tags []models.Tag,
	evidence AITaggingEvidence,
	fingerprint string,
	config AITaggingConfig,
	client AITaggingAIClient,
) (AITaggingEvidence, error) {
	attempt, err := s.currentAttempt(video.ID)
	if err != nil {
		return evidence, err
	}
	decisionClient, supportsDecisions := client.(AITaggingDecisionClient)
	observations := make([]AITagAgentObservation, 0, aiTagAgentMaxDecisions)
	extraFramesUsed := 0
	transcriptUsed := false
	sameSourceUsed := false
	maxExtraFrames := normalizeAITaggingMaxExtraFrames(config.MaxExtraFrames)

	for round := 1; round <= aiTagAgentMaxDecisions; round++ {
		startedAt := time.Now()
		decision := AITagAgentDecision{Action: models.AITagAgentActionFinalize}
		if supportsDecisions {
			decision, err = decisionClient.DecideNextAction(ctx, AITagAgentDecisionRequest{
				Video: video, ExistingTags: tags, Evidence: evidence, Observations: observations,
				Round: round, MaxRounds: aiTagAgentMaxDecisions,
				RemainingExtraFrames: maxExtraFrames - extraFramesUsed,
				TranscriptUsed:       transcriptUsed, SameSourceUsed: sameSourceUsed,
			})
			if err != nil {
				return evidence, err
			}
		}

		step := models.AITagAgentStep{
			VideoID: video.ID, EvidenceFingerprint: fingerprint, Attempt: attempt, Round: round,
			Action: decision.Action, RequestedCount: decision.RequestedFrameCount,
			ToolStatus: models.AITagToolStatusNotRun,
		}
		if attribution := aiRunAttributionFromContext(ctx); attribution.RunID != 0 {
			step.RunID = &attribution.RunID
		}
		if !supportsDecisions {
			step.FinishReason = "client_without_agent_support"
			step.DurationMS = time.Since(startedAt).Milliseconds()
			return evidence, s.persistAgentStep(step)
		}
		if decision.Action != models.AITagAgentActionMoreFrames && decision.RequestedFrameCount != 0 {
			step.ToolStatus = models.AITagToolStatusRejected
			step.ObservationCode = "unexpected_frame_count"
			if round == aiTagAgentMaxDecisions {
				step.FinishReason = "decision_budget_exhausted"
			}
			step.DurationMS = time.Since(startedAt).Milliseconds()
			if err := s.persistAgentStep(step); err != nil {
				return evidence, err
			}
			if step.FinishReason != "" {
				return evidence, nil
			}
			observations = append(observations, AITagAgentObservation{
				Tool: decision.Action, Status: models.AITagToolStatusRejected,
				Code: "unexpected_frame_count", RequestedCount: decision.RequestedFrameCount,
			})
			continue
		}
		if decision.Action == models.AITagAgentActionFinalize {
			step.FinishReason = "model_finalize"
			step.DurationMS = time.Since(startedAt).Milliseconds()
			return evidence, s.persistAgentStep(step)
		}
		if round == aiTagAgentMaxDecisions {
			step.ToolStatus = models.AITagToolStatusRejected
			step.ObservationCode = "decision_budget_exhausted"
			step.FinishReason = "decision_budget_exhausted"
			step.DurationMS = time.Since(startedAt).Milliseconds()
			return evidence, s.persistAgentStep(step)
		}

		observation := AITagAgentObservation{Tool: decision.Action, RequestedCount: decision.RequestedFrameCount}
		switch decision.Action {
		case models.AITagAgentActionMoreFrames:
			remaining := maxExtraFrames - extraFramesUsed
			if decision.RequestedFrameCount <= 0 {
				observation.Status = models.AITagToolStatusRejected
				observation.Code = "invalid_frame_count"
			} else if decision.RequestedFrameCount > remaining {
				observation.Status = models.AITagToolStatusRejected
				observation.Code = "extra_frame_budget_exceeded"
			} else {
				frames, warnings := s.extractor.CollectAdditionalFrames(ctx, video, evidence.Frames, decision.RequestedFrameCount)
				evidence.Frames = append(evidence.Frames, frames...)
				evidence.Warnings = append(evidence.Warnings, warnings...)
				extraFramesUsed += len(frames)
				observation.ActualCount = len(frames)
				switch {
				case len(frames) == decision.RequestedFrameCount:
					observation.Status = models.AITagToolStatusSuccess
					observation.Code = "additional_frames_collected"
				case len(frames) > 0:
					observation.Status = models.AITagToolStatusPartial
					observation.Code = "additional_frames_partial"
				default:
					observation.Status = models.AITagToolStatusFailed
					observation.Code = "additional_frames_failed"
				}
			}

		case models.AITagAgentActionTranscript:
			if transcriptUsed {
				observation.Status = models.AITagToolStatusRejected
				observation.Code = "transcript_already_requested"
			} else if strings.TrimSpace(evidence.SubtitleText) != "" {
				transcriptUsed = true
				observation.Status = models.AITagToolStatusRejected
				observation.Code = "subtitle_already_available"
			} else {
				transcriptUsed = true
				if s.transcript == nil {
					observation.Status = models.AITagToolStatusFailed
					observation.Code = "temporary_transcript_unavailable"
				} else {
					transcript, transcriptErr := s.transcript.GenerateTemporaryTranscript(ctx, video, config.SubtitleCharLimit, SubtitleRecognitionConfig{
						WhisperXModel:       config.SubtitleWhisperXModel,
						WhisperXBatchSize:   config.SubtitleWhisperXBatchSize,
						WhisperXComputeType: defaultSubtitleWhisperXComputeType,
					})
					if transcriptErr != nil {
						observation.Status = models.AITagToolStatusFailed
						observation.Code = "temporary_transcript_failed"
					} else {
						evidence.SubtitleText = transcript.Text
						evidence.SubtitleTemporary = true
						if evidence.AdditionalProperties == nil {
							evidence.AdditionalProperties = map[string]string{}
						}
						evidence.AdditionalProperties["temporary_transcript_engine"] = transcript.Engine
						evidence.AdditionalProperties["temporary_transcript_language"] = transcript.DetectedLang
						observation.Status = models.AITagToolStatusSuccess
						observation.Code = "temporary_transcript_collected"
					}
				}
			}

		case models.AITagAgentActionFindSameSource:
			if sameSourceUsed {
				observation.Status = models.AITagToolStatusRejected
				observation.Code = "same_source_already_requested"
			} else {
				sameSourceUsed = true
				if s.sameSource == nil {
					observation.Status = models.AITagToolStatusFailed
					observation.Code = "same_source_unavailable"
				} else {
					sameSourceEvidence, sameSourceErr := s.sameSource.FindSameSource(ctx, video, client)
					if sameSourceErr != nil {
						var fatalErr *AITaggingFatalError
						if errors.As(sameSourceErr, &fatalErr) {
							return evidence, fatalErr
						}
						observation.Status = models.AITagToolStatusFailed
						observation.Code = "same_source_failed"
					} else {
						if evidence.AdditionalProperties == nil {
							evidence.AdditionalProperties = map[string]string{}
						}
						evidence.AdditionalProperties["same_source_evidence"] = sameSourceEvidence.Summary
						observation.ActualCount = len(sameSourceEvidence.Relations)
						observation.Status = models.AITagToolStatusSuccess
						observation.Code = "same_source_checked"
					}
				}
			}

		default:
			return evidence, fmt.Errorf("unsupported AI agent action %q", decision.Action)
		}

		step.ActualCount = observation.ActualCount
		step.ToolStatus = observation.Status
		step.ObservationCode = observation.Code
		step.DurationMS = time.Since(startedAt).Milliseconds()
		if err := s.persistAgentStep(step); err != nil {
			return evidence, err
		}
		observations = append(observations, observation)
	}
	return evidence, nil
}

func (s *AITaggingService) currentAttempt(videoID uint) (int, error) {
	var state models.AITaggingState
	if err := database.DB.Select("attempt_count").Where("video_id = ?", videoID).First(&state).Error; err != nil {
		return 0, err
	}
	if state.AttemptCount <= 0 {
		return 0, fmt.Errorf("AI tagging state has invalid attempt count")
	}
	return state.AttemptCount, nil
}

func (s *AITaggingService) persistAgentStep(step models.AITagAgentStep) error {
	return database.DB.Create(&step).Error
}
