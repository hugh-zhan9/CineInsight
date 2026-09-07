package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
)

func jellyfinSubtitlePath(video models.Video) string {
	return strings.TrimSuffix(video.Path, filepath.Ext(video.Path)) + ".srt"
}

func (s *JellyfinServer) mediaSources(r *http.Request, video models.Video) ([]map[string]interface{}, error) {
	var rows []models.MediaStream
	if err := database.DB.WithContext(r.Context()).Where("video_id = ?", video.ID).Order("stream_index").Find(&rows).Error; err != nil {
		return nil, err
	}
	streams := []map[string]interface{}{}
	nextIndex := 0
	var audioIndex *int
	for _, row := range rows {
		streamType := map[string]string{"video": "Video", "audio": "Audio", "subtitle": "Subtitle"}[row.StreamType]
		if streamType == "" {
			continue
		}
		if row.StreamIndex >= nextIndex {
			nextIndex = row.StreamIndex + 1
		}
		stream := map[string]interface{}{"Index": row.StreamIndex, "Type": streamType, "Codec": row.CodecName, "Language": row.Language, "Title": row.Title, "DisplayTitle": row.Title, "IsDefault": row.IsDefault, "IsExternal": false, "IsTextSubtitleStream": streamType == "Subtitle" && (row.CodecName == "subrip" || row.CodecName == "ass" || row.CodecName == "webvtt"), "Width": row.Width, "Height": row.Height, "Channels": row.Channels, "SampleRate": row.SampleRate, "BitRate": row.BitRate, "Profile": row.Profile, "PixelFormat": row.PixelFormat}
		streams = append(streams, stream)
		if streamType == "Audio" && (audioIndex == nil || row.IsDefault) {
			index := row.StreamIndex
			audioIndex = &index
		}
	}
	id := jellyfinID(jellyVideo, video.ID)
	if info, err := os.Lstat(jellyfinSubtitlePath(video)); err == nil && info.Mode().IsRegular() {
		streams = append(streams, map[string]interface{}{"Index": nextIndex, "Type": "Subtitle", "Codec": "srt", "IsDefault": false, "IsExternal": true, "IsTextSubtitleStream": true, "SupportsExternalStream": true, "DeliveryMethod": "External", "DeliveryUrl": fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/Stream.srt", id, id, nextIndex), "DisplayTitle": "外置字幕"})
	}
	source := map[string]interface{}{"Id": id, "Name": video.Name, "Path": video.Name, "Protocol": "File", "Type": "Default", "Container": strings.TrimPrefix(strings.ToLower(filepath.Ext(video.Path)), "."), "Size": video.Size, "RunTimeTicks": int64(video.Duration * 1e7), "SupportsDirectPlay": true, "SupportsDirectStream": false, "SupportsTranscoding": false, "IsRemote": false, "RequiresOpening": false, "RequiresClosing": false, "MediaStreams": streams, "DirectStreamUrl": "/Videos/" + id + "/stream?Static=true", "DefaultAudioStreamIndex": audioIndex}
	if video.Duration > 0 {
		source["Bitrate"] = int64(float64(video.Size) * 8 / video.Duration)
	}
	return []map[string]interface{}{source}, nil
}

func (s *JellyfinServer) servePlayback(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) >= 2 && parts[0] == "sessions" {
		s.playbackProgress(w, r, parts)
		return
	}
	if len(parts) < 3 {
		jellyfinError(w, 404, "资源不存在")
		return
	}
	kind, id, err := jellyfinParseID(parts[1])
	if err != nil || kind != jellyVideo {
		jellyfinError(w, 404, "视频不存在")
		return
	}
	video, err := s.visibleVideo(r, id)
	if s.libraryError(w, err) {
		return
	}
	if parts[0] == "items" && len(parts) == 3 && parts[2] == "playbackinfo" {
		if r.Method != "POST" && r.Method != "GET" {
			jellyfinError(w, 405, "方法不支持")
			return
		}
		var input struct {
			UserId              string
			EnableDirectPlay    *bool
			MediaSourceId       string
			MaxStreamingBitrate *int64
			DeviceProfile       *jellyfinDeviceProfile
		}
		if r.Method == "POST" && r.ContentLength != 0 {
			if !jellyfinDecode(w, r, &input) {
				return
			}
			if input.UserId != "" && strings.ReplaceAll(input.UserId, "-", "") != jellyfinUserID {
				jellyfinError(w, 403, "用户无效")
				return
			}
			if input.MediaSourceId != "" && input.MediaSourceId != jellyfinID(jellyVideo, id) {
				jellyfinError(w, 400, "媒体源无效")
				return
			}
		}
		if raw := jellyfinParam(r.URL.Query(), "EnableDirectPlay"); raw != "" {
			value, err := strconv.ParseBool(raw)
			if err != nil {
				jellyfinError(w, 400, "直播放参数无效")
				return
			}
			input.EnableDirectPlay = &value
		}
		if raw := jellyfinParam(r.URL.Query(), "MaxStreamingBitrate"); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 0 {
				jellyfinError(w, 400, "码率参数无效")
				return
			}
			input.MaxStreamingBitrate = &value
		}
		if input.EnableDirectPlay != nil && !*input.EnableDirectPlay {
			jellyfinNoCompatibleStream(w)
			return
		}
		info, err := os.Stat(video.Path)
		if err != nil || !info.Mode().IsRegular() {
			jellyfinError(w, 404, "视频文件不可用")
			return
		}
		var snapshot models.VideoTechnicalMetadata
		probeErr := database.DB.WithContext(r.Context()).First(&snapshot, "video_id = ?", id).Error
		if probeErr != nil && !errors.Is(probeErr, gorm.ErrRecordNotFound) {
			s.libraryError(w, probeErr)
			return
		}
		fresh := probeErr == nil && snapshot.SuccessfulSourceSize != nil && snapshot.SuccessfulSourceModTimeNS != nil && *snapshot.SuccessfulSourceSize == info.Size() && *snapshot.SuccessfulSourceModTimeNS == info.ModTime().UnixNano()
		if !fresh {
			if s.probe == nil {
				jellyfinError(w, 503, "视频技术信息不可用")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			if err := s.probe.Refresh(ctx, id); err != nil {
				jellyfinError(w, 503, "无法读取原片技术信息，请在桌面检查 ffprobe")
				return
			}
			video, err = s.visibleVideo(r, id)
			if s.libraryError(w, err) {
				return
			}
			if err := database.DB.WithContext(r.Context()).First(&snapshot, "video_id = ?", id).Error; s.libraryError(w, err) {
				return
			}
		}
		var streams []models.MediaStream
		if err := database.DB.WithContext(r.Context()).Where("video_id = ?", id).Order("stream_index").Find(&streams).Error; s.libraryError(w, err) {
			return
		}
		if !jellyfinCanDirectPlay(*video, snapshot, streams, input.DeviceProfile, input.MaxStreamingBitrate) {
			jellyfinNoCompatibleStream(w)
			return
		}
		sources, err := s.mediaSources(r, *video)
		if s.libraryError(w, err) {
			return
		}
		playID, err := jellyfinRandom(16)
		if s.libraryError(w, err) {
			return
		}
		jellyfinJSON(w, map[string]interface{}{"MediaSources": sources, "PlaySessionId": playID})
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		jellyfinError(w, 405, "方法不支持")
		return
	}
	if parts[0] == "items" && (len(parts) == 4 || len(parts) == 5) && parts[2] == "images" && parts[3] == "primary" {
		if s.thumbnail == nil {
			jellyfinError(w, 404, "封面不可用")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		media, err := s.thumbnail.ResolveThumbnail(ctx, id)
		if err != nil {
			jellyfinError(w, 404, "封面不可用")
			return
		}
		jellyfinServeFile(w, r, media.Path, "image/jpeg")
		return
	}
	if parts[0] == "videos" && len(parts) == 3 && (parts[2] == "stream" || strings.HasPrefix(parts[2], "stream.")) {
		if parts[2] != "stream" && parts[2] != "stream"+strings.ToLower(filepath.Ext(video.Path)) {
			jellyfinError(w, 400, "此服务不提供转封装或转码")
			return
		}
		if raw := jellyfinParam(r.URL.Query(), "MediaSourceId"); raw != "" && raw != jellyfinID(jellyVideo, id) {
			jellyfinError(w, 400, "媒体源无效")
			return
		}
		if raw := jellyfinParam(r.URL.Query(), "Static"); raw != "" && !strings.EqualFold(raw, "true") {
			jellyfinError(w, 400, "仅支持原片直播放")
			return
		}
		contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(video.Path)))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		jellyfinServeFile(w, r, video.Path, contentType)
		return
	}
	if parts[0] == "videos" && len(parts) == 6 && parts[2] == jellyfinID(jellyVideo, id) && parts[3] == "subtitles" && parts[5] == "stream.srt" {
		index, err := strconv.Atoi(parts[4])
		if err != nil {
			jellyfinError(w, 404, "字幕不存在")
			return
		}
		sources, err := s.mediaSources(r, *video)
		if s.libraryError(w, err) {
			return
		}
		valid := false
		for _, stream := range sources[0]["MediaStreams"].([]map[string]interface{}) {
			if stream["IsExternal"] == true && stream["Index"] == index {
				valid = true
			}
		}
		if !valid {
			jellyfinError(w, 404, "字幕不存在")
			return
		}
		jellyfinServeFile(w, r, jellyfinSubtitlePath(*video), "application/x-subrip; charset=utf-8")
		return
	}
	jellyfinError(w, 404, "不支持的播放接口")
}

func jellyfinServeFile(w http.ResponseWriter, r *http.Request, path, contentType string) {
	file, err := os.Open(path)
	if err != nil {
		jellyfinError(w, 404, "媒体文件不可用")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		jellyfinError(w, 404, "媒体文件不可用")
		return
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (s *JellyfinServer) mutateUserData(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 4 || (r.Method != "POST" && r.Method != "DELETE") {
		jellyfinError(w, 405, "方法不支持")
		return
	}
	kind, id, err := jellyfinParseID(parts[3])
	if err != nil || kind != jellyVideo {
		jellyfinError(w, 404, "视频不存在")
		return
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	identity, _ := r.Context().Value(jellyfinIdentityKey{}).(jellyfinIdentity)
	if !s.authorized(identity) {
		jellyfinError(w, 401, "会话已失效")
		return
	}
	if _, err := s.visibleVideo(r, id); s.libraryError(w, err) {
		return
	}
	var video *models.Video
	if parts[2] == "favoriteitems" {
		video, err = s.video.SetVideoFavorite(id, r.Method == "POST")
	} else {
		video, err = s.video.SetVideoWatched(id, r.Method == "POST")
	}
	if s.libraryError(w, err) {
		return
	}
	jellyfinJSON(w, jellyfinUserData(*video))
}

func (s *JellyfinServer) playbackProgress(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != "POST" || !(len(parts) == 2 && parts[1] == "playing" || len(parts) == 3 && parts[1] == "playing" && (parts[2] == "progress" || parts[2] == "stopped")) {
		jellyfinError(w, 404, "不支持的会话接口")
		return
	}
	var input struct {
		ItemId        string
		PositionTicks *int64
	}
	if !jellyfinDecode(w, r, &input) {
		return
	}
	kind, id, err := jellyfinParseID(input.ItemId)
	if err != nil || kind != jellyVideo {
		jellyfinError(w, 400, "视频 ID 无效")
		return
	}
	if input.PositionTicks != nil && *input.PositionTicks < 0 {
		jellyfinError(w, 400, "观看进度不能为负数")
		return
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	identity, _ := r.Context().Value(jellyfinIdentityKey{}).(jellyfinIdentity)
	if !s.authorized(identity) {
		jellyfinError(w, 401, "会话已失效")
		return
	}
	video, err := s.visibleVideo(r, id)
	if s.libraryError(w, err) {
		return
	}
	if input.PositionTicks != nil {
		seconds := float64(*input.PositionTicks) / 1e7
		completed := video.Duration > 0 && seconds >= video.Duration
		if _, err = s.video.UpdateVideoWatchProgress(id, seconds, completed); s.libraryError(w, err) {
			return
		}
	}
	w.WriteHeader(204)
}

// Profile negotiation is deliberately limited to original files. An unknown
// condition is not evidence that the client can decode the source.
type jellyfinProfileCondition struct {
	Condition  string
	Property   string
	Value      string
	IsRequired bool
}
type jellyfinCodecProfile struct {
	Type            string
	Codec           string
	Container       string
	SubContainer    string
	Conditions      []jellyfinProfileCondition
	ApplyConditions []jellyfinProfileCondition
}
type jellyfinDeviceProfile struct {
	MaxStaticBitrate    *int64
	MaxStreamingBitrate *int64
	DirectPlayProfiles  []struct{ Type, Container, VideoCodec, AudioCodec string }
	CodecProfiles       []jellyfinCodecProfile
	ContainerProfiles   []jellyfinCodecProfile
}

func jellyfinNoCompatibleStream(w http.ResponseWriter) {
	jellyfinJSON(w, map[string]interface{}{"MediaSources": []interface{}{}, "ErrorCode": "NoCompatibleStream"})
}
func jellyfinCodecMatches(list, value string) bool {
	return list == "" || jellyfinCSVContains(list, value)
}
func jellyfinCanDirectPlay(video models.Video, snapshot models.VideoTechnicalMetadata, streams []models.MediaStream, profile *jellyfinDeviceProfile, maxBitrate *int64) bool {
	var mainVideo, mainAudio *models.MediaStream
	for i := range streams {
		stream := &streams[i]
		if stream.StreamType == "video" && !stream.IsAttachedPic && mainVideo == nil {
			mainVideo = stream
		}
		if stream.StreamType == "audio" && (mainAudio == nil || stream.IsDefault) {
			mainAudio = stream
		}
	}
	if mainVideo == nil {
		return false
	}
	bitrate := int64(0)
	if snapshot.TotalBitRate != nil {
		bitrate = *snapshot.TotalBitRate
	} else if video.Duration > 0 {
		bitrate = int64(float64(video.Size) * 8 / video.Duration)
	}
	limits := []*int64{maxBitrate}
	if profile != nil {
		limits = append(limits, profile.MaxStaticBitrate, profile.MaxStreamingBitrate)
	}
	for _, limit := range limits {
		if limit != nil && (*limit < 0 || (*limit > 0 && (bitrate <= 0 || bitrate > *limit))) {
			return false
		}
	}
	if profile == nil {
		return true
	}
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(video.Path)), ".")
	matched := false
	for _, direct := range profile.DirectPlayProfiles {
		if !strings.EqualFold(direct.Type, "Video") || !jellyfinCodecMatches(direct.Container, container) || !jellyfinCodecMatches(direct.VideoCodec, mainVideo.CodecName) {
			continue
		}
		if mainAudio != nil && !jellyfinCodecMatches(direct.AudioCodec, mainAudio.CodecName) {
			continue
		}
		matched = true
		break
	}
	if !matched {
		return false
	}
	for _, restriction := range append(append([]jellyfinCodecProfile{}, profile.CodecProfiles...), profile.ContainerProfiles...) {
		if !jellyfinCodecMatches(restriction.Container, container) {
			continue
		}
		stream := mainVideo
		if strings.EqualFold(restriction.Type, "VideoAudio") || strings.EqualFold(restriction.Type, "Audio") {
			stream = mainAudio
		}
		if stream == nil || !jellyfinCodecMatches(restriction.Codec, stream.CodecName) {
			continue
		}
		if restriction.SubContainer != "" {
			return false
		}
		// Conditional-profile predicates require metadata that this model may not store.
		// Unsupported predicates must fail closed rather than silently skipping a limit.
		if len(restriction.ApplyConditions) > 0 {
			return false
		}
		for _, condition := range restriction.Conditions {
			if !jellyfinMeetsCondition(video, *stream, condition) {
				return false
			}
		}
	}
	return true
}
func jellyfinMeetsCondition(video models.Video, stream models.MediaStream, c jellyfinProfileCondition) bool {
	var value string
	switch strings.ToLower(c.Property) {
	case "width":
		if stream.Width != nil {
			value = strconv.Itoa(*stream.Width)
		} else if video.Width > 0 {
			value = strconv.Itoa(video.Width)
		}
	case "height":
		if stream.Height != nil {
			value = strconv.Itoa(*stream.Height)
		} else if video.Height > 0 {
			value = strconv.Itoa(video.Height)
		}
	case "audiobitrate", "videobitrate":
		if stream.BitRate != nil {
			value = strconv.FormatInt(*stream.BitRate, 10)
		}
	case "audiochannels":
		if stream.Channels != nil {
			value = strconv.Itoa(*stream.Channels)
		}
	case "audiosamplerate":
		if stream.SampleRate != nil {
			value = strconv.FormatInt(*stream.SampleRate, 10)
		}
	case "videobitdepth":
		if stream.BitsPerRawSample != nil {
			value = strconv.Itoa(*stream.BitsPerRawSample)
		}
	case "videoprofile":
		value = stream.Profile
	case "videoframerate":
		numerator, denominator, ok := strings.Cut(stream.AvgFrameRate, "/")
		if ok {
			a, e1 := strconv.ParseFloat(numerator, 64)
			b, e2 := strconv.ParseFloat(denominator, 64)
			if e1 == nil && e2 == nil && b > 0 {
				value = strconv.FormatFloat(a/b, 'f', 6, 64)
			}
		}
	default:
		return false
	}
	if value == "" {
		return !c.IsRequired
	}
	switch strings.ToLower(c.Condition) {
	case "equals":
		return strings.EqualFold(value, c.Value)
	case "notequals":
		return !strings.EqualFold(value, c.Value)
	case "equalsany":
		for _, expected := range strings.Split(c.Value, "|") {
			if strings.EqualFold(value, expected) {
				return true
			}
		}
		return false
	case "lessthanequal", "greaterthanequal":
		a, e1 := strconv.ParseFloat(value, 64)
		b, e2 := strconv.ParseFloat(c.Value, 64)
		if e1 != nil || e2 != nil || math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) {
			return false
		}
		if strings.EqualFold(c.Condition, "LessThanEqual") {
			return a <= b
		}
		return a >= b
	default:
		return false
	}
}
