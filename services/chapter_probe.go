package services

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type mediaProbeChapter struct {
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Tags      struct {
		Title string `json:"title"`
	} `json:"tags"`
}

func parseProbeChapters(chapters *[]mediaProbeChapter, duration *float64) (*string, error) {
	if chapters == nil {
		return nil, nil
	}
	result := []ChapterInput{}
	for i, chapter := range *chapters {
		start, err := strconv.ParseFloat(chapter.StartTime, 64)
		if err != nil {
			return nil, ErrChapterInvalid
		}
		end, err := strconv.ParseFloat(chapter.EndTime, 64)
		if err != nil {
			return nil, ErrChapterInvalid
		}
		if math.IsNaN(start) || math.IsNaN(end) || math.IsInf(start, 0) || math.IsInf(end, 0) || start < 0 || end <= start || end*1000 > experienceMaxSafeInteger {
			return nil, ErrChapterInvalid
		}
		title := strings.TrimSpace(chapter.Tags.Title)
		if title == "" {
			title = fmt.Sprintf("章节 %d", i+1)
		}
		result = append(result, ChapterInput{ID: uint(i + 1), StartMS: int64(math.Round(start * 1000)), EndMS: int64(math.Round(end * 1000)), Title: title})
	}
	limit := int64(experienceMaxSafeInteger)
	if duration != nil && *duration > 0 && !math.IsNaN(*duration) && !math.IsInf(*duration, 0) && *duration*1000 < experienceMaxSafeInteger {
		limit = int64(math.Round(*duration * 1000))
	}
	for _, chapter := range result {
		if chapter.StartMS < 0 || chapter.EndMS <= chapter.StartMS || chapter.EndMS > limit {
			return nil, ErrChapterInvalid
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	text := string(encoded)
	return &text, nil
}
