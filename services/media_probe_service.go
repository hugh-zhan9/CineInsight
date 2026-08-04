package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	mediaProbeMaxOutputBytes = 16 << 20
	mediaProbeMaxErrorBytes  = 4 << 10
)

var (
	ErrMediaProbeSourceChanged  = errors.New("source_changed_during_probe")
	ErrMediaProbeOutputTooLarge = errors.New("ffprobe_output_too_large")
)

type mediaProbeRunner func(ctx context.Context, path string) (stdout []byte, stderr string, err error)

// MediaProbeService owns local ffprobe execution and the last successful technical snapshot.
type MediaProbeService struct {
	runner  mediaProbeRunner
	now     func() time.Time
	gatesMu sync.Mutex
	gates   map[uint]*mediaProbeGate
}

type mediaProbeGate struct {
	token chan struct{}
	refs  int
}

// NewMediaProbeService creates a probe service that invokes the local ffprobe binary.
func NewMediaProbeService() *MediaProbeService {
	return newMediaProbeServiceWithRunner(runLocalFFProbe)
}

func newMediaProbeServiceWithRunner(runner mediaProbeRunner) *MediaProbeService {
	return &MediaProbeService{runner: runner, now: time.Now, gates: make(map[uint]*mediaProbeGate)}
}

func (s *MediaProbeService) acquireVideo(ctx context.Context, videoID uint) (func(), error) {
	s.gatesMu.Lock()
	if s.gates == nil {
		s.gates = make(map[uint]*mediaProbeGate)
	}
	gate := s.gates[videoID]
	if gate == nil {
		gate = &mediaProbeGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		s.gates[videoID] = gate
	}
	gate.refs++
	s.gatesMu.Unlock()

	acquired := func() (func(), error) {
		return func() {
			gate.token <- struct{}{}
			s.releaseVideoGateReference(videoID, gate)
		}, nil
	}
	select {
	case <-gate.token:
		return acquired()
	default:
	}
	select {
	case <-gate.token:
		return acquired()
	case <-ctx.Done():
		// If ownership became available together with cancellation, preserve the
		// established attempt-recording semantics instead of choosing randomly.
		select {
		case <-gate.token:
			return acquired()
		default:
			s.releaseVideoGateReference(videoID, gate)
			return nil, ctx.Err()
		}
	}
}

func (s *MediaProbeService) releaseVideoGateReference(videoID uint, gate *mediaProbeGate) {
	s.gatesMu.Lock()
	defer s.gatesMu.Unlock()
	gate.refs--
	if gate.refs == 0 && s.gates[videoID] == gate {
		delete(s.gates, videoID)
	}
}

type boundedCapture struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (w *boundedCapture) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = w.buf.Write(p[:remaining])
	}
	if len(p) > remaining {
		w.overflow = true
	}
	return len(p), nil
}

func runLocalFFProbe(ctx context.Context, path string) ([]byte, string, error) {
	ffprobePath, err := findFFProbeBinary()
	if err != nil {
		return nil, "", err
	}
	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-show_format",
		"-show_streams",
		"-print_format", "json",
		path,
	)
	stdout := &boundedCapture{limit: mediaProbeMaxOutputBytes}
	stderr := &boundedCapture{limit: mediaProbeMaxErrorBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, stderr.buf.String(), ctxErr
	}
	if stdout.overflow {
		return nil, stderr.buf.String(), ErrMediaProbeOutputTooLarge
	}
	if err != nil {
		return nil, stderr.buf.String(), fmt.Errorf("run ffprobe: %w", err)
	}
	return stdout.buf.Bytes(), stderr.buf.String(), nil
}

func findFFProbeBinary() (string, error) {
	if path, err := exec.LookPath("ffprobe"); err == nil {
		return path, nil
	}
	for _, path := range []string{"/opt/homebrew/bin/ffprobe", "/usr/local/bin/ffprobe"} {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", errors.New("ffprobe not found")
}

type mediaProbePayload struct {
	Streams []mediaProbeStream `json:"streams"`
	Format  mediaProbeFormat   `json:"format"`
}

type mediaProbeFormat struct {
	FormatName     string `json:"format_name"`
	FormatLongName string `json:"format_long_name"`
	Duration       string `json:"duration"`
	BitRate        string `json:"bit_rate"`
}

type mediaProbeStream struct {
	Index            int                   `json:"index"`
	CodecName        string                `json:"codec_name"`
	CodecLongName    string                `json:"codec_long_name"`
	Profile          string                `json:"profile"`
	CodecType        string                `json:"codec_type"`
	BitRate          string                `json:"bit_rate"`
	Width            *int                  `json:"width"`
	Height           *int                  `json:"height"`
	AvgFrameRate     string                `json:"avg_frame_rate"`
	RealFrameRate    string                `json:"r_frame_rate"`
	PixelFormat      string                `json:"pix_fmt"`
	BitsPerRawSample string                `json:"bits_per_raw_sample"`
	ColorRange       string                `json:"color_range"`
	ColorSpace       string                `json:"color_space"`
	ColorTransfer    string                `json:"color_transfer"`
	ColorPrimaries   string                `json:"color_primaries"`
	SampleRate       string                `json:"sample_rate"`
	Channels         *int                  `json:"channels"`
	ChannelLayout    string                `json:"channel_layout"`
	Tags             mediaProbeStreamTags  `json:"tags"`
	Disposition      mediaProbeDisposition `json:"disposition"`
}

type mediaProbeStreamTags struct {
	Language string `json:"language"`
	Title    string `json:"title"`
}

type mediaProbeDisposition struct {
	Default     int `json:"default"`
	AttachedPic int `json:"attached_pic"`
}

type parsedMediaProbe struct {
	FormatName     string
	FormatLongName string
	Duration       *float64
	TotalBitRate   *int64
	Streams        []models.MediaStream
	PrimaryVideo   *models.MediaStream
}

func parseMediaProbeOutput(output []byte) (parsedMediaProbe, error) {
	if len(output) == 0 {
		return parsedMediaProbe{}, errors.New("empty ffprobe output")
	}
	if len(output) > mediaProbeMaxOutputBytes {
		return parsedMediaProbe{}, ErrMediaProbeOutputTooLarge
	}
	var payload mediaProbePayload
	if err := json.Unmarshal(output, &payload); err != nil {
		return parsedMediaProbe{}, fmt.Errorf("decode ffprobe output: %w", err)
	}
	result := parsedMediaProbe{
		FormatName:     payload.Format.FormatName,
		FormatLongName: payload.Format.FormatLongName,
		Duration:       parseOptionalFloat(payload.Format.Duration),
		TotalBitRate:   parseOptionalInt64(payload.Format.BitRate),
		Streams:        make([]models.MediaStream, 0, len(payload.Streams)),
	}
	for _, source := range payload.Streams {
		if source.CodecType != "video" && source.CodecType != "audio" && source.CodecType != "subtitle" {
			continue
		}
		stream := models.MediaStream{
			StreamIndex:      source.Index,
			StreamType:       source.CodecType,
			CodecName:        source.CodecName,
			CodecLongName:    source.CodecLongName,
			Profile:          source.Profile,
			BitRate:          parseOptionalInt64(source.BitRate),
			Language:         source.Tags.Language,
			Title:            source.Tags.Title,
			IsDefault:        source.Disposition.Default == 1,
			Width:            positiveInt(source.Width),
			Height:           positiveInt(source.Height),
			AvgFrameRate:     source.AvgFrameRate,
			RealFrameRate:    source.RealFrameRate,
			PixelFormat:      source.PixelFormat,
			BitsPerRawSample: parseOptionalInt(source.BitsPerRawSample),
			ColorRange:       source.ColorRange,
			ColorSpace:       source.ColorSpace,
			ColorTransfer:    source.ColorTransfer,
			ColorPrimaries:   source.ColorPrimaries,
			IsHDR:            deriveHDR(source.ColorTransfer, source.ColorPrimaries),
			IsAttachedPic:    source.Disposition.AttachedPic == 1,
			SampleRate:       parseOptionalInt64(source.SampleRate),
			Channels:         positiveInt(source.Channels),
			ChannelLayout:    source.ChannelLayout,
		}
		result.Streams = append(result.Streams, stream)
		if result.PrimaryVideo == nil && stream.StreamType == "video" && !stream.IsAttachedPic {
			primary := stream
			result.PrimaryVideo = &primary
		}
	}
	return result, nil
}

func parseOptionalInt64(value string) *int64 {
	if value == "" || value == "N/A" {
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return nil
	}
	return &parsed
}

func parseOptionalInt(value string) *int {
	parsed := parseOptionalInt64(value)
	if parsed == nil || int64(int(*parsed)) != *parsed {
		return nil
	}
	result := int(*parsed)
	return &result
}

func parseOptionalFloat(value string) *float64 {
	if value == "" || value == "N/A" {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 {
		return nil
	}
	return &parsed
}

func positiveInt(value *int) *int {
	if value == nil || *value <= 0 {
		return nil
	}
	result := *value
	return &result
}

func deriveHDR(transfer, primaries string) *bool {
	transfer = strings.ToLower(strings.TrimSpace(transfer))
	primaries = strings.ToLower(strings.TrimSpace(primaries))
	switch transfer {
	case "smpte2084", "arib-std-b67":
		value := true
		return &value
	case "bt709", "gamma22", "gamma28", "smpte170m", "smpte240m", "linear", "log", "log_sqrt", "iec61966-2-4", "bt1361e", "iec61966-2-1":
		value := false
		return &value
	}
	// Wide-gamut primaries without an explicit transfer function do not prove HDR.
	_ = primaries
	return nil
}

type mediaProbeFingerprint struct {
	size      int64
	modTimeNS int64
}

func mediaProbeStat(path string) (mediaProbeFingerprint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return mediaProbeFingerprint{}, fmt.Errorf("stat source video: %w", err)
	}
	if !info.Mode().IsRegular() {
		return mediaProbeFingerprint{}, errors.New("source video is not a regular file")
	}
	return mediaProbeFingerprint{size: info.Size(), modTimeNS: info.ModTime().UnixNano()}, nil
}

func (f mediaProbeFingerprint) matches(other mediaProbeFingerprint) bool {
	return f.size == other.size && f.modTimeNS == other.modTimeNS
}

// Refresh probes one active video and atomically replaces its successful technical snapshot.
func (s *MediaProbeService) Refresh(ctx context.Context, videoID uint) error {
	if ctx == nil {
		return errors.New("media probe context is nil")
	}
	if s == nil || s.runner == nil {
		return errors.New("media probe service is not initialized")
	}
	release, err := s.acquireVideo(ctx, videoID)
	if err != nil {
		return err
	}
	defer release()
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return fmt.Errorf("load video %d: %w", videoID, err)
	}
	attemptedAt := s.now()
	before, err := mediaProbeStat(video.Path)
	if err != nil {
		return s.recordFailureOrJoin(video.ID, nil, attemptedAt, err)
	}

	output, stderr, err := s.runner(ctx, video.Path)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return s.recordFailureOrJoin(video.ID, &before, attemptedAt, ctxErr)
		}
		probeErr := fmt.Errorf("ffprobe failed: %w", err)
		if message := strings.TrimSpace(stderr); message != "" {
			probeErr = fmt.Errorf("%w: %s", probeErr, truncateMediaProbeError(message))
		}
		return s.recordFailureOrJoin(video.ID, &before, attemptedAt, probeErr)
	}
	parsed, err := parseMediaProbeOutput(output)
	if err != nil {
		return s.recordFailureOrJoin(video.ID, &before, attemptedAt, err)
	}
	after, err := mediaProbeStat(video.Path)
	if err != nil {
		return s.recordFailureOrJoin(video.ID, &before, attemptedAt, err)
	}
	if !before.matches(after) {
		return s.recordFailureOrJoin(video.ID, &after, attemptedAt, ErrMediaProbeSourceChanged)
	}

	if err := s.persistSuccess(video, before, attemptedAt, parsed); err != nil {
		resultErr := fmt.Errorf("persist media probe snapshot: %w", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return resultErr
		}
		return s.recordFailureOrJoin(video.ID, &before, attemptedAt, resultErr)
	}
	return nil
}

func (s *MediaProbeService) persistSuccess(video models.Video, fingerprint mediaProbeFingerprint, attemptedAt time.Time, parsed parsedMediaProbe) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var current models.Video
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, video.ID).Error; err != nil {
			return err
		}
		if current.Path != video.Path {
			return ErrMediaProbeSourceChanged
		}
		metadata := models.VideoTechnicalMetadata{
			VideoID:                    video.ID,
			FormatName:                 parsed.FormatName,
			FormatLongName:             parsed.FormatLongName,
			TotalBitRate:               parsed.TotalBitRate,
			SuccessfulSourceSize:       &fingerprint.size,
			SuccessfulSourceModTimeNS:  &fingerprint.modTimeNS,
			ProbedAt:                   &attemptedAt,
			LastAttemptSourceSize:      &fingerprint.size,
			LastAttemptSourceModTimeNS: &fingerprint.modTimeNS,
			LastAttemptAt:              &attemptedAt,
			LastError:                  "",
		}
		updates := []string{
			"format_name", "format_long_name", "total_bit_rate",
			"successful_source_size", "successful_source_mod_time_ns", "probed_at",
			"last_attempt_source_size", "last_attempt_source_mod_time_ns", "last_attempt_at", "last_error", "updated_at",
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "video_id"}},
			DoUpdates: clause.AssignmentColumns(updates),
		}).Create(&metadata).Error; err != nil {
			return err
		}
		if err := tx.Where("video_id = ?", video.ID).Delete(&models.MediaStream{}).Error; err != nil {
			return err
		}
		for index := range parsed.Streams {
			parsed.Streams[index].VideoID = video.ID
		}
		if len(parsed.Streams) > 0 {
			if err := tx.Create(&parsed.Streams).Error; err != nil {
				return err
			}
		}
		videoUpdates := map[string]any{
			"size": fingerprint.size, "duration": 0, "resolution": "", "width": 0, "height": 0,
		}
		if parsed.Duration != nil {
			videoUpdates["duration"] = *parsed.Duration
		}
		if primary := parsed.PrimaryVideo; primary != nil && primary.Width != nil && primary.Height != nil {
			videoUpdates["width"] = *primary.Width
			videoUpdates["height"] = *primary.Height
			videoUpdates["resolution"] = fmt.Sprintf("%dx%d", *primary.Width, *primary.Height)
		}
		result := tx.Model(&models.Video{}).Where("id = ?", video.ID).Updates(videoUpdates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (s *MediaProbeService) recordFailureOrJoin(videoID uint, fingerprint *mediaProbeFingerprint, attemptedAt time.Time, probeErr error) error {
	if err := s.recordFailure(videoID, fingerprint, attemptedAt, probeErr); err != nil {
		return errors.Join(probeErr, fmt.Errorf("persist media probe failure state: %w", err))
	}
	return probeErr
}

func (s *MediaProbeService) recordFailure(videoID uint, fingerprint *mediaProbeFingerprint, attemptedAt time.Time, probeErr error) error {
	metadata := models.VideoTechnicalMetadata{
		VideoID:       videoID,
		LastAttemptAt: &attemptedAt,
		LastError:     mediaProbeErrorCode(probeErr),
	}
	if fingerprint != nil {
		metadata.LastAttemptSourceSize = &fingerprint.size
		metadata.LastAttemptSourceModTimeNS = &fingerprint.modTimeNS
	}
	updates := []string{
		"last_attempt_source_size", "last_attempt_source_mod_time_ns", "last_attempt_at", "last_error", "updated_at",
	}
	return database.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "video_id"}},
		DoUpdates: clause.AssignmentColumns(updates),
	}).Create(&metadata).Error
}

func mediaProbeErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, ErrMediaProbeSourceChanged):
		return ErrMediaProbeSourceChanged.Error()
	case errors.Is(err, ErrMediaProbeOutputTooLarge):
		return ErrMediaProbeOutputTooLarge.Error()
	default:
		return truncateMediaProbeError(err.Error())
	}
}

func truncateMediaProbeError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= mediaProbeMaxErrorBytes {
		return message
	}
	return message[:mediaProbeMaxErrorBytes]
}
