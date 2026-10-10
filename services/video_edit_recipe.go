package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"video-master/models"
)

// 视频编辑配方（视频编辑合同「三种任务与配方」）。配方以带 "v":1 的 JSON 存在项目行上；
// 时间一律为整数毫秒。这里只做结构校验（UpdateEditRecipe 接受编辑中的草稿），
// 区间、确认与映射等导出前条件由预检逐项报告（video_edit_preflight.go）。

const (
	videoEditRecipeVersion = 1

	videoEditMergeMinSources = 2
	videoEditMergeMaxSources = 50
	videoEditTrimMaxItems    = 200
	videoEditMaxSegments     = 500
	videoEditTitleMaxRunes   = 200

	// 片头分析窗口：默认 10 分钟，可设 1–20 分钟。
	videoEditDefaultIntroWindowMS = 600_000
	videoEditMinIntroWindowMS     = 60_000
	videoEditMaxIntroWindowMS     = 1_200_000
)

// 配方项来源（origin）。
const (
	EditOriginUniform  = "uniform"
	EditOriginManual   = "manual"
	EditOriginDetected = "detected"
)

// 自动识别状态（detect_status）：detected 之外的两个值要求用户手填或移出。
const (
	EditDetectDetected   = "detected"
	EditDetectUndetected = "undetected"
	EditDetectAmbiguous  = "ambiguous"
)

// 高清替换段的音频来源。
const (
	EditAudioSourceLong = "long"
	EditAudioSourceHD   = "hd"
)

// 轨道映射选择（tracks[].choice）。
const (
	EditTrackChoiceStream  = "stream"  // 用 stream_index 指定来源的绝对流序号
	EditTrackChoiceSilence = "silence" // 仅音轨：该来源片段填静音（仅精确模式）
	EditTrackChoiceNone    = "none"    // 仅文本字幕轨：该来源片段不出字幕（仅精确模式）
	EditTrackChoiceDrop    = "drop"    // 整条输出轨不导出（video_id 填 0）
)

// EditRecipe 是项目配方。kind 由项目决定，配方里只填对应的那一节。
type EditRecipe struct {
	V         int              `json:"v"`
	Merge     *EditMergeRecipe `json:"merge,omitempty"`
	TrimIntro *EditTrimRecipe  `json:"trim_intro,omitempty"`
	HDReplace *EditHDRecipe    `json:"hd_replace,omitempty"`
	Tracks    EditTrackChoices `json:"tracks"`
}

// EditMergeRecipe：sources 按用户顺序；spec_source_video_id 为 0 表示取第一个来源。
type EditMergeRecipe struct {
	Sources           []EditMergeSource `json:"sources"`
	SpecSourceVideoID uint              `json:"spec_source_video_id"`
}

type EditMergeSource struct {
	VideoID uint `json:"video_id"`
}

// EditTrimRecipe：每项单独产出一个成品。analysis_window_ms 为 0 时用默认 10 分钟。
type EditTrimRecipe struct {
	Items            []EditTrimItem `json:"items"`
	AnalysisWindowMS int64          `json:"analysis_window_ms"`
}

// EditTrimItem 是一个来源的移除区间 [remove_start_ms, remove_end_ms)。
type EditTrimItem struct {
	VideoID       uint    `json:"video_id"`
	RemoveStartMS int64   `json:"remove_start_ms"`
	RemoveEndMS   int64   `json:"remove_end_ms"`
	Origin        string  `json:"origin"`
	Confidence    float64 `json:"confidence"`
	DetectStatus  string  `json:"detect_status"`
	Confirmed     bool    `json:"confirmed"`
}

// EditHDRecipe：长版是主时间线；未被 segments 覆盖的长版区间保留长版画面。
type EditHDRecipe struct {
	LongVideoID uint            `json:"long_video_id"`
	HDVideoID   uint            `json:"hd_video_id"`
	Segments    []EditHDSegment `json:"segments"`
}

// EditHDSegment 的长版与高清区间时长必须相等（不变速，Q7）。
type EditHDSegment struct {
	LongStartMS int64   `json:"long_start_ms"`
	LongEndMS   int64   `json:"long_end_ms"`
	HDStartMS   int64   `json:"hd_start_ms"`
	HDEndMS     int64   `json:"hd_end_ms"`
	AudioSource string  `json:"audio_source"`
	Origin      string  `json:"origin"`
	MatchRate   float64 `json:"match_rate"`
	Status      string  `json:"status"`
	Confirmed   bool    `json:"confirmed"`
}

// EditTrackChoices 是用户对无法自动对应的轨道做出的明确选择（Q9）。
type EditTrackChoices struct {
	Audio    []EditTrackChoice `json:"audio"`
	Subtitle []EditTrackChoice `json:"subtitle"`
}

// EditTrackChoice 把输出轨 output（预检 tracks 里的 output_index）在来源 video_id 上
// 映射到 stream_index（choice=stream）或填充（silence/none）；choice=drop 时整条输出轨不导出。
type EditTrackChoice struct {
	Output      int    `json:"output"`
	VideoID     uint   `json:"video_id"`
	Choice      string `json:"choice"`
	StreamIndex int    `json:"stream_index"`
}

// VideoEditError 是带固定错误码的错误；文案形如 "code: 说明"，前端按前缀取码。
type VideoEditError struct {
	Code    string
	Message string
}

func (e *VideoEditError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func editError(code, format string, args ...any) error {
	return &VideoEditError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// VideoEditErrorCode 取出错误码；不是编辑错误时返回空串。
func VideoEditErrorCode(err error) string {
	var coded *VideoEditError
	if err != nil && errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

// parseEditRecipe 解析存储或请求里的配方 JSON。
func parseEditRecipe(raw string) (EditRecipe, error) {
	var recipe EditRecipe
	if strings.TrimSpace(raw) == "" {
		return recipe, editError("recipe_invalid", "配方为空")
	}
	if err := json.Unmarshal([]byte(raw), &recipe); err != nil {
		return recipe, editError("recipe_invalid", "配方不是合法 JSON")
	}
	return recipe, nil
}

func encodeEditRecipe(recipe EditRecipe) (string, error) {
	recipe.V = videoEditRecipeVersion
	normalizeEditRecipe(&recipe)
	raw, err := json.Marshal(recipe)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// normalizeEditRecipe 把 nil 切片换成空切片，JSON 往返后前端拿到的始终是数组。
func normalizeEditRecipe(recipe *EditRecipe) {
	if recipe.Tracks.Audio == nil {
		recipe.Tracks.Audio = []EditTrackChoice{}
	}
	if recipe.Tracks.Subtitle == nil {
		recipe.Tracks.Subtitle = []EditTrackChoice{}
	}
	if recipe.Merge != nil && recipe.Merge.Sources == nil {
		recipe.Merge.Sources = []EditMergeSource{}
	}
	if recipe.TrimIntro != nil && recipe.TrimIntro.Items == nil {
		recipe.TrimIntro.Items = []EditTrimItem{}
	}
	if recipe.HDReplace != nil && recipe.HDReplace.Segments == nil {
		recipe.HDReplace.Segments = []EditHDSegment{}
	}
}

// defaultEditRecipe 用建项目时选中的视频生成初始配方。hd_replace 的 videoIDs 为 [长版, 高清]。
func defaultEditRecipe(kind string, videoIDs []uint) (EditRecipe, error) {
	recipe := EditRecipe{V: videoEditRecipeVersion}
	switch kind {
	case models.VideoEditKindMerge:
		merge := &EditMergeRecipe{}
		for _, id := range videoIDs {
			merge.Sources = append(merge.Sources, EditMergeSource{VideoID: id})
		}
		recipe.Merge = merge
	case models.VideoEditKindTrimIntro:
		trim := &EditTrimRecipe{AnalysisWindowMS: videoEditDefaultIntroWindowMS}
		for _, id := range videoIDs {
			trim.Items = append(trim.Items, EditTrimItem{VideoID: id, Origin: EditOriginManual})
		}
		recipe.TrimIntro = trim
	case models.VideoEditKindHDReplace:
		if len(videoIDs) != 2 {
			return recipe, editError("recipe_invalid", "高清替换需要且只需要两个视频：长版与高清版")
		}
		recipe.HDReplace = &EditHDRecipe{LongVideoID: videoIDs[0], HDVideoID: videoIDs[1]}
	default:
		return recipe, editError("recipe_invalid", "未知的编辑类型")
	}
	normalizeEditRecipe(&recipe)
	return recipe, validateEditRecipeStructure(kind, recipe)
}

// validateEditRecipeStructure 只检查形状与取值集合；不查库、不判区间是否越界或重叠。
func validateEditRecipeStructure(kind string, recipe EditRecipe) error {
	if recipe.V != 0 && recipe.V != videoEditRecipeVersion {
		return editError("recipe_invalid", "不支持的配方版本 %d", recipe.V)
	}
	sections := 0
	for _, present := range []bool{recipe.Merge != nil, recipe.TrimIntro != nil, recipe.HDReplace != nil} {
		if present {
			sections++
		}
	}
	if sections != 1 {
		return editError("recipe_invalid", "配方必须且只能包含与项目类型对应的一节")
	}
	switch kind {
	case models.VideoEditKindMerge:
		if err := validateMergeStructure(recipe.Merge); err != nil {
			return err
		}
	case models.VideoEditKindTrimIntro:
		if err := validateTrimStructure(recipe.TrimIntro); err != nil {
			return err
		}
	case models.VideoEditKindHDReplace:
		if err := validateHDStructure(recipe.HDReplace); err != nil {
			return err
		}
	default:
		return editError("recipe_invalid", "未知的编辑类型")
	}
	return validateTrackChoices(recipe.Tracks)
}

func validateMergeStructure(merge *EditMergeRecipe) error {
	if merge == nil {
		return editError("recipe_invalid", "顺序合并缺少 merge 配置")
	}
	if len(merge.Sources) < videoEditMergeMinSources || len(merge.Sources) > videoEditMergeMaxSources {
		return editError("recipe_invalid", "顺序合并需要 %d–%d 个来源", videoEditMergeMinSources, videoEditMergeMaxSources)
	}
	seen := map[uint]bool{}
	for _, source := range merge.Sources {
		if source.VideoID == 0 || seen[source.VideoID] {
			return editError("recipe_invalid", "合并来源为空或重复")
		}
		seen[source.VideoID] = true
	}
	if merge.SpecSourceVideoID != 0 && !seen[merge.SpecSourceVideoID] {
		return editError("recipe_invalid", "输出规格来源必须是合并来源之一")
	}
	return nil
}

func validateTrimStructure(trim *EditTrimRecipe) error {
	if trim == nil {
		return editError("recipe_invalid", "批量去片头缺少 trim_intro 配置")
	}
	if len(trim.Items) < 1 || len(trim.Items) > videoEditTrimMaxItems {
		return editError("recipe_invalid", "批量去片头需要 1–%d 项", videoEditTrimMaxItems)
	}
	if trim.AnalysisWindowMS != 0 && (trim.AnalysisWindowMS < videoEditMinIntroWindowMS || trim.AnalysisWindowMS > videoEditMaxIntroWindowMS) {
		return editError("recipe_invalid", "片头分析窗口必须在 1–20 分钟之间")
	}
	seen := map[uint]bool{}
	for index, item := range trim.Items {
		if item.VideoID == 0 || seen[item.VideoID] {
			return editError("recipe_invalid", "第 %d 项的来源为空或重复", index+1)
		}
		seen[item.VideoID] = true
		if item.RemoveStartMS < 0 || item.RemoveEndMS < 0 {
			return editError("recipe_invalid", "第 %d 项的时间不能为负数", index+1)
		}
		if !oneOf(item.Origin, EditOriginUniform, EditOriginManual, EditOriginDetected) {
			return editError("recipe_invalid", "第 %d 项的 origin 无效", index+1)
		}
		if !oneOf(item.DetectStatus, "", EditDetectDetected, EditDetectUndetected, EditDetectAmbiguous) {
			return editError("recipe_invalid", "第 %d 项的 detect_status 无效", index+1)
		}
		if item.Confidence < 0 || item.Confidence > 1 {
			return editError("recipe_invalid", "第 %d 项的置信度必须在 0–1 之间", index+1)
		}
	}
	return nil
}

func validateHDStructure(hd *EditHDRecipe) error {
	if hd == nil {
		return editError("recipe_invalid", "高清替换缺少 hd_replace 配置")
	}
	if hd.LongVideoID == 0 || hd.HDVideoID == 0 || hd.LongVideoID == hd.HDVideoID {
		return editError("recipe_invalid", "长版与高清版必须是两个不同的视频")
	}
	if len(hd.Segments) > videoEditMaxSegments {
		return editError("recipe_invalid", "替换段不能超过 %d 段", videoEditMaxSegments)
	}
	for index, segment := range hd.Segments {
		if segment.LongStartMS < 0 || segment.LongEndMS < 0 || segment.HDStartMS < 0 || segment.HDEndMS < 0 {
			return editError("recipe_invalid", "第 %d 段的时间不能为负数", index+1)
		}
		if !oneOf(segment.AudioSource, "", EditAudioSourceLong, EditAudioSourceHD) {
			return editError("recipe_invalid", "第 %d 段的音频来源无效", index+1)
		}
		if !oneOf(segment.Origin, "", EditOriginDetected, EditOriginManual) {
			return editError("recipe_invalid", "第 %d 段的 origin 无效", index+1)
		}
		if !oneOf(segment.Status, "", "matched", "conflict") {
			return editError("recipe_invalid", "第 %d 段的 status 无效", index+1)
		}
		if segment.MatchRate < 0 || segment.MatchRate > 1 {
			return editError("recipe_invalid", "第 %d 段的匹配率必须在 0–1 之间", index+1)
		}
	}
	return nil
}

const videoEditMaxTrackChoices = 2000

func validateTrackChoices(tracks EditTrackChoices) error {
	if len(tracks.Audio)+len(tracks.Subtitle) > videoEditMaxTrackChoices {
		return editError("recipe_invalid", "轨道映射选择过多")
	}
	check := func(choices []EditTrackChoice, fill string, label string) error {
		seen := map[[2]uint]bool{}
		for _, choice := range choices {
			if choice.Output < 0 {
				return editError("recipe_invalid", "%s映射的输出轨序号无效", label)
			}
			switch choice.Choice {
			case EditTrackChoiceStream:
				if choice.VideoID == 0 || choice.StreamIndex < 0 {
					return editError("recipe_invalid", "%s映射缺少来源或流序号", label)
				}
			case EditTrackChoiceDrop:
				if choice.VideoID != 0 {
					return editError("recipe_invalid", "%s整轨不导出时 video_id 必须为 0", label)
				}
			case fill:
				if choice.VideoID == 0 {
					return editError("recipe_invalid", "%s填充缺少来源", label)
				}
			default:
				return editError("recipe_invalid", "%s映射的 choice 无效", label)
			}
			key := [2]uint{uint(choice.Output), choice.VideoID}
			if seen[key] {
				return editError("recipe_invalid", "%s映射对同一输出轨与来源重复给出", label)
			}
			seen[key] = true
		}
		return nil
	}
	if err := check(tracks.Audio, EditTrackChoiceSilence, "音轨"); err != nil {
		return err
	}
	return check(tracks.Subtitle, EditTrackChoiceNone, "字幕轨")
}

// editRecipeVideoIDs 返回配方引用的全部视频（按配方顺序去重）。
func editRecipeVideoIDs(recipe EditRecipe) []uint {
	ids := []uint{}
	seen := map[uint]bool{}
	add := func(id uint) {
		if id != 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if recipe.Merge != nil {
		for _, source := range recipe.Merge.Sources {
			add(source.VideoID)
		}
	}
	if recipe.TrimIntro != nil {
		for _, item := range recipe.TrimIntro.Items {
			add(item.VideoID)
		}
	}
	if recipe.HDReplace != nil {
		add(recipe.HDReplace.LongVideoID)
		add(recipe.HDReplace.HDVideoID)
	}
	return ids
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func editAudioSource(segment EditHDSegment) string {
	if segment.AudioSource == EditAudioSourceHD {
		return EditAudioSourceHD
	}
	return EditAudioSourceLong
}
