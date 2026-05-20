package services

import (
	"context"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestVideoFaceServiceSkipsWhenDetectorUnavailable(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "no-detector.mp4", Path: "/tmp/no-detector.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	svc := NewVideoFaceService(VideoFaceServiceOptions{
		Detector: nil,
	})

	result, err := svc.AnalyzeVideo(context.Background(), video)
	if err != nil {
		t.Fatalf("检测器不可用时应降级而非失败: %v", err)
	}
	if result.Status != VideoFaceAnalysisStatusSkipped || result.Reason != "detector_unavailable" {
		t.Fatalf("状态错误: %+v", result)
	}
	if got := countRows(t, "video_faces"); got != 0 {
		t.Fatalf("跳过时不应写入人脸记录，实际 %d", got)
	}
}

func TestVideoFaceServicePersistsDetectedFacesAndClusters(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "face.mp4", Path: "/tmp/face.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	detector := fakeVideoFaceDetector{
		faces: []DetectedVideoFace{
			{FrameIndex: 1, FramePosition: 3.5, X: 10, Y: 20, Width: 64, Height: 64, Score: 92, Signature: "sig-a"},
			{FrameIndex: 2, FramePosition: 9.0, X: 12, Y: 22, Width: 63, Height: 63, Score: 89, Signature: "sig-a"},
		},
	}
	svc := NewVideoFaceService(VideoFaceServiceOptions{Detector: detector})

	result, err := svc.AnalyzeVideo(context.Background(), video)
	if err != nil {
		t.Fatalf("人脸分析失败: %v", err)
	}
	if result.Status != VideoFaceAnalysisStatusCompleted || result.FaceCount != 2 || result.ClusterCount != 1 {
		t.Fatalf("分析结果错误: %+v", result)
	}
	if got := countRows(t, "video_faces"); got != 2 {
		t.Fatalf("应写入 2 条人脸，实际 %d", got)
	}
	if got := countRows(t, "face_clusters"); got != 1 {
		t.Fatalf("相同签名应归入 1 个簇，实际 %d", got)
	}
}

func TestVideoFaceServiceReanalysisDoesNotInflateClusterCounts(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "repeat.mp4", Path: "/tmp/repeat.mp4", Directory: "/tmp"}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	detector := fakeVideoFaceDetector{
		faces: []DetectedVideoFace{
			{FrameIndex: 1, X: 10, Y: 20, Width: 64, Height: 64, Score: 92, Signature: "sig-repeat"},
			{FrameIndex: 2, X: 12, Y: 22, Width: 63, Height: 63, Score: 89, Signature: "sig-repeat"},
		},
	}
	svc := NewVideoFaceService(VideoFaceServiceOptions{Detector: detector})
	if _, err := svc.AnalyzeVideo(context.Background(), video); err != nil {
		t.Fatalf("第一次分析失败: %v", err)
	}
	if _, err := svc.AnalyzeVideo(context.Background(), video); err != nil {
		t.Fatalf("第二次分析失败: %v", err)
	}

	var cluster models.FaceCluster
	if err := database.DB.Where("signature = ?", "sig-repeat").First(&cluster).Error; err != nil {
		t.Fatalf("读取人脸簇失败: %v", err)
	}
	if cluster.FaceCount != 2 {
		t.Fatalf("重复分析不应累加同一视频旧记录，实际 face_count=%d", cluster.FaceCount)
	}
	var activeFaces int64
	if err := database.DB.Model(&models.VideoFace{}).Where("video_id = ?", video.ID).Count(&activeFaces).Error; err != nil {
		t.Fatalf("统计有效人脸失败: %v", err)
	}
	if activeFaces != 2 {
		t.Fatalf("重复分析后应只有 2 条有效人脸，实际 %d", activeFaces)
	}
}

type fakeVideoFaceDetector struct {
	faces []DetectedVideoFace
	err   error
}

func (d fakeVideoFaceDetector) DetectVideoFaces(ctx context.Context, video models.Video) ([]DetectedVideoFace, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.faces, ctx.Err()
}
