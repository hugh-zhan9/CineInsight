package services

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"video-master/models"
)

// 冻结计划（合同「执行与发布」第 0 步）：排队时为每个导出项写入 plan_json——来源路径与 size:mtimeNS、
// 片段列表、轨道映射、编码参数、输出目录与计划文件名。worker 只按计划执行，不再读配方。

const editPlanVersion = 1

type editPlan struct {
	V              int                  `json:"v"`
	Seq            int                  `json:"seq"`
	Kind           string               `json:"kind"`
	Mode           string               `json:"mode"`
	Container      string               `json:"container"`
	OutputDir      string               `json:"output_dir"`
	OutputBase     string               `json:"output_base"`
	OutputExt      string               `json:"output_ext"`
	Sources        []editPlanSource     `json:"sources"`
	Video          editPlanVideo        `json:"video"`
	Audio          []editPlanTrack      `json:"audio"`
	Subtitles      []editPlanTrack      `json:"subtitles"`
	Attachments    []editPlanAttachment `json:"attachments"`
	Segments       []editPlanSegment    `json:"segments"`
	Sidecar        editPlanSidecar      `json:"sidecar"`
	DurationMS     int64                `json:"duration_ms"`
	EstimatedBytes int64                `json:"estimated_bytes"`
	WorkBytes      int64                `json:"work_bytes"`
}

type editPlanSource struct {
	VideoID    uint   `json:"video_id"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModTimeNS  int64  `json:"mod_time_ns"`
	DurationMS int64  `json:"duration_ms"`
}

// editPlanVideo：精确模式的统一视频规格；快速模式只用 Codec/尺寸做校验，不重编码。
type editPlanVideo struct {
	Encoder        string `json:"encoder"`
	Codec          string `json:"codec"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	FrameRate      string `json:"frame_rate"`
	PixFmt         string `json:"pix_fmt"`
	Main10         bool   `json:"main10"`
	BitRate        int64  `json:"bit_rate"`
	ColorPrimaries string `json:"color_primaries"`
	ColorTransfer  string `json:"color_transfer"`
	ColorSpace     string `json:"color_space"`
	ColorRange     string `json:"color_range"`
}

type editPlanTrack struct {
	Language string `json:"language"`
	Title    string `json:"title"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`
	Layout   string `json:"layout"`
	Image    bool   `json:"image"`
}

type editPlanAttachment struct {
	Source   int    `json:"source"`
	Stream   int    `json:"stream"`
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
}

// editPlanRef 是片段里一条输出轨的来源：Source 为 sources 下标；Fill 非空时为静音/空字幕填充。
type editPlanRef struct {
	Source int    `json:"source"`
	Stream int    `json:"stream"`
	InMS   int64  `json:"in_ms"`
	Fill   string `json:"fill,omitempty"`
}

// editPlanSegment 是成品时间线上的一段：画面取自 VideoSource 的 [InMS, InMS+DurMS)。
// RequestedInMS 是配方里的切点；快速模式下 InMS 是不晚于它的关键帧。
type editPlanSegment struct {
	VideoSource   int           `json:"video_source"`
	VideoStream   int           `json:"video_stream"`
	InMS          int64         `json:"in_ms"`
	RequestedInMS int64         `json:"requested_in_ms"`
	DurMS         int64         `json:"dur_ms"`
	OutMS         int64         `json:"out_ms"`
	Audio         []editPlanRef `json:"audio"`
	Subtitles     []editPlanRef `json:"subtitles"`
	SidecarSource int           `json:"sidecar_source"`
	SidecarInMS   int64         `json:"sidecar_in_ms"`
}

// editPlanSidecar：copy 原样复制 CopySource 的同名 .srt；retime 按片段重写；none 不写。
type editPlanSidecar struct {
	Mode       string `json:"mode"`
	CopySource int    `json:"copy_source"`
}

func editSeconds(ms int64) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}

// editInputSet 为一个片段收集输入：同一来源同一入点只打开一次。
type editInputSet struct {
	args    []string
	indexes map[string]int
	count   int
}

func (set *editInputSet) source(plan editPlan, source int, inMS, durMS int64) int {
	key := fmt.Sprintf("%d@%d", source, inMS)
	if index, ok := set.indexes[key]; ok {
		return index
	}
	if inMS > 0 {
		set.args = append(set.args, "-ss", editSeconds(inMS))
	}
	set.args = append(set.args, "-t", editSeconds(durMS), "-i", plan.Sources[source].Path)
	set.indexes[key] = set.count
	set.count++
	return set.count - 1
}

func (set *editInputSet) raw(args ...string) int {
	set.args = append(set.args, args...)
	set.count++
	return set.count - 1
}

func editChannelLayout(track editPlanTrack) string {
	if track.Layout != "" {
		return strings.SplitN(track.Layout, "(", 2)[0]
	}
	switch track.Channels {
	case 1:
		return "mono"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		return "stereo"
	}
}

// editSegmentArgs 生成一个片段的 ffmpeg 参数数组。精确模式把画面重编码为统一规格（缩放+加黑边、
// setsar=1、统一帧率与像素格式），音频转 48 kHz PCM（AAC 在拼接时一次编码，避免逐段编码器延迟累积），
// 文本字幕转为统一编码；快速模式整段 -c copy。输出一律为 mkv 片段。
func editSegmentArgs(plan editPlan, segment editPlanSegment, segmentPath, blankSRT string) []string {
	inputs := &editInputSet{indexes: map[string]int{}}
	maps := []string{}
	videoInput := inputs.source(plan, segment.VideoSource, segment.InMS, segment.DurMS)
	maps = append(maps, "-map", fmt.Sprintf("%d:%d", videoInput, segment.VideoStream))
	for k, ref := range segment.Audio {
		if ref.Fill == EditTrackChoiceSilence {
			layout := editChannelLayout(plan.Audio[k])
			index := inputs.raw("-f", "lavfi", "-t", editSeconds(segment.DurMS), "-i", "anullsrc=r=48000:cl="+layout)
			maps = append(maps, "-map", fmt.Sprintf("%d:0", index))
			continue
		}
		index := inputs.source(plan, ref.Source, ref.InMS, segment.DurMS)
		maps = append(maps, "-map", fmt.Sprintf("%d:%d", index, ref.Stream))
	}
	for _, ref := range segment.Subtitles {
		if ref.Fill == EditTrackChoiceNone {
			index := inputs.raw("-i", blankSRT)
			maps = append(maps, "-map", fmt.Sprintf("%d:0", index))
			continue
		}
		index := inputs.source(plan, ref.Source, ref.InMS, segment.DurMS)
		maps = append(maps, "-map", fmt.Sprintf("%d:%d", index, ref.Stream))
	}
	args := append([]string{"-nostdin", "-hide_banner", "-v", "error", "-y"}, inputs.args...)
	args = append(args, maps...)
	if plan.Mode == "fast" {
		// -copypriorss 0：丢掉定位点之前的包。mpegts 等容器定位不精确，流复制时会带上关键帧之前的一段音频，
		// make_zero 再把整段平移，画面就在片段里晚出现；切点取关键帧所在毫秒的下取整，第一个视频包就是该关键帧。
		args = append(args, "-c", "copy", "-copypriorss", "0", "-avoid_negative_ts", "make_zero")
	} else {
		args = append(args, "-filter:v", editVideoFilter(plan.Video))
		if frames := editFrameCount(segment.DurMS, plan.Video.FrameRate); frames > 0 {
			args = append(args, "-frames:v", strconv.FormatInt(frames, 10))
		}
		args = append(args, editVideoEncodeArgs(plan.Video)...)
		if len(plan.Audio) > 0 {
			args = append(args, "-af", "aresample=48000,apad", "-c:a", "pcm_s16le", "-ar", "48000")
			for k, track := range plan.Audio {
				args = append(args, fmt.Sprintf("-ac:a:%d", k), strconv.Itoa(track.Channels))
			}
		}
		for k, track := range plan.Subtitles {
			codec := track.Codec
			if track.Image {
				codec = "copy"
			}
			args = append(args, fmt.Sprintf("-c:s:%d", k), codec)
		}
	}
	args = append(args, "-t", editSeconds(segment.DurMS), "-map_metadata", "-1", "-map_chapters", "-1",
		"-max_muxing_queue_size", "4096", "-progress", "pipe:1", "-nostats", "-f", "matroska", segmentPath)
	return args
}

// editVideoFilter：缩放加黑边、统一帧率与像素格式；tpad 用最后一帧补齐，配合 -frames:v 得到整帧数。
func editVideoFilter(video editPlanVideo) string {
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%s,tpad=stop_mode=clone:stop=-1,format=%s",
		video.Width, video.Height, video.Width, video.Height, video.FrameRate, video.PixFmt)
}

// editFrameCount 是 durMS 在帧率 rate 下的整帧数（至少 1）。
func editFrameCount(durMS int64, rate string) int64 {
	fps := editFrameRateValue(rate)
	if fps <= 0 {
		return 0
	}
	return max(int64(math.Round(float64(durMS)*fps/1000)), 1)
}

// editQuantizeMS 把时长量化到整帧（毫秒取整）。
func editQuantizeMS(durMS int64, rate string) int64 {
	frames := editFrameCount(durMS, rate)
	if frames == 0 {
		return durMS
	}
	return int64(math.Round(float64(frames) * 1000 / editFrameRateValue(rate)))
}

func editVideoEncodeArgs(video editPlanVideo) []string {
	args := []string{"-c:v", video.Encoder, "-b:v", strconv.FormatInt(video.BitRate, 10)}
	if strings.HasPrefix(video.Encoder, "lib") {
		args = append(args, "-preset", "medium")
	}
	if video.Main10 {
		args = append(args, "-profile:v", "main10")
	}
	args = append(args, "-pix_fmt", video.PixFmt)
	for _, pair := range [][2]string{
		{"-color_primaries", video.ColorPrimaries}, {"-color_trc", video.ColorTransfer},
		{"-colorspace", video.ColorSpace}, {"-color_range", video.ColorRange},
	} {
		if pair[1] != "" && pair[1] != "unknown" && pair[1] != "reserved" {
			args = append(args, pair[0], pair[1])
		}
	}
	return args
}

// editConcatList 写 concat demuxer 列表：片段都在工作目录里，用相对文件名，避免路径转义。
func editConcatList(segmentNames []string) string {
	var builder strings.Builder
	builder.WriteString("ffconcat version 1.0\n")
	for _, name := range segmentNames {
		builder.WriteString("file '")
		builder.WriteString(strings.ReplaceAll(name, "'", `'\''`))
		builder.WriteString("'\n")
	}
	return builder.String()
}

func editAudioBitRate(channels int) int {
	rate := 64000 * channels
	if rate < 96000 {
		rate = 96000
	}
	if rate > 512000 {
		rate = 512000
	}
	return rate
}

// editConcatArgs 用 concat demuxer -c copy 拼接为工作目录内的 final.part（显式 -f 指定容器）。
// 精确模式的 PCM 音频在这一步统一编码为 AAC；字体附件从来源按文件名去重后附加（仅 mkv）。
func editConcatArgs(plan editPlan, listPath, stagingPath string) []string {
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-y", "-f", "concat", "-safe", "0", "-i", listPath}
	attachInputs := map[int]int{}
	next := 1
	for _, attachment := range plan.Attachments {
		if _, ok := attachInputs[attachment.Source]; ok {
			continue
		}
		args = append(args, "-i", plan.Sources[attachment.Source].Path)
		attachInputs[attachment.Source] = next
		next++
	}
	args = append(args, "-map", "0:v:0")
	for k := range plan.Audio {
		args = append(args, "-map", fmt.Sprintf("0:a:%d", k))
	}
	for k := range plan.Subtitles {
		args = append(args, "-map", fmt.Sprintf("0:s:%d", k))
	}
	for _, attachment := range plan.Attachments {
		args = append(args, "-map", fmt.Sprintf("%d:%d", attachInputs[attachment.Source], attachment.Stream))
	}
	args = append(args, "-c:v", "copy")
	if plan.Mode == "fast" {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac")
		for k, track := range plan.Audio {
			args = append(args, fmt.Sprintf("-b:a:%d", k), strconv.Itoa(editAudioBitRate(track.Channels)))
		}
	}
	if len(plan.Subtitles) > 0 {
		args = append(args, "-c:s", "copy")
	}
	if len(plan.Attachments) > 0 {
		args = append(args, "-c:t", "copy")
	}
	for k, track := range plan.Audio {
		args = append(args, editStreamMetadata("a", k, track)...)
	}
	for k, track := range plan.Subtitles {
		args = append(args, editStreamMetadata("s", k, track)...)
	}
	// -map_metadata -1 连流标签一起清掉；附件没有 filename/mimetype 标签时 mkv 写不出来，显式写回。
	for j, attachment := range plan.Attachments {
		args = append(args, fmt.Sprintf("-metadata:s:t:%d", j), "filename="+attachment.Filename,
			fmt.Sprintf("-metadata:s:t:%d", j), "mimetype="+attachment.MimeType)
	}
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1")
	if plan.Container == "mp4" {
		if plan.Video.Codec == "hevc" {
			args = append(args, "-tag:v", "hvc1")
		}
		args = append(args, "-movflags", "+faststart")
	}
	format := "matroska"
	if plan.Container == "mp4" {
		format = "mp4"
	}
	return append(args, "-progress", "pipe:1", "-nostats", "-f", format, stagingPath)
}

func editStreamMetadata(kind string, index int, track editPlanTrack) []string {
	args := []string{}
	language := strings.TrimSpace(track.Language)
	if language == "" {
		language = "und"
	}
	args = append(args, fmt.Sprintf("-metadata:s:%s:%d", kind, index), "language="+language)
	if track.Title != "" {
		args = append(args, fmt.Sprintf("-metadata:s:%s:%d", kind, index), "title="+track.Title)
	}
	disposition := "0"
	if index == 0 && kind == "a" {
		disposition = "default"
	}
	return append(args, fmt.Sprintf("-disposition:%s:%d", kind, index), disposition)
}

// editWorkdirFor 是导出项的隐藏工作目录：<输出目录>/.cineinsight-edit-<itemID>/。
func editWorkdirFor(plan editPlan, itemID uint) string {
	return filepath.Join(plan.OutputDir, fmt.Sprintf("%s%d", editWorkdirPrefix, itemID))
}

const editWorkdirPrefix = ".cineinsight-edit-"

// ===== 计划构建 =====

const (
	editMinBitRate = 2_000_000
	editMaxBitRate = 80_000_000
	// editMinSegmentMS 以下的片段（头尾零碎）不单独编码。
	editMinSegmentMS = 50
)

type editPlanBuilder struct {
	plan  editPlan
	index map[uint]int
}

func newEditPlanBuilder(seq int, kind, mode string) *editPlanBuilder {
	return &editPlanBuilder{
		plan: editPlan{V: editPlanVersion, Seq: seq, Kind: kind, Mode: mode, Sources: []editPlanSource{}, Audio: []editPlanTrack{},
			Subtitles: []editPlanTrack{}, Attachments: []editPlanAttachment{}, Segments: []editPlanSegment{}},
		index: map[uint]int{},
	}
}

func (b *editPlanBuilder) source(sc *editSourceCtx) int {
	if index, ok := b.index[sc.video.ID]; ok {
		return index
	}
	b.plan.Sources = append(b.plan.Sources, editPlanSource{
		VideoID: sc.video.ID, Path: sc.path, Size: sc.size, ModTimeNS: sc.modTimeNS, DurationMS: sc.probe.DurationMS,
	})
	b.index[sc.video.ID] = len(b.plan.Sources) - 1
	return len(b.plan.Sources) - 1
}

// editVideoEncoderName：macOS 用 VideoToolbox，其他平台用 libx264/libx265（合同「编码器」）。
func editVideoEncoderName(goos string, hevc bool) string {
	if goos == "darwin" {
		if hevc {
			return "hevc_videotoolbox"
		}
		return "h264_videotoolbox"
	}
	if hevc {
		return "libx265"
	}
	return "libx264"
}

// planVideoSpec 取规格来源的宽高、帧率、色彩与码率。HDR 或 HEVC 来源走 HEVC Main10 并保留色彩元数据；
// 码率取规格来源视频码率（缺失时按大小×8/时长估），夹在 2–80 Mbps。
func (s *VideoEditService) planVideoSpec(spec *editSourceCtx, mode string) editPlanVideo {
	stream := spec.probe.Video
	hevc := stream.Codec == "hevc" || editStreamHDR(stream)
	video := editPlanVideo{
		Codec: "h264", Width: stream.Width &^ 1, Height: stream.Height &^ 1, FrameRate: stream.FrameRate, PixFmt: "yuv420p",
		ColorPrimaries: stream.ColorPrimaries, ColorTransfer: stream.ColorTransfer, ColorSpace: stream.ColorSpace, ColorRange: stream.ColorRange,
	}
	if editFrameRateValue(video.FrameRate) <= 0 {
		video.FrameRate = "25"
	}
	if hevc {
		video.Codec, video.Main10 = "hevc", true
	}
	video.Encoder = editVideoEncoderName(s.goos, hevc)
	if video.Main10 {
		video.PixFmt = "yuv420p10le"
		if strings.HasSuffix(video.Encoder, "_videotoolbox") {
			video.PixFmt = "p010le"
		}
	}
	bitRate := stream.BitRate
	if bitRate <= 0 && spec.probe.DurationMS > 0 {
		bitRate = spec.size * 8 * 1000 / spec.probe.DurationMS
	}
	video.BitRate = min(max(bitRate, editMinBitRate), editMaxBitRate)
	if mode == "fast" {
		video.Encoder, video.Codec, video.PixFmt = "", stream.Codec, stream.PixFmt
	}
	return video
}

func editSameVideoSpec(a, b *editStream) bool {
	return a.Width == b.Width && a.Height == b.Height &&
		math.Abs(editFrameRateValue(a.FrameRate)-editFrameRateValue(b.FrameRate)) < 0.01 && editStreamHDR(a) == editStreamHDR(b)
}

// checkEditHDRMix：HDR 与 SDR 混合直接拒绝，不偷偷映射色调。
func checkEditHDRMix(pre *EditPreflight, seq int, sources []*editSourceCtx) bool {
	hdr, sdr := false, false
	for _, sc := range sources {
		if editStreamHDR(sc.probe.Video) {
			hdr = true
		} else {
			sdr = true
		}
	}
	if hdr && sdr {
		pre.addError("hdr_sdr_mix_unsupported", fmt.Sprintf("hdr_sdr_mix_unsupported:%d", seq), 0, seq, "HDR 与 SDR 来源不能混合导出")
		return false
	}
	return true
}

// buildMergePlan：按用户顺序首尾相接；规格取所选来源（默认第一个），其余来源缩放加黑边、转帧率。
func (s *VideoEditService) buildMergePlan(pre *EditPreflight, mode string, recipe EditRecipe, sources map[uint]*editSourceCtx) (editPlan, bool) {
	merge := recipe.Merge
	ordered := make([]*editSourceCtx, 0, len(merge.Sources))
	for _, source := range merge.Sources {
		sc := sources[source.VideoID]
		if sc == nil || !sc.ok {
			return editPlan{}, false
		}
		ordered = append(ordered, sc)
	}
	if !checkEditHDRMix(pre, 1, ordered) {
		return editPlan{}, false
	}
	spec := ordered[0]
	if merge.SpecSourceVideoID != 0 {
		spec = sources[merge.SpecSourceVideoID]
	}
	distinct := false
	for _, sc := range ordered[1:] {
		if !editSameVideoSpec(ordered[0].probe.Video, sc.probe.Video) {
			distinct = true
		}
	}
	if distinct {
		for _, sc := range ordered {
			v := sc.probe.Video
			pre.SpecOptions = append(pre.SpecOptions, EditSpecOption{VideoID: sc.video.ID, Width: v.Width, Height: v.Height,
				FrameRate: v.FrameRate, HDR: editStreamHDR(v), Selected: merge.SpecSourceVideoID == sc.video.ID})
		}
		if merge.SpecSourceVideoID == 0 {
			pre.addError("spec_required", "spec_required:1", 0, 1, "来源的分辨率、帧率或动态范围不同，请选择输出规格来源")
		}
	}
	builder := newEditPlanBuilder(1, models.VideoEditKindMerge, mode)
	builder.plan.Video = s.planVideoSpec(spec, mode)
	others := []editMapSource{}
	for _, sc := range ordered {
		if sc == spec {
			continue
		}
		others = append(others, editMapSource{VideoID: sc.video.ID, Role: "source", Streams: sc.probe.Audio})
		v := sc.probe.Video
		if v.Width != spec.probe.Video.Width || v.Height != spec.probe.Video.Height {
			pre.addWarning("scale", fmt.Sprintf("scale:%d:%dx%d", sc.video.ID, builder.plan.Video.Width, builder.plan.Video.Height), sc.video.ID, 1,
				"《%s》将按比例缩放到 %d×%d 并居中加黑边", sc.video.Name, builder.plan.Video.Width, builder.plan.Video.Height)
		}
		if math.Abs(editFrameRateValue(v.FrameRate)-editFrameRateValue(spec.probe.Video.FrameRate)) >= 0.01 {
			pre.addWarning("fps", fmt.Sprintf("fps:%d:%s", sc.video.ID, builder.plan.Video.FrameRate), sc.video.ID, 1,
				"《%s》的帧率 %s 将转换为 %s", sc.video.Name, v.FrameRate, builder.plan.Video.FrameRate)
		}
	}
	audio := mapEditTracks(pre, 1, editTrackAudio, spec.video.ID, "source", spec.probe.Audio, others, recipe.Tracks.Audio, mode)
	subOthers := make([]editMapSource, 0, len(others))
	for _, other := range others {
		subOthers = append(subOthers, editMapSource{VideoID: other.VideoID, Role: other.Role, Streams: sources[other.VideoID].probe.Subtitles})
	}
	subtitles := mapEditTracks(pre, 1, editTrackSubtitle, spec.video.ID, "source", spec.probe.Subtitles, subOthers, recipe.Tracks.Subtitle, mode)
	out := int64(0)
	for _, sc := range ordered {
		index := builder.source(sc)
		builder.plan.Segments = append(builder.plan.Segments, editPlanSegment{
			VideoSource: index, VideoStream: sc.probe.Video.Index, DurMS: sc.probe.DurationMS, OutMS: out,
			Audio: audio.segmentRefs(sc.video.ID, index, 0), Subtitles: subtitles.segmentRefs(sc.video.ID, index, 0),
			SidecarSource: index,
		})
		out += sc.probe.DurationMS
		if unused := audio.unusedStreams(sc.video.ID, sc.probe.Audio) + subtitles.unusedStreams(sc.video.ID, sc.probe.Subtitles); unused > 0 {
			pre.addWarning("track_unused", fmt.Sprintf("track_unused:1:%d:%d", sc.video.ID, unused), sc.video.ID, 1,
				"《%s》有 %d 条音轨/字幕轨不在输出中", sc.video.Name, unused)
		}
	}
	s.completeEditPlan(pre, builder, ordered, ordered[0], " (合并)", audio, subtitles)
	return builder.plan, true
}

// completeEditPlan 填轨道、附件、容器、输出位置、旁挂字幕方式与空间估算。
func (s *VideoEditService) completeEditPlan(pre *EditPreflight, b *editPlanBuilder, participating []*editSourceCtx, primary *editSourceCtx,
	suffix string, audio, subtitles editTrackPlan) {
	plan := &b.plan
	if pre.trackOutputs == nil {
		pre.trackOutputs = map[int][2][]EditOutputTrack{}
	}
	pre.trackOutputs[plan.Seq] = [2][]EditOutputTrack{audio.outputs, subtitles.outputs}
	plan.Audio = audio.planTracks(editTrackAudio, plan.Mode)
	plan.Subtitles = subtitles.planTracks(editTrackSubtitle, plan.Mode)
	seenFonts := map[string]bool{}
	allMP4 := true
	for _, sc := range participating {
		if !strings.Contains(sc.probe.FormatName, "mp4") && !strings.Contains(sc.probe.FormatName, "mov") {
			allMP4 = false
		}
		for _, attachment := range sc.probe.Attachments {
			name := strings.ToLower(strings.TrimSpace(attachment.Filename))
			if !editFontAttachment(attachment) || name == "" || seenFonts[name] {
				continue
			}
			seenFonts[name] = true
			mime := attachment.MimeType
			if mime == "" {
				mime = "application/x-truetype-font"
			}
			plan.Attachments = append(plan.Attachments, editPlanAttachment{Source: b.source(sc), Stream: attachment.Index,
				Filename: attachment.Filename, MimeType: mime})
		}
	}
	plan.Container, plan.OutputExt = "mkv", ".mkv"
	if allMP4 && len(plan.Subtitles) == 0 && len(plan.Attachments) == 0 && len(plan.Audio) <= 1 {
		plan.Container, plan.OutputExt = "mp4", ".mp4"
	}
	plan.OutputDir = filepath.Dir(primary.path)
	plan.OutputBase = sanitizeEditBaseName(editVideoDisplayName(primary.video)) + suffix
	plan.Sidecar = editPlanSidecar{Mode: "none", CopySource: -1}
	if plan.Mode != "fast" {
		// 精确模式按输出帧率把每段时长量化到整帧：画面补齐到整帧数，音频与旁挂字幕按同一时长对齐，
		// 多段拼接后不累积半帧误差。
		out := int64(0)
		for index := range plan.Segments {
			plan.Segments[index].DurMS = editQuantizeMS(plan.Segments[index].DurMS, plan.Video.FrameRate)
			plan.Segments[index].OutMS = out
			out += plan.Segments[index].DurMS
		}
	}
	for _, segment := range plan.Segments {
		plan.DurationMS += segment.DurMS
		if source := plan.Sources[segment.SidecarSource]; source.VideoID != 0 {
			for _, sc := range participating {
				if sc.video.ID == source.VideoID && sc.sidecarSRT {
					plan.Sidecar.Mode = "retime"
				}
			}
		}
	}
	for _, track := range plan.Subtitles {
		if track.Image && len(plan.Segments) > 1 {
			pre.addWarning("graphic_subtitle_cut", fmt.Sprintf("graphic_subtitle_cut:%d", plan.Seq), 0, plan.Seq,
				"图形字幕跨切点的一条会被截断或丢失")
			break
		}
	}
	if audio.hasFill() || subtitles.hasFill() {
		pre.addWarning("track_fill", fmt.Sprintf("track_fill:%d", plan.Seq), 0, plan.Seq, "部分片段的音轨将填充静音或字幕留空")
	}
	editEstimatePlan(plan)
}

// editEstimatePlan 估算成品大小与工作目录片段大小（精确模式片段含 PCM 音频）。
func editEstimatePlan(plan *editPlan) {
	seconds := float64(plan.DurationMS) / 1000
	if plan.Mode == "fast" {
		var bytes float64
		for _, segment := range plan.Segments {
			source := plan.Sources[segment.VideoSource]
			if source.DurationMS > 0 {
				bytes += float64(source.Size) * float64(segment.DurMS) / float64(source.DurationMS)
			}
		}
		plan.EstimatedBytes = int64(bytes) + 1<<20
		plan.WorkBytes = plan.EstimatedBytes
		return
	}
	audioBits, pcmBytes := 0.0, 0.0
	for _, track := range plan.Audio {
		audioBits += float64(editAudioBitRate(track.Channels))
		pcmBytes += 48000 * 2 * float64(track.Channels)
	}
	videoBytes := float64(plan.Video.BitRate) / 8 * seconds
	plan.EstimatedBytes = int64((videoBytes+audioBits/8*seconds)*1.05) + 1<<20
	plan.WorkBytes = int64((videoBytes+pcmBytes*seconds)*1.05) + 1<<20
}

// editMaxBaseNameBytes 是成品基本名（不含序号与扩展名）的字节上限：常见文件系统单个名字上限 255 字节，
// 留出 " (去片头)"、" (99)" 与扩展名的余量。
const editMaxBaseNameBytes = 180

// sanitizeEditBaseName 把显示名变成安全的文件基本名：去掉路径分隔与保留字符、控制字符，
// 不以点开头（避免成为隐藏文件），长度有上限。
func sanitizeEditBaseName(name string) string {
	var builder strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			builder.WriteRune(' ')
		case strings.ContainsRune(`/\:*?"<>|`, r):
			builder.WriteRune('_')
		default:
			builder.WriteRune(r)
		}
	}
	cleaned := strings.Join(strings.Fields(builder.String()), " ")
	cleaned = strings.TrimLeft(strings.TrimSpace(cleaned), ".")
	cleaned = strings.TrimSpace(cleaned)
	if runes := []rune(cleaned); len(runes) > 120 {
		cleaned = strings.TrimSpace(string(runes[:120]))
	}
	// 中文一个字 3 字节：按字节再截一次（在字符边界上），整个文件名不超过常见的 255 字节上限。
	for len(cleaned) > editMaxBaseNameBytes {
		runes := []rune(cleaned)
		cleaned = strings.TrimSpace(string(runes[:len(runes)-1]))
	}
	if cleaned == "" {
		cleaned = "视频"
	}
	return cleaned
}

// buildTrimPlans：每个来源一项，去掉 [remove_start, remove_end)，画面规格取自身；已完成的项跳过。
func (s *VideoEditService) buildTrimPlans(pre *EditPreflight, mode string, recipe EditRecipe, sources map[uint]*editSourceCtx, completedSeqs map[int]bool) []editPlan {
	plans := []editPlan{}
	for index, item := range recipe.TrimIntro.Items {
		seq := index + 1
		if completedSeqs[seq] {
			continue
		}
		sc := sources[item.VideoID]
		if sc == nil || !sc.ok {
			continue
		}
		duration := sc.probe.DurationMS
		key := fmt.Sprintf("%d", seq)
		switch {
		case item.RemoveStartMS >= item.RemoveEndMS:
			pre.addError("range_invalid", "range_invalid:"+key, item.VideoID, seq, "第 %d 项的移除区间无效（开始须早于结束）", seq)
			continue
		case item.RemoveEndMS > duration:
			pre.addError("range_invalid", "range_invalid:"+key, item.VideoID, seq, "第 %d 项的移除区间超出视频时长", seq)
			continue
		}
		if !item.Confirmed {
			pre.addError("unconfirmed", "unconfirmed:"+key, item.VideoID, seq, "第 %d 项尚未确认", seq)
		}
		builder := newEditPlanBuilder(seq, models.VideoEditKindTrimIntro, mode)
		builder.plan.Video = s.planVideoSpec(sc, mode)
		audio := mapEditTracks(pre, seq, editTrackAudio, sc.video.ID, "source", sc.probe.Audio, nil, nil, mode)
		subtitles := mapEditTracks(pre, seq, editTrackSubtitle, sc.video.ID, "source", sc.probe.Subtitles, nil, nil, mode)
		source := builder.source(sc)
		ranges := [][2]int64{}
		if item.RemoveStartMS >= editMinSegmentMS {
			ranges = append(ranges, [2]int64{0, item.RemoveStartMS})
		}
		if duration-item.RemoveEndMS >= editMinSegmentMS {
			ranges = append(ranges, [2]int64{item.RemoveEndMS, duration})
		}
		if len(ranges) == 0 {
			pre.addError("range_invalid", "range_invalid:"+key, item.VideoID, seq, "第 %d 项移除后没有剩余内容", seq)
			continue
		}
		out := int64(0)
		for _, r := range ranges {
			builder.plan.Segments = append(builder.plan.Segments, editPlanSegment{
				VideoSource: source, VideoStream: sc.probe.Video.Index, InMS: r[0], RequestedInMS: r[0], DurMS: r[1] - r[0], OutMS: out,
				Audio: audio.segmentRefs(sc.video.ID, source, r[0]), Subtitles: subtitles.segmentRefs(sc.video.ID, source, r[0]),
				SidecarSource: source, SidecarInMS: r[0],
			})
			out += r[1] - r[0]
		}
		s.completeEditPlan(pre, builder, []*editSourceCtx{sc}, sc, " (去片头)", audio, subtitles)
		plans = append(plans, builder.plan)
	}
	return plans
}

// buildHDPlan：长版是主时间线；替换段画面取高清版（宽高取高清来源，长版段按比例放大并居中加黑边），
// 音频按段选择来源（默认长版），字幕始终沿长版时间线（Q3、Q9）。
func (s *VideoEditService) buildHDPlan(pre *EditPreflight, mode string, recipe EditRecipe, sources map[uint]*editSourceCtx) (editPlan, bool) {
	hd := recipe.HDReplace
	long, high := sources[hd.LongVideoID], sources[hd.HDVideoID]
	if long == nil || !long.ok || high == nil || !high.ok {
		return editPlan{}, false
	}
	if !checkEditHDRMix(pre, 1, []*editSourceCtx{long, high}) {
		return editPlan{}, false
	}
	segments := append([]EditHDSegment{}, hd.Segments...)
	sort.SliceStable(segments, func(i, j int) bool { return segments[i].LongStartMS < segments[j].LongStartMS })
	if len(segments) == 0 {
		pre.addError("range_invalid", "range_invalid:segments", 0, 1, "至少需要一个已确认的替换段")
	}
	valid := true
	usesHDAudio := false
	for i, segment := range segments {
		key := fmt.Sprintf("%d-%d", segment.LongStartMS, segment.LongEndMS)
		label := fmt.Sprintf("长版 %s–%s 的替换段", editClock(segment.LongStartMS), editClock(segment.LongEndMS))
		switch {
		case segment.LongStartMS >= segment.LongEndMS || segment.HDStartMS >= segment.HDEndMS:
			pre.addError("range_invalid", "range_invalid:"+key, 0, 1, "%s区间无效", label)
			valid = false
		case segment.LongEndMS > long.probe.DurationMS || segment.HDEndMS > high.probe.DurationMS:
			pre.addError("range_invalid", "range_invalid:"+key, 0, 1, "%s超出视频时长", label)
			valid = false
		case segment.LongEndMS-segment.LongStartMS != segment.HDEndMS-segment.HDStartMS:
			pre.addError("range_invalid", "range_invalid:"+key, 0, 1, "%s的长版与高清时长不相等（不支持变速）", label)
			valid = false
		}
		if i > 0 && segment.LongStartMS < segments[i-1].LongEndMS {
			pre.addError("range_overlap", "range_overlap:"+key, 0, 1, "%s与上一段在长版时间线上重叠", label)
			valid = false
		}
		if !segment.Confirmed {
			pre.addError("unconfirmed", "unconfirmed:"+key, 0, 1, "%s尚未确认", label)
		}
		if editAudioSource(segment) == EditAudioSourceHD {
			usesHDAudio = true
		}
	}
	builder := newEditPlanBuilder(1, models.VideoEditKindHDReplace, mode)
	builder.plan.Video = s.planVideoSpec(high, mode)
	lv, hv := long.probe.Video, high.probe.Video
	if lv.Width != hv.Width || lv.Height != hv.Height {
		pre.addWarning("upscale", fmt.Sprintf("upscale:%dx%d", builder.plan.Video.Width, builder.plan.Video.Height), long.video.ID, 1,
			"长版画面将按比例缩放到 %d×%d 并居中加黑边（不裁切）", builder.plan.Video.Width, builder.plan.Video.Height)
	}
	if math.Abs(editFrameRateValue(lv.FrameRate)-editFrameRateValue(hv.FrameRate)) >= 0.01 {
		pre.addWarning("fps", fmt.Sprintf("fps:%d:%s", long.video.ID, builder.plan.Video.FrameRate), long.video.ID, 1,
			"长版帧率 %s 将转换为 %s", lv.FrameRate, builder.plan.Video.FrameRate)
	}
	audioOthers := []editMapSource{}
	if usesHDAudio {
		audioOthers = append(audioOthers, editMapSource{VideoID: high.video.ID, Role: "hd", Streams: high.probe.Audio})
	}
	audio := mapEditTracks(pre, 1, editTrackAudio, long.video.ID, "long", long.probe.Audio, audioOthers, recipe.Tracks.Audio, mode)
	subtitles := mapEditTracks(pre, 1, editTrackSubtitle, long.video.ID, "long", long.probe.Subtitles, nil, recipe.Tracks.Subtitle, mode)
	if !valid {
		return editPlan{}, false
	}
	s.appendHDTimeline(builder, long, high, segments, audio, subtitles)
	// 旁挂字幕沿长版时间线（Q9）：每个片段的 SidecarSource 都是长版、SidecarInMS 是长版坐标，
	// completeEditPlan 据此定为 retime。不原样复制：精确模式片段时长量化到整帧、<50 ms 的间隙被丢弃，
	// 成品时间线与长版并不逐毫秒相同。
	s.completeEditPlan(pre, builder, []*editSourceCtx{long, high}, long, " (高清替换)", audio, subtitles)
	return builder.plan, true
}

func editClock(ms int64) string {
	seconds := ms / 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", seconds/3600, seconds/60%60, seconds%60, ms%1000)
}

// appendHDTimeline 沿长版时间线排出片段：未覆盖的区间保留长版画面与音轨，替换段画面取高清来源。
// 字幕（内嵌与旁挂）始终取长版对应时间。短于 editMinSegmentMS 的零碎间隙不单独编码。
func (s *VideoEditService) appendHDTimeline(b *editPlanBuilder, long, high *editSourceCtx, segments []EditHDSegment, audio, subtitles editTrackPlan) {
	longIndex, highIndex := b.source(long), b.source(high)
	out, cursor := int64(0), int64(0)
	appendLong := func(start, end int64) {
		if end-start < editMinSegmentMS {
			return
		}
		b.plan.Segments = append(b.plan.Segments, editPlanSegment{
			VideoSource: longIndex, VideoStream: long.probe.Video.Index, InMS: start, RequestedInMS: start, DurMS: end - start, OutMS: out,
			Audio: audio.segmentRefs(long.video.ID, longIndex, start), Subtitles: subtitles.segmentRefs(long.video.ID, longIndex, start),
			SidecarSource: longIndex, SidecarInMS: start,
		})
		out += end - start
	}
	for _, segment := range segments {
		appendLong(cursor, segment.LongStartMS)
		duration := segment.LongEndMS - segment.LongStartMS
		audioRefs := audio.segmentRefs(long.video.ID, longIndex, segment.LongStartMS)
		if editAudioSource(segment) == EditAudioSourceHD {
			audioRefs = audio.segmentRefs(high.video.ID, highIndex, segment.HDStartMS)
		}
		b.plan.Segments = append(b.plan.Segments, editPlanSegment{
			VideoSource: highIndex, VideoStream: high.probe.Video.Index, InMS: segment.HDStartMS, RequestedInMS: segment.HDStartMS,
			DurMS: duration, OutMS: out, Audio: audioRefs,
			Subtitles:     subtitles.segmentRefs(long.video.ID, longIndex, segment.LongStartMS),
			SidecarSource: longIndex, SidecarInMS: segment.LongStartMS,
		})
		out += duration
		cursor = segment.LongEndMS
	}
	appendLong(cursor, long.probe.DurationMS)
}
