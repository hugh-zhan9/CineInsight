package services

import (
	"crypto/sha256"
	"fmt"
)

// This is an opaque representation of the existing size/mtime source identity,
// not a content hash. Including videoID prevents accepting another video's token.
func videoSourceVersion(videoID uint, size, modTimeNS int64) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d", videoID, size, modTimeNS))))
}
