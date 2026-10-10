package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"video-master/database"
	"video-master/models"
)

// TC-16 / AC-07：版本组服务、片库聚合查询与建议。夹具不配置扫描根，片库查询不做根裁剪。

func seedVersionVideo(t *testing.T, name string, mutate func(*models.Video)) models.Video {
	t.Helper()
	video := models.Video{
		Name: name + ".mp4", Path: "/library/" + name + ".mp4", Directory: "/library",
		Size: 1000, Duration: 600, Width: 1280, Height: 720, Resolution: "1280x720",
	}
	if mutate != nil {
		mutate(&video)
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("建视频 %s 失败: %v", name, err)
	}
	return video
}

func versionIDs(videos ...models.Video) []uint {
	ids := make([]uint, 0, len(videos))
	for _, video := range videos {
		ids = append(ids, video.ID)
	}
	return ids
}

func memberIDs(members []VersionGroupMember) []uint {
	ids := make([]uint, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.VideoID)
	}
	return ids
}

func requireUintSlice(t *testing.T, label string, got, want []uint) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: got %v want %v", label, got, want)
	}
}

func countVersionRows(t *testing.T, model any, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Model(model).Where(query, args...).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func TestVersionGroupCreateValidatesAndOrdersMembers(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	a := seedVersionVideo(t, "a", nil)
	b := seedVersionVideo(t, "b", nil)
	c := seedVersionVideo(t, "c", nil)
	gone := seedVersionVideo(t, "gone", nil)
	if err := database.DB.Delete(&gone).Error; err != nil {
		t.Fatal(err)
	}

	for name, ids := range map[string][]uint{
		"single": {a.ID}, "duplicate-only": {a.ID, a.ID}, "zero": {a.ID, 0}, "deleted": {a.ID, gone.ID},
	} {
		if _, err := service.Create(ctx, ids, ""); !errors.Is(err, ErrVersionGroupInvalid) {
			t.Fatalf("%s: 期望 version_group_invalid，得到 %v", name, err)
		}
	}
	tooMany := make([]uint, 0, 21)
	for i := 0; i < 21; i++ {
		tooMany = append(tooMany, seedVersionVideo(t, fmt.Sprintf("many-%d", i), nil).ID)
	}
	if _, err := service.Create(ctx, tooMany, ""); !errors.Is(err, ErrVersionGroupInvalid) {
		t.Fatalf("21 个视频应被拒绝: %v", err)
	}
	if _, err := service.Create(ctx, []uint{a.ID, b.ID}, strings.Repeat("长", 201)); !errors.Is(err, ErrVersionGroupInvalid) {
		t.Fatalf("超长标题应被拒绝: %v", err)
	}

	detail, err := service.Create(ctx, []uint{c.ID, a.ID, c.ID, b.ID}, "  电影  ")
	if err != nil {
		t.Fatal(err)
	}
	requireUintSlice(t, "成员按传入顺序（去重）", memberIDs(detail.Members), []uint{c.ID, a.ID, b.ID})
	if detail.Title != "电影" || detail.Revision != 1 || detail.MemberCount != 3 || detail.DeletedMemberCount != 0 {
		t.Fatalf("组详情不对: %+v", detail)
	}
	for index, member := range detail.Members {
		if member.Position != index+1 || member.Label != "" {
			t.Fatalf("位置或标签不对: %+v", member)
		}
	}
	if countVersionRows(t, &models.VideoVersionGroup{}, "1 = 1") != 1 {
		t.Fatal("失败的创建不应留下组")
	}
}

func TestVersionGroupMemberUniquenessConflictListsGroups(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	a, b, c, d := seedVersionVideo(t, "a", nil), seedVersionVideo(t, "b", nil), seedVersionVideo(t, "c", nil), seedVersionVideo(t, "d", nil)
	first, err := service.Create(ctx, versionIDs(a, b), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(ctx, versionIDs(c, b, d), "")
	var conflict *VersionMemberConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, ErrVersionMemberConflict) {
		t.Fatalf("期望 version_member_conflict，得到 %v", err)
	}
	requireUintSlice(t, "冲突视频", conflict.VideoIDs, []uint{b.ID})
	requireUintSlice(t, "冲突组", conflict.GroupIDs, []uint{first.GroupID})
	// 文案以错误码开头，末尾 JSON 供前端解析「加入该组」。
	message := err.Error()
	if !strings.HasPrefix(message, "version_member_conflict: ") {
		t.Fatalf("错误码前缀不对: %q", message)
	}
	var payload struct {
		VideoIDs []uint `json:"video_ids"`
		GroupIDs []uint `json:"group_ids"`
	}
	if err := json.Unmarshal([]byte(message[strings.Index(message, "{"):]), &payload); err != nil {
		t.Fatalf("冲突明细不是 JSON: %q %v", message, err)
	}
	requireUintSlice(t, "JSON 冲突组", payload.GroupIDs, []uint{first.GroupID})
	if countVersionRows(t, &models.VideoVersionMember{}, "video_id IN ?", versionIDs(c, d)) != 0 {
		t.Fatal("冲突时不应写入任何成员")
	}

	// 加入已在本组的视频同样是成员冲突；CAS 已在事务里回滚，revision 不变。
	_, err = service.AddMembers(ctx, first.GroupID, first.Revision, versionIDs(a, c))
	if !errors.Is(err, ErrVersionMemberConflict) {
		t.Fatalf("加入已在组内的视频应冲突: %v", err)
	}
	reread, err := service.Get(ctx, first.GroupID)
	if err != nil || reread.Revision != first.Revision {
		t.Fatalf("冲突不应推进 revision: %+v %v", reread, err)
	}
	// 主键是视频 ID：即使绕过服务直接插入，同一视频也进不了第二个组。
	other := models.VideoVersionGroup{Title: "x", Revision: 1}
	if err := database.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	err = database.DB.Create(&models.VideoVersionMember{VideoID: a.ID, GroupID: other.ID, Position: 1}).Error
	if !versionMemberKeyViolation(err) {
		t.Fatalf("成员主键应拒绝第二个组: %v", err)
	}
}

func TestVersionGroupRevisionCompareAndSwap(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	a, b, c := seedVersionVideo(t, "a", nil), seedVersionVideo(t, "b", nil), seedVersionVideo(t, "c", nil)
	group, err := service.Create(ctx, versionIDs(a, b), "")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Update(ctx, group.GroupID, group.Revision, "新标题", map[uint]string{a.ID: " 原版 ", b.ID: "高清版"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != group.Revision+1 || updated.Title != "新标题" || updated.Members[0].Label != "原版" || updated.Members[1].Label != "高清版" {
		t.Fatalf("更新结果不对: %+v", updated)
	}
	stale := []func() error{
		func() error { _, err := service.Update(ctx, group.GroupID, group.Revision, "旧", nil); return err },
		func() error {
			_, err := service.AddMembers(ctx, group.GroupID, group.Revision, []uint{c.ID})
			return err
		},
		func() error { _, err := service.RemoveMember(ctx, group.GroupID, group.Revision, a.ID); return err },
		func() error {
			_, err := service.Reorder(ctx, group.GroupID, group.Revision, versionIDs(b, a))
			return err
		},
		func() error { return service.Dissolve(ctx, group.GroupID, group.Revision) },
	}
	for index, call := range stale {
		if err := call(); !errors.Is(err, ErrVersionGroupConflict) {
			t.Fatalf("过期 revision 的写操作 %d 应返回 version_group_conflict: %v", index, err)
		}
	}
	after, err := service.Get(ctx, group.GroupID)
	if err != nil || after.Revision != updated.Revision || after.Title != "新标题" || after.MemberCount != 2 {
		t.Fatalf("冲突的写操作不应留下任何改动: %+v %v", after, err)
	}
	if _, err := service.Update(ctx, group.GroupID, updated.Revision, "", map[uint]string{c.ID: "x"}); !errors.Is(err, ErrVersionGroupInvalid) {
		t.Fatalf("给非成员设标签应被拒绝: %v", err)
	}
	if _, err := service.Update(ctx, group.GroupID, updated.Revision, "", map[uint]string{a.ID: strings.Repeat("签", 41)}); !errors.Is(err, ErrVersionGroupInvalid) {
		t.Fatalf("超长标签应被拒绝: %v", err)
	}
	if final, _ := service.Get(ctx, group.GroupID); final.Revision != updated.Revision {
		t.Fatalf("被拒绝的更新不应推进 revision: %+v", final)
	}
}

func TestVersionGroupReorderRemoveAndDissolve(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	a, b, c, d := seedVersionVideo(t, "a", nil), seedVersionVideo(t, "b", nil), seedVersionVideo(t, "c", nil), seedVersionVideo(t, "d", nil)
	group, err := service.Create(ctx, versionIDs(a, b, c, d), "")
	if err != nil {
		t.Fatal(err)
	}
	// 软删 b：重排只接受活跃成员的排列，软删成员按原相对顺序附在后面。
	if err := database.DB.Delete(&b).Error; err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]uint{versionIDs(c, a), versionIDs(c, a, d, b), versionIDs(c, a, a), versionIDs(c, a, d, d)} {
		if _, err := service.Reorder(ctx, group.GroupID, group.Revision, bad); !errors.Is(err, ErrVersionGroupInvalid) {
			t.Fatalf("非排列 %v 应被拒绝: %v", bad, err)
		}
	}
	reordered, err := service.Reorder(ctx, group.GroupID, group.Revision, versionIDs(d, a, c))
	if err != nil {
		t.Fatal(err)
	}
	requireUintSlice(t, "重排后的活跃成员", memberIDs(reordered.Members), versionIDs(d, a, c))
	var deletedMember models.VideoVersionMember
	if err := database.DB.First(&deletedMember, "video_id = ?", b.ID).Error; err != nil || deletedMember.Position != 4 {
		t.Fatalf("软删成员应排在最后: %+v %v", deletedMember, err)
	}
	if reordered.DeletedMemberCount != 1 || reordered.MemberCount != 3 {
		t.Fatalf("软删成员计数不对: %+v", reordered)
	}

	afterRemove, err := service.RemoveMember(ctx, group.GroupID, reordered.Revision, d.ID)
	if err != nil || afterRemove == nil {
		t.Fatalf("移出成员失败: %+v %v", afterRemove, err)
	}
	requireUintSlice(t, "移出主版本后次位成为主版本", memberIDs(afterRemove.Members), versionIDs(a, c))
	if _, err := service.RemoveMember(ctx, group.GroupID, afterRemove.Revision, d.ID); !errors.Is(err, ErrVersionGroupInvalid) {
		t.Fatalf("移出非成员应被拒绝: %v", err)
	}
	if countVersionRows(t, &models.Video{}, "id = ?", d.ID) != 1 {
		t.Fatal("移出成员不应删除视频")
	}
	// 剩 a、c 两个活跃成员加软删的 b：再移出 a 后成员行还有 2 条（c 与 b），组保留。
	afterSecond, err := service.RemoveMember(ctx, group.GroupID, afterRemove.Revision, a.ID)
	if err != nil || afterSecond == nil || afterSecond.MemberCount != 1 || afterSecond.DeletedMemberCount != 1 {
		t.Fatalf("含软删成员的组应保留: %+v %v", afterSecond, err)
	}
	// 再移出 c：成员行只剩 1 条，组随之解散，返回 nil，剩下的成员行一并删除。
	dissolved, err := service.RemoveMember(ctx, group.GroupID, afterSecond.Revision, c.ID)
	if err != nil || dissolved != nil {
		t.Fatalf("不足两个成员时应解散: %+v %v", dissolved, err)
	}
	if countVersionRows(t, &models.VideoVersionGroup{}, "id = ?", group.GroupID) != 0 ||
		countVersionRows(t, &models.VideoVersionMember{}, "group_id = ?", group.GroupID) != 0 {
		t.Fatal("解散后组与成员行都应删除")
	}
	if _, err := service.Get(ctx, group.GroupID); !errors.Is(err, ErrVersionGroupNotFound) {
		t.Fatalf("解散后读取应返回 not found: %v", err)
	}

	second, err := service.Create(ctx, versionIDs(a, c, d), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Dissolve(ctx, second.GroupID, second.Revision); err != nil {
		t.Fatal(err)
	}
	if countVersionRows(t, &models.VideoVersionMember{}, "1 = 1") != 0 || countVersionRows(t, &models.VideoVersionGroup{}, "1 = 1") != 0 {
		t.Fatal("解散应删除组与全部成员行")
	}
	if countVersionRows(t, &models.Video{}, "id IN ?", versionIDs(a, c, d)) != 3 {
		t.Fatal("解散不应删除视频")
	}
}

func restoreVersionVideo(t *testing.T, video models.Video) {
	t.Helper()
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID).
		Updates(map[string]interface{}{"deleted_at": nil, "is_stale": false, "stale_reason": ""}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestVersionGroupPermanentDeleteCascadesAndNextWriteCleansUp(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	a, b, c, d := seedVersionVideo(t, "a", nil), seedVersionVideo(t, "b", nil), seedVersionVideo(t, "c", nil), seedVersionVideo(t, "d", nil)
	pair, err := service.Create(ctx, versionIDs(a, b), "")
	if err != nil {
		t.Fatal(err)
	}
	// 回收站「永久删除」走 hardDeleteVideoTx：成员行随外键级联删除，其他版本不受影响。
	if err := database.DB.Transaction(func(tx *gorm.DB) error { return hardDeleteVideoTx(tx, b.ID) }); err != nil {
		t.Fatal(err)
	}
	if countVersionRows(t, &models.VideoVersionMember{}, "video_id = ?", b.ID) != 0 {
		t.Fatal("永久删除应级联删除成员行")
	}
	if countVersionRows(t, &models.Video{}, "id = ?", a.ID) != 1 {
		t.Fatal("删除一个版本不应影响其他版本")
	}
	// 组只剩一条成员行：管理列表不再列它，片库页也不把它当组展示；下一次任意写操作把它清掉。
	page, err := service.List(ctx, 0, 0)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("不足两个成员的组不应出现在列表里: %+v %v", page, err)
	}
	if _, err := service.Create(ctx, versionIDs(c, d), ""); err != nil {
		t.Fatal(err)
	}
	if countVersionRows(t, &models.VideoVersionGroup{}, "id = ?", pair.GroupID) != 0 ||
		countVersionRows(t, &models.VideoVersionMember{}, "video_id = ?", a.ID) != 0 {
		t.Fatal("写操作应顺带清理不足两个成员的组")
	}
	// a 因而被释放，可以加入别的组。
	if _, err := service.Create(ctx, []uint{a.ID, seedVersionVideo(t, "e", nil).ID}, ""); err != nil {
		t.Fatalf("清理后 a 应能重新建组: %v", err)
	}
}

func TestVersionGroupSoftDeletedMembersHiddenAndRestored(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	a := seedVersionVideo(t, "a", func(v *models.Video) { v.IsWatched = true })
	b := seedVersionVideo(t, "b", nil)
	c := seedVersionVideo(t, "c", nil)
	group, err := service.Create(ctx, versionIDs(a, b, c), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&a).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := service.Get(ctx, group.GroupID)
	if err != nil {
		t.Fatal(err)
	}
	requireUintSlice(t, "软删成员不展示", memberIDs(detail.Members), versionIDs(b, c))
	if detail.MemberCount != 2 || detail.DeletedMemberCount != 1 || detail.WatchedCount != 0 {
		t.Fatalf("软删成员不参与汇总: %+v", detail)
	}
	if countVersionRows(t, &models.VideoVersionMember{}, "video_id = ?", a.ID) != 1 {
		t.Fatal("软删除应保留成员行")
	}
	restoreVersionVideo(t, a)
	restored, err := service.Get(ctx, group.GroupID)
	if err != nil {
		t.Fatal(err)
	}
	requireUintSlice(t, "恢复后回到原位", memberIDs(restored.Members), versionIDs(a, b, c))
	if restored.WatchedCount != 1 || restored.DeletedMemberCount != 0 {
		t.Fatalf("恢复后汇总不对: %+v", restored)
	}
}

func snapshotVersionVideos(t *testing.T, ids []uint) string {
	t.Helper()
	var videos []models.Video
	if err := database.DB.Unscoped().Where("id IN ?", ids).Order("id ASC").Find(&videos).Error; err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(videos)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// Q15：已看、进度、评分、收藏、点赞、播放计数等全部按文件独立；任何版本组操作与聚合读取都不写视频行。
func TestVersionGroupOperationsNeverWriteMemberVideoFields(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	rating := 7.5
	played := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	a := seedVersionVideo(t, "a", func(v *models.Video) {
		v.IsWatched, v.PersonalRating, v.IsFavorite, v.PlayCount, v.LastPlayedAt = true, &rating, true, 3, &played
	})
	b := seedVersionVideo(t, "b", func(v *models.Video) { v.WatchPositionSeconds, v.IsLiked = 120, true })
	c := seedVersionVideo(t, "c", nil)
	ids := versionIDs(a, b, c)
	before := snapshotVersionVideos(t, ids)

	group, err := service.Create(ctx, versionIDs(a, b), "")
	if err != nil {
		t.Fatal(err)
	}
	steps := []func(rev int64) (*VersionGroupDetail, error){
		func(rev int64) (*VersionGroupDetail, error) {
			return service.AddMembers(ctx, group.GroupID, rev, []uint{c.ID})
		},
		func(rev int64) (*VersionGroupDetail, error) {
			return service.Update(ctx, group.GroupID, rev, "片", map[uint]string{a.ID: "原版", c.ID: "剪辑版"})
		},
		func(rev int64) (*VersionGroupDetail, error) {
			return service.Reorder(ctx, group.GroupID, rev, versionIDs(c, b, a))
		},
		func(rev int64) (*VersionGroupDetail, error) {
			return service.RemoveMember(ctx, group.GroupID, rev, c.ID)
		},
	}
	revision := group.Revision
	for index, step := range steps {
		detail, err := step(revision)
		if err != nil || detail == nil {
			t.Fatalf("步骤 %d 失败: %+v %v", index, detail, err)
		}
		revision = detail.Revision
	}
	videoService := &VideoService{}
	if _, err := videoService.SearchLibraryVideoPage(LibraryFilter{CollapseVersions: true}, nil, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := videoService.CountLibraryVideos(LibraryFilter{CollapseVersions: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListSuggestions(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := service.Dissolve(ctx, group.GroupID, revision); err != nil {
		t.Fatal(err)
	}
	if after := snapshotVersionVideos(t, ids); after != before {
		t.Fatalf("版本组操作改写了视频行:\nbefore=%s\nafter =%s", before, after)
	}
}

func seedSameSourcePair(t *testing.T, a, b models.Video, status string, at time.Time) {
	t.Helper()
	low, high := a.ID, b.ID
	if low > high {
		low, high = high, low
	}
	relation := models.VideoSameSourceRelation{
		VideoAID: low, VideoBID: high, VideoAFingerprint: "fa", VideoBFingerprint: "fb",
		Status: status, DetectionVersion: "test", CreatedAt: at, UpdatedAt: at,
	}
	if err := database.DB.Omit("VideoA", "VideoB").Create(&relation).Error; err != nil {
		t.Fatalf("建同源关系失败: %v", err)
	}
}

func suggestionSets(suggestions []VersionGroupSuggestion) []string {
	sets := make([]string, 0, len(suggestions))
	for _, suggestion := range suggestions {
		sets = append(sets, fmt.Sprint(sortedIDs(suggestion.VideoIDs)))
	}
	return sets
}

func TestVersionGroupSuggestionsFromDetectedSameSourcePairs(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	dur := func(seconds float64) func(*models.Video) { return func(v *models.Video) { v.Duration = seconds } }
	// 正常配对：时长 100 与 91，较短者 ≥90%。高清的那个排在前面作建议主版本。
	a := seedVersionVideo(t, "a", func(v *models.Video) { v.Duration, v.Width, v.Height = 91, 1920, 1080 })
	b := seedVersionVideo(t, "b", dur(100))
	clipLong, clipShort := seedVersionVideo(t, "clip-long", dur(100)), seedVersionVideo(t, "clip-short", dur(89))
	rejA, rejB := seedVersionVideo(t, "rej-a", nil), seedVersionVideo(t, "rej-b", nil)
	grouped, free := seedVersionVideo(t, "grouped", nil), seedVersionVideo(t, "free", nil)
	disA, disB := seedVersionVideo(t, "dis-a", nil), seedVersionVideo(t, "dis-b", nil)
	staleA, staleB := seedVersionVideo(t, "stale-a", func(v *models.Video) { v.IsStale = true }), seedVersionVideo(t, "stale-b", nil)
	unknownA, unknownB := seedVersionVideo(t, "unknown-a", dur(0)), seedVersionVideo(t, "unknown-b", nil)
	gone, kept := seedVersionVideo(t, "gone", nil), seedVersionVideo(t, "kept", nil)
	// 传递链：p-q、q-r 检测为同源，但 p-r 被否认，不能连成 {p,q,r}。
	p, q, r := seedVersionVideo(t, "p", nil), seedVersionVideo(t, "q", nil), seedVersionVideo(t, "r", nil)
	other := seedVersionVideo(t, "other", nil)

	seedSameSourcePair(t, a, b, models.VideoSameSourceStatusDetected, base.Add(1*time.Hour))
	seedSameSourcePair(t, clipLong, clipShort, models.VideoSameSourceStatusDetected, base)
	seedSameSourcePair(t, rejA, rejB, models.VideoSameSourceStatusRejected, base)
	seedSameSourcePair(t, grouped, free, models.VideoSameSourceStatusDetected, base)
	seedSameSourcePair(t, disA, disB, models.VideoSameSourceStatusDetected, base)
	seedSameSourcePair(t, staleA, staleB, models.VideoSameSourceStatusDetected, base)
	seedSameSourcePair(t, unknownA, unknownB, models.VideoSameSourceStatusDetected, base)
	seedSameSourcePair(t, gone, kept, models.VideoSameSourceStatusDetected, base)
	seedSameSourcePair(t, p, q, models.VideoSameSourceStatusDetected, base.Add(3*time.Hour))
	seedSameSourcePair(t, q, r, models.VideoSameSourceStatusDetected, base.Add(2*time.Hour))
	seedSameSourcePair(t, p, r, models.VideoSameSourceStatusRejected, base)
	if _, err := service.Create(ctx, versionIDs(grouped, other), ""); err != nil {
		t.Fatal(err)
	}
	if err := service.DismissSuggestion(ctx, versionIDs(disB, disA)); err != nil {
		t.Fatal(err)
	}
	if err := service.DismissSuggestion(ctx, versionIDs(disA, disB)); err != nil {
		t.Fatalf("重复忽略应幂等: %v", err)
	}
	if err := database.DB.Delete(&gone).Error; err != nil {
		t.Fatal(err)
	}

	suggestions, err := service.ListSuggestions(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := suggestionSets(suggestions)
	want := []string{fmt.Sprint(sortedIDs(versionIDs(p, q))), fmt.Sprint(sortedIDs(versionIDs(a, b)))}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("建议组不对（按成员数、关系时间排序）: got %v want %v", got, want)
	}
	requireUintSlice(t, "建议主版本取画质高的", suggestions[1].VideoIDs, versionIDs(a, b))
	if !suggestions[0].LatestRelationAt.Equal(base.Add(3 * time.Hour)) {
		t.Fatalf("最新关系时间不对: %v", suggestions[0].LatestRelationAt)
	}
	if limited, err := service.ListSuggestions(ctx, 1); err != nil || len(limited) != 1 {
		t.Fatalf("limit 应生效: %v %v", limited, err)
	}
	if countVersionRows(t, &models.VideoVersionGroup{}, "1 = 1") != 1 {
		t.Fatal("建议从不自动建组")
	}
	if err := service.DismissSuggestion(ctx, versionIDs(a, b)); err != nil {
		t.Fatal(err)
	}
	if after, err := service.ListSuggestions(ctx, 0); err != nil || len(after) != 1 {
		t.Fatalf("忽略后不再建议: %v %v", suggestionSets(after), err)
	}
	if err := service.DismissSuggestion(ctx, []uint{a.ID}); !errors.Is(err, ErrVersionGroupInvalid) {
		t.Fatalf("少于两个视频的忽略应被拒绝: %v", err)
	}
}

func TestVersionGroupSuggestionComponentsRespectSizeLimit(t *testing.T) {
	var edges []versionSuggestionEdge
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for id := uint(2); id <= 25; id++ {
		edges = append(edges, versionSuggestionEdge{VideoAID: 1, VideoBID: id, UpdatedAt: at})
	}
	components := groupVersionSuggestionEdges(edges, map[versionPairKey]bool{})
	if len(components) != 1 || len(components[0].members) != versionGroupMaxMembers {
		t.Fatalf("候选组最多 %d 个: %+v", versionGroupMaxMembers, components)
	}
}

// 复审 #4：只剩一条成员行的组，清理不能因写事务回滚而丢失——否则它会一直冲突。
// 现在清理先在独立事务里提交；目标组被清掉时报 version_group_not_found，而不是 revision 冲突。
func TestVersionGroupCleanupSurvivesFailedWriteAndReportsNotFound(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx := context.Background()
	service := NewVersionGroupService()
	a, b := seedVersionVideo(t, "a", nil), seedVersionVideo(t, "b", nil)
	c, d := seedVersionVideo(t, "c", nil), seedVersionVideo(t, "d", nil)
	e, f := seedVersionVideo(t, "e", nil), seedVersionVideo(t, "f", nil)
	stranded, err := service.Create(ctx, versionIDs(a, b), "")
	if err != nil {
		t.Fatal(err)
	}
	hardDelete := func(video models.Video) {
		if err := database.DB.Transaction(func(tx *gorm.DB) error { return hardDeleteVideoTx(tx, video.ID) }); err != nil {
			t.Fatal(err)
		}
	}
	hardDelete(b)
	// 带着正确 revision 写这个组：组已在写之前被清理，报 not_found，并且清理结果留下来。
	for attempt := 0; attempt < 2; attempt++ {
		_, err = service.Update(ctx, stranded.GroupID, stranded.Revision, "x", nil)
		if !errors.Is(err, ErrVersionGroupNotFound) {
			t.Fatalf("第 %d 次写被清理的组应返回 version_group_not_found: %v", attempt+1, err)
		}
	}
	if countVersionRows(t, &models.VideoVersionGroup{}, "id = ?", stranded.GroupID) != 0 ||
		countVersionRows(t, &models.VideoVersionMember{}, "video_id = ?", a.ID) != 0 {
		t.Fatal("清理应已提交，不随失败的写事务回滚")
	}

	// 写事务因成员冲突回滚时，事先的清理同样保留：c 所在的单成员组被清掉，冲突只报 e。
	second, err := service.Create(ctx, versionIDs(c, d), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, versionIDs(e, f), ""); err != nil {
		t.Fatal(err)
	}
	hardDelete(d)
	_, err = service.Create(ctx, versionIDs(c, e), "")
	var conflict *VersionMemberConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("应报成员冲突: %v", err)
	}
	requireUintSlice(t, "冲突只含真正在组里的视频", conflict.VideoIDs, []uint{e.ID})
	if countVersionRows(t, &models.VideoVersionGroup{}, "id = ?", second.GroupID) != 0 {
		t.Fatal("冲突回滚不应撤销事先的清理")
	}
	// 组还在但 revision 过期时仍是冲突，不是 not_found。
	if _, err := service.Update(ctx, conflict.GroupIDs[0], 99, "", nil); !errors.Is(err, ErrVersionGroupConflict) {
		t.Fatalf("revision 过期应仍是冲突: %v", err)
	}
}
