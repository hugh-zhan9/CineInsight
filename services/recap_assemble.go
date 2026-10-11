package services

// hydrate 一次读回本期要显示的视频与图片（Top、新发现样本、每月之最、今日流水、正在播放）。
func (b *recapBuilder) hydrate() (map[uint]RecapVideo, map[uint]RecapImage, error) {
	videos, err := hydrateRecapVideos(b.db, recapIDList(b.videoIDs))
	if err != nil {
		return nil, nil, err
	}
	images, err := hydrateRecapImages(b.db, recapIDList(b.imageIDs))
	if err != nil {
		return nil, nil, err
	}
	return videos, images, nil
}

func recapIDList(set map[uint]bool) []uint {
	ids := make([]uint, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	return ids
}

// recapVideoOf / recapImageOf：水合不到（视频已被永久删除，事件随之级联消失之前的竞态）时只留 ID。
func recapVideoOf(videos map[uint]RecapVideo, id uint) RecapVideo {
	if video, ok := videos[id]; ok {
		return video
	}
	return RecapVideo{ID: id}
}

func recapImageOf(images map[uint]RecapImage, id uint) RecapImage {
	if image, ok := images[id]; ok {
		return image
	}
	return RecapImage{ID: id}
}

func (b *recapBuilder) assemble(videos map[uint]RecapVideo, images map[uint]RecapImage) {
	r := b.result
	for index, item := range b.rankedVideos {
		if index == recapTopVideos {
			break
		}
		r.TopVideos = append(r.TopVideos, RecapVideoRank{Video: recapVideoOf(videos, item.id), Count: item.count, LastAt: item.last})
	}
	for index, item := range b.rankedImages {
		if index == recapTopImages {
			break
		}
		r.TopImages = append(r.TopImages, RecapImageRank{Image: recapImageOf(images, item.id), Count: item.count, LastAt: item.last})
	}
	for _, item := range b.newSamples {
		r.Freshness.NewSamples = append(r.Freshness.NewSamples, recapVideoOf(videos, item.id))
	}
	if r.Period == RecapPeriodYear && !r.Empty {
		for month, item := range b.monthBest {
			best := RecapMonthBest{Month: month + 1}
			if item != nil {
				best.Best = &RecapVideoRank{Video: recapVideoOf(videos, item.id), Count: item.count, LastAt: item.last}
			}
			r.MonthBest = append(r.MonthBest, best)
		}
	}
	for _, item := range b.feed {
		if item.Kind == RecapActivityVideo {
			video := recapVideoOf(videos, item.ID)
			item.Title, item.Available = video.Title, video.Available
		} else {
			image := recapImageOf(images, item.ID)
			item.Title, item.Available = image.Name, image.Available
		}
		r.Feed = append(r.Feed, item)
	}
	if b.nowPlaying != nil {
		now := *b.nowPlaying
		now.Video = recapVideoOf(videos, now.Video.ID)
		r.NowPlaying = &now
	}
}

// favorites 填本期最爱视频与图片：附它的标签、人物、作品集，以及「相关视频（图片）还有」。
func (b *recapBuilder) favorites(videos map[uint]RecapVideo, images map[uint]RecapImage) error {
	if len(b.rankedVideos) > 0 {
		top := b.rankedVideos[0]
		favorite := &RecapFavoriteVideo{Video: recapVideoOf(videos, top.id), Count: top.count, LastAt: top.last}
		var err error
		if favorite.Tags, err = recapVideoTags(b.db, top.id); err != nil {
			return err
		}
		if favorite.People, err = recapPeople(b.db, "video_people", "video_id", top.id); err != nil {
			return err
		}
		if favorite.Collections, err = recapVideoCollections(b.db, top.id); err != nil {
			return err
		}
		if favorite.Related, err = recapRelatedVideos(b.db, top.id, b.w.start, b.w.end); err != nil {
			return err
		}
		b.result.Favorite = favorite
	}
	if len(b.rankedImages) > 0 {
		top := b.rankedImages[0]
		favorite := &RecapFavoriteImage{Image: recapImageOf(images, top.id), Count: top.count, LastAt: top.last}
		var err error
		if favorite.Tags, err = recapImageTags(b.db, top.id); err != nil {
			return err
		}
		if favorite.People, err = recapPeople(b.db, "image_people", "image_id", top.id); err != nil {
			return err
		}
		if favorite.Related, err = recapRelatedImages(b.db, top.id, b.w.start, b.w.end); err != nil {
			return err
		}
		b.result.FavoriteImage = favorite
	}
	return nil
}
