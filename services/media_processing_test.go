package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func processingTestPNG(t *testing.T, depth16 bool) []byte {
	t.Helper()
	var canvas image.Image
	if depth16 {
		pic := image.NewNRGBA64(image.Rect(0, 0, 32, 24))
		for y := 0; y < 24; y++ {
			for x := 0; x < 32; x++ {
				pic.SetNRGBA64(x, y, color.NRGBA64{R: uint16(x * 1577), G: uint16(y * 2011), B: 23456, A: uint16(10000 + x*1000)})
			}
		}
		canvas = pic
	} else {
		pic := image.NewNRGBA(image.Rect(0, 0, 32, 24))
		for y := 0; y < 24; y++ {
			for x := 0; x < 32; x++ {
				pic.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 7), G: uint8(y * 9), B: 45, A: uint8(100 + x*4)})
			}
		}
		canvas = pic
	}
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&output, canvas); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestMediaProcessingRechecksImageVisibilityAtEachBoundary(t *testing.T) {
	previous := database.DB
	setupImageThumbnailTestDB(t)
	t.Cleanup(func() { database.DB = previous })
	dir := t.TempDir()
	path := filepath.Join(dir, "visible.png")
	original := processingTestPNG(t, false)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	img := imageThumbnailTestCreateImage(t, path, "png")
	s := NewMediaProcessingService(context.Background(), dir, nil, nil)
	defer s.StopAndWait(context.Background())
	parent, err := s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: img.ID, Operation: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: img.ID, Operation: "edit", ParentID: parent.ID, PNGBase64: base64.StdEncoding.EncodeToString(original)})
	if err != nil {
		t.Fatal(err)
	}
	processingWait(t, s, view.ID)
	if err = database.DB.Model(&models.Image{}).Where("id = ?", img.ID).Update("is_stale", true).Error; err != nil {
		t.Fatal(err)
	}
	if file, release, err := s.OpenAsset(context.Background(), parent.ID, "source"); err == nil {
		release()
		file.Close()
		t.Fatal("read hidden image")
	}
	if err = s.Save(context.Background(), view.ID, filepath.Join(dir, "hidden-copy.png")); err == nil {
		t.Fatal("saved hidden source")
	}
	if _, err = s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: img.ID, Operation: "compress", ParentID: parent.ID}); err == nil {
		t.Fatal("processed hidden source")
	}
	if err = database.DB.Model(&models.Image{}).Where("id = ?", img.ID).Update("is_stale", false).Error; err != nil {
		t.Fatal(err)
	}
	var settings models.Settings
	settings.ID = 1
	settings.ImageScanExcludePaths = dir
	if err = database.DB.Save(&settings).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(parent.ID); err == nil {
		t.Fatal("old token bypassed blacklist")
	}
}

func TestImageEnhancementBudgetIncludesSourceOutputAndSimultaneousBuffers(t *testing.T) {
	if processingEnhancementWorkingBytes(8000, 6000, 0, 0) <= 1<<30 {
		t.Fatal("48MP full image passed working set budget")
	}
	if processingEnhancementWorkingBytes(8000, 6000, 512, 512) > 1<<30 {
		t.Fatal("bounded local trial unexpectedly rejected")
	}
	if processingEnhancementWorkingBytes(1920, 1080, 0, 0) >= 1<<30 {
		t.Fatal("ordinary low-resolution whole image should fit")
	}
}

func processingTestService(t *testing.T) (*MediaProcessingService, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.png")
	original := processingTestPNG(t, false)
	if err := os.WriteFile(sourcePath, original, 0600); err != nil {
		t.Fatal(err)
	}
	s := NewMediaProcessingService(context.Background(), dir, nil, nil)
	s.resolve = func(ctx context.Context, kind string, id uint) (processingSource, error) {
		info, err := os.Stat(sourcePath)
		return processingSource{kind: kind, id: id, path: sourcePath, name: "source.png", format: "png", width: 32, height: 24, info: info}, err
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.StopAndWait(ctx); err != nil {
			t.Errorf("stop processing: %v", err)
		}
	})
	return s, sourcePath, original
}

func processingWait(t *testing.T, s *MediaProcessingService, id string) *MediaProcessingView {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		view, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if view.State != "queued" && view.State != "running" {
			return view
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("processing did not finish")
	return nil
}

func processingInspect(t *testing.T, s *MediaProcessingService) *MediaProcessingView {
	t.Helper()
	view, err := s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: 1, Operation: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestMediaProcessingCopiesNeverOverwriteAndRejectChangedSource(t *testing.T) {
	s, path, original := processingTestService(t)
	parent := processingInspect(t, s)
	view, err := s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: 1, Operation: "edit", ParentID: parent.ID, PNGBase64: base64.StdEncoding.EncodeToString(original)})
	if err != nil {
		t.Fatal(err)
	}
	if got := processingWait(t, s, view.ID); got.State != "ready" {
		t.Fatal(got)
	}
	if err = s.Save(context.Background(), view.ID, path); err == nil {
		t.Fatal("overwrote original")
	}
	alias := filepath.Join(filepath.Dir(path), "alias.png")
	if err = os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(context.Background(), view.ID, alias); err == nil {
		t.Fatal("overwrote hardlink")
	}
	target := filepath.Join(filepath.Dir(path), "edited.png")
	if err = s.Save(context.Background(), view.ID, target); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(target)
	if !bytes.Equal(saved, original) {
		t.Fatal("saved different pixels")
	}
	if err = s.Save(context.Background(), view.ID, target); err == nil {
		t.Fatal("overwrote saved copy")
	}
	if err = os.WriteFile(path, append(append([]byte{}, original...), 0), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(context.Background(), view.ID, filepath.Join(filepath.Dir(path), "stale.png")); err == nil {
		t.Fatal("saved stale edit")
	}
	if _, err = s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: 1, Operation: "compress", ParentID: parent.ID}); err == nil {
		t.Fatal("accepted changed source")
	}
}

func TestMediaProcessingMaintenanceClosesAssetBeforeGlobalPathLock(t *testing.T) {
	s, _, _ := processingTestService(t)
	parent := processingInspect(t, s)
	file, release, err := s.OpenAsset(context.Background(), parent.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.StopAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = file.Stat(); err == nil {
		t.Fatal("lease still open")
	}
	unlock := BeginLibraryMaintenance()
	unlock()
	if _, err = s.Get(parent.ID); err == nil {
		t.Fatal("old source token survived")
	}
	if err = s.Resume(); err != nil {
		t.Fatal(err)
	}
	fresh := processingInspect(t, s)
	if fresh.ID == parent.ID {
		t.Fatal("request identity reused")
	}
}

func TestMediaProcessingDirtyRevisionAndPrivateOutputProtection(t *testing.T) {
	s, _, original := processingTestService(t)
	parent := processingInspect(t, s)
	if err := s.SetDirty(parent.ID, 3, true); err != nil {
		t.Fatal(err)
	}
	_ = s.SetDirty(parent.ID, 2, false)
	if len(s.ActiveViews(true)) != 1 {
		t.Fatal("late clean revision erased draft")
	}
	if len(s.ActiveViews(false)) != 0 {
		t.Fatal("draft counted as a running job")
	}
	_ = s.SetDirty(parent.ID, 4, false)
	if len(s.ActiveViews(true)) != 0 {
		t.Fatal("saved draft stayed dirty")
	}
	view, err := s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: 1, Operation: "edit", ParentID: parent.ID, PNGBase64: base64.StdEncoding.EncodeToString(original)})
	if err != nil {
		t.Fatal(err)
	}
	processingWait(t, s, view.ID)
	if err = s.Save(context.Background(), view.ID, filepath.Join(s.entries[view.ID].dir, "saved.png")); err == nil {
		t.Fatal("saved user output inside temporary cleanup tree")
	}
}

func TestMediaProcessingCancelsQueuedWorkAndMalformedOutput(t *testing.T) {
	s, path, original := processingTestService(t)
	parent := processingInspect(t, s)
	if err := s.slot.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	view, err := s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: 1, Operation: "compress", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.Release(ctx, view.ID); err != nil {
		t.Fatal(err)
	}
	s.slot.Release()
	if _, err = s.Get(view.ID); err == nil {
		t.Fatal("cancelled request still accessible")
	}
	bad, err := s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: 1, Operation: "edit", ParentID: parent.ID, PNGBase64: "bad"})
	if err != nil {
		t.Fatal(err)
	}
	if result := processingWait(t, s, bad.ID); result.State != "failed" || result.ResultURL != "" {
		t.Fatal(result)
	}
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(unchanged, original) {
		t.Fatal("failed processing changed source")
	}
	if _, err = s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: 1, Operation: "edit"}); err == nil {
		t.Fatal("accepted unbound edit")
	}
}

func TestMediaProcessingCompressionPreservesOriginalAndReportsSavings(t *testing.T) {
	s, path, original := processingTestService(t)
	parent := processingInspect(t, s)
	view, err := s.Begin(context.Background(), MediaProcessingRequest{Kind: "image", MediaID: 1, Operation: "compress", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	result := processingWait(t, s, view.ID)
	if result.State != "ready" || result.OutputBytes >= result.SourceBytes {
		t.Fatal(result)
	}
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(unchanged, original) {
		t.Fatal("compression changed original")
	}
	target := filepath.Join(filepath.Dir(path), "compressed.png")
	if err = s.Save(context.Background(), result.ID, target); err != nil {
		t.Fatal(err)
	}
	compressed, _ := os.ReadFile(target)
	expected, _ := recompressPNG(original)
	if !bytes.Equal(compressed, expected) {
		t.Fatal("published different result")
	}
}

func TestPNGOptimizationPreserves16BitPixelsTransparencyAndMetadata(t *testing.T) {
	for _, depth16 := range []bool{false, true} {
		original := processingTestPNG(t, depth16)
		data := []byte("note\x00metadata retained")
		var chunk bytes.Buffer
		_ = binary.Write(&chunk, binary.BigEndian, uint32(len(data)))
		chunk.WriteString("tEXt")
		chunk.Write(data)
		_ = binary.Write(&chunk, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("tEXt"), data...)))
		original = append(append(append([]byte{}, original[:len(original)-12]...), chunk.Bytes()...), original[len(original)-12:]...)
		compressed, err := recompressPNG(original)
		if err != nil {
			t.Fatal(err)
		}
		if len(compressed) >= len(original) || !bytes.Contains(compressed, chunk.Bytes()) {
			t.Fatal("no savings or metadata lost")
		}
		a, err := png.Decode(bytes.NewReader(original))
		if err != nil {
			t.Fatal(err)
		}
		b, err := png.Decode(bytes.NewReader(compressed))
		if err != nil {
			t.Fatal(err)
		}
		for y := 0; y < 24; y++ {
			for x := 0; x < 32; x++ {
				ar, ag, ab, aa := a.At(x, y).RGBA()
				br, bg, bb, ba := b.At(x, y).RGBA()
				if ar != br || ag != bg || ab != bb || aa != ba {
					t.Fatalf("pixel changed at %d,%d", x, y)
				}
			}
		}
		compressed[29] ^= 1
		if _, err = recompressPNG(compressed); err == nil {
			t.Fatal("accepted invalid CRC")
		}
	}
}

func TestPNGOptimizationPreservesAllAnimationFramesAndTiming(t *testing.T) {
	chunk := func(kind string, data []byte) []byte {
		var out bytes.Buffer
		_ = binary.Write(&out, binary.BigEndian, uint32(len(data)))
		out.WriteString(kind)
		out.Write(data)
		_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(kind), data...)))
		return out.Bytes()
	}
	base := processingTestPNG(t, false)
	var idat []byte
	for offset := 8; offset < len(base); {
		size := int(binary.BigEndian.Uint32(base[offset:]))
		if string(base[offset+4:offset+8]) == "IDAT" {
			idat = append(idat, base[offset+8:offset+8+size]...)
		}
		offset += size + 12
	}
	control := func(sequence uint32) []byte {
		data := make([]byte, 26)
		binary.BigEndian.PutUint32(data, sequence)
		binary.BigEndian.PutUint32(data[4:], 32)
		binary.BigEndian.PutUint32(data[8:], 24)
		binary.BigEndian.PutUint16(data[20:], 7)
		binary.BigEndian.PutUint16(data[22:], 100)
		data[24] = 1
		return chunk("fcTL", data)
	}
	actl := chunk("acTL", []byte{0, 0, 0, 2, 0, 0, 0, 3})
	first, second := control(0), control(1)
	fdat := chunk("fdAT", append([]byte{0, 0, 0, 2}, idat...))
	animated := append(append([]byte{}, base[:33]...), actl...)
	animated = append(animated, first...)
	animated = append(animated, chunk("IDAT", idat)...)
	animated = append(animated, second...)
	animated = append(animated, fdat...)
	animated = append(animated, chunk("IEND", nil)...)
	optimized, err := recompressPNG(animated)
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range [][]byte{actl, first, second, fdat} {
		if !bytes.Contains(optimized, metadata) {
			t.Fatal("animation frame, sequence, timing or loop count changed")
		}
	}
	if _, err = png.Decode(bytes.NewReader(optimized)); err != nil {
		t.Fatal(err)
	}
}

func TestJPEGOptimizationRejectsTrailingPayload(t *testing.T) {
	var original bytes.Buffer
	if err := jpeg.Encode(&original, image.NewNRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	// Motion Photos can append a video after EOI; dropping it is not strict lossless.
	withPayload := append(append([]byte{}, original.Bytes()...), []byte("synthetic-motion-photo-payload")...)
	if _, err := jpegMetadata(withPayload); err == nil {
		t.Fatal("accepted a JPEG trailer that jpegtran would discard")
	}
	if _, err := jpegMetadata(original.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestJPEGOptimizationReallyPreservesPixelsAndExtraMarkers(t *testing.T) {
	tool, err := findJPEGTran()
	if err != nil {
		t.Fatal("test requires the installed jpegtran:", err)
	}
	picture := image.NewNRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			picture.Set(x, y, color.NRGBA{R: uint8(x * 4), G: uint8(y * 5), B: 100, A: 255})
		}
	}
	var data bytes.Buffer
	if err = jpeg.Encode(&data, picture, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	comment := []byte("\xff\xfe\x00\x0bkeep-meta")
	original := append(append(append([]byte{}, data.Bytes()[:2]...), comment...), data.Bytes()[2:]...)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jpg")
	target := filepath.Join(dir, "target.jpg")
	if err = os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = runEnhancementCommand(context.Background(), tool, []string{"-copy", "all", "-optimize", "-outfile", target, source}); err != nil {
		t.Fatal(err)
	}
	optimized, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err = losslessJPEGEqual(context.Background(), original, optimized); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(optimized, []byte("keep-meta")) {
		t.Fatal("comment lost")
	}
	changed := image.NewNRGBA(picture.Bounds())
	changed.Set(0, 0, color.White)
	var different bytes.Buffer
	_ = jpeg.Encode(&different, changed, nil)
	differentWithMetadata := append(append(append([]byte{}, different.Bytes()[:2]...), comment...), different.Bytes()[2:]...)
	if err = losslessJPEGEqual(context.Background(), original, differentWithMetadata); err == nil {
		t.Fatal("accepted lossy image")
	}
}
