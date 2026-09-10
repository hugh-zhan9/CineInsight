package migrator

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// seedSource 造一份覆盖面尽量宽的夹具：主表、关联表、隐式多对多、软删除行。
// 迁移器出问题时，最容易悄悄漏掉的就是后两类。
func seedSource(t *testing.T, db *gorm.DB) (videoIDs []uint, tagID uint) {
	t.Helper()

	tag := models.Tag{Name: "夜景", Color: "#0f8f82"}
	if err := db.Create(&tag).Error; err != nil {
		t.Fatalf("建标签失败: %v", err)
	}

	for index := 0; index < 3; index++ {
		video := models.Video{
			Name:      "v.mp4",
			Path:      "/lib/v" + string(rune('a'+index)) + ".mp4",
			Directory: "/lib",
			Size:      int64(index+1) * 100,
			PlayCount: index,
		}
		if err := db.Create(&video).Error; err != nil {
			t.Fatalf("建视频失败: %v", err)
		}
		videoIDs = append(videoIDs, video.ID)
	}

	// 隐式多对多：漏掉这张表，迁移后标签关联会全丢，而主表行数看起来完全正常。
	if err := db.Model(&models.Video{ID: videoIDs[0]}).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("挂标签失败: %v", err)
	}

	// 软删除行：回收站与"否认后不得重复确认"都依赖它存在。
	if err := db.Delete(&models.Video{}, videoIDs[2]).Error; err != nil {
		t.Fatalf("软删除失败: %v", err)
	}
	if err := db.Create(&models.VideoTrashEntry{
		VideoID: videoIDs[2], VideoName: "v.mp4", OriginalPath: "/lib/vc.mp4",
		State: "deleted", CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("建回收站条目失败: %v", err)
	}

	if err := db.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2.0}).Error; err != nil {
		t.Fatalf("建设置失败: %v", err)
	}

	seedBlobHeavyRows(t, db, videoIDs)
	// 手写夹具覆盖不到的表由反射补一行：往返比对是逐表比行数，空表永远 0 == 0，
	// 一张表整张被漏掉也照样"通过"（N-1 就是这样藏住的）。
	seedEveryTable(t, db)
	return videoIDs, tag.ID
}

// seedEveryTable 保证 AllModels() 里每张表都至少有一行。
//
// 用反射而不是手写 45 张表：手写的清单一定会跟着模型漂移，而"每张表都非空"
// 这件事必须对**将来新加的表**也成立，否则下一张新表又会带着同一个盲区上线。
//
// 造数策略：主键留给自增；外键列指向本库里已经存在的那行；其余 not null 且没有
// 默认值的列填一个最小非零值。填不出合法行的表（唯一键冲突、check 约束等）会
// 直接 t.Fatalf——那说明这里要为它加一条专门的造数分支，而不是静默跳过。
func seedEveryTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	namer := schema.NamingStrategy{}
	cache := &sync.Map{}
	for _, model := range models.AllModels() {
		if countUnscoped(t, db, model) > 0 {
			continue
		}
		sch, err := schema.Parse(model, cache, namer)
		if err != nil {
			t.Fatalf("解析模型 %T 失败: %v", model, err)
		}
		row := reflect.New(sch.ModelType)
		fillMinimalRow(t, db, sch, row.Elem())
		if err := db.Create(row.Interface()).Error; err != nil {
			t.Fatalf("为表 %s 造最小合法行失败（需要为它加一条专门的造数分支）: %v", sch.Table, err)
		}
		if countUnscoped(t, db, model) == 0 {
			t.Fatalf("表 %s 造完仍然是空的", sch.Table)
		}
	}
}

// fillMinimalRow 按 GORM 解析出的字段元信息填一行最小合法数据。
func fillMinimalRow(t *testing.T, db *gorm.DB, sch *schema.Schema, row reflect.Value) {
	t.Helper()
	// 先认出哪些列是外键，以及它们各自指向哪张表。
	foreignKeys := map[string]*schema.Schema{}
	for _, rel := range sch.Relationships.BelongsTo {
		for _, ref := range rel.References {
			if ref.ForeignKey != nil && ref.ForeignKey.Schema == sch {
				foreignKeys[ref.ForeignKey.Name] = rel.FieldSchema
			}
		}
	}

	for _, field := range sch.Fields {
		if field.PrimaryKey && field.AutoIncrement {
			continue
		}
		// 关联字段本身（嵌套 struct / slice）不填：填了 GORM 会级联插入。
		if _, isRelation := sch.Relationships.Relations[field.Name]; isRelation {
			continue
		}
		target := row.FieldByIndex(field.StructField.Index)
		if !target.CanSet() {
			continue
		}
		if parent, isFK := foreignKeys[field.Name]; isFK {
			setForeignKey(t, db, parent, target)
			continue
		}
		if field.PrimaryKey {
			// 复合主键（video_people 这类）：没有自增，必须给个非零值。
			setMinimalScalar(target, 1)
			continue
		}
		if field.NotNull && !field.HasDefaultValue {
			setMinimalScalar(target, 1)
		}
	}
}

// setForeignKey 把外键列指向父表里已经存在的那一行；父表为空则报错，
// 因为那意味着 AllModels() 的顺序把子表排到了父表前面（N-1 的形状）。
func setForeignKey(t *testing.T, db *gorm.DB, parent *schema.Schema, target reflect.Value) {
	t.Helper()
	var ids []uint
	if err := db.Unscoped().Table(parent.Table).Order("id ASC").Limit(1).Pluck("id", &ids).Error; err != nil {
		t.Fatalf("读取父表 %s 的主键失败: %v", parent.Table, err)
	}
	if len(ids) == 0 {
		t.Fatalf("父表 %s 还没有数据，说明 AllModels() 把子表排在了它前面", parent.Table)
	}
	setMinimalScalar(target, int64(ids[0]))
}

// setMinimalScalar 按字段类型填一个最小非零值。指针字段就地分配。
func setMinimalScalar(target reflect.Value, value int64) {
	if target.Kind() == reflect.Ptr {
		if target.IsNil() {
			target.Set(reflect.New(target.Type().Elem()))
		}
		setMinimalScalar(target.Elem(), value)
		return
	}
	switch target.Kind() {
	case reflect.String:
		target.SetString("x")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if target.Type() == reflect.TypeOf(time.Duration(0)) {
			target.SetInt(value)
			return
		}
		target.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		target.SetUint(uint64(value))
	case reflect.Float32, reflect.Float64:
		target.SetFloat(1)
	case reflect.Bool:
		target.SetBool(false)
	case reflect.Slice:
		if target.Type().Elem().Kind() == reflect.Uint8 {
			target.SetBytes([]byte{1})
		}
	case reflect.Struct:
		if target.Type() == reflect.TypeOf(time.Time{}) {
			target.Set(reflect.ValueOf(time.Unix(1_700_000_000, 0).UTC()))
		}
	}
}

// seedBlobHeavyRows 给 2026-09 新加的六张表各造一行非空数据。
//
// 存在的理由：往返比对是逐表比行数，表里没有行的时候 0 == 0 永远成立——
// 一张新表就算被迁移器整张漏掉也照样"通过"。这里额外把三个 BLOB 列填成
// 可辨识的字节序列，让 TestMigrateRoundTripPreservesBinaryColumns 能逐字节验。
func seedBlobHeavyRows(t *testing.T, db *gorm.DB, videoIDs []uint) {
	t.Helper()

	// 播放代理（P-006）：video_id 唯一 + last_used_at 索引。
	if err := db.Create(&models.VideoPlaybackProxy{
		VideoID: videoIDs[0], SourceSize: 12345, SourceModTimeNS: 1_700_000_000_123_000_000,
		Strategy: models.PlaybackProxyStrategyTranscode, Status: models.PlaybackProxyStatusReady,
		OutputSize: 6789, LastUsedAt: time.Unix(1_700_000_100, 0).UTC(), LastError: "",
	}).Error; err != nil {
		t.Fatalf("建播放代理失败: %v", err)
	}

	// 帧哈希序列（P-008）：hashes 是 uint64 序列的 BLOB。
	if err := db.Create(&models.VideoFrameHashSequence{
		VideoID: videoIDs[0], IntervalMS: 2000, Hashes: frameHashSeedBytes(),
		FrameCount: 8, SourceSize: 12345, SourceModTimeNS: 1_700_000_000_123_000_000,
		ComputedAt: time.Unix(1_700_000_200, 0).UTC(),
	}).Error; err != nil {
		t.Fatalf("建帧哈希序列失败: %v", err)
	}

	// 截取片段忽略记录（P-008）。
	if err := db.Create(&models.ClipDismissal{
		VideoFullID: videoIDs[0], VideoClipID: videoIDs[1],
		FullSourceSize: 12345, FullSourceModTimeNS: 1_700_000_000_123_000_000,
		ClipSourceSize: 234, ClipSourceModTimeNS: 1_700_000_000_456_000_000,
	}).Error; err != nil {
		t.Fatalf("建截取忽略记录失败: %v", err)
	}

	// 人脸三张表（P-012）：centroid 与 embedding 都是 2048 字节 float32×512。
	person := models.Person{DisplayName: "迁移测试人物"}
	if err := db.Create(&person).Error; err != nil {
		t.Fatalf("建人物失败: %v", err)
	}
	cluster := models.FaceCluster{
		Status: "named", PersonID: &person.ID, Centroid: faceVectorSeedBytes(0x5a),
		ObservationCount: 1,
	}
	if err := db.Create(&cluster).Error; err != nil {
		t.Fatalf("建人脸簇失败: %v", err)
	}
	frameMS := int64(4000)
	observation := models.FaceObservation{
		MediaKind: "video", MediaID: videoIDs[0], SourceFingerprint: "12345:1700000000123000000",
		FrameMS: &frameMS, BBox: "0.1,0.2,0.3,0.4", BBoxHash: "0123456789abcdef",
		Quality: 0.875, Embedding: faceVectorSeedBytes(0xa5), CropPath: "obs-1.jpg",
		ClusterID: &cluster.ID, AppendStatus: "pending",
	}
	if err := db.Create(&observation).Error; err != nil {
		t.Fatalf("建人脸观测失败: %v", err)
	}
	if err := db.Model(&models.FaceCluster{}).Where("id = ?", cluster.ID).
		Update("representative_observation_id", observation.ID).Error; err != nil {
		t.Fatalf("回填簇代表观测失败: %v", err)
	}
	if err := db.Create(&models.FacePersonCandidate{
		ClusterID: cluster.ID, PersonID: person.ID, Similarity: 0.6125,
	}).Error; err != nil {
		t.Fatalf("建簇人物候选失败: %v", err)
	}
}

// frameHashSeedBytes 造一段可辨识的 uint64 序列字节（8 帧 × 8 字节）。
func frameHashSeedBytes() []byte {
	buffer := make([]byte, 0, 64)
	for frame := 0; frame < 8; frame++ {
		hash := uint64(0x0123456789abcdef) ^ (uint64(frame) << 8)
		encoded := make([]byte, 8)
		binary.LittleEndian.PutUint64(encoded, hash)
		buffer = append(buffer, encoded...)
	}
	return buffer
}

// faceVectorSeedBytes 造 2048 字节（float32×512）的向量，字节里刻意带 0x00 与 0xff：
// 有些驱动会在 BLOB 里被 NUL 截断，全非零的填充测不出来。
func faceVectorSeedBytes(salt byte) []byte {
	buffer := make([]byte, 2048)
	for index := range buffer {
		buffer[index] = byte(index) ^ salt
	}
	buffer[0], buffer[1] = 0x00, 0xff
	buffer[len(buffer)-1] = 0x00
	return buffer
}

func countUnscoped(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var count int64
	if err := db.Unscoped().Model(model).Count(&count).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	return count
}

func TestMigrateCopiesEverythingAndLeavesSourceUntouched(t *testing.T) {
	source := dbtest.Open(t)
	target := dbtest.Open(t)
	videoIDs, tagID := seedSource(t, source)

	sourceVideosBefore := countUnscoped(t, source, &models.Video{})
	var progressed []Progress
	result, err := Migrate(context.Background(), Options{
		Source: source, Target: target,
		TargetBackend: backendOfTest(),
		OnProgress:    func(p Progress) { progressed = append(progressed, p) },
	})
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 源库全程只读——这是"改回配置重启即可回滚"的前提。
	if got := countUnscoped(t, source, &models.Video{}); got != sourceVideosBefore {
		t.Fatalf("迁移不得改动源库: before=%d after=%d", sourceVideosBefore, got)
	}

	// 软删除行必须一起搬：3 条视频里有 1 条是软删除的。
	if got := countUnscoped(t, target, &models.Video{}); got != 3 {
		t.Fatalf("目标库视频数应为 3（含软删除），实际 %d", got)
	}
	var active int64
	if err := target.Model(&models.Video{}).Count(&active).Error; err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if active != 2 {
		t.Fatalf("软删除状态应被保留，活跃数应为 2，实际 %d", active)
	}

	// 主键必须保留，否则关联表和回收站恢复路径全部错位。
	var migrated models.Video
	if err := target.Unscoped().First(&migrated, videoIDs[0]).Error; err != nil {
		t.Fatalf("主键未保留，按原 ID 查不到: %v", err)
	}

	// 隐式多对多。
	var linked models.Video
	if err := target.Preload("Tags").First(&linked, videoIDs[0]).Error; err != nil {
		t.Fatalf("读取迁移后的视频失败: %v", err)
	}
	if len(linked.Tags) != 1 || linked.Tags[0].ID != tagID {
		t.Fatalf("标签关联未迁移: %+v", linked.Tags)
	}

	if result.RowCounts["video_tags"] != 1 {
		t.Fatalf("关联表行数应为 1: %+v", result.RowCounts)
	}
	if len(progressed) != result.Tables {
		t.Fatalf("每张表都应上报一次进度: got=%d tables=%d", len(progressed), result.Tables)
	}
	if last := progressed[len(progressed)-1]; last.TableIndex != last.TableTotal {
		t.Fatalf("最后一次进度应到达总数: %+v", last)
	}
}

func TestMigrateRoundTripPreservesRowCountsAndKeys(t *testing.T) {
	source := dbtest.Open(t)
	middle := dbtest.Open(t)
	back := dbtest.Open(t)
	videoIDs, _ := seedSource(t, source)
	wish := models.WatchlistEntry{Title: "沙丘（1984）", CreatedAt: time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)}
	if err := source.Create(&wish).Error; err != nil {
		t.Fatal(err)
	}

	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{
			Source: step.from, Target: step.to, TargetBackend: backendOfTest(),
		}); err != nil {
			t.Fatalf("迁移失败: %v", err)
		}
	}

	// 往返之后逐表比对行数与主键集合。
	var restoredWish models.WatchlistEntry
	if err := back.First(&restoredWish, wish.ID).Error; err != nil {
		t.Fatalf("往返后想看记录丢失: %v", err)
	}
	if restoredWish.Title != wish.Title || !restoredWish.CreatedAt.Equal(wish.CreatedAt) {
		t.Fatalf("往返后想看片名或添加时间变化: got=%+v want=%+v", restoredWish, wish)
	}
	//
	// 先钉住"每张表都非空"：行数比对在空表上是 0 == 0，永远成立——一张表整张
	// 被迁移器漏掉也照样通过。这一条让夹具的覆盖面本身成为断言的一部分，
	// 新加表忘了造数会在这里直接失败，而不是悄悄留下一个盲区。
	for _, model := range models.AllModels() {
		if countUnscoped(t, source, model) == 0 {
			t.Fatalf("源库里 %T 是空表，行数往返比对对它没有意义（seedEveryTable 应当已经补上）", model)
		}
	}
	for _, model := range models.AllModels() {
		want := countUnscoped(t, source, model)
		got := countUnscoped(t, back, model)
		if want != got {
			t.Fatalf("往返后行数不一致 %T: source=%d back=%d", model, want, got)
		}
	}
	var ids []uint
	if err := back.Unscoped().Model(&models.Video{}).Order("id ASC").Pluck("id", &ids).Error; err != nil {
		t.Fatalf("读取主键失败: %v", err)
	}
	if len(ids) != len(videoIDs) {
		t.Fatalf("往返后主键数量不一致: %v vs %v", ids, videoIDs)
	}
	for index, id := range ids {
		if id != videoIDs[index] {
			t.Fatalf("往返后主键变了: %v vs %v", ids, videoIDs)
		}
	}
}

func TestMigrateInsertsWorkAfterMigrationOnTargetBackend(t *testing.T) {
	source := dbtest.Open(t)
	target := dbtest.Open(t)
	seedSource(t, source)

	if _, err := Migrate(context.Background(), Options{
		Source: source, Target: target, TargetBackend: backendOfTest(),
	}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 这条是序列重置的验收：迁移带着显式 ID 插入，不推进 Postgres 的 BIGSERIAL。
	// 不重置的话，迁移之后新建的第一条记录就撞主键——而且报错发生在迁移结束很久
	// 之后，现场早没了。
	fresh := models.Video{Name: "new.mp4", Path: "/lib/new.mp4", Directory: "/lib"}
	if err := target.Create(&fresh).Error; err != nil {
		t.Fatalf("迁移后新建记录失败（Postgres 上多半是序列没重置）: %v", err)
	}
	if fresh.ID == 0 {
		t.Fatalf("新记录未分配主键")
	}
}

func TestPreflightRefusesNonEmptyAndHalfMigratedTargets(t *testing.T) {
	source := dbtest.Open(t)
	seedSource(t, source)

	t.Run("目标非空时拒绝", func(t *testing.T) {
		target := dbtest.Open(t)
		if err := target.Create(&models.Tag{Name: "占位"}).Error; err != nil {
			t.Fatalf("建行失败: %v", err)
		}
		err := Preflight(target)
		if err == nil {
			t.Fatalf("目标非空应被拒绝")
		}
		var notEmpty *ErrTargetNotEmpty
		if !asErrTargetNotEmpty(err, &notEmpty) {
			t.Fatalf("应返回 ErrTargetNotEmpty，实际 %v", err)
		}
		if _, err := Migrate(context.Background(), Options{
			Source: source, Target: target, TargetBackend: backendOfTest(),
		}); err == nil {
			t.Fatalf("Migrate 也应拒绝非空目标")
		}
	})

	t.Run("半迁移的目标必须先清空", func(t *testing.T) {
		target := dbtest.Open(t)
		if err := target.AutoMigrate(&migrationMarker{}); err != nil {
			t.Fatalf("建标记表失败: %v", err)
		}
		if err := target.Create(&migrationMarker{Completed: false}).Error; err != nil {
			t.Fatalf("写标记失败: %v", err)
		}
		if err := Preflight(target); err != ErrTargetHalfMigrated {
			t.Fatalf("半迁移目标应被拒绝，实际 %v", err)
		}
	})
}

func TestMigrateSkipsSemanticIndexStateSoTargetReportsRebuildNeeded(t *testing.T) {
	source := dbtest.Open(t)
	target := dbtest.Open(t)
	seedSource(t, source)

	if _, err := Migrate(context.Background(), Options{
		Source: source, Target: target, TargetBackend: backendOfTest(),
	}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 语义索引的元数据表不在 AllModels 里（由 PrepareSemanticVectorStorage 单独建），
	// 所以按 AllModels 驱动的复制天然不会搬它们。向量本身更是 pgvector 专有、
	// 跨不过来。这条测试把"不搬"钉成契约：一旦有人把这些表加进 AllModels，
	// 目标库就会声称索引存在却搜不出东西。
	for _, name := range []string{"video_semantic_indices", "video_semantic_vectors", "image_semantic_vectors"} {
		if target.Migrator().HasTable(name) {
			var count int64
			if err := target.Table(name).Count(&count).Error; err == nil && count > 0 {
				t.Fatalf("语义索引状态不应跨后端携带，%s 有 %d 行", name, count)
			}
		}
	}
}

func backendOfTest() database.Backend {
	if dbtest.IsPostgres() {
		return database.BackendPostgres
	}
	return database.BackendSQLite
}

func asErrTargetNotEmpty(err error, target **ErrTargetNotEmpty) bool {
	if casted, ok := err.(*ErrTargetNotEmpty); ok {
		*target = casted
		return true
	}
	return false
}

// 三个 BLOB 列必须逐字节往返：SQLite 的 blob 与 Postgres 的 bytea 是两种类型，
// 中间任何一次转换出错（NUL 截断、hex 编码、驱动按文本处理）都会让向量与
// 哈希序列在迁移之后变成一堆无意义的字节，而行数比对完全看不出来。
func TestMigrateRoundTripPreservesBinaryColumns(t *testing.T) {
	source := dbtest.Open(t)
	middle := dbtest.Open(t)
	back := dbtest.Open(t)
	seedSource(t, source)

	for _, step := range []struct{ from, to *gorm.DB }{{source, middle}, {middle, back}} {
		if _, err := Migrate(context.Background(), Options{
			Source: step.from, Target: step.to, TargetBackend: backendOfTest(),
		}); err != nil {
			t.Fatalf("迁移失败: %v", err)
		}
	}

	var sequence models.VideoFrameHashSequence
	if err := back.Unscoped().First(&sequence).Error; err != nil {
		t.Fatalf("往返后读不到帧哈希序列: %v", err)
	}
	wantHashes := frameHashSeedBytes()
	if !bytes.Equal(sequence.Hashes, wantHashes) {
		t.Fatalf("hashes 往返后不一致: got=%d bytes want=%d bytes", len(sequence.Hashes), len(wantHashes))
	}
	if sequence.FrameCount != 8 || sequence.IntervalMS != 2000 {
		t.Fatalf("帧哈希标量列往返后不一致: %+v", sequence)
	}

	var observation models.FaceObservation
	if err := back.Unscoped().First(&observation).Error; err != nil {
		t.Fatalf("往返后读不到人脸观测: %v", err)
	}
	wantEmbedding := faceVectorSeedBytes(0xa5)
	if len(observation.Embedding) != 2048 {
		t.Fatalf("embedding 长度应为 2048，实际 %d", len(observation.Embedding))
	}
	if !bytes.Equal(observation.Embedding, wantEmbedding) {
		t.Fatalf("embedding 往返后不一致")
	}
	if observation.BBoxHash != "0123456789abcdef" || observation.AppendStatus != "pending" {
		t.Fatalf("人脸观测标量列往返后不一致: %+v", observation)
	}

	var cluster models.FaceCluster
	if err := back.Unscoped().First(&cluster).Error; err != nil {
		t.Fatalf("往返后读不到人脸簇: %v", err)
	}
	wantCentroid := faceVectorSeedBytes(0x5a)
	if !bytes.Equal(cluster.Centroid, wantCentroid) {
		t.Fatalf("centroid 往返后不一致: got=%d bytes want=%d bytes", len(cluster.Centroid), len(wantCentroid))
	}
	if cluster.RepresentativeObservationID == nil || *cluster.RepresentativeObservationID != observation.ID {
		t.Fatalf("簇代表观测往返后不一致: %+v", cluster)
	}

	// 三张只有标量列的新表也各自确认一行搬到了，且关键列没变形。
	var proxy models.VideoPlaybackProxy
	if err := back.Unscoped().First(&proxy).Error; err != nil {
		t.Fatalf("往返后读不到播放代理: %v", err)
	}
	if proxy.Strategy != models.PlaybackProxyStrategyTranscode || proxy.Status != models.PlaybackProxyStatusReady {
		t.Fatalf("播放代理列往返后不一致: %+v", proxy)
	}
	if proxy.SourceModTimeNS != 1_700_000_000_123_000_000 || proxy.OutputSize != 6789 {
		t.Fatalf("播放代理的大整数列往返后不一致: %+v", proxy)
	}
	var dismissal models.ClipDismissal
	if err := back.Unscoped().First(&dismissal).Error; err != nil {
		t.Fatalf("往返后读不到截取忽略记录: %v", err)
	}
	if dismissal.ClipSourceModTimeNS != 1_700_000_000_456_000_000 {
		t.Fatalf("截取忽略记录往返后不一致: %+v", dismissal)
	}
	var candidate models.FacePersonCandidate
	if err := back.Unscoped().First(&candidate).Error; err != nil {
		t.Fatalf("往返后读不到簇人物候选: %v", err)
	}
	if candidate.Similarity != 0.6125 {
		t.Fatalf("相似度往返后不一致: %+v", candidate)
	}
}
