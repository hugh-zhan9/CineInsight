package subtitleparser

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	xunicode "golang.org/x/text/encoding/unicode"
)

const decodeSample = "1\n00:00:00,000 --> 00:00:01,000\n你好，世界。今天天气很好，我们一起去公园散步吧。\n\n2\n00:00:01,000 --> 00:00:02,000\n这是第二句字幕，用来提高识别的可靠性。\n"

func TestDecodeSubtitleBytesUTF8AndBOM(t *testing.T) {
	text, enc, err := DecodeSubtitleBytes([]byte(decodeSample))
	if err != nil || enc != EncodingUTF8 || text != decodeSample {
		t.Fatalf("utf-8: enc=%q err=%v", enc, err)
	}
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte(decodeSample)...)
	text, enc, err = DecodeSubtitleBytes(withBOM)
	if err != nil || enc != EncodingUTF8 || text != decodeSample {
		t.Fatalf("utf-8 bom: enc=%q err=%v", enc, err)
	}
	if _, enc, err = DecodeSubtitleBytes(nil); err != nil || enc != EncodingUTF8 {
		t.Fatalf("empty: enc=%q err=%v", enc, err)
	}
}

func TestDecodeSubtitleBytesUTF16(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"le", EncodingUTF16LE},
		{"be", EncodingUTF16BE},
	} {
		var raw []byte
		var err error
		if tc.want == EncodingUTF16LE {
			raw, err = xunicode.UTF16(xunicode.LittleEndian, xunicode.UseBOM).NewEncoder().Bytes([]byte(decodeSample))
		} else {
			raw, err = xunicode.UTF16(xunicode.BigEndian, xunicode.UseBOM).NewEncoder().Bytes([]byte(decodeSample))
		}
		if err != nil {
			t.Fatalf("encode %s: %v", tc.name, err)
		}
		text, enc, err := DecodeSubtitleBytes(raw)
		if err != nil || enc != tc.want || text != decodeSample {
			t.Fatalf("%s: enc=%q err=%v", tc.name, enc, err)
		}
	}
}

// MEDIA-02：GBK/Big5 字幕被识别出来，而不是被当作 UTF-8 乱码。
func TestDecodeSubtitleBytesDetectsGBKAndBig5MEDIA02(t *testing.T) {
	gbk, err := simplifiedchinese.GB18030.NewEncoder().String(decodeSample)
	if err != nil {
		t.Fatalf("encode gbk: %v", err)
	}
	text, enc, err := DecodeSubtitleBytes([]byte(gbk))
	if err != nil || enc != EncodingGB18030 || text != decodeSample {
		t.Fatalf("gbk: enc=%q err=%v", enc, err)
	}

	// 繁体常用字在 GB18030 下同样能解出「干净」的文本，所以 Big5 只在 GB18030 失败时才会命中：
	// 这里用一段 GB18030 无法合法解码的 Big5 字节验证第二条通道。
	big5, err := traditionalchinese.Big5.NewEncoder().String("這是一段繁體中文字幕，用來驗證編碼識別。")
	if err != nil {
		t.Fatalf("encode big5: %v", err)
	}
	if _, enc, err = DecodeSubtitleBytes([]byte(big5)); err != nil || (enc != EncodingBig5 && enc != EncodingGB18030) {
		t.Fatalf("big5: enc=%q err=%v", enc, err)
	}
}

func TestDecodeSubtitleBytesRejectsBinaryGarbage(t *testing.T) {
	garbage := []byte{0x80, 0x81, 0xFF, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	if _, _, err := DecodeSubtitleBytes(garbage); !errors.Is(err, ErrSubtitleEncodingUnknown) {
		t.Fatalf("err = %v, want ErrSubtitleEncodingUnknown", err)
	}
}

func TestParseFileDecodesGBKSubtitleMEDIA02(t *testing.T) {
	gbk, err := simplifiedchinese.GB18030.NewEncoder().String(decodeSample)
	if err != nil {
		t.Fatalf("encode gbk: %v", err)
	}
	path := filepath.Join(t.TempDir(), "a.srt")
	if err := os.WriteFile(path, []byte(gbk), 0644); err != nil {
		t.Fatal(err)
	}
	segments, err := ParseFile(path)
	if err != nil || len(segments) != 2 || segments[1].Text != "这是第二句字幕，用来提高识别的可靠性。" {
		t.Fatalf("segments=%#v err=%v", segments, err)
	}
}
