package migrator

import (
	"context"
	"testing"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
)

// 多版本聚合（D-MW-VERSIONS）的三张表。成员表带 position > 0 与视频主键，忽略表带 low < high
// 检查约束，seedEveryTable 的最小行未必合法，所以在它之前显式造贴近真实的数据。
func versionGroupTables() []any {
	return []any{&models.VideoVersionGroup{}, &models.VideoVersionMember{}, &models.VideoVersionSuggestionDismissal{}}
}

// seedVersionGroupRows 造一个组：活跃成员 videoIDs[0] 与软删成员 videoIDs[2]（软删成员行保留），
// 以及一条忽略配对。revision 用非 1 的值，验证往返不被默认值改写。
func seedVersionGroupRows(t *testing.T, db *gorm.DB, videoIDs []uint) {
	t.Helper()
	group := models.VideoVersionGroup{Title: "版本组夹具", Revision: 3}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("建版本组夹具失败: %v", err)
	}
	members := []models.VideoVersionMember{
		{VideoID: videoIDs[0], GroupID: group.ID, Label: "原版", Position: 1},
		{VideoID: videoIDs[2], GroupID: group.ID, Label: "高清版", Position: 2},
	}
	if err := db.Omit("Video", "Group").Create(&members).Error; err != nil {
		t.Fatalf("建版本组成员夹具失败: %v", err)
	}
	dismissal := models.VideoVersionSuggestionDismissal{VideoLowID: videoIDs[0], VideoHighID: videoIDs[1]}
	if err := db.Create(&dismissal).Error; err != nil {
		t.Fatalf("建建议忽略夹具失败: %v", err)
	}
}

func TestMigrateCarriesVersionGroupTables(t *testing.T) {
	source := dbtest.Open(t)
	middle := dbtest.Open(t)
	back := dbtest.Open(t)
	videoIDs, _ := seedSource(t, source)
	for _, model := range versionGroupTables() {
		if countUnscoped(t, source, model) == 0 {
			t.Fatalf("夹具里新表 %T 必须至少有一行", model)
		}
	}
	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{
			Source: step.from, Target: step.to, TargetBackend: backendOfTest(),
		}); err != nil {
			t.Fatalf("迁移失败: %v", err)
		}
	}
	for _, model := range versionGroupTables() {
		if want, got := countUnscoped(t, source, model), countUnscoped(t, back, model); want != got {
			t.Fatalf("往返后新表 %T 行数不一致: source=%d back=%d", model, want, got)
		}
	}
	var group models.VideoVersionGroup
	if err := back.Where("title = ?", "版本组夹具").First(&group).Error; err != nil {
		t.Fatal(err)
	}
	if group.Revision != 3 {
		t.Fatalf("revision 往返后应原样保留: %+v", group)
	}
	var members []models.VideoVersionMember
	if err := back.Where("group_id = ?", group.ID).Order("position ASC").Find(&members).Error; err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].VideoID != videoIDs[0] || members[0].Label != "原版" ||
		members[1].VideoID != videoIDs[2] || members[1].Position != 2 {
		t.Fatalf("成员（含软删视频的成员行）往返后不一致: %+v", members)
	}
	var dismissal models.VideoVersionSuggestionDismissal
	if err := back.Where("video_low_id = ? AND video_high_id = ?", videoIDs[0], videoIDs[1]).First(&dismissal).Error; err != nil {
		t.Fatalf("忽略配对往返后丢失: %v", err)
	}
}
