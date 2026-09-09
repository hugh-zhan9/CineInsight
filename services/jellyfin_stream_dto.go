package services

import (
	"fmt"
	"strconv"
	"strings"
	"video-master/models"
)

// jellyfinStreamDTO maps one probed stream to Jellyfin's MediaStream shape. DisplayTitle is
// composed here the way Jellyfin's server does it: clients such as Fileball print it verbatim on
// the detail screen ("视频:" / "音频:") and show an empty line when the server leaves it out.
func jellyfinStreamDTO(row models.MediaStream) map[string]interface{} {
	var streamType string
	switch row.StreamType {
	case "video":
		streamType = "Video"
		if row.IsAttachedPic {
			streamType = "EmbeddedImage"
		}
	case "audio":
		streamType = "Audio"
	case "subtitle":
		streamType = "Subtitle"
	default:
		return nil
	}
	stream := map[string]interface{}{
		"Index": row.StreamIndex, "Type": streamType, "Codec": row.CodecName, "Language": row.Language, "Title": row.Title,
		"IsDefault": row.IsDefault, "IsForced": false, "IsHearingImpaired": false, "IsExternal": false, "IsInterlaced": false,
		"SupportsExternalStream": false, "IsTextSubtitleStream": streamType == "Subtitle" && jellyfinTextSubtitleCodec(row.CodecName),
		"Width": row.Width, "Height": row.Height, "Channels": row.Channels, "SampleRate": row.SampleRate, "BitRate": row.BitRate,
		"Profile": row.Profile, "PixelFormat": row.PixelFormat, "BitDepth": row.BitsPerRawSample,
		"AverageFrameRate": jellyfinFrameRate(row.AvgFrameRate), "RealFrameRate": jellyfinFrameRate(row.RealFrameRate),
		"ColorSpace": row.ColorSpace, "ColorTransfer": row.ColorTransfer, "ColorPrimaries": row.ColorPrimaries, "ColorRange": row.ColorRange,
	}
	if row.ChannelLayout != "" {
		stream["ChannelLayout"] = row.ChannelLayout
	}
	if row.Width != nil && row.Height != nil && *row.Width > 0 && *row.Height > 0 {
		stream["AspectRatio"] = jellyfinAspectRatio(*row.Width, *row.Height)
	}
	if streamType == "Video" || streamType == "EmbeddedImage" {
		// Only SDR is a fully known range type; the HDR flavour (HDR10/HLG/DOVI) is not stored.
		videoRange, rangeType := "Unknown", "Unknown"
		if row.IsHDR != nil {
			if *row.IsHDR {
				videoRange, rangeType = "HDR", "HDR"
			} else {
				videoRange, rangeType = "SDR", "SDR"
			}
		}
		stream["VideoRange"], stream["VideoRangeType"] = videoRange, rangeType
	}
	stream["DisplayTitle"] = jellyfinStreamDisplayTitle(row, streamType, false)
	return stream
}

func jellyfinTextSubtitleCodec(codec string) bool {
	switch strings.ToLower(codec) {
	case "subrip", "srt", "ass", "ssa", "webvtt", "vtt", "mov_text", "text":
		return true
	}
	return false
}

// jellyfinStreamDisplayTitle follows MediaStream.GetDisplayTitle: video attributes are joined with
// spaces ("1080p HEVC SDR"), audio and subtitle attributes with " - " ("AAC - Stereo - Default").
// A probed Title comes first and attributes it already contains are not repeated.
func jellyfinStreamDisplayTitle(row models.MediaStream, streamType string, external bool) string {
	var attributes []string
	separator := " - "
	switch streamType {
	case "Video", "EmbeddedImage":
		separator = " "
		if row.Width != nil && row.Height != nil {
			if text := jellyfinResolutionText(*row.Width, *row.Height); text != "" {
				attributes = append(attributes, text)
			}
		}
		if row.CodecName != "" {
			attributes = append(attributes, strings.ToUpper(row.CodecName))
		}
		if row.IsHDR != nil {
			attributes = append(attributes, map[bool]string{true: "HDR", false: "SDR"}[*row.IsHDR])
		}
	case "Audio":
		if name := jellyfinLanguageName(row.Language); name != "" {
			attributes = append(attributes, name)
		}
		if codec := strings.ToLower(row.CodecName); codec != "" && codec != "dca" && codec != "dts" {
			attributes = append(attributes, jellyfinAudioCodecName(codec))
		} else if row.Profile != "" && !strings.EqualFold(row.Profile, "lc") {
			attributes = append(attributes, row.Profile)
		} else if codec != "" {
			attributes = append(attributes, "DTS")
		}
		if row.ChannelLayout != "" {
			attributes = append(attributes, jellyfinFirstUpper(row.ChannelLayout))
		} else if row.Channels != nil && *row.Channels > 0 {
			attributes = append(attributes, strconv.Itoa(*row.Channels)+" ch")
		}
		if row.IsDefault {
			attributes = append(attributes, "Default")
		}
		if external {
			attributes = append(attributes, "External")
		}
	case "Subtitle":
		name := jellyfinLanguageName(row.Language)
		if name == "" {
			name = "Und"
		}
		attributes = append(attributes, name)
		if row.IsDefault {
			attributes = append(attributes, "Default")
		}
		if row.CodecName != "" {
			attributes = append(attributes, strings.ToUpper(row.CodecName))
		}
		if external {
			attributes = append(attributes, "External")
		}
	}
	if title := strings.TrimSpace(row.Title); title != "" {
		parts := []string{title}
		for _, attribute := range attributes {
			if !strings.Contains(strings.ToLower(title), strings.ToLower(attribute)) {
				parts = append(parts, attribute)
			}
		}
		return strings.Join(parts, " - ")
	}
	return strings.Join(attributes, separator)
}

// jellyfinResolutionText buckets like Jellyfin's GetResolutionText, but orientation-agnostic:
// Jellyfin keys on width first and labels a 1080×1920 portrait clip "4K"; here the long side
// plays the role of width so portrait and landscape material get the same label.
func jellyfinResolutionText(width, height int) string {
	long, short := width, height
	if short > long {
		long, short = short, long
	}
	if long <= 0 || short <= 0 {
		return ""
	}
	buckets := []struct {
		long, short int
		label       string
	}{{256, 144, "144p"}, {426, 240, "240p"}, {640, 360, "360p"}, {682, 384, "384p"}, {720, 404, "404p"}, {854, 480, "480p"}, {960, 544, "540p"}, {1024, 576, "576p"}, {1280, 962, "720p"}, {2560, 1080, "1080p"}, {2560, 1440, "1440p"}, {4096, 3072, "4K"}, {8192, 6144, "8K"}}
	for _, bucket := range buckets {
		if long <= bucket.long && short <= bucket.short {
			return bucket.label
		}
	}
	return ""
}

// jellyfinAudioCodecName mirrors Jellyfin's AudioCodec.GetFriendlyName.
func jellyfinAudioCodecName(codec string) string {
	switch strings.ToLower(codec) {
	case "ac3":
		return "Dolby Digital"
	case "eac3":
		return "Dolby Digital+"
	case "truehd":
		return "Dolby TrueHD"
	case "dca", "dts":
		return "DTS"
	}
	return strings.ToUpper(codec)
}

// jellyfinLanguageName gives the English name for the language tags ffprobe commonly reports;
// Jellyfin resolves these through CultureInfo. Unknown tags are shown capitalised, "und" is hidden.
func jellyfinLanguageName(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	switch code {
	case "", "und":
		return ""
	case "eng", "en":
		return "English"
	case "chi", "zho", "zh", "cmn", "yue":
		return "Chinese"
	case "jpn", "ja":
		return "Japanese"
	case "kor", "ko":
		return "Korean"
	case "fre", "fra", "fr":
		return "French"
	case "ger", "deu", "de":
		return "German"
	case "spa", "es":
		return "Spanish"
	case "rus", "ru":
		return "Russian"
	case "ita", "it":
		return "Italian"
	case "por", "pt":
		return "Portuguese"
	case "tha", "th":
		return "Thai"
	case "vie", "vi":
		return "Vietnamese"
	}
	return jellyfinFirstUpper(code)
}

func jellyfinFirstUpper(text string) string {
	if text == "" {
		return ""
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// jellyfinAspectRatio reduces width:height like Jellyfin ("16:9", "9:16", "4:3").
func jellyfinAspectRatio(width, height int) string {
	a, b := width, height
	for b != 0 {
		a, b = b, a%b
	}
	if a <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", width/a, height/a)
}

// jellyfinFrameRate turns ffprobe's "30000/1001" into the float Jellyfin reports; nil when unknown.
func jellyfinFrameRate(raw string) interface{} {
	numerator, denominator, ok := strings.Cut(raw, "/")
	if !ok {
		if value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && value > 0 {
			return value
		}
		return nil
	}
	a, e1 := strconv.ParseFloat(strings.TrimSpace(numerator), 64)
	b, e2 := strconv.ParseFloat(strings.TrimSpace(denominator), 64)
	if e1 != nil || e2 != nil || b <= 0 || a <= 0 {
		return nil
	}
	return a / b
}
