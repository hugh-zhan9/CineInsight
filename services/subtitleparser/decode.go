package subtitleparser

import (
	"bytes"
	"errors"
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

// DecodeSubtitleBytes 把字幕字节解成文本并报告识别到的编码。
// UTF-8 BOM 与 UTF-16 BOM 会被剥掉；合法 UTF-8（含纯 ASCII）原样返回。
func DecodeSubtitleBytes(b []byte) (text string, encodingName string, err error) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		rest := b[3:]
		if !utf8.Valid(rest) {
			return "", "", ErrSubtitleEncodingUnknown
		}
		return string(rest), EncodingUTF8, nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		decoded, decodeErr := decodeWith(xunicode.UTF16(xunicode.LittleEndian, xunicode.ExpectBOM), b)
		if decodeErr != nil {
			return "", "", ErrSubtitleEncodingUnknown
		}
		return decoded, EncodingUTF16LE, nil
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		decoded, decodeErr := decodeWith(xunicode.UTF16(xunicode.BigEndian, xunicode.ExpectBOM), b)
		if decodeErr != nil {
			return "", "", ErrSubtitleEncodingUnknown
		}
		return decoded, EncodingUTF16BE, nil
	case utf8.Valid(b):
		return string(b), EncodingUTF8, nil
	}

	candidates := []struct {
		name string
		enc  encoding.Encoding
	}{
		{EncodingGB18030, simplifiedchinese.GB18030},
		{EncodingBig5, traditionalchinese.Big5},
	}
	for _, candidate := range candidates {
		decoded, decodeErr := decodeWith(candidate.enc, b)
		if decodeErr != nil || !isCleanDecoding(decoded) {
			continue
		}
		return decoded, candidate.name, nil
	}
	return "", "", ErrSubtitleEncodingUnknown
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
