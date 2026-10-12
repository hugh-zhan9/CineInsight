package services

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// D-LD-05：回收站中的记录的文件不算孤儿；文件名无法解析的不动；所属记录彻底不存在的按位置统计并回收。
func TestLostMediaOrphanFilesReportAndReclaim(t *testing.T) {
	service, dataDir := setupLostMediaTest(t)
	service.SetDependencies(&VideoService{}, NewImageService(), NewPlaybackProxyService(dataDir, nil))
	active := models.Video{Name: "a.mp4", Path: "/x/a.mp4"}
	trashed := models.Video{Name: "b.mp4", Path: "/x/b.mp4"}
	person := models.Person{DisplayName: "p"}
	lostMustCreate(t, &active, &trashed, &person)
	database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", trashed.ID).Updates(map[string]any{"deleted_at": time.Now(), "deleted_by": "user"})
	gone := strconv.Itoa(int(trashed.ID + 100))
	keep := map[string]int{
		filepath.Join("thumbnails", strconv.Itoa(int(active.ID))+".jpg"):                10,
		filepath.Join("thumbnails", strconv.Itoa(int(trashed.ID))+".seek.f10-i500.jpg"): 10,
		filepath.Join("thumbnails", "abc.jpg"):                                          10,
		filepath.Join("thumbnails", "."+gone+"-123.jpg"):                                10,
		filepath.Join("media-details", "videos", "not-an-id", "p.jpg"):                  10,
		filepath.Join("media-details", "people", strconv.Itoa(int(person.ID)), "p.jpg"): 10,
		filepath.Join("subtitle-backups", strconv.Itoa(int(trashed.ID)), "1.srt"):       10,
		filepath.Join("wallpapers", "0123abcd.jpg"):                                     10,
		filepath.Join("proxies", "notes.txt"):                                           10,
	}
	orphan := map[string]int{
		filepath.Join("thumbnails", gone+".jpg"):                     3,
		filepath.Join("thumbnails", gone+".seek.f10-i500.jpg"):       4,
		filepath.Join("image-thumbnails", "77.view.jpg"):             5,
		filepath.Join("media-details", "videos", gone, "poster.jpg"): 6,
		filepath.Join("media-details", "people", "88", "avatar.jpg"): 7,
		filepath.Join("subtitle-backups", gone, "1.srt"):             8,
		filepath.Join("faces", "99.jpg"):                             9,
		filepath.Join("proxies", gone+"-0123456789abcdef.mp4"):       11,
	}
	for rel, size := range keep {
		lostTestWriteFile(t, filepath.Join(dataDir, rel), size)
	}
	for rel, size := range orphan {
		lostTestWriteFile(t, filepath.Join(dataDir, rel), size)
	}

	report, err := service.GetOrphanFileReport(lostTestContext())
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalFiles != len(orphan) || report.TotalBytes != 53 {
		t.Fatalf("孤儿合计不对: %+v", report)
	}
	byKey := map[string]OrphanFileLocation{}
	for _, location := range report.Locations {
		byKey[location.Key] = location
	}
	if byKey["thumbnails"].Files != 2 || byKey["thumbnails"].Bytes != 7 || byKey[OrphanLocationProxies].Files != 1 || byKey["person_images"].Bytes != 7 {
		t.Fatalf("按位置统计不对: %+v", report.Locations)
	}
	files, bytes, err := service.OrphanFileTotals(lostTestContext())
	if err != nil || files != report.TotalFiles || bytes != report.TotalBytes {
		t.Fatalf("合计接口应与报告一致: %d %d %v", files, bytes, err)
	}

	keys := []string{}
	for _, location := range report.Locations {
		keys = append(keys, location.Key)
	}
	result, err := service.ReclaimOrphanFiles(lostTestContext(), keys)
	if err != nil || result.Files != len(orphan) || result.Bytes != 53 || result.Failed != 0 {
		t.Fatalf("回收结果不对: %+v err=%v", result, err)
	}
	for rel := range orphan {
		if _, err := os.Stat(filepath.Join(dataDir, rel)); !os.IsNotExist(err) {
			t.Fatalf("孤儿应被回收: %s", rel)
		}
	}
	for rel := range keep {
		if _, err := os.Stat(filepath.Join(dataDir, rel)); err != nil {
			t.Fatalf("不该动的文件被删了: %s", rel)
		}
	}
	if _, err := service.ReclaimOrphanFiles(lostTestContext(), []string{"wallpapers"}); LostMediaErrorCode(err) != "lost_media_invalid_request" {
		t.Fatalf("未知位置应报错: %v", err)
	}
}

// M5：正在排队或转封装的视频的代理文件不当孤儿删（它的行在发布之后才写入）。
func TestRemoveOrphanPlaybackProxiesSkipsInFlightVideos(t *testing.T) {
	_, dataDir := setupLostMediaTest(t)
	proxies := NewPlaybackProxyService(dataDir, nil)
	busy := filepath.Join(dataDir, "proxies", "41-0123456789abcdef.mp4")
	idle := filepath.Join(dataDir, "proxies", "42-0123456789abcdef.mp4")
	lostTestWriteFile(t, busy, 3)
	lostTestWriteFile(t, idle, 4)
	proxies.mu.Lock()
	proxies.inFlight[41] = struct{}{}
	proxies.mu.Unlock()
	removed, bytes, failed, err := proxies.RemoveOrphanPlaybackProxies()
	if err != nil || removed != 1 || bytes != 4 || failed != 0 {
		t.Fatalf("应只回收空闲视频的孤儿: removed=%d bytes=%d failed=%d err=%v", removed, bytes, failed, err)
	}
	if _, err := os.Stat(busy); err != nil {
		t.Fatalf("在处理中的视频的代理文件不应被删: %v", err)
	}
}
