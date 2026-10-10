package main

import (
	"errors"
	"testing"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

// 版本组绑定：完整走一遍增改排删，并确认数据库维护期间每个绑定都被拒绝。
func TestVersionGroupBindingsRoundTripAndMaintenanceGate(t *testing.T) {
	setupAppTestDB(t)
	var ids []uint
	for _, name := range []string{"a", "b", "c"} {
		video := models.Video{Name: name + ".mp4", Path: "/library/" + name + ".mp4", Directory: "/library", Duration: 60}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, video.ID)
	}
	a := &App{}
	group, err := a.CreateVersionGroup(ids[:2], "影片")
	if err != nil || group.MemberCount != 2 {
		t.Fatalf("建组失败: %+v %v", group, err)
	}
	if _, err := a.CreateVersionGroup([]uint{ids[1], ids[2]}, ""); !errors.Is(err, services.ErrVersionMemberConflict) {
		t.Fatalf("成员冲突应透传: %v", err)
	}
	group, err = a.AddVersionMembers(group.GroupID, group.Revision, ids[2:])
	if err != nil || group.MemberCount != 3 {
		t.Fatalf("加入成员失败: %+v %v", group, err)
	}
	group, err = a.ReorderVersionMembers(group.GroupID, group.Revision, []uint{ids[2], ids[0], ids[1]})
	if err != nil || group.Members[0].VideoID != ids[2] {
		t.Fatalf("重排失败: %+v %v", group, err)
	}
	group, err = a.UpdateVersionGroup(group.GroupID, group.Revision, "新名", map[uint]string{ids[2]: "高清版"})
	if err != nil || group.Title != "新名" || group.Members[0].Label != "高清版" {
		t.Fatalf("更新失败: %+v %v", group, err)
	}
	if _, err := a.UpdateVersionGroup(group.GroupID, group.Revision-1, "旧", nil); !errors.Is(err, services.ErrVersionGroupConflict) {
		t.Fatalf("revision 冲突应透传: %v", err)
	}
	group, err = a.RemoveVersionMember(group.GroupID, group.Revision, ids[1])
	if err != nil || group == nil || group.MemberCount != 2 {
		t.Fatalf("移出失败: %+v %v", group, err)
	}
	if fetched, err := a.GetVersionGroup(group.GroupID); err != nil || fetched.Revision != group.Revision {
		t.Fatalf("读取失败: %+v %v", fetched, err)
	}
	if page, err := a.ListVersionGroups(0, 0); err != nil || len(page.Items) != 1 {
		t.Fatalf("列表失败: %+v %v", page, err)
	}
	if suggestions, err := a.ListVersionGroupSuggestions(0); err != nil || len(suggestions) != 0 {
		t.Fatalf("建议失败: %+v %v", suggestions, err)
	}
	if err := a.DismissVersionGroupSuggestion(ids[:2]); err != nil {
		t.Fatal(err)
	}

	release := database.BeginMaintenance()
	calls := []func() error{
		func() error { _, err := a.CreateVersionGroup(ids[:2], ""); return err },
		func() error { _, err := a.GetVersionGroup(group.GroupID); return err },
		func() error { _, err := a.ListVersionGroups(0, 0); return err },
		func() error { _, err := a.AddVersionMembers(group.GroupID, group.Revision, ids[1:2]); return err },
		func() error { _, err := a.RemoveVersionMember(group.GroupID, group.Revision, ids[0]); return err },
		func() error { _, err := a.ReorderVersionMembers(group.GroupID, group.Revision, ids[:1]); return err },
		func() error { _, err := a.UpdateVersionGroup(group.GroupID, group.Revision, "", nil); return err },
		func() error { return a.DissolveVersionGroup(group.GroupID, group.Revision) },
		func() error { _, err := a.ListVersionGroupSuggestions(0); return err },
		func() error { return a.DismissVersionGroupSuggestion(ids[:2]) },
	}
	for index, call := range calls {
		if err := call(); err == nil {
			release()
			t.Fatalf("绑定 %d 绕过了数据库维护门", index)
		}
	}
	release()
	if err := a.DissolveVersionGroup(group.GroupID, group.Revision); err != nil {
		t.Fatal(err)
	}
	if fetched, err := a.GetVersionGroup(group.GroupID); !errors.Is(err, services.ErrVersionGroupNotFound) {
		t.Fatalf("解散后应读不到: %+v %v", fetched, err)
	}
}
