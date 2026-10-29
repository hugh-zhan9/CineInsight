package services

import "video-master/models"

// listPage 按种类填一页，返回这一页的条数。标签 / 人物 / 作品集、新收藏、日记直接在数据库里分页；
// 其余几类与 GetRecap 一样把时间窗内的事件读进 tally，再按页面同一个顺序切出这一页、只水合这一页。
func (b *recapBuilder) listPage(page *RecapListPage, offset, limit int) (int, error) {
	var err error
	start, end := b.w.start, b.w.end
	switch page.Kind {
	case RecapListTags:
		page.Tags, page.Total, err = recapTagRanks(b.db, start, end, offset, limit)
		return len(page.Tags), err
	case RecapListPeople:
		page.Entities, page.Total, err = recapPeopleRanks(b.db, start, end, offset, limit)
		return len(page.Entities), err
	case RecapListCollections:
		page.Entities, page.Total, err = recapCollectionRanks(b.db, start, end, offset, limit)
		return len(page.Entities), err
	case RecapListFavorites:
		videos, images, err := recapFavoriteCounts(b.db, start, end)
		if err != nil {
			return 0, err
		}
		page.Total = videos + images
		page.Favorites, err = recapNewFavoriteList(b.db, start, end, offset, limit, false, b.loc)
		return len(page.Favorites), err
	case RecapListDiary:
		if page.Total, err = recapDiaryRated(b.db, start, end); err != nil {
			return 0, err
		}
		page.Diary, err = recapDiaryRatedList(b.db, start, end, offset, limit)
		return len(page.Diary), err
	}
	if err := b.loadEvents(start); err != nil {
		return 0, err
	}
	switch page.Kind {
	case RecapListImages:
		return b.listImages(page, offset, limit)
	case RecapListFeed:
		return b.listFeed(page, offset, limit)
	}
	return b.listVideos(page, offset, limit)
}

// listVideos 填 videos（排行顺序）、new（新发现，排行顺序）或 rewatched（期内最后一次播放从近到远）。
func (b *recapBuilder) listVideos(page *RecapListPage, offset, limit int) (int, error) {
	items := rankRecapMedia(b.tally.videos)
	if page.Kind != RecapListVideos {
		prior, err := recapPriorVideoIDs(b.db, b.w.start, b.w.end)
		if err != nil {
			return 0, err
		}
		fresh, rewatched := recapFreshnessGroups(items, prior)
		items = fresh
		if page.Kind == RecapListRewatched {
			items = recapMostRecent(rewatched, len(rewatched))
		}
	}
	if page.Kind == RecapListNew {
		available, err := recapAvailableIDs(b.db, &models.Video{}, recapItemIDs(items))
		if err != nil {
			return 0, err
		}
		items = recapOnlyAvailable(items, available)
	}
	page.Total = int64(len(items))
	from, to := recapPageBounds(len(items), offset, limit)
	items = items[from:to]
	videos, err := hydrateRecapVideos(b.db, recapItemIDs(items))
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		video := recapVideoOf(videos, item.id)
		if page.Kind == RecapListVideos {
			page.VideoRanks = append(page.VideoRanks, RecapVideoRank{Video: video, Count: item.count, LastAt: item.last})
		} else {
			page.Videos = append(page.Videos, video)
		}
	}
	return len(items), nil
}

// listImages 填 images：按排行顺序（次数降序、最近一次更晚、ID 升序）。
func (b *recapBuilder) listImages(page *RecapListPage, offset, limit int) (int, error) {
	items := rankRecapMedia(b.tally.images)
	page.Total = int64(len(items))
	from, to := recapPageBounds(len(items), offset, limit)
	items = items[from:to]
	images, err := hydrateRecapImages(b.db, recapItemIDs(items))
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		page.ImageRanks = append(page.ImageRanks, RecapImageRank{Image: recapImageOf(images, item.id), Count: item.count, LastAt: item.last})
	}
	return len(items), nil
}

// listFeed 填 feed：期内全部播放与查看事件，最新的在前（实时页即今天的全部流水）。
func (b *recapBuilder) listFeed(page *RecapListPage, offset, limit int) (int, error) {
	items := recapFeedEvents(b.plays, b.views, b.w.start, b.loc)
	page.Total = int64(len(items))
	from, to := recapPageBounds(len(items), offset, limit)
	items = items[from:to]
	videoIDs, imageIDs := map[uint]bool{}, map[uint]bool{}
	for _, item := range items {
		if item.Kind == RecapActivityVideo {
			videoIDs[item.ID] = true
		} else {
			imageIDs[item.ID] = true
		}
	}
	videos, err := hydrateRecapVideos(b.db, recapIDList(videoIDs))
	if err != nil {
		return 0, err
	}
	images, err := hydrateRecapImages(b.db, recapIDList(imageIDs))
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		page.Feed = append(page.Feed, recapFeedTitled(item, videos, images))
	}
	return len(items), nil
}

func recapItemIDs(items []*recapMediaTally) []uint {
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.id)
	}
	return ids
}
