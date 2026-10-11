package services

import (
	"sort"
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

// yearExtras：12 个月的分布、每月之最（各月 Top1 视频，与本期最爱同一排序口径）、每季 Top3 标签。
func (b *recapBuilder) yearExtras() error {
	r := b.result
	r.Months = append([]RecapMonthBucket{}, b.tally.months[:]...)
	for month := range b.tally.monthVideos {
		if ranked := recapOnlyAvailable(rankRecapMedia(b.tally.monthVideos[month]), b.availableVideos); len(ranked) > 0 {
			b.monthBest[month] = ranked[0]
			b.videoIDs[ranked[0].id] = true
		}
	}
	for quarter := 0; quarter < 4; quarter++ {
		start := b.w.start.AddDate(0, 3*quarter, 0)
		tags, err := recapTagRanks(b.db, start, start.AddDate(0, 3, 0), recapQuarterTags)
		if err != nil {
			return err
		}
		r.QuarterTags = append(r.QuarterTags, RecapQuarterTags{Quarter: quarter + 1, Tags: tags})
	}
	return nil
}

// live：近 7 天逐日柱状、今日流水（最近 30 条，视频与图片合并）、正在播放提示。
func (b *recapBuilder) live(weekStart time.Time) error {
	days := recapDays(weekStart, b.w.end)
	week := make(map[string]*RecapDayBucket, len(days))
	for _, date := range days {
		week[date] = &RecapDayBucket{Date: date}
	}
	for _, event := range b.plays {
		if bucket := week[event.PlayedAt.In(b.loc).Format("2006-01-02")]; bucket != nil {
			bucket.Plays++
		}
	}
	for _, event := range b.views {
		if bucket := week[event.ViewedAt.In(b.loc).Format("2006-01-02")]; bucket != nil {
			bucket.Views++
		}
	}
	for _, date := range days {
		b.result.Week = append(b.result.Week, *week[date])
	}
	// 事件是按时间升序读入的：从尾部各取最多 30 条今日事件再合并。
	for i := len(b.plays) - 1; i >= 0 && len(b.feed) < recapFeedLimit; i-- {
		event := b.plays[i]
		if event.PlayedAt.Before(b.w.start) {
			break
		}
		if !b.availableVideos[event.VideoID] {
			continue
		}
		b.feed = append(b.feed, RecapFeedItem{Kind: RecapActivityVideo, ID: event.VideoID, Source: event.Source, At: event.PlayedAt.In(b.loc)})
	}
	videoItems := len(b.feed)
	for i := len(b.views) - 1; i >= 0 && len(b.feed)-videoItems < recapFeedLimit; i-- {
		event := b.views[i]
		if event.ViewedAt.Before(b.w.start) {
			break
		}
		if !b.availableImages[event.ImageID] {
			continue
		}
		b.feed = append(b.feed, RecapFeedItem{Kind: RecapActivityImage, ID: event.ImageID, Source: event.Source, At: event.ViewedAt.In(b.loc)})
	}
	sort.SliceStable(b.feed, func(i, j int) bool { return b.feed[i].At.After(b.feed[j].At) })
	if len(b.feed) > recapFeedLimit {
		b.feed = b.feed[:recapFeedLimit]
	}
	for _, item := range b.feed {
		if item.Kind == RecapActivityVideo {
			b.videoIDs[item.ID] = true
		} else {
			b.imageIDs[item.ID] = true
		}
	}
	now, err := recapNowPlaying(b.db, b.now)
	if err != nil || now == nil {
		return err
	}
	b.nowPlaying = now
	b.videoIDs[now.Video.ID] = true
	return nil
}

// recapNowPlaying 只作提示：待播队列当前项正在起播 / 播放 / 已交给系统播放器时取它；否则取 2 小时内最后一条播放事件。
func recapNowPlaying(db *gorm.DB, now time.Time) (*RecapNowPlaying, error) {
	var states []models.PlaybackQueueState
	if err := db.Where("id = ?", 1).Limit(1).Find(&states).Error; err != nil {
		return nil, err
	}
	if len(states) == 1 && states[0].CurrentEntryID != nil {
		switch state := states[0]; state.Status {
		case "starting", "playing", "dispatched":
			var entries []models.PlaybackQueueEntry
			if err := db.Where("id = ?", *state.CurrentEntryID).Limit(1).Find(&entries).Error; err != nil {
				return nil, err
			}
			if len(entries) == 1 {
				return &RecapNowPlaying{Video: RecapVideo{ID: entries[0].VideoID}, Basis: "queue", Source: state.Status, At: state.UpdatedAt}, nil
			}
		}
	}
	var events []recapPlayEvent
	if err := db.Model(&models.PlayEvent{}).Select("id, video_id, played_at, source").
		Where("played_at >= ? AND played_at <= ?", now.Add(-recapNowPlayingWindow), now).
		Order("played_at DESC").Order("id DESC").Limit(1).Scan(&events).Error; err != nil || len(events) == 0 {
		return nil, err
	}
	return &RecapNowPlaying{Video: RecapVideo{ID: events[0].VideoID}, Basis: "recent_play", Source: events[0].Source, At: events[0].PlayedAt.In(now.Location())}, nil
}
