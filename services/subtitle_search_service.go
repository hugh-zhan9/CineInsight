package services

import (
	"os"
	"strings"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"
)

type SubtitleSearchMatch struct {
	Video   models.Video           `json:"video"`
	Segment subtitleparser.Segment `json:"segment"`
}

type SubtitleSearchService struct{}

func (s *SubtitleSearchService) SearchSubtitleMatches(keyword string, limit int) ([]SubtitleSearchMatch, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return []SubtitleSearchMatch{}, nil
	}
	if limit <= 0 {
		limit = 20
	}

	var videos []models.Video
	if err := database.DB.Preload("Tags").Order("id desc").Find(&videos).Error; err != nil {
		return nil, err
	}

	needle := strings.ToLower(keyword)
	matches := make([]SubtitleSearchMatch, 0, limit)
	for _, video := range videos {
		srtPath := subtitleparser.SRTPathForVideo(video.Path)
		if _, err := os.Stat(srtPath); err != nil {
			continue
		}

		segments, err := subtitleparser.ParseFile(srtPath)
		if err != nil {
			continue
		}

		for _, segment := range segments {
			if !strings.Contains(strings.ToLower(segment.Text), needle) {
				continue
			}
			matches = append(matches, SubtitleSearchMatch{
				Video:   video,
				Segment: segment,
			})
			break
		}

		if len(matches) >= limit {
			return matches, nil
		}
	}

	return matches, nil
}
