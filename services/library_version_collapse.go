package services

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"video-master/database"
	"video-master/models"
)

// 多版本聚合的片库查询部分（D-MW-VERSIONS）。只有 SearchLibraryVideoPage（均衡与评分排序）和
// CountLibraryVideos 读 LibraryFilter.CollapseVersions；其余文件级入口（随机、今晚看什么、语义、
// 最近播放、继续观看、字幕命中、按筛选批量操作、保存视图）一律不调用这里，等于忽略该字段。

// versionRepresentativeSQL 只给「在组里」的视频排组内名次：按成员顺序、再按视频 ID 取第一位。
// 不在任何组里的视频本来就是自己的代表，不进窗口，窗口只覆盖已分组且符合筛选的那一小部分。
const versionRepresentativeSQL = `videos.id, ROW_NUMBER() OVER (
	PARTITION BY vm.group_id
	ORDER BY vm.position, videos.id) AS version_rank`

// hasVersionMembers 是一次 LIMIT 1 探测：库里还没有任何版本组成员时，聚合等于原查询，整段子查询都省掉。
func hasVersionMembers(query *gorm.DB) (bool, error) {
	var ids []uint
	err := query.Session(&gorm.Session{NewDB: true}).Table("video_version_members").Limit(1).Pluck("video_id", &ids).Error
	if err != nil {
		return false, fmt.Errorf("读取版本组成员失败: %w", err)
	}
	return len(ids) > 0, nil
}

// applyVersionCollapse 在原有过滤之外加一个代表成员条件：每组在符合当前筛选的活跃成员里取顺序
// 最靠前的一个作代表，组内没有成员符合筛选时该组不出现。原有排序与游标沿用代表行，代码不动。
//
// query 必须是刚套完 applyLibraryFilter、还没加排序与游标的片库查询：窗口子查询直接从它分叉，
// 筛选条件与扫描范围只解析一次，内外两层的条件逐字相同。与合同里的整库窗口等价：
// 不在组里的视频在那条 SQL 里自成一组、名次恒为 1，这里直接放行。
func applyVersionCollapse(query *gorm.DB, filter LibraryFilter, _ time.Time) (*gorm.DB, error) {
	if !filter.CollapseVersions {
		return query, nil
	}
	grouped, err := hasVersionMembers(query)
	if err != nil || !grouped {
		return query, err
	}
	ranked := query.Session(&gorm.Session{}).Select(versionRepresentativeSQL).
		Joins("JOIN video_version_members vm ON vm.video_id = videos.id")
	// 分叉出的子查询只取 ID，不需要外层的 Preload("Tags")。
	ranked.Statement.Preloads = map[string][]interface{}{}
	representatives := query.Session(&gorm.Session{NewDB: true}).
		Table("(?) AS ranked", ranked).Select("ranked.id").Where("ranked.version_rank = 1")
	return query.Where(`(NOT EXISTS (SELECT 1 FROM video_version_members vmx WHERE vmx.video_id = videos.id)
		OR videos.id IN (?))`, representatives), nil
}

// loadPageVersionGroups 为本页代表一次批量读出所属组，只返回活跃成员 ≥2 的组；键为本页视频 ID。
func loadPageVersionGroups(db *gorm.DB, videoIDs []uint) (map[uint]VersionGroupSummary, error) {
	result := map[uint]VersionGroupSummary{}
	ids := uniqueUintIDs(videoIDs)
	if len(ids) == 0 {
		return result, nil
	}
	var rows []versionMemberRow
	err := db.Table("video_version_members AS vm").
		Select(versionMemberColumnsSQL+", g.title AS group_title, g.revision AS group_revision").
		Joins("JOIN video_version_groups g ON g.id = vm.group_id").
		Joins("JOIN videos v ON v.id = vm.video_id").
		Where("v.deleted_at IS NULL").
		Where("vm.group_id IN (SELECT pm.group_id FROM video_version_members pm WHERE pm.video_id IN ?)", ids).
		Order("vm.group_id ASC, vm.position ASC, vm.video_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("读取本页版本组失败: %w", err)
	}
	onPage := make(map[uint]bool, len(ids))
	for _, id := range ids {
		onPage[id] = true
	}
	var order []uint
	byGroup := map[uint][]versionMemberRow{}
	for _, row := range rows {
		if _, seen := byGroup[row.GroupID]; !seen {
			order = append(order, row.GroupID)
		}
		byGroup[row.GroupID] = append(byGroup[row.GroupID], row)
	}
	for _, groupID := range order {
		members := byGroup[groupID]
		header := models.VideoVersionGroup{ID: groupID, Title: members[0].GroupTitle, Revision: members[0].GroupRevision}
		summary, _ := summarizeVersionGroup(header, members)
		if summary.MemberCount < versionGroupMinMembers {
			continue
		}
		for _, member := range summary.Members {
			if onPage[member.VideoID] {
				result[member.VideoID] = summary
			}
		}
	}
	return result, nil
}

// attachPageVersionGroups 在聚合开关打开时给本页补上 version_groups；关闭时保持空映射，与旧结果一致。
func attachPageVersionGroups(page *LibraryVideoPage, filter LibraryFilter) (*LibraryVideoPage, error) {
	if page == nil || !filter.CollapseVersions {
		return page, nil
	}
	ids := make([]uint, 0, len(page.Videos))
	for _, video := range page.Videos {
		ids = append(ids, video.ID)
	}
	groups, err := loadPageVersionGroups(database.DB, ids)
	if err != nil {
		return nil, err
	}
	page.VersionGroups = groups
	return page, nil
}
