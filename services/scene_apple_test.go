package services

import (
	"context"
	"errors"
	"os"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestSceneAppleIndexIndependentOfTextRuntime(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	video := seedSceneVideo(t, "v.mp4", []byte("video"), 10)
	text, _, _, _ := sceneTestVectors(1)
	seedSceneIndex(t, video, SceneLocalModelID, 5000, []sceneSegmentFixture{{StartMS: 0, EndMS: 5000, Vector: text}})
	query, _, _, _ := appleImageTestVectors(0)
	vectors := &fakeSceneVectors{image: func(string) []float32 { return query }}
	index := newTestSceneIndex(t, nil, nil, nil, false)
	index.imageAvailable = func() bool { return true }
	index.imageVectors = vectors
	index.extractImages = func(context.Context, string, string, int64) ([]string, error) {
		return []string{"frame1", "frame2"}, nil
	}
	if _, err := index.Start(context.Background(), SceneIndexRequest{VideoIDs: []uint{video.ID}, Provider: SceneProviderApple}); err != nil {
		t.Fatal(err)
	}
	status := waitSceneIndex(t, index, func(s SceneIndexStatus) bool { return !s.Running })
	if status.Succeeded != 1 {
		t.Fatal(status)
	}
	if countFaceRows(t, &models.SceneVisualSegment{}, "model_id = ?", SceneLocalModelID) != 1 {
		t.Fatal("Apple build deleted text segments")
	}
	var textState models.SceneIndexState
	if err := database.DB.First(&textState, video.ID).Error; err != nil || textState.ModelID != SceneLocalModelID {
		t.Fatal("text state overwritten", err)
	}
	search := NewSceneSearchService(index)
	coverage, err := search.GetCoverage(context.Background(), nil)
	if err != nil || coverage.VisualIndexed != 1 || coverage.ImageIndexed != 1 {
		t.Fatal(coverage, err)
	}
	before := countFaceRows(t, &models.SceneVisualSegment{}, "model_id = ?", SceneAppleModelID)
	if before != 2 {
		t.Fatal("Apple frames must not use CLIP merging threshold", before)
	}
	item := sceneIndexItem{VideoID: video.ID, Provider: SceneProviderLocal, ModelID: SceneLocalModelID, IntervalMS: 5000}
	info, _ := os.Stat(video.Path)
	if err := index.replaceVideoSegments(context.Background(), item, info, []sceneSegmentDraft{{StartMS: 0, EndMS: 5000, Vector: text}}); err != nil {
		t.Fatal(err)
	}
	if countFaceRows(t, &models.SceneVisualSegment{}, "model_id = ?", SceneAppleModelID) != before {
		t.Fatal("text rebuild deleted Apple")
	}
	item.Provider = SceneProviderApple
	item.ModelID = SceneAppleModelID
	index.recordStateFailure(context.Background(), item, video.Size, info.ModTime().UnixNano(), errors.New("test"))
	if countFaceRows(t, &models.SceneVisualSegment{}, "model_id = ?", SceneLocalModelID) != 1 {
		t.Fatal("Apple failure deleted text")
	}
}
func TestSceneAppleEuclideanAndInvalidVectors(t *testing.T) {
	query := make([]float32, appleImageDims)
	near := make([]float32, appleImageDims)
	far := make([]float32, appleImageDims)
	query[0] = 2
	near[0] = 2.1
	far[0] = 4
	a, _ := encodeAppleImageVector(near)
	b, _ := encodeAppleImageVector(far)
	sa, ok := appleImageSimilarity(query, a)
	sb, _ := appleImageSimilarity(query, b)
	if !ok || sa <= sb {
		t.Fatal("must rank by Euclidean distance, not dot product", sa, sb)
	}
	if _, ok := appleImageSimilarity(query, make([]byte, sceneQuantizedBytes)); ok {
		t.Fatal("accepted CLIP bytes")
	}
}
