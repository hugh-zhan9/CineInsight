package services

import (
	"strings"
	"unicode/utf8"

	"video-master/services/btmanifest"
)

// btOriginalLayout projects protocol paths into qBittorrent 5.2.3/libtorrent 2
// Original layout. IDs and digests remain those of the original metadata.
// extract_files2 omits info.name only for a pure-v2 sole root leaf. A nested
// sole leaf or a shared tree root still gets info.name, even when names repeat.
func btOriginalLayout(m btmanifest.Manifest) (btmanifest.Manifest, error) {
	prefix := m.InfoHashV1 == "" && m.InfoHashV2 != "" && !(len(m.Files) == 1 && !strings.Contains(m.Files[0].Path, "/"))
	if prefix && !btOriginalComponent(m.Name) {
		return btmanifest.Manifest{}, ErrBTPath
	}
	out := m
	out.Files = append([]btmanifest.File(nil), m.Files...)
	total := 0
	for i, file := range out.Files {
		if file.Path == "" && file.Padding {
			continue
		}
		length := len(file.Path)
		depth := strings.Count(file.Path, "/") + 1
		if prefix {
			length += len(m.Name) + 1
			depth++
		}
		if depth > btmanifest.MaxDepth || length > btmanifest.MaxBytes-total {
			return btmanifest.Manifest{}, ErrBTPath
		}
		total += length
		for _, part := range strings.Split(file.Path, "/") {
			if !btOriginalComponent(part) {
				return btmanifest.Manifest{}, ErrBTPath
			}
		}
		if prefix {
			out.Files[i].Path = m.Name + "/" + file.Path
		}
	}
	return out, nil
}

// Reject components the target libtorrent would sanitize/truncate. We never
// silently match a renamed client path back to a different protocol path.
func btOriginalComponent(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 240 || !utf8.ValidString(s) || strings.ContainsAny(s, "/\\:\x00") {
		return false
	}
	for _, r := range s {
		if r < 32 || r == '\u202a' || r == '\u202b' || r == '\u202c' || r == '\u202d' || r == '\u202e' || r == '\u200e' || r == '\u200f' {
			return false
		}
	}
	return true
}
