package services

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var structurePTSPattern = regexp.MustCompile(`\bn:\s*\d+\s+pts:\s*[-0-9]+\s+pts_time:([-+0-9.eE]+)`)

func extractStructureFrames(ctx context.Context, path string, start, end int64) ([]structureFrame, error) {
	binary, err := findThumbnailFFmpeg()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStructureRuntimeUnavailable, err)
	}
	if start < 0 || end <= start || end-start > structureWindowMS {
		return nil, fmt.Errorf("structure_invalid_window")
	}
	selectFilter := fmt.Sprintf("select='isnan(prev_selected_t)+gte(t-prev_selected_t,0.5)',scale=%d:%d,format=gray,showinfo", structureFrameWidth, structureFrameHeight)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "info", "-threads", "2", "-ss", strconv.FormatFloat(float64(start)/1000, 'f', 3, 64), "-t", strconv.FormatFloat(float64(end-start)/1000, 'f', 3, 64), "-copyts", "-start_at_zero", "-i", path, "-map", "0:V:0", "-an", "-sn", "-dn", "-vf", selectFilter, "-fps_mode", "passthrough", "-threads", "2", "-f", "rawvideo", "pipe:1"}
	command := exec.CommandContext(ctx, binary, args...)
	applyEnhancementProcessGroup(command)
	command.Cancel = func() error { killEnhancementProcessGroup(command); return nil }
	command.WaitDelay = 3 * time.Second
	output := &boundedCapture{limit: 245 * structureFrameBytes}
	stderr := &boundedCapture{limit: 1024 * 1024}
	command.Stdout = output
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("structure_decode_failed: %w", err)
	}
	if output.overflow || stderr.overflow {
		return nil, fmt.Errorf("structure_output_too_large")
	}
	matches := structurePTSPattern.FindAllStringSubmatch(stderr.buf.String(), -1)
	data := output.buf.Bytes()
	if len(data)%structureFrameBytes != 0 || len(matches) != len(data)/structureFrameBytes {
		return nil, fmt.Errorf("structure_frame_timestamps_mismatch")
	}
	frames := make([]structureFrame, 0, len(matches))
	for i, match := range matches {
		seconds, err := strconv.ParseFloat(strings.TrimSpace(match[1]), 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return nil, fmt.Errorf("structure_invalid_frame_time")
		}
		pts := int64(math.Round(seconds * 1000))
		if pts < start || pts >= end {
			continue
		}
		frames = append(frames, structureFrame{PTS: pts, Gray: append([]byte(nil), data[i*structureFrameBytes:(i+1)*structureFrameBytes]...)})
	}
	return frames, nil
}
