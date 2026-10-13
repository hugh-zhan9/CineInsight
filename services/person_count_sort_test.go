package services

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"video-master/database"
	"video-master/models"
)

// D-P2-06 头像墙按出现次数：active_video_count 倒序、同数按 LOWER(名称)、再按 id；同数同名的翻页不重不漏（两个后端）。
func TestListPeopleByCountOrdersAndPaginatesWithTies(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := NewPersonService(t.TempDir())
	videos := make([]models.Video, 0, 6)
	for i := 0; i < 6; i++ {
		video := models.Video{Name: fmt.Sprintf("p%d.mp4", i), Path: fmt.Sprintf("/library/people/%d.mp4", i), Directory: "/library/people"}
		if err := database.DB.Create(&video).Error; err != nil {
			t.Fatal(err)
		}
		videos = append(videos, video)
	}
	if err := database.DB.Model(&models.Video{}).Where("id = ?", videos[5].ID).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	type seed struct {
		name   string
		videos []int
	}
	seeds := []seed{
		{"Bob", []int{0, 1, 2}}, {"alice", []int{0, 1}}, {"Alice", []int{2, 3}}, {"张三", []int{0, 1}},
		{"carol", nil}, {"Dave", []int{4}}, {"erin", []int{5}}, // erin 只出现在失效视频里：计 0
		{"alice", []int{4, 3}}, {"Frank", nil},
	}
	people := make([]models.Person, 0, len(seeds))
	counts := map[uint]int{}
	for _, item := range seeds {
		person, err := service.CreatePerson(item.name, "")
		if err != nil {
			t.Fatal(err)
		}
		ids := []uint{}
		for _, index := range item.videos {
			ids = append(ids, videos[index].ID)
			if index != 5 {
				counts[person.ID]++
			}
		}
		if len(ids) > 0 {
			if err := service.AddPersonVideos(person.ID, ids); err != nil {
				t.Fatal(err)
			}
		}
		people = append(people, *person)
	}
	sort.SliceStable(people, func(i, j int) bool {
		a, b := people[i], people[j]
		if counts[a.ID] != counts[b.ID] {
			return counts[a.ID] > counts[b.ID]
		}
		if strings.ToLower(a.DisplayName) != strings.ToLower(b.DisplayName) {
			return strings.ToLower(a.DisplayName) < strings.ToLower(b.DisplayName)
		}
		return a.ID < b.ID
	})
	want := make([]uint, 0, len(people))
	for _, person := range people {
		want = append(want, person.ID)
	}
	full, err := service.ListPeopleByCount("", 0, "", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]uint, 0, len(full))
	for _, item := range full {
		got = append(got, item.Person.ID)
		if item.ActiveVideoCount != int64(counts[item.Person.ID]) {
			t.Fatalf("%s 的计数应是可见视频数 %d: %d", item.Person.DisplayName, counts[item.Person.ID], item.ActiveVideoCount)
		}
	}
	requireUintSlice(t, "一次取完", got, want)
	for _, limit := range []int{1, 2, 3, 4} {
		paged := []uint{}
		cursorCount, cursorName, cursorID := 0, "", uint(0)
		for pages := 0; pages < 50; pages++ {
			page, err := service.ListPeopleByCount("", cursorCount, cursorName, cursorID, limit)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page {
				paged = append(paged, item.Person.ID)
			}
			if len(page) < limit {
				break
			}
			last := page[len(page)-1]
			cursorCount, cursorName, cursorID = int(last.ActiveVideoCount), last.CursorName, last.Person.ID
		}
		requireUintSlice(t, fmt.Sprintf("limit=%d 翻页", limit), paged, want)
	}
	filtered, err := service.ListPeopleByCount("ALI", 0, "", 0, 10)
	if err != nil || len(filtered) != 3 || filtered[0].ActiveVideoCount != 2 {
		t.Fatalf("关键词筛选: %+v %v", filtered, err)
	}
}
