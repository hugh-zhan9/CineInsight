package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"video-master/database"
	"video-master/models"
)

// 多版本聚合（D-MW-VERSIONS，详见 docs/loopx/design/2026-10-10-media-workbench/版本分组合同.md）。
// 版本组只拥有组与成员关系；任何操作都不写成员视频的已看、进度、评分、收藏、标签、人物、
// 字幕或播放计数。所有成员变更先 CAS 组的 revision，再改成员，不用数据库锁。
const (
	versionGroupMinMembers     = 2
	versionGroupMaxMembers     = 20
	versionGroupTitleMaxRunes  = 200
	versionGroupLabelMaxRunes  = 40
	versionGroupPageDefault    = 50
	versionGroupPageMax        = 200
	versionSuggestionDefault   = 50
	versionSuggestionMax       = 200
	versionSuggestionMinRatio  = 0.9
	versionMemberConflictLabel = "version_member_conflict"
)

var (
	// ErrVersionGroupConflict：expected_revision 过期或组已解散，调用方重读后再操作。
	ErrVersionGroupConflict = errors.New("version_group_conflict: 版本组已被修改或解散，请刷新后重试")
	// ErrVersionMemberConflict 是 VersionMemberConflictError 的哨兵，供 errors.Is 判断。
	ErrVersionMemberConflict = errors.New(versionMemberConflictLabel)
	ErrVersionGroupNotFound  = errors.New("version_group_not_found: 版本组不存在或已解散")
	ErrVersionGroupInvalid   = errors.New("version_group_invalid")
)

func versionGroupInvalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrVersionGroupInvalid, reason)
}

// VersionMemberConflictError 列出已在某个版本组里的视频及这些组的 ID。文案以错误码开头、
// 末尾附一段 JSON（{"video_ids":[...],"group_ids":[...]}），前端据此决定能否提供「加入该组」。
type VersionMemberConflictError struct {
	VideoIDs []uint `json:"video_ids"`
	GroupIDs []uint `json:"group_ids"`
}

func (e *VersionMemberConflictError) Error() string {
	payload, _ := json.Marshal(e)
	return fmt.Sprintf("%s: %d 个视频已在版本组中 %s", versionMemberConflictLabel, len(e.VideoIDs), payload)
}

func (e *VersionMemberConflictError) Is(target error) bool { return target == ErrVersionMemberConflict }

// VersionGroupMember 是一个活跃成员（未软删除）的展示字段；只读汇总，不回写视频。
type VersionGroupMember struct {
	VideoID              uint     `json:"video_id"`
	Label                string   `json:"label"`
	Position             int      `json:"position"`
	DisplayTitle         string   `json:"display_title"`
	Name                 string   `json:"name"`
	Resolution           string   `json:"resolution"`
	Width                int      `json:"width"`
	Height               int      `json:"height"`
	Size                 int64    `json:"size"`
	Duration             float64  `json:"duration"`
	IsWatched            bool     `json:"is_watched"`
	WatchPositionSeconds float64  `json:"watch_position_seconds"`
	PersonalRating       *float64 `json:"personal_rating"`
	IsStale              bool     `json:"is_stale"`
}

// VersionGroupSummary 是片库页 version_groups 的值：活跃成员按顺序及其汇总。
// min_rating / max_rating 在没有任何评分时为 null，不报 0。
type VersionGroupSummary struct {
	GroupID      uint                 `json:"group_id"`
	Title        string               `json:"title"`
	Revision     int64                `json:"revision"`
	MemberCount  int                  `json:"member_count"`
	WatchedCount int                  `json:"watched_count"`
	RatedCount   int                  `json:"rated_count"`
	MinRating    *float64             `json:"min_rating"`
	MaxRating    *float64             `json:"max_rating"`
	Members      []VersionGroupMember `json:"members"`
}

// VersionGroupDetail 在汇总之外带上软删成员数（这些成员行保留，恢复后回到组内）。
type VersionGroupDetail struct {
	VersionGroupSummary
	DeletedMemberCount int       `json:"deleted_member_count"`
	CreatedAt          time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt          time.Time `json:"updated_at" ts_type:"string"`
}

// VersionGroupPage 是管理面板的分页结果：按 id 倒序，next_cursor_id 为本页最后一组的 id。
type VersionGroupPage struct {
	Items        []VersionGroupDetail `json:"items"`
	HasMore      bool                 `json:"has_more"`
	NextCursorID uint                 `json:"next_cursor_id"`
}

// VersionGroupSuggestion 是由已检测同源关系连出的候选组；只是建议，从不自动建组。
type VersionGroupSuggestion struct {
	VideoIDs         []uint               `json:"video_ids"`
	Members          []VersionGroupMember `json:"members"`
	LatestRelationAt time.Time            `json:"latest_relation_at" ts_type:"string"`
}

// VersionGroupService 拥有版本组与成员关系；不拥有任何视频字段。
type VersionGroupService struct{}

func NewVersionGroupService() *VersionGroupService { return &VersionGroupService{} }

// versionMemberRow 是成员与其视频的联合读取行；Deleted 表示视频已软删除。
type versionMemberRow struct {
	GroupID              uint
	VideoID              uint
	Label                string
	Position             int
	DisplayTitle         string
	Name                 string
	Resolution           string
	Width                int
	Height               int
	Size                 int64
	Duration             float64
	IsWatched            bool
	WatchPositionSeconds float64
	PersonalRating       *float64
	IsStale              bool
	Deleted              bool
	// 只有片库页的批量读取会带出组标题与版本号（g.title / g.revision）。
	GroupTitle    string
	GroupRevision int64
}

const versionMemberColumnsSQL = `vm.group_id, vm.video_id, vm.label, vm.position,
	COALESCE(v.display_title, '') AS display_title, COALESCE(v.name, '') AS name,
	COALESCE(v.resolution, '') AS resolution, COALESCE(v.width, 0) AS width, COALESCE(v.height, 0) AS height,
	COALESCE(v.size, 0) AS size, COALESCE(v.duration, 0) AS duration, v.is_watched,
	v.watch_position_seconds, v.personal_rating, COALESCE(v.is_stale, false) AS is_stale,
	CASE WHEN v.deleted_at IS NULL THEN 0 ELSE 1 END AS deleted`

// loadVersionMemberRows 一次读出这些组的全部成员行（含软删成员），按组、顺序排列。
func loadVersionMemberRows(db *gorm.DB, groupIDs []uint) ([]versionMemberRow, error) {
	rows := []versionMemberRow{}
	if len(groupIDs) == 0 {
		return rows, nil
	}
	err := db.Table("video_version_members AS vm").Select(versionMemberColumnsSQL).
		Joins("JOIN videos v ON v.id = vm.video_id").
		Where("vm.group_id IN ?", groupIDs).
		Order("vm.group_id ASC, vm.position ASC, vm.video_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("读取版本组成员失败: %w", err)
	}
	return rows, nil
}

func (row versionMemberRow) member() VersionGroupMember {
	member := VersionGroupMember{
		VideoID: row.VideoID, Label: row.Label, Position: row.Position,
		DisplayTitle: row.DisplayTitle, Name: row.Name, Resolution: row.Resolution,
		Width: row.Width, Height: row.Height, Size: row.Size, Duration: row.Duration,
		IsWatched: row.IsWatched, WatchPositionSeconds: row.WatchPositionSeconds, IsStale: row.IsStale,
	}
	if row.PersonalRating != nil {
		rating := *row.PersonalRating
		member.PersonalRating = &rating
	}
	return member
}

// summarizeVersionGroup 只汇总活跃成员（软删的不参与展示与汇总），返回软删成员数。
func summarizeVersionGroup(group models.VideoVersionGroup, rows []versionMemberRow) (VersionGroupSummary, int) {
	summary := VersionGroupSummary{GroupID: group.ID, Title: group.Title, Revision: group.Revision, Members: []VersionGroupMember{}}
	deleted := 0
	for _, row := range rows {
		if row.Deleted {
			deleted++
			continue
		}
		member := row.member()
		summary.Members = append(summary.Members, member)
		if member.IsWatched {
			summary.WatchedCount++
		}
		if rating := member.PersonalRating; rating != nil {
			summary.RatedCount++
			if summary.MinRating == nil || *rating < *summary.MinRating {
				value := *rating
				summary.MinRating = &value
			}
			if summary.MaxRating == nil || *rating > *summary.MaxRating {
				value := *rating
				summary.MaxRating = &value
			}
		}
	}
	summary.MemberCount = len(summary.Members)
	return summary, deleted
}

// loadVersionGroupDetails 批量读组与成员（两条查询），按组 ID 返回。不存在的组不出现在结果里。
func loadVersionGroupDetails(db *gorm.DB, groupIDs []uint) (map[uint]VersionGroupDetail, error) {
	details := map[uint]VersionGroupDetail{}
	ids := uniqueUintIDs(groupIDs)
	if len(ids) == 0 {
		return details, nil
	}
	var groups []models.VideoVersionGroup
	if err := db.Where("id IN ?", ids).Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("读取版本组失败: %w", err)
	}
	rows, err := loadVersionMemberRows(db, ids)
	if err != nil {
		return nil, err
	}
	byGroup := map[uint][]versionMemberRow{}
	for _, row := range rows {
		byGroup[row.GroupID] = append(byGroup[row.GroupID], row)
	}
	for _, group := range groups {
		summary, deleted := summarizeVersionGroup(group, byGroup[group.ID])
		details[group.ID] = VersionGroupDetail{
			VersionGroupSummary: summary, DeletedMemberCount: deleted,
			CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
		}
	}
	return details, nil
}

func loadVersionGroupDetail(db *gorm.DB, groupID uint) (*VersionGroupDetail, error) {
	details, err := loadVersionGroupDetails(db, []uint{groupID})
	if err != nil {
		return nil, err
	}
	detail, ok := details[groupID]
	if !ok {
		return nil, ErrVersionGroupNotFound
	}
	return &detail, nil
}

func normalizeVersionGroupTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if !utf8.ValidString(title) || utf8.RuneCountInString(title) > versionGroupTitleMaxRunes {
		return "", versionGroupInvalid(fmt.Sprintf("标题最多 %d 个字符", versionGroupTitleMaxRunes))
	}
	return title, nil
}

func normalizeVersionLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if !utf8.ValidString(label) || utf8.RuneCountInString(label) > versionGroupLabelMaxRunes {
		return "", versionGroupInvalid(fmt.Sprintf("版本标签最多 %d 个字符", versionGroupLabelMaxRunes))
	}
	return label, nil
}

// normalizeVersionVideoIDs 去重并保持传入顺序；含 0 视为无效请求。
func normalizeVersionVideoIDs(videoIDs []uint, minCount int) ([]uint, error) {
	for _, id := range videoIDs {
		if id == 0 {
			return nil, versionGroupInvalid("视频 ID 不能为空")
		}
	}
	ids := uniqueUintIDs(videoIDs)
	if len(ids) < minCount || len(ids) > versionGroupMaxMembers {
		return nil, versionGroupInvalid(fmt.Sprintf("版本组需要 %d–%d 个视频", versionGroupMinMembers, versionGroupMaxMembers))
	}
	return ids, nil
}

// bumpVersionGroupRevision 是每个成员变更的第一步：条件更新 revision+1，零行即冲突（含组已解散）。
func bumpVersionGroupRevision(tx *gorm.DB, groupID uint, expected int64, now time.Time) error {
	if groupID == 0 || expected <= 0 {
		return versionGroupInvalid("缺少版本组 ID 或版本号")
	}
	result := tx.Model(&models.VideoVersionGroup{}).
		Where("id = ? AND revision = ?", groupID, expected).
		Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		// 组已不存在（被解散或被清理）与 revision 过期分开报：前者重读也没用，界面应当按已解散处理。
		var count int64
		if err := tx.Model(&models.VideoVersionGroup{}).Where("id = ?", groupID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrVersionGroupNotFound
		}
		return ErrVersionGroupConflict
	}
	return nil
}

// cleanupVersionGroupsBeforeWrite 在独立事务里先清理不足两个成员的组并提交。写事务随后若因 CAS 冲突、
// 成员冲突或校验失败回滚，这次清理不会跟着回滚——否则只剩一条成员行的组会一直留着、一直冲突。
func cleanupVersionGroupsBeforeWrite(ctx context.Context) error {
	return database.TransactionWithContext(ctx, cleanupUndersizedVersionGroups)
}

// cleanupUndersizedVersionGroups 删除成员行总数（含软删成员）不足 2 的组；任何版本组写操作都顺带执行。
// 永久删除视频时成员行随外键级联消失，留下的单成员组就在下一次写操作里被清掉。
func cleanupUndersizedVersionGroups(tx *gorm.DB) error {
	kept := func() *gorm.DB {
		return tx.Session(&gorm.Session{NewDB: true}).Model(&models.VideoVersionMember{}).
			Select("group_id").Group("group_id").Having("COUNT(*) >= ?", versionGroupMinMembers)
	}
	if err := tx.Where("group_id NOT IN (?)", kept()).Delete(&models.VideoVersionMember{}).Error; err != nil {
		return fmt.Errorf("清理不足两个成员的版本组失败: %w", err)
	}
	if err := tx.Where("id NOT IN (?)", kept()).Delete(&models.VideoVersionGroup{}).Error; err != nil {
		return fmt.Errorf("清理不足两个成员的版本组失败: %w", err)
	}
	return nil
}

// requireActiveVideos 要求这些视频都存在且未软删除（GORM 的软删除条件自动生效）。
func requireActiveVideos(tx *gorm.DB, ids []uint) error {
	var count int64
	if err := tx.Model(&models.Video{}).Where("id IN ?", ids).Count(&count).Error; err != nil {
		return err
	}
	if int(count) != len(ids) {
		return versionGroupInvalid("有视频不存在或已删除")
	}
	return nil
}

// findVersionMemberConflict 返回这些视频里已在某个组中的那部分；都不在组里时返回 nil。
func findVersionMemberConflict(db *gorm.DB, ids []uint) (*VersionMemberConflictError, error) {
	var rows []models.VideoVersionMember
	if err := db.Select("video_id", "group_id").Where("video_id IN ?", ids).
		Order("video_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	conflict := &VersionMemberConflictError{VideoIDs: []uint{}, GroupIDs: []uint{}}
	groups := map[uint]bool{}
	for _, row := range rows {
		conflict.VideoIDs = append(conflict.VideoIDs, row.VideoID)
		if !groups[row.GroupID] {
			groups[row.GroupID] = true
			conflict.GroupIDs = append(conflict.GroupIDs, row.GroupID)
		}
	}
	sort.Slice(conflict.GroupIDs, func(i, j int) bool { return conflict.GroupIDs[i] < conflict.GroupIDs[j] })
	return conflict, nil
}

// versionMemberKeyViolation 识别并发加入时撞上成员主键（视频已被别的事务放进某个组）。
// SQLite 报列名，Postgres 报主键约束名；若全局打开 TranslateError 则是 gorm.ErrDuplicatedKey。
func versionMemberKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "video_version_members_pkey") ||
		strings.Contains(message, "UNIQUE constraint failed: video_version_members.video_id")
}

// mapVersionMemberRace 把并发撞主键翻译成带冲突明细的 version_member_conflict。
func mapVersionMemberRace(ctx context.Context, err error, ids []uint) error {
	if !versionMemberKeyViolation(err) {
		return err
	}
	var conflict *VersionMemberConflictError
	readErr := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var findErr error
		conflict, findErr = findVersionMemberConflict(db, ids)
		return findErr
	})
	if readErr != nil || conflict == nil {
		return &VersionMemberConflictError{VideoIDs: append([]uint{}, ids...), GroupIDs: []uint{}}
	}
	return conflict
}

// Create 用 2–20 个去重后的活跃视频建组，按传入顺序成为 1..N（第 1 个是主版本）。
func (s *VersionGroupService) Create(ctx context.Context, videoIDs []uint, title string) (*VersionGroupDetail, error) {
	ids, err := normalizeVersionVideoIDs(videoIDs, versionGroupMinMembers)
	if err != nil {
		return nil, err
	}
	if title, err = normalizeVersionGroupTitle(title); err != nil {
		return nil, err
	}
	if err := cleanupVersionGroupsBeforeWrite(ctx); err != nil {
		return nil, err
	}
	var detail *VersionGroupDetail
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if err := requireActiveVideos(tx, ids); err != nil {
			return err
		}
		conflict, err := findVersionMemberConflict(tx, ids)
		if err != nil {
			return err
		}
		if conflict != nil {
			return conflict
		}
		group := models.VideoVersionGroup{Title: title, Revision: 1}
		if err := tx.Create(&group).Error; err != nil {
			return err
		}
		members := make([]models.VideoVersionMember, 0, len(ids))
		for index, id := range ids {
			members = append(members, models.VideoVersionMember{VideoID: id, GroupID: group.ID, Position: index + 1})
		}
		if err := tx.Omit(clause.Associations).Create(&members).Error; err != nil {
			return err
		}
		detail, err = loadVersionGroupDetail(tx, group.ID)
		return err
	})
	if err != nil {
		return nil, mapVersionMemberRace(ctx, err, ids)
	}
	return detail, nil
}

// Get 返回组、全部活跃成员及软删成员数。
func (s *VersionGroupService) Get(ctx context.Context, groupID uint) (*VersionGroupDetail, error) {
	if groupID == 0 {
		return nil, versionGroupInvalid("缺少版本组 ID")
	}
	var detail *VersionGroupDetail
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var err error
		detail, err = loadVersionGroupDetail(db, groupID)
		return err
	})
	return detail, err
}

// List 给管理面板按 id 倒序分页；只列成员行总数 ≥2 的组（不足的等下一次写操作清理）。
func (s *VersionGroupService) List(ctx context.Context, cursorID uint, limit int) (*VersionGroupPage, error) {
	if limit <= 0 {
		limit = versionGroupPageDefault
	}
	if limit > versionGroupPageMax {
		limit = versionGroupPageMax
	}
	page := &VersionGroupPage{Items: []VersionGroupDetail{}}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		sized := db.Session(&gorm.Session{NewDB: true}).Model(&models.VideoVersionMember{}).
			Select("group_id").Group("group_id").Having("COUNT(*) >= ?", versionGroupMinMembers)
		query := db.Model(&models.VideoVersionGroup{}).Where("id IN (?)", sized)
		if cursorID > 0 {
			query = query.Where("id < ?", cursorID)
		}
		var ids []uint
		if err := query.Order("id DESC").Limit(limit+1).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) > limit {
			ids = ids[:limit]
			page.HasMore = true
		}
		details, err := loadVersionGroupDetails(db, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if detail, ok := details[id]; ok {
				page.Items = append(page.Items, detail)
			}
		}
		if page.HasMore && len(ids) > 0 {
			page.NextCursorID = ids[len(ids)-1]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// mutateVersionGroup 是成员变更的公共骨架：先在独立事务里清理不足两成员的组，再在一个事务内
// CAS revision、执行 change、顺带清理，最后回读组（组被解散时返回 nil）。
func (s *VersionGroupService) mutateVersionGroup(ctx context.Context, groupID uint, expected int64, change func(tx *gorm.DB) error) (*VersionGroupDetail, error) {
	if err := cleanupVersionGroupsBeforeWrite(ctx); err != nil {
		return nil, err
	}
	var detail *VersionGroupDetail
	err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if err := bumpVersionGroupRevision(tx, groupID, expected, time.Now()); err != nil {
			return err
		}
		if err := change(tx); err != nil {
			return err
		}
		if err := cleanupUndersizedVersionGroups(tx); err != nil {
			return err
		}
		loaded, err := loadVersionGroupDetail(tx, groupID)
		if errors.Is(err, ErrVersionGroupNotFound) {
			return nil
		}
		detail = loaded
		return err
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
}

// groupMemberRows 读组内全部成员行（含软删成员）及软删标记，按顺序排列。
func groupMemberRows(tx *gorm.DB, groupID uint) ([]versionMemberRow, error) {
	return loadVersionMemberRows(tx, []uint{groupID})
}

// AddMembers 把活跃视频追加到末尾；成员行总数（含软删成员）不超过 20。
func (s *VersionGroupService) AddMembers(ctx context.Context, groupID uint, expected int64, videoIDs []uint) (*VersionGroupDetail, error) {
	ids, err := normalizeVersionVideoIDs(videoIDs, 1)
	if err != nil {
		return nil, err
	}
	detail, err := s.mutateVersionGroup(ctx, groupID, expected, func(tx *gorm.DB) error {
		if err := requireActiveVideos(tx, ids); err != nil {
			return err
		}
		conflict, err := findVersionMemberConflict(tx, ids)
		if err != nil {
			return err
		}
		if conflict != nil {
			return conflict
		}
		rows, err := groupMemberRows(tx, groupID)
		if err != nil {
			return err
		}
		if len(rows)+len(ids) > versionGroupMaxMembers {
			return versionGroupInvalid(fmt.Sprintf("一个版本组最多 %d 个视频", versionGroupMaxMembers))
		}
		next := 1
		for _, row := range rows {
			if row.Position >= next {
				next = row.Position + 1
			}
		}
		members := make([]models.VideoVersionMember, 0, len(ids))
		for index, id := range ids {
			members = append(members, models.VideoVersionMember{VideoID: id, GroupID: groupID, Position: next + index})
		}
		return tx.Omit(clause.Associations).Create(&members).Error
	})
	if err != nil {
		return nil, mapVersionMemberRace(ctx, err, ids)
	}
	return detail, nil
}

// RemoveMember 只移出关系、不动文件；剩余成员行不足 2 时组随之解散（返回 nil）。
func (s *VersionGroupService) RemoveMember(ctx context.Context, groupID uint, expected int64, videoID uint) (*VersionGroupDetail, error) {
	if videoID == 0 {
		return nil, versionGroupInvalid("视频 ID 不能为空")
	}
	return s.mutateVersionGroup(ctx, groupID, expected, func(tx *gorm.DB) error {
		result := tx.Where("group_id = ? AND video_id = ?", groupID, videoID).Delete(&models.VideoVersionMember{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return versionGroupInvalid("这个视频不在该版本组里")
		}
		return nil
	})
}

// Reorder 接收当前全部活跃成员的一个排列；软删成员按原相对顺序排在其后。第 1 位即主版本。
func (s *VersionGroupService) Reorder(ctx context.Context, groupID uint, expected int64, videoIDs []uint) (*VersionGroupDetail, error) {
	return s.mutateVersionGroup(ctx, groupID, expected, func(tx *gorm.DB) error {
		rows, err := groupMemberRows(tx, groupID)
		if err != nil {
			return err
		}
		active := map[uint]bool{}
		var deleted []uint
		for _, row := range rows {
			if row.Deleted {
				deleted = append(deleted, row.VideoID)
			} else {
				active[row.VideoID] = true
			}
		}
		seen := map[uint]bool{}
		for _, id := range videoIDs {
			if !active[id] || seen[id] {
				return versionGroupInvalid("新顺序必须正好包含当前全部活跃成员")
			}
			seen[id] = true
		}
		if len(seen) != len(active) {
			return versionGroupInvalid("新顺序必须正好包含当前全部活跃成员")
		}
		for index, id := range append(append([]uint{}, videoIDs...), deleted...) {
			if err := tx.Model(&models.VideoVersionMember{}).Where("group_id = ? AND video_id = ?", groupID, id).
				Update("position", index+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Update 只改标题与成员标签；labels 的键必须是本组成员（含软删成员）。
func (s *VersionGroupService) Update(ctx context.Context, groupID uint, expected int64, title string, labels map[uint]string) (*VersionGroupDetail, error) {
	title, err := normalizeVersionGroupTitle(title)
	if err != nil {
		return nil, err
	}
	normalized := make(map[uint]string, len(labels))
	for videoID, label := range labels {
		if videoID == 0 {
			return nil, versionGroupInvalid("视频 ID 不能为空")
		}
		if normalized[videoID], err = normalizeVersionLabel(label); err != nil {
			return nil, err
		}
	}
	return s.mutateVersionGroup(ctx, groupID, expected, func(tx *gorm.DB) error {
		if err := tx.Model(&models.VideoVersionGroup{}).Where("id = ?", groupID).Update("title", title).Error; err != nil {
			return err
		}
		videoIDs := make([]uint, 0, len(normalized))
		for videoID := range normalized {
			videoIDs = append(videoIDs, videoID)
		}
		sort.Slice(videoIDs, func(i, j int) bool { return videoIDs[i] < videoIDs[j] })
		for _, videoID := range videoIDs {
			result := tx.Model(&models.VideoVersionMember{}).Where("group_id = ? AND video_id = ?", groupID, videoID).
				Update("label", normalized[videoID])
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return versionGroupInvalid("只能给本组成员设置标签")
			}
		}
		return nil
	})
}

// Dissolve 删除组与成员行，不动任何视频。
func (s *VersionGroupService) Dissolve(ctx context.Context, groupID uint, expected int64) error {
	_, err := s.mutateVersionGroup(ctx, groupID, expected, func(tx *gorm.DB) error {
		if err := tx.Where("group_id = ?", groupID).Delete(&models.VideoVersionMember{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", groupID).Delete(&models.VideoVersionGroup{}).Error
	})
	return err
}

// DismissSuggestion 记录这些视频两两之间的全部配对，之后它们不再相互建议。
func (s *VersionGroupService) DismissSuggestion(ctx context.Context, videoIDs []uint) error {
	ids, err := normalizeVersionVideoIDs(videoIDs, versionGroupMinMembers)
	if err != nil {
		return err
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	rows := make([]models.VideoVersionSuggestionDismissal, 0, len(ids)*(len(ids)-1)/2)
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			rows = append(rows, models.VideoVersionSuggestionDismissal{VideoLowID: ids[i], VideoHighID: ids[j]})
		}
	}
	if err := cleanupVersionGroupsBeforeWrite(ctx); err != nil {
		return err
	}
	return database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	})
}
