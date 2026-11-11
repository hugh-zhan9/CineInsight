package services

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

func editSourceHighDepth(v *editStream) bool  { return max(v.BitDepth, editPixelDepth(v.PixFmt)) > 8 }
func editSourceAbove10Bit(v *editStream) bool { return max(v.BitDepth, editPixelDepth(v.PixFmt)) > 10 }
func editProfileIdentity(p *editContainerProfile) string {
	if p.Muxer == "vob" {
		return "mpeg"
	}
	return p.Muxer
}

func (s *VideoEditService) inspectEditContainer(path, format string) (string, error) {
	if s.containerInspector != nil {
		return s.containerInspector(path, format)
	}
	return identifyEditContainer(path, format)
}

// identifyEditContainer只读取最多64KiB头，避免以ffprobe家族别名把MOV当成MP4或把Matroska当成WebM。
func identifyEditContainer(path, format string) (string, error) {
	if !strings.Contains(format, "mov") && !strings.Contains(format, "matroska") && !strings.Contains(format, "webm") {
		return format, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	head, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return "", err
	}
	if strings.Contains(format, "mov") {
		classic := false
		for at := 0; at+8 <= len(head); {
			size := uint64(binary.BigEndian.Uint32(head[at:]))
			kind := string(head[at+4 : at+8])
			header := 8
			if size == 1 {
				if at+16 > len(head) {
					break
				}
				size = binary.BigEndian.Uint64(head[at+8:])
				header = 16
			}
			if size < uint64(header) {
				break
			}
			if kind == "ftyp" {
				if size < uint64(header+8) || at+header+4 > len(head) {
					return "", fmt.Errorf("invalid ftyp")
				}
				brand := strings.TrimSpace(string(head[at+header : at+header+4]))
				switch {
				case brand == "qt":
					return "mov", nil
				case strings.HasPrefix(brand, "3g"):
					return "3gp", nil
				case strings.HasPrefix(strings.ToLower(brand), "f4v"):
					return "f4v", nil
				default:
					return "mp4", nil
				}
			}
			if oneOf(kind, "moov", "mdat", "wide") {
				classic = true
			}
			if size > uint64(len(head)-at) {
				break
			}
			at += int(size)
		}
		if classic {
			return "mov", nil
		}
		return "", fmt.Errorf("container ftyp not found")
	}
	if len(head) < 5 || binary.BigEndian.Uint32(head) != 0x1a45dfa3 {
		return "", fmt.Errorf("invalid EBML header")
	}
	size, n, ok := editEBMLVint(head[4:], false)
	if !ok || size > uint64(len(head)-4-n) {
		return "", fmt.Errorf("invalid EBML header size")
	}
	end := 4 + n + int(size)
	for at := 4 + n; at < end; {
		id, idn, ok := editEBMLVint(head[at:end], true)
		if !ok {
			return "", fmt.Errorf("invalid EBML ID")
		}
		at += idn
		length, sn, ok := editEBMLVint(head[at:end], false)
		if !ok || length > uint64(end-at-sn) {
			return "", fmt.Errorf("invalid EBML element")
		}
		at += sn
		if id == 0x4282 {
			doc := string(head[at : at+int(length)])
			if oneOf(doc, "matroska", "webm") {
				return doc, nil
			}
			return "", fmt.Errorf("unsupported EBML DocType")
		}
		at += int(length)
	}
	return "", fmt.Errorf("EBML DocType missing")
}
func editEBMLVint(b []byte, keepMarker bool) (uint64, int, bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	mask := byte(0x80)
	n := 1
	for b[0]&mask == 0 {
		n++
		mask >>= 1
	}
	if n > 8 || n > len(b) {
		return 0, 0, false
	}
	value := uint64(b[0])
	if !keepMarker {
		value = uint64(b[0] &^ mask)
	}
	for _, v := range b[1:n] {
		value = value<<8 | uint64(v)
	}
	return value, n, true
}

var editPackedDepthPattern = regexp.MustCompile(`^p[024](10|12|16)(?:le|be)$`)
var editPixelDepthPattern = regexp.MustCompile(`(?:p|gray|gbrap?)(9|10|12|14|16)(?:le|be)?$`)

func editPixelDepth(p string) int {
	if m := editPackedDepthPattern.FindStringSubmatch(strings.ToLower(p)); len(m) > 1 {
		v, _ := strconv.Atoi(m[1])
		return v
	}
	if m := editPixelDepthPattern.FindStringSubmatch(strings.ToLower(p)); len(m) > 1 {
		v, _ := strconv.Atoi(m[1])
		return v
	}
	switch strings.ToLower(p) {
	case "p010le", "p010be":
		return 10
	case "p012le", "p012be":
		return 12
	case "p016le", "p016be", "rgb48le", "rgb48be", "rgba64le", "rgba64be":
		return 16
	case "gbrpf32le", "rgbf32le", "grayf32le":
		return 32
	}
	return 8
}
