package services

import (
	"context"
	"time"
	"video-master/database"

	"gorm.io/gorm"
)

// RecapService 生成观看回顾（D-RECAP-CONTENT）。只读：事件按时间窗一次读入后在 Go 侧按本机时区归并，
// 标签 / 人物 / 作品集排行与相关推荐在数据库里分组取前 N，Top 列表只水合要显示的行。
type RecapService struct {
	now func() time.Time
	loc *time.Location
}

func NewRecapService() *RecapService {
	return &RecapService{now: time.Now}
}

const (
	recapTopVideos        = 10
	recapTopTags          = 10
	recapTopPeople        = 5
	recapTopCollections   = 5
	recapTopImages        = 12
	recapNewSamples       = 6
	recapFeedLimit        = 30
	recapWeekDays         = 7
	recapQuarterTags      = 3
	recapNowPlayingWindow = 2 * time.Hour
)

func (s *RecapService) location() *time.Location {
	if s.loc != nil {
		return s.loc
	}
	return time.Local
}

func (s *RecapService) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// GetRecap 计算一期回顾。live 取今天 0 点至今（附近 7 天逐日），month / year 取自然月 / 自然年。
func (s *RecapService) GetRecap(ctx context.Context, req RecapRequest) (*RecapResult, error) {
	loc := s.location()
	now := s.clock().In(loc)
	w, err := resolveRecapWindow(req, now, loc)
	if err != nil {
		return nil, err
	}
	result := &RecapResult{Period: w.period, Year: w.year, Month: w.month, Label: w.label, Start: w.start, End: w.end,
		InProgress: w.inProgress, GeneratedAt: now, TopVideos: []RecapVideoRank{}, TopTags: []RecapTagRank{},
		TopPeople: []RecapEntityRank{}, TopCollections: []RecapEntityRank{}, TopImages: []RecapImageRank{},
		Titles: []RecapTitle{}, Months: []RecapMonthBucket{}, MonthBest: []RecapMonthBest{}, QuarterTags: []RecapQuarterTags{},
		Feed: []RecapFeedItem{}, Week: []RecapDayBucket{}, Freshness: RecapFreshness{NewSamples: []RecapVideo{},
			RewatchedSamples: []RecapVideo{}, NewFavoriteSamples: []RecapNewFavorite{}, DiaryRatedSamples: []RecapDiaryRated{}}}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		return (&recapBuilder{db: db, w: w, now: now, loc: loc, result: result}).build()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// recapBuilder 持有一次计算的中间状态，把 GetRecap 拆成几步可读的小函数。
type recapBuilder struct {
	db       *gorm.DB
	w        recapWindow
	now      time.Time
	loc      *time.Location
	result   *RecapResult
	tally    *recapTally
	plays    []recapPlayEvent
	views    []recapViewEvent
	videoIDs map[uint]bool
	imageIDs map[uint]bool

	// rankedVideos / rankedImages 只含仍可展示的媒体（未删除、未失效）；allRankedVideos 含期内全部，
	// 供新发现/重温计数使用。
	rankedVideos    []*recapMediaTally
	rankedImages    []*recapMediaTally
	allRankedVideos []*recapMediaTally
	availableVideos map[uint]bool
	availableImages map[uint]bool
	newSamples      []*recapMediaTally
	rewatchSamples  []*recapMediaTally
	monthBest       [12]*recapMediaTally
	feed            []RecapFeedItem
	nowPlaying      *RecapNowPlaying
	topTagWeight    int64
	topTagName      string
}

func (b *recapBuilder) build() error {
	fetchStart := b.w.start
	if b.w.period == RecapPeriodLive {
		fetchStart = b.w.start.AddDate(0, 0, -(recapWeekDays - 1))
	}
	if err := b.loadEvents(fetchStart); err != nil {
		return err
	}
	b.videoIDs, b.imageIDs = map[uint]bool{}, map[uint]bool{}
	if err := b.loadAvailability(); err != nil {
		return err
	}
	if err := b.overview(); err != nil {
		return err
	}
	if b.w.period == RecapPeriodLive {
		if err := b.live(fetchStart); err != nil {
			return err
		}
	}
	if !b.result.Empty {
		if b.w.period == RecapPeriodYear {
			if err := b.yearExtras(); err != nil {
				return err
			}
		}
		if err := b.rankings(); err != nil {
			return err
		}
		if err := b.freshness(); err != nil {
			return err
		}
	}
	videos, images, err := b.hydrate()
	if err != nil {
		return err
	}
	b.assemble(videos, images)
	if b.result.Empty {
		return nil
	}
	if err := b.favorites(videos, images); err != nil {
		return err
	}
	b.result.Titles = recapTitles(recapTitleFacts{plays: b.tally.plays, views: b.tally.views, lateNight: b.tally.lateNight,
		weekend: b.tally.weekend, newVideos: b.result.Freshness.NewVideos, rewatched: b.result.Freshness.RewatchedVideos,
		longestStreak: b.result.Overview.LongestStreak, topTagName: b.topTagName, topTagWeight: b.topTagWeight})
	return nil
}

// loadEvents 读 [from, end) 的播放与查看事件（实时页要多读近 7 天），只把落在本期内的计入 tally。
func (b *recapBuilder) loadEvents(from time.Time) error {
	var err error
	if b.plays, err = loadRecapPlayEvents(b.db, from, b.w.end); err != nil {
		return err
	}
	if b.views, err = loadRecapViewEvents(b.db, from, b.w.end); err != nil {
		return err
	}
	b.tally = newRecapTally(b.loc)
	for _, event := range b.plays {
		if !event.PlayedAt.Before(b.w.start) {
			b.tally.addPlay(event)
		}
	}
	for _, event := range b.views {
		if !event.ViewedAt.Before(b.w.start) {
			b.tally.addView(event)
		}
	}
	return nil
}
