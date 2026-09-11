package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 人脸审阅（D-019、D-015 的写入部分、D-020）。
//
// 这个文件是 video_people / image_people 在人脸链路上的唯一写入点，而且每一次写入
// 都由用户的一次动作触发：命名、关联、忽略、确认追加、忽略追加。分析服务只产出
// 观测、簇与候选，一行关系都不写（requirements D-4f、AC-11）。
//
// 因此这里也不提供「全部接受」之类的批量自动化：一次动作对应一个簇，用户看得见
// 自己在确认什么。

var (
	// ErrFaceClusterNotFound 对应设计 7.3.1 的 cluster_not_found。
	ErrFaceClusterNotFound = errors.New("cluster_not_found")
	// ErrFaceClusterNotUnnamed 对应 cluster_not_unnamed：命名/关联/忽略都只对
	// 未命名簇有意义，两个人同时给一个簇命名时第二个人拿到的就是这个错。
	ErrFaceClusterNotUnnamed = errors.New("cluster_not_unnamed")
	// ErrFaceClusterNotNamed 是确认追加的前置：追加候选要写给某个人物，
	// 簇还没命名就没有这个人物。
	ErrFaceClusterNotNamed = errors.New("cluster_not_named")
	// ErrFacePersonNameInvalid 对应 person_name_invalid。
	ErrFacePersonNameInvalid = errors.New("person_name_invalid")
	// ErrFacePersonNotFound 是关联到现有人物时人物不存在。
	ErrFacePersonNotFound = errors.New("person_not_found")
)

// FaceClusterFilter 是审阅面板的查询条件（7.2 `ListFaceClusters(filter)`）。
// 两个字段都留空就是不筛。
type FaceClusterFilter struct {
	// Status 是簇状态：unnamed / named / ignored。
	Status string `json:"status"`
	// MediaKind 只保留至少有一条该类媒体观测的簇：video / image。
	MediaKind string `json:"media_kind"`
}

// FaceClusterCandidateView 是「这个簇可能是这个人」的建议（D-017）。
// 只是建议：相似度再高也不会自动写关系。
type FaceClusterCandidateView struct {
	PersonID    uint    `json:"person_id"`
	DisplayName string  `json:"display_name"`
	Similarity  float64 `json:"similarity"`
}

// FaceClusterMediaView 是追加候选涉及的一件媒体（D-019：人物 + 新增媒体列表）。
type FaceClusterMediaView struct {
	MediaKind string `json:"media_kind"`
	MediaID   uint   `json:"media_id"`
	Name      string `json:"name"`
}

// FaceClusterView 是审阅面板上的一张簇卡片。
type FaceClusterView struct {
	ID     uint   `json:"id"`
	Status string `json:"status"`
	// ObservationCount / VideoCount / ImageCount 都由观测行现算，不读簇上的计数列：
	// 面板上的数字是用户判断「这一簇值不值得命名」的依据，宁可慢一点也要是真的。
	ObservationCount int `json:"observation_count"`
	VideoCount       int `json:"video_count"`
	ImageCount       int `json:"image_count"`
	// RepresentativeObservationID 是簇头像的来源，前端据此取 /preview/face-crop/{id}；
	// 0 表示这一簇还没有可用的代表观测。
	RepresentativeObservationID uint `json:"representative_observation_id"`
	// PersonID / PersonName 只在 named 簇上有值。
	PersonID   uint                       `json:"person_id"`
	PersonName string                     `json:"person_name"`
	Candidates []FaceClusterCandidateView `json:"candidates"`
	// AppendPendingCount / AppendPendingMedia 是已命名簇后来吸收到的新观测
	// （D-019 的追加候选）：确认之后才写关系。
	AppendPendingCount int                    `json:"append_pending_count"`
	AppendPendingMedia []FaceClusterMediaView `json:"append_pending_media"`
}

// FaceReviewService 把审阅动作转成关系写入。无状态：每个动作一个事务。
type FaceReviewService struct {
	images      *ManagedImageService
	resolveCrop func(*gorm.DB, uint) (*FaceCropAsset, error)
}

func NewFaceReviewService(dataDir string, analysis *FaceAnalysisService) *FaceReviewService {
	return &FaceReviewService{images: NewManagedImageService(dataDir), resolveCrop: analysis.resolveFaceCrop}
}

// ListFaceClusters 返回簇视图，按观测数降序（观测多的簇先看，命名一次收益最大）。
func (s *FaceReviewService) ListFaceClusters(ctx context.Context, filter FaceClusterFilter) ([]FaceClusterView, error) {
	// 人物被删除的簇先拉回未命名，否则它既不出现在面板上也过不了命名校验。
	if err := reconcileFaceClusterPeople(ctx); err != nil {
		return nil, fmt.Errorf("reconcile face clusters: %w", err)
	}

	query := database.DB.WithContext(ctx).Model(&models.FaceCluster{}).
		Select("id", "status", "person_id", "centroid", "representative_observation_id")
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.MediaKind != "" {
		observations := database.DB.Model(&models.FaceObservation{}).Select("1").
			Where("face_observations.cluster_id = face_clusters.id").
			Where("face_observations.media_kind = ?", filter.MediaKind)
		query = query.Where("EXISTS (?)", observations)
	}
	var clusters []models.FaceCluster
	if err := query.Order("id ASC").Find(&clusters).Error; err != nil {
		return nil, fmt.Errorf("list face clusters: %w", err)
	}
	if len(clusters) == 0 {
		return []FaceClusterView{}, nil
	}

	clusterIDs := make([]uint, 0, len(clusters))
	for _, cluster := range clusters {
		clusterIDs = append(clusterIDs, cluster.ID)
	}
	counts, err := loadFaceClusterCounts(ctx, clusterIDs)
	if err != nil {
		return nil, err
	}
	pending, err := loadFaceClusterPendingMedia(ctx, clusterIDs)
	if err != nil {
		return nil, err
	}
	candidates, err := loadFaceClusterCandidates(ctx, clusters)
	if err != nil {
		return nil, err
	}
	names, err := loadFaceClusterPersonNames(ctx, clusters)
	if err != nil {
		return nil, err
	}

	views := make([]FaceClusterView, 0, len(clusters))
	for _, cluster := range clusters {
		count := counts[cluster.ID]
		view := FaceClusterView{
			ID:                 cluster.ID,
			Status:             cluster.Status,
			ObservationCount:   count.Observations,
			VideoCount:         count.Videos,
			ImageCount:         count.Images,
			Candidates:         candidates[cluster.ID],
			AppendPendingCount: count.Pending,
			AppendPendingMedia: pending[cluster.ID],
		}
		if cluster.RepresentativeObservationID != nil {
			view.RepresentativeObservationID = *cluster.RepresentativeObservationID
		}
		if cluster.PersonID != nil {
			view.PersonID = *cluster.PersonID
			view.PersonName = names[*cluster.PersonID]
		}
		if view.Candidates == nil {
			view.Candidates = []FaceClusterCandidateView{}
		}
		if view.AppendPendingMedia == nil {
			view.AppendPendingMedia = []FaceClusterMediaView{}
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].ObservationCount != views[j].ObservationCount {
			return views[i].ObservationCount > views[j].ObservationCount
		}
		return views[i].ID < views[j].ID
	})
	return views, nil
}

// NameFaceCluster 给未命名簇建一个新人物并把簇内媒体关联上去（7.3.1）。
func (s *FaceReviewService) NameFaceCluster(ctx context.Context, clusterID uint, displayName, originalName string) (FaceClusterView, error) {
	displayName, originalName, err := validatePersonNames(displayName, originalName)
	if err != nil {
		log.Printf("Face cluster naming rejected cluster_id=%d err=%v", clusterID, err)
		return FaceClusterView{}, ErrFacePersonNameInvalid
	}
	if err := reconcileFaceClusterPeople(ctx); err != nil {
		return FaceClusterView{}, fmt.Errorf("reconcile face clusters: %w", err)
	}

	var personID uint
	var imported managedImageImport
	var written int64
	err = database.Transaction(func(tx *gorm.DB) error {
		cluster, err := lockFaceCluster(ctx, tx, clusterID)
		if err != nil {
			return err
		}
		if cluster.Status != models.FaceClusterStatusUnnamed {
			return ErrFaceClusterNotUnnamed
		}
		person := models.Person{DisplayName: displayName, OriginalName: originalName}
		if err := tx.WithContext(ctx).Create(&person).Error; err != nil {
			return err
		}
		personID = person.ID
		imported, err = s.importClusterAvatar(tx.WithContext(ctx), cluster, person.ID)
		if err != nil {
			return err
		}
		if imported.RelativePath != "" {
			if err := tx.Model(&person).Update("avatar_path", imported.RelativePath).Error; err != nil {
				return err
			}
		}
		written, err = writeFaceClusterRelations(ctx, tx, cluster.ID, person.ID, false)
		if err != nil {
			return err
		}
		return claimFaceCluster(ctx, tx, cluster.ID, person.ID)
	})
	if err != nil {
		s.cleanupUnreferencedAvatar(imported)
		return FaceClusterView{}, err
	}
	log.Printf("Face cluster named cluster_id=%d person_id=%d relations=%d", clusterID, personID, written)
	return s.clusterView(ctx, clusterID)
}

// LinkFaceCluster 把未命名簇关联到现有人物：同一个人被拆成多簇时（角度差异）
// 用它合并语义（4.4.4）。除了不建人物，写入与 NameFaceCluster 完全一致。
func (s *FaceReviewService) LinkFaceCluster(ctx context.Context, clusterID, personID uint) (FaceClusterView, error) {
	if err := reconcileFaceClusterPeople(ctx); err != nil {
		return FaceClusterView{}, fmt.Errorf("reconcile face clusters: %w", err)
	}
	var written int64
	err := database.Transaction(func(tx *gorm.DB) error {
		cluster, err := lockFaceCluster(ctx, tx, clusterID)
		if err != nil {
			return err
		}
		if cluster.Status != models.FaceClusterStatusUnnamed {
			return ErrFaceClusterNotUnnamed
		}
		var person models.Person
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&person, personID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrFacePersonNotFound
			}
			return err
		}
		written, err = writeFaceClusterRelations(ctx, tx, cluster.ID, person.ID, false)
		if err != nil {
			return err
		}
		return claimFaceCluster(ctx, tx, cluster.ID, person.ID)
	})
	if err != nil {
		return FaceClusterView{}, err
	}
	log.Printf("Face cluster linked cluster_id=%d person_id=%d relations=%d", clusterID, personID, written)
	return s.clusterView(ctx, clusterID)
}

// IgnoreFaceCluster 把簇标为忽略：观测全部保留（源没变就不再重新冒出来，AC-12），
// 候选删掉，一行关系都不写。
func (s *FaceReviewService) IgnoreFaceCluster(ctx context.Context, clusterID uint) error {
	if err := reconcileFaceClusterPeople(ctx); err != nil {
		return fmt.Errorf("reconcile face clusters: %w", err)
	}
	err := database.Transaction(func(tx *gorm.DB) error {
		cluster, err := lockFaceCluster(ctx, tx, clusterID)
		if err != nil {
			return err
		}
		if cluster.Status == models.FaceClusterStatusIgnored {
			// 重复忽略是同一个终态，不报错（7.2：状态机幂等）。
			return nil
		}
		if cluster.Status != models.FaceClusterStatusUnnamed {
			return ErrFaceClusterNotUnnamed
		}
		if err := tx.WithContext(ctx).Model(&models.FaceCluster{}).
			Where("id = ? AND status = ?", cluster.ID, models.FaceClusterStatusUnnamed).
			Update("status", models.FaceClusterStatusIgnored).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).Where("cluster_id = ?", cluster.ID).
			Delete(&models.FacePersonCandidate{}).Error
	})
	if err != nil {
		return err
	}
	log.Printf("Face cluster ignored cluster_id=%d", clusterID)
	return nil
}

// ConfirmFaceClusterAppend 确认追加候选：把 pending 观测涉及的媒体关联到簇的人物
// 身上，然后把这些观测置 confirmed（D-019）。这是已命名簇写关系的唯一入口。
func (s *FaceReviewService) ConfirmFaceClusterAppend(ctx context.Context, clusterID uint) error {
	var personID uint
	var written int64
	err := database.Transaction(func(tx *gorm.DB) error {
		cluster, err := lockFaceCluster(ctx, tx, clusterID)
		if err != nil {
			return err
		}
		if cluster.Status != models.FaceClusterStatusNamed || cluster.PersonID == nil {
			return ErrFaceClusterNotNamed
		}
		personID = *cluster.PersonID
		written, err = writeFaceClusterRelations(ctx, tx, cluster.ID, personID, true)
		if err != nil {
			return err
		}
		return tx.WithContext(ctx).Model(&models.FaceObservation{}).
			Where("cluster_id = ? AND append_status = ?", cluster.ID, models.FaceAppendStatusPending).
			Update("append_status", models.FaceAppendStatusConfirmed).Error
	})
	if err != nil {
		return err
	}
	log.Printf("Face cluster append confirmed cluster_id=%d person_id=%d relations=%d", clusterID, personID, written)
	return nil
}

// DismissFaceClusterAppend 忽略追加候选：pending → dismissed，同一条观测不再提示，
// 关系一行不写。簇状态不变——用户否掉的是「这些新媒体算不算这个人」，不是命名本身。
func (s *FaceReviewService) DismissFaceClusterAppend(ctx context.Context, clusterID uint) error {
	err := database.Transaction(func(tx *gorm.DB) error {
		cluster, err := lockFaceCluster(ctx, tx, clusterID)
		if err != nil {
			return err
		}
		return tx.WithContext(ctx).Model(&models.FaceObservation{}).
			Where("cluster_id = ? AND append_status = ?", cluster.ID, models.FaceAppendStatusPending).
			Update("append_status", models.FaceAppendStatusDismissed).Error
	})
	if err != nil {
		return err
	}
	log.Printf("Face cluster append dismissed cluster_id=%d", clusterID)
	return nil
}

// clusterView 取单个簇的视图，动作返回值用它（7.3.1 的返回形态）。
func (s *FaceReviewService) clusterView(ctx context.Context, clusterID uint) (FaceClusterView, error) {
	views, err := s.ListFaceClusters(ctx, FaceClusterFilter{})
	if err != nil {
		return FaceClusterView{}, err
	}
	for _, view := range views {
		if view.ID == clusterID {
			return view, nil
		}
	}
	return FaceClusterView{}, ErrFaceClusterNotFound
}

// lockFaceCluster 取簇并锁住这一行：并发命名同一个簇时，后到的那次要看到前一次
// 提交后的状态才能正确报 cluster_not_unnamed。
func lockFaceCluster(ctx context.Context, tx *gorm.DB, clusterID uint) (models.FaceCluster, error) {
	var cluster models.FaceCluster
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&cluster, clusterID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return cluster, ErrFaceClusterNotFound
	}
	if err != nil {
		return cluster, err
	}
	return cluster, nil
}

// claimFaceCluster 把簇翻成 named：状态条件写在 WHERE 里，行锁之外再兜一层，
// 影响行数为 0 就说明别人已经命名过了。同一事务里清掉候选、把观测标为已确认
// （首次命名等于确认了簇内全部观测）。
func claimFaceCluster(ctx context.Context, tx *gorm.DB, clusterID, personID uint) error {
	result := tx.WithContext(ctx).Model(&models.FaceCluster{}).
		Where("id = ? AND status = ?", clusterID, models.FaceClusterStatusUnnamed).
		Updates(map[string]interface{}{
			"status":    models.FaceClusterStatusNamed,
			"person_id": personID,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrFaceClusterNotUnnamed
	}
	if err := tx.WithContext(ctx).Where("cluster_id = ?", clusterID).
		Delete(&models.FacePersonCandidate{}).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Model(&models.FaceObservation{}).
		Where("cluster_id = ? AND append_status <> ?", clusterID, models.FaceAppendStatusConfirmed).
		Update("append_status", models.FaceAppendStatusConfirmed).Error
}

// writeFaceClusterRelations 是本仓库人脸链路上唯一写 video_people / image_people
// 的地方，只由上面五个用户动作在事务内调用。
//
// pendingOnly 为真时只看 append_status = pending 的观测（确认追加）；为假时看簇内
// 全部观测（首次命名或关联）。按 (media_kind, media_id) 去重，已存在的关系靠
// ON CONFLICT DO NOTHING 跳过，因此重复确认不会报错也不会重复写。
func writeFaceClusterRelations(ctx context.Context, tx *gorm.DB, clusterID, personID uint, pendingOnly bool) (int64, error) {
	type mediaRef struct {
		MediaKind string `gorm:"column:media_kind"`
		MediaID   uint   `gorm:"column:media_id"`
	}
	query := tx.WithContext(ctx).Model(&models.FaceObservation{}).
		Distinct("media_kind", "media_id").
		Where("cluster_id = ?", clusterID).
		Where("media_kind IN ?", []string{models.FaceMediaKindVideo, models.FaceMediaKindImage})
	if pendingOnly {
		query = query.Where("append_status = ?", models.FaceAppendStatusPending)
	}
	var refs []mediaRef
	if err := query.Scan(&refs).Error; err != nil {
		return 0, err
	}
	videoIDs := make([]uint, 0, len(refs))
	imageIDs := make([]uint, 0, len(refs))
	for _, ref := range refs {
		switch ref.MediaKind {
		case models.FaceMediaKindVideo:
			videoIDs = append(videoIDs, ref.MediaID)
		case models.FaceMediaKindImage:
			imageIDs = append(imageIDs, ref.MediaID)
		}
	}

	var written int64
	if len(videoIDs) > 0 {
		// Unscoped：软删除的视频行还在库里，关系也应当建起来（人物清理判定本就把
		// 软删除媒体的关系算作有效关系）；只有已经永久删除的媒体要跳过，否则外键报错。
		var existing []uint
		if err := tx.WithContext(ctx).Unscoped().Model(&models.Video{}).
			Where("id IN ?", videoIDs).Pluck("id", &existing).Error; err != nil {
			return written, err
		}
		if len(existing) > 0 {
			relations := make([]models.VideoPerson, 0, len(existing))
			for _, videoID := range existing {
				relations = append(relations, models.VideoPerson{VideoID: videoID, PersonID: personID})
			}
			result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&relations)
			if result.Error != nil {
				return written, result.Error
			}
			written += result.RowsAffected
		}
	}
	if len(imageIDs) > 0 {
		var existing []uint
		if err := tx.WithContext(ctx).Unscoped().Model(&models.Image{}).
			Where("id IN ?", imageIDs).Pluck("id", &existing).Error; err != nil {
			return written, err
		}
		if len(existing) > 0 {
			relations := make([]models.ImagePerson, 0, len(existing))
			for _, imageID := range existing {
				relations = append(relations, models.ImagePerson{ImageID: imageID, PersonID: personID})
			}
			result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&relations)
			if result.Error != nil {
				return written, result.Error
			}
			written += result.RowsAffected
		}
	}
	return written, nil
}

// reconcileFaceClusterPeople 把人物已被删除的簇拉回未命名（4.4.4）。
// 外键 SET NULL 只能置空 person_id，改不了 status；不修的话这些簇既不会在面板上
// 重新出现，也过不了「必须是未命名」这道校验，等于永久卡死。
func reconcileFaceClusterPeople(ctx context.Context) error {
	return database.DB.WithContext(ctx).Model(&models.FaceCluster{}).
		Where("status = ? AND person_id IS NULL", models.FaceClusterStatusNamed).
		Update("status", models.FaceClusterStatusUnnamed).Error
}

// faceClusterCounts 是一个簇的观测与媒体计数。
type faceClusterCounts struct {
	Observations int
	Videos       int
	Images       int
	Pending      int
}

func loadFaceClusterCounts(ctx context.Context, clusterIDs []uint) (map[uint]faceClusterCounts, error) {
	type aggregate struct {
		ClusterID        uint   `gorm:"column:cluster_id"`
		MediaKind        string `gorm:"column:media_kind"`
		ObservationCount int64  `gorm:"column:observation_count"`
		MediaCount       int64  `gorm:"column:media_count"`
		PendingCount     int64  `gorm:"column:pending_count"`
	}
	var rows []aggregate
	if err := database.DB.WithContext(ctx).Model(&models.FaceObservation{}).
		Select("cluster_id, media_kind, COUNT(*) AS observation_count, COUNT(DISTINCT media_id) AS media_count, SUM(CASE WHEN append_status = ? THEN 1 ELSE 0 END) AS pending_count",
			models.FaceAppendStatusPending).
		Where("cluster_id IN ?", clusterIDs).
		Group("cluster_id").Group("media_kind").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count face cluster observations: %w", err)
	}
	counts := make(map[uint]faceClusterCounts, len(clusterIDs))
	for _, row := range rows {
		entry := counts[row.ClusterID]
		entry.Observations += int(row.ObservationCount)
		entry.Pending += int(row.PendingCount)
		switch row.MediaKind {
		case models.FaceMediaKindVideo:
			entry.Videos += int(row.MediaCount)
		case models.FaceMediaKindImage:
			entry.Images += int(row.MediaCount)
		}
		counts[row.ClusterID] = entry
	}
	return counts, nil
}

// loadFaceClusterPendingMedia 列出每个簇的追加候选涉及哪些媒体（D-019）。
// 名字用 Unscoped 取：软删除的媒体也要能在卡片上认出来。
func loadFaceClusterPendingMedia(ctx context.Context, clusterIDs []uint) (map[uint][]FaceClusterMediaView, error) {
	type pendingRow struct {
		ClusterID uint   `gorm:"column:cluster_id"`
		MediaKind string `gorm:"column:media_kind"`
		MediaID   uint   `gorm:"column:media_id"`
	}
	var rows []pendingRow
	if err := database.DB.WithContext(ctx).Model(&models.FaceObservation{}).
		Distinct("cluster_id", "media_kind", "media_id").
		Where("cluster_id IN ?", clusterIDs).
		Where("append_status = ?", models.FaceAppendStatusPending).
		Where("media_kind IN ?", []string{models.FaceMediaKindVideo, models.FaceMediaKindImage}).
		Order("cluster_id ASC, media_kind ASC, media_id ASC").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load face cluster pending media: %w", err)
	}
	if len(rows) == 0 {
		return map[uint][]FaceClusterMediaView{}, nil
	}

	videoIDs := make([]uint, 0, len(rows))
	imageIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		switch row.MediaKind {
		case models.FaceMediaKindVideo:
			videoIDs = append(videoIDs, row.MediaID)
		case models.FaceMediaKindImage:
			imageIDs = append(imageIDs, row.MediaID)
		}
	}
	videoNames, err := loadMediaNames(ctx, &models.Video{}, videoIDs)
	if err != nil {
		return nil, err
	}
	imageNames, err := loadMediaNames(ctx, &models.Image{}, imageIDs)
	if err != nil {
		return nil, err
	}

	pending := make(map[uint][]FaceClusterMediaView, len(clusterIDs))
	for _, row := range rows {
		name := ""
		switch row.MediaKind {
		case models.FaceMediaKindVideo:
			name = videoNames[row.MediaID]
		case models.FaceMediaKindImage:
			name = imageNames[row.MediaID]
		}
		pending[row.ClusterID] = append(pending[row.ClusterID], FaceClusterMediaView{
			MediaKind: row.MediaKind,
			MediaID:   row.MediaID,
			Name:      name,
		})
	}
	return pending, nil
}

func loadMediaNames(ctx context.Context, model interface{}, ids []uint) (map[uint]string, error) {
	names := map[uint]string{}
	if len(ids) == 0 {
		return names, nil
	}
	var rows []struct {
		ID   uint   `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := database.DB.WithContext(ctx).Unscoped().Model(model).
		Select("id", "name").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load media names: %w", err)
	}
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names, nil
}

// loadFaceClusterCandidates 取「可能是谁」并按当前相似度过滤。
//
// 库里的候选行只增不删（分析侧刻意如此），相似度会随簇代表更新而漂移；漂到阈值
// 以下的旧候选不该再摆在用户面前，因此这里用簇当前的代表向量与人物头像种子重算
// 一次，低于阈值的不展示。展示的相似度也是重算值，而不是当初写库的那一个。
func loadFaceClusterCandidates(ctx context.Context, clusters []models.FaceCluster) (map[uint][]FaceClusterCandidateView, error) {
	clusterIDs := make([]uint, 0, len(clusters))
	for _, cluster := range clusters {
		clusterIDs = append(clusterIDs, cluster.ID)
	}
	var rows []models.FacePersonCandidate
	if err := database.DB.WithContext(ctx).Model(&models.FacePersonCandidate{}).
		Select("id", "cluster_id", "person_id", "similarity").
		Where("cluster_id IN ?", clusterIDs).
		Order("cluster_id ASC, person_id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load face person candidates: %w", err)
	}
	if len(rows) == 0 {
		return map[uint][]FaceClusterCandidateView{}, nil
	}

	seeds, err := loadFacePersonSeeds(ctx)
	if err != nil {
		return nil, err
	}
	centroids := make(map[uint][]float32, len(clusters))
	for _, cluster := range clusters {
		vector, err := decodeFaceEmbedding(cluster.Centroid)
		if err != nil {
			continue
		}
		if normalized, ok := normalizeFaceEmbedding(vector); ok {
			centroids[cluster.ID] = normalized
		}
	}
	personIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		personIDs = append(personIDs, row.PersonID)
	}
	names, err := loadPersonDisplayNames(ctx, personIDs)
	if err != nil {
		return nil, err
	}

	candidates := make(map[uint][]FaceClusterCandidateView, len(clusters))
	for _, row := range rows {
		centroid, ok := centroids[row.ClusterID]
		if !ok {
			continue
		}
		seed, ok := seeds[row.PersonID]
		if !ok {
			// 人物头像换过或被移除，这条候选已经无从核对，不展示。
			continue
		}
		similarity := faceSimilarity(centroid, seed)
		if similarity < facePersonSeedThreshold {
			continue
		}
		candidates[row.ClusterID] = append(candidates[row.ClusterID], FaceClusterCandidateView{
			PersonID:    row.PersonID,
			DisplayName: names[row.PersonID],
			Similarity:  similarity,
		})
	}
	for clusterID := range candidates {
		list := candidates[clusterID]
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Similarity != list[j].Similarity {
				return list[i].Similarity > list[j].Similarity
			}
			return list[i].PersonID < list[j].PersonID
		})
		candidates[clusterID] = list
	}
	return candidates, nil
}

func loadFaceClusterPersonNames(ctx context.Context, clusters []models.FaceCluster) (map[uint]string, error) {
	personIDs := make([]uint, 0, len(clusters))
	for _, cluster := range clusters {
		if cluster.PersonID != nil {
			personIDs = append(personIDs, *cluster.PersonID)
		}
	}
	return loadPersonDisplayNames(ctx, personIDs)
}

func loadPersonDisplayNames(ctx context.Context, personIDs []uint) (map[uint]string, error) {
	names := map[uint]string{}
	if len(personIDs) == 0 {
		return names, nil
	}
	var rows []struct {
		ID          uint   `gorm:"column:id"`
		DisplayName string `gorm:"column:display_name"`
	}
	if err := database.DB.WithContext(ctx).Model(&models.Person{}).
		Select("id", "display_name").Where("id IN ?", personIDs).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load person names: %w", err)
	}
	for _, row := range rows {
		names[row.ID] = row.DisplayName
	}
	return names, nil
}
