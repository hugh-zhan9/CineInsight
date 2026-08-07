package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"github.com/rwcarlsen/goexif/exif"
)

// ===== EXIF 夹具构造：手写 TIFF/IFD，避免依赖外部工具或二进制夹具文件 =====

type exifTestEntry struct {
	tag       uint16
	dataType  uint16
	count     uint32
	inline    [4]byte
	external  []byte
	subIFDRef string // "exif" / "gps"，写 IFD 时替换为实际偏移
}

func exifTestASCII(tag uint16, value string) exifTestEntry {
	payload := append([]byte(value), 0)
	entry := exifTestEntry{tag: tag, dataType: 2, count: uint32(len(payload))}
	if len(payload) <= 4 {
		copy(entry.inline[:], payload)
	} else {
		entry.external = payload
	}
	return entry
}

func exifTestShort(tag uint16, value uint16) exifTestEntry {
	entry := exifTestEntry{tag: tag, dataType: 3, count: 1}
	binary.LittleEndian.PutUint16(entry.inline[0:2], value)
	return entry
}

func exifTestRational(tag uint16, values ...[2]uint32) exifTestEntry {
	payload := make([]byte, 0, len(values)*8)
	for _, value := range values {
		payload = binary.LittleEndian.AppendUint32(payload, value[0])
		payload = binary.LittleEndian.AppendUint32(payload, value[1])
	}
	return exifTestEntry{tag: tag, dataType: 5, count: uint32(len(values)), external: payload}
}

func exifTestPointer(tag uint16, target string) exifTestEntry {
	return exifTestEntry{tag: tag, dataType: 4, count: 1, subIFDRef: target}
}

// buildTestTIFF 组装小端 TIFF：header | IFD0 | Exif SubIFD | GPS IFD | 外置数据。
func buildTestTIFF(ifd0, subIFD, gpsIFD []exifTestEntry) []byte {
	directorySize := func(entries []exifTestEntry) int { return 2 + 12*len(entries) + 4 }
	offsetIFD0 := 8
	offsetSubIFD := offsetIFD0 + directorySize(ifd0)
	offsetGPSIFD := offsetSubIFD + directorySize(subIFD)
	dataStart := offsetGPSIFD + directorySize(gpsIFD)

	out := make([]byte, dataStart)
	copy(out, []byte{'I', 'I', 0x2a, 0x00})
	binary.LittleEndian.PutUint32(out[4:8], uint32(offsetIFD0))

	dataOffset := dataStart
	writeDirectory := func(base int, entries []exifTestEntry) {
		binary.LittleEndian.PutUint16(out[base:], uint16(len(entries)))
		cursor := base + 2
		for _, entry := range entries {
			binary.LittleEndian.PutUint16(out[cursor:], entry.tag)
			binary.LittleEndian.PutUint16(out[cursor+2:], entry.dataType)
			binary.LittleEndian.PutUint32(out[cursor+4:], entry.count)
			switch {
			case entry.subIFDRef == "exif":
				binary.LittleEndian.PutUint32(out[cursor+8:], uint32(offsetSubIFD))
			case entry.subIFDRef == "gps":
				binary.LittleEndian.PutUint32(out[cursor+8:], uint32(offsetGPSIFD))
			case entry.external != nil:
				binary.LittleEndian.PutUint32(out[cursor+8:], uint32(dataOffset))
				out = append(out, entry.external...)
				dataOffset += len(entry.external)
			default:
				copy(out[cursor+8:cursor+12], entry.inline[:])
			}
			cursor += 12
		}
		binary.LittleEndian.PutUint32(out[cursor:], 0)
	}
	writeDirectory(offsetIFD0, ifd0)
	writeDirectory(offsetSubIFD, subIFD)
	writeDirectory(offsetGPSIFD, gpsIFD)
	return out
}

// 夹具的期望取值，测试断言与构造共用一份常量。
const (
	exifFixtureMake         = "CineCam"
	exifFixtureModel        = "CI-900"
	exifFixtureLens         = "CineLens 35mm F2.8"
	exifFixtureExposure     = "1/250"
	exifFixtureDateTime     = "2024:03:11 08:09:10"
	exifFixtureISO          = 400
	exifFixtureOrientation  = 6
	exifFixtureFNumber      = 2.8
	exifFixtureFocalLength  = 35.0
	exifFixtureLatitude     = 31.233333333333334
	exifFixtureLongitude    = 121.46666666666667
	exifFixtureLongitudeDeg = 121
)

// exifFixtureTakenAt 与 goexif 的解释一致：EXIF 的 DateTimeOriginal 不带时区，
// 按本地时区当作墙钟时间解析。
func exifFixtureTakenAt() time.Time {
	return time.Date(2024, 3, 11, 8, 9, 10, 0, time.Local)
}

// exifFixtureTIFF 是一份含相机参数、拍摄时间与 GPS 的完整 EXIF TIFF blob。
func exifFixtureTIFF() []byte {
	return buildTestTIFF(
		[]exifTestEntry{
			exifTestASCII(0x010f, exifFixtureMake),
			exifTestASCII(0x0110, exifFixtureModel),
			exifTestShort(0x0112, exifFixtureOrientation),
			exifTestASCII(0x0132, exifFixtureDateTime),
			exifTestPointer(0x8769, "exif"),
			exifTestPointer(0x8825, "gps"),
		},
		[]exifTestEntry{
			exifTestRational(0x829a, [2]uint32{1, 250}),
			exifTestRational(0x829d, [2]uint32{28, 10}),
			exifTestShort(0x8827, exifFixtureISO),
			exifTestASCII(0x9003, exifFixtureDateTime),
			exifTestRational(0x920a, [2]uint32{35, 1}),
			exifTestASCII(0xa434, exifFixtureLens),
		},
		[]exifTestEntry{
			exifTestASCII(0x0001, "N"),
			exifTestRational(0x0002, [2]uint32{31, 1}, [2]uint32{14, 1}, [2]uint32{0, 1}),
			exifTestASCII(0x0003, "E"),
			exifTestRational(0x0004, [2]uint32{121, 1}, [2]uint32{28, 1}, [2]uint32{0, 1}),
		},
	)
}

// exifTestBaseJPEG 是一张真实可解码的小 JPEG，作为承载 EXIF 的图像本体。
func exifTestBaseJPEG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 5), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, nil); err != nil {
		t.Fatalf("编码基础 JPEG 失败: %v", err)
	}
	return buf.Bytes()
}

// exifFixtureJPEG 在 SOI 之后插入含 EXIF+GPS 的 APP1 段。
func exifFixtureJPEG(t *testing.T) []byte {
	t.Helper()
	base := exifTestBaseJPEG(t)
	payload := append(append([]byte{}, exifMagic...), exifFixtureTIFF()...)
	segment := []byte{0xFF, 0xE1}
	segment = binary.BigEndian.AppendUint16(segment, uint16(len(payload)+2))
	segment = append(segment, payload...)

	out := make([]byte, 0, len(base)+len(segment))
	out = append(out, base[:2]...)
	out = append(out, segment...)
	out = append(out, base[2:]...)
	return out
}

// exifFixtureISOBMFF 模拟 HEIC/CR3 这类 ISO-BMFF 容器：box 头 + Exif 魔数 + TIFF blob。
func exifFixtureISOBMFF() []byte {
	container := []byte{0x00, 0x00, 0x00, 0x18}
	container = append(container, []byte("ftypheic")...)
	container = append(container, make([]byte, 16)...)
	container = append(container, []byte("mdat")...)
	// 规范里 ExifDataBlock 前有 4 字节 TIFF 头偏移，这里一并模拟。
	container = append(container, 0x00, 0x00, 0x00, 0x00)
	container = append(container, exifMagic...)
	container = append(container, exifFixtureTIFF()...)
	container = append(container, []byte("trailing-media-bytes")...)
	return container
}

func writeTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("写夹具文件失败: %v", err)
	}
	return path
}

func assertFixtureEXIF(t *testing.T, data ImageEXIF) {
	t.Helper()
	if data.TakenAt == nil || !data.TakenAt.Equal(exifFixtureTakenAt()) {
		t.Fatalf("拍摄时间不符: %v", data.TakenAt)
	}
	if data.CameraMake != exifFixtureMake || data.CameraModel != exifFixtureModel {
		t.Fatalf("相机字段不符: %q / %q", data.CameraMake, data.CameraModel)
	}
	if data.LensModel != exifFixtureLens {
		t.Fatalf("镜头字段不符: %q", data.LensModel)
	}
	if data.ISO != exifFixtureISO {
		t.Fatalf("ISO 不符: %d", data.ISO)
	}
	if data.FNumber != exifFixtureFNumber {
		t.Fatalf("光圈不符: %v", data.FNumber)
	}
	if data.ExposureTime != exifFixtureExposure {
		t.Fatalf("快门原始分数文本不符: %q", data.ExposureTime)
	}
	if data.FocalLength != exifFixtureFocalLength {
		t.Fatalf("焦距不符: %v", data.FocalLength)
	}
	if data.ExifOrientation != exifFixtureOrientation {
		t.Fatalf("方向不符: %d", data.ExifOrientation)
	}
	if data.GPSLatitude == nil || data.GPSLongitude == nil {
		t.Fatal("GPS 未解析出来")
	}
	if *data.GPSLatitude != exifFixtureLatitude || *data.GPSLongitude != exifFixtureLongitude {
		t.Fatalf("GPS 坐标不符: %v, %v", *data.GPSLatitude, *data.GPSLongitude)
	}
}

// ===== 解析：按格式分流 =====

func TestImageExifParseJPEGExtractsAllFieldsIncludingGPS(t *testing.T) {
	path := writeTestFile(t, "fixture.jpg", exifFixtureJPEG(t))
	data, err := ParseImageEXIF(path)
	if err != nil {
		t.Fatalf("解析 JPEG EXIF 失败: %v", err)
	}
	assertFixtureEXIF(t, data)
}

func TestImageExifParseTIFFRawExtractsAllFields(t *testing.T) {
	// TIFF 系 RAW（DNG/CR2/NEF/ARW/RW2/ORF）的文件头就是 TIFF blob 本身。
	for _, extension := range []string{"dng", "cr2", "nef", "arw", "orf", "rw2", "tif"} {
		path := writeTestFile(t, "fixture."+extension, exifFixtureTIFF())
		data, err := ParseImageEXIF(path)
		if err != nil {
			t.Fatalf("解析 %s EXIF 失败: %v", extension, err)
		}
		assertFixtureEXIF(t, data)
	}
}

func TestImageExifParseISOBMFFContainerScansExifMagic(t *testing.T) {
	for _, extension := range []string{"heic", "heif", "cr3"} {
		path := writeTestFile(t, "fixture."+extension, exifFixtureISOBMFF())
		data, err := ParseImageEXIF(path)
		if err != nil {
			t.Fatalf("解析 %s EXIF 失败: %v", extension, err)
		}
		assertFixtureEXIF(t, data)
	}
}

func TestImageExifParseTreatsMissingAndBrokenEXIFAsEmpty(t *testing.T) {
	cases := map[string][]byte{
		"plain.jpg":   exifTestBaseJPEG(t),              // 合法 JPEG，无 APP1
		"cover.png":   []byte("\x89PNG\r\n\x1a\n....."), // 不在提取范围的格式
		"broken.heic": []byte("ftypheic no exif here"),  // ISO-BMFF 但无魔数
		"garbage.dng": bytes.Repeat([]byte{0x7f}, 4096), // 结构损坏
	}
	for name, content := range cases {
		path := writeTestFile(t, name, content)
		data, err := ParseImageEXIF(path)
		if err != nil {
			t.Fatalf("%s 解析不应报错: %v", name, err)
		}
		if !data.IsEmpty() {
			t.Fatalf("%s 应解析为空: %+v", name, data)
		}
	}
}

func TestImageExifParseReportsIOErrorForMissingFile(t *testing.T) {
	if _, err := ParseImageEXIF(filepath.Join(t.TempDir(), "absent.jpg")); err == nil {
		t.Fatal("文件不存在时应返回 IO 错误")
	}
}

func TestImageExifParseIgnoresEXIFBeyondHeaderWindow(t *testing.T) {
	// EXIF 在头部扫描窗口之外时按"无 EXIF"处理，避免为一张 RAW 读入整份文件。
	padded := append(bytes.Repeat([]byte{0x00}, imageEXIFHeaderScanLimit), exifMagic...)
	padded = append(padded, exifFixtureTIFF()...)
	path := writeTestFile(t, "far.heic", padded)
	data, err := ParseImageEXIF(path)
	if err != nil {
		t.Fatalf("解析不应报错: %v", err)
	}
	if !data.IsEmpty() {
		t.Fatalf("窗口外的 EXIF 不应被提取: %+v", data)
	}
}

// ===== 持久化：限定 EXIF 列 =====

func TestImageExifRefreshPersistsColumnsWithoutTouchingOthers(t *testing.T) {
	setupVideoServiceTestDB(t)
	path := writeTestFile(t, "persist.jpg", exifFixtureJPEG(t))
	img := &models.Image{Name: "persist.jpg", Path: path, Directory: filepath.Dir(path), Size: 42, Format: "jpg", IsFavorite: true}
	if err := database.DB.Create(img).Error; err != nil {
		t.Fatalf("创建图片记录失败: %v", err)
	}

	parsedAt := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	found, err := refreshImageEXIF(database.DB, img.ID, path, func() time.Time { return parsedAt })
	if err != nil {
		t.Fatalf("EXIF 回填失败: %v", err)
	}
	if !found {
		t.Fatal("夹具含 EXIF，found 应为 true")
	}

	var stored models.Image
	if err := database.DB.First(&stored, img.ID).Error; err != nil {
		t.Fatalf("读取图片失败: %v", err)
	}
	if stored.CameraMake != exifFixtureMake || stored.CameraModel != exifFixtureModel || stored.LensModel != exifFixtureLens {
		t.Fatalf("相机/镜头列未落库: %+v", stored)
	}
	if stored.ISO != exifFixtureISO || stored.FNumber != exifFixtureFNumber || stored.ExposureTime != exifFixtureExposure {
		t.Fatalf("拍摄参数列未落库: %+v", stored)
	}
	if stored.FocalLength != exifFixtureFocalLength || stored.ExifOrientation != exifFixtureOrientation {
		t.Fatalf("焦距/方向列未落库: %+v", stored)
	}
	if stored.TakenAt == nil || !stored.TakenAt.Equal(exifFixtureTakenAt()) {
		t.Fatalf("taken_at 未落库: %v", stored.TakenAt)
	}
	if stored.GPSLatitude == nil || stored.GPSLongitude == nil {
		t.Fatalf("GPS 未落库: %+v", stored)
	}
	if stored.ExifParsedAt == nil || !stored.ExifParsedAt.Equal(parsedAt) {
		t.Fatalf("exif_parsed_at 未落库: %v", stored.ExifParsedAt)
	}
	// 非 EXIF 列不受影响。
	if !stored.IsFavorite || stored.Size != 42 || stored.Format != "jpg" {
		t.Fatalf("非 EXIF 列被改写: %+v", stored)
	}
}

func TestImageExifRefreshStampsParsedAtWhenNoEXIF(t *testing.T) {
	setupVideoServiceTestDB(t)
	path := writeTestFile(t, "bare.png", []byte("\x89PNG\r\n\x1a\nnot-a-real-png"))
	img := &models.Image{Name: "bare.png", Path: path, Directory: filepath.Dir(path), Size: 8, Format: "png"}
	if err := database.DB.Create(img).Error; err != nil {
		t.Fatalf("创建图片记录失败: %v", err)
	}

	found, err := refreshImageEXIF(database.DB, img.ID, path, time.Now)
	if err != nil {
		t.Fatalf("EXIF 回填失败: %v", err)
	}
	if found {
		t.Fatal("PNG 无 EXIF，found 应为 false")
	}
	var stored models.Image
	if err := database.DB.First(&stored, img.ID).Error; err != nil {
		t.Fatalf("读取图片失败: %v", err)
	}
	if stored.ExifParsedAt == nil {
		t.Fatal("无 EXIF 也必须写 exif_parsed_at，用于区分未解析")
	}
	if stored.TakenAt != nil || stored.CameraMake != "" || stored.GPSLatitude != nil {
		t.Fatalf("无 EXIF 时其余列应留空: %+v", stored)
	}
}

// ===== 双轨回填之一：扫描入库时解析一次 =====

func TestImageExifScanIngestParsesOnAdd(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "scanned.jpg")
	if err := os.WriteFile(path, exifFixtureJPEG(t), 0644); err != nil {
		t.Fatalf("写扫描夹具失败: %v", err)
	}

	created, err := NewImageService().addImage(path)
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	var stored models.Image
	if err := database.DB.First(&stored, created.ID).Error; err != nil {
		t.Fatalf("读取图片失败: %v", err)
	}
	if stored.ExifParsedAt == nil || stored.TakenAt == nil || stored.CameraMake != exifFixtureMake {
		t.Fatalf("入库时未解析 EXIF: %+v", stored)
	}
}

// ===== 双轨回填之二：显式后台补全任务 =====

func waitImageEXIFBackfill(t *testing.T, svc *ImageEXIFBackfillService) ImageEXIFBackfillStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := svc.GetImageEXIFBackfillStatus()
		if !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("EXIF 补全 worker 未停止: %+v", svc.GetImageEXIFBackfillStatus())
	return ImageEXIFBackfillStatus{}
}

func TestImageExifBackfillTargetsUnparsedActiveImages(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()

	writeImage := func(name string, content []byte, format string) *models.Image {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatalf("写夹具失败: %v", err)
		}
		img := &models.Image{Name: name, Path: path, Directory: root, Size: int64(len(content)), Format: format}
		if err := database.DB.Create(img).Error; err != nil {
			t.Fatalf("创建图片记录失败: %v", err)
		}
		return img
	}

	withEXIF := writeImage("a.jpg", exifFixtureJPEG(t), "jpg")
	withoutEXIF := writeImage("b.png", []byte("\x89PNG\r\n\x1a\nx"), "png")
	missingFile := writeImage("c.jpg", exifFixtureJPEG(t), "jpg")
	if err := os.Remove(missingFile.Path); err != nil {
		t.Fatalf("删除夹具失败: %v", err)
	}
	// 已解析过的图片不进目标集。
	alreadyParsed := writeImage("d.jpg", exifFixtureJPEG(t), "jpg")
	stamp := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := database.DB.Model(&models.Image{}).Where("id = ?", alreadyParsed.ID).
		Update("exif_parsed_at", stamp).Error; err != nil {
		t.Fatalf("预置 exif_parsed_at 失败: %v", err)
	}

	svc := NewImageEXIFBackfillService()
	if _, err := svc.StartImageEXIFBackfill(context.Background()); err != nil {
		t.Fatalf("启动补全任务失败: %v", err)
	}
	status := waitImageEXIFBackfill(t, svc)

	if status.Total != 3 {
		t.Fatalf("目标集应为 3 张未解析图片, got %d", status.Total)
	}
	if status.Succeeded != 1 || status.Skipped != 1 || status.Failed != 1 {
		t.Fatalf("计数不符: %+v", status)
	}
	if !status.Completed || status.Cancelled {
		t.Fatalf("任务应正常完成: %+v", status)
	}
	if len(status.Failures) != 1 || status.Failures[0].ImageID != missingFile.ID {
		t.Fatalf("失败留痕不符: %+v", status.Failures)
	}

	var parsed models.Image
	if err := database.DB.First(&parsed, withEXIF.ID).Error; err != nil {
		t.Fatalf("读取图片失败: %v", err)
	}
	if parsed.TakenAt == nil || parsed.CameraMake != exifFixtureMake {
		t.Fatalf("含 EXIF 图片未回填: %+v", parsed)
	}

	var stamped models.Image
	if err := database.DB.First(&stamped, withoutEXIF.ID).Error; err != nil {
		t.Fatalf("读取图片失败: %v", err)
	}
	if stamped.ExifParsedAt == nil {
		t.Fatal("无 EXIF 图片也应写 exif_parsed_at")
	}

	var untouched models.Image
	if err := database.DB.First(&untouched, alreadyParsed.ID).Error; err != nil {
		t.Fatalf("读取图片失败: %v", err)
	}
	if untouched.ExifParsedAt == nil || !untouched.ExifParsedAt.Equal(stamp) {
		t.Fatalf("已解析图片不应被重跑: %v", untouched.ExifParsedAt)
	}
	if untouched.TakenAt != nil {
		t.Fatalf("已解析图片不应被改写: %+v", untouched)
	}

	// 失败图片的 exif_parsed_at 仍为 NULL，下次可重试。
	var failedRow models.Image
	if err := database.DB.First(&failedRow, missingFile.ID).Error; err != nil {
		t.Fatalf("读取图片失败: %v", err)
	}
	if failedRow.ExifParsedAt != nil {
		t.Fatalf("读文件失败时不应标记已解析: %v", failedRow.ExifParsedAt)
	}
}

func TestImageExifBackfillRejectsSecondStartWhileRunning(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewImageEXIFBackfillService()
	if err := svc.CancelImageEXIFBackfill(); err == nil {
		t.Fatal("未运行时取消应报错")
	}

	// 第 1 次 now() 在 Start 内（Running 尚未置位），第 2 次在 worker 的首个
	// update 里（Running 已置位）——卡在第 2 次即可确定性地观察"运行中"窗口。
	release := make(chan struct{})
	calls := 0
	var callsMu sync.Mutex
	svc.now = func() time.Time {
		callsMu.Lock()
		calls++
		blocked := calls == 2
		callsMu.Unlock()
		if blocked {
			<-release
		}
		return time.Now()
	}

	if _, err := svc.StartImageEXIFBackfill(context.Background()); err != nil {
		t.Fatalf("首次启动失败: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		callsMu.Lock()
		reached := calls >= 2
		callsMu.Unlock()
		if reached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker 未进入运行窗口")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := svc.StartImageEXIFBackfill(context.Background()); !errors.Is(err, ErrImageEXIFBackfillBusy) {
		t.Fatalf("运行中重复启动应返回 busy, got %v", err)
	}
	close(release)
	if status := waitImageEXIFBackfill(t, svc); !status.Completed {
		t.Fatalf("任务应完成: %+v", status)
	}
}

func TestImageExifBackfillReportsCancelledWhenContextDone(t *testing.T) {
	setupVideoServiceTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := NewImageEXIFBackfillService()
	if _, err := svc.StartImageEXIFBackfill(ctx); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitImageEXIFBackfill(t, svc)
	if !status.Cancelled || status.Completed {
		t.Fatalf("已取消的 context 应产出 cancelled 终态: %+v", status)
	}
	if len(status.Failures) != 0 {
		t.Fatalf("取消不应记为失败: %+v", status.Failures)
	}
}

// ===== AC-14：外发 JPEG 不含 EXIF/GPS =====

// exifBytesPresent 用字节断言判定 JPEG 里是否残留 EXIF 段与 GPS 数值。
func exifBytesPresent(data []byte) bool { return bytes.Contains(data, exifMagic) }

// gpsRationalBytesPresent 在字节流里找夹具 GPS 经度的小端 RATIONAL 编码
// （121/1 后接 28/1），确认坐标本身没有以任何形式残留。
func gpsRationalBytesPresent(data []byte) bool {
	needle := make([]byte, 0, 16)
	needle = binary.LittleEndian.AppendUint32(needle, exifFixtureLongitudeDeg)
	needle = binary.LittleEndian.AppendUint32(needle, 1)
	needle = binary.LittleEndian.AppendUint32(needle, 28)
	needle = binary.LittleEndian.AppendUint32(needle, 1)
	bigEndian := make([]byte, 0, 16)
	bigEndian = binary.BigEndian.AppendUint32(bigEndian, exifFixtureLongitudeDeg)
	bigEndian = binary.BigEndian.AppendUint32(bigEndian, 1)
	bigEndian = binary.BigEndian.AppendUint32(bigEndian, 28)
	bigEndian = binary.BigEndian.AppendUint32(bigEndian, 1)
	return bytes.Contains(data, needle) || bytes.Contains(data, bigEndian)
}

func TestImageExifStripJPEGMetadataRemovesEXIFAndGPS(t *testing.T) {
	source := exifFixtureJPEG(t)
	if !exifBytesPresent(source) || !gpsRationalBytesPresent(source) {
		t.Fatal("夹具本身应含 EXIF 与 GPS，否则断言无意义")
	}

	stripped, err := StripJPEGMetadataForUpload(source)
	if err != nil {
		t.Fatalf("剥除失败: %v", err)
	}
	if exifBytesPresent(stripped) {
		t.Fatal("剥除后仍含 Exif\\x00\\x00 魔数")
	}
	if gpsRationalBytesPresent(stripped) {
		t.Fatal("剥除后仍含 GPS 坐标字节")
	}
	if _, err := exif.Decode(bytes.NewReader(stripped)); err == nil {
		t.Fatal("剥除后 goexif 仍能解出 EXIF")
	}
	// 图像本体必须完好——剥除是无损的段删除，不是重编码。
	decoded, err := jpeg.Decode(bytes.NewReader(stripped))
	if err != nil {
		t.Fatalf("剥除后 JPEG 不可解码: %v", err)
	}
	if decoded.Bounds().Dx() != 64 || decoded.Bounds().Dy() != 48 {
		t.Fatalf("剥除后尺寸变化: %v", decoded.Bounds())
	}
}

func TestImageExifStripJPEGMetadataRejectsNonJPEG(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":       nil,
		"png":         []byte("\x89PNG\r\n\x1a\n"),
		"truncated":   {0xFF, 0xD8, 0xFF, 0xE1, 0x00},
		"noScanNoEOI": append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x08}, exifMagic...),
	} {
		if _, err := StripJPEGMetadataForUpload(data); err == nil {
			t.Fatalf("%s 应被拒绝，剥不掉就不能外发", name)
		}
	}
}

// TestImageExifStripJPEGMetadataRemovesTrailingImageMetadata 覆盖多帧 JPEG：MPF /
// 动态照片会在 EOI 之后追加第二张完整图像，其 EXIF 同样带 GPS。"SOS 之后原样透传"
// 的实现会让这部分绕过剥除。
func TestImageExifStripJPEGMetadataRemovesTrailingImageMetadata(t *testing.T) {
	single := exifFixtureJPEG(t)
	multiFrame := append(append([]byte{}, single...), single...)

	stripped, err := StripJPEGMetadataForUpload(multiFrame)
	if err != nil {
		t.Fatalf("剥除多帧 JPEG 失败: %v", err)
	}
	if exifBytesPresent(stripped) || gpsRationalBytesPresent(stripped) {
		t.Fatal("EOI 之后追加的第二帧 EXIF/GPS 未被剥除")
	}
	if _, err := jpeg.Decode(bytes.NewReader(stripped)); err != nil {
		t.Fatalf("剥除后首帧不可解码: %v", err)
	}
}

// TestImageExifStripJPEGMetadataKeepsOnlyWhitelistedAPPSegments 覆盖白名单语义：
// 渲染必需段透传，其余 APPn（这里用能携带定位的 Ricoh RMETA APP5 与 JUMBF APP11 代表）
// 一律剥除。
func TestImageExifStripJPEGMetadataKeepsOnlyWhitelistedAPPSegments(t *testing.T) {
	appSegment := func(marker byte, payload []byte) []byte {
		segment := []byte{0xFF, marker}
		segment = binary.BigEndian.AppendUint16(segment, uint16(len(payload)+2))
		return append(segment, payload...)
	}
	base := exifTestBaseJPEG(t)
	injected := append([]byte{}, base[:2]...)
	injected = append(injected, appSegment(0xE2, append([]byte("ICC_PROFILE\x00"), 0x01, 0x02))...)
	injected = append(injected, appSegment(0xEE, []byte("Adobe-keepme"))...)
	injected = append(injected, appSegment(0xE5, []byte("RMETA-secret-location"))...)
	injected = append(injected, appSegment(0xEB, []byte("JUMBF-c2pa-provenance"))...)
	injected = append(injected, appSegment(0xFE, []byte("COM-private-note"))...)
	injected = append(injected, base[2:]...)

	stripped, err := StripJPEGMetadataForUpload(injected)
	if err != nil {
		t.Fatalf("剥除失败: %v", err)
	}
	for _, kept := range []string{"ICC_PROFILE", "Adobe-keepme"} {
		if !bytes.Contains(stripped, []byte(kept)) {
			t.Fatalf("白名单内的渲染必需段被误删: %s", kept)
		}
	}
	for _, dropped := range []string{"RMETA-secret-location", "JUMBF-c2pa-provenance", "COM-private-note"} {
		if bytes.Contains(stripped, []byte(dropped)) {
			t.Fatalf("非白名单元数据段未被剥除: %s", dropped)
		}
	}
	if _, err := jpeg.Decode(bytes.NewReader(stripped)); err != nil {
		t.Fatalf("剥除后 JPEG 不可解码: %v", err)
	}
}

// TestImageExifAIDescriptionRefusesToSendWhenStripFails 钉住 AC-14 的 fail-closed 语义：
// 剥不掉就一个字节都不外发，并留下 metadata_strip_failed 留痕。
func TestImageExifAIDescriptionRefusesToSendWhenStripFails(t *testing.T) {
	called := false
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		called = true
		return "不该走到这里", nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	// 缩略图产物不是可解析的 JPEG——剥除会失败。
	svc.thumbnails.SetDecodeRunnersForTest(
		func(ctx context.Context, sourcePath, destinationPath string, maxEdge int) error {
			return os.WriteFile(destinationPath, []byte("definitely-not-a-jpeg"), 0644)
		},
		func(ctx context.Context, sourcePath string) (int, int, error) { return 8, 6, nil },
	)
	img := imageAIDescriptionTestImage(t, "heic")

	if _, err := svc.RegenerateImageAIDescription(img.ID); err == nil {
		t.Fatal("剥除失败时应报错")
	}
	if called {
		t.Fatal("剥除失败后仍然发起了外发请求")
	}
	var row models.ImageAIDescription
	if err := database.DB.First(&row, "image_id = ?", img.ID).Error; err != nil {
		t.Fatalf("读取描述行失败: %v", err)
	}
	if row.Status != imageAIDescriptionStatusFailed || row.ErrorCode != imageAIDescriptionErrorMetadataStrip {
		t.Fatalf("失败留痕不符: status=%s code=%s", row.Status, row.ErrorCode)
	}
}

// TestImageExifRejectsValueBudgetAmplification 覆盖解析期的内存放大防护：goexif 会为
// 每个 IFD 条目单独保留一份值拷贝，成千上万个条目指向同一大块数据能把小文件放大成
// 数百 GB 内存。预检超预算时按"没有 EXIF"处理，不进解析器。
func TestImageExifRejectsValueBudgetAmplification(t *testing.T) {
	const entries = 4000
	const valueBytes = 64 << 10 // 每条目 64 KiB × 4000 = 244 MiB，远超 16 MiB 预算

	blob := make([]byte, 8+2+entries*12+4)
	copy(blob, []byte{'I', 'I', 0x2a, 0x00})
	binary.LittleEndian.PutUint32(blob[4:8], 8)
	binary.LittleEndian.PutUint16(blob[8:10], entries)
	valueOffset := uint32(len(blob))
	for entry := 0; entry < entries; entry++ {
		base := 10 + entry*12
		binary.LittleEndian.PutUint16(blob[base:], uint16(0x0100+entry%0xFF))
		binary.LittleEndian.PutUint16(blob[base+2:], 1) // BYTE
		binary.LittleEndian.PutUint32(blob[base+4:], valueBytes)
		binary.LittleEndian.PutUint32(blob[base+8:], valueOffset) // 全部指向同一处
	}
	blob = append(blob, make([]byte, valueBytes)...)

	if exifBlobWithinValueBudget(blob) {
		t.Fatal("放大构造应被预检拦下")
	}
	if data := parseImageEXIFFromHead(blob, imageEXIFSourceDirect); !data.IsEmpty() {
		t.Fatalf("超预算的 blob 应按无 EXIF 处理: %+v", data)
	}
	// 正常夹具不受影响。
	if !exifBlobWithinValueBudget(exifFixtureTIFF()) {
		t.Fatal("正常 EXIF 夹具被预检误伤")
	}
}

// TestImageExifSipsThumbnailRetainsEXIFUntilStripped 固化实测结论：darwin 的 sips
// 转码会原样保留源图 EXIF（含 GPS），因此外发前的剥除是必需的；ffmpeg 的 mjpeg
// 编码器则不写 EXIF。两条产线的产物剥除后都必须无 EXIF。
func TestImageExifSipsThumbnailRetainsEXIFUntilStripped(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sips 仅 darwin 可用")
	}
	if _, err := exec.LookPath("sips"); err != nil {
		t.Skip("sips 不可用")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jpg")
	if err := os.WriteFile(source, exifFixtureJPEG(t), 0644); err != nil {
		t.Fatalf("写源图失败: %v", err)
	}
	output := filepath.Join(dir, "sips.jpg")
	cmd := exec.Command("sips", "-s", "format", "jpeg", "-Z", "480", source, "--out", output)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("sips 转码失败: %v %s", err, combined)
	}
	produced, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("读取 sips 产物失败: %v", err)
	}
	if !exifBytesPresent(produced) || !gpsRationalBytesPresent(produced) {
		t.Fatalf("实测前提变了：sips 产物已不含 EXIF/GPS（bytes=%d）", len(produced))
	}
	stripped, err := StripJPEGMetadataForUpload(produced)
	if err != nil {
		t.Fatalf("剥除 sips 产物失败: %v", err)
	}
	if exifBytesPresent(stripped) || gpsRationalBytesPresent(stripped) {
		t.Fatal("sips 产物剥除后仍残留 EXIF/GPS")
	}
}

func TestImageExifFFmpegThumbnailCarriesNoEXIF(t *testing.T) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg 不可用")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jpg")
	if err := os.WriteFile(source, exifFixtureJPEG(t), 0644); err != nil {
		t.Fatalf("写源图失败: %v", err)
	}
	output := filepath.Join(dir, "ffmpeg.jpg")
	if err := runImageThumbnailFFmpeg(context.Background(), binary, source, output, 480); err != nil {
		t.Skipf("ffmpeg 转码失败: %v", err)
	}
	produced, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("读取 ffmpeg 产物失败: %v", err)
	}
	if exifBytesPresent(produced) || gpsRationalBytesPresent(produced) {
		t.Fatal("ffmpeg 产物意外携带 EXIF/GPS")
	}
}

// TestImageExifAIDescriptionSendsStrippedJPEG 端到端断言外发字节：缩略图带 EXIF+GPS，
// 到达 client 的 payload 必须已被剥净。
func TestImageExifAIDescriptionSendsStrippedJPEG(t *testing.T) {
	var sent []byte
	client := imageDescriptionClientFunc(func(ctx context.Context, imageID uint, prompt string, jpegData []byte) (string, error) {
		sent = append([]byte(nil), jpegData...)
		return "一段中文描述", nil
	})
	svc := newImageAIDescriptionTestService(t, client)
	// 覆写缩略图 runner，让 heic 产出带 EXIF+GPS 的 JPEG（复现 sips 的行为）。
	fixture := exifFixtureJPEG(t)
	svc.thumbnails.SetDecodeRunnersForTest(
		func(ctx context.Context, sourcePath, destinationPath string, maxEdge int) error {
			return os.WriteFile(destinationPath, fixture, 0644)
		},
		func(ctx context.Context, sourcePath string) (int, int, error) { return 64, 48, nil },
	)
	img := imageAIDescriptionTestImage(t, "heic")

	if _, err := svc.RegenerateImageAIDescription(img.ID); err != nil {
		t.Fatalf("生成描述失败: %v", err)
	}
	if len(sent) == 0 {
		t.Fatal("client 没有收到图片数据")
	}
	if exifBytesPresent(sent) {
		t.Fatal("外发字节仍含 Exif 段")
	}
	if gpsRationalBytesPresent(sent) {
		t.Fatal("外发字节仍含 GPS 坐标")
	}
	if _, err := jpeg.Decode(bytes.NewReader(sent)); err != nil {
		t.Fatalf("外发字节不是可解码 JPEG: %v", err)
	}
}

// TestImageExifSemanticIndexTextExcludesEXIF 守住"索引文本=标题+标签+AI 描述"三段不变：
// GPS 与相机参数不得进入语义索引。
func TestImageExifSemanticIndexTextExcludesEXIF(t *testing.T) {
	latitude, longitude := exifFixtureLatitude, exifFixtureLongitude
	takenAt := exifFixtureTakenAt()
	img := models.Image{
		Name:         "beach.jpg",
		CameraMake:   exifFixtureMake,
		CameraModel:  exifFixtureModel,
		LensModel:    exifFixtureLens,
		ISO:          exifFixtureISO,
		ExposureTime: exifFixtureExposure,
		TakenAt:      &takenAt,
		GPSLatitude:  &latitude,
		GPSLongitude: &longitude,
		Tags:         []models.Tag{{Name: "海边"}},
	}
	text, _ := (&ImageSemanticIndexService{}).buildIndexText(img, "画面里是一片海滩。", SemanticIndexConfig{MaxTextRunes: 4000})
	for _, forbidden := range []string{
		exifFixtureMake, exifFixtureModel, exifFixtureLens, exifFixtureExposure,
		"31.23", "121.46", "GPS", "2024",
	} {
		if bytes.Contains([]byte(text), []byte(forbidden)) {
			t.Fatalf("索引文本混入 EXIF 片段 %q: %q", forbidden, text)
		}
	}
	for _, expected := range []string{"beach.jpg", "海边", "画面里是一片海滩。"} {
		if !bytes.Contains([]byte(text), []byte(expected)) {
			t.Fatalf("索引文本缺少三段之一 %q: %q", expected, text)
		}
	}
}
