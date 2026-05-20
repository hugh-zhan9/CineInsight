package services

import (
	"context"
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"video-master/models"

	pigo "github.com/esimov/pigo/core"
)

//go:embed assets/pigo_facefinder
var pigoFaceAssets embed.FS

const (
	defaultVideoFaceFrameCount = 3
	videoFaceFrameMaxWidth     = 480
	videoFaceMinScore          = 50
)

type PigoVideoFaceDetector struct {
	frameCount int
	classifier *pigo.Pigo
}

func NewPigoVideoFaceDetector() (*PigoVideoFaceDetector, error) {
	cascade, err := pigoFaceAssets.ReadFile("assets/pigo_facefinder")
	if err != nil {
		return nil, err
	}
	classifier, err := pigo.NewPigo().Unpack(cascade)
	if err != nil {
		return nil, err
	}
	return &PigoVideoFaceDetector{
		frameCount: defaultVideoFaceFrameCount,
		classifier: classifier,
	}, nil
}

func (d *PigoVideoFaceDetector) DetectVideoFaces(ctx context.Context, video models.Video) ([]DetectedVideoFace, error) {
	if d == nil || d.classifier == nil {
		return nil, ErrVideoFaceDetectorUnavailable
	}
	ffmpegBin := findMediaBinary("ffmpeg")
	if ffmpegBin == "" {
		return nil, ErrVideoFaceDetectorUnavailable
	}
	if strings.TrimSpace(video.Path) == "" {
		return nil, nil
	}
	if _, err := os.Stat(video.Path); err != nil {
		return nil, ErrVideoFaceDetectorUnavailable
	}
	tmpDir, err := os.MkdirTemp("", "cineinsight-face-frames-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	framePaths, err := sampleFaceFrames(ctx, ffmpegBin, video, tmpDir, d.frameCount)
	if err != nil {
		return nil, err
	}
	var faces []DetectedVideoFace
	for _, frame := range framePaths {
		detected, err := d.detectImage(frame.path, frame.index, frame.position)
		if err != nil {
			continue
		}
		faces = append(faces, detected...)
	}
	return faces, nil
}

type sampledFaceFrame struct {
	path     string
	index    int
	position float64
}

func sampleFaceFrames(ctx context.Context, ffmpegBin string, video models.Video, tmpDir string, count int) ([]sampledFaceFrame, error) {
	if count <= 0 {
		count = defaultVideoFaceFrameCount
	}
	duration := video.Duration
	if duration <= 0 {
		duration = float64(count + 1)
	}
	frames := make([]sampledFaceFrame, 0, count)
	for i := 0; i < count; i++ {
		position := duration * float64(i+1) / float64(count+1)
		outPath := filepath.Join(tmpDir, fmt.Sprintf("face-frame-%d.jpg", i))
		cmd := exec.CommandContext(ctx, ffmpegBin,
			"-y",
			"-ss", strconv.FormatFloat(position, 'f', 2, 64),
			"-i", video.Path,
			"-frames:v", "1",
			"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", videoFaceFrameMaxWidth),
			"-q:v", "8",
			outPath,
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			return frames, fmt.Errorf("sample face frame failed: %w %s", err, truncateLogSnippet(string(output), 160))
		}
		frames = append(frames, sampledFaceFrame{path: outPath, index: i + 1, position: position})
	}
	return frames, nil
}

func (d *PigoVideoFaceDetector) detectImage(path string, frameIndex int, position float64) ([]DetectedVideoFace, error) {
	src, err := pigo.GetImage(path)
	if err != nil {
		return nil, err
	}
	pixels := pigo.RgbToGrayscale(src)
	bounds := src.Bounds()
	cols, rows := bounds.Dx(), bounds.Dy()
	params := pigo.CascadeParams{
		MinSize:     24,
		MaxSize:     minInt(cols, rows),
		ShiftFactor: 0.12,
		ScaleFactor: 1.12,
		ImageParams: pigo.ImageParams{
			Pixels: pixels,
			Rows:   rows,
			Cols:   cols,
			Dim:    cols,
		},
	}
	dets := d.classifier.RunCascade(params, 0)
	dets = d.classifier.ClusterDetections(dets, 0.2)
	faces := make([]DetectedVideoFace, 0, len(dets))
	for _, det := range dets {
		if det.Q < videoFaceMinScore {
			continue
		}
		x := det.Col - det.Scale/2
		y := det.Row - det.Scale/2
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		size := det.Scale
		faces = append(faces, DetectedVideoFace{
			FrameIndex:    frameIndex,
			FramePosition: position,
			X:             x,
			Y:             y,
			Width:         size,
			Height:        size,
			Score:         float64(det.Q),
			Signature:     faceSignature(src, image.Rect(x, y, x+size, y+size)),
			Source:        "pigo",
		})
	}
	sort.Slice(faces, func(i, j int) bool {
		return faces[i].Score > faces[j].Score
	})
	return faces, nil
}

func faceSignature(img image.Image, rect image.Rectangle) string {
	rect = rect.Intersect(img.Bounds())
	if rect.Empty() {
		return ""
	}
	const cells = 8
	values := make([]byte, 0, cells*cells)
	width := rect.Dx()
	height := rect.Dy()
	for cy := 0; cy < cells; cy++ {
		for cx := 0; cx < cells; cx++ {
			x := rect.Min.X + width*cx/cells
			y := rect.Min.Y + height*cy/cells
			r, g, b, _ := img.At(x, y).RGBA()
			gray := byte(((r >> 8) + (g >> 8) + (b >> 8)) / 3)
			values = append(values, gray)
		}
	}
	sum := sha1.Sum(values)
	return hex.EncodeToString(sum[:])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
