package services

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
	"time"
)

const SceneAppleModelID = "apple-image-featureprint-r2-768-v1"
const SceneProviderApple = "apple"
const appleImageDims = 768

type sceneImageVectorSource interface {
	EmbedImages(context.Context, []string) ([][]float32, error)
}
type appleImageVectors struct{ run appleAIRunner }

func (a appleImageVectors) Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := a.run(ctx, appleAIRequest{Operation: "image_status"})
	return err == nil && r.Supported && r.ModelID == SceneAppleModelID
}
func (a appleImageVectors) EmbedImages(ctx context.Context, paths []string) ([][]float32, error) {
	if len(paths) < 1 || len(paths) > 32 {
		return nil, errors.New("apple_vision_input")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	r, err := a.run(ctx, appleAIRequest{Operation: "image_features", Paths: paths})
	if err != nil {
		return nil, err
	}
	if r.ModelID != SceneAppleModelID || len(r.Vectors) != len(paths) {
		return nil, errSceneVectorShape
	}
	out := make([][]float32, len(paths))
	for i, encoded := range r.Vectors {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, errSceneVectorShape
		}
		out[i], err = decodeAppleImageVector(data)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func encodeAppleImageVector(vector []float32) ([]byte, error) {
	if len(vector) != appleImageDims {
		return nil, errSceneVectorShape
	}
	data := make([]byte, appleImageDims*4)
	for i, v := range vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, errSceneVectorShape
		}
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(v))
	}
	return data, nil
}
func decodeAppleImageVector(data []byte) ([]float32, error) {
	if len(data) != appleImageDims*4 {
		return nil, errSceneVectorShape
	}
	out := make([]float32, appleImageDims)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
		if math.IsNaN(float64(out[i])) || math.IsInf(float64(out[i]), 0) {
			return nil, errSceneVectorShape
		}
	}
	return out, nil
}
func appleImageSimilarity(query []float32, data []byte) (float32, bool) {
	if len(query) != appleImageDims {
		return 0, false
	}
	vector, err := decodeAppleImageVector(data)
	if err != nil {
		return 0, false
	}
	var distance float64
	for i, v := range vector {
		delta := float64(v) - float64(query[i])
		distance += delta * delta
	}
	if math.IsNaN(distance) || math.IsInf(distance, 0) {
		return 0, false
	}
	return float32(1 / (1 + math.Sqrt(distance))), true
}
func sceneStateTable(modelID string) string {
	if modelID == SceneAppleModelID {
		return "scene_image_index_states"
	}
	return "scene_index_states"
}
