package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log"
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
	for key, users := range r.URL.Query() {
		if !strings.EqualFold(key, "UserId") {
			continue
		}
		for _, user := range users {
			if user != "" && strings.ReplaceAll(user, "-", "") != jellyfinUserID {
				jellyfinError(w, 403, "用户无效")
				return
			}
		}
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
	if strings.HasPrefix(path, "/sessions/playing") || strings.HasPrefix(path, "/videos/") || (len(parts) >= 3 && parts[0] == "items" && (parts[2] == "playbackinfo" || parts[2] == "images" || parts[2] == "download")) {
		s.servePlayback(w, r, parts)
		return
	}
	if len(parts) == 2 && parts[0] == "displaypreferences" {
		s.serveDisplayPreferences(w, r, parts[1])
		return
	}
	if len(parts) == 2 && parts[0] == "items" && r.Method == "DELETE" {
		s.deleteItem(w, r, parts[1])
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		jellyfinError(w, 405, "此接口不支持该方法")
		return
	}
	if path == "/search/hints" {
		s.searchHints(w, r)
		return
	}
	// Fileball's home and detail screens query these; this library has no series, studios or
	// exposed people, so they get Jellyfin's empty answers rather than a 404.
	if path == "/shows/nextup" || path == "/studios" || path == "/persons" || path == "/artists" || path == "/artists/albumartists" {
		jellyfinJSON(w, jellyfinResult([]map[string]interface{}{}, 0, 0))
		return
	}
	if path == "/movies/recommendations" {
		jellyfinJSON(w, []interface{}{})
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
			jellyfinSetParam(q, "SortBy", "DateCreated")
			jellyfinSetParam(q, "SortOrder", "Descending")
		}
		if path == "/items/resume" {
			jellyfinSetParam(q, "IsResumable", "true")
		}
		if path == "/genres" || path == "/tags" {
			// These endpoints enumerate tag folders; Recursive/IncludeItemTypes describe their contents.
			jellyfinSetParam(q, "ParentId", jellyfinID(jellyGroup, 2))
			jellyfinDeleteParam(q, "Recursive")
			jellyfinDeleteParam(q, "IncludeItemTypes")
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
	// Detail screens ask for extras this library does not model; Jellyfin answers these with
	// empty collections for any visible item, never 404, so the client keeps rendering the item.
	// Query parameters are not validated here: the answer is empty whatever they say, so there
	// is no wrong-scope result to guard against (the D-02 concern).
	if len(parts) == 3 && (parts[0] == "items" || parts[0] == "movies" || parts[0] == "shows") {
		switch parts[2] {
		case "specialfeatures", "localtrailers", "additionalparts", "themesongs", "themevideos", "similar", "intros":
			kind, id, err := jellyfinParseID(parts[1])
			if s.libraryError(w, err) {
				return
			}
			if kind == jellyVideo {
				_, err = s.visibleVideo(r, id)
			} else {
				_, err = s.folderByID(r, kind, id)
			}
			if s.libraryError(w, err) {
				return
			}
			if parts[2] == "similar" || parts[2] == "intros" {
				jellyfinJSON(w, jellyfinResult([]map[string]interface{}{}, 0, 0))
			} else {
				jellyfinJSON(w, []interface{}{})
			}
			return
		}
	}
	jellyfinError(w, 404, "不支持的 Jellyfin 接口")
}

// searchHints answers Jellyfin's Search/Hints with the same visible videos and folders the item
// list would return for the term; the hint shape is what search screens render.
func (s *JellyfinServer) searchHints(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	term := strings.TrimSpace(jellyfinParam(q, "SearchTerm"))
	if term == "" {
		jellyfinError(w, 400, "缺少搜索词")
		return
	}
	if raw := jellyfinParam(q, "IncludeMedia"); raw != "" {
		includeMedia, err := strconv.ParseBool(raw)
		if err != nil {
			jellyfinError(w, 400, "不支持或无效的查询参数")
			return
		}
		if !includeMedia {
			jellyfinJSON(w, map[string]interface{}{"SearchHints": []interface{}{}, "TotalRecordCount": 0})
			return
		}
	}
	jellyfinSetParam(q, "SearchTerm", term)
	jellyfinSetParam(q, "Recursive", "true")
	items, total, _, err := s.listItems(r, q)
	if s.libraryError(w, err) {
		return
	}
	hints := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		hint := map[string]interface{}{"Id": item["Id"], "ItemId": item["Id"], "Name": item["Name"], "MatchedTerm": term, "Type": item["Type"], "IsFolder": item["IsFolder"], "Artists": []interface{}{}, "PrimaryImageAspectRatio": item["PrimaryImageAspectRatio"]}
		if item["IsFolder"] == false {
			hint["MediaType"] = item["MediaType"]
			hint["RunTimeTicks"] = item["RunTimeTicks"]
			if tags, ok := item["ImageTags"].(map[string]string); ok {
				hint["PrimaryImageTag"] = tags["Primary"]
			}
		}
		hints = append(hints, hint)
	}
	jellyfinJSON(w, map[string]interface{}{"SearchHints": hints, "TotalRecordCount": total})
}

// deleteItem implements DELETE Items/{id} for videos (V1.0.3): the file moves to the library's
// trash folder through the same VideoService path the desktop and phone feed use, so it stays
// recoverable from the desktop trash view. Folders, views and tags cannot be deleted here.
func (s *JellyfinServer) deleteItem(w http.ResponseWriter, r *http.Request, raw string) {
	kind, id, err := jellyfinParseID(raw)
	if s.libraryError(w, err) {
		return
	}
	if kind != jellyVideo {
		jellyfinError(w, 403, "只能删除视频，视图、标签与作品集不可删除")
		return
	}
	// Not under s.writes: that lock keeps progress/favorite writes in arrival order, and moving a
	// multi-GB file to the trash hashes the whole file first. VideoService serializes deletions
	// itself (libraryPathMutationMu), so holding s.writes here would only stall other clients'
	// progress reports. The session is re-checked so a revoked token cannot delete.
	identity, _ := r.Context().Value(jellyfinIdentityKey{}).(jellyfinIdentity)
	if !s.authorized(identity) {
		jellyfinError(w, 401, "会话已失效")
		return
	}
	if _, err := s.visibleVideo(r, id); s.libraryError(w, err) {
		return
	}
	if err := s.video.DeleteVideo(id, true); err != nil {
		// The error text can carry the media path; the desktop trash view shows the recorded reason.
		log.Printf("[Jellyfin] 删除视频 %d 失败，详情见桌面回收站", id)
		jellyfinError(w, 500, "删除失败，请在桌面端查看回收站状态")
		return
	}
	w.WriteHeader(204)
}

// serveDisplayPreferences answers the per-client view settings Jellyfin stores for a user. The
// desktop app owns view settings, so GET returns Jellyfin's defaults and POST is acknowledged
// without being stored (D-02 V1.0.2).
func (s *JellyfinServer) serveDisplayPreferences(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case "GET":
		jellyfinJSON(w, map[string]interface{}{"Id": id, "SortBy": "SortName", "SortOrder": "Ascending", "RememberIndexing": false, "RememberSorting": false, "PrimaryImageHeight": 250, "PrimaryImageWidth": 250, "ScrollDirection": "Horizontal", "ShowBackdrop": true, "ShowSidebar": false, "CustomPrefs": map[string]string{}, "Client": jellyfinParam(r.URL.Query(), "client")})
	case "POST":
		var body map[string]interface{}
		if jellyfinDecode(w, r, &body) {
			w.WriteHeader(204)
		}
	default:
		jellyfinError(w, 405, "此接口不支持该方法")
	}
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
	q, err := jellyfinNormalizeQuery(q)
	if err != nil {
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
	recursive, _ := strconv.ParseBool(jellyfinParam(q, "Recursive"))
	wantsVideos := include == "" || jellyfinCSVContains(include, "movie") || jellyfinCSVContains(include, "video")
	// Group folders list their children unless the client asks for the leaf videos underneath.
	if kind == jellyGroup && !(recursive && wantsVideos) {
		return s.listFolders(r, q, id, start, limit)
	}
	if !wantsVideos || jellyfinScopeEmpty(q, false) {
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
	case jellyCollection, jellyGroup:
	default:
		return nil, 0, start, errJellyfinQuery
	}
	// HTTP queries consume the existing subtitle index; indexing remains desktop-owned.
	query, err := s.videoQuery(r, filter)
	if err != nil {
		return nil, 0, start, err
	}
	switch {
	case kind == jellyCollection:
		query = query.Where("videos.id IN (?)", database.DB.Model(&models.CollectionVideo{}).Select("video_id").Where("collection_id = ?", id))
	case kind == jellyGroup && id == 1:
		query = query.Where("videos.id IN (?)", database.DB.Model(&models.CollectionVideo{}).Select("video_id"))
	case kind == jellyGroup:
		query = query.Where("videos.id IN (?)", database.DB.Table("video_tags").Select("video_id").Joins("JOIN tags ON tags.id = video_tags.tag_id").Where("tags.deleted_at IS NULL"))
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
		switch strings.TrimSpace(f) {
		case "":
		case "isfavorite":
			query = query.Where("videos.is_favorite = ?", true)
		case "isplayed":
			query = query.Where("videos.is_watched = ?", true)
		case "isunplayed":
			query = query.Where("videos.is_watched = ?", false)
		case "isresumable":
			query = query.Where("videos.is_watched = ? AND videos.watch_position_seconds > 0", false)
		case "isnotfolder":
		case "isfolder":
			query = query.Where("1 = 0")
		default:
			return nil, 0, start, errJellyfinQuery
		}
	}
	// Every exposed leaf is a Movie, so the item-kind flags resolve to the whole list or nothing.
	for _, entry := range jellyfinKindFlags {
		if raw := jellyfinParam(q, entry.name); raw != "" {
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, 0, start, errJellyfinQuery
			}
			if b != entry.movie {
				query = query.Where("1 = 0")
			}
		}
	}
	// Alphabet-index parameters compare against the lower-cased sort name, like Jellyfin's SortName.
	if raw := jellyfinParam(q, "NameStartsWith"); raw != "" {
		query = query.Where(jellyfinSortNameExpr+" LIKE ? ESCAPE '\\'", strings.ToLower(escapeSQLLike(raw))+"%")
	}
	if raw := jellyfinParam(q, "NameStartsWithOrGreater"); raw != "" {
		query = query.Where(jellyfinSortNameExpr+" >= ?", strings.ToLower(raw))
	}
	if raw := jellyfinParam(q, "NameLessThan"); raw != "" {
		query = query.Where(jellyfinSortNameExpr+" < ?", strings.ToLower(raw))
	}
	// Several tag/genre names select the union, as Jellyfin's GetWhereClauses does.
	for _, name := range []string{"Tags", "Genres"} {
		if raw := jellyfinParam(q, name); raw != "" {
			query = query.Where("videos.id IN (?)", database.DB.Table("video_tags").Select("video_id").Joins("JOIN tags ON tags.id = video_tags.tag_id").Where("tags.deleted_at IS NULL AND tags.name IN ?", strings.Split(raw, "|")))
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
	orders, err := jellyfinSortOrders(q)
	if err != nil {
		return nil, err
	}
	if sortBy := jellyfinParam(q, "SortBy"); sortBy != "" {
		// Keys with no counterpart in this model (ProductionYear, IsFolder, …) are ignored per D-02;
		// every accepted key maps through this table, so client text never reaches the SQL.
		columns := map[string]string{"sortname": jellyfinSortNameExpr, "name": "LOWER(videos.name)", "datecreated": "videos.created_at", "datelastcontentadded": "videos.created_at", "dateplayed": "videos.last_played_at", "playcount": "videos.play_count", "runtime": "videos.duration", "communityrating": "videos.personal_rating", "random": "RANDOM()"}
		ordered := false
		for i, sortKey := range strings.Split(strings.ToLower(sortBy), ",") {
			column, ok := columns[strings.TrimSpace(sortKey)]
			if !ok {
				continue
			}
			ordered = true
			if column == "RANDOM()" {
				query = query.Order(column)
				continue
			}
			query = query.Order(column + jellyfinSortDirection(orders, i))
		}
		if ordered {
			return query.Order("videos.id ASC"), nil
		}
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
	orders, err := jellyfinSortOrders(q)
	if err != nil {
		return nil, 0, start, err
	}
	// Folders sort by name only; the SortOrder paired with the name key decides the direction.
	// Without SortBy, SortOrder is a no-op, as in Jellyfin's GetOrderBy.
	nameIndex := -1
	for i, key := range strings.Split(strings.ToLower(jellyfinParam(q, "SortBy")), ",") {
		if key = strings.TrimSpace(key); key == "sortname" || key == "name" {
			nameIndex = i
			break
		}
	}
	// Tag and video ID selections do not apply to folders; reject them explicitly.
	for _, key := range []string{"Tags", "Genres", "TagIds", "Ids", "ExcludeItemIds"} {
		if jellyfinParam(q, key) != "" {
			return nil, 0, start, errJellyfinQuery
		}
	}
	// Folder DTOs always report IsFavorite=false and Played=false, so these selections resolve
	// deterministically to the empty set or the whole list.
	for _, key := range []string{"IsFavorite", "IsPlayed", "IsResumable"} {
		if raw := jellyfinParam(q, key); raw != "" {
			selected, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, 0, start, errJellyfinQuery
			}
			if selected {
				return items, 0, start, nil
			}
		}
	}
	for _, f := range strings.Split(strings.ToLower(jellyfinParam(q, "Filters")), ",") {
		switch strings.TrimSpace(f) {
		case "", "isfolder", "isunplayed":
		case "isnotfolder", "isfavorite", "isplayed", "isresumable":
			return items, 0, start, nil
		default:
			return nil, 0, start, errJellyfinQuery
		}
	}
	// Folders are neither movies nor series, so any item-kind flag set to true empties the list.
	for _, entry := range jellyfinKindFlags {
		if raw := jellyfinParam(q, entry.name); raw != "" {
			selected, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, 0, start, errJellyfinQuery
			}
			if selected {
				return items, 0, start, nil
			}
		}
	}
	if jellyfinScopeEmpty(q, true) {
		return items, 0, start, nil
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
	startsWith, orGreater, lessThan := strings.ToLower(jellyfinParam(q, "NameStartsWith")), strings.ToLower(jellyfinParam(q, "NameStartsWithOrGreater")), strings.ToLower(jellyfinParam(q, "NameLessThan"))
	include, exclude := jellyfinParam(q, "IncludeItemTypes"), jellyfinParam(q, "ExcludeItemTypes")
	filtered := items[:0]
	for _, item := range items {
		itemType := item["Type"].(string)
		if (include != "" && !jellyfinCSVContains(include, itemType)) || jellyfinCSVContains(exclude, itemType) {
			continue
		}
		name := strings.ToLower(item["Name"].(string))
		if !strings.Contains(name, search) || !strings.HasPrefix(name, startsWith) || (orGreater != "" && name < orGreater) || (lessThan != "" && name >= lessThan) {
			continue
		}
		filtered = append(filtered, item)
	}
	items = filtered
	// Both directions use one comparator here, so DESC is the exact inverse of ASC regardless of
	// the database collation; fixed-width hex IDs compare like the integers.
	descending := nameIndex >= 0 && jellyfinSortDirection(orders, nameIndex) == " DESC"
	sort.SliceStable(items, func(i, j int) bool {
		a, b := strings.ToLower(items[i]["Name"].(string)), strings.ToLower(items[j]["Name"].(string))
		if a == b {
			a, b = items[i]["Id"].(string), items[j]["Id"].(string)
		}
		if descending {
			return a > b
		}
		return a < b
	})
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
	// CanDelete/CanDownload are what clients gate their delete and download actions on; deletion
	// moves the file to the library trash (V1.0.3), the same path the desktop and phone feed use.
	item := map[string]interface{}{"Id": jellyfinID(jellyVideo, video.ID), "ServerId": serverID, "Name": name, "SortName": name, "OriginalTitle": video.OriginalTitle, "Type": "Movie", "MediaType": "Video", "VideoType": "VideoFile", "IsFolder": false, "LocationType": "FileSystem", "PlayAccess": "Full", "CanDelete": true, "CanDownload": true, "RunTimeTicks": int64(video.Duration * 1e7), "Size": video.Size, "Overview": video.Description, "Tags": tags, "Genres": tags, "DateCreated": video.CreatedAt.UTC().Format(time.RFC3339), "UserData": jellyfinUserData(video), "ImageTags": map[string]string{"Primary": strconv.FormatInt(video.UpdatedAt.UnixNano(), 16)}, "BackdropImageTags": []string{}, "PrimaryImageAspectRatio": 16.0 / 9.0, "People": []interface{}{}, "Studios": []interface{}{}, "Chapters": []interface{}{}, "Taglines": []interface{}{}, "ExternalUrls": []interface{}{}, "ProviderIds": map[string]string{}, "MediaSourceCount": 1}
	if video.PersonalRating != nil {
		item["CommunityRating"] = *video.PersonalRating
	}
	sources, summary, err := s.mediaSources(r, video)
	if err != nil {
		return nil, err
	}
	// Jellyfin repeats the stream list and its derived facts at the top level; Fileball's detail
	// screen reads MediaStreams from the item, not from MediaSources.
	item["MediaSources"] = sources
	item["MediaStreams"] = summary.streams
	item["Container"] = sources[0]["Container"]
	item["HasSubtitles"] = summary.hasSubtitles
	if summary.width > 0 && summary.height > 0 {
		item["Width"], item["Height"] = summary.width, summary.height
		item["AspectRatio"] = jellyfinAspectRatio(summary.width, summary.height)
		// Jellyfin's IsHD is Height >= 720; the short side plays that role so portrait clips agree.
		short := summary.width
		if summary.height < short {
			short = summary.height
		}
		item["IsHD"] = short >= 720
	}
	return item, nil
}
func jellyfinUserData(video models.Video) map[string]interface{} {
	data := map[string]interface{}{"Key": jellyfinID(jellyVideo, video.ID), "ItemId": jellyfinID(jellyVideo, video.ID), "IsFavorite": video.IsFavorite, "Played": video.IsWatched, "PlaybackPositionTicks": int64(video.WatchPositionSeconds * 1e7), "PlayCount": video.PlayCount}
	if video.LastPlayedAt != nil {
		data["LastPlayedDate"] = video.LastPlayedAt.UTC().Format(time.RFC3339)
	}
	return data
}

func jellyfinCSVContains(list, value string) bool {
	for _, entry := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(entry), value) {
			return true
		}
	}
	return false
}

// Projection hints only shape the DTO, which already carries every field; their values are ignored.
// The Search/Hints Include* switches are hints too: this library has no people, genres, studios or
// artists to offer, and IncludeMedia is evaluated by searchHints before the list runs.
var jellyfinHintParams = map[string]bool{"fields": true, "enableimages": true, "imagetypelimit": true, "enableimagetypes": true, "enableuserdata": true, "enabletotalrecordcount": true, "groupitems": true, "api_key": true, "apikey": true, "userid": true, "includepeople": true, "includemedia": true, "includegenres": true, "includestudios": true, "includeartists": true}

// Semantic parameters are implemented by listItems/listFolders; anything else stays a 400 (D-02).
var jellyfinSemanticParams = map[string]bool{"parentid": true, "startindex": true, "limit": true, "sortby": true, "sortorder": true, "searchterm": true, "includeitemtypes": true, "excludeitemtypes": true, "recursive": true, "isfavorite": true, "isplayed": true, "isresumable": true, "filters": true, "tags": true, "genres": true, "tagids": true, "ids": true, "excludeitemids": true, "mediatypes": true, "excludelocationtypes": true, "locationtypes": true, "ismissing": true, "collapseboxsetitems": true, "ismovie": true, "isseries": true, "isnews": true, "iskids": true, "issports": true, "namestartswith": true, "namestartswithorgreater": true, "namelessthan": true}

// jellyfinKindFlags are Jellyfin's item-kind switches; only IsMovie describes this library's leaves.
var jellyfinKindFlags = []struct {
	name  string
	movie bool
}{{"IsMovie", true}, {"IsSeries", false}, {"IsNews", false}, {"IsKids", false}, {"IsSports", false}}

// jellyfinSortNameExpr is the SQL for Jellyfin's SortName: the user title when set, else the file name.
const jellyfinSortNameExpr = "LOWER(CASE WHEN videos.display_title <> '' THEN videos.display_title ELSE videos.name END)"

// List parameters may arrive as one delimited value or as repeated keys, like Jellyfin's
// CommaDelimitedCollectionModelBinder accepts; the separator matches how each one is parsed.
var jellyfinListParams = map[string]string{"includeitemtypes": ",", "excludeitemtypes": ",", "sortby": ",", "sortorder": ",", "filters": ",", "mediatypes": ",", "excludelocationtypes": ",", "locationtypes": ",", "tagids": ",", "ids": ",", "excludeitemids": ",", "fields": ",", "enableimagetypes": ",", "tags": "|", "genres": "|"}

// jellyfinNormalizeQuery folds keys to lower case, merges repeated list parameters and rejects
// unknown parameters or scalar parameters given twice with different values.
func jellyfinNormalizeQuery(q url.Values) (url.Values, error) {
	// Keys are visited in sorted order so case variants of one list parameter merge deterministically.
	keys := make([]string, 0, len(q))
	for key := range q {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	merged := url.Values{}
	for _, key := range keys {
		lower := strings.ToLower(key)
		if !jellyfinHintParams[lower] && !jellyfinSemanticParams[lower] {
			return nil, errJellyfinQuery
		}
		merged[lower] = append(merged[lower], q[key]...)
	}
	for key, values := range merged {
		if separator, ok := jellyfinListParams[key]; ok {
			merged[key] = []string{strings.Join(values, separator)}
			continue
		}
		for _, value := range values[1:] {
			if value != values[0] {
				return nil, errJellyfinQuery
			}
		}
		merged[key] = values[:1]
	}
	for _, name := range []string{"recursive", "ismissing", "collapseboxsetitems"} {
		if value := merged.Get(name); value != "" {
			if _, err := strconv.ParseBool(value); err != nil {
				return nil, errJellyfinQuery
			}
		}
	}
	return merged, nil
}

func jellyfinDeleteParam(q url.Values, name string) {
	for key := range q {
		if strings.EqualFold(key, name) {
			delete(q, key)
		}
	}
}

// jellyfinSetParam replaces every case variant of a parameter with one server-side value.
func jellyfinSetParam(q url.Values, name, value string) {
	jellyfinDeleteParam(q, name)
	q.Set(name, value)
}

// jellyfinScopeEmpty evaluates the Jellyfin scope parameters this model can only satisfy
// trivially: every exposed item is a present FileSystem video, and folders have no MediaType.
func jellyfinScopeEmpty(q url.Values, folders bool) bool {
	if raw := jellyfinParam(q, "MediaTypes"); raw != "" && (folders || !jellyfinCSVContains(raw, "Video")) {
		return true
	}
	if raw := jellyfinParam(q, "LocationTypes"); raw != "" && !jellyfinCSVContains(raw, "FileSystem") {
		return true
	}
	if jellyfinCSVContains(jellyfinParam(q, "ExcludeLocationTypes"), "FileSystem") {
		return true
	}
	if missing, _ := strconv.ParseBool(jellyfinParam(q, "IsMissing")); missing {
		return true
	}
	if exclude := jellyfinParam(q, "ExcludeItemTypes"); !folders && (jellyfinCSVContains(exclude, "Movie") || jellyfinCSVContains(exclude, "Video")) {
		return true
	}
	return false
}

// jellyfinSortOrders validates SortOrder; entries pair with SortBy keys by position.
func jellyfinSortOrders(q url.Values) ([]string, error) {
	raw := jellyfinParam(q, "SortOrder")
	if raw == "" {
		return nil, nil
	}
	orders := strings.Split(strings.ToLower(raw), ",")
	for i, order := range orders {
		orders[i] = strings.TrimSpace(order)
		if orders[i] != "ascending" && orders[i] != "descending" {
			return nil, errJellyfinQuery
		}
	}
	return orders, nil
}

// jellyfinSortDirection mirrors Jellyfin's RequestHelpers.GetOrderBy: the order at the same
// position, else the first order, else ascending.
func jellyfinSortDirection(orders []string, index int) string {
	order := "ascending"
	if index < len(orders) {
		order = orders[index]
	} else if len(orders) > 0 {
		order = orders[0]
	}
	if order == "descending" {
		return " DESC"
	}
	return " ASC"
}
