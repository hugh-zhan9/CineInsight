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
	svc := &WatchlistService{}
	entry, err := svc.Create("  沙丘（1984）  ")
	if err != nil || entry.Title != "沙丘（1984）" || entry.ID == 0 || entry.CreatedAt.IsZero() {
		t.Fatalf("添加失败: %+v %v", entry, err)
	}
	duplicate, err := svc.Create(entry.Title)
	if err != nil || duplicate.ID == entry.ID {
		t.Fatalf("同名片名应独立保存: %+v %v", duplicate, err)
	}
	video := models.Video{Name: entry.Title, Path: "/movies/dune.mp4"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	// 重新构造服务，并创建同名本地视频，片单仍保持两条。
	page, err := (&WatchlistService{}).List("沙丘", 0, 50)
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
	svc := &WatchlistService{}
	entry, err := svc.Create(strings.Repeat("影", 200))
	if err != nil {
		t.Fatalf("200 个 Unicode 字符应允许: %v", err)
	}
	for _, title := range []string{"", " \t\n", strings.Repeat("影", 201), "片\x00名", "片\n名", string([]byte{0xff})} {
		if _, err := svc.Create(title); err == nil {
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
	svc := &WatchlistService{}
	page, err := svc.List("", 0, 0)
	if err != nil || page.Entries == nil || len(page.Entries) != 0 || page.NextID != 0 {
		t.Fatalf("空库应为空数组: %+v %v", page, err)
	}
	for _, title := range []string{"Dune", "沙丘", "100%", "a_b", `a\b`, "aXb", "O'Brien"} {
		if _, err := svc.Create(title); err != nil {
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
	newest, err := svc.Create("后来添加")
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
	svc := &WatchlistService{}
	for _, tc := range []struct{ limit, want int }{{-1, 50}, {0, 50}, {1, 1}, {200, 200}, {10000, 200}} {
		page, err := svc.List("", 0, tc.limit)
		if err != nil || len(page.Entries) != tc.want || page.NextID == 0 {
			t.Fatalf("limit=%d 错误: %+v %v", tc.limit, page, err)
		}
	}
}
