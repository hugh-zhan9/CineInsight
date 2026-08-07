package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"github.com/rwcarlsen/goexif/exif"
	"gorm.io/gorm"
)

const (
	// imageEXIFHeaderScanLimit 限定为提取 EXIF 而读入内存的文件头字节数。EXIF 位于
	// JPEG/TIFF/ISO-BMFF 的文件头部；goexif 的 tiff.Decode 会 ReadAll 传入的 reader，
	// 不加窗会把整个 RAW（可达数十 MB）读进内存。窗口外的 EXIF 视为不存在。
	imageEXIFHeaderScanLimit = 2 << 20

	// imageEXIFTextLimit 与 images 表相机/镜头列的 size:128 对齐。
	imageEXIFTextLimit = 128

	// imageEXIFExposureLimit 与 images.exposure_time 的 size:32 对齐。
	imageEXIFExposureLimit = 32

	// imageEXIFISOBMFFTIFFGap 是 ISO-BMFF 容器里 "Exif\x00\x00" 魔数与其后 TIFF 头之间
	// 允许的最大填充字节数。
	imageEXIFISOBMFFTIFFGap = 16

	// imageEXIFValueBudget 限制一个 TIFF blob 里全部 IFD 条目的外置值字节总量。
	// goexif 会为每个条目单独保留一份值拷贝，因此"上万个条目都指向同一大块数据"的
	// 构造能把 2 MiB 输入放大成数百 GB 内存并把进程打爆。预检超预算即当作没有 EXIF。
	imageEXIFValueBudget = 16 << 20

	// imageEXIFMaxDirectories 限制 IFD 链的遍历深度，避免构造出的环形/超长链拖住预检。
	imageEXIFMaxDirectories = 32
)

// exifMagic 是 ISO-BMFF（HEIC/HEIF/CR3）里 EXIF 数据块的定位魔数。
var exifMagic = []byte("Exif\x00\x00")

// tiffHeaders 是 TIFF blob 的两种字节序头，goexif 从这里开始解析。
var tiffHeaders = [][]byte{[]byte("II*\x00"), []byte("MM\x00*")}

// ImageEXIF 是一张图片的 EXIF 提取结果。零值表示该项缺失——解析失败与"文件本来就
// 没有 EXIF"在本类型上不做区分，两者都由调用方按"已解析但无 EXIF"落库。
type ImageEXIF struct {
	TakenAt         *time.Time `json:"taken_at,omitempty"`
	CameraMake      string     `json:"camera_make"`
	CameraModel     string     `json:"camera_model"`
	LensModel       string     `json:"lens_model"`
	ISO             int        `json:"iso"`
	FNumber         float64    `json:"f_number"`
	ExposureTime    string     `json:"exposure_time"`
	FocalLength     float64    `json:"focal_length"`
	ExifOrientation int        `json:"exif_orientation"`
	GPSLatitude     *float64   `json:"gps_latitude,omitempty"`
	GPSLongitude    *float64   `json:"gps_longitude,omitempty"`
}

// IsEmpty 报告是否一个 EXIF 字段都没提取到。
func (e ImageEXIF) IsEmpty() bool {
	return e.TakenAt == nil && e.CameraMake == "" && e.CameraModel == "" && e.LensModel == "" &&
		e.ISO == 0 && e.FNumber == 0 && e.ExposureTime == "" && e.FocalLength == 0 &&
		e.ExifOrientation == 0 && e.GPSLatitude == nil && e.GPSLongitude == nil
}

// imageEXIFSource 表示按格式分流出的 EXIF 提取方式。
type imageEXIFSource int

const (
	// imageEXIFSourceNone 表示该格式不在 EXIF 提取范围内（PNG/GIF/WebP 等）。
	imageEXIFSourceNone imageEXIFSource = iota
	// imageEXIFSourceDirect 表示 goexif 可直接解析：JPEG 与 TIFF 系 RAW。
	imageEXIFSourceDirect
	// imageEXIFSourceISOBMFF 表示 ISO-BMFF 容器，需先扫描 Exif 魔数定位 TIFF blob。
	imageEXIFSourceISOBMFF
)

// imageEXIFSourceForFormat 按小写扩展名（无点）分流提取方式；未收录格式一律不提取，
// 不做未约定的兜底解析。
func imageEXIFSourceForFormat(format string) imageEXIFSource {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), ".")) {
	case "jpg", "jpeg", "tif", "tiff", "dng", "cr2", "nef", "arw", "orf", "rw2":
		return imageEXIFSourceDirect
	case "heic", "heif", "cr3":
		return imageEXIFSourceISOBMFF
	default:
		return imageEXIFSourceNone
	}
}

// ParseImageEXIF 提取单个图片文件的 EXIF。返回 error 只表示文件读不出来（IO 故障）；
// 格式不支持、无 EXIF 段、EXIF 结构损坏都返回零值与 nil error——按设计这些都不是错误。
func ParseImageEXIF(path string) (ImageEXIF, error) {
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	source := imageEXIFSourceForFormat(format)
	if source == imageEXIFSourceNone {
		return ImageEXIF{}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return ImageEXIF{}, err
	}
	defer file.Close()
	head, err := io.ReadAll(io.LimitReader(file, imageEXIFHeaderScanLimit))
	if err != nil {
		return ImageEXIF{}, err
	}
	return parseImageEXIFFromHead(head, source), nil
}

// parseImageEXIFFromHead 在已读入的文件头字节上做提取，便于测试直接喂字节。
func parseImageEXIFFromHead(head []byte, source imageEXIFSource) ImageEXIF {
	blob, ok := locateEXIFTIFFBlob(head, source)
	if !ok {
		return ImageEXIF{}
	}
	if !exifBlobWithinValueBudget(blob) {
		return ImageEXIF{}
	}
	// goexif 对损坏的子 IFD 返回非致命错误但仍给出可用对象；只有返回 nil 才是真解析失败，
	// 而解析失败按设计等价于"没有 EXIF"。
	decoded := decodeEXIFBlob(blob)
	if decoded == nil {
		return ImageEXIF{}
	}
	return exifFieldsFrom(decoded)
}

// decodeEXIFBlob 调用 goexif 并把 panic 归一成"没有 EXIF"。goexif 停更于 2019 年，
// 而输入是用户目录里的任意文件；解析失败按设计本就不是错误，不该让一次后台扫描
// 把整个应用带崩。
func decodeEXIFBlob(blob []byte) (decoded *exif.Exif) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("[ImageEXIF] 解析器 panic，按无 EXIF 处理: %v", recovered)
			decoded = nil
		}
	}()
	decoded, _ = exif.Decode(bytes.NewReader(blob))
	return decoded
}

// exifTypeSizes 是 TIFF 字段类型到单元字节数的映射，未知类型按 0 处理。
var exifTypeSizes = [...]int{0, 1, 1, 2, 4, 8, 1, 1, 2, 4, 8, 4, 8}

// exifBlobWithinValueBudget 在交给 goexif 之前预走一遍 IFD 链，累计全部条目的外置值
// 字节量。超过 imageEXIFValueBudget（或结构本身走不通）就返回 false，让调用方按
// "没有 EXIF" 处理，从而把解析期的内存占用钉在预算之内。
func exifBlobWithinValueBudget(blob []byte) bool {
	if len(blob) < 8 {
		return false
	}
	var order binary.ByteOrder
	switch {
	case bytes.HasPrefix(blob, tiffHeaders[0]):
		order = binary.LittleEndian
	case bytes.HasPrefix(blob, tiffHeaders[1]):
		order = binary.BigEndian
	default:
		return false
	}

	pending := []int{int(order.Uint32(blob[4:8]))}
	visited := make(map[int]struct{}, imageEXIFMaxDirectories)
	total := 0
	for directories := 0; len(pending) > 0 && directories < imageEXIFMaxDirectories; directories++ {
		offset := pending[0]
		pending = pending[1:]
		if offset <= 0 || offset+2 > len(blob) {
			continue
		}
		if _, seen := visited[offset]; seen {
			continue
		}
		visited[offset] = struct{}{}

		count := int(order.Uint16(blob[offset : offset+2]))
		entriesEnd := offset + 2 + count*12
		if count < 0 || entriesEnd+4 > len(blob) {
			return false
		}
		for entry := 0; entry < count; entry++ {
			base := offset + 2 + entry*12
			tag := order.Uint16(blob[base : base+2])
			fieldType := int(order.Uint16(blob[base+2 : base+4]))
			valueCount := int(order.Uint32(blob[base+4 : base+8]))
			if fieldType <= 0 || fieldType >= len(exifTypeSizes) || valueCount < 0 {
				continue
			}
			valueBytes := exifTypeSizes[fieldType] * valueCount
			if valueBytes > 4 {
				total += valueBytes
				if total > imageEXIFValueBudget {
					log.Printf("[ImageEXIF] EXIF 值字节量超预算（%d > %d），按无 EXIF 处理", total, imageEXIFValueBudget)
					return false
				}
			}
			// 子 IFD 指针：Exif / GPS / Interoperability。
			if (tag == 0x8769 || tag == 0x8825 || tag == 0xA005) && valueCount == 1 && fieldType == 4 {
				pending = append(pending, int(order.Uint32(blob[base+8:base+12])))
			}
		}
		pending = append(pending, int(order.Uint32(blob[entriesEnd:entriesEnd+4])))
	}
	return true
}

// locateEXIFTIFFBlob 把各格式的文件头收敛成一段 TIFF blob，交给同一个解析器。
func locateEXIFTIFFBlob(head []byte, source imageEXIFSource) ([]byte, bool) {
	switch source {
	case imageEXIFSourceISOBMFF:
		return locateISOBMFFTIFFBlob(head)
	case imageEXIFSourceDirect:
		// TIFF 系 RAW 的文件头本身就是 TIFF blob。
		if hasTIFFHeader(head) {
			return head, true
		}
		return locateJPEGEXIFTIFFBlob(head)
	default:
		return nil, false
	}
}

func hasTIFFHeader(data []byte) bool {
	for _, header := range tiffHeaders {
		if bytes.HasPrefix(data, header) {
			return true
		}
	}
	return false
}

// locateISOBMFFTIFFBlob 在 ISO-BMFF（HEIC/HEIF/CR3）文件头里定位 "Exif\x00\x00" 魔数，
// 返回其后紧跟的 TIFF blob。
func locateISOBMFFTIFFBlob(head []byte) ([]byte, bool) {
	index := bytes.Index(head, exifMagic)
	if index < 0 {
		return nil, false
	}
	return tiffBlobAfterExifMagic(head[index+len(exifMagic):])
}

// tiffBlobAfterExifMagic 从 "Exif\x00\x00" 之后的字节里找出 TIFF 头。容器规范允许两者
// 之间有少量填充，故在有限窗口内继续寻找字节序头。
func tiffBlobAfterExifMagic(rest []byte) ([]byte, bool) {
	limit := imageEXIFISOBMFFTIFFGap
	if limit > len(rest) {
		limit = len(rest)
	}
	for offset := 0; offset <= limit; offset++ {
		if hasTIFFHeader(rest[offset:]) {
			return rest[offset:], true
		}
	}
	return nil, false
}

// locateJPEGEXIFTIFFBlob 按 JPEG 段结构定位第一个 EXIF APP1 并返回其 TIFF blob。
// 走段结构而不是全文搜魔数，避免把图像数据里的巧合字节当成 EXIF。
func locateJPEGEXIFTIFFBlob(data []byte) ([]byte, bool) {
	if len(data) < 2 || data[0] != jpegMarkerPrefix || data[1] != jpegMarkerSOI {
		return nil, false
	}
	index := 2
	for index < len(data) {
		if data[index] != jpegMarkerPrefix {
			return nil, false
		}
		markerIndex := index
		for markerIndex < len(data) && data[markerIndex] == jpegMarkerPrefix {
			markerIndex++
		}
		if markerIndex >= len(data) {
			return nil, false
		}
		marker := data[markerIndex]
		// EXIF APP1 必须出现在扫描数据之前；到 SOS/EOI 仍未命中即判定无 EXIF。
		if marker == jpegMarkerSOS || marker == jpegMarkerEOI {
			return nil, false
		}
		if marker == jpegMarkerTEM || (marker >= jpegMarkerRST0 && marker <= jpegMarkerRST7) {
			index = markerIndex + 1
			continue
		}
		if markerIndex+3 > len(data) {
			return nil, false
		}
		length := int(binary.BigEndian.Uint16(data[markerIndex+1 : markerIndex+3]))
		segmentEnd := markerIndex + 1 + length
		if length < 2 || segmentEnd > len(data) {
			return nil, false
		}
		payload := data[markerIndex+3 : segmentEnd]
		if marker == jpegMarkerAPP1 && bytes.HasPrefix(payload, exifMagic) {
			return tiffBlobAfterExifMagic(payload[len(exifMagic):])
		}
		index = segmentEnd
	}
	return nil, false
}

// exifFieldsFrom 把 goexif 解出的标签映射到 ImageEXIF；缺失标签一律留零值。
func exifFieldsFrom(decoded *exif.Exif) ImageEXIF {
	data := ImageEXIF{
		CameraMake:      exifTrimmedString(decoded, exif.Make, imageEXIFTextLimit),
		CameraModel:     exifTrimmedString(decoded, exif.Model, imageEXIFTextLimit),
		LensModel:       exifTrimmedString(decoded, exif.LensModel, imageEXIFTextLimit),
		ISO:             exifInt(decoded, exif.ISOSpeedRatings),
		FNumber:         exifFloat(decoded, exif.FNumber),
		ExposureTime:    exifFraction(decoded, exif.ExposureTime),
		FocalLength:     exifFloat(decoded, exif.FocalLength),
		ExifOrientation: exifInt(decoded, exif.Orientation),
	}
	if taken, err := decoded.DateTime(); err == nil && !taken.IsZero() {
		data.TakenAt = &taken
	}
	if latitude, longitude, err := decoded.LatLong(); err == nil && validGPSCoordinate(latitude, longitude) {
		data.GPSLatitude = &latitude
		data.GPSLongitude = &longitude
	}
	return data
}

// validGPSCoordinate 拒绝越界或非有限的坐标，避免把损坏的 GPS IFD 当成有效定位落库。
func validGPSCoordinate(latitude, longitude float64) bool {
	if math.IsNaN(latitude) || math.IsNaN(longitude) || math.IsInf(latitude, 0) || math.IsInf(longitude, 0) {
		return false
	}
	return latitude >= -90 && latitude <= 90 && longitude >= -180 && longitude <= 180
}

func exifTrimmedString(decoded *exif.Exif, name exif.FieldName, limit int) string {
	tag, err := decoded.Get(name)
	if err != nil {
		return ""
	}
	value, err := tag.StringVal()
	if err != nil {
		return ""
	}
	return truncateRunes(strings.TrimSpace(strings.TrimRight(value, "\x00")), limit)
}

func exifInt(decoded *exif.Exif, name exif.FieldName) int {
	tag, err := decoded.Get(name)
	if err != nil {
		return 0
	}
	value, err := tag.Int(0)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func exifFloat(decoded *exif.Exif, name exif.FieldName) float64 {
	numerator, denominator, ok := exifRational(decoded, name)
	if !ok || denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

// exifFraction 保留快门速度的原始分数文本（如 1/250），不换算成小数。
func exifFraction(decoded *exif.Exif, name exif.FieldName) string {
	numerator, denominator, ok := exifRational(decoded, name)
	if !ok || denominator == 0 {
		return ""
	}
	if denominator == 1 {
		return truncateRunes(fmt.Sprintf("%d", numerator), imageEXIFExposureLimit)
	}
	return truncateRunes(fmt.Sprintf("%d/%d", numerator, denominator), imageEXIFExposureLimit)
}

func exifRational(decoded *exif.Exif, name exif.FieldName) (int64, int64, bool) {
	tag, err := decoded.Get(name)
	if err != nil {
		return 0, 0, false
	}
	numerator, denominator, err := tag.Rat2(0)
	if err != nil {
		return 0, 0, false
	}
	return numerator, denominator, true
}

// imageEXIFColumnUpdates 把提取结果展开成只覆盖 EXIF 列的 Updates 映射；无论是否解析到
// 内容都写 exif_parsed_at，让"已解析但无 EXIF"与"未解析"可区分。
func imageEXIFColumnUpdates(data ImageEXIF, parsedAt time.Time) map[string]interface{} {
	return map[string]interface{}{
		"taken_at":         data.TakenAt,
		"camera_make":      data.CameraMake,
		"camera_model":     data.CameraModel,
		"lens_model":       data.LensModel,
		"iso":              data.ISO,
		"f_number":         data.FNumber,
		"exposure_time":    data.ExposureTime,
		"focal_length":     data.FocalLength,
		"exif_orientation": data.ExifOrientation,
		"gps_latitude":     data.GPSLatitude,
		"gps_longitude":    data.GPSLongitude,
		"exif_parsed_at":   parsedAt,
	}
}

// persistImageEXIF 用限定列的 Updates 写回 EXIF，绝不触碰其他列。
func persistImageEXIF(db *gorm.DB, imageID uint, data ImageEXIF, parsedAt time.Time) error {
	return db.Model(&models.Image{}).Where("id = ?", imageID).
		Updates(imageEXIFColumnUpdates(data, parsedAt)).Error
}

// refreshImageEXIF 解析单张图片并落库，返回本次是否提取到 EXIF 内容。
// 文件读不出来时返回 error 且不写 exif_parsed_at，留给补全任务下次重试。
func refreshImageEXIF(db *gorm.DB, imageID uint, path string, now func() time.Time) (bool, error) {
	data, err := ParseImageEXIF(path)
	if err != nil {
		return false, err
	}
	if err := persistImageEXIF(db, imageID, data, now()); err != nil {
		return false, err
	}
	return !data.IsEmpty(), nil
}

// ===== EXIF 补全任务（双轨回填的显式后台一轨，镜像既有长任务三件套形态） =====

const (
	imageEXIFBackfillMaxFailures = 50
	imageEXIFBackfillErrorLimit  = 500
	imageEXIFBackfillBatchSize   = 500
)

// ErrImageEXIFBackfillBusy 表示补全任务已在运行，拒绝并发启动。
var ErrImageEXIFBackfillBusy = errors.New("EXIF 补全任务运行中")

// ImageEXIFBackfillFailure 是状态面板可见的单图失败留痕（有界列表）。
type ImageEXIFBackfillFailure struct {
	ImageID uint   `json:"image_id"`
	Name    string `json:"name"`
	Error   string `json:"error"`
}

// ImageEXIFBackfillStatus 形态镜像 ImageAIDescriptionStatus，供进度事件与状态查询共用。
// Succeeded=解析到 EXIF；Skipped=已标记解析但文件无 EXIF；Failed=文件读写失败。
type ImageEXIFBackfillStatus struct {
	Running        bool                       `json:"running"`
	Cancelled      bool                       `json:"cancelled"`
	Completed      bool                       `json:"completed"`
	Total          int                        `json:"total"`
	Processed      int                        `json:"processed"`
	Succeeded      int                        `json:"succeeded"`
	Skipped        int                        `json:"skipped"`
	Failed         int                        `json:"failed"`
	CurrentImageID uint                       `json:"current_image_id"`
	StartedAt      *time.Time                 `json:"started_at,omitempty" ts_type:"string"`
	UpdatedAt      *time.Time                 `json:"updated_at,omitempty" ts_type:"string"`
	Failures       []ImageEXIFBackfillFailure `json:"failures"`
}

// ImageEXIFBackfillService 为历史图片补全 EXIF：目标集 = exif_parsed_at IS NULL 的活跃图片。
// 与扫描入库时的即时解析组成双轨回填（沿用 dHash 的双轨模式）。
type ImageEXIFBackfillService struct {
	now func() time.Time

	mu       sync.Mutex
	stopMu   sync.Mutex
	status   ImageEXIFBackfillStatus
	cancel   context.CancelFunc
	worker   sync.WaitGroup
	emitter  func(ImageEXIFBackfillStatus)
	stopping bool
}

// NewImageEXIFBackfillService 创建 EXIF 补全服务（单 worker、显式启动）。
func NewImageEXIFBackfillService() *ImageEXIFBackfillService {
	return &ImageEXIFBackfillService{
		now:    time.Now,
		status: ImageEXIFBackfillStatus{Failures: []ImageEXIFBackfillFailure{}},
	}
}

// SetEventEmitter 注入进度事件回调（app 层接 Wails 事件 image-exif-backfill-progress）。
func (s *ImageEXIFBackfillService) SetEventEmitter(emitter func(ImageEXIFBackfillStatus)) {
	s.mu.Lock()
	s.emitter = emitter
	s.mu.Unlock()
}

// StartImageEXIFBackfill 启动补全任务；运行中重复启动返回当前状态，不重复起 worker。
func (s *ImageEXIFBackfillService) StartImageEXIFBackfill(parent context.Context) (ImageEXIFBackfillStatus, error) {
	if parent == nil {
		parent = context.Background()
	}
	if database.DB == nil {
		return ImageEXIFBackfillStatus{}, errors.New("数据库未初始化")
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return ImageEXIFBackfillStatus{}, errors.New("EXIF 补全任务正在停止")
	}
	if s.status.Running {
		status := cloneImageEXIFBackfillStatus(s.status)
		s.mu.Unlock()
		return status, ErrImageEXIFBackfillBusy
	}
	now := s.now()
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.status = ImageEXIFBackfillStatus{
		Running: true, StartedAt: &now, UpdatedAt: &now,
		Failures: []ImageEXIFBackfillFailure{},
	}
	status, emitter := cloneImageEXIFBackfillStatus(s.status), s.emitter
	s.worker.Add(1)
	s.mu.Unlock()
	emitImageEXIFBackfillStatus(emitter, status)
	go s.run(ctx)
	return status, nil
}

// GetImageEXIFBackfillStatus 返回当前任务状态快照。
func (s *ImageEXIFBackfillService) GetImageEXIFBackfillStatus() ImageEXIFBackfillStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneImageEXIFBackfillStatus(s.status)
}

// CancelImageEXIFBackfill 取消运行中的补全任务。
func (s *ImageEXIFBackfillService) CancelImageEXIFBackfill() error {
	s.mu.Lock()
	cancel, running := s.cancel, s.status.Running
	s.mu.Unlock()
	if !running || cancel == nil {
		return errors.New("EXIF 补全任务未运行")
	}
	cancel()
	return nil
}

// StopAndWait 供 shutdown 与数据库恢复：取消在途任务并等待 worker 退出。
func (s *ImageEXIFBackfillService) StopAndWait() {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	s.mu.Lock()
	s.stopping = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.worker.Wait()
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
}

// run 是单 worker 主循环：按 id 递增分批取未解析图片，解析后逐张写库。
// 分批而非一次性快照，是因为每张成功后 exif_parsed_at 即被填上，游标只需向前推进 id。
func (s *ImageEXIFBackfillService) run(ctx context.Context) {
	defer s.worker.Done()
	total, err := s.countPending(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.update(func(status *ImageEXIFBackfillStatus) {
				status.Failed++
				appendImageEXIFBackfillFailure(status, ImageEXIFBackfillFailure{Error: boundedError(err, imageEXIFBackfillErrorLimit)})
			})
		}
		s.finish(ctx.Err() != nil, ctx.Err() == nil)
		return
	}
	s.update(func(status *ImageEXIFBackfillStatus) { status.Total = total })

	// aborted 记录"因查询失败提前退出"，与正常跑完区分开：中断的任务不能报"已完成"。
	aborted := false
	var afterID uint
	for ctx.Err() == nil {
		batch, err := s.loadPendingBatch(ctx, afterID)
		if err != nil {
			if ctx.Err() == nil {
				aborted = true
				s.update(func(status *ImageEXIFBackfillStatus) {
					status.Failed++
					appendImageEXIFBackfillFailure(status, ImageEXIFBackfillFailure{Error: boundedError(err, imageEXIFBackfillErrorLimit)})
				})
			}
			break
		}
		if len(batch) == 0 {
			break
		}
		for _, image := range batch {
			if ctx.Err() != nil {
				break
			}
			afterID = image.ID
			s.update(func(status *ImageEXIFBackfillStatus) { status.CurrentImageID = image.ID })
			found, err := refreshImageEXIF(database.DB.WithContext(ctx), image.ID, image.Path, s.now)
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				s.update(func(status *ImageEXIFBackfillStatus) {
					status.Processed++
					status.Failed++
					appendImageEXIFBackfillFailure(status, ImageEXIFBackfillFailure{
						ImageID: image.ID, Name: image.Name, Error: boundedError(err, imageEXIFBackfillErrorLimit),
					})
				})
				continue
			}
			s.update(func(status *ImageEXIFBackfillStatus) {
				status.Processed++
				if found {
					status.Succeeded++
					return
				}
				status.Skipped++
			})
		}
	}
	s.finish(ctx.Err() != nil, !aborted)
}

func (s *ImageEXIFBackfillService) countPending(ctx context.Context) (int, error) {
	var count int64
	if err := database.DB.WithContext(ctx).Model(&models.Image{}).
		Where("exif_parsed_at IS NULL").Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// loadPendingBatch 取 id > afterID 且尚未解析的活跃图片。失败图片的 exif_parsed_at 仍为
// NULL，用 afterID 前进游标保证不会在同一张上死循环。
func (s *ImageEXIFBackfillService) loadPendingBatch(ctx context.Context, afterID uint) ([]models.Image, error) {
	var batch []models.Image
	err := database.DB.WithContext(ctx).Model(&models.Image{}).
		Select("id", "name", "path").
		Where("exif_parsed_at IS NULL AND id > ?", afterID).
		Order("id ASC").
		Limit(imageEXIFBackfillBatchSize).
		Find(&batch).Error
	return batch, err
}

func (s *ImageEXIFBackfillService) update(update func(*ImageEXIFBackfillStatus)) {
	// 时钟在锁外取值：注入的 now 不应在持锁期间执行任意代码。
	now := s.now()
	s.mu.Lock()
	update(&s.status)
	s.status.UpdatedAt = &now
	status, emitter := cloneImageEXIFBackfillStatus(s.status), s.emitter
	s.mu.Unlock()
	emitImageEXIFBackfillStatus(emitter, status)
}

// finish 结束任务：cancelled 表示被 context 取消，completed 表示整轮目标集确实跑完了。
// 因查询失败提前退出时两者皆为 false——既没取消也没跑完，状态面板不该显示"已完成"。
func (s *ImageEXIFBackfillService) finish(cancelled, completed bool) {
	now := s.now()
	s.mu.Lock()
	s.status.Running = false
	s.status.Cancelled = cancelled
	s.status.Completed = !cancelled && completed
	s.status.CurrentImageID = 0
	s.status.UpdatedAt = &now
	s.cancel = nil
	status, emitter := cloneImageEXIFBackfillStatus(s.status), s.emitter
	s.mu.Unlock()
	emitImageEXIFBackfillStatus(emitter, status)
	log.Printf("[ImageEXIF] 补全任务结束 cancelled=%v processed=%d succeeded=%d skipped=%d failed=%d",
		cancelled, status.Processed, status.Succeeded, status.Skipped, status.Failed)
}

func appendImageEXIFBackfillFailure(status *ImageEXIFBackfillStatus, failure ImageEXIFBackfillFailure) {
	if len(status.Failures) < imageEXIFBackfillMaxFailures {
		status.Failures = append(status.Failures, failure)
	}
}

func cloneImageEXIFBackfillStatus(status ImageEXIFBackfillStatus) ImageEXIFBackfillStatus {
	status.Failures = append([]ImageEXIFBackfillFailure(nil), status.Failures...)
	return status
}

func emitImageEXIFBackfillStatus(emitter func(ImageEXIFBackfillStatus), status ImageEXIFBackfillStatus) {
	if emitter != nil {
		emitter(status)
	}
}

// ===== 外发剥除（AC-14） =====

// JPEG 标记。
const (
	jpegMarkerPrefix = 0xFF
	jpegMarkerSOI    = 0xD8
	jpegMarkerEOI    = 0xD9
	jpegMarkerSOS    = 0xDA
	jpegMarkerTEM    = 0x01
	jpegMarkerRST0   = 0xD0
	jpegMarkerRST7   = 0xD7
	jpegMarkerAPP0   = 0xE0
	jpegMarkerAPP1   = 0xE1
	jpegMarkerAPP2   = 0xE2
	jpegMarkerAPP14  = 0xEE
	jpegMarkerAPP15  = 0xEF
)

// ErrNotJPEG 表示待外发的字节不是可解析的 JPEG，因而无法确认元数据已被剥除。
var ErrNotJPEG = errors.New("待外发数据不是 JPEG")

// ErrJPEGMetadataResidue 表示剥除后仍在字节里检出元数据特征，属于剥除逻辑本身失效。
var ErrJPEGMetadataResidue = errors.New("剥除后仍检出元数据残留")

// xmpNamespaceMagic 是 APP1 里 XMP 包的命名空间前缀；XMP 同样能携带 GPS。
var xmpNamespaceMagic = []byte("http://ns.adobe.com/xap/")

// jpegKeptAPPSegments 是允许透传的 APPn 段白名单：键为标记，值为该段必须携带的
// 标识前缀。只有渲染必需且不承载拍摄元数据的段在列——JFIF/JFXX 是基础密度信息，
// ICC_PROFILE 是色彩管理，Adobe 决定 CMYK/YCCK 的解释方式。其余 APPn（EXIF、XMP、
// MPF、Ricoh RMETA、JUMBF/C2PA、Ducky……）一律剥除：它们要么直接带 GPS 与设备身份，
// 要么带能引出更多元数据的二级图像索引。
var jpegKeptAPPSegments = map[byte][][]byte{
	jpegMarkerAPP0:  {[]byte("JFIF\x00"), []byte("JFXX\x00")},
	jpegMarkerAPP2:  {[]byte("ICC_PROFILE\x00")},
	jpegMarkerAPP14: {[]byte("Adobe")},
}

// StripJPEGMetadataForUpload 无损剥除 JPEG 里承载元数据的段，只保留图像本体与
// jpegKeptAPPSegments 白名单里的渲染必需段。实测结论：darwin 的 sips 转码会原样保留
// 源图的 EXIF APP1（含 GPS）并额外写入 APP13 Photoshop IRB，ffmpeg 的 mjpeg 编码器不写
// EXIF；缩略图两条产线都可能成为外发输入，故在送出前统一剥除。
//
// 多帧 JPEG（MPF/动态照片在 EOI 之后追加第二张完整图像）会被继续按段解析，不做
// "SOS 之后原样透传"的偷懒处理——那正是元数据能绕过剥除的口子。
//
// 无法按 JPEG 结构完整解析（含缺少 EOI）时返回 ErrNotJPEG——外发前确认不了就不发，
// 不做降级。
func StripJPEGMetadataForUpload(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != jpegMarkerPrefix || data[1] != jpegMarkerSOI {
		return nil, ErrNotJPEG
	}
	out := make([]byte, 0, len(data))
	out = append(out, data[0], data[1])

	sawEOI := false
	index := 2
	for index < len(data) {
		if data[index] != jpegMarkerPrefix {
			return nil, fmt.Errorf("%w: 偏移 %d 处缺少段标记", ErrNotJPEG, index)
		}
		// 标记前允许任意数量的 0xFF 填充字节，原样保留。
		markerIndex := index
		for markerIndex < len(data) && data[markerIndex] == jpegMarkerPrefix {
			markerIndex++
		}
		if markerIndex >= len(data) {
			return nil, fmt.Errorf("%w: 偏移 %d 处的段标记被截断", ErrNotJPEG, index)
		}
		marker := data[markerIndex]

		// 无长度字段的独立标记：SOI（多帧的下一帧）、EOI、TEM、RSTn。
		if marker == jpegMarkerSOI || marker == jpegMarkerEOI || marker == jpegMarkerTEM ||
			(marker >= jpegMarkerRST0 && marker <= jpegMarkerRST7) {
			out = append(out, data[index:markerIndex+1]...)
			index = markerIndex + 1
			sawEOI = sawEOI || marker == jpegMarkerEOI
			continue
		}

		if markerIndex+3 > len(data) {
			return nil, fmt.Errorf("%w: 偏移 %d 处的段长度被截断", ErrNotJPEG, markerIndex)
		}
		length := int(binary.BigEndian.Uint16(data[markerIndex+1 : markerIndex+3]))
		if length < 2 || markerIndex+1+length > len(data) {
			return nil, fmt.Errorf("%w: 偏移 %d 处的段长度 %d 非法", ErrNotJPEG, markerIndex, length)
		}
		segmentEnd := markerIndex + 1 + length

		if marker == jpegMarkerSOS {
			// SOS 头之后是熵编码数据，其中的 0xFF 以 0xFF00 转义、RSTn 是合法内嵌标记；
			// 扫到下一个真标记为止，中间字节原样保留。
			scanEnd, err := jpegScanDataEnd(data, segmentEnd)
			if err != nil {
				return nil, err
			}
			out = append(out, data[index:scanEnd]...)
			index = scanEnd
			continue
		}
		if jpegSegmentIsMetadata(marker, data[markerIndex+3:segmentEnd]) {
			index = segmentEnd
			continue
		}
		out = append(out, data[index:segmentEnd]...)
		index = segmentEnd
	}
	if !sawEOI {
		return nil, fmt.Errorf("%w: 缺少 EOI，无法确认已解析完整个文件", ErrNotJPEG)
	}
	// 后置校验：剥除逻辑失效时宁可报错也不外发。
	if bytes.Contains(out, exifMagic) || bytes.Contains(out, xmpNamespaceMagic) {
		return nil, ErrJPEGMetadataResidue
	}
	return out, nil
}

// jpegSegmentIsMetadata 判定一个带长度的段是否应被剥除：所有 APPn 与 COM 默认剥除，
// 只有命中白名单标识前缀的才透传；SOF/DHT/DQT/DRI 等结构段不在此判定内。
func jpegSegmentIsMetadata(marker byte, payload []byte) bool {
	if marker == 0xFE { // COM：注释段可携带任意用户文本
		return true
	}
	if marker < jpegMarkerAPP0 || marker > jpegMarkerAPP15 {
		return false
	}
	for _, prefix := range jpegKeptAPPSegments[marker] {
		if bytes.HasPrefix(payload, prefix) {
			return false
		}
	}
	return true
}

// jpegScanDataEnd 返回 SOS 熵编码数据的结束偏移（即下一个真标记的 0xFF 位置）。
func jpegScanDataEnd(data []byte, start int) (int, error) {
	for index := start; index < len(data); index++ {
		if data[index] != jpegMarkerPrefix {
			continue
		}
		next := index + 1
		for next < len(data) && data[next] == jpegMarkerPrefix {
			next++ // 0xFF 填充
		}
		if next >= len(data) {
			return 0, fmt.Errorf("%w: 扫描数据在偏移 %d 处被截断", ErrNotJPEG, index)
		}
		marker := data[next]
		// 0xFF00 是转义的数据字节，RSTn 是内嵌重启标记，两者都属于扫描数据。
		if marker == 0x00 || (marker >= jpegMarkerRST0 && marker <= jpegMarkerRST7) {
			index = next
			continue
		}
		return index, nil
	}
	return 0, fmt.Errorf("%w: 扫描数据之后没有标记", ErrNotJPEG)
}
