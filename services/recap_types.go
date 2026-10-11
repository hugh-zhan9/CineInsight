package services

import "time"

// 观看回顾（D-RECAP-CONTENT）的请求与返回结构。全部是只读事实与按固定规则生成的文案：
// 不推算观看时长，排行一律按 ID 兜底稳定排序。

const (
	RecapPeriodLive  = "live"
	RecapPeriodMonth = "month"
	RecapPeriodYear  = "year"
)

type RecapRequest struct {
	Period string `json:"period"` // live / month / year
	Year   int    `json:"year"`
	Month  int    `json:"month"`
}

// RecapVideo 是排行与卡片里的一部视频。Available=false 表示已进回收站或路径失效：
// 事实照样计入，只是点开不了。
type RecapVideo struct {
	ID             uint     `json:"id"`
	Title          string   `json:"title"`
	Name           string   `json:"name"`
	Duration       float64  `json:"duration"`
	PersonalRating *float64 `json:"personal_rating"`
	Available      bool     `json:"available"`
}

type RecapImage struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Directory string `json:"directory"`
	Available bool   `json:"available"`
}

type RecapTag struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// RecapEntity 是人物或作品集；ImageURL 是头像 / 封面的受控路由，没有时为空。
type RecapEntity struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	ImageURL string `json:"image_url"`
}

type RecapVideoRank struct {
	Video  RecapVideo `json:"video"`
	Count  int64      `json:"count"`
	LastAt time.Time  `json:"last_at" ts_type:"string"`
}

type RecapImageRank struct {
	Image  RecapImage `json:"image"`
	Count  int64      `json:"count"`
	LastAt time.Time  `json:"last_at" ts_type:"string"`
}

// RecapTagRank / RecapEntityRank：Weight 是带这个标签（人物、作品集）的视频在本期的播放次数之和，
// Videos 是其中不同视频的部数。
type RecapTagRank struct {
	Tag    RecapTag `json:"tag"`
	Weight int64    `json:"weight"`
	Videos int64    `json:"videos"`
}

type RecapEntityRank struct {
	Entity RecapEntity `json:"entity"`
	Weight int64       `json:"weight"`
	Videos int64       `json:"videos"`
}

// RecapRelatedVideo / RecapRelatedImage：Shared 是与本期最爱共享的标签、人物、作品集个数之和。
type RecapRelatedVideo struct {
	Video  RecapVideo `json:"video"`
	Shared int64      `json:"shared"`
}

type RecapRelatedImage struct {
	Image  RecapImage `json:"image"`
	Shared int64      `json:"shared"`
}

// RecapFavoriteVideo 是本期最爱：播放次数最多，并列取最近一次播放更晚的，再并列取 ID 小的。
type RecapFavoriteVideo struct {
	Video       RecapVideo          `json:"video"`
	Count       int64               `json:"count"`
	LastAt      time.Time           `json:"last_at" ts_type:"string"`
	Tags        []RecapTag          `json:"tags"`
	People      []RecapEntity       `json:"people"`
	Collections []RecapEntity       `json:"collections"`
	Related     []RecapRelatedVideo `json:"related"`
}

type RecapFavoriteImage struct {
	Image   RecapImage          `json:"image"`
	Count   int64               `json:"count"`
	LastAt  time.Time           `json:"last_at" ts_type:"string"`
	Tags    []RecapTag          `json:"tags"`
	People  []RecapEntity       `json:"people"`
	Related []RecapRelatedImage `json:"related"`
}

// RecapComparison 是与上一同期的对比。进行中的期次只取上一期同样长的一段（今天对昨天同一时段、
// 本月对上月同期）。上一期为 0 时百分比为 nil，前端不显示百分比。
type RecapComparison struct {
	Label            string    `json:"label"`
	Start            time.Time `json:"start" ts_type:"string"`
	End              time.Time `json:"end" ts_type:"string"`
	Plays            int64     `json:"plays"`
	Videos           int64     `json:"videos"`
	ImageViews       int64     `json:"image_views"`
	PlaysChange      *float64  `json:"plays_change"`
	VideosChange     *float64  `json:"videos_change"`
	ImageViewsChange *float64  `json:"image_views_change"`
}

type RecapOverview struct {
	Plays         int64           `json:"plays"`
	LegacyPlays   int64           `json:"legacy_plays"`
	Videos        int64           `json:"videos"`
	ImageViews    int64           `json:"image_views"`
	Images        int64           `json:"images"`
	ActiveDays    int             `json:"active_days"`
	LongestStreak int             `json:"longest_streak"`
	Previous      RecapComparison `json:"previous"`
}

type RecapHourBucket struct {
	Hour  int   `json:"hour"`
	Plays int64 `json:"plays"`
	Views int64 `json:"views"`
}

type RecapDayBucket struct {
	Date  string `json:"date"` // 本机时区的 2006-01-02
	Plays int64  `json:"plays"`
	Views int64  `json:"views"`
}

type RecapMonthBucket struct {
	Month int   `json:"month"`
	Plays int64 `json:"plays"`
	Views int64 `json:"views"`
}

// RecapSourceShare：Kind 为 video / image，Source 为事件来源。
type RecapSourceShare struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Count  int64  `json:"count"`
}

// RecapHabits 的分布与占比都按「活动」计：视频播放与图片查看各算一条。
// PeakSlot 取 late_night(0–5) / morning(6–11) / afternoon(12–17) / evening(18–23)，没有活动时为空。
type RecapHabits struct {
	Total          int64              `json:"total"`
	Hours          []RecapHourBucket  `json:"hours"`
	Days           []RecapDayBucket   `json:"days"`
	BusiestDay     *RecapDayBucket    `json:"busiest_day"`
	PeakHour       int                `json:"peak_hour"`
	PeakSlot       string             `json:"peak_slot"`
	WeekendCount   int64              `json:"weekend_count"`
	WeekendShare   float64            `json:"weekend_share"`
	LateNightCount int64              `json:"late_night_count"`
	LateNightShare float64            `json:"late_night_share"`
	Sources        []RecapSourceShare `json:"sources"`
}

// RecapFreshness：新发现 = 本期第一次出现播放事件的视频；重温 = 本期有播放且本期开始前已有事件。
// legacy 事件计入总数但不让一部片成为新发现（它可以作为「以前看过」的证据）。
// 新收藏按 favorited_at 落在本期且至今仍收藏；DiaryRated 是本期观影日记里带评分的条目数
// （片库评分本身没有时间戳，无法归期）。
type RecapFreshness struct {
	NewVideos         int64        `json:"new_videos"`
	RewatchedVideos   int64        `json:"rewatched_videos"`
	NewSamples        []RecapVideo `json:"new_samples"`
	NewFavoriteVideos int64        `json:"new_favorite_videos"`
	NewFavoriteImages int64        `json:"new_favorite_images"`
	DiaryRated        int64        `json:"diary_rated"`
}

// RecapTitle 是按固定规则给出的称号，Detail 写明依据的数字。
type RecapTitle struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type RecapMonthBest struct {
	Month int             `json:"month"`
	Best  *RecapVideoRank `json:"best"`
}

type RecapQuarterTags struct {
	Quarter int            `json:"quarter"`
	Tags    []RecapTagRank `json:"tags"`
}

// RecapFeedItem 是实时页的今日流水：Kind 为 video / image。
type RecapFeedItem struct {
	Kind      string    `json:"kind"`
	ID        uint      `json:"id"`
	Title     string    `json:"title"`
	Source    string    `json:"source"`
	At        time.Time `json:"at" ts_type:"string"`
	Available bool      `json:"available"`
}

// RecapNowPlaying 只作提示：Basis=queue 表示待播队列当前项正在播放，recent_play 表示 2 小时内最后一条播放事件。
type RecapNowPlaying struct {
	Video  RecapVideo `json:"video"`
	Basis  string     `json:"basis"`
	Source string     `json:"source"`
	At     time.Time  `json:"at" ts_type:"string"`
}

// RecapResult 是一期回顾。Start/End 是本机时区的 [Start, End) 自然边界；InProgress 表示这一期还没结束。
// Empty 表示期内没有任何播放或查看事件：此时不出称号，排行与最爱为空。
// 年度附加：Months、MonthBest、QuarterTags；实时附加：Feed、Week、NowPlaying。其余期次这些字段为空。
type RecapResult struct {
	Period         string              `json:"period"`
	Year           int                 `json:"year"`
	Month          int                 `json:"month"`
	Label          string              `json:"label"`
	Start          time.Time           `json:"start" ts_type:"string"`
	End            time.Time           `json:"end" ts_type:"string"`
	InProgress     bool                `json:"in_progress"`
	GeneratedAt    time.Time           `json:"generated_at" ts_type:"string"`
	Empty          bool                `json:"empty"`
	Overview       RecapOverview       `json:"overview"`
	Favorite       *RecapFavoriteVideo `json:"favorite"`
	FavoriteImage  *RecapFavoriteImage `json:"favorite_image"`
	TopVideos      []RecapVideoRank    `json:"top_videos"`
	TopTags        []RecapTagRank      `json:"top_tags"`
	TopPeople      []RecapEntityRank   `json:"top_people"`
	TopCollections []RecapEntityRank   `json:"top_collections"`
	TopImages      []RecapImageRank    `json:"top_images"`
	Habits         RecapHabits         `json:"habits"`
	Freshness      RecapFreshness      `json:"freshness"`
	Titles         []RecapTitle        `json:"titles"`
	Months         []RecapMonthBucket  `json:"months"`
	MonthBest      []RecapMonthBest    `json:"month_best"`
	QuarterTags    []RecapQuarterTags  `json:"quarter_tags"`
	Feed           []RecapFeedItem     `json:"feed"`
	Week           []RecapDayBucket    `json:"week"`
	NowPlaying     *RecapNowPlaying    `json:"now_playing"`
}
