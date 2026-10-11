package services

import (
	"sort"
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

// 回顾的事件只按时间窗读三列，分组放在 Go 侧按本机时区归并：与洞察热力图同一个理由，
// SQLite 没有 DATE 类型，CAST(played_at AS DATE) 会静默返回错误的分组。
type recapPlayEvent struct {
	ID       uint
	VideoID  uint
	PlayedAt time.Time
	Source   string
}

type recapViewEvent struct {
	ID       uint
	ImageID  uint
	ViewedAt time.Time
	Source   string
}

func loadRecapPlayEvents(db *gorm.DB, start, end time.Time) ([]recapPlayEvent, error) {
	rows := []recapPlayEvent{}
	err := db.Model(&models.PlayEvent{}).Select("id, video_id, played_at, source").
		Where("played_at >= ? AND played_at < ?", start, end).
		Order("played_at ASC").Order("id ASC").Scan(&rows).Error
	return rows, err
}

func loadRecapViewEvents(db *gorm.DB, start, end time.Time) ([]recapViewEvent, error) {
	rows := []recapViewEvent{}
	err := db.Model(&models.ImageViewEvent{}).Select("id, image_id, viewed_at, source").
		Where("viewed_at >= ? AND viewed_at < ?", start, end).
		Order("viewed_at ASC").Order("id ASC").Scan(&rows).Error
	return rows, err
}

// recapMediaTally 是一部视频或一张图片在期内的计数。firstSource 是期内最早一条事件的来源，
// nonLegacy 表示期内至少有一条非 legacy 事件——两者用于新发现 / 重温判定。
type recapMediaTally struct {
	id          uint
	count       int64
	last        time.Time
	firstSource string
	nonLegacy   bool
}

func (t *recapMediaTally) add(at time.Time, source string) {
	if t.count == 0 {
		t.firstSource = source
	}
	t.count++
	if at.After(t.last) {
		t.last = at
	}
	if source != models.PlayEventSourceLegacy {
		t.nonLegacy = true
	}
}

// rankRecapMedia 按次数降序、最近一次更晚者优先、ID 升序排出稳定顺序（本期最爱与排行同一口径）。
func rankRecapMedia(items map[uint]*recapMediaTally) []*recapMediaTally {
	ranked := make([]*recapMediaTally, 0, len(items))
	for _, item := range items {
		ranked = append(ranked, item)
	}
	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.count != b.count {
			return a.count > b.count
		}
		if !a.last.Equal(b.last) {
			return a.last.After(b.last)
		}
		return a.id < b.id
	})
	return ranked
}

// recapTally 累计一期内的全部事件。活动（视频播放与图片查看各算一条）用于分布与习惯。
type recapTally struct {
	loc         *time.Location
	videos      map[uint]*recapMediaTally
	images      map[uint]*recapMediaTally
	plays       int64
	legacy      int64
	views       int64
	days        map[string]*RecapDayBucket
	hours       [24]RecapHourBucket
	weekend     int64
	lateNight   int64
	sources     map[RecapSourceShare]int64
	months      [12]RecapMonthBucket
	monthVideos [12]map[uint]*recapMediaTally
}

func newRecapTally(loc *time.Location) *recapTally {
	t := &recapTally{loc: loc, videos: map[uint]*recapMediaTally{}, images: map[uint]*recapMediaTally{},
		days: map[string]*RecapDayBucket{}, sources: map[RecapSourceShare]int64{}}
	for i := range t.hours {
		t.hours[i].Hour = i
	}
	for i := range t.months {
		t.months[i].Month = i + 1
		t.monthVideos[i] = map[uint]*recapMediaTally{}
	}
	return t
}

func recapTallyFor(items map[uint]*recapMediaTally, id uint) *recapMediaTally {
	item := items[id]
	if item == nil {
		item = &recapMediaTally{id: id}
		items[id] = item
	}
	return item
}

func (t *recapTally) addPlay(event recapPlayEvent) {
	at := event.PlayedAt.In(t.loc)
	t.plays++
	if event.Source == models.PlayEventSourceLegacy {
		t.legacy++
	}
	recapTallyFor(t.videos, event.VideoID).add(at, event.Source)
	recapTallyFor(t.monthVideos[at.Month()-1], event.VideoID).add(at, event.Source)
	t.sources[RecapSourceShare{Kind: RecapActivityVideo, Source: event.Source}]++
	day := t.activity(at)
	day.Plays++
	t.hours[at.Hour()].Plays++
	t.months[at.Month()-1].Plays++
}

func (t *recapTally) addView(event recapViewEvent) {
	at := event.ViewedAt.In(t.loc)
	t.views++
	recapTallyFor(t.images, event.ImageID).add(at, event.Source)
	t.sources[RecapSourceShare{Kind: RecapActivityImage, Source: event.Source}]++
	day := t.activity(at)
	day.Views++
	t.hours[at.Hour()].Views++
	t.months[at.Month()-1].Views++
}

// activity 记周末与深夜（0–5 点），返回当天的桶。
func (t *recapTally) activity(at time.Time) *RecapDayBucket {
	if weekday := at.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
		t.weekend++
	}
	if at.Hour() < 6 {
		t.lateNight++
	}
	date := at.Format("2006-01-02")
	day := t.days[date]
	if day == nil {
		day = &RecapDayBucket{Date: date}
		t.days[date] = day
	}
	return day
}
