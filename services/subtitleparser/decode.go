package subtitleparser

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	xunicode "golang.org/x/text/encoding/unicode"
)

// 字幕编码识别（D-PC14）。DecodeSubtitleBytes 返回的 encoding 取值：
// utf-8、utf-16le、utf-16be、gb18030、big5。

const (
	EncodingUTF8    = "utf-8"
	EncodingUTF16LE = "utf-16le"
	EncodingUTF16BE = "utf-16be"
	EncodingGB18030 = "gb18030"
	EncodingBig5    = "big5"
)

// ErrSubtitleEncodingUnknown 表示字节既不是 UTF-8/UTF-16，也无法按 GB18030、Big5 干净地解码。
var ErrSubtitleEncodingUnknown = errors.New("无法识别字幕文件的编码")

// minPrintableRatio 是回退到 GB18030 / Big5 时，解码结果里可打印字符占比的下限。
const minPrintableRatio = 0.95

// EncodingCandidate 是歧义编码识别里的一个候选：Preview 是按该编码解出的前几条字幕文本，
// 让用户凭肉眼在 GB18030 与 Big5 之间选出不是乱码的那个。
type EncodingCandidate struct {
	Encoding string `json:"encoding"`
	Preview  string `json:"preview"`
}

// SubtitleDetection 是编码识别的完整结果。Candidates 只在歧义时（多个编码都能干净解码）非空，
// 且包含主推测本身；Encoding/Text 始终是主推测。
type SubtitleDetection struct {
	Text       string
	Encoding   string
	Candidates []EncodingCandidate
}

// previewSegmentCount 是歧义候选预览取的字幕条数。
const previewSegmentCount = 3

var legacyCandidates = []struct {
	name string
	enc  encoding.Encoding
}{
	{EncodingGB18030, simplifiedchinese.GB18030},
	{EncodingBig5, traditionalchinese.Big5},
}

// DecodeSubtitleBytes 把字幕字节解成文本并报告识别到的编码（歧义时取主推测）。
// UTF-8 BOM 与 UTF-16 BOM 会被剥掉；合法 UTF-8（含纯 ASCII）原样返回。
func DecodeSubtitleBytes(b []byte) (text string, encodingName string, err error) {
	detection, err := DetectSubtitleEncoding(b)
	if err != nil {
		return "", "", err
	}
	return detection.Text, detection.Encoding, nil
}

// DetectSubtitleEncoding 与 DecodeSubtitleBytes 同规则，另外在 GB18030 与 Big5 都能干净解码时
// 给出带预览的候选列表（歧义）。主推测仍按 GB18030、Big5 的顺序取第一个。
func DetectSubtitleEncoding(b []byte) (SubtitleDetection, error) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		rest := b[3:]
		if !utf8.Valid(rest) {
			return SubtitleDetection{}, ErrSubtitleEncodingUnknown
		}
		return SubtitleDetection{Text: string(rest), Encoding: EncodingUTF8}, nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		decoded, decodeErr := decodeWith(xunicode.UTF16(xunicode.LittleEndian, xunicode.ExpectBOM), b)
		if decodeErr != nil {
			return SubtitleDetection{}, ErrSubtitleEncodingUnknown
		}
		return SubtitleDetection{Text: decoded, Encoding: EncodingUTF16LE}, nil
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		decoded, decodeErr := decodeWith(xunicode.UTF16(xunicode.BigEndian, xunicode.ExpectBOM), b)
		if decodeErr != nil {
			return SubtitleDetection{}, ErrSubtitleEncodingUnknown
		}
		return SubtitleDetection{Text: decoded, Encoding: EncodingUTF16BE}, nil
	case utf8.Valid(b):
		return SubtitleDetection{Text: string(b), Encoding: EncodingUTF8}, nil
	}

	var detection SubtitleDetection
	var clean []EncodingCandidate
	for _, candidate := range legacyCandidates {
		decoded, decodeErr := decodeWith(candidate.enc, b)
		if decodeErr != nil || !isCleanDecoding(decoded) {
			continue
		}
		if detection.Encoding == "" {
			detection.Text, detection.Encoding = decoded, candidate.name
		}
		clean = append(clean, EncodingCandidate{Encoding: candidate.name, Preview: previewText(decoded)})
	}
	if detection.Encoding == "" {
		return SubtitleDetection{}, ErrSubtitleEncodingUnknown
	}
	if len(clean) > 1 {
		detection.Candidates = clean
	}
	return detection, nil
}

// DecodeSubtitleBytesAs 按调用方指定的编码解码（用户在歧义候选里的选择）。
// 只接受 GB18030 与 Big5 这两个需要用户裁决的传统编码；解码结果仍要通过「干净」检查。
func DecodeSubtitleBytesAs(b []byte, encodingName string) (string, error) {
	for _, candidate := range legacyCandidates {
		if candidate.name != encodingName {
			continue
		}
		decoded, err := decodeWith(candidate.enc, b)
		if err != nil || !isCleanDecoding(decoded) {
			return "", ErrSubtitleEncodingUnknown
		}
		return decoded, nil
	}
	return "", ErrSubtitleEncodingUnknown
}

// previewText 取前 previewSegmentCount 条字幕文本；解析不出字幕块时退回前几行非空、非序号、非时间轴的文本。
func previewText(decoded string) string {
	var texts []string
	if segments, err := Parse(decoded); err == nil {
		for _, segment := range segments {
			if len(texts) == previewSegmentCount {
				break
			}
			texts = append(texts, segment.Text)
		}
	}
	if len(texts) == 0 {
		for _, line := range strings.Split(normalizeContent(decoded), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.Contains(line, "-->") {
				continue
			}
			if _, err := strconv.Atoi(line); err == nil {
				continue
			}
			texts = append(texts, line)
			if len(texts) == previewSegmentCount {
				break
			}
		}
	}
	return strings.Join(texts, "\n")
}

func decodeWith(enc encoding.Encoding, b []byte) (string, error) {
	decoded, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// isCleanDecoding 要求没有 U+FFFD，且可打印字符（含换行与空白）占比不低于阈值。
func isCleanDecoding(text string) bool {
	if text == "" {
		return true
	}
	total, printable := 0, 0
	for _, r := range text {
		if r == utf8.RuneError {
			return false
		}
		total++
		if unicode.IsPrint(r) || unicode.IsSpace(r) {
			printable++
		}
	}
	return float64(printable)/float64(total) >= minPrintableRatio
}
