package services

import (
	"sort"
	"video-master/models"
)

// overview 填总览、习惯与对比；期内没有任何事件时标 Empty。
func (b *recapBuilder) overview() error {
	t, r := b.tally, b.result
	days := recapDays(b.w.start, b.w.end)
	active, streak := t.activeDays(days)
	r.Overview = RecapOverview{Plays: t.plays, LegacyPlays: t.legacy, Videos: int64(len(t.videos)), ImageViews: t.views,
		Images: int64(len(t.images)), ActiveDays: active, LongestStreak: streak}
	r.Habits = t.habits(days)
	r.Empty = t.plays+t.views == 0
	plays, videos, views, err := recapPreviousCounts(b.db, b.w.prevStart, b.w.prevEnd)
	if err != nil {
		return err
	}
	r.Overview.Previous = RecapComparison{Label: b.w.prevLabel, Start: b.w.prevStart, End: b.w.prevEnd,
		Plays: plays, Videos: videos, ImageViews: views,
		PlaysChange: recapChange(t.plays, plays), VideosChange: recapChange(int64(len(t.videos)), videos),
		ImageViewsChange: recapChange(t.views, views)}
	return nil
}

// rankings 排出视频与图片 Top（Go 侧，已有全部期内计数）与标签 / 人物 / 作品集 Top（SQL 分组）。
func (b *recapBuilder) rankings() error {
	b.allRankedVideos = rankRecapMedia(b.tally.videos)
	b.rankedVideos = recapOnlyAvailable(b.allRankedVideos, b.availableVideos)
	b.rankedImages = recapOnlyAvailable(rankRecapMedia(b.tally.images), b.availableImages)
	for index, item := range b.rankedVideos {
		if index == recapTopVideos {
			break
		}
		b.videoIDs[item.id] = true
	}
	for index, item := range b.rankedImages {
		if index == recapTopImages {
			break
		}
		b.imageIDs[item.id] = true
	}
	var err error
	r := b.result
	if r.TopTags, err = recapTagRanks(b.db, b.w.start, b.w.end, recapTopTags); err != nil {
		return err
	}
	if len(r.TopTags) > 0 {
		b.topTagName, b.topTagWeight = r.TopTags[0].Tag.Name, r.TopTags[0].Weight
	}
	if r.TopPeople, err = recapPeopleRanks(b.db, b.w.start, b.w.end, recapTopPeople); err != nil {
		return err
	}
	r.TopCollections, err = recapCollectionRanks(b.db, b.w.start, b.w.end, recapTopCollections)
	return err
}

// freshness 判新发现 / 重温：只看期内有非 legacy 播放的视频；本期开始前已有任意事件（含 legacy）为重温，
// 否则若期内最早一条是 legacy 则两边都不算，其余为新发现。新发现样本按排行顺序取前 6；
// 重温样本按期内最后一次播放从近到远取前 6。两者都只取仍可展示的视频。
func (b *recapBuilder) freshness() error {
	prior, err := recapPriorVideoIDs(b.db, b.w.start, b.w.end)
	if err != nil {
		return err
	}
	f := &b.result.Freshness
	var rewatched []*recapMediaTally
	for _, item := range b.allRankedVideos {
		if !item.nonLegacy {
			continue
		}
		switch {
		case prior[item.id]:
			f.RewatchedVideos++
			if b.availableVideos[item.id] {
				rewatched = append(rewatched, item)
			}
		case item.firstSource == models.PlayEventSourceLegacy:
		default:
			f.NewVideos++
			if len(b.newSamples) < recapNewSamples && b.availableVideos[item.id] {
				b.newSamples = append(b.newSamples, item)
				b.videoIDs[item.id] = true
			}
		}
	}
	b.rewatchSamples = recapMostRecent(rewatched, recapNewSamples)
	for _, item := range b.rewatchSamples {
		b.videoIDs[item.id] = true
	}
	if f.NewFavoriteVideos, f.NewFavoriteImages, err = recapFavoriteCounts(b.db, b.w.start, b.w.end); err != nil {
		return err
	}
	if f.NewFavoriteSamples, err = recapNewFavoriteSamples(b.db, b.w.start, b.w.end, recapNewSamples, b.loc); err != nil {
		return err
	}
	if f.DiaryRated, err = recapDiaryRated(b.db, b.w.start, b.w.end); err != nil {
		return err
	}
	f.DiaryRatedSamples, err = recapDiaryRatedSamples(b.db, b.w.start, b.w.end, recapNewSamples)
	return err
}

// recapMostRecent 按期内最后一次事件从近到远（同一时刻 ID 小的在前）取前 limit 个。
func recapMostRecent(items []*recapMediaTally, limit int) []*recapMediaTally {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].last.Equal(items[j].last) {
			return items[i].last.After(items[j].last)
		}
		return items[i].id < items[j].id
	})
	return items[:min(limit, len(items))]
}
