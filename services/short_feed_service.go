package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrShortFeedNoEligibleVideos = errors.New("no eligible short-feed videos")

// ErrShortFeedUnsupportedMedia 表示这个媒体类型在当前构建下还没有实现。
var ErrShortFeedUnsupportedMedia = errors.New("unsupported short-feed media kind")

// ErrShortFeedTagNameRequired 是手机端新建标签时名字为空。
var ErrShortFeedTagNameRequired = errors.New("tag name is required")

// ErrShortFeedAutomaticTag 是新建的名字撞上了系统自动标签：自动标签不给手动增删。
var ErrShortFeedAutomaticTag = errors.New("automatic tags cannot be attached manually")

// ErrShortFeedTagNameTooLong / ErrShortFeedTagNameInvalid：这是第一个由局域网客户端往全局表里
// 写任意字符串的入口，名字必须有长度上限、不能带控制字符。
var ErrShortFeedTagNameTooLong = errors.New("tag name too long")
var ErrShortFeedTagNameInvalid = errors.New("tag name contains control characters")

// ErrShortFeedInvalidMediaFilter 是 media 查询参数不在 all / video / image 之内。
var ErrShortFeedInvalidMediaFilter = errors.New("不支持的资源类型")

// shortFeedInlineImageMaxBytes 是直接把原图发给手机的体积上限。超过它就发降采样后的
// JPEG：手机屏幕用不上原始分辨率，而几十 MB 走 WiFi 要好几秒。
const shortFeedInlineImageMaxBytes int64 = 3 << 20

// shortFeedCandidateTTL 是候选快照的有效期。Feed 本身就是近似的随机抽取，
// 没必要每划一次就把整库重读一遍。删除等会主动让它失效。
const shortFeedCandidateTTL = 30 * time.Second

// shortFeedMissingRetries 是选中项文件缺失后的重试次数：只 stat 被选中的那一条，
// 而不是先把整库 stat 一遍。
const shortFeedMissingRetries = 8

type ShortFeedMedia struct {
	Path        string
	DisplayName string
	MIME        string
	ModTime     time.Time
}

type ShortFeedService struct {
	videoService *VideoService
	// imageThumbnail 复用桌面端的图片解码矩阵与缓存：RAW/HEIC 经 sips 转成 JPEG
	// 后才可在浏览器显示，因此"能不能显示"这件事由它裁定，而不是另立一张白名单。
	// 为 nil 时图片一律不入选（测试与降级路径）。
	imageThumbnail *ImageThumbnailService
	// 手机端直连上限的判定缓存与自动代理请求节流（short_feed_mobile_fit.go）。
	mobileFitMu          sync.Mutex
	heavyVerdicts        map[uint]shortFeedHeavyVerdict
	autoProxyRequestedAt map[uint]time.Time
	now                  func() time.Time
	randFloat64          func() float64
	// statFile 可注入，测试用它统计"一次抽取到底 stat 了几个文件"。
	statFile func(string) (os.FileInfo, error)

	candidateMu   sync.Mutex
	candidates    []shortFeedCandidate
	candidateHint *models.Video
	// candidateUnplayable 是被格式门排除的视频（既不能内嵌、手机端白名单不命中、也没有代理），
	// 只用来给 /feed/scopes 报 unplayable_count。
	candidateUnplayable []shortFeedCandidate
	candidatesAt        time.Time

	// deleteVideoFn 可注入，测试用它模拟「该磁盘不支持废纸篓」等删除结果；为 nil 时走 VideoService.DeleteVideo。
	deleteVideoFn func(id uint, deleteFile bool) error

	// 手机端访问控制（short_feed_auth.go）：PIN 会话与失败计数。
	authOnce sync.Once
	auth     *shortFeedAuth

	// 手机端删除记录（short_feed_interactions.go）：只有本服务通过手机端删掉的条目才能被「撤销」。
	mobileDeletedMu    sync.Mutex
	mobileDeleted      map[ShortFeedMediaRef]uint
	mobileDeletedOrder []ShortFeedMediaRef

	// mobileInlineMIME 的判定缓存（short_feed_mobile_mime.go）。
	mobileMIMEMu    sync.Mutex
	mobileMIMECache map[uint]shortFeedMobileMIMEVerdict
}

type ShortFeedFeedbackSyncResult struct {
	Enabled bool `json:"enabled"`
	// 视频侧
	LikesAdded     int64 `json:"likes_added"`
	LikesRemoved   int64 `json:"likes_removed"`
	FavoritesAdded int64 `json:"favorites_added"`
	// 图片侧。以前两侧共用一个自动标签，所以只有一组计数；改成各自的 is_liked
	// 列之后，两侧分开计数才说得清同步做了什么。
	ImageLikesAdded     int64 `json:"image_likes_added"`
	ImageLikesRemoved   int64 `json:"image_likes_removed"`
	ImageFavoritesAdded int64 `json:"image_favorites_added"`
}

func NewShortFeedService(videoService *VideoService) *ShortFeedService {
	if videoService == nil {
		videoService = &VideoService{}
	}
	return &ShortFeedService{
		videoService: videoService,
		now:          time.Now,
		randFloat64:  rand.Float64,
		statFile:     os.Stat,
	}
}

// invalidateCandidates 让候选快照立即失效（删除等改变可选集合的操作后调用）。
func (s *ShortFeedService) invalidateCandidates() {
	s.candidateMu.Lock()
	s.candidates = nil
	s.candidateHint = nil
	s.candidateUnplayable = nil
	s.candidatesAt = time.Time{}
	s.candidateMu.Unlock()
}

// SetImageThumbnailService 注入图片解码/缓存服务。app 层在构造完
// ImageThumbnailService 之后调用；未注入时图片不参与 feed。
func (s *ShortFeedService) SetImageThumbnailService(service *ImageThumbnailService) {
	s.imageThumbnail = service
}

// shortFeedImageEligible 图片入选判据。图片没有时长，所以视频那条时长门槛
// 在这里不适用；判据是"未失效 + 有可用解码器"，文件是否存在由调用方另行 stat。
func (s *ShortFeedService) shortFeedImageEligible(img models.Image) bool {
	if s.imageThumbnail == nil || img.IsStale {
		return false
	}
	return imageDecoderForFormatAndPath(img.Format, img.Path) != imageDecoderUnsupported
}

// loadEligibleImages 读取可入选的图片。与视频侧对称：稳定按 id 升序，预载标签。
func (s *ShortFeedService) loadEligibleImages(excludeIDs []uint) ([]models.Image, error) {
	if s.imageThumbnail == nil {
		return nil, nil
	}
	var images []models.Image
	// 刻意不 Preload("Tags")：GORM 会按加载条数生成 IN 参数，整库预载会撑爆
	// Postgres extended protocol 的 65535 参数上限（那是线路协议的硬限制）。
	// 权重需要的标签信息改由 tagBoostMap 精确取，展示需要的标签只为选中项再查。
	query := applyImageVisibility(database.DB.Model(&models.Image{}), database.DB).
		Order("id ASC")
	if len(excludeIDs) > 0 {
		query = query.Where("id NOT IN ?", excludeIDs)
	}
	if err := query.Find(&images).Error; err != nil {
		return nil, err
	}
	eligible := make([]models.Image, 0, len(images))
	for _, img := range images {
		if s.shortFeedImageEligible(img) {
			eligible = append(eligible, img)
		}
	}
	return eligible, nil
}

// resolveImageMedia 解析图片的可下发字节。view=true 取适配大图（RAW/HEIC 会被
// 转成 JPEG 缓存），否则取缩略图。
func (s *ShortFeedService) resolveImageMedia(imageID uint, view bool) (*ShortFeedMedia, error) {
	if s.imageThumbnail == nil {
		return nil, ErrShortFeedUnsupportedMedia
	}
	var img models.Image
	if err := applyImageVisibility(database.DB.Model(&models.Image{}), database.DB).
		Where("images.id = ?", imageID).First(&img).Error; err != nil {
		return nil, err
	}
	if !s.shortFeedImageEligible(img) {
		return nil, ErrShortFeedNoEligibleVideos
	}
	ctx := context.Background()
	var media *ImageMedia
	var err error
	if view {
		media, err = s.imageThumbnail.ResolveImageFeedView(ctx, imageID, shortFeedInlineImageMaxBytes)
	} else {
		media, err = s.imageThumbnail.ResolveImageThumbnail(ctx, imageID)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.markImageStale(imageID)
		}
		return nil, err
	}
	mime := media.MIME
	if !view || mime == "" {
		// 缩略图恒为 JPEG；大图分支缺 MIME 时同样按 JPEG 缓存处理。
		mime = "image/jpeg"
	}
	return &ShortFeedMedia{
		Path:        media.Path,
		DisplayName: img.Name,
		MIME:        mime,
		ModTime:     media.ModTime,
	}, nil
}

// markImageStale：images 表没有 stale_reason 列（原因只记在视频侧，D-PC06）。
func (s *ShortFeedService) markImageStale(imageID uint) {
	_ = database.DB.Model(&models.Image{}).Where("id = ?", imageID).Update("is_stale", true).Error
}

// shortFeedCandidate 是抽取阶段的统一候选：两种媒体在这一层不再有分支。
type shortFeedCandidate struct {
	ref ShortFeedMediaRef
	// boost 是这条内容因标签偏好获得的加权（上限 ShortFeedPreferenceBoostCap）。
	// 只存这个数而不是整串标签：抽签不需要标签名，展示才需要，而展示只涉及被选中的一条。
	boost float64
	video *models.Video
	image *models.Image
}

// NextItem 抽取下一条内容。exclude 是客户端最近看过的类型化标识，
// 混编流里图片与视频按同一套标签偏好加权后随机抽一条。
func (s *ShortFeedService) NextItem(exclude []ShortFeedMediaRef) (*ShortFeedItemDTO, error) {
	return s.NextItemInScope(exclude, ShortFeedScopeAll)
}

// NextItemInScope 在指定播放范围内抽下一条。范围只收窄候选池，
// 加权抽取本身与全部范围完全一致。
func (s *ShortFeedService) NextItemInScope(exclude []ShortFeedMediaRef, scope string) (*ShortFeedItemDTO, error) {
	return s.NextItemFiltered(exclude, scope, ShortFeedMediaFilterAll)
}

// NextItemFiltered 在播放范围之上再按资源类型（全部 / 仅视频 / 仅图片）收窄候选池。
// 两个维度正交：范围管"看过没、收藏没"，类型管"是视频还是图片"。
func (s *ShortFeedService) NextItemFiltered(exclude []ShortFeedMediaRef, scope string, mediaFilter string) (*ShortFeedItemDTO, error) {
	normalizedScope, err := normalizeShortFeedScope(scope)
	if err != nil {
		return nil, err
	}
	normalizedMedia, err := normalizeShortFeedMediaFilter(mediaFilter)
	if err != nil {
		return nil, err
	}
	all, unsupportedVideo, err := s.cachedCandidates()
	if err != nil {
		return nil, err
	}
	if all, err = s.filterCandidatesByScope(all, normalizedScope); err != nil {
		return nil, err
	}
	all = filterCandidatesByMedia(all, normalizedMedia)
	if len(all) == 0 {
		// 一条能播/能显示的都没有：如果还有存在但不可内联播放的视频，
		// 就把它连同原因一起给出去，让手机端能显示"这个格式放不了"。只看图片时
		// 这个提示没有意义。
		if unsupportedVideo != nil && normalizedMedia != ShortFeedMediaFilterImage {
			return s.videoDTO(unsupportedVideo, "inline_not_supported", "当前文件格式不适合浏览器内播放。")
		}
		return nil, ErrShortFeedNoEligibleVideos
	}

	pool := shortFeedExcludeCandidates(all, exclude)
	if len(pool) == 0 {
		// 排除集把所有候选都盖住了。小库上这很常见（客户端固定发最近 12 条），
		// 硬报耗尽会让几张图的库刷十几下就停流，所以回退到全量允许重复，
		// 但至少把"最近一条"排掉，避免同一条连着出现两次。
		pool = shortFeedExcludeCandidates(all, lastShortFeedRef(exclude))
		if len(pool) == 0 {
			pool = all
		}
	}

	// 只对被选中的那一条做 stat。以前是先把整库每个文件都 stat 一遍再抽签，
	// 外置盘上几千次系统调用会让每一次划动都等上好几秒。
	for attempt := 0; attempt < shortFeedMissingRetries && len(pool) > 0; attempt++ {
		index := s.weightedSelectIndex(pool)
		selected := pool[index]
		if !s.candidateFileExists(selected) {
			s.invalidateCandidates()
			pool = append(pool[:index:index], pool[index+1:]...)
			continue
		}
		// 标签只为被选中的这一条查：抽签阶段刻意没有预载。
		if selected.image != nil {
			image := *selected.image
			if err := database.DB.Preload("Tags").First(&image, image.ID).Error; err == nil {
				return s.imageDTO(&image)
			}
			return s.imageDTO(selected.image)
		}
		video := *selected.video
		if err := database.DB.Preload("Tags").First(&video, video.ID).Error; err == nil {
			return s.videoDTO(&video, "", "")
		}
		return s.videoDTO(selected.video, "", "")
	}
	return nil, ErrShortFeedNoEligibleVideos
}

// cachedCandidates 返回候选快照。Feed 是近似的随机抽取，没必要每划一次就把整库
// 重读一遍；快照过期或被主动失效后才重建。
// filterCandidatesByMedia 按资源类型收窄；全部时原样返回，不复制。
func filterCandidatesByMedia(candidates []shortFeedCandidate, mediaFilter string) []shortFeedCandidate {
	if mediaFilter == ShortFeedMediaFilterAll {
		return candidates
	}
	filtered := make([]shortFeedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		switch mediaFilter {
		case ShortFeedMediaFilterVideo:
			if candidate.video != nil {
				filtered = append(filtered, candidate)
			}
		case ShortFeedMediaFilterImage:
			if candidate.image != nil {
				filtered = append(filtered, candidate)
			}
		}
	}
	return filtered
}

func (s *ShortFeedService) cachedCandidates() ([]shortFeedCandidate, *models.Video, error) {
	snapshot, err := s.cachedSnapshot()
	if err != nil {
		return nil, nil, err
	}
	return snapshot.candidates, snapshot.hint, nil
}

// shortFeedSnapshot 是一次候选收集的完整结果。
type shortFeedSnapshot struct {
	candidates []shortFeedCandidate
	// hint 是"存在但不可播放"的视频样本，候选为空时用来给出可解释的原因。
	hint *models.Video
	// unplayable 是被格式门排除的全部视频，供 unplayable_count 按范围统计。
	unplayable []shortFeedCandidate
}

func (s *ShortFeedService) cachedSnapshot() (shortFeedSnapshot, error) {
	s.candidateMu.Lock()
	if s.candidates != nil && s.now().Sub(s.candidatesAt) < shortFeedCandidateTTL {
		snapshot := shortFeedSnapshot{candidates: s.candidates, hint: s.candidateHint, unplayable: s.candidateUnplayable}
		s.candidateMu.Unlock()
		return snapshot, nil
	}
	s.candidateMu.Unlock()

	snapshot, err := s.collectSnapshot()
	if err != nil {
		return shortFeedSnapshot{}, err
	}
	s.candidateMu.Lock()
	s.candidates, s.candidateHint, s.candidateUnplayable, s.candidatesAt = snapshot.candidates, snapshot.hint, snapshot.unplayable, s.now()
	s.candidateMu.Unlock()
	return snapshot, nil
}

// candidateFileExists 只检查这一条；缺失的顺手标记 stale，下次重建快照时自然排除。
func (s *ShortFeedService) candidateFileExists(candidate shortFeedCandidate) bool {
	path := ""
	if candidate.video != nil {
		path = candidate.video.Path
	} else if candidate.image != nil {
		path = candidate.image.Path
	}
	if path == "" {
		return false
	}
	info, err := s.stat(path)
	if err != nil || info.IsDir() {
		if candidate.video != nil {
			s.markStale(candidate.ref.ID, shortFeedStaleReason(err))
		} else {
			s.markImageStale(candidate.ref.ID)
		}
		return false
	}
	// 抽签阶段只看"有没有 ready 代理行"，不校验指纹（那一步要 stat 源文件）。
	// 抽中之后必须补上这一校验：代理刚失效（源被替换、产物被删）的话，
	// 这条视频本来就不该入选，放过去手机端只会拿到一段播不了的源字节。
	// resolveVideoProxy 顺手把失效的文件与表行清掉，重建快照时它自然消失。
	if candidate.video != nil {
		if _, inline := s.mobileMIMEForVideo(*candidate.video); !inline {
			if s.resolveVideoProxy(*candidate.video, false) == nil {
				return false
			}
		}
	}
	return true
}

func (s *ShortFeedService) stat(path string) (os.FileInfo, error) {
	if s.statFile != nil {
		return s.statFile(path)
	}
	return os.Stat(path)
}

// collectCandidates 合并两种媒体的可入选集合。第二个返回值是"存在但不可内联
// 播放"的视频样本，仅在候选为空时用于给出可解释的原因。
// shortFeedTagBoosts 是"哪些媒体因标签偏好该被加权"的稀疏映射。
// 偏好只存在于用户喜欢过的少量标签上，所以这里只查那几个标签的关联，
// 参数量与库大小无关。
type shortFeedTagBoosts struct {
	videos map[uint]float64
	images map[uint]float64
}

func (b shortFeedTagBoosts) forVideo(id uint) float64 { return clampShortFeedBoost(b.videos[id]) }
func (b shortFeedTagBoosts) forImage(id uint) float64 { return clampShortFeedBoost(b.images[id]) }

func clampShortFeedBoost(boost float64) float64 {
	if boost > ShortFeedPreferenceBoostCap {
		return ShortFeedPreferenceBoostCap
	}
	if boost < 0 {
		return 0
	}
	return boost
}

func (s *ShortFeedService) loadTagBoosts() (shortFeedTagBoosts, error) {
	boosts := shortFeedTagBoosts{videos: map[uint]float64{}, images: map[uint]float64{}}
	prefs, err := s.tagPreferenceMap()
	if err != nil {
		return boosts, err
	}
	tagIDs := make([]uint, 0, len(prefs))
	for tagID, score := range prefs {
		if score != 0 {
			tagIDs = append(tagIDs, tagID)
		}
	}
	if len(tagIDs) == 0 {
		return boosts, nil
	}
	type link struct {
		MediaID uint
		TagID   uint
	}
	var videoLinks []link
	if err := database.DB.Table("video_tags").
		Select("video_id AS media_id, tag_id").
		Where("tag_id IN ?", tagIDs).Scan(&videoLinks).Error; err != nil {
		return boosts, err
	}
	for _, row := range videoLinks {
		boosts.videos[row.MediaID] += prefs[row.TagID]
	}
	var imageLinks []link
	if err := database.DB.Table("image_tags").
		Select("image_id AS media_id, tag_id").
		Where("tag_id IN ?", tagIDs).Scan(&imageLinks).Error; err != nil {
		return boosts, err
	}
	for _, row := range imageLinks {
		boosts.images[row.MediaID] += prefs[row.TagID]
	}
	return boosts, nil
}

func (s *ShortFeedService) collectCandidates() ([]shortFeedCandidate, *models.Video, error) {
	snapshot, err := s.collectSnapshot()
	if err != nil {
		return nil, nil, err
	}
	return snapshot.candidates, snapshot.hint, nil
}

func (s *ShortFeedService) collectSnapshot() (shortFeedSnapshot, error) {
	boosts, err := s.loadTagBoosts()
	if err != nil {
		return shortFeedSnapshot{}, err
	}
	// 这里不做文件存在性检查：抽中之后只 stat 那一条即可。
	existingVideos, err := s.loadEligibleVideos(nil)
	if err != nil {
		return shortFeedSnapshot{}, err
	}

	// 有有效代理的视频也入选（D-004）：内嵌白名单不命中但代理已经生成好了，
	// 手机端拿到的是代理字节。
	proxied, err := loadProxiedVideoIDs()
	if err != nil {
		return shortFeedSnapshot{}, err
	}

	candidates := make([]shortFeedCandidate, 0, len(existingVideos))
	var unsupportedVideo *models.Video
	var unplayable []shortFeedCandidate
	for i := range existingVideos {
		video := existingVideos[i]
		// 手机端白名单（§8.7）在内嵌白名单之上多认 h264/hevc + aac 的 .mov。
		if _, ok := s.mobileMIMEForVideo(video); !ok {
			if _, hasProxy := proxied[video.ID]; !hasProxy {
				if unsupportedVideo == nil {
					unsupportedVideo = &existingVideos[i]
				}
				unplayable = append(unplayable, shortFeedCandidate{
					ref:   ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID},
					video: &existingVideos[i],
				})
				continue
			}
		}
		candidates = append(candidates, shortFeedCandidate{
			ref:   ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID},
			boost: boosts.forVideo(video.ID),
			video: &existingVideos[i],
		})
	}

	images, err := s.loadEligibleImages(nil)
	if err != nil {
		return shortFeedSnapshot{}, err
	}
	for i := range images {
		candidates = append(candidates, shortFeedCandidate{
			ref:   ShortFeedMediaRef{Kind: ShortFeedMediaImage, ID: images[i].ID},
			boost: boosts.forImage(images[i].ID),
			image: &images[i],
		})
	}
	return shortFeedSnapshot{candidates: candidates, hint: unsupportedVideo, unplayable: unplayable}, nil
}

func (s *ShortFeedService) imageFileExists(img models.Image) bool {
	info, err := os.Stat(img.Path)
	if err != nil {
		if os.IsNotExist(err) {
			s.markImageStale(img.ID)
		}
		return false
	}
	if info.IsDir() {
		s.markImageStale(img.ID)
		return false
	}
	return true
}

func shortFeedExcludeCandidates(candidates []shortFeedCandidate, exclude []ShortFeedMediaRef) []shortFeedCandidate {
	if len(exclude) == 0 {
		return candidates
	}
	excluded := make(map[ShortFeedMediaRef]struct{}, len(exclude))
	for _, ref := range exclude {
		excluded[ref] = struct{}{}
	}
	kept := make([]shortFeedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, skip := excluded[candidate.ref]; skip {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
}

func lastShortFeedRef(exclude []ShortFeedMediaRef) []ShortFeedMediaRef {
	if len(exclude) == 0 {
		return nil
	}
	return exclude[len(exclude)-1:]
}

func (s *ShortFeedService) weightedSelectIndex(candidates []shortFeedCandidate) int {
	if len(candidates) == 1 {
		return 0
	}
	weights := make([]float64, len(candidates))
	total := 0.0
	for i := range candidates {
		weights[i] = 1.0 + candidates[i].boost
		total += weights[i]
	}
	if total <= 0 {
		return 0
	}
	draw := s.randFloat64() * total
	cumulative := 0.0
	for i, weight := range weights {
		cumulative += weight
		if draw <= cumulative {
			return i
		}
	}
	return len(candidates) - 1
}

// shortFeedFavoriteOrder 收藏页的排序：favorited_at 倒序、NULL 最后、id 倒序。
// NULLS LAST 两个后端写法不同，用 CASE 表达式统一（历史收藏并集迁移前的行可能没有时间）。
func shortFeedFavoriteOrder(query *gorm.DB, table string) *gorm.DB {
	return query.
		Order("CASE WHEN " + table + ".favorited_at IS NULL THEN 1 ELSE 0 END").
		Order(table + ".favorited_at DESC").
		Order(table + ".id DESC")
}

// FavoriteItems 收藏页：收藏的唯一数据是 videos/images.is_favorite（D-PC40），与桌面端同源；
// 视频与图片合并后按 favorited_at 倒序（NULL 最后），同时刻按 id 倒序。
func (s *ShortFeedService) FavoriteItems() ([]ShortFeedItemDTO, error) {
	type favoriteEntry struct {
		at  *time.Time
		id  uint
		dto ShortFeedItemDTO
	}
	var entries []favoriteEntry

	var videos []models.Video
	maxDurationSeconds := s.maxDurationSeconds()
	// 与 feed 同一可见边界：扫描根 + 黑名单 + 未失效（PLAY-01）。
	videoQuery, err := applyScanRootScope(database.DB.Model(&models.Video{}).Preload("Tags"))
	if err != nil {
		return nil, err
	}
	videoQuery = videoQuery.
		Where("videos.is_favorite = ?", true).
		Where("videos.is_stale = ?", false).
		Where("videos.duration > ? AND videos.duration < ?", 0, maxDurationSeconds)
	if err := shortFeedFavoriteOrder(videoQuery, "videos").Find(&videos).Error; err != nil {
		return nil, err
	}
	for _, video := range s.filterExistingVideos(videos) {
		video := video
		dto, err := s.videoDTO(&video, "", "")
		if err != nil {
			return nil, err
		}
		entries = append(entries, favoriteEntry{at: video.FavoritedAt, id: video.ID, dto: *dto})
	}

	if s.imageThumbnail != nil {
		var images []models.Image
		imageQuery := applyImageVisibility(database.DB.Model(&models.Image{}).Preload("Tags"), database.DB).
			Where("images.is_favorite = ?", true)
		if err := shortFeedFavoriteOrder(imageQuery, "images").Find(&images).Error; err != nil {
			return nil, err
		}
		for i := range images {
			if !s.shortFeedImageEligible(images[i]) || !s.imageFileExists(images[i]) {
				continue
			}
			dto, err := s.imageDTO(&images[i])
			if err != nil {
				return nil, err
			}
			entries = append(entries, favoriteEntry{at: images[i].FavoritedAt, id: images[i].ID, dto: *dto})
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		switch {
		case a.at != nil && b.at == nil:
			return true
		case a.at == nil && b.at != nil:
			return false
		case a.at != nil && b.at != nil && !a.at.Equal(*b.at):
			return a.at.After(*b.at)
		}
		return a.id > b.id
	})
	result := make([]ShortFeedItemDTO, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.dto)
	}
	return result, nil
}

// loadVisibleVideo 读取一条对手机端可见的视频：扫描根 + 黑名单之内（applyScanRootScope）。
// 不可见与不存在同样返回 gorm.ErrRecordNotFound，调用方一律映射成 404。
func (s *ShortFeedService) loadVisibleVideo(videoID uint) (models.Video, error) {
	var video models.Video
	query, err := applyScanRootScope(database.DB.Model(&models.Video{}))
	if err != nil {
		return video, err
	}
	err = query.Where("videos.id = ?", videoID).First(&video).Error
	return video, err
}

// loadEligibleVideo 在可见的基础上再套 feed 的入选条件（未失效、时长在范围内）。
func (s *ShortFeedService) loadEligibleVideo(videoID uint) (models.Video, error) {
	video, err := s.loadVisibleVideo(videoID)
	if err != nil {
		return video, err
	}
	if !shortFeedEligible(video, s.maxDurationSeconds()) {
		return video, ErrShortFeedNoEligibleVideos
	}
	return video, nil
}

// RecordPlayback 记录一次播放/浏览。
func (s *ShortFeedService) RecordPlayback(ref ShortFeedMediaRef) (*ShortFeedInteractionDTO, error) {
	return s.RecordPlaybackSession(ref, "")
}

// RecordPlaybackSession adds an optional persistent effective-view identity.
func (s *ShortFeedService) RecordPlaybackSession(ref ShortFeedMediaRef, sessionID string) (*ShortFeedInteractionDTO, error) {
	if len(sessionID) > viewEventSessionIDMaxLength || sessionID != "" && strings.TrimSpace(sessionID) == "" {
		return nil, ErrViewingNoteInvalid
	}
	sessionID = strings.TrimSpace(sessionID)
	if ref.Kind == ShortFeedMediaImage {
		return s.recordImageView(ref)
	}
	if ref.Kind != ShortFeedMediaVideo {
		return nil, ErrShortFeedUnsupportedMedia
	}
	videoID := ref.ID
	now := s.now()
	maxDurationSeconds := s.maxDurationSeconds()
	// 可见性检查放在事务外：它要读扫描目录与黑名单，不该占着事务连接去查别的表。
	if _, err := s.loadVisibleVideo(videoID); err != nil {
		return nil, err
	}
	key := ""
	if sessionID != "" {
		key = diarySessionKey(models.PlayEventSourceMobileFeed, videoID, sessionID)
	}
	var interaction models.ShortFeedInteraction
	err := database.Transaction(func(tx *gorm.DB) error {
		var video models.Video
		if err := tx.First(&video, videoID).Error; err != nil {
			return err
		}
		if !shortFeedEligible(video, maxDurationSeconds) {
			return ErrShortFeedNoEligibleVideos
		}
		if key != "" {
			exists, err := diarySessionRecorded(tx, key)
			if err != nil {
				return err
			}
			if exists {
				return tx.Where("video_id = ?", videoID).First(&interaction).Error
			}
		}
		if err := tx.Model(&models.Video{}).Where("id = ?", videoID).Updates(map[string]interface{}{
			"random_play_count": gorm.Expr("random_play_count + 1"),
			"last_played_at":    now,
			"is_stale":          false,
			"stale_reason":      "",
		}).Error; err != nil {
			return err
		}
		// 播放事件与计数递增同事务：手机端与桌面端在账本里必须是同一种一致性。
		event := models.PlayEvent{VideoID: videoID, PlayedAt: now, Source: models.PlayEventSourceMobileFeed}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
		if key != "" {
			if err := appendViewingDiaryTx(tx, video, event, key); err != nil {
				return err
			}
		}

		return upsertShortFeedInteraction(tx, videoID, func(row *models.ShortFeedInteraction) {
			row.ViewCount++
			row.LastViewedAt = &now
			interaction = *row
		})
	})
	if errors.Is(err, errDiarySessionExists) {
		err = database.WithOperationContext(context.Background(), func(db *gorm.DB) error {
			exists, checkErr := diarySessionRecorded(db, key)
			if checkErr != nil {
				return checkErr
			}
			if !exists {
				return errDiarySessionExists
			}
			return db.Where("video_id = ?", videoID).First(&interaction).Error
		})
	}
	if err != nil {
		return nil, err
	}
	return s.videoInteractionDTO(&interaction, nil), nil
}

// SetLiked 手机端点赞。点赞的唯一数据是 videos/images.is_liked（D-PC40）：直接走桌面同一个
// setter，不再有互动表投影，也就没有「同步一下把桌面点赞清掉」的路径。
// 互动表只继续记 view_count / last_viewed_at。
func (s *ShortFeedService) SetLiked(ref ShortFeedMediaRef, liked bool) (*ShortFeedInteractionDTO, error) {
	if ref.Kind == ShortFeedMediaImage {
		return s.setImageLiked(ref, liked)
	}
	if ref.Kind != ShortFeedMediaVideo {
		return nil, ErrShortFeedUnsupportedMedia
	}
	before, err := s.loadEligibleVideo(ref.ID)
	if err != nil {
		return nil, err
	}
	updated, err := s.videoService.SetVideoLiked(ref.ID, liked)
	if err != nil {
		return nil, err
	}
	if liked && !before.IsLiked {
		if err := s.bumpTagPreferences(updated.Tags); err != nil {
			// 点赞已提交；推荐偏好只是弱加权，失败只记录，不把已成功的操作报成失败。
			log.Printf("short-feed: 记录标签偏好失败 video_id=%d: %v", ref.ID, err)
		}
	}
	return s.videoInteractionDTO(nil, updated), nil
}

func (s *ShortFeedService) SetFavorited(ref ShortFeedMediaRef, favorited bool) (*ShortFeedInteractionDTO, error) {
	if ref.Kind == ShortFeedMediaImage {
		return s.setImageFavorited(ref, favorited)
	}
	if ref.Kind != ShortFeedMediaVideo {
		return nil, ErrShortFeedUnsupportedMedia
	}
	if _, err := s.loadEligibleVideo(ref.ID); err != nil {
		return nil, err
	}
	updated, err := s.videoService.SetVideoFavorite(ref.ID, favorited)
	if err != nil {
		return nil, err
	}
	return s.videoInteractionDTO(nil, updated), nil
}

// bumpTagPreferences 对一批标签各加一步偏好。
func (s *ShortFeedService) bumpTagPreferences(tags []models.Tag) error {
	if len(tags) == 0 {
		return nil
	}
	return database.Transaction(func(tx *gorm.DB) error {
		for _, tag := range tags {
			if err := incrementShortFeedTagPreference(tx, tag.ID, ShortFeedPreferenceStep); err != nil {
				return err
			}
		}
		return nil
	})
}

// videoInteractionDTO 组装视频互动结果：liked/favorited/favorited_at 取自 videos 行（唯一数据），
// 浏览次数取自互动表。传 interaction 时（RecordPlayback）直接用它，否则现读；video 为 nil 时
// 按 interaction 里的 video_id 去读行。
func (s *ShortFeedService) videoInteractionDTO(interaction *models.ShortFeedInteraction, video *models.Video) *ShortFeedInteractionDTO {
	var row models.ShortFeedInteraction
	switch {
	case interaction != nil:
		row = *interaction
	case video != nil:
		if loaded, err := interactionForVideo(video.ID); err == nil {
			row = loaded
		} else {
			row = models.ShortFeedInteraction{VideoID: video.ID}
		}
	}
	if video == nil {
		var loaded models.Video
		if err := database.DB.First(&loaded, row.VideoID).Error; err == nil {
			video = &loaded
		}
	}
	dto := interactionDTO(&row)
	dto.LikedAt = nil
	dto.FavoritedAt = nil
	dto.Liked = false
	dto.Favorited = false
	if video != nil {
		dto.Liked = video.IsLiked
		dto.Favorited = video.IsFavorite
		dto.FavoritedAt = video.FavoritedAt
	}
	return dto
}

// SyncFeedback 保留为空操作。
//
// 以前它把手机端互动表投影进主片库，并对 is_liked 双向对账：那会让桌面端点赞在下一次
// 同步时被清掉（手机端从没点过赞）。D-PC40 之后收藏与点赞只有 videos/images 一份数据，
// 「反馈回流」开关（short_feed_feedback_sync_enabled）除了这两项投影之外不控制任何行为，
// 也就没有东西可同步。保留方法与返回结构只为让 App 层现有调用点继续编译，
// 调用点与设置页入口由接线切片（P-029 / P-033）删除，列保留不删。
func (s *ShortFeedService) SyncFeedback() (ShortFeedFeedbackSyncResult, error) {
	return ShortFeedFeedbackSyncResult{}, nil
}

func (s *ShortFeedService) DeleteItem(ref ShortFeedMediaRef) error {
	// 删除会动磁盘文件：不可见的条目（黑名单、扫描根之外、失效）一律按不存在处理，什么都不动。
	if err := s.ensureItemVisible(ref); err != nil {
		return err
	}
	switch ref.Kind {
	case ShortFeedMediaVideo:
		deleteVideo := s.videoService.DeleteVideo
		if s.deleteVideoFn != nil {
			deleteVideo = s.deleteVideoFn
		}
		err := deleteVideo(ref.ID, true)
		if err == nil {
			s.rememberMobileDeleted(ref)
			s.invalidateCandidates()
		}
		return err
	case ShortFeedMediaImage:
		err := NewImageService().DeleteImage(ref.ID, true)
		if err == nil {
			s.rememberMobileDeleted(ref)
			s.invalidateCandidates()
		}
		return err
	default:
		return ErrShortFeedUnsupportedMedia
	}
}

// recordImageView 图片没有播放计数与 last_played_at 这类视频专属列，
// 只记浏览次数与最近浏览时间，并顺带清掉 stale 标记（文件刚被成功读出）。
func (s *ShortFeedService) recordImageView(ref ShortFeedMediaRef) (*ShortFeedInteractionDTO, error) {
	now := s.now()
	var state shortFeedInteractionState
	var viewed *models.Image
	err := database.Transaction(func(tx *gorm.DB) error {
		img, err := s.loadEligibleImage(tx, ref.ID)
		if err != nil {
			return err
		}
		viewed = img
		if err := tx.Model(&models.Image{}).Where("id = ?", img.ID).Update("is_stale", false).Error; err != nil {
			return err
		}
		state, err = upsertShortFeedInteractionFor(tx, ref, func(row *shortFeedInteractionState) {
			row.ViewCount++
			row.LastViewedAt = &now
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	// liked/favorited 取自 images 行（唯一数据），互动表只提供浏览次数。
	dto := stateInteractionDTO(ref, state)
	dto.Liked = viewed.IsLiked
	dto.Favorited = viewed.IsFavorite
	dto.LikedAt = nil
	dto.FavoritedAt = viewed.FavoritedAt
	return dto, nil
}

func (s *ShortFeedService) setImageLiked(ref ShortFeedMediaRef, liked bool) (*ShortFeedInteractionDTO, error) {
	before, err := s.loadEligibleImage(database.DB, ref.ID)
	if err != nil {
		return nil, err
	}
	updated, err := NewImageLibraryService().SetImageLiked(ref.ID, liked)
	if err != nil {
		return nil, err
	}
	if liked && !before.IsLiked {
		// 标签偏好表是图片与视频共用的，因为 tags 表本身就共用。
		if err := s.bumpTagPreferences(updated.Tags); err != nil {
			log.Printf("short-feed: 记录标签偏好失败 image_id=%d: %v", ref.ID, err)
		}
	}
	return s.imageInteractionDTO(ref, updated), nil
}

func (s *ShortFeedService) setImageFavorited(ref ShortFeedMediaRef, favorited bool) (*ShortFeedInteractionDTO, error) {
	if _, err := s.loadEligibleImage(database.DB, ref.ID); err != nil {
		return nil, err
	}
	updated, err := NewImageLibraryService().SetImageFavorite(ref.ID, favorited)
	if err != nil {
		return nil, err
	}
	return s.imageInteractionDTO(ref, updated), nil
}

// imageInteractionDTO 与视频侧对称：liked/favorited 取自 images 行，浏览次数取自互动表。
func (s *ShortFeedService) imageInteractionDTO(ref ShortFeedMediaRef, img *models.Image) *ShortFeedInteractionDTO {
	state, err := loadShortFeedInteraction(ref)
	if err != nil {
		state = shortFeedInteractionState{}
	}
	dto := stateInteractionDTO(ref, state)
	dto.Liked = img.IsLiked
	dto.Favorited = img.IsFavorite
	dto.LikedAt = nil
	dto.FavoritedAt = img.FavoritedAt
	return dto
}

// loadEligibleImage 读取并校验图片资格，交互类方法统一走它，避免各处重复判据。
func (s *ShortFeedService) loadEligibleImage(tx *gorm.DB, imageID uint) (*models.Image, error) {
	var img models.Image
	// 与 feed 同一图片可见边界（未失效 + 黑名单）：不可见的图片同样按不存在处理。
	query := applyImageVisibility(tx.Model(&models.Image{}), tx)
	if err := query.Preload("Tags").Where("images.id = ?", imageID).First(&img).Error; err != nil {
		return nil, err
	}
	if !s.shortFeedImageEligible(img) {
		return nil, ErrShortFeedNoEligibleVideos
	}
	return &img, nil
}

// ResolveMedia 解析一条内容的可下发字节。
func (s *ShortFeedService) ResolveMedia(ref ShortFeedMediaRef) (*ShortFeedMedia, error) {
	if ref.Kind == ShortFeedMediaImage {
		return s.resolveImageMedia(ref.ID, true)
	}
	if ref.Kind != ShortFeedMediaVideo {
		return nil, ErrShortFeedUnsupportedMedia
	}
	// 先套可见边界（扫描根 + 黑名单）：黑名单目录里的视频按不存在处理，映射成 404。
	video, err := s.loadEligibleVideo(ref.ID)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(video.Path)
	if err != nil {
		if os.IsNotExist(err) {
			s.markStale(video.ID, models.StaleReasonMissingFile)
		}
		return nil, err
	}
	if info.IsDir() {
		s.markStale(video.ID, models.StaleReasonReadError)
		return nil, fmt.Errorf("short-feed media path is directory")
	}
	mimeType, ok := s.mobileMIMEForVideo(video)
	if !ok {
		// 白名单不命中时才可能有代理：命中就下发代理字节（D-004）。
		// 路径形态不变，手机端仍然只知道 /short-media/video/{id} 这一个地址。
		if proxies := s.videoService.playbackProxies(); proxies != nil {
			if proxy := proxies.resolveValidProxy(video.ID, playbackProxyFingerprintOf(info), true); proxy != nil {
				if proxyInfo, statErr := s.stat(proxy.Path); statErr == nil {
					return &ShortFeedMedia{
						Path:        proxy.Path,
						DisplayName: video.Name,
						MIME:        playbackProxyMIME,
						ModTime:     proxyInfo.ModTime(),
					}, nil
				}
			}
		}
		// 白名单不命中、又没有可用代理：这条视频没有任何能发给浏览器的字节。
		// **不回落到源文件**——把一段 mkv 配上猜出来的 MIME 发过去，手机端只会
		// 得到一个放不出来的黑框，还白占几十兆流量。报"没有可播内容"，
		// 由 writeMediaError 收敛成 404，这才是诚实的答案。
		//
		// 这一条也兜住"抽签时代理还有效、下发时刚失效"的窗口：抽签阶段只看
		// 有没有 ready 行，指纹校验落在 resolveValidProxy 里，就是上面那一步。
		return nil, ErrShortFeedNoEligibleVideos
	}
	// 白名单命中但源文件超出手机端直连上限（4K、高码率）：有代理就发代理，
	// 没有就照发源文件——总比放不了强——同时请求后台生成，下次就能换上。
	if s.sourceTooHeavyForMobileCached(video, info) {
		if proxies := s.videoService.playbackProxies(); proxies != nil {
			if proxy := proxies.resolveValidProxy(video.ID, playbackProxyFingerprintOf(info), true); proxy != nil {
				if proxyInfo, statErr := s.stat(proxy.Path); statErr == nil {
					return &ShortFeedMedia{
						Path:        proxy.Path,
						DisplayName: video.Name,
						MIME:        playbackProxyMIME,
						ModTime:     proxyInfo.ModTime(),
					}, nil
				}
			}
			s.requestMobileFitProxy(proxies, video.ID)
		}
	}
	return &ShortFeedMedia{
		Path:        video.Path,
		DisplayName: video.Name,
		MIME:        mimeType,
		ModTime:     info.ModTime(),
	}, nil
}

func (s *ShortFeedService) loadEligibleVideos(excludeIDs []uint) ([]models.Video, error) {
	var videos []models.Video
	maxDurationSeconds := s.maxDurationSeconds()
	// 同上：抽签阶段不预载标签。
	// 可见性与桌面默认视图同一口径（PLAY-01）：扫描根 + 黑名单 + 未失效。规则只在
	// applyScanRootScope 里有一份，这里不复制。
	query, err := applyScanRootScope(database.DB.Model(&models.Video{}))
	if err != nil {
		return nil, err
	}
	query = query.
		Where("videos.is_stale = ?", false).
		Where("videos.duration > ? AND videos.duration < ?", 0, maxDurationSeconds).
		Order("videos.id ASC")
	if len(excludeIDs) > 0 {
		query = query.Where("videos.id NOT IN ?", excludeIDs)
	}
	if err := query.Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

func (s *ShortFeedService) filterExistingVideos(videos []models.Video) []models.Video {
	existing := make([]models.Video, 0, len(videos))
	for _, video := range videos {
		if s.videoFileExists(video) {
			existing = append(existing, video)
		}
	}
	return existing
}

func (s *ShortFeedService) videoFileExists(video models.Video) bool {
	info, err := os.Stat(video.Path)
	if err != nil {
		if os.IsNotExist(err) {
			s.markStale(video.ID, models.StaleReasonMissingFile)
		}
		return false
	}
	if info.IsDir() {
		s.markStale(video.ID, models.StaleReasonReadError)
		return false
	}
	return true
}

// markStale 把视频标成失效，**必须同时写原因**（详细设计 §3.1）：is_stale 与 stale_reason 成对出现。
func (s *ShortFeedService) markStale(videoID uint, reason string) {
	_ = database.DB.Model(&models.Video{}).Where("id = ?", videoID).
		Updates(map[string]interface{}{"is_stale": true, "stale_reason": reason}).Error
}

// shortFeedStaleReason 把文件检查错误翻成失效原因：文件确实不在是 missing_file，
// 其余（权限、I/O、路径变成目录）是 read_error。
func shortFeedStaleReason(err error) string {
	if err != nil && os.IsNotExist(err) {
		return models.StaleReasonMissingFile
	}
	return models.StaleReasonReadError
}

func (s *ShortFeedService) tagPreferenceMap() (map[uint]float64, error) {
	var rows []models.ShortFeedTagPreference
	if err := database.DB.Find(&rows).Error; err != nil {
		return nil, err
	}
	prefs := make(map[uint]float64, len(rows))
	for _, row := range rows {
		prefs[row.TagID] = row.Score
	}
	return prefs, nil
}

func (s *ShortFeedService) weightedSelect(videos []models.Video, prefs map[uint]float64) models.Video {
	if len(videos) == 1 {
		return videos[0]
	}
	weights := make([]float64, len(videos))
	total := 0.0
	for i := range videos {
		weights[i] = shortFeedWeight(videos[i], prefs)
		total += weights[i]
	}
	if total <= 0 {
		return videos[0]
	}
	draw := s.randFloat64() * total
	cumulative := 0.0
	for i, weight := range weights {
		cumulative += weight
		if draw <= cumulative {
			return videos[i]
		}
	}
	return videos[len(videos)-1]
}

// ResolveThumbnail 解析缩略图字节。视频暂无缩略图路线，只对图片有效。
func (s *ShortFeedService) ResolveThumbnail(ref ShortFeedMediaRef) (*ShortFeedMedia, error) {
	if ref.Kind != ShortFeedMediaImage {
		return nil, ErrShortFeedUnsupportedMedia
	}
	return s.resolveImageMedia(ref.ID, false)
}

// shortFeedRefIDs 取出指定媒体类型的裸 ID，供仍按单表查询的内部路径使用。
func shortFeedRefIDs(refs []ShortFeedMediaRef, kind ShortFeedMediaKind) []uint {
	ids := make([]uint, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind == kind && ref.ID > 0 {
			ids = append(ids, ref.ID)
		}
	}
	return ids
}

func shortFeedMediaURL(ref ShortFeedMediaRef) string {
	return fmt.Sprintf("/short-media/%s/%d", ref.Kind, ref.ID)
}

func shortFeedWeight(video models.Video, prefs map[uint]float64) float64 {
	return shortFeedTagWeight(video.Tags, prefs)
}

// shortFeedTagWeight 权重只看标签，与媒体类型无关：tags 表本身图片与视频共用。
func shortFeedTagWeight(tags []models.Tag, prefs map[uint]float64) float64 {
	boost := 0.0
	for _, tag := range tags {
		boost += prefs[tag.ID]
	}
	if boost > ShortFeedPreferenceBoostCap {
		boost = ShortFeedPreferenceBoostCap
	}
	if boost < 0 {
		boost = 0
	}
	return 1.0 + boost
}

func (s *ShortFeedService) videoDTO(video *models.Video, reasonCode string, reasonMessage string) (*ShortFeedItemDTO, error) {
	mediaURL := ""
	mediaMIME := ""
	if mimeType, ok := s.mobileMIMEForVideo(*video); ok {
		mediaURL = shortFeedMediaURL(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID})
		mediaMIME = mimeType
	} else if proxy := s.resolveVideoProxy(*video, false); proxy != nil {
		// 有有效代理：媒体地址不变，MIME 换成代理产物的 mp4（D-004）。
		mediaURL = shortFeedMediaURL(ShortFeedMediaRef{Kind: ShortFeedMediaVideo, ID: video.ID})
		mediaMIME = playbackProxyMIME
	} else if reasonCode == "" {
		reasonCode = "inline_not_supported"
		reasonMessage = "当前文件格式不适合浏览器内播放。"
	}

	tags := make([]ShortFeedTagDTO, 0, len(video.Tags))
	for _, tag := range video.Tags {
		tags = append(tags, ShortFeedTagDTO{ID: tag.ID, Name: tag.Name, Color: tag.Color})
	}
	return &ShortFeedItemDTO{
		MediaKind:      ShortFeedMediaVideo,
		ID:             video.ID,
		Name:           video.Name,
		Duration:       video.Duration,
		Width:          video.Width,
		Height:         video.Height,
		Tags:           tags,
		MediaURL:       mediaURL,
		MediaMIME:      mediaMIME,
		Liked:          video.IsLiked,
		Favorited:      video.IsFavorite,
		PersonalRating: video.PersonalRating,
		Watched:        video.IsWatched,
		ReasonCode:     reasonCode,
		ReasonMessage:  reasonMessage,
	}, nil
}

// imageDTO 图片条目。没有时长与进度条；图说用的是已接受的标签，Tags 在这里已经预载好。
func (s *ShortFeedService) imageDTO(img *models.Image) (*ShortFeedItemDTO, error) {
	ref := ShortFeedMediaRef{Kind: ShortFeedMediaImage, ID: img.ID}
	tags := make([]ShortFeedTagDTO, 0, len(img.Tags))
	for _, tag := range img.Tags {
		tags = append(tags, ShortFeedTagDTO{ID: tag.ID, Name: tag.Name, Color: tag.Color})
	}
	return &ShortFeedItemDTO{
		MediaKind: ShortFeedMediaImage,
		ID:        img.ID,
		Name:      img.Name,
		Width:     img.Width,
		Height:    img.Height,
		Tags:      tags,
		MediaURL:  shortFeedMediaURL(ref),
		MediaMIME: "image/jpeg",
		Liked:     img.IsLiked,
		Favorited: img.IsFavorite,
		// 图片没有观看状态，Watched 保持 false。
		PersonalRating: img.PersonalRating,
	}, nil
}

func (s *ShortFeedService) maxDurationSeconds() float64 {
	if database.DB == nil {
		return defaultShortFeedMaxDurationSeconds
	}
	var settings models.Settings
	if err := database.DB.Select("short_feed_max_duration_minutes").First(&settings).Error; err != nil {
		return defaultShortFeedMaxDurationSeconds
	}
	if settings.ShortFeedMaxDurationMinutes <= 0 {
		return defaultShortFeedMaxDurationSeconds
	}
	return float64(settings.ShortFeedMaxDurationMinutes * 60)
}

func shortFeedEligible(video models.Video, maxDurationSeconds float64) bool {
	if maxDurationSeconds <= 0 {
		maxDurationSeconds = defaultShortFeedMaxDurationSeconds
	}
	return !video.IsStale && video.Duration > 0 && video.Duration < maxDurationSeconds
}

// loadProxiedVideoIDs 读出当前有 ready 代理行的视频集合（D-004）。
//
// 抽签阶段只看这一份集合，不 stat：与既有候选收集的口径一致——那里也刻意不做
// 文件存在性检查，抽中之后才 stat 那一条。指纹校验留给真正命中代理的时刻。
func loadProxiedVideoIDs() (map[uint]struct{}, error) {
	if database.DB == nil {
		return nil, nil
	}
	var ids []uint
	if err := database.DB.Model(&models.VideoPlaybackProxy{}).
		Where("status = ?", models.PlaybackProxyStatusReady).
		Pluck("video_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("读取播放代理集合失败: %w", err)
	}
	proxied := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		proxied[id] = struct{}{}
	}
	return proxied, nil
}

// resolveVideoProxy 取一条视频当前可用的代理；没有代理服务或没有有效代理时返回 nil。
// 白名单本身一个字都没改——代理是白名单之外的第二条路，而不是把那张表扩宽。
func (s *ShortFeedService) resolveVideoProxy(video models.Video, touch bool) *resolvedPlaybackProxy {
	proxies := s.videoService.playbackProxies()
	if proxies == nil {
		return nil
	}
	info, err := s.stat(video.Path)
	if err != nil || info.IsDir() {
		return nil
	}
	return proxies.resolveValidProxy(video.ID, playbackProxyFingerprintOf(info), touch)
}

// shortFeedInteractionState 是两种媒体互动的公共形态。两张并行表形状一致，
// 上层只跟它打交道，媒体类型的分派集中在下面这三个函数里，而不是散落在每个方法。
type shortFeedInteractionState struct {
	Liked                   bool
	Favorited               bool
	FavoriteSyncedToLibrary bool
	ViewCount               int
	LastViewedAt            *time.Time
	LikedAt                 *time.Time
	FavoritedAt             *time.Time
}

func stateFromVideoInteraction(row models.ShortFeedInteraction) shortFeedInteractionState {
	return shortFeedInteractionState{
		Liked: row.Liked, Favorited: row.Favorited, FavoriteSyncedToLibrary: row.FavoriteSyncedToLibrary,
		ViewCount: row.ViewCount, LastViewedAt: row.LastViewedAt, LikedAt: row.LikedAt, FavoritedAt: row.FavoritedAt,
	}
}

func stateFromImageInteraction(row models.ShortFeedImageInteraction) shortFeedInteractionState {
	return shortFeedInteractionState{
		Liked: row.Liked, Favorited: row.Favorited, FavoriteSyncedToLibrary: row.FavoriteSyncedToLibrary,
		ViewCount: row.ViewCount, LastViewedAt: row.LastViewedAt, LikedAt: row.LikedAt, FavoritedAt: row.FavoritedAt,
	}
}

func applyStateToImageInteraction(row *models.ShortFeedImageInteraction, state shortFeedInteractionState) {
	row.Liked = state.Liked
	row.Favorited = state.Favorited
	row.FavoriteSyncedToLibrary = state.FavoriteSyncedToLibrary
	row.ViewCount = state.ViewCount
	row.LastViewedAt = state.LastViewedAt
	row.LikedAt = state.LikedAt
	row.FavoritedAt = state.FavoritedAt
}

// loadShortFeedInteraction 读取互动状态；没有记录时返回零值而不是报错。
func loadShortFeedInteraction(ref ShortFeedMediaRef) (shortFeedInteractionState, error) {
	if ref.Kind == ShortFeedMediaImage {
		var row models.ShortFeedImageInteraction
		err := database.DB.Where("image_id = ?", ref.ID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return shortFeedInteractionState{}, nil
		}
		return stateFromImageInteraction(row), err
	}
	row, err := interactionForVideo(ref.ID)
	return stateFromVideoInteraction(row), err
}

// upsertShortFeedInteractionFor 按媒体类型落到对应的并行表；行锁语义与视频侧一致。
func upsertShortFeedInteractionFor(tx *gorm.DB, ref ShortFeedMediaRef, mutate func(*shortFeedInteractionState)) (shortFeedInteractionState, error) {
	var state shortFeedInteractionState
	if ref.Kind == ShortFeedMediaImage {
		var row models.ShortFeedImageInteraction
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("image_id = ?", ref.ID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = models.ShortFeedImageInteraction{ImageID: ref.ID}
		} else if err != nil {
			return state, err
		}
		state = stateFromImageInteraction(row)
		mutate(&state)
		applyStateToImageInteraction(&row, state)
		if row.ID == 0 {
			return state, tx.Create(&row).Error
		}
		return state, tx.Save(&row).Error
	}
	err := upsertShortFeedInteraction(tx, ref.ID, func(row *models.ShortFeedInteraction) {
		state = stateFromVideoInteraction(*row)
		mutate(&state)
		row.Liked = state.Liked
		row.Favorited = state.Favorited
		row.FavoriteSyncedToLibrary = state.FavoriteSyncedToLibrary
		row.ViewCount = state.ViewCount
		row.LastViewedAt = state.LastViewedAt
		row.LikedAt = state.LikedAt
		row.FavoritedAt = state.FavoritedAt
	})
	return state, err
}

func stateInteractionDTO(ref ShortFeedMediaRef, state shortFeedInteractionState) *ShortFeedInteractionDTO {
	return &ShortFeedInteractionDTO{
		MediaKind:    ref.Kind,
		MediaID:      ref.ID,
		Liked:        state.Liked,
		Favorited:    state.Favorited,
		ViewCount:    state.ViewCount,
		LastViewedAt: state.LastViewedAt,
		LikedAt:      state.LikedAt,
		FavoritedAt:  state.FavoritedAt,
	}
}

func interactionForVideo(videoID uint) (models.ShortFeedInteraction, error) {
	var interaction models.ShortFeedInteraction
	err := database.DB.Where("video_id = ?", videoID).First(&interaction).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.ShortFeedInteraction{VideoID: videoID}, nil
	}
	return interaction, err
}

func upsertShortFeedInteraction(tx *gorm.DB, videoID uint, mutate func(*models.ShortFeedInteraction)) error {
	var interaction models.ShortFeedInteraction
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("video_id = ?", videoID).First(&interaction).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		interaction = models.ShortFeedInteraction{VideoID: videoID}
	} else if err != nil {
		return err
	}

	mutate(&interaction)
	if interaction.ID == 0 {
		return tx.Create(&interaction).Error
	}
	return tx.Save(&interaction).Error
}

func incrementShortFeedTagPreference(tx *gorm.DB, tagID uint, delta float64) error {
	var tag models.Tag
	if err := tx.First(&tag, tagID).Error; err != nil {
		return err
	}
	if tag.AutomaticKind != "" {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tag_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"score":      gorm.Expr("short_feed_tag_preferences.score + ?", delta),
			"updated_at": time.Now(),
		}),
	}).Create(&models.ShortFeedTagPreference{TagID: tagID, Score: delta}).Error
}

func interactionDTO(interaction *models.ShortFeedInteraction) *ShortFeedInteractionDTO {
	return &ShortFeedInteractionDTO{
		MediaKind:    ShortFeedMediaVideo,
		MediaID:      interaction.VideoID,
		Liked:        interaction.Liked,
		Favorited:    interaction.Favorited,
		ViewCount:    interaction.ViewCount,
		LastViewedAt: interaction.LastViewedAt,
		LikedAt:      interaction.LikedAt,
		FavoritedAt:  interaction.FavoritedAt,
	}
}
