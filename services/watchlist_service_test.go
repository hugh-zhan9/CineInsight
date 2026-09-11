package services

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestWatchlistCRUDAndMediaIsolation(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewWatchlistService(t.TempDir())
	entry, err := svc.Create("  沙丘（1984）  ", "")
	if err != nil || entry.Title != "沙丘（1984）" || entry.ID == 0 || entry.CreatedAt.IsZero() {
		t.Fatalf("添加失败: %+v %v", entry, err)
	}
	if _, err := svc.Create(" "+entry.Title+" ", ""); !errors.Is(err, ErrWatchlistTitleExists) {
		t.Fatalf("去掉首尾空白后的同名片名应拒绝: %v", err)
	}
	duplicate, err := svc.Create("另一部片", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Update(duplicate.ID, entry.Title); !errors.Is(err, ErrWatchlistTitleExists) {
		t.Fatalf("改名冲突应拒绝: %v", err)
	}
	if err := svc.Update(entry.ID, entry.Title); err != nil {
		t.Fatalf("保留自身名称应成功: %v", err)
	}
	video := models.Video{Name: entry.Title, Path: "/movies/dune.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	// 重新构造服务，并创建同名本地视频，片单仍保持两条。
	page, err := NewWatchlistService(t.TempDir()).List("", 0, 50)
	if err != nil || len(page.Entries) != 2 || page.Entries[0].ID != duplicate.ID {
		t.Fatalf("持久记录或顺序错误: %+v %v", page, err)
	}
	if err := svc.Update(entry.ID, "  沙丘（2021） "); err != nil {
		t.Fatal(err)
	}
	var saved models.WatchlistEntry
	if err := database.DB.First(&saved, entry.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Title != "沙丘（2021）" || saved.CreatedAt.UnixMicro() != entry.CreatedAt.UnixMicro() {
		t.Fatalf("改名不应改变创建时间: %+v", saved)
	}
	if err := svc.Delete(entry.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Update(entry.ID, "不能复活"); !errors.Is(err, ErrWatchlistEntryNotFound) {
		t.Fatalf("移除后更新应失败: %v", err)
	}
	if err := svc.Delete(entry.ID); !errors.Is(err, ErrWatchlistEntryNotFound) {
		t.Fatalf("重复移除应明确不存在: %v", err)
	}
	var count int64
	database.DB.Unscoped().Model(&models.WatchlistEntry{}).Count(&count)
	if count != 1 {
		t.Fatalf("只应删除指定条目，剩余=%d", count)
	}
	if err := database.DB.First(&video, video.ID).Error; err != nil {
		t.Fatalf("移除片名不能影响媒体: %v", err)
	}
}

func TestWatchlistValidation(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewWatchlistService(t.TempDir())
	entry, err := svc.Create(strings.Repeat("影", 200), "")
	if err != nil {
		t.Fatalf("200 个 Unicode 字符应允许: %v", err)
	}
	for _, title := range []string{"", " \t\n", strings.Repeat("影", 201), "片\x00名", "片\n名", string([]byte{0xff})} {
		if _, err := svc.Create(title, ""); err == nil {
			t.Errorf("不应创建非法片名 %q", title)
		}
		if err := svc.Update(entry.ID, title); err == nil {
			t.Errorf("不应更新为非法片名 %q", title)
		}
	}
	for _, id := range []uint{0, entry.ID + 100} {
		if err := svc.Update(id, "片名"); !errors.Is(err, ErrWatchlistEntryNotFound) {
			t.Errorf("不存在的 ID 更新未失败: %d %v", id, err)
		}
		if err := svc.Delete(id); !errors.Is(err, ErrWatchlistEntryNotFound) {
			t.Errorf("不存在的 ID 删除未失败: %d %v", id, err)
		}
	}
	page, err := svc.List("", 0, 50)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Title != entry.Title {
		t.Fatalf("非法写入不得改变片单: %+v %v", page, err)
	}
	if _, err := svc.List(strings.Repeat("影", 201), 0, 50); err == nil {
		t.Fatal("超长搜索应失败")
	}
}

func TestWatchlistSearchAndPagination(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewWatchlistService(t.TempDir())
	page, err := svc.List("", 0, 0)
	if err != nil || page.Entries == nil || len(page.Entries) != 0 || page.NextID != 0 {
		t.Fatalf("空库应为空数组: %+v %v", page, err)
	}
	for _, title := range []string{"Dune", "沙丘", "100%", "a_b", `a\b`, "aXb", "O'Brien"} {
		if _, err := svc.Create(title, ""); err != nil {
			t.Fatal(err)
		}
	}
	for keyword, want := range map[string]string{" dune ": "Dune", "丘": "沙丘", "%": "100%", "_": "a_b", `\`: `a\b`, "O'B": "O'Brien"} {
		page, err := svc.List(keyword, 0, 1)
		if err != nil || len(page.Entries) != 1 || page.Entries[0].Title != want || page.NextID != 0 {
			t.Fatalf("搜索 %q 错误: %+v %v", keyword, page, err)
		}
	}
	page, err = svc.List("", 0, 3)
	if err != nil || len(page.Entries) != 3 || page.NextID != page.Entries[2].ID {
		t.Fatalf("首屏分页错误: %+v %v", page, err)
	}
	newest, err := svc.Create("后来添加", "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint]bool{}
	for {
		for _, entry := range page.Entries {
			if seen[entry.ID] || entry.ID == newest.ID {
				t.Fatalf("翻页不应重复或插入新首行: %+v", entry)
			}
			seen[entry.ID] = true
		}
		if page.NextID == 0 {
			if len(page.Entries) != 1 {
				t.Fatalf("尾批应只有一行: %+v", page)
			}
			break
		}
		page, err = svc.List("", page.NextID, 3)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 7 {
		t.Fatalf("翻页漏行: %v", seen)
	}
}

func TestWatchlistPageLimit(t *testing.T) {
	setupVideoServiceTestDB(t)
	entries := make([]models.WatchlistEntry, 201)
	for i := range entries {
		entries[i].Title = fmt.Sprintf("片名 %d", i)
	}
	if err := database.DB.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewWatchlistService(t.TempDir())
	for _, tc := range []struct{ limit, want int }{{-1, 50}, {0, 50}, {1, 1}, {200, 200}, {10000, 200}} {
		page, err := svc.List("", 0, tc.limit)
		if err != nil || len(page.Entries) != tc.want || page.NextID == 0 {
			t.Fatalf("limit=%d 错误: %+v %v", tc.limit, page, err)
		}
	}
}

// 唯一键换成 (title, kind) 之后，撞名仍必须翻译成那句用户文案。
//
// 这条用例钉的是文案本身，不只是 error 值：watchlistTitleConflict 靠数据库报错里的
// 索引名（Postgres）或列清单（SQLite）识别撞名，索引一改名或一调列序判据就失配，
// 用户看到的会退化成一条通用数据库错误，而只断言 errors.Is 抓不到这种退化。
func TestWatchlistDuplicateTitleMessage(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewWatchlistService(t.TempDir())
	entry, err := svc.Create("沙丘", "")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Kind != models.WatchlistKindMovie || entry.EnrichmentStatus != models.WatchlistEnrichmentPending {
		t.Fatalf("新建条目应是 movie/pending: %+v", entry)
	}
	other, err := svc.Create("沙丘（1984）", "")
	if err != nil {
		t.Fatal(err)
	}
	_, createErr := svc.Create("沙丘", "")
	for name, conflict := range map[string]error{
		"新建撞名": createErr,
		"改名撞名": svc.Update(other.ID, "沙丘"),
	} {
		if !errors.Is(conflict, ErrWatchlistTitleExists) {
			t.Fatalf("%s 应判为撞名: %v", name, conflict)
		}
		if conflict.Error() != "该片名已在想看片单中" {
			t.Fatalf("%s 的用户文案不对: %q", name, conflict.Error())
		}
	}
}

// 同名不同类型是两条独立记录，撞名只在同类型之间发生。
func TestWatchlistSameTitleDifferentKindCoexists(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewWatchlistService(t.TempDir())
	movie, err := svc.Create("沙丘", "")
	if err != nil {
		t.Fatal(err)
	}
	tv := models.WatchlistEntry{
		Title:            movie.Title,
		Kind:             models.WatchlistKindTV,
		EnrichmentStatus: models.WatchlistEnrichmentPending,
	}
	if err := database.DB.Create(&tv).Error; err != nil {
		t.Fatalf("同名不同类型应可共存: %v", err)
	}
	if _, err := svc.Create("沙丘", ""); !errors.Is(err, ErrWatchlistTitleExists) {
		t.Fatalf("同名同类型仍应拒绝: %v", err)
	}
	// 共存不改变列表、搜索与分页的既有行为。
	page, err := svc.List("沙丘", 0, 50)
	if err != nil || len(page.Entries) != 2 || page.NextID != 0 {
		t.Fatalf("搜索应同时返回两条: %+v %v", page, err)
	}
	if page.Entries[0].ID != tv.ID || page.Entries[1].ID != movie.ID {
		t.Fatalf("列表顺序应仍是 id 倒序: %+v", page.Entries)
	}
	// 删除只影响指定的一条，不会连坐同名的另一类型。
	if err := svc.Delete(movie.ID); err != nil {
		t.Fatal(err)
	}
	page, err = svc.List("沙丘", 0, 50)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].ID != tv.ID {
		t.Fatalf("删除不应连坐同名的另一类型: %+v %v", page, err)
	}
}
