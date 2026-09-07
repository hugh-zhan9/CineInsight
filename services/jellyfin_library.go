package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
)

const (
	jellyVideo      = "01"
	jellyCollection = "02"
	jellyTag        = "03"
	jellySaved      = "04"
	jellyView       = "05"
	jellyGroup      = "06"
)

var jellyfinViews = []struct{ name, view string }{
	{"全部视频", LibraryViewAll}, {"收藏", LibraryViewFavorites}, {"继续观看", LibraryViewContinueWatching},
	{"未看", LibraryViewUnwatched}, {"已看", LibraryViewWatched}, {"最近添加", LibraryViewRecentlyAdded}, {"最近播放", LibraryViewRecentlyPlayed},
}

func jellyfinID(kind string, id uint) string { return fmt.Sprintf("%s%030x", kind, id) }
func jellyfinParseID(raw string) (string, uint, error) {
	raw = strings.ReplaceAll(raw, "-", "")
	if len(raw) != 32 {
		return "", 0, fmt.Errorf("%w: 资源 ID 无效", errJellyfinQuery)
	}
	id, err := strconv.ParseUint(raw[2:], 16, 64)
	if err != nil || id == 0 || uint64(uint(id)) != id {
		return "", 0, fmt.Errorf("%w: 资源 ID 无效", errJellyfinQuery)
	}
	switch raw[:2] {
	case jellyVideo, jellyCollection, jellyTag, jellySaved, jellyView, jellyGroup:
		return raw[:2], uint(id), nil
	}
	return "", 0, fmt.Errorf("%w: 资源类型无效", errJellyfinQuery)
}
func jellyfinParam(q url.Values, name string) string {
	for key, values := range q {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}
func jellyfinPage(q url.Values) (int, int, error) {
	start, limit := 0, 100
	for _, entry := range []struct {
		name   string
		target *int
	}{{"StartIndex", &start}, {"Limit", &limit}} {
		if raw := jellyfinParam(q, entry.name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				return 0, 0, fmt.Errorf("分页参数无效")
			}
			*entry.target = value
		}
	}
	if limit > 200 {
		limit = 200
	}
	return start, limit, nil
}
func jellyfinResult(items []map[string]interface{}, total int64, start int) map[string]interface{} {
	return map[string]interface{}{"Items": items, "TotalRecordCount": total, "StartIndex": start}
}

func (s *JellyfinServer) folder(kind string, id uint, name, itemType, collectionType string) map[string]interface{} {
	s.mu.Lock()
	serverID := s.config.JellyfinServerID
	s.mu.Unlock()
	return map[string]interface{}{"Id": jellyfinID(kind, id), "Name": name, "SortName": name, "ServerId": serverID, "Type": itemType, "IsFolder": true, "CollectionType": collectionType, "ImageTags": map[string]string{}, "UserData": map[string]interface{}{"IsFavorite": false, "Played": false, "PlaybackPositionTicks": 0}}
}

func (s *JellyfinServer) views(r *http.Request) ([]map[string]interface{}, error) {
	items := []map[string]interface{}{}
	for i, view := range jellyfinViews {
		items = append(items, s.folder(jellyView, uint(i+1), view.name, "CollectionFolder", "movies"))
	}
	items = append(items, s.folder(jellyGroup, 1, "作品集", "CollectionFolder", "boxsets"), s.folder(jellyGroup, 2, "标签", "CollectionFolder", "movies"))
	var views []models.SavedLibraryView
	if err := database.DB.WithContext(r.Context()).Where("smart_view <> ?", LibraryViewStale).Order("LOWER(name), id").Find(&views).Error; err != nil {
		return nil, err
	}
	for _, view := range views {
		items = append(items, s.folder(jellySaved, view.ID, view.Name, "CollectionFolder", "movies"))
	}
	return items, nil
}

func (s *JellyfinServer) serveLibrary(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if user := jellyfinParam(r.URL.Query(), "UserId"); user != "" && strings.ReplaceAll(user, "-", "") != jellyfinUserID {
		jellyfinError(w, 403, "用户无效")
		return
	}
	if len(parts) > 1 && parts[0] == "users" {
		if strings.ReplaceAll(parts[1], "-", "") != jellyfinUserID {
			jellyfinError(w, 403, "用户无效")
			return
		}
		if len(parts) == 3 && parts[2] == "views" && r.Method == "GET" {
			items, err := s.views(r)
			if s.libraryError(w, err) {
				return
			}
			jellyfinJSON(w, jellyfinResult(items, int64(len(items)), 0))
			return
		}
		if len(parts) >= 3 && (parts[2] == "favoriteitems" || parts[2] == "playeditems") {
			s.mutateUserData(w, r, parts)
			return
		}
		if len(parts) >= 3 && parts[2] == "items" {
			parts = parts[2:]
			path = "/" + strings.Join(parts, "/")
		}
	}
	if strings.HasPrefix(path, "/sessions/playing") || strings.HasPrefix(path, "/videos/") || (len(parts) >= 3 && parts[0] == "items" && (parts[2] == "playbackinfo" || parts[2] == "images")) {
		s.servePlayback(w, r, parts)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		jellyfinError(w, 405, "此接口不支持该方法")
		return
	}
	if path == "/quickconnect/enabled" {
		jellyfinJSON(w, false)
		return
	}
	if path == "/sessions" {
		jellyfinJSON(w, []interface{}{})
		return
	}
	if path == "/items" || path == "/items/latest" || path == "/items/resume" || path == "/genres" || path == "/tags" {
		q := r.URL.Query()
		if path == "/items/latest" {
			q.Set("SortBy", "DateCreated")
			q.Set("SortOrder", "Descending")
		}
		if path == "/items/resume" {
			q.Set("IsResumable", "true")
		}
		if path == "/genres" || path == "/tags" {
			q.Set("ParentId", jellyfinID(jellyGroup, 2))
		}
		items, total, start, err := s.listItems(r, q)
		if s.libraryError(w, err) {
			return
		}
		if path == "/items/latest" {
			jellyfinJSON(w, items)
		} else {
			jellyfinJSON(w, jellyfinResult(items, total, start))
		}
		return
	}
	if len(parts) == 2 && parts[0] == "items" {
		kind, id, err := jellyfinParseID(parts[1])
		if s.libraryError(w, err) {
			return
		}
		if kind == jellyVideo {
			video, err := s.visibleVideo(r, id)
			if s.libraryError(w, err) {
				return
			}
			item, err := s.videoDTO(r, *video)
			if s.libraryError(w, err) {
				return
			}
			jellyfinJSON(w, item)
			return
		}
		item, err := s.folderByID(r, kind, id)
		if s.libraryError(w, err) {
			return
		}
		jellyfinJSON(w, item)
		return
	}
	jellyfinError(w, 404, "不支持的 Jellyfin 接口")
}

func (s *JellyfinServer) libraryError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		jellyfinError(w, 404, "资源不存在或不可访问")
	} else if errors.Is(err, errJellyfinQuery) {
		jellyfinError(w, 400, "不支持或无效的查询参数")
	} else {
		jellyfinError(w, 500, "媒体请求失败")
	}
	return true
}

var errJellyfinQuery = errors.New("invalid_jellyfin_query")

func (s *JellyfinServer) folderByID(r *http.Request, kind string, id uint) (map[string]interface{}, error) {
	switch kind {
	case jellyView:
		if id <= uint(len(jellyfinViews)) {
			return s.folder(kind, id, jellyfinViews[id-1].name, "CollectionFolder", "movies"), nil
		}
	case jellyGroup:
		if id == 1 {
			return s.folder(kind, id, "作品集", "CollectionFolder", "boxsets"), nil
		}
		if id == 2 {
			return s.folder(kind, id, "标签", "CollectionFolder", "movies"), nil
		}
	case jellyCollection:
		var row models.MediaCollection
		if err := database.DB.WithContext(r.Context()).First(&row, id).Error; err != nil {
			return nil, err
		}
		item := s.folder(kind, id, row.Name, "BoxSet", "")
		item["Overview"] = row.Description
		return item, nil
	case jellyTag:
		var row models.Tag
		if err := database.DB.WithContext(r.Context()).First(&row, id).Error; err != nil {
			return nil, err
		}
		return s.folder(kind, id, row.Name, "Folder", ""), nil
	case jellySaved:
		var row models.SavedLibraryView
		if err := database.DB.WithContext(r.Context()).Where("smart_view <> ?", LibraryViewStale).First(&row, id).Error; err != nil {
			return nil, err
		}
		return s.folder(kind, id, row.Name, "CollectionFolder", "movies"), nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (s *JellyfinServer) listItems(r *http.Request, q url.Values) ([]map[string]interface{}, int64, int, error) {
	if err := jellyfinValidateQuery(q); err != nil {
		return nil, 0, 0, err
	}
	start, limit, err := jellyfinPage(q)
	if err != nil {
		return nil, 0, 0, errJellyfinQuery
	}
	kind, id := jellyView, uint(1)
	if parent := jellyfinParam(q, "ParentId"); parent != "" {
		kind, id, err = jellyfinParseID(parent)
		if err != nil {
			return nil, 0, start, errJellyfinQuery
		}
		if _, err = s.folderByID(r, kind, id); err != nil {
			return nil, 0, start, err
		}
	}
	include := strings.ToLower(jellyfinParam(q, "IncludeItemTypes"))
	if include == "boxset" && jellyfinParam(q, "ParentId") == "" {
		kind, id = jellyGroup, 1
	}
	if kind == jellyGroup {
		return s.listFolders(r, q, id, start, limit)
	}
	if include != "" && !jellyfinCSVContains(include, "movie") && !jellyfinCSVContains(include, "video") {
		return []map[string]interface{}{}, 0, start, nil
	}
	filter := LibraryFilter{}
	switch kind {
	case jellyView:
		if id > uint(len(jellyfinViews)) {
			return nil, 0, start, gorm.ErrRecordNotFound
		}
		filter.SmartView = jellyfinViews[id-1].view
	case jellyTag:
		filter.TagIDs = []uint{id}
	case jellySaved:
		var view models.SavedLibraryView
		if err := database.DB.WithContext(r.Context()).First(&view, id).Error; err != nil {
			return nil, 0, start, err
		}
		filter = LibraryFilter{SearchMode: view.SearchMode, Keyword: view.Keyword, SmartView: view.SmartView, MinSize: view.MinSize, MaxSize: view.MaxSize, MinHeight: view.MinHeight, MaxHeight: view.MaxHeight, MinRating: view.MinRating, MaxRating: view.MaxRating, SortMode: view.SortMode}
		if err := json.Unmarshal([]byte(view.TagIDsJSON), &filter.TagIDs); err != nil {
			return nil, 0, start, err
		}
	case jellyCollection:
	default:
		return nil, 0, start, errJellyfinQuery
	}
	// HTTP queries consume the existing subtitle index; indexing remains desktop-owned.
	query, err := s.videoQuery(r, filter)
	if err != nil {
		return nil, 0, start, err
	}
	if kind == jellyCollection {
		query = query.Where("videos.id IN (?)", database.DB.Model(&models.CollectionVideo{}).Select("video_id").Where("collection_id = ?", id))
	}
	// Additional client filters intersect the saved view instead of replacing it.
	if search := jellyfinParam(q, "SearchTerm"); search != "" {
		query, err = applyLibraryFilter(query, LibraryFilter{Keyword: search}, time.Now())
		if err != nil {
			return nil, 0, start, err
		}
	}
	for _, entry := range []struct{ name, column string }{{"IsFavorite", "videos.is_favorite"}, {"IsPlayed", "videos.is_watched"}} {
		if value := jellyfinParam(q, entry.name); value != "" {
			b, err := strconv.ParseBool(value)
			if err != nil {
				return nil, 0, start, errJellyfinQuery
			}
			query = query.Where(entry.column+" = ?", b)
		}
	}
	if value := jellyfinParam(q, "IsResumable"); value != "" {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return nil, 0, start, errJellyfinQuery
		}
		if b {
			query = query.Where("videos.is_watched = ? AND videos.watch_position_seconds > 0", false)
		} else {
			query = query.Where("videos.is_watched = ? OR videos.watch_position_seconds <= 0", true)
		}
	}
	for _, f := range strings.Split(strings.ToLower(jellyfinParam(q, "Filters")), ",") {
		switch f {
		case "":
		case "isfavorite":
			query = query.Where("videos.is_favorite = ?", true)
		case "isplayed":
			query = query.Where("videos.is_watched = ?", true)
		case "isunplayed":
			query = query.Where("videos.is_watched = ?", false)
		case "isresumable":
			query = query.Where("videos.is_watched = ? AND videos.watch_position_seconds > 0", false)
		default:
			return nil, 0, start, errJellyfinQuery
		}
	}
	for _, name := range []string{"Tags", "Genres"} {
		if raw := jellyfinParam(q, name); raw != "" {
			for _, tag := range strings.Split(raw, "|") {
				query = query.Where("videos.id IN (?)", database.DB.Table("video_tags").Select("video_id").Joins("JOIN tags ON tags.id = video_tags.tag_id").Where("tags.deleted_at IS NULL AND tags.name = ?", tag))
			}
		}
	}
	if raw := jellyfinParam(q, "TagIds"); raw != "" {
		for _, rawID := range strings.Split(raw, ",") {
			k, tagID, err := jellyfinParseID(rawID)
			if err != nil || k != jellyTag {
				return nil, 0, start, errJellyfinQuery
			}
			query = query.Where("videos.id IN (?)", database.DB.Table("video_tags").Select("video_id").Where("tag_id = ?", tagID))
		}
	}
	for _, key := range []string{"Ids", "ExcludeItemIds"} {
		if raw := jellyfinParam(q, key); raw != "" {
			ids := []uint{}
			for _, rawID := range strings.Split(raw, ",") {
				k, id, err := jellyfinParseID(rawID)
				if err != nil || k != jellyVideo {
					return nil, 0, start, errJellyfinQuery
				}
				ids = append(ids, id)
			}
			if key == "Ids" {
				query = query.Where("videos.id IN ?", ids)
			} else {
				query = query.Where("videos.id NOT IN ?", ids)
			}
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, start, err
	}
	items := []map[string]interface{}{}
	query, err = s.orderVideos(query, q, filter, kind, id)
	if err != nil {
		return nil, 0, start, err
	}
	if limit == 0 {
		return items, total, start, nil
	}
	var videos []models.Video
	if err := query.Preload("Tags").Offset(start).Limit(limit).Find(&videos).Error; err != nil {
		return nil, 0, start, err
	}
	for _, video := range videos {
		dto, err := s.videoDTO(r, video)
		if err != nil {
			return nil, 0, start, err
		}
		items = append(items, dto)
	}
	return items, total, start, nil
}

func (s *JellyfinServer) videoQuery(r *http.Request, filter LibraryFilter) (*gorm.DB, error) {
	// Error SQL would otherwise interpolate private scan roots and search terms.
	query, err := applyLibraryFilter(database.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).WithContext(r.Context()).Model(&models.Video{}), filter, time.Now())
	if err != nil {
		return nil, err
	}
	// Empty configured roots are not permission to expose historical orphan rows.
	roots, err := loadScanRootScope()
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		query = query.Where("1 = 0")
	}
	return query.Where("videos.is_stale = ?", false), nil
}
func (s *JellyfinServer) visibleVideo(r *http.Request, id uint) (*models.Video, error) {
	query, err := s.videoQuery(r, LibraryFilter{})
	if err != nil {
		return nil, err
	}
	var video models.Video
	err = query.Preload("Tags").First(&video, id).Error
	return &video, err
}

func (s *JellyfinServer) orderVideos(query *gorm.DB, q url.Values, filter LibraryFilter, kind string, id uint) (*gorm.DB, error) {
	sortBy := jellyfinParam(q, "SortBy")
	direction := strings.ToLower(jellyfinParam(q, "SortOrder"))
	if direction != "" && direction != "ascending" && direction != "descending" {
		return nil, errJellyfinQuery
	}
	sqlDirection := " ASC"
	if direction == "descending" {
		sqlDirection = " DESC"
	}
	if sortBy != "" {
		columns := map[string]string{"sortname": "LOWER(CASE WHEN videos.display_title <> '' THEN videos.display_title ELSE videos.name END)", "name": "LOWER(videos.name)", "datecreated": "videos.created_at", "dateplayed": "videos.last_played_at", "playcount": "videos.play_count", "runtime": "videos.duration", "communityrating": "videos.personal_rating"}
		for _, sortKey := range strings.Split(strings.ToLower(sortBy), ",") {
			column, ok := columns[sortKey]
			if !ok {
				return nil, errJellyfinQuery
			}
			query = query.Order(column + sqlDirection)
		}
		return query.Order("videos.id ASC"), nil
	}
	if kind == jellyCollection {
		return query.Order(fmt.Sprintf("(SELECT position FROM collection_videos WHERE collection_id = %d AND video_id = videos.id) ASC", id)).Order("videos.id ASC"), nil
	}
	if filter.SortMode == LibrarySortRatingAsc || filter.SortMode == LibrarySortRatingDesc {
		direction := " ASC"
		if filter.SortMode == LibrarySortRatingDesc {
			direction = " DESC"
		}
		return query.Order("CASE WHEN videos.personal_rating IS NULL THEN 1 ELSE 0 END ASC").Order("videos.personal_rating" + direction).Order("videos.id DESC"), nil
	}
	if filter.SmartView == LibraryViewRecentlyAdded {
		return query.Order("videos.created_at DESC").Order("videos.id DESC"), nil
	}
	if filter.SmartView == LibraryViewRecentlyPlayed {
		return query.Order("videos.last_played_at DESC").Order("videos.id DESC"), nil
	}
	weight, err := s.video.getPlayWeight()
	if err != nil {
		return nil, err
	}
	return query.Order(scoreExprForTable("videos.", weight) + " ASC").Order("videos.size DESC").Order("videos.id DESC"), nil
}

func (s *JellyfinServer) listFolders(r *http.Request, q url.Values, group uint, start, limit int) ([]map[string]interface{}, int64, int, error) {
	items := []map[string]interface{}{}
	if sortBy := strings.ToLower(jellyfinParam(q, "SortBy")); sortBy != "" && sortBy != "sortname" && sortBy != "name" {
		return nil, 0, start, errJellyfinQuery
	}
	// Folder lists only support folder selection; reject video-only filters explicitly.
	for _, key := range []string{"IsFavorite", "IsPlayed", "IsResumable", "Filters", "Tags", "Genres", "TagIds", "Ids", "ExcludeItemIds"} {
		if jellyfinParam(q, key) != "" {
			return nil, 0, start, errJellyfinQuery
		}
	}
	switch group {
	case 1:
		var rows []models.MediaCollection
		if err := database.DB.WithContext(r.Context()).Order("LOWER(name), id").Find(&rows).Error; err != nil {
			return nil, 0, start, err
		}
		for _, row := range rows {
			items = append(items, s.folder(jellyCollection, row.ID, row.Name, "BoxSet", ""))
		}
	case 2:
		var rows []models.Tag
		if err := database.DB.WithContext(r.Context()).Order("LOWER(name), id").Find(&rows).Error; err != nil {
			return nil, 0, start, err
		}
		for _, row := range rows {
			items = append(items, s.folder(jellyTag, row.ID, row.Name, "Folder", ""))
		}
	default:
		return nil, 0, start, gorm.ErrRecordNotFound
	}
	search := strings.ToLower(jellyfinParam(q, "SearchTerm"))
	include := jellyfinParam(q, "IncludeItemTypes")
	filtered := items[:0]
	for _, item := range items {
		if include != "" && !jellyfinCSVContains(include, item["Type"].(string)) {
			continue
		}
		if strings.Contains(strings.ToLower(item["Name"].(string)), search) {
			filtered = append(filtered, item)
		}
	}
	items = filtered
	if order := jellyfinParam(q, "SortOrder"); strings.EqualFold(order, "Descending") {
		sort.SliceStable(items, func(i, j int) bool { return items[i]["Name"].(string) > items[j]["Name"].(string) })
	} else if order != "" && !strings.EqualFold(order, "Ascending") {
		return nil, 0, start, errJellyfinQuery
	}
	total := len(items)
	if start >= total || limit == 0 {
		return []map[string]interface{}{}, int64(total), start, nil
	}
	end := total
	if limit < total-start {
		end = start + limit
	}
	return items[start:end], int64(total), start, nil
}

func (s *JellyfinServer) videoDTO(r *http.Request, video models.Video) (map[string]interface{}, error) {
	s.mu.Lock()
	serverID := s.config.JellyfinServerID
	s.mu.Unlock()
	name := video.DisplayTitle
	if name == "" {
		name = video.Name
	}
	tags := []string{}
	for _, tag := range video.Tags {
		tags = append(tags, tag.Name)
	}
	item := map[string]interface{}{"Id": jellyfinID(jellyVideo, video.ID), "ServerId": serverID, "Name": name, "SortName": name, "OriginalTitle": video.OriginalTitle, "Type": "Movie", "MediaType": "Video", "IsFolder": false, "LocationType": "FileSystem", "RunTimeTicks": int64(video.Duration * 1e7), "Size": video.Size, "Overview": video.Description, "Tags": tags, "Genres": tags, "DateCreated": video.CreatedAt.UTC().Format(time.RFC3339), "UserData": jellyfinUserData(video), "ImageTags": map[string]string{"Primary": strconv.FormatInt(video.UpdatedAt.UnixNano(), 16)}, "PrimaryImageAspectRatio": 16.0 / 9.0}
	if video.PersonalRating != nil {
		item["CommunityRating"] = *video.PersonalRating
	}
	sources, err := s.mediaSources(r, video)
	if err != nil {
		return nil, err
	}
	item["MediaSources"] = sources
	return item, nil
}
func jellyfinUserData(video models.Video) map[string]interface{} {
	return map[string]interface{}{"Key": jellyfinID(jellyVideo, video.ID), "ItemId": jellyfinID(jellyVideo, video.ID), "IsFavorite": video.IsFavorite, "Played": video.IsWatched, "PlaybackPositionTicks": int64(video.WatchPositionSeconds * 1e7), "PlayCount": video.PlayCount}
}

func jellyfinCSVContains(list, value string) bool {
	for _, entry := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(entry), value) {
			return true
		}
	}
	return false
}

func jellyfinValidateQuery(q url.Values) error {
	// Metadata projection hints may be ignored because the DTO includes those fields.
	allowed := strings.Fields("parentid startindex limit sortby sortorder searchterm includeitemtypes recursive isfavorite isplayed isresumable filters tags genres tagids ids excludeitemids userid fields enableimages imagetypelimit enableimagetypes enableuserdata enabletotalrecordcount api_key apikey")
	seen := map[string]bool{}
	for key, values := range q {
		lower := strings.ToLower(key)
		if seen[lower] || len(values) != 1 {
			return errJellyfinQuery
		}
		seen[lower] = true
		found := false
		for _, candidate := range allowed {
			if lower == candidate {
				found = true
				break
			}
		}
		if !found {
			return errJellyfinQuery
		}
	}
	if value := jellyfinParam(q, "Recursive"); value != "" {
		if _, err := strconv.ParseBool(value); err != nil {
			return errJellyfinQuery
		}
	}
	return nil
}
