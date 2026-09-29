package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"
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
	// ErrFaceClusterConflict 是改派在并发下输掉的一方：簇在读取之后又被别人改派过。
	ErrFaceClusterConflict = errors.New("cluster_conflict")
	// ErrFaceClusterNotIgnored 是恢复的前置：只有被忽略的簇才能恢复。
	ErrFaceClusterNotIgnored = errors.New("cluster_not_ignored")
	// ErrFaceClusterIgnored 是对已忽略簇做只对未命名 / 已命名簇有意义的动作（移除来源）：
	// 与 cluster_not_unnamed 分开，界面据此提示「先在『已忽略』里恢复」（META-04 M-8）。
	ErrFaceClusterIgnored = errors.New("cluster_ignored")
)

// FaceUnlinkMediaView.Source 的取值（META-04）：关系是不是这一簇经人脸链路写出的。
const (
	// FaceRelationSourceFace：这一簇写过这条关系（face_relation_writes 里有仍然作数的记录），
	// 解除关联 / 改派时可以删除或迁走（仍被该人物其他已命名簇持有或覆盖的除外）。
	FaceRelationSourceFace = "face"
	// FaceRelationSourceUnknown：没有这一簇的写入记录——NFO 导入、手动添加、标签转人物，
	// 或升级前的历史关系，也包括关系已不存在的媒体。一律保留。
	FaceRelationSourceUnknown = "unknown"
)

// withFaceClusterAssignment 在 faceClusterAssignmentMu 下执行 fn：命名、关联、解除、改派、
// 恢复、移除来源与分析侧的聚类彼此串行（META-04 M-1），用 defer 保证 fn 内 panic 也会放锁。
func withFaceClusterAssignment(fn func() error) error {
	faceClusterAssignmentMu.Lock()
	defer faceClusterAssignmentMu.Unlock()
	return fn()
}

// faceClusterAfterSnapshotHook 是测试接缝：解除关联 / 改派在读完簇快照之后、条件更新之前调用它，
// 测试据此模拟另一个写入者恰好在这段窗口里改写了簇（META-04 M-2）。生产代码里恒为 nil。
var faceClusterAfterSnapshotHook func(tx *gorm.DB, clusterID uint)

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

	query := applyFaceClusterFilter(database.DB.WithContext(ctx).Model(&models.FaceCluster{}).
		Select("id", "status", "person_id", "centroid", "representative_observation_id"), filter)
	var clusters []models.FaceCluster
	if err := query.Order("id ASC").Find(&clusters).Error; err != nil {
		return nil, fmt.Errorf("list face clusters: %w", err)
	}
	if len(clusters) == 0 {
		return []FaceClusterView{}, nil
	}

	views, err := buildFaceClusterViews(ctx, clusters)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].ObservationCount != views[j].ObservationCount {
			return views[i].ObservationCount > views[j].ObservationCount
		}
		return views[i].ID < views[j].ID
	})
	return views, nil
}

func applyFaceClusterFilter(query *gorm.DB, filter FaceClusterFilter) *gorm.DB {
	if filter.Status != "" {
		query = query.Where("face_clusters.status = ?", filter.Status)
	}
	if filter.MediaKind != "" {
		observations := database.DB.Model(&models.FaceObservation{}).Select("1").
			Where("face_observations.cluster_id = face_clusters.id").
			Where("face_observations.media_kind = ?", filter.MediaKind)
		query = query.Where("EXISTS (?)", observations)
	}
	return query
}

// buildFaceClusterViews 把簇行补成卡片视图，保持入参顺序（排序由调用方决定）。
func buildFaceClusterViews(ctx context.Context, clusters []models.FaceCluster) ([]FaceClusterView, error) {
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
	// 与解除关联 / 改派串行（M-1）：否则一次命名可能夹在别人的「读快照 → 条件更新」之间。
	err = withFaceClusterAssignment(func() error {
		return database.Transaction(func(tx *gorm.DB) error {
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
			written, err = writeFaceClusterRelations(ctx, tx, cluster.ID, person.ID, false, nil)
			if err != nil {
				return err
			}
			return claimFaceCluster(ctx, tx, cluster.ID, person.ID)
		})
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
	err := withFaceClusterAssignment(func() error {
		return database.Transaction(func(tx *gorm.DB) error {
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
			written, err = writeFaceClusterRelations(ctx, tx, cluster.ID, person.ID, false, nil)
			if err != nil {
				return err
			}
			return claimFaceCluster(ctx, tx, cluster.ID, person.ID)
		})
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
			Updates(map[string]interface{}{
				"status":     models.FaceClusterStatusIgnored,
				"ignored_at": time.Now(),
			}).Error; err != nil {
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
// 全部确认；逐条确认见 ConfirmFaceClusterAppendObservations。
func (s *FaceReviewService) ConfirmFaceClusterAppend(ctx context.Context, clusterID uint) error {
	return s.ConfirmFaceClusterAppendObservations(ctx, clusterID, nil)
}

// ConfirmFaceClusterAppendObservations 是逐条确认（D-PC30）：observationIDs 为空表示
// 全部 pending 观测（兼容旧行为）；非空时只确认所列的 pending 观测并只写它们涉及的
// 媒体，其余保持 pending。所列 ID 里不属于该簇或已不是 pending 的会被跳过。
func (s *FaceReviewService) ConfirmFaceClusterAppendObservations(ctx context.Context, clusterID uint, observationIDs []uint) error {
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
		written, err = writeFaceClusterRelations(ctx, tx, cluster.ID, personID, true, observationIDs)
		if err != nil {
			return err
		}
		update := tx.WithContext(ctx).Model(&models.FaceObservation{}).
			Where("cluster_id = ? AND append_status = ?", cluster.ID, models.FaceAppendStatusPending)
		if len(observationIDs) > 0 {
			update = update.Where("id IN ?", observationIDs)
		}
		return update.Update("append_status", models.FaceAppendStatusConfirmed).Error
	})
	if err != nil {
		return err
	}
	log.Printf("Face cluster append confirmed cluster_id=%d person_id=%d selected=%d relations=%d", clusterID, personID, len(observationIDs), written)
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
	if err := reconcileFaceClusterPeople(ctx); err != nil {
		return FaceClusterView{}, fmt.Errorf("reconcile face clusters: %w", err)
	}
	var cluster models.FaceCluster
	err := database.DB.WithContext(ctx).Select("id", "status", "person_id", "centroid", "representative_observation_id").
		First(&cluster, clusterID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return FaceClusterView{}, ErrFaceClusterNotFound
	}
	if err != nil {
		return FaceClusterView{}, err
	}
	views, err := buildFaceClusterViews(ctx, []models.FaceCluster{cluster})
	if err != nil {
		return FaceClusterView{}, err
	}
	return views[0], nil
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
//
// 用户忽略过的追加（dismissed）不在「全部」之内（META-04 M-3）：簇解除关联后再命名 / 关联，
// 那些观测是用户明确否掉过的，保持 dismissed，也不写关系（见 writeFaceClusterRelations）。
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
		Where("cluster_id = ? AND append_status NOT IN ?", clusterID,
			[]string{models.FaceAppendStatusConfirmed, models.FaceAppendStatusDismissed}).
		Update("append_status", models.FaceAppendStatusConfirmed).Error
}

// faceMediaRef 是一件媒体的多态引用。
type faceMediaRef struct {
	MediaKind string `gorm:"column:media_kind"`
	MediaID   uint   `gorm:"column:media_id"`
}

// writeFaceClusterRelations 是本仓库人脸链路上唯一写 video_people / image_people
// 的地方（连同下面的解除/改派），只由用户动作在事务内调用。
//
// pendingOnly 为真时只看 append_status = pending 的观测（确认追加），observationIDs
// 非空时进一步限定为所列观测；为假时看簇内全部观测（首次命名或关联），但用户忽略过的
// 追加（dismissed）除外（M-3）。按 (media_kind, media_id) 去重，已存在的关系靠
// ON CONFLICT DO NOTHING 跳过，因此重复确认不会报错也不会重复写。
func writeFaceClusterRelations(ctx context.Context, tx *gorm.DB, clusterID, personID uint, pendingOnly bool, observationIDs []uint) (int64, error) {
	query := tx.WithContext(ctx).Model(&models.FaceObservation{}).
		Distinct("media_kind", "media_id").
		Where("cluster_id = ?", clusterID).
		Where("media_kind IN ?", []string{models.FaceMediaKindVideo, models.FaceMediaKindImage})
	if pendingOnly {
		query = query.Where("append_status = ?", models.FaceAppendStatusPending)
		if len(observationIDs) > 0 {
			query = query.Where("id IN ?", observationIDs)
		}
	} else {
		query = query.Where("append_status <> ?", models.FaceAppendStatusDismissed)
	}
	var refs []faceMediaRef
	if err := query.Scan(&refs).Error; err != nil {
		return 0, err
	}
	return writeFaceRelations(ctx, tx, clusterID, personID, refs)
}

// writeFaceRelations 给人物补上这批媒体的关系（幂等），并为人脸链路真正写出的关系记账
// （face_relation_writes，META-04 I-1）：
//   - 这一次 INSERT … ON CONFLICT DO NOTHING 真的插进去了（RowsAffected = 1）：记 (人物, 媒体, 本簇)；
//   - 关系已经存在，而且它本身就是人脸链路写的（有仍然作数的写入记录）：本簇也记一行，
//     共同持有——之后任何一个簇离开该人物，只要还有别的簇持有，关系就留着；
//   - 关系已经存在、来源不是人脸链路（NFO、手动、标签转人物、升级前的历史关系）：不记，
//     解除关联 / 改派时一律保留。
//
// 逐行插入才拿得到每一行的 RowsAffected。关系行与记录行的 created_at 取同一个时间，
// 见 faceRelationWriteValid。
func writeFaceRelations(ctx context.Context, tx *gorm.DB, clusterID, personID uint, refs []faceMediaRef) (int64, error) {
	videoIDs, imageIDs := splitFaceMediaRefs(refs)
	now := time.Now()
	var written int64
	records := make([]models.FaceRelationWrite, 0, len(refs))
	existed := make([]faceMediaRef, 0)
	record := func(kind string, mediaID uint) {
		records = append(records, models.FaceRelationWrite{
			PersonID: personID, MediaKind: kind, MediaID: mediaID, ClusterID: clusterID, CreatedAt: now,
		})
	}

	if len(videoIDs) > 0 {
		// Unscoped：软删除的视频行还在库里，关系也应当建起来（人物清理判定本就把
		// 软删除媒体的关系算作有效关系）；只有已经永久删除的媒体要跳过，否则外键报错。
		var present []uint
		if err := tx.WithContext(ctx).Unscoped().Model(&models.Video{}).
			Where("id IN ?", videoIDs).Pluck("id", &present).Error; err != nil {
			return written, err
		}
		for _, videoID := range present {
			result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
				Create(&models.VideoPerson{VideoID: videoID, PersonID: personID, CreatedAt: now})
			if result.Error != nil {
				return written, result.Error
			}
			if result.RowsAffected == 1 {
				written++
				record(models.FaceMediaKindVideo, videoID)
			} else {
				existed = append(existed, faceMediaRef{MediaKind: models.FaceMediaKindVideo, MediaID: videoID})
			}
		}
	}
	if len(imageIDs) > 0 {
		var present []uint
		if err := tx.WithContext(ctx).Unscoped().Model(&models.Image{}).
			Where("id IN ?", imageIDs).Pluck("id", &present).Error; err != nil {
			return written, err
		}
		for _, imageID := range present {
			result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
				Create(&models.ImagePerson{ImageID: imageID, PersonID: personID, CreatedAt: now})
			if result.Error != nil {
				return written, result.Error
			}
			if result.RowsAffected == 1 {
				written++
				record(models.FaceMediaKindImage, imageID)
			} else {
				existed = append(existed, faceMediaRef{MediaKind: models.FaceMediaKindImage, MediaID: imageID})
			}
		}
	}

	if len(existed) > 0 {
		relationTimes, err := loadFaceRelationTimes(ctx, tx, personID, existed)
		if err != nil {
			return written, err
		}
		writes, err := loadFaceRelationWrites(ctx, tx, personID, existed)
		if err != nil {
			return written, err
		}
		shared := make(map[faceMediaRef]bool, len(existed))
		for _, write := range writes {
			ref := faceMediaRef{MediaKind: write.MediaKind, MediaID: write.MediaID}
			if faceRelationWriteValid(write, relationTimes) {
				shared[ref] = true
			}
		}
		for _, ref := range existed {
			if shared[ref] {
				record(ref.MediaKind, ref.MediaID)
			}
		}
	}
	if len(records) == 0 {
		return written, nil
	}
	// 同一 (人物, 媒体, 簇) 再写一次（关系被删后本簇又确认了一遍）时刷新 created_at，
	// 让记录重新与新的关系行对上。refs 已按媒体去重，一批里不会有重复键（PG 21000）；
	// SET 右侧是 excluded 列，不引用目标表（PG 42702）。
	return written, tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "person_id"}, {Name: "media_kind"}, {Name: "media_id"}, {Name: "cluster_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"created_at"}),
	}).CreateInBatches(&records, faceRelationChunk).Error
}

// faceRelationWriteValid 报告一条写入记录是否仍然作数：对应的关系还在，且关系行不晚于记录。
// 关系被别的路径删掉、又由 NFO / 手动等来源重建时，新关系的 created_at 晚于旧记录，
// 旧记录于是失效——那条新关系不是人脸链路写的，解除时不能删。
func faceRelationWriteValid(write models.FaceRelationWrite, relationTimes map[faceMediaRef]time.Time) bool {
	createdAt, ok := relationTimes[faceMediaRef{MediaKind: write.MediaKind, MediaID: write.MediaID}]
	return ok && !createdAt.After(write.CreatedAt)
}

// loadFaceRelationTimes 取人物与这批媒体现有关系行的 created_at（没有关系的媒体不在结果里）。
func loadFaceRelationTimes(ctx context.Context, tx *gorm.DB, personID uint, refs []faceMediaRef) (map[faceMediaRef]time.Time, error) {
	videoIDs, imageIDs := splitFaceMediaRefs(refs)
	times := make(map[faceMediaRef]time.Time, len(refs))
	type relationRow struct {
		MediaID   uint      `gorm:"column:media_id"`
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	load := func(model interface{}, column, kind string, ids []uint) error {
		for start := 0; start < len(ids); start += faceRelationChunk {
			end := min(start+faceRelationChunk, len(ids))
			var rows []relationRow
			if err := tx.WithContext(ctx).Model(model).
				Select(column+" AS media_id, created_at").
				Where("person_id = ? AND "+column+" IN ?", personID, ids[start:end]).
				Scan(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				times[faceMediaRef{MediaKind: kind, MediaID: row.MediaID}] = row.CreatedAt
			}
		}
		return nil
	}
	if err := load(&models.VideoPerson{}, "video_id", models.FaceMediaKindVideo, videoIDs); err != nil {
		return nil, err
	}
	if err := load(&models.ImagePerson{}, "image_id", models.FaceMediaKindImage, imageIDs); err != nil {
		return nil, err
	}
	return times, nil
}

// loadFaceRelationWrites 取人物在这批媒体上的全部写入记录（任意簇）。
func loadFaceRelationWrites(ctx context.Context, tx *gorm.DB, personID uint, refs []faceMediaRef) ([]models.FaceRelationWrite, error) {
	videoIDs, imageIDs := splitFaceMediaRefs(refs)
	var writes []models.FaceRelationWrite
	for _, group := range []struct {
		kind string
		ids  []uint
	}{{models.FaceMediaKindVideo, videoIDs}, {models.FaceMediaKindImage, imageIDs}} {
		for start := 0; start < len(group.ids); start += faceRelationChunk {
			end := min(start+faceRelationChunk, len(group.ids))
			var rows []models.FaceRelationWrite
			if err := tx.WithContext(ctx).
				Where("person_id = ? AND media_kind = ? AND media_id IN ?", personID, group.kind, group.ids[start:end]).
				Find(&rows).Error; err != nil {
				return nil, err
			}
			writes = append(writes, rows...)
		}
	}
	return writes, nil
}

// deleteFaceClusterWrites 删掉簇在某人物名下的全部写入记录：簇离开该人物（解除关联、改派）之后，
// 这些记录不再能由它行使。removeRelations / moveRelations 为假时关系是用户选择留下的，
// 记录一并删掉等于把它们交还给用户——之后任何人脸动作都不会再删它们。
func deleteFaceClusterWrites(ctx context.Context, tx *gorm.DB, personID, clusterID uint) error {
	return tx.WithContext(ctx).Where("person_id = ? AND cluster_id = ?", personID, clusterID).
		Delete(&models.FaceRelationWrite{}).Error
}

func splitFaceMediaRefs(refs []faceMediaRef) (videoIDs, imageIDs []uint) {
	for _, ref := range refs {
		switch ref.MediaKind {
		case models.FaceMediaKindVideo:
			videoIDs = append(videoIDs, ref.MediaID)
		case models.FaceMediaKindImage:
			imageIDs = append(imageIDs, ref.MediaID)
		}
	}
	return videoIDs, imageIDs
}

// reconcileFaceClusterPeople 把人物已被删除的簇拉回未命名（4.4.4）。
// 外键 SET NULL 只能置空 person_id，改不了 status；不修的话这些簇既不会在面板上
// 重新出现，也过不了「必须是未命名」这道校验，等于永久卡死。
//
// 同一处清掉已删除人物的写入记录（face_relation_writes 不挂外键，人物删除时不会级联）。
// 人物 ID 自增不复用，残留行本身不会被误用；这里清掉是为了不让表无限长。先读再删：
// 绝大多数时候没有可删的行，只读一次就够，免得每次列举都去抢写锁。
func reconcileFaceClusterPeople(ctx context.Context) error {
	if err := database.DB.WithContext(ctx).Model(&models.FaceCluster{}).
		Where("status = ? AND person_id IS NULL", models.FaceClusterStatusNamed).
		Update("status", models.FaceClusterStatusUnnamed).Error; err != nil {
		return err
	}
	orphans := database.DB.WithContext(ctx).Model(&models.FaceRelationWrite{}).
		Where("person_id NOT IN (?)", database.DB.Model(&models.Person{}).Select("id"))
	var stale []uint
	if err := orphans.Limit(1).Pluck("id", &stale).Error; err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	return database.DB.WithContext(ctx).
		Where("person_id NOT IN (?)", database.DB.Model(&models.Person{}).Select("id")).
		Delete(&models.FaceRelationWrite{}).Error
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

// ===== 人脸可逆与分页（D-PC29、D-PC30） =====
//
// 下面的方法都不使用 SELECT ... FOR UPDATE：先读快照，再用 WHERE status/person_id
// 条件更新，以影响行数判定竞争，输的一方整个事务回滚（G-2）。

// FaceClusterCursorKey 是 ListFaceClusterPage 的键集游标：上一页最后一张卡片的
// (观测数, 簇 id)。零值（ID 为 0）表示从头开始。
type FaceClusterCursorKey struct {
	Count int  `json:"count"`
	ID    uint `json:"id"`
}

// FaceClusterPage 是一页簇卡片。HasMore 为假时 Next 无意义。
type FaceClusterPage struct {
	Clusters []FaceClusterView    `json:"clusters"`
	Next     FaceClusterCursorKey `json:"next"`
	HasMore  bool                 `json:"has_more"`
}

// ListFaceClusterPage 按 (观测数 DESC, id DESC) 键集分页。排序键里带 id，
// 多个簇观测数相同时游标也是稳定的；页大小复用 normalizeEntityPageLimit。
//
// 排序读维护列 face_clusters.observation_count（META-11 M-4），走 idx_face_clusters_count_id，
// 不再每页对每个簇现算一次 COUNT(*)。卡片上显示的观测数仍按观测行现算（buildFaceClusterViews），
// 游标用的是排序键本身（列值），翻页语义与之前一致。
func (s *FaceReviewService) ListFaceClusterPage(ctx context.Context, filter FaceClusterFilter, cursor FaceClusterCursorKey, limit int) (FaceClusterPage, error) {
	if err := reconcileFaceClusterPeople(ctx); err != nil {
		return FaceClusterPage{}, fmt.Errorf("reconcile face clusters: %w", err)
	}
	limit = normalizeEntityPageLimit(limit)

	query := applyFaceClusterFilter(database.DB.WithContext(ctx).Model(&models.FaceCluster{}).
		Select("face_clusters.id AS id, face_clusters.observation_count AS observation_count"), filter)
	if cursor.ID != 0 {
		query = query.Where("(face_clusters.observation_count < ? OR (face_clusters.observation_count = ? AND face_clusters.id < ?))",
			cursor.Count, cursor.Count, cursor.ID)
	}
	var keys []struct {
		ID               uint `gorm:"column:id"`
		ObservationCount int  `gorm:"column:observation_count"`
	}
	if err := query.Order("face_clusters.observation_count DESC, face_clusters.id DESC").Limit(limit + 1).Scan(&keys).Error; err != nil {
		return FaceClusterPage{}, fmt.Errorf("list face cluster page: %w", err)
	}
	page := FaceClusterPage{Clusters: []FaceClusterView{}}
	if len(keys) > limit {
		keys = keys[:limit]
		page.HasMore = true
		last := keys[len(keys)-1]
		page.Next = FaceClusterCursorKey{Count: last.ObservationCount, ID: last.ID}
	}
	if len(keys) == 0 {
		return page, nil
	}
	ids := make([]uint, 0, len(keys))
	for _, key := range keys {
		ids = append(ids, key.ID)
	}
	var rows []models.FaceCluster
	if err := database.DB.WithContext(ctx).
		Select("id", "status", "person_id", "centroid", "representative_observation_id").
		Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return FaceClusterPage{}, fmt.Errorf("load face cluster page: %w", err)
	}
	byID := make(map[uint]models.FaceCluster, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	ordered := make([]models.FaceCluster, 0, len(keys))
	for _, key := range keys {
		if cluster, ok := byID[key.ID]; ok {
			ordered = append(ordered, cluster)
		}
	}
	views, err := buildFaceClusterViews(ctx, ordered)
	if err != nil {
		return FaceClusterPage{}, err
	}
	page.Clusters = views
	return page, nil
}

// IgnoredFaceClusterView 是「已忽略」列表的一行。AbsorbedSinceIgnored 是忽略之后
// 又并入该簇的观测数；历史行（没有 ignored_at）显示为 0。
type IgnoredFaceClusterView struct {
	Cluster              FaceClusterView `json:"cluster"`
	IgnoredAt            *time.Time      `json:"ignored_at" ts_type:"string"`
	AbsorbedSinceIgnored int             `json:"absorbed_since_ignored"`
}

// IgnoredFaceClusterPage 按簇 id 倒序（新近的先看）。NextCursor 为 0 表示没有下一页。
type IgnoredFaceClusterPage struct {
	Clusters   []IgnoredFaceClusterView `json:"clusters"`
	NextCursor uint                     `json:"next_cursor"`
}

// ListIgnoredFaceClusters 列出 status='ignored' 的簇。cursor 是上一页的 NextCursor（0 从头）。
func (s *FaceReviewService) ListIgnoredFaceClusters(ctx context.Context, cursor uint, limit int) (IgnoredFaceClusterPage, error) {
	limit = normalizeEntityPageLimit(limit)
	query := database.DB.WithContext(ctx).Model(&models.FaceCluster{}).
		Select("id", "status", "person_id", "centroid", "representative_observation_id", "ignored_at").
		Where("status = ?", models.FaceClusterStatusIgnored)
	if cursor != 0 {
		query = query.Where("id < ?", cursor)
	}
	var clusters []models.FaceCluster
	if err := query.Order("id DESC").Limit(limit + 1).Find(&clusters).Error; err != nil {
		return IgnoredFaceClusterPage{}, fmt.Errorf("list ignored face clusters: %w", err)
	}
	page := IgnoredFaceClusterPage{Clusters: []IgnoredFaceClusterView{}}
	if len(clusters) > limit {
		clusters = clusters[:limit]
		page.NextCursor = clusters[len(clusters)-1].ID
	}
	views, err := buildFaceClusterViews(ctx, clusters)
	if err != nil {
		return IgnoredFaceClusterPage{}, err
	}
	for i, cluster := range clusters {
		row := IgnoredFaceClusterView{Cluster: views[i], IgnoredAt: cluster.IgnoredAt}
		if cluster.IgnoredAt != nil {
			// 忽略之后并入的观测：聚类把新观测挂到簇上时会更新观测行，updated_at 即并入时间。
			var absorbed int64
			if err := database.DB.WithContext(ctx).Model(&models.FaceObservation{}).
				Where("cluster_id = ? AND updated_at > ?", cluster.ID, *cluster.IgnoredAt).
				Count(&absorbed).Error; err != nil {
				return IgnoredFaceClusterPage{}, fmt.Errorf("count absorbed face observations: %w", err)
			}
			row.AbsorbedSinceIgnored = int(absorbed)
		}
		page.Clusters = append(page.Clusters, row)
	}
	return page, nil
}

// RestoreFaceCluster 把被忽略的簇恢复为未命名：条件更新 ignored → unnamed，
// 清掉 ignored_at。观测不动（忽略期间并入的观测随簇一起回到面板）。
// 并发下输的一方重读：已经是未命名就按幂等返回，否则报 cluster_not_ignored。
func (s *FaceReviewService) RestoreFaceCluster(ctx context.Context, clusterID uint) error {
	faceClusterAssignmentMu.Lock()
	defer faceClusterAssignmentMu.Unlock()
	err := database.Transaction(func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Model(&models.FaceCluster{}).
			Where("id = ? AND status = ?", clusterID, models.FaceClusterStatusIgnored).
			Updates(map[string]interface{}{
				"status":     models.FaceClusterStatusUnnamed,
				"ignored_at": nil,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		var cluster models.FaceCluster
		if err := tx.WithContext(ctx).Select("id", "status").First(&cluster, clusterID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrFaceClusterNotFound
			}
			return err
		}
		if cluster.Status == models.FaceClusterStatusUnnamed {
			return nil
		}
		return ErrFaceClusterNotIgnored
	})
	if err != nil {
		return err
	}
	log.Printf("Face cluster restored cluster_id=%d", clusterID)
	return nil
}

// FaceUnlinkMediaView 是解除关联预览里的一件媒体。
//
//   - HasRelation：原人物与这件媒体现在有没有关系行；
//   - Source：face 表示这条关系是这一簇经人脸链路写的（有仍然作数的写入记录），unknown 表示
//     来源不明（NFO、手动、标签转人物或升级前的历史关系），unknown 的关系解除时一律保留；
//   - CoveredByOther：该人物的其他 named 簇仍然覆盖它（已确认观测，或那个簇也持有这条关系），
//     解除时关系会保留。
//
// 解除关联（removeRelations）/ 改派（moveRelations）真正删除的，恰好是
// HasRelation && Source == face && !CoveredByOther 的那些（META-04 I-1、M-7）。
type FaceUnlinkMediaView struct {
	MediaKind      string `json:"media_kind"`
	MediaID        uint   `json:"media_id"`
	Name           string `json:"name"`
	HasRelation    bool   `json:"has_relation"`
	Source         string `json:"source"`
	CoveredByOther bool   `json:"covered_by_other"`
}

// namedFaceClusterSnapshot 读簇并要求它是 named 且有人物。不加锁。
func namedFaceClusterSnapshot(ctx context.Context, tx *gorm.DB, clusterID uint) (models.FaceCluster, error) {
	var cluster models.FaceCluster
	err := tx.WithContext(ctx).Select("id", "status", "person_id").First(&cluster, clusterID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return cluster, ErrFaceClusterNotFound
	}
	if err != nil {
		return cluster, err
	}
	if cluster.Status != models.FaceClusterStatusNamed || cluster.PersonID == nil {
		return cluster, ErrFaceClusterNotNamed
	}
	return cluster, nil
}

// faceClusterUpdateLost 在解除关联 / 改派的条件更新落空时重读簇，给出输的一方该报的错
// （M-2）：簇已不在 → cluster_not_found；已不是 named（或人物刚被删、person_id 已置空）→
// cluster_not_named；仍是 named、只是换了人物 → cluster_conflict（别人在读快照之后改派过它）。
func faceClusterUpdateLost(ctx context.Context, tx *gorm.DB, clusterID uint) error {
	var current models.FaceCluster
	if err := tx.WithContext(ctx).Select("id", "status", "person_id").First(&current, clusterID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrFaceClusterNotFound
		}
		return err
	}
	if current.Status != models.FaceClusterStatusNamed || current.PersonID == nil {
		return ErrFaceClusterNotNamed
	}
	return ErrFaceClusterConflict
}

// loadFaceClusterConfirmedMedia 是该簇已确认观测涉及的媒体（关系就是这些观测写出去的）。
func loadFaceClusterConfirmedMedia(ctx context.Context, tx *gorm.DB, clusterID uint) ([]faceMediaRef, error) {
	var refs []faceMediaRef
	err := tx.WithContext(ctx).Model(&models.FaceObservation{}).
		Distinct("media_kind", "media_id").
		Where("cluster_id = ? AND append_status = ?", clusterID, models.FaceAppendStatusConfirmed).
		Where("media_kind IN ?", []string{models.FaceMediaKindVideo, models.FaceMediaKindImage}).
		Order("media_kind ASC, media_id ASC").
		Scan(&refs).Error
	return refs, err
}

// loadOtherNamedFaceClusters 是同一人物名下、除本簇以外的 named 簇。
func loadOtherNamedFaceClusters(ctx context.Context, tx *gorm.DB, clusterID, personID uint) (map[uint]bool, error) {
	var ids []uint
	if err := tx.WithContext(ctx).Model(&models.FaceCluster{}).
		Where("status = ? AND person_id = ? AND id <> ?", models.FaceClusterStatusNamed, personID, clusterID).
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	others := make(map[uint]bool, len(ids))
	for _, id := range ids {
		others[id] = true
	}
	return others, nil
}

// loadMediaCoveredByOtherClusters 返回被这些簇（已确认观测）覆盖的媒体。
func loadMediaCoveredByOtherClusters(ctx context.Context, tx *gorm.DB, others map[uint]bool) (map[faceMediaRef]bool, error) {
	covered := map[faceMediaRef]bool{}
	if len(others) == 0 {
		return covered, nil
	}
	ids := make([]uint, 0, len(others))
	for id := range others {
		ids = append(ids, id)
	}
	for start := 0; start < len(ids); start += faceRelationChunk {
		end := min(start+faceRelationChunk, len(ids))
		var refs []faceMediaRef
		if err := tx.WithContext(ctx).Model(&models.FaceObservation{}).
			Distinct("media_kind", "media_id").
			Where("cluster_id IN ? AND append_status = ?", ids[start:end], models.FaceAppendStatusConfirmed).
			Where("media_kind IN ?", []string{models.FaceMediaKindVideo, models.FaceMediaKindImage}).
			Scan(&refs).Error; err != nil {
			return nil, err
		}
		for _, ref := range refs {
			covered[ref] = true
		}
	}
	return covered, nil
}

// faceUnlinkItem 是簇离开人物时对一件媒体的判定，预览与执行共用（M-7：预览说会删的，
// 就是执行时删的那些）。
type faceUnlinkItem struct {
	ref            faceMediaRef
	hasRelation    bool
	fromFace       bool
	coveredByOther bool
}

// removable 报告簇离开人物时这条关系该不该删：关系还在、是本簇写的、且没有别的 named 簇
// 覆盖或持有。来源不明的关系（没有本簇的写入记录）一律保留。
func (item faceUnlinkItem) removable() bool {
	return item.hasRelation && item.fromFace && !item.coveredByOther
}

// planFaceClusterUnlink 对簇的已确认媒体逐件判定（见 faceUnlinkItem）。
//
// 覆盖取两条口径的并集：其他 named 簇的已确认观测里有这件媒体，或其他 named 簇也持有这条
// 关系的写入记录。宁可多留：留下的关系用户随时能手动删，删错的关系没有人知道该补回来。
func planFaceClusterUnlink(ctx context.Context, tx *gorm.DB, clusterID, personID uint) ([]faceUnlinkItem, error) {
	refs, err := loadFaceClusterConfirmedMedia(ctx, tx, clusterID)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return []faceUnlinkItem{}, nil
	}
	others, err := loadOtherNamedFaceClusters(ctx, tx, clusterID, personID)
	if err != nil {
		return nil, err
	}
	covered, err := loadMediaCoveredByOtherClusters(ctx, tx, others)
	if err != nil {
		return nil, err
	}
	relationTimes, err := loadFaceRelationTimes(ctx, tx, personID, refs)
	if err != nil {
		return nil, err
	}
	writes, err := loadFaceRelationWrites(ctx, tx, personID, refs)
	if err != nil {
		return nil, err
	}
	own := make(map[faceMediaRef]bool, len(writes))
	for _, write := range writes {
		if !faceRelationWriteValid(write, relationTimes) {
			continue
		}
		ref := faceMediaRef{MediaKind: write.MediaKind, MediaID: write.MediaID}
		switch {
		case write.ClusterID == clusterID:
			own[ref] = true
		case others[write.ClusterID]:
			covered[ref] = true
		}
	}
	items := make([]faceUnlinkItem, 0, len(refs))
	for _, ref := range refs {
		_, hasRelation := relationTimes[ref]
		items = append(items, faceUnlinkItem{
			ref:            ref,
			hasRelation:    hasRelation,
			fromFace:       own[ref],
			coveredByOther: covered[ref],
		})
	}
	return items, nil
}

func removableFaceMedia(items []faceUnlinkItem) []faceMediaRef {
	refs := make([]faceMediaRef, 0, len(items))
	for _, item := range items {
		if item.removable() {
			refs = append(refs, item.ref)
		}
	}
	return refs
}

// PreviewFaceClusterUnlink 列出解除/改派会牵动的媒体：该簇确认观测涉及的 (kind, id, name)，
// 每项标出关系在不在、是不是本簇写的、是否仍被同一人物其他 named 簇覆盖（M-7）。只读。
func (s *FaceReviewService) PreviewFaceClusterUnlink(ctx context.Context, clusterID uint) ([]FaceUnlinkMediaView, error) {
	cluster, err := namedFaceClusterSnapshot(ctx, database.DB, clusterID)
	if err != nil {
		return nil, err
	}
	items, err := planFaceClusterUnlink(ctx, database.DB, clusterID, *cluster.PersonID)
	if err != nil {
		return nil, fmt.Errorf("load face cluster media: %w", err)
	}
	refs := make([]faceMediaRef, 0, len(items))
	for _, item := range items {
		refs = append(refs, item.ref)
	}
	videoIDs, imageIDs := splitFaceMediaRefs(refs)
	videoNames, err := loadMediaNames(ctx, &models.Video{}, videoIDs)
	if err != nil {
		return nil, err
	}
	imageNames, err := loadMediaNames(ctx, &models.Image{}, imageIDs)
	if err != nil {
		return nil, err
	}
	views := make([]FaceUnlinkMediaView, 0, len(items))
	for _, item := range items {
		name := videoNames[item.ref.MediaID]
		if item.ref.MediaKind == models.FaceMediaKindImage {
			name = imageNames[item.ref.MediaID]
		}
		source := FaceRelationSourceUnknown
		if item.fromFace {
			source = FaceRelationSourceFace
		}
		views = append(views, FaceUnlinkMediaView{
			MediaKind:      item.ref.MediaKind,
			MediaID:        item.ref.MediaID,
			Name:           name,
			HasRelation:    item.hasRelation,
			Source:         source,
			CoveredByOther: item.coveredByOther,
		})
	}
	return views, nil
}

const faceRelationChunk = 500

// deletePersonRelations 删除人物与这批媒体的关系行。
func deletePersonRelations(ctx context.Context, tx *gorm.DB, personID uint, refs []faceMediaRef) (int64, error) {
	videoIDs, imageIDs := splitFaceMediaRefs(refs)
	var removed int64
	for start := 0; start < len(videoIDs); start += faceRelationChunk {
		end := min(start+faceRelationChunk, len(videoIDs))
		result := tx.WithContext(ctx).Where("person_id = ? AND video_id IN ?", personID, videoIDs[start:end]).
			Delete(&models.VideoPerson{})
		if result.Error != nil {
			return removed, result.Error
		}
		removed += result.RowsAffected
	}
	for start := 0; start < len(imageIDs); start += faceRelationChunk {
		end := min(start+faceRelationChunk, len(imageIDs))
		result := tx.WithContext(ctx).Where("person_id = ? AND image_id IN ?", personID, imageIDs[start:end]).
			Delete(&models.ImagePerson{})
		if result.Error != nil {
			return removed, result.Error
		}
		removed += result.RowsAffected
	}
	return removed, nil
}

// UnlinkFaceCluster 把已命名簇解回未命名（person_id 置空）。removeRelations 为真时，同一事务里
// 只删除「本簇写过、且没被该人物其他 named 簇覆盖或持有」的关系；来源不明的关系（NFO、手动、
// 标签转人物、升级前的历史关系）一律保留（META-04 I-1）。无论删不删，本簇在原人物名下的写入
// 记录都随之删除。簇的追加候选（pending）回到 none，用户忽略过的（dismissed）保持不变。
// 并发下输的一方：簇已不是 named 报 cluster_not_named，仍是 named 但被改派过报 cluster_conflict。
func (s *FaceReviewService) UnlinkFaceCluster(ctx context.Context, clusterID uint, removeRelations bool) (FaceClusterView, error) {
	var removed int64
	err := withFaceClusterAssignment(func() error {
		return database.Transaction(func(tx *gorm.DB) error {
			cluster, err := namedFaceClusterSnapshot(ctx, tx, clusterID)
			if err != nil {
				return err
			}
			personID := *cluster.PersonID
			if hook := faceClusterAfterSnapshotHook; hook != nil {
				hook(tx, clusterID)
			}
			result := tx.WithContext(ctx).Model(&models.FaceCluster{}).
				Where("id = ? AND status = ? AND person_id = ?", clusterID, models.FaceClusterStatusNamed, personID).
				Updates(map[string]interface{}{
					"status":    models.FaceClusterStatusUnnamed,
					"person_id": nil,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return faceClusterUpdateLost(ctx, tx, clusterID)
			}
			if err := tx.WithContext(ctx).Model(&models.FaceObservation{}).
				Where("cluster_id = ? AND append_status = ?", clusterID, models.FaceAppendStatusPending).
				Update("append_status", models.FaceAppendStatusNone).Error; err != nil {
				return err
			}
			if removeRelations {
				items, err := planFaceClusterUnlink(ctx, tx, clusterID, personID)
				if err != nil {
					return err
				}
				if removed, err = deletePersonRelations(ctx, tx, personID, removableFaceMedia(items)); err != nil {
					return err
				}
			}
			return deleteFaceClusterWrites(ctx, tx, personID, clusterID)
		})
	})
	if err != nil {
		return FaceClusterView{}, err
	}
	log.Printf("Face cluster unlinked cluster_id=%d remove_relations=%v removed=%d", clusterID, removeRelations, removed)
	return s.clusterView(ctx, clusterID)
}

// ReassignFaceCluster 把已命名簇改指向另一个人物。moveRelations 为真时，同一事务里给目标人物
// 补上簇确认观测涉及的媒体（去重，按人脸链路记账），并从原人物只删除「本簇写过、且没被原人物
// 其他 named 簇覆盖或持有」的关系；来源不明的关系在原人物侧保留（META-04 I-1）。本簇在原人物
// 名下的写入记录随之删除。目标就是当前人物时是幂等空操作。
// 并发下簇被别人改派过报 cluster_conflict，簇已不是 named 报 cluster_not_named。
func (s *FaceReviewService) ReassignFaceCluster(ctx context.Context, clusterID, targetPersonID uint, moveRelations bool) (FaceClusterView, error) {
	var moved, removed int64
	err := withFaceClusterAssignment(func() error {
		return database.Transaction(func(tx *gorm.DB) error {
			cluster, err := namedFaceClusterSnapshot(ctx, tx, clusterID)
			if err != nil {
				return err
			}
			sourcePersonID := *cluster.PersonID
			var target models.Person
			if err := tx.WithContext(ctx).Select("id").First(&target, targetPersonID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrFacePersonNotFound
				}
				return err
			}
			if sourcePersonID == targetPersonID {
				return nil
			}
			if hook := faceClusterAfterSnapshotHook; hook != nil {
				hook(tx, clusterID)
			}
			result := tx.WithContext(ctx).Model(&models.FaceCluster{}).
				Where("id = ? AND status = ? AND person_id = ?", clusterID, models.FaceClusterStatusNamed, sourcePersonID).
				Update("person_id", targetPersonID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return faceClusterUpdateLost(ctx, tx, clusterID)
			}
			if moveRelations {
				items, err := planFaceClusterUnlink(ctx, tx, clusterID, sourcePersonID)
				if err != nil {
					return err
				}
				refs := make([]faceMediaRef, 0, len(items))
				for _, item := range items {
					refs = append(refs, item.ref)
				}
				if moved, err = writeFaceRelations(ctx, tx, clusterID, targetPersonID, refs); err != nil {
					return err
				}
				if removed, err = deletePersonRelations(ctx, tx, sourcePersonID, removableFaceMedia(items)); err != nil {
					return err
				}
			}
			return deleteFaceClusterWrites(ctx, tx, sourcePersonID, clusterID)
		})
	})
	if err != nil {
		return FaceClusterView{}, err
	}
	log.Printf("Face cluster reassigned cluster_id=%d target_person_id=%d move_relations=%v added=%d removed=%d", clusterID, targetPersonID, moveRelations, moved, removed)
	return s.clusterView(ctx, clusterID)
}
