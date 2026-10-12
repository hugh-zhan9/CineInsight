package services

import (
	"fmt"
	"os"
	"strings"
	"video-master/services/subtitleparser"
)

func formatSRTTimestamp(ms int64) string {
	hours := ms / 3600000
	minutes := (ms % 3600000) / 60000
	seconds := (ms % 60000) / 1000
	millis := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, seconds, millis)
}
func writeSRT(path string, segments []subtitleparser.Segment) error {
	var builder strings.Builder
	for idx, segment := range segments {
		builder.WriteString(fmt.Sprintf("%d\n", idx+1))
		builder.WriteString(formatSRTTimestamp(segment.StartTimeMs))
		builder.WriteString(" --> ")
		builder.WriteString(formatSRTTimestamp(segment.EndTimeMs))
		builder.WriteString("\n")
		builder.WriteString(segment.Text)
		builder.WriteString("\n\n")
	}
	return os.WriteFile(path, []byte(builder.String()), 0644)
}
