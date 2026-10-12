package services

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strings"
)

// 以图搜片（2026-10-12）：拿一张图——拖进来、粘贴、选文件，或某部片某一时刻的画面——在画面索引里找相似的段。
// 与文字查询共用范围、有效段与打分（scanSceneVisualSegments），只是查询向量换成同一 Chinese-CLIP 模型的
// 图片向量。只走本地向量：外部描述没有向量，给提示不执行。上传的图片只落临时文件、用完即删，不写日志、不入库。
const (
	// SceneNoticeImageSearchUnsupported：画面来源是外部描述（只有文字描述、没有向量），以图搜片没有执行。
	SceneNoticeImageSearchUnsupported = "scene_image_search_unsupported"

	sceneImageMaxBytes = 20 << 20
	// 用某部片的画面找相似时，排除来源片子里这一刻前后 10 秒的段：否则第一条命中总是它自己。
	sceneSimilarExcludeMS = 10_000
	// 排除来源附近的段会吃掉几个名额：多取一些再截到 limit。
	sceneSimilarExtraCandidates = 16
	sceneSimilarFrameWidth      = 480
)

// ErrSceneImageInvalid：没有给图片、同时给了图片和视频画面、图片无法解码、格式不支持或超过 20 MB。
var ErrSceneImageInvalid = errors.New("scene_image_invalid")

var sceneImageTypes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true, "image/bmp": true,
}

// SceneImageSearchRequest 是 SearchScenesByImage 的参数。ImageBase64 与 VideoID 二选一。
type SceneImageSearchRequest struct {
	// ImageBase64：上传的图片（JPEG / PNG / GIF / WebP / BMP，解码后不超过 20 MB），可带 data: 前缀。
	ImageBase64 string `json:"image_base64"`
	// VideoID + AtMS：用这部片这一刻的画面找相似（命中卡片「相似画面」、播放器「找相似画面」）。
	VideoID uint           `json:"video_id"`
	AtMS    int64          `json:"at_ms"`
	Filter  *LibraryFilter `json:"filter"`
	Limit   int            `json:"limit"`
}

// SceneFrameRenderer 取某部片某一时刻的画面（JPEG 字节），由宿主接上缩略图服务的单帧渲染。
type SceneFrameRenderer func(ctx context.Context, videoID uint, ms int64, width int) ([]byte, error)

// SetFrameRenderer 接上单帧渲染；没接时用视频画面找相似会返回错误。
func (s *SceneSearchService) SetFrameRenderer(render SceneFrameRenderer) {
	s.renderFrame = render
}

// decodeSceneQueryImage 解出上传图片并校验格式与大小。
func decodeSceneQueryImage(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if comma := strings.Index(encoded, ","); strings.HasPrefix(encoded, "data:") && comma >= 0 {
		encoded = encoded[comma+1:]
	}
	if encoded == "" || base64.StdEncoding.DecodedLen(len(encoded)) > sceneImageMaxBytes+3 {
		return nil, ErrSceneImageInvalid
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > sceneImageMaxBytes {
		return nil, ErrSceneImageInvalid
	}
	if !sceneImageTypes[http.DetectContentType(data)] {
		return nil, ErrSceneImageInvalid
	}
	return data, nil
}

// SearchByImage 用一张图检索画面。返回与文字检索同形的结果，命中来源都是 visual。
func (s *SceneSearchService) SearchByImage(ctx context.Context, request SceneImageSearchRequest) (SceneSearchResult, error) {
	hasImage := strings.TrimSpace(request.ImageBase64) != ""
	if hasImage == (request.VideoID > 0) || request.AtMS < 0 {
		return SceneSearchResult{}, ErrSceneImageInvalid
	}
	var image []byte
	if hasImage {
		var err error
		if image, err = decodeSceneQueryImage(request.ImageBase64); err != nil {
			return SceneSearchResult{}, err
		}
	}
	switch {
	case request.Limit <= 0:
		request.Limit = sceneDefaultLimit
	case request.Limit > sceneMaxLimit:
		request.Limit = sceneMaxLimit
	}
	provider, modelID, intervalMS, err := s.visualModel(ctx)
	if err != nil {
		return SceneSearchResult{}, err
	}
	coverage, err := sceneCoverage(ctx, request.Filter, provider, modelID, intervalMS)
	if err != nil {
		return SceneSearchResult{}, err
	}
	result := SceneSearchResult{Hits: []SceneHit{}, Coverage: coverage, Notices: []string{}}
	switch {
	case provider == SceneProviderExternal:
		result.Notices = append(result.Notices, SceneNoticeImageSearchUnsupported)
		return result, nil
	case s.vectors == nil || s.runtimeAvailable == nil || !s.runtimeAvailable():
		result.Notices = append(result.Notices, SceneNoticeRuntimeUnavailable)
		return result, nil
	case coverage.VisualIndexed == 0:
		result.Notices = append(result.Notices, SceneNoticeVisualIndexEmpty)
		return result, nil
	}
	if !hasImage {
		if s.renderFrame == nil {
			return SceneSearchResult{}, errors.New("scene frame renderer unavailable")
		}
		if image, err = s.renderFrame(ctx, request.VideoID, request.AtMS, sceneSimilarFrameWidth); err != nil {
			return SceneSearchResult{}, err
		}
	}
	query, err := s.embedQueryImage(ctx, image)
	if err != nil {
		if ctx.Err() != nil {
			return SceneSearchResult{}, ctx.Err()
		}
		result.Notices = append(result.Notices, SceneNoticeVisualFailed)
		return result, nil
	}
	scanLimit := request.Limit
	if !hasImage {
		scanLimit += sceneSimilarExtraCandidates
	}
	candidates, err := scanSceneVisualSegments(ctx, request.Filter, modelID, intervalMS, query, scanLimit)
	if err != nil {
		return SceneSearchResult{}, err
	}
	if !hasImage {
		candidates = excludeSceneSource(candidates, request.VideoID, request.AtMS)
	}
	result.Hits = sceneVisualHits(mergeAdjacentSceneCandidates(candidates))
	if len(result.Hits) > request.Limit {
		result.Hits = result.Hits[:request.Limit]
	}
	if err := fillSceneHitTitles(ctx, result.Hits); err != nil {
		return SceneSearchResult{}, err
	}
	return result, nil
}

// embedQueryImage 把图片写进临时文件交给 worker 向量化，用完即删。
func (s *SceneSearchService) embedQueryImage(ctx context.Context, image []byte) ([]float32, error) {
	file, err := os.CreateTemp("", "cineinsight-scene-query-*")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(image); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	vectors, err := s.vectors.EmbedImages(ctx, []string{path})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, errors.New("scene image embedding missing")
	}
	return vectors[0], nil
}

// excludeSceneSource 去掉来源片子里与 [atMS-10s, atMS+10s] 重叠的段；同一部片里别处的相似镜头保留。
func excludeSceneSource(candidates []sceneVisualCandidate, videoID uint, atMS int64) []sceneVisualCandidate {
	from, to := atMS-sceneSimilarExcludeMS, atMS+sceneSimilarExcludeMS
	kept := candidates[:0]
	for _, candidate := range candidates {
		if candidate.VideoID == videoID && candidate.StartMS < to && candidate.EndMS > from {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
}

func sceneVisualHits(merged []sceneVisualCandidate) []SceneHit {
	hits := make([]SceneHit, 0, len(merged))
	for _, candidate := range merged {
		hits = append(hits, SceneHit{
			VideoID: candidate.VideoID, StartMS: candidate.StartMS, EndMS: candidate.EndMS,
			Source: SceneSourceVisual, Score: float64(candidate.Score),
		})
	}
	return hits
}
