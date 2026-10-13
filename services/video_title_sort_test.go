package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// §2.3 片名排序键：算法、钩子、统一写法、启动补齐与「每个写标题 / 文件名的入口写后排序键正确」守卫。

const staleTitleSortKey = "ff"

func readTitleSortKey(t *testing.T, videoID uint) (models.Video, string) {
	t.Helper()
	var video models.Video
	if err := database.DB.Unscoped().First(&video, videoID).Error; err != nil {
		t.Fatalf("读取视频 %d 失败: %v", videoID, err)
	}
	return video, video.TitleSortKey
}

// requireTitleSortKeyCurrent 断言库里的排序键等于按当前两列重新算出的值，且不是测试预先埋的哨兵值。
func requireTitleSortKeyCurrent(t *testing.T, label string, videoID uint) models.Video {
	t.Helper()
	video, key := readTitleSortKey(t, videoID)
	want := models.VideoTitleSortKey(video.DisplayTitle, video.Name)
	if key != want || key == staleTitleSortKey || key == "" {
		t.Fatalf("%s：排序键没跟上 display_title=%q name=%q key=%q want=%q", label, video.DisplayTitle, video.Name, key, want)
	}
	return video
}

// plantStaleTitleSortKey 绕过钩子把键改成哨兵值，之后的入口必须重新算。
func plantStaleTitleSortKey(t *testing.T, videoID uint) {
	t.Helper()
	if err := database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", videoID).
		UpdateColumn("title_sort_key", staleTitleSortKey).Error; err != nil {
		t.Fatal(err)
	}
}

func TestVideoTitleSortKeyPinyinCaseWidthAndSource(t *testing.T) {
	// 拼音序（无多音字的例子；「长安」的「长」在 CLDR 里排在 zhang，见 models/video_title_sort.go 的说明）。
	titles := []string{"大话西游", "茶馆", "霸王别姬", "阿凡达", "银河护卫队", "Zootopia", "2046"}
	sort.Slice(titles, func(i, j int) bool {
		return models.VideoTitleSortKey(titles[i], "") < models.VideoTitleSortKey(titles[j], "")
	})
	if got := strings.Join(titles, ","); got != "2046,Zootopia,阿凡达,霸王别姬,茶馆,大话西游,银河护卫队" {
		t.Fatalf("排序键顺序不对: %s", got)
	}
	if a, b, c := models.VideoTitleSortKey("Avatar", ""), models.VideoTitleSortKey("avatar", ""), models.VideoTitleSortKey("ＡＶＡＴＡＲ", ""); a != b || b != c {
		t.Fatalf("应忽略大小写与全半角: %s %s %s", a, b, c)
	}
	if models.VideoTitleSortKey("", "阿凡达.mkv") != models.VideoTitleSortKey("阿凡达", "") {
		t.Fatal("display_title 为空时应取去扩展名的 name")
	}
	if models.VideoTitleSortKey("霸王别姬", "阿凡达.mkv") != models.VideoTitleSortKey("霸王别姬", "") {
		t.Fatal("display_title 非空时应只看 display_title")
	}
	if models.VideoTitleSortKey("", "") != "" {
		t.Fatal("两列都为空时键应为空串")
	}
	key := models.VideoTitleSortKey(strings.Repeat("龘", 255), "")
	if len(key) != 512 {
		t.Fatalf("长片名的键应截在列宽 512: %d", len(key))
	}
	if !isLowerHex(key) {
		t.Fatal("排序键应是小写十六进制")
	}
}

func TestVideoTitleSortKeyHookOnCreateSaveAndLoadedUpdates(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "阿凡达.mkv", Path: "/library/a.mkv", Directory: "/library"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if _, key := readTitleSortKey(t, video.ID); key != models.VideoTitleSortKey("", "阿凡达.mkv") {
		t.Fatalf("Create 应写入排序键: %q", key)
	}
	batch := []models.Video{
		{Name: "b.mkv", DisplayTitle: "霸王别姬", Path: "/library/b.mkv", Directory: "/library"},
		{Name: "c.mkv", DisplayTitle: "茶馆", Path: "/library/c.mkv", Directory: "/library"},
	}
	if err := database.DB.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range batch {
		requireTitleSortKeyCurrent(t, "批量 Create", row.ID)
	}

	// Save 整行写回。
	plantStaleTitleSortKey(t, video.ID)
	loaded, _ := readTitleSortKey(t, video.ID)
	loaded.DisplayTitle = "大话西游"
	if err := database.DB.Save(&loaded).Error; err != nil {
		t.Fatal(err)
	}
	requireTitleSortKeyCurrent(t, "Save", video.ID)

	// 按已加载的行做 map 更新，只改一列：钩子读现值补另一列。
	plantStaleTitleSortKey(t, video.ID)
	if err := database.DB.Model(&loaded).Updates(map[string]any{"display_title": ""}).Error; err != nil {
		t.Fatal(err)
	}
	got := requireTitleSortKeyCurrent(t, "Model(&video).Updates(display_title)", video.ID)
	if got.TitleSortKey != models.VideoTitleSortKey("", "阿凡达.mkv") {
		t.Fatalf("清空显示标题后应回到文件名: %q", got.TitleSortKey)
	}
	if err := database.DB.Model(&loaded).Updates(map[string]any{"display_title": "大话西游"}).Error; err != nil {
		t.Fatal(err)
	}
	plantStaleTitleSortKey(t, video.ID)
	if err := database.DB.Model(&loaded).Update("name", "茶馆.mkv").Error; err != nil {
		t.Fatal(err)
	}
	if got := requireTitleSortKeyCurrent(t, "Model(&video).Update(name)", video.ID); got.TitleSortKey != models.VideoTitleSortKey("大话西游", "") {
		t.Fatalf("显示标题非空时改文件名不该改变排序依据: %q", got.TitleSortKey)
	}

	// 不碰标题列的更新不动排序键。
	plantStaleTitleSortKey(t, video.ID)
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Updates(map[string]any{"is_liked": true}).Error; err != nil {
		t.Fatal(err)
	}
	if _, key := readTitleSortKey(t, video.ID); key != staleTitleSortKey {
		t.Fatalf("不改标题的更新不该写排序键: %q", key)
	}
}

// 按条件更新标题列却没经 UpdateVideoTitleFields：测试里（Strict）直接报错；生产上清空键留给补齐。
func TestVideoTitleSortKeyConditionalBypassIsCaught(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "a.mkv", Path: "/library/a.mkv", Directory: "/library"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("display_title", "阿凡达").Error
	if !errors.Is(err, models.ErrVideoTitleSortKeyBypass) {
		t.Fatalf("Strict 模式下应拒绝绕过统一写法的更新: %v", err)
	}
	models.StrictVideoTitleSortKey = false
	defer func() { models.StrictVideoTitleSortKey = true }()
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).Update("display_title", "阿凡达").Error; err != nil {
		t.Fatal(err)
	}
	if got, key := readTitleSortKey(t, video.ID); key != "" || got.DisplayTitle != "阿凡达" {
		t.Fatalf("生产上应照常写入并把键清空待补齐: %+v key=%q", got, key)
	}
	filled, err := BackfillVideoTitleSortKeys(context.Background())
	if err != nil || filled != 1 {
		t.Fatalf("补齐应补上这一行: filled=%d err=%v", filled, err)
	}
	requireTitleSortKeyCurrent(t, "补齐", video.ID)
}

func TestUpdateVideoTitleFieldsComputesFromMergedValues(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "阿凡达.mkv", DisplayTitle: "霸王别姬", Path: "/library/a.mkv", Directory: "/library"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	// 只改 name：显示标题仍非空，键不随文件名变。
	result := UpdateVideoTitleFields(database.DB.Model(&models.Video{}).Where("id = ? AND path = ?", video.ID, video.Path), video.ID,
		map[string]any{"name": "茶馆.mkv", "path": "/library/b.mkv"})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("更新失败: %v rows=%d", result.Error, result.RowsAffected)
	}
	if got := requireTitleSortKeyCurrent(t, "只改 name", video.ID); got.TitleSortKey != models.VideoTitleSortKey("霸王别姬", "") {
		t.Fatal("显示标题非空时键应跟显示标题")
	}
	// 乐观条件落空：RowsAffected 为 0，照旧交给调用方判断。
	result = UpdateVideoTitleFields(database.DB.Model(&models.Video{}).Where("id = ? AND path = ?", video.ID, "/elsewhere"), video.ID,
		map[string]any{"display_title": ""})
	if result.Error != nil || result.RowsAffected != 0 {
		t.Fatalf("条件不成立时应不写: %v rows=%d", result.Error, result.RowsAffected)
	}
	// 软删行同样能写（失联找回走 Unscoped）。
	if err := database.DB.Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	result = UpdateVideoTitleFields(database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", video.ID), video.ID,
		map[string]any{"display_title": ""})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("软删行更新失败: %v rows=%d", result.Error, result.RowsAffected)
	}
	if got := requireTitleSortKeyCurrent(t, "软删行", video.ID); got.TitleSortKey != models.VideoTitleSortKey("", "茶馆.mkv") {
		t.Fatal("清空显示标题后键应取文件名")
	}
}

func TestBackfillVideoTitleSortKeysFillsEmptyRowsInBatches(t *testing.T) {
	setupVideoServiceTestDB(t)
	total := videoTitleSortBackfillBatch + 3
	videos := make([]models.Video, 0, total)
	for i := 0; i < total; i++ {
		videos = append(videos, models.Video{Name: "片" + strconv.Itoa(i) + ".mp4", Path: "/library/v/" + strconv.Itoa(i) + ".mp4", Directory: "/library/v"})
	}
	if err := database.DB.CreateInBatches(&videos, 200).Error; err != nil {
		t.Fatal(err)
	}
	// 模拟升级前的存量行：键为空。软删行也要补；两列都空的行补不出来，原样跳过。
	if err := database.DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Model(&models.Video{}).UpdateColumn("title_sort_key", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Delete(&models.Video{}, videos[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	blank := models.Video{Name: "", Path: "/library/blank", Directory: "/library"}
	if err := database.DB.Create(&blank).Error; err != nil {
		t.Fatal(err)
	}
	filled, err := BackfillVideoTitleSortKeys(context.Background())
	if err != nil || filled != total {
		t.Fatalf("应补齐 %d 行: filled=%d err=%v", total, filled, err)
	}
	requireTitleSortKeyCurrent(t, "软删行补齐", videos[0].ID)
	requireTitleSortKeyCurrent(t, "最后一批", videos[total-1].ID)
	if _, key := readTitleSortKey(t, blank.ID); key != "" {
		t.Fatalf("两列都空的行应保持空串: %q", key)
	}
	again, err := BackfillVideoTitleSortKeys(context.Background())
	if err != nil || again != 0 {
		t.Fatalf("再跑一次应没有可补的: %d %v", again, err)
	}
}

// --- 守卫：每个写 display_title / name 的服务入口，写后排序键正确（§2.3）。 ---

func TestTitleSortKeyGuardScanCreateRenameRelocate(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "霸王别姬.mp4")
	mustCreateFile(t, path)
	service := NewVideoService(nil)
	// 扫描建档（AddVideo 是扫描与下载入库共用的建档入口）。
	video, err := service.AddVideo(path)
	if err != nil {
		t.Fatal(err)
	}
	requireTitleSortKeyCurrent(t, "扫描建档", video.ID)

	// 重命名。
	plantStaleTitleSortKey(t, video.ID)
	if err := service.RenameVideo(video.ID, "阿凡达"); err != nil {
		t.Fatal(err)
	}
	if got := requireTitleSortKeyCurrent(t, "重命名", video.ID); got.Name != "阿凡达.mp4" {
		t.Fatalf("重命名没生效: %+v", got)
	}

	// 重新定位（播放失败后的找回、迁移检测同一条写路径）。
	moved := filepath.Join(root, "moved", "茶馆.mp4")
	mustCreateFile(t, moved)
	plantStaleTitleSortKey(t, video.ID)
	if err := service.RelocateVideo(video.ID, moved); err != nil {
		t.Fatal(err)
	}
	if got := requireTitleSortKeyCurrent(t, "重新定位", video.ID); got.Name != "茶馆.mp4" {
		t.Fatalf("重新定位没生效: %+v", got)
	}
}

func TestTitleSortKeyGuardMetadataEditAndLocalMetadata(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	people, collections := NewPersonService(dataDir), NewCollectionService(dataDir)
	root := t.TempDir()
	path := filepath.Join(root, "movie.mp4")
	mustCreateFile(t, path)
	video := models.Video{Name: "movie.mp4", Path: path, Directory: root}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}

	// 元数据编辑（详情抽屉）。
	plantStaleTitleSortKey(t, video.ID)
	if _, err := NewVideoDetailService(people, collections).UpdateVideoDetails(VideoDetailsUpdate{VideoID: video.ID, DisplayTitle: "大话西游"}); err != nil {
		t.Fatal(err)
	}
	if got := requireTitleSortKeyCurrent(t, "元数据编辑", video.ID); got.DisplayTitle != "大话西游" {
		t.Fatalf("编辑没生效: %+v", got)
	}

	// NFO 应用：清空显示标题后由本地资料填空。
	if _, err := NewVideoDetailService(people, collections).UpdateVideoDetails(VideoDetailsUpdate{VideoID: video.ID}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), []byte(`<movie><title>霸王别姬</title></movie>`), 0644); err != nil {
		t.Fatal(err)
	}
	plantStaleTitleSortKey(t, video.ID)
	metadata := NewLocalMetadataService(dataDir, people, collections)
	if _, err := metadata.ApplyDefaults(video.ID); err != nil {
		t.Fatal(err)
	}
	if got := requireTitleSortKeyCurrent(t, "NFO 应用", video.ID); got.DisplayTitle != "霸王别姬" {
		t.Fatalf("NFO 没应用: %+v", got)
	}
}

func TestTitleSortKeyGuardLostMediaRelink(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	trashed, _ := lostTestScannerTrashVideo(t, filepath.Join(root, "old", "阿凡达.mp4"), 6)
	newPath := filepath.Join(root, "new", "霸王别姬.mp4")
	lostTestWriteFile(t, newPath, 6)
	plantStaleTitleSortKey(t, trashed.ID)
	result, err := service.ApplyLostRelink(lostTestContext(), LostRelinkApplyRequest{Kind: LostMediaKindVideo,
		Pairs: []LostRelinkPair{{ID: trashed.ID, NewPath: newPath}}})
	if err != nil || result.Done != 1 {
		t.Fatalf("应找回 1 条: %+v err=%v", result, err)
	}
	if got := requireTitleSortKeyCurrent(t, "失联找回", trashed.ID); got.Name != "霸王别姬.mp4" {
		t.Fatalf("找回没改名: %+v", got)
	}
}

func TestTitleSortKeyGuardConsolidationMigration(t *testing.T) {
	s, p, a, _, _ := prepareConsolidationRun(t, "near")
	plantStaleTitleSortKey(t, a.ID)
	result, err := s.StartConsolidation(context.Background(), p.PreviewID, false)
	if err != nil {
		t.Fatal(err)
	}
	if state := waitConsolidation(t, s, result.ID); state.Status != "completed" {
		t.Fatalf("%+v", state)
	}
	if got := requireTitleSortKeyCurrent(t, "整理迁移", a.ID); got.Path != p.Items[0].DestinationPath {
		t.Fatalf("迁移没生效: %+v", got)
	}
}
