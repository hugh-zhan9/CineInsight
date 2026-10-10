package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"video-master/models"
	"video-master/services/subtitleparser"
)

// 预检（视频编辑合同「预检、规格与轨道映射」）：每个来源跑一次有界 ffprobe（30 秒期限、输出上限与
// MediaProbeService 相同），不写技术快照表。

const videoEditProbeTimeout = 30 * time.Second

// editStream 是预检读到的一条流。
type editStream struct {
	Index          int
	Type           string
	Codec          string
	Profile        string
	Width          int
	Height         int
	PixFmt         string
	FrameRate      string
	TimeBase       string
	SampleRate     int
	Channels       int
	ChannelLayout  string
	BitRate        int64
	ColorPrimaries string
	ColorTransfer  string
	ColorSpace     string
	ColorRange     string
	Language       string
	Title          string
	Filename       string
	MimeType       string
	Default        bool
	DolbyVision    bool
}

// editProbe 是一个来源的探测结果。
type editProbe struct {
	FormatName string
	// StartSeconds 是容器的 start_time：ffmpeg 在 -i 之前的 -ss 相对它，而 ffprobe 的 pts_time 是绝对时间戳。
	StartSeconds float64
	DurationMS   int64
	BitRate      int64
	Video        *editStream
	Audio        []editStream
	Subtitles    []editStream
	Attachments  []editStream
}

type editProbeJSON struct {
	Streams []struct {
		Index          int    `json:"index"`
		CodecType      string `json:"codec_type"`
		CodecName      string `json:"codec_name"`
		Profile        string `json:"profile"`
		Width          int    `json:"width"`
		Height         int    `json:"height"`
		PixFmt         string `json:"pix_fmt"`
		RFrameRate     string `json:"r_frame_rate"`
		AvgFrameRate   string `json:"avg_frame_rate"`
		TimeBase       string `json:"time_base"`
		SampleRate     string `json:"sample_rate"`
		Channels       int    `json:"channels"`
		ChannelLayout  string `json:"channel_layout"`
		BitRate        string `json:"bit_rate"`
		ColorPrimaries string `json:"color_primaries"`
		ColorTransfer  string `json:"color_transfer"`
		ColorSpace     string `json:"color_space"`
		ColorRange     string `json:"color_range"`
		Tags           struct {
			Language string `json:"language"`
			Title    string `json:"title"`
			Filename string `json:"filename"`
			MimeType string `json:"mimetype"`
		} `json:"tags"`
		Disposition struct {
			Default     int `json:"default"`
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
		SideDataList []struct {
			SideDataType string `json:"side_data_type"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
		StartTime  string `json:"start_time"`
	} `json:"format"`
}

// parseEditProbe 解析 ffprobe -show_streams -show_format 的 JSON。主视频流取第一条非封面视频流。
func parseEditProbe(raw []byte) (editProbe, error) {
	var payload editProbeJSON
	if len(raw) == 0 {
		return editProbe{}, fmt.Errorf("ffprobe 输出为空")
	}
	if len(raw) > mediaProbeMaxOutputBytes {
		return editProbe{}, ErrMediaProbeOutputTooLarge
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return editProbe{}, fmt.Errorf("ffprobe 输出不可解析")
	}
	probe := editProbe{FormatName: payload.Format.FormatName}
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(payload.Format.Duration), 64); err == nil && seconds > 0 {
		probe.DurationMS = int64(math.Round(seconds * 1000))
	}
	probe.BitRate, _ = strconv.ParseInt(strings.TrimSpace(payload.Format.BitRate), 10, 64)
	if start, err := strconv.ParseFloat(strings.TrimSpace(payload.Format.StartTime), 64); err == nil && start > 0 && !math.IsInf(start, 0) {
		probe.StartSeconds = start
	}
	for _, raw := range payload.Streams {
		stream := editStream{
			Index: raw.Index, Type: raw.CodecType, Codec: raw.CodecName, Profile: raw.Profile,
			Width: raw.Width, Height: raw.Height, PixFmt: raw.PixFmt, FrameRate: raw.RFrameRate, TimeBase: raw.TimeBase,
			Channels: raw.Channels, ChannelLayout: raw.ChannelLayout,
			ColorPrimaries: raw.ColorPrimaries, ColorTransfer: raw.ColorTransfer, ColorSpace: raw.ColorSpace, ColorRange: raw.ColorRange,
			Language: raw.Tags.Language, Title: raw.Tags.Title, Filename: raw.Tags.Filename, MimeType: raw.Tags.MimeType,
			Default: raw.Disposition.Default == 1,
		}
		if editFrameRateValue(stream.FrameRate) <= 0 || editFrameRateValue(stream.FrameRate) > 240 {
			stream.FrameRate = raw.AvgFrameRate
		}
		stream.SampleRate, _ = strconv.Atoi(raw.SampleRate)
		stream.BitRate, _ = strconv.ParseInt(raw.BitRate, 10, 64)
		for _, side := range raw.SideDataList {
			if strings.Contains(strings.ToLower(side.SideDataType), "dovi") || strings.Contains(strings.ToLower(side.SideDataType), "dolby vision") {
				stream.DolbyVision = true
			}
		}
		switch raw.CodecType {
		case "video":
			if raw.Disposition.AttachedPic == 1 || probe.Video != nil {
				continue
			}
			copied := stream
			probe.Video = &copied
		case "audio":
			probe.Audio = append(probe.Audio, stream)
		case "subtitle":
			probe.Subtitles = append(probe.Subtitles, stream)
		case "attachment":
			probe.Attachments = append(probe.Attachments, stream)
		}
	}
	return probe, nil
}

// editFrameRateValue 把 "24000/1001" 这类有理数转成浮点；不可解析时为 0。
func editFrameRateValue(rate string) float64 {
	numerator, denominator, found := strings.Cut(strings.TrimSpace(rate), "/")
	n, err := strconv.ParseFloat(numerator, 64)
	if err != nil {
		return 0
	}
	if !found {
		return n
	}
	d, err := strconv.ParseFloat(denominator, 64)
	if err != nil || d == 0 {
		return 0
	}
	return n / d
}

func editStreamHDR(stream *editStream) bool {
	if stream == nil {
		return false
	}
	hdr := deriveHDR(stream.ColorTransfer, stream.ColorPrimaries)
	return hdr != nil && *hdr
}

func editImageSubtitle(codec string) bool {
	switch codec {
	case "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "xsub", "dvb_teletext":
		return true
	}
	return false
}

// editTextSubtitleTarget 是精确模式下文本字幕在 mkv 里的统一编码。
func editTextSubtitleTarget(codec string) string {
	switch codec {
	case "ass", "ssa":
		return "ass"
	case "webvtt":
		return "webvtt"
	default:
		return "srt"
	}
}

func editFontAttachment(stream editStream) bool {
	mime := strings.ToLower(stream.MimeType)
	name := strings.ToLower(stream.Filename)
	if strings.Contains(mime, "font") || strings.Contains(mime, "truetype") || strings.Contains(mime, "opentype") {
		return true
	}
	for _, ext := range []string{".ttf", ".otf", ".ttc", ".woff", ".woff2"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

func (s *VideoEditService) probeEditSource(ctx context.Context, path string) (editProbe, error) {
	ffprobe, err := s.findFFprobe()
	if err != nil {
		return editProbe{}, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, videoEditProbeTimeout)
	defer cancel()
	stdout, stderr, err := s.runOutput(probeCtx, ffprobe, []string{"-v", "error", "-show_streams", "-show_format", "-of", "json", path})
	if err != nil {
		// stderr 原样带回（已限 2 KB），由调用方按已知路径擦除。
		return editProbe{}, fmt.Errorf("%v: %s", err, stderr)
	}
	return parseEditProbe([]byte(stdout))
}

// editFileFingerprint 是来源的 size 与 mtime（纳秒），与技术快照同一口径。片库来源可以是指向普通文件的
// 符号链接：指纹取解析后的文件（链接被改指向别的文件时 size/mtime 随之变化，报 source_changed）；
// 编辑服务从不写来源，所以跟随链接只是读。
func editFileFingerprint(path string) (int64, int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, 0, fmt.Errorf("不是常规文件")
	}
	return info.Size(), info.ModTime().UnixNano(), nil
}

// EditIssue 是预检的一条警告或错误。key 稳定且唯一：排队时 acknowledged_warnings 按 key 确认；
// 切点、规格等会变的内容带进 key（例如快速模式切点的摘要），内容变了旧确认自动失效。
type EditIssue struct {
	Code    string `json:"code"`
	Key     string `json:"key"`
	Message string `json:"message"`
	VideoID uint   `json:"video_id"`
	Seq     int    `json:"seq"`
}

// EditStreamInfo 是预检展示的一条来源流。
type EditStreamInfo struct {
	Index          int    `json:"index"`
	Codec          string `json:"codec"`
	Profile        string `json:"profile"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	FrameRate      string `json:"frame_rate"`
	PixFmt         string `json:"pix_fmt"`
	ColorPrimaries string `json:"color_primaries"`
	ColorTransfer  string `json:"color_transfer"`
	ColorSpace     string `json:"color_space"`
	BitRate        int64  `json:"bit_rate"`
	Language       string `json:"language"`
	Title          string `json:"title"`
	Channels       int    `json:"channels"`
	ChannelLayout  string `json:"channel_layout"`
	SampleRate     int    `json:"sample_rate"`
	Image          bool   `json:"image"`
	Default        bool   `json:"default"`
}

// EditPreflightSource 是一个来源的探测摘要。error 非空时其余字段可能为空。
type EditPreflightSource struct {
	VideoID         uint             `json:"video_id"`
	Name            string           `json:"name"`
	DurationMS      int64            `json:"duration_ms"`
	Container       string           `json:"container"`
	Size            int64            `json:"size"`
	Video           *EditStreamInfo  `json:"video"`
	Audio           []EditStreamInfo `json:"audio"`
	Subtitles       []EditStreamInfo `json:"subtitles"`
	Attachments     int              `json:"attachments"`
	FontAttachments int              `json:"font_attachments"`
	SidecarSRT      bool             `json:"sidecar_srt"`
	SidecarOther    []string         `json:"sidecar_other"`
	HDR             bool             `json:"hdr"`
	DolbyVision     bool             `json:"dolby_vision"`
	Error           string           `json:"error"`
}

// EditSpecOption 是合并时可选的输出规格来源。
type EditSpecOption struct {
	VideoID   uint   `json:"video_id"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	FrameRate string `json:"frame_rate"`
	HDR       bool   `json:"hdr"`
	Selected  bool   `json:"selected"`
}

// EditTrackMapping 是输出轨在某个来源上的对应：stream_index 为 -1 表示填充或未解决。
type EditTrackMapping struct {
	VideoID     uint   `json:"video_id"`
	Role        string `json:"role"`
	StreamIndex int    `json:"stream_index"`
	Fill        string `json:"fill"`
	Explicit    bool   `json:"explicit"`
	Status      string `json:"status"`
}

// EditOutputTrack 是一条输出音轨/字幕轨（output_index 按参照来源的流顺序，与配方 tracks[].output 对应）。
type EditOutputTrack struct {
	OutputIndex int                `json:"output_index"`
	Language    string             `json:"language"`
	Title       string             `json:"title"`
	Codec       string             `json:"codec"`
	Channels    int                `json:"channels"`
	Image       bool               `json:"image"`
	Dropped     bool               `json:"dropped"`
	Mappings    []EditTrackMapping `json:"mappings"`
}

// EditTrackConflict 是无法按语言+序号自动对应的一处映射，须在配方 tracks 里明确选择。
type EditTrackConflict struct {
	Seq          int              `json:"seq"`
	Kind         string           `json:"kind"`
	OutputIndex  int              `json:"output_index"`
	VideoID      uint             `json:"video_id"`
	Reason       string           `json:"reason"`
	Candidates   []EditStreamInfo `json:"candidates"`
	AllowedFills []string         `json:"allowed_fills"`
}

// EditCutPoint 是快速模式的一个切点：请求时间与实际取到的关键帧时间。
type EditCutPoint struct {
	Seq         int   `json:"seq"`
	Segment     int   `json:"segment"`
	VideoID     uint  `json:"video_id"`
	RequestedMS int64 `json:"requested_ms"`
	ActualMS    int64 `json:"actual_ms"`
}

// EditFastInfo：available=false 时 reasons 说明原因；可用时列出每个切点。
type EditFastInfo struct {
	Available bool           `json:"available"`
	Reasons   []string       `json:"reasons"`
	CutPoints []EditCutPoint `json:"cut_points"`
}

// EditOutputSegment 是成品时间线上的一段（out_ms 起，取自 video_id 的 in_ms，时长 duration_ms）。
type EditOutputSegment struct {
	VideoID       uint  `json:"video_id"`
	InMS          int64 `json:"in_ms"`
	RequestedInMS int64 `json:"requested_in_ms"`
	DurationMS    int64 `json:"duration_ms"`
	OutMS         int64 `json:"out_ms"`
	Replaced      bool  `json:"replaced"`
}

// EditPreflightOutput 是一个导出项的输出规格（merge/hd_replace 一项，trim_intro 每个来源一项）。
type EditPreflightOutput struct {
	Seq            int                 `json:"seq"`
	VideoIDs       []uint              `json:"video_ids"`
	PlannedName    string              `json:"planned_name"`
	Container      string              `json:"container"`
	VideoCodec     string              `json:"video_codec"`
	Encoder        string              `json:"encoder"`
	Width          int                 `json:"width"`
	Height         int                 `json:"height"`
	FrameRate      string              `json:"frame_rate"`
	PixFmt         string              `json:"pix_fmt"`
	ColorPrimaries string              `json:"color_primaries"`
	ColorTransfer  string              `json:"color_transfer"`
	ColorSpace     string              `json:"color_space"`
	HDR            bool                `json:"hdr"`
	BitRate        int64               `json:"bit_rate"`
	AudioCodec     string              `json:"audio_codec"`
	AudioTracks    []EditOutputTrack   `json:"audio_tracks"`
	SubtitleTracks []EditOutputTrack   `json:"subtitle_tracks"`
	Attachments    int                 `json:"attachments"`
	Sidecar        string              `json:"sidecar"`
	DurationMS     int64               `json:"duration_ms"`
	EstimatedBytes int64               `json:"estimated_bytes"`
	WorkBytes      int64               `json:"work_bytes"`
	FreeBytes      int64               `json:"free_bytes"`
	Segments       []EditOutputSegment `json:"segments"`
}

// EditPreflight 是 PreflightEditProject 的结果。ready=errors 为空；排队还要求每条 warning 都被确认。
type EditPreflight struct {
	ProjectID   uint                  `json:"project_id"`
	Revision    uint64                `json:"revision"`
	Kind        string                `json:"kind"`
	Mode        string                `json:"mode"`
	Sources     []EditPreflightSource `json:"sources"`
	SpecOptions []EditSpecOption      `json:"spec_options"`
	Outputs     []EditPreflightOutput `json:"outputs"`
	Conflicts   []EditTrackConflict   `json:"conflicts"`
	Fast        EditFastInfo          `json:"fast"`
	Warnings    []EditIssue           `json:"warnings"`
	Errors      []EditIssue           `json:"errors"`
	Ready       bool                  `json:"ready"`

	// trackOutputs 在构建计划时记下每个导出项的输出轨映射，finishEditOutputs 写进 outputs。
	trackOutputs map[int][2][]EditOutputTrack
}

func (p *EditPreflight) addError(code, key string, videoID uint, seq int, format string, args ...any) {
	for _, existing := range p.Errors {
		if existing.Key == key {
			return
		}
	}
	p.Errors = append(p.Errors, EditIssue{Code: code, Key: key, VideoID: videoID, Seq: seq, Message: fmt.Sprintf(format, args...)})
}

func (p *EditPreflight) addWarning(code, key string, videoID uint, seq int, format string, args ...any) {
	for _, existing := range p.Warnings {
		if existing.Key == key {
			return
		}
	}
	p.Warnings = append(p.Warnings, EditIssue{Code: code, Key: key, VideoID: videoID, Seq: seq, Message: fmt.Sprintf(format, args...)})
}

func editStreamInfo(stream editStream) EditStreamInfo {
	return EditStreamInfo{
		Index: stream.Index, Codec: stream.Codec, Profile: stream.Profile, Width: stream.Width, Height: stream.Height,
		FrameRate: stream.FrameRate, PixFmt: stream.PixFmt, ColorPrimaries: stream.ColorPrimaries,
		ColorTransfer: stream.ColorTransfer, ColorSpace: stream.ColorSpace, BitRate: stream.BitRate,
		Language: stream.Language, Title: stream.Title, Channels: stream.Channels, ChannelLayout: stream.ChannelLayout,
		SampleRate: stream.SampleRate, Image: editImageSubtitle(stream.Codec), Default: stream.Default,
	}
}

func editStreamInfos(streams []editStream) []EditStreamInfo {
	infos := make([]EditStreamInfo, 0, len(streams))
	for _, stream := range streams {
		infos = append(infos, editStreamInfo(stream))
	}
	return infos
}

// editSourceCtx 是预检阶段一个来源的全部事实。ok=false 时已经写过错误，不再参与计划。
type editSourceCtx struct {
	video        models.Video
	path         string
	size         int64
	modTimeNS    int64
	probe        editProbe
	sidecarSRT   bool
	sidecarOther []string
	ok           bool
}

var editSidecarOtherExts = []string{".ass", ".ssa", ".vtt", ".sub", ".idx", ".sup"}

// preflight 对项目当前配方做完整预检，并为每个要导出的项生成计划（completedSeqs 里的项跳过）。
// 返回的 error 只表示基础设施失败（数据库等）；内容问题都写在 EditPreflight.Errors 里。
func (s *VideoEditService) preflight(ctx context.Context, project models.VideoEditProject, recipe EditRecipe, completedSeqs map[int]bool) (*EditPreflight, []editPlan, error) {
	pre := &EditPreflight{
		ProjectID: project.ID, Revision: project.Revision, Kind: project.Kind, Mode: project.Mode,
		Sources: []EditPreflightSource{}, SpecOptions: []EditSpecOption{}, Outputs: []EditPreflightOutput{},
		Conflicts: []EditTrackConflict{}, Fast: EditFastInfo{Reasons: []string{}, CutPoints: []EditCutPoint{}},
		Warnings: []EditIssue{}, Errors: []EditIssue{},
	}
	if err := validateEditRecipeStructure(project.Kind, recipe); err != nil {
		pre.addError("recipe_invalid", "recipe_invalid", 0, 0, "%s", err.Error())
		return pre, nil, nil
	}
	ids := editPreflightVideoIDs(recipe, completedSeqs)
	sources, err := s.collectEditSources(ctx, pre, ids)
	if err != nil {
		return nil, nil, err
	}
	var plans []editPlan
	switch project.Kind {
	case models.VideoEditKindMerge:
		if plan, ok := s.buildMergePlan(pre, project.Mode, recipe, sources); ok {
			plans = append(plans, plan)
		}
	case models.VideoEditKindTrimIntro:
		plans = s.buildTrimPlans(pre, project.Mode, recipe, sources, completedSeqs)
	case models.VideoEditKindHDReplace:
		if plan, ok := s.buildHDPlan(pre, project.Mode, recipe, sources); ok {
			plans = append(plans, plan)
		}
	}
	plans = s.applyFastMode(ctx, pre, project.Mode, plans, sources)
	s.checkEditEncoders(ctx, pre, project.Mode, plans)
	s.finishEditOutputs(ctx, pre, plans)
	pre.Ready = len(pre.Errors) == 0
	return pre, plans, nil
}

// editPreflightVideoIDs 列出本次要导出的项引用的视频（跳过已完成的去片头项）。
func editPreflightVideoIDs(recipe EditRecipe, completedSeqs map[int]bool) []uint {
	if recipe.TrimIntro == nil {
		return editRecipeVideoIDs(recipe)
	}
	ids := []uint{}
	for index, item := range recipe.TrimIntro.Items {
		if !completedSeqs[index+1] {
			ids = append(ids, item.VideoID)
		}
	}
	return ids
}

func (s *VideoEditService) collectEditSources(ctx context.Context, pre *EditPreflight, ids []uint) (map[uint]*editSourceCtx, error) {
	db, err := editDB(ctx)
	if err != nil {
		return nil, err
	}
	var videos []models.Video
	if len(ids) > 0 {
		if err := db.Where("id IN ?", ids).Find(&videos).Error; err != nil {
			return nil, err
		}
	}
	byID := map[uint]models.Video{}
	for _, video := range videos {
		byID[video.ID] = video
	}
	sources := map[uint]*editSourceCtx{}
	for _, id := range ids {
		sc := &editSourceCtx{}
		sources[id] = sc
		info := EditPreflightSource{VideoID: id, Audio: []EditStreamInfo{}, Subtitles: []EditStreamInfo{}, SidecarOther: []string{}}
		info.Error = s.inspectEditSource(ctx, pre, id, byID, sc)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if sc.ok {
			probe := sc.probe
			video := editStreamInfo(*probe.Video)
			info.Video = &video
			info.DurationMS, info.Container, info.Size = probe.DurationMS, probe.FormatName, sc.size
			info.Audio, info.Subtitles = editStreamInfos(probe.Audio), editStreamInfos(probe.Subtitles)
			info.Attachments = len(probe.Attachments)
			for _, attachment := range probe.Attachments {
				if editFontAttachment(attachment) {
					info.FontAttachments++
				}
			}
			info.SidecarSRT, info.SidecarOther = sc.sidecarSRT, append([]string{}, sc.sidecarOther...)
			info.HDR, info.DolbyVision = editStreamHDR(probe.Video), probe.Video.DolbyVision
		}
		if sc.video.ID != 0 {
			info.Name = sc.video.Name
		}
		pre.Sources = append(pre.Sources, info)
	}
	return sources, nil
}

// inspectEditSource 校验来源仍在库、文件可读，并跑一次有界 ffprobe。返回写入 sources[].error 的说明。
func (s *VideoEditService) inspectEditSource(ctx context.Context, pre *EditPreflight, id uint, byID map[uint]models.Video, sc *editSourceCtx) string {
	video, ok := byID[id]
	if !ok {
		pre.addError("source_missing", fmt.Sprintf("source_missing:%d", id), id, 0, "视频 %d 不存在或已删除", id)
		return "视频不存在或已删除"
	}
	sc.video, sc.path = video, video.Path
	if video.IsStale {
		pre.addError("source_missing", fmt.Sprintf("source_missing:%d", id), id, 0, "《%s》已失效，请先在片库纠偏", video.Name)
		return "来源已失效"
	}
	size, modTimeNS, err := editFileFingerprint(video.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			pre.addError("source_missing", fmt.Sprintf("source_missing:%d", id), id, 0, "《%s》的文件不可访问", video.Name)
			return "来源文件不可访问"
		}
		pre.addError("source_missing", fmt.Sprintf("source_missing:%d", id), id, 0, "《%s》不是可读取的普通文件", video.Name)
		return "来源不是普通文件"
	}
	sc.size, sc.modTimeNS = size, modTimeNS
	probe, err := s.probeEditSource(ctx, video.Path)
	if err == nil && probe.Video == nil {
		err = fmt.Errorf("来源没有视频流")
	}
	if err == nil && probe.DurationMS <= 0 {
		err = fmt.Errorf("无法读取来源时长")
	}
	if err != nil {
		message := scrubEditPaths(err.Error(), video.Path)
		pre.addError("probe_failed", fmt.Sprintf("probe_failed:%d", id), id, 0, "《%s》探测失败：%s", video.Name, message)
		return message
	}
	sc.probe, sc.ok = probe, true
	if info, err := os.Lstat(subtitleparser.SRTPathForVideo(video.Path)); err == nil && info.Mode().IsRegular() {
		sc.sidecarSRT = true
	}
	base := strings.TrimSuffix(video.Path, filepath.Ext(video.Path))
	for _, ext := range editSidecarOtherExts {
		if info, err := os.Lstat(base + ext); err == nil && info.Mode().IsRegular() {
			sc.sidecarOther = append(sc.sidecarOther, ext)
		}
	}
	if len(sc.sidecarOther) > 0 {
		pre.addWarning("sidecar_unsupported", fmt.Sprintf("sidecar_unsupported:%d", id), id, 0,
			"《%s》的旁挂字幕 %s 不随成品复制（只处理同名 .srt）", video.Name, strings.Join(sc.sidecarOther, " "))
	}
	if probe.Video.DolbyVision {
		pre.addWarning("dolby_vision", fmt.Sprintf("dolby_vision:%d", id), id, 0, "《%s》的杜比视界元数据无法保留", video.Name)
	}
	return ""
}

// availableEncoders 以 ffmpeg -encoders 探测编码器（结果缓存在服务上）。
func (s *VideoEditService) availableEncoders(ctx context.Context) (map[string]bool, error) {
	s.mu.Lock()
	cached := s.encoders
	s.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	ffmpeg, err := s.findFFmpeg()
	if err != nil {
		return nil, err
	}
	var encoders map[string]bool
	if s.listEncoders != nil {
		encoders, err = s.listEncoders(ctx, ffmpeg)
	} else {
		probeCtx, cancel := context.WithTimeout(ctx, videoEditProbeTimeout)
		var stdout string
		stdout, _, err = s.runOutput(probeCtx, ffmpeg, []string{"-hide_banner", "-encoders"})
		cancel()
		encoders = parseEditEncoders(stdout)
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.encoders = encoders
	s.mu.Unlock()
	return encoders, nil
}

// parseEditEncoders 解析 `ffmpeg -encoders`：每行 " V....D name  描述"。
func parseEditEncoders(output string) map[string]bool {
	encoders := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && len(fields[0]) == 6 && strings.ContainsAny(fields[0][:1], "VAS") {
			encoders[fields[1]] = true
		}
	}
	return encoders
}

// checkEditEncoders：精确模式需要的视频编码器与 AAC 必须存在，缺失报 encoder_unavailable，不换用其他编码。
func (s *VideoEditService) checkEditEncoders(ctx context.Context, pre *EditPreflight, mode string, plans []editPlan) {
	if mode == "fast" || len(plans) == 0 {
		return
	}
	needed := []string{}
	for _, plan := range plans {
		if !oneOf(plan.Video.Encoder, needed...) {
			needed = append(needed, plan.Video.Encoder)
		}
		if len(plan.Audio) > 0 && !oneOf("aac", needed...) {
			needed = append(needed, "aac")
		}
	}
	encoders, err := s.availableEncoders(ctx)
	for _, name := range needed {
		if err != nil || !encoders[name] {
			pre.addError("encoder_unavailable", "encoder_unavailable:"+name, 0, 0, "编码器 %s 不可用", name)
		}
	}
}

// finishEditOutputs 为每个计划写输出摘要，检查文件名是否还有空位，并按输出目录汇总空间需求：
// 可用空间须 ≥（成品 + 工作目录片段）预计大小 × 1.2。
func (s *VideoEditService) finishEditOutputs(ctx context.Context, pre *EditPreflight, plans []editPlan) {
	type dirNeed struct {
		required int64
		outputs  []int
	}
	needs := map[string]*dirNeed{}
	order := []string{}
	for _, plan := range plans {
		name, free, err := firstFreeEditOutputName(ctx, plan.OutputDir, plan.OutputBase, plan.OutputExt)
		switch {
		case err != nil:
			// 权限、名字过长、数据库等错误：不是「被占用」，照实说明（文案擦掉路径）。
			pre.addError("output_unavailable", fmt.Sprintf("output_unavailable:%d", plan.Seq), 0, plan.Seq,
				"无法检查输出位置是否可用：%s", scrubEditPaths(err.Error(), plan.OutputDir))
		case !free:
			pre.addError("output_conflict", fmt.Sprintf("output_conflict:%d", plan.Seq), 0, plan.Seq,
				"输出文件名「%s%s」及 (2)…(99) 均已被占用", plan.OutputBase, plan.OutputExt)
		}
		output := EditPreflightOutput{
			Seq: plan.Seq, VideoIDs: []uint{}, PlannedName: name, Container: plan.Container, VideoCodec: plan.Video.Codec,
			Encoder: plan.Video.Encoder, Width: plan.Video.Width, Height: plan.Video.Height, FrameRate: plan.Video.FrameRate,
			PixFmt: plan.Video.PixFmt, ColorPrimaries: plan.Video.ColorPrimaries, ColorTransfer: plan.Video.ColorTransfer,
			ColorSpace: plan.Video.ColorSpace, HDR: plan.Video.Main10 && oneOf(plan.Video.ColorTransfer, "smpte2084", "arib-std-b67"),
			BitRate: plan.Video.BitRate, AudioCodec: "aac", AudioTracks: []EditOutputTrack{}, SubtitleTracks: []EditOutputTrack{},
			Attachments: len(plan.Attachments), Sidecar: plan.Sidecar.Mode, DurationMS: plan.DurationMS,
			EstimatedBytes: plan.EstimatedBytes, WorkBytes: plan.WorkBytes, Segments: []EditOutputSegment{},
		}
		if plan.Mode == "fast" {
			output.AudioCodec = "copy"
		}
		if tracks, ok := pre.trackOutputs[plan.Seq]; ok {
			output.AudioTracks, output.SubtitleTracks = tracks[0], tracks[1]
		}
		for _, source := range plan.Sources {
			output.VideoIDs = append(output.VideoIDs, source.VideoID)
		}
		for _, segment := range plan.Segments {
			output.Segments = append(output.Segments, EditOutputSegment{
				VideoID: plan.Sources[segment.VideoSource].VideoID, InMS: segment.InMS, RequestedInMS: segment.RequestedInMS,
				DurationMS: segment.DurMS, OutMS: segment.OutMS,
				Replaced: plan.Kind == models.VideoEditKindHDReplace && segment.VideoSource != segment.SidecarSource,
			})
		}
		pre.Outputs = append(pre.Outputs, output)
		need := needs[plan.OutputDir]
		if need == nil {
			need = &dirNeed{}
			needs[plan.OutputDir] = need
			order = append(order, plan.OutputDir)
		}
		need.required += int64(float64(plan.EstimatedBytes+plan.WorkBytes) * 1.2)
		need.outputs = append(need.outputs, len(pre.Outputs)-1)
	}
	for _, dir := range order {
		need := needs[dir]
		seq := pre.Outputs[need.outputs[0]].Seq
		free, err := s.diskFree(dir)
		if err != nil {
			pre.addError("disk_full", fmt.Sprintf("disk_full:%d", seq), 0, seq, "无法检查输出目录的可用空间")
			continue
		}
		for _, index := range need.outputs {
			pre.Outputs[index].FreeBytes = int64(min(free, uint64(math.MaxInt64)))
		}
		if free < uint64(need.required) {
			pre.addError("disk_full", fmt.Sprintf("disk_full:%d", seq), 0, seq, "输出目录可用空间不足：需要约 %s，可用 %s",
				formatEnhancementBytes(uint64(need.required)), formatEnhancementBytes(free))
		}
	}
}
