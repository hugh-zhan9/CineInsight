package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"video-master/database"

	"gorm.io/gorm"
)

// 回顾「查看全部」：页面上的数字（新发现 137 部、看过的视频 N 部……）与只展示前几个的样本 / 排行之间，
// 用同口径的完整列表分页补上。完整列表含已删除 / 已移除的媒体（Available=false），所以 Total 与页面上的数字相等；
// 顺序与样本 / 排行相同。每页重算一次时间窗内的事实，只水合这一页要显示的行。

const (
	RecapListNew         = "new"
	RecapListRewatched   = "rewatched"
	RecapListFavorites   = "favorites"
	RecapListDiary       = "diary"
	RecapListVideos      = "videos"
	RecapListImages      = "images"
	RecapListFeed        = "feed"
	RecapListTags        = "tags"
	RecapListPeople      = "people"
	RecapListCollections = "collections"

	recapListDefaultLimit = 60
	recapListMaxLimit     = 200
)

var (
	// ErrRecapListKindInvalid 是未知的列表种类。
	ErrRecapListKindInvalid = errors.New("recap_list_kind_invalid")
	// ErrRecapListOffsetInvalid 是负的 offset。
	ErrRecapListOffsetInvalid = errors.New("recap_list_offset_invalid")
)

var recapListKinds = map[string]bool{RecapListNew: true, RecapListRewatched: true, RecapListFavorites: true, RecapListDiary: true,
	RecapListVideos: true, RecapListImages: true, RecapListFeed: true, RecapListTags: true, RecapListPeople: true, RecapListCollections: true}

// RecapListRequest：Period / Year / Month 与 RecapRequest 同义；Limit 不传（≤0）取 60，最多 200。
type RecapListRequest struct {
	Period string `json:"period"`
	Year   int    `json:"year"`
	Month  int    `json:"month"`
	Kind   string `json:"kind"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// RecapListPage 是一页完整列表。按 Kind 只填其中一个切片，其余是空数组（永远不是 null）：
// new / rewatched → Videos，videos → VideoRanks，images → ImageRanks，favorites → Favorites，diary → Diary，
// feed → Feed，tags → Tags，people / collections → Entities。
type RecapListPage struct {
	Kind       string             `json:"kind"`
	Total      int64              `json:"total"`
	Offset     int                `json:"offset"`
	HasMore    bool               `json:"has_more"`
	Videos     []RecapVideo       `json:"videos"`
	VideoRanks []RecapVideoRank   `json:"video_ranks"`
	ImageRanks []RecapImageRank   `json:"image_ranks"`
	Favorites  []RecapNewFavorite `json:"favorites"`
	Diary      []RecapDiaryRated  `json:"diary"`
	Feed       []RecapFeedItem    `json:"feed"`
	Tags       []RecapTagRank     `json:"tags"`
	Entities   []RecapEntityRank  `json:"entities"`
}

func newRecapListPage(kind string, offset int) *RecapListPage {
	return &RecapListPage{Kind: kind, Offset: offset, Videos: []RecapVideo{}, VideoRanks: []RecapVideoRank{},
		ImageRanks: []RecapImageRank{}, Favorites: []RecapNewFavorite{}, Diary: []RecapDiaryRated{}, Feed: []RecapFeedItem{},
		Tags: []RecapTagRank{}, Entities: []RecapEntityRank{}}
}

// ListRecapItems 返回一期回顾里某一类的完整列表的一页（D-RECAP-LIST）。
func (s *RecapService) ListRecapItems(ctx context.Context, req RecapListRequest) (*RecapListPage, error) {
	kind := strings.TrimSpace(req.Kind)
	if !recapListKinds[kind] {
		return nil, fmt.Errorf("%w: %q", ErrRecapListKindInvalid, req.Kind)
	}
	if req.Offset < 0 {
		return nil, fmt.Errorf("%w: %d", ErrRecapListOffsetInvalid, req.Offset)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = recapListDefaultLimit
	}
	limit = min(limit, recapListMaxLimit)
	loc := s.location()
	now := s.clock().In(loc)
	w, err := resolveRecapWindow(RecapRequest{Period: req.Period, Year: req.Year, Month: req.Month}, now, loc)
	if err != nil {
		return nil, err
	}
	page := newRecapListPage(kind, req.Offset)
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		b := &recapBuilder{db: db, w: w, now: now, loc: loc, result: &RecapResult{}}
		count, err := b.listPage(page, req.Offset, limit)
		page.HasMore = int64(req.Offset+count) < page.Total
		return err
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// recapPageBounds 是 [offset, offset+limit) 落在 total 个元素里的那一段。
func recapPageBounds(total, offset, limit int) (int, int) {
	return min(offset, total), min(offset+limit, total)
}
