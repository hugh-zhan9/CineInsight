package services

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"log"
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

// jellyfinStreamSummary is what the stream snapshot contributes to the item DTO besides the
// MediaSource itself: Jellyfin repeats MediaStreams, Width/Height and HasSubtitles at the top level.
type jellyfinStreamSummary struct {
	streams      []map[string]interface{}
	width        int
	height       int
	hasSubtitles bool
}

func (s *JellyfinServer) mediaSources(r *http.Request, video models.Video) ([]map[string]interface{}, jellyfinStreamSummary, error) {
	var rows []models.MediaStream
	if err := database.DB.WithContext(r.Context()).Where("video_id = ?", video.ID).Order("stream_index").Find(&rows).Error; err != nil {
		return nil, jellyfinStreamSummary{}, err
	}
	summary := jellyfinStreamSummary{streams: []map[string]interface{}{}}
	nextIndex := 0
	var audioIndex *int
	audioDefault := false
	for _, row := range rows {
		stream := jellyfinStreamDTO(row)
		if stream == nil {
			continue
		}
		if row.StreamIndex >= nextIndex {
			nextIndex = row.StreamIndex + 1
		}
		summary.streams = append(summary.streams, stream)
		switch row.StreamType {
		case "video":
			if !row.IsAttachedPic && summary.width == 0 && row.Width != nil && row.Height != nil {
				summary.width, summary.height = *row.Width, *row.Height
			}
		case "audio":
			// Like Jellyfin, the first default-flagged track is the primary audio track.
			if audioIndex == nil || (row.IsDefault && !audioDefault) {
				index := row.StreamIndex
				audioIndex, audioDefault = &index, row.IsDefault
			}
		case "subtitle":
			summary.hasSubtitles = true
		}
	}
	if summary.width == 0 {
		summary.width, summary.height = video.Width, video.Height
	}
	id := jellyfinID(jellyVideo, video.ID)
	if info, err := os.Lstat(jellyfinSubtitlePath(video)); err == nil && info.Mode().IsRegular() {
		summary.hasSubtitles = true
		summary.streams = append(summary.streams, map[string]interface{}{"Index": nextIndex, "Type": "Subtitle", "Codec": "srt", "IsDefault": false, "IsForced": false, "IsExternal": true, "IsTextSubtitleStream": true, "SupportsExternalStream": true, "DeliveryMethod": "External", "DeliveryUrl": fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/Stream.srt", id, id, nextIndex), "DisplayTitle": "外置字幕"})
	}
	source := map[string]interface{}{"Id": id, "Name": video.Name, "Path": video.Name, "Protocol": "File", "Type": "Default", "Container": strings.TrimPrefix(strings.ToLower(filepath.Ext(video.Path)), "."), "Size": video.Size, "RunTimeTicks": int64(video.Duration * 1e7), "SupportsDirectPlay": true, "SupportsDirectStream": false, "SupportsTranscoding": false, "IsRemote": false, "RequiresOpening": false, "RequiresClosing": false, "MediaStreams": summary.streams, "DirectStreamUrl": "/Videos/" + id + "/stream?Static=true", "DefaultAudioStreamIndex": audioIndex}
	if video.Duration > 0 {
		source["Bitrate"] = int64(float64(video.Size) * 8 / video.Duration)
	}
	return []map[string]interface{}{source}, summary, nil
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
		if ok, reason := jellyfinCanDirectPlay(*video, snapshot, streams, input.DeviceProfile, input.MaxStreamingBitrate); !ok {
			log.Printf("[Jellyfin] PlaybackInfo 视频 %d 不可直放：%s", id, jellyfinLogSafe(reason, 240))
			jellyfinNoCompatibleStream(w)
			return
		}
		sources, _, err := s.mediaSources(r, *video)
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
		jellyfinServeFile(w, r, media.Path, "image/jpeg", "")
		return
	}
	if parts[0] == "items" && len(parts) == 3 && parts[2] == "download" {
		// Fileball's download/"play via path" action fetches the original through this route.
		contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(video.Path)))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		jellyfinServeFile(w, r, video.Path, contentType, filepath.Base(video.Path))
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
		jellyfinServeFile(w, r, video.Path, contentType, "")
		return
	}
	if parts[0] == "videos" && len(parts) == 6 && parts[2] == jellyfinID(jellyVideo, id) && parts[3] == "subtitles" && parts[5] == "stream.srt" {
		index, err := strconv.Atoi(parts[4])
		if err != nil {
			jellyfinError(w, 404, "字幕不存在")
			return
		}
		sources, _, err := s.mediaSources(r, *video)
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
		jellyfinServeFile(w, r, jellyfinSubtitlePath(*video), "application/x-subrip; charset=utf-8", "")
		return
	}
	jellyfinError(w, 404, "不支持的播放接口")
}

// jellyfinServeFile streams one local file; a non-empty attachment name adds a download disposition
// only once the file is known to be readable, so error bodies never carry it.
func jellyfinServeFile(w http.ResponseWriter, r *http.Request, path, contentType, attachment string) {
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
	if attachment != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment}))
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
// IsRequired defaults to true, matching Jellyfin's ProfileCondition constructor.
type jellyfinProfileCondition struct {
	Condition  string
	Property   string
	Value      string
	IsRequired *bool
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

// jellyfinCodecName folds Jellyfin's codec aliases so client profiles and ffprobe names compare.
func jellyfinCodecName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "h265" {
		return "hevc"
	}
	return name
}

// jellyfinCodecMatches follows ContainerHelper.ContainsContainer: an empty list matches
// everything and a leading "-" turns the list into an exclusion list.
func jellyfinCodecMatches(list, value string) bool {
	if list == "" {
		return true
	}
	negative := strings.HasPrefix(list, "-")
	value = jellyfinCodecName(value)
	for _, entry := range strings.Split(strings.TrimPrefix(list, "-"), ",") {
		if jellyfinCodecName(entry) == value {
			return !negative
		}
	}
	return negative
}

// jellyfinCanDirectPlay reports whether the original file satisfies the client profile and,
// when it does not, a reason built only from codec, container and condition names (D-03).
func jellyfinCanDirectPlay(video models.Video, snapshot models.VideoTechnicalMetadata, streams []models.MediaStream, profile *jellyfinDeviceProfile, maxBitrate *int64) (bool, string) {
	var mainVideo, mainAudio *models.MediaStream
	for i := range streams {
		stream := &streams[i]
		if stream.StreamType == "video" && !stream.IsAttachedPic && mainVideo == nil {
			mainVideo = stream
		}
		// Like Jellyfin, the first default-flagged track is the primary audio track.
		if stream.StreamType == "audio" && (mainAudio == nil || (stream.IsDefault && !mainAudio.IsDefault)) {
			mainAudio = stream
		}
	}
	if mainVideo == nil {
		return false, "技术快照中没有视频流"
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
			return false, fmt.Sprintf("码率 %d 不满足客户端上限 %d", bitrate, *limit)
		}
	}
	if profile == nil {
		return true, ""
	}
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(video.Path)), ".")
	audioCodec := ""
	if mainAudio != nil {
		audioCodec = mainAudio.CodecName
	}
	matched := false
	for _, direct := range profile.DirectPlayProfiles {
		if !strings.EqualFold(direct.Type, "Video") || !jellyfinCodecMatches(direct.Container, container) || !jellyfinCodecMatches(direct.VideoCodec, mainVideo.CodecName) {
			continue
		}
		if mainAudio != nil && !jellyfinCodecMatches(direct.AudioCodec, audioCodec) {
			continue
		}
		matched = true
		break
	}
	if !matched {
		return false, fmt.Sprintf("DirectPlayProfiles 不含 container=%s video=%s audio=%s", container, mainVideo.CodecName, audioCodec)
	}
	for _, restriction := range append(append([]jellyfinCodecProfile{}, profile.CodecProfiles...), profile.ContainerProfiles...) {
		if !jellyfinCodecMatches(restriction.Container, container) {
			continue
		}
		// Jellyfin consults only Video and VideoAudio profiles for video items; SubContainer only
		// replaces Container for hls transcoding profiles, so the Container check above already decides.
		stream := mainVideo
		switch {
		case strings.EqualFold(restriction.Type, "VideoAudio"):
			stream = mainAudio
		case restriction.Type == "" || strings.EqualFold(restriction.Type, "Video"):
		default:
			continue
		}
		if stream == nil || !jellyfinCodecMatches(restriction.Codec, stream.CodecName) {
			continue
		}
		// ApplyConditions decide whether the profile applies, using the same unknown-value rules.
		applies := true
		for _, condition := range restriction.ApplyConditions {
			if !jellyfinMeetsCondition(video, *stream, streams, condition) {
				applies = false
				break
			}
		}
		if !applies {
			continue
		}
		for _, condition := range restriction.Conditions {
			if !jellyfinMeetsCondition(video, *stream, streams, condition) {
				return false, fmt.Sprintf("%s profile codec=%s 条件不成立：%s %s %s", restriction.Type, stream.CodecName, condition.Property, condition.Condition, condition.Value)
			}
		}
	}
	return true, ""
}

// jellyfinMeetsCondition mirrors Jellyfin's ConditionProcessor: a value this model cannot
// determine satisfies the condition only when the client marked it IsRequired=false.
func jellyfinMeetsCondition(video models.Video, stream models.MediaStream, streams []models.MediaStream, c jellyfinProfileCondition) bool {
	required := c.IsRequired == nil || *c.IsRequired
	var value string
	switch property := strings.ToLower(c.Property); property {
	case "numaudiostreams", "numvideostreams":
		count := 0
		for _, s := range streams {
			if (property == "numaudiostreams" && s.StreamType == "audio") || (property == "numvideostreams" && s.StreamType == "video" && !s.IsAttachedPic) {
				count++
			}
		}
		value = strconv.Itoa(count)
	case "issecondaryaudio":
		// Only the default audio track is negotiated, so it is never the secondary one.
		value = "false"
	case "videorange":
		if stream.IsHDR != nil {
			value = map[bool]string{true: "HDR", false: "SDR"}[*stream.IsHDR]
		}
	case "videorangetype":
		// The HDR flavour (HDR10/HLG/DOVI) is not stored, so only SDR is a known value.
		if stream.IsHDR != nil && !*stream.IsHDR {
			value = "SDR"
		}
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
		// Properties this model does not store (IsAnamorphic, VideoLevel, …) stay unknown.
	}
	if value == "" {
		return !required
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
