package services

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"video-master/models"
)

// 条件求值（需求设计文档 §3.2）：只看文件与技术属性，在 Go 侧求值。技术属性未知（时长/高度为 0、
// 没有视频流、EXIF 未解析）时这一项不满足；规则不满足就不写账本，补探测后还能再评估。

// automationMediaFacts 是求值需要的一条媒体的属性。
type automationMediaFacts struct {
	Kind      string
	ID        uint
	Path      string
	Directory string
	Size      int64
	Duration  float64 // 视频；0 为未知
	Height    int     // 视频；0 为未知
	Codec     string  // 视频首个视频流的编码（小写）；空为未知
	HasGPS    *bool   // 图片；nil 为 EXIF 未解析
}

func videoAutomationFacts(video models.Video, codec string) automationMediaFacts {
	return automationMediaFacts{
		Kind: AutomationMediaVideo, ID: video.ID, Path: video.Path, Directory: video.Directory,
		Size: video.Size, Duration: video.Duration, Height: video.Height, Codec: codec,
	}
}

func imageAutomationFacts(image models.Image) automationMediaFacts {
	facts := automationMediaFacts{Kind: AutomationMediaImage, ID: image.ID, Path: image.Path, Directory: image.Directory, Size: image.Size}
	if image.ExifParsedAt != nil {
		hasGPS := image.GPSLatitude != nil && image.GPSLongitude != nil
		facts.HasGPS = &hasGPS
	}
	return facts
}

// compiledAutomationRule 是解析好、可直接求值的规则（正则只编译一次）。
type compiledAutomationRule struct {
	ID         uint
	Name       string
	MediaKind  string
	Conditions []AutomationRuleCondition
	Actions    []AutomationRuleAction
	regexps    map[int]*regexp.Regexp
}

// compileAutomationRule 解析并校验库里的规则；坏规则返回错误，由调用方停用（invalid_condition）。
func compileAutomationRule(rule models.AutomationRule) (*compiledAutomationRule, error) {
	conditions, actions, err := decodeAutomationRule(rule)
	if err != nil {
		return nil, err
	}
	input, err := normalizeAutomationRuleInput(AutomationRuleInput{Name: rule.Name, MediaKind: rule.MediaKind, Conditions: conditions, Actions: actions})
	if err != nil {
		return nil, err
	}
	compiled := &compiledAutomationRule{
		ID: rule.ID, Name: rule.Name, MediaKind: rule.MediaKind,
		Conditions: input.Conditions, Actions: input.Actions, regexps: map[int]*regexp.Regexp{},
	}
	for index, condition := range input.Conditions {
		if condition.Type == automationCondNameRegex {
			compiled.regexps[index] = regexp.MustCompile(condition.Pattern)
		}
	}
	return compiled, nil
}

// usesCodec 报告规则是否需要视频流编码（没有就不必为这一页去查 media_streams）。
func (r *compiledAutomationRule) usesCodec() bool {
	for _, condition := range r.Conditions {
		if condition.Type == automationCondVideoCodecIn {
			return true
		}
	}
	return false
}

// matches 报告这条媒体是否满足全部条件（AND）；任一项不满足或未知即为 false。
func (r *compiledAutomationRule) matches(facts automationMediaFacts) bool {
	if facts.Kind != r.MediaKind {
		return false
	}
	for index, condition := range r.Conditions {
		if !automationConditionHolds(condition, r.regexps[index], facts) {
			return false
		}
	}
	return true
}

// automationConditionHolds 求值一项条件。文件名取路径最后一段。
func automationConditionHolds(condition AutomationRuleCondition, re *regexp.Regexp, facts automationMediaFacts) bool {
	name := filepath.Base(facts.Path)
	switch condition.Type {
	case automationCondUnderDirectory:
		return automationPathUnder(facts.Path, condition.Path)
	case automationCondNameContains:
		return strings.Contains(strings.ToLower(name), strings.ToLower(condition.Text))
	case automationCondNameGlob:
		matched, err := path.Match(condition.Pattern, name)
		return err == nil && matched
	case automationCondNameRegex:
		return re != nil && re.MatchString(name)
	case automationCondExtensionIn:
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
		return ext != "" && automationListContains(condition.Exts, ext)
	case automationCondSizeBetween:
		return automationWithin(float64(facts.Size), condition.Min, condition.Max)
	case automationCondDuration:
		return facts.Duration > 0 && automationWithin(facts.Duration, condition.Min, condition.Max)
	case automationCondHeight:
		return facts.Height > 0 && automationWithin(float64(facts.Height), condition.Min, condition.Max)
	case automationCondVideoCodecIn:
		return facts.Codec != "" && automationListContains(condition.Codecs, facts.Codec)
	case automationCondHasGPS:
		return facts.HasGPS != nil && condition.Value != nil && *facts.HasGPS == *condition.Value
	}
	return false
}

// automationPathUnder 是按路径段的前缀判断：/a/b 不算在 /a/bc 之下（与扫描根裁剪同一口径）。
func automationPathUnder(mediaPath, dir string) bool {
	if dir == "" {
		return false
	}
	mediaPath = filepath.Clean(mediaPath)
	return mediaPath == dir || strings.HasPrefix(mediaPath, scanRootChildPrefix(dir))
}

func automationWithin(value float64, min, max *float64) bool {
	if min != nil && value < *min {
		return false
	}
	if max != nil && value > *max {
		return false
	}
	return true
}

func automationListContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
