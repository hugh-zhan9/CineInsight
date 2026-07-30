package services

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const managedImageMaxBytes int64 = 20 << 20

type ManagedImageAsset struct {
	Path        string
	DisplayName string
	MIME        string
	ModTime     time.Time
}

type managedImageImport struct {
	RelativePath string
	Created      bool
}

type ManagedImageService struct {
	root string
	mu   sync.Mutex
}

func NewManagedImageService(dataDir string) *ManagedImageService {
	return &ManagedImageService{root: filepath.Join(dataDir, "media-details")}
}

func (s *ManagedImageService) Import(entityType string, entityID uint, sourcePath string) (managedImageImport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entityType != "people" && entityType != "collections" {
		return managedImageImport{}, fmt.Errorf("unsupported managed image entity %q", entityType)
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return managedImageImport{}, fmt.Errorf("open image source: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return managedImageImport{}, fmt.Errorf("stat image source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return managedImageImport{}, errors.New("image source is not a regular file")
	}
	if info.Size() > managedImageMaxBytes {
		return managedImageImport{}, fmt.Errorf("image exceeds %d byte limit", managedImageMaxBytes)
	}
	content, err := io.ReadAll(io.LimitReader(file, managedImageMaxBytes+1))
	if err != nil {
		return managedImageImport{}, fmt.Errorf("read image source: %w", err)
	}
	if int64(len(content)) > managedImageMaxBytes {
		return managedImageImport{}, fmt.Errorf("image exceeds %d byte limit", managedImageMaxBytes)
	}
	extension, err := managedImageExtension(content)
	if err != nil {
		return managedImageImport{}, err
	}
	digest := sha256.Sum256(content)
	directory := filepath.Join(s.root, entityType, strconv.FormatUint(uint64(entityID), 10))
	if err := os.MkdirAll(directory, 0755); err != nil {
		return managedImageImport{}, fmt.Errorf("create managed image directory: %w", err)
	}
	if err := s.verifyExistingPathWithinRoot(directory); err != nil {
		return managedImageImport{}, err
	}
	name := fmt.Sprintf("%x%s", digest, extension)
	target := filepath.Join(directory, name)
	relative, err := filepath.Rel(s.root, target)
	if err != nil {
		return managedImageImport{}, fmt.Errorf("build managed image path: %w", err)
	}
	if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, content) {
		return managedImageImport{RelativePath: filepath.ToSlash(relative)}, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return managedImageImport{}, fmt.Errorf("read managed image target: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".image-import-*")
	if err != nil {
		return managedImageImport{}, fmt.Errorf("create managed image temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		_ = temporary.Close()
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0644); err != nil {
		return managedImageImport{}, fmt.Errorf("set managed image permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return managedImageImport{}, fmt.Errorf("write managed image: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return managedImageImport{}, fmt.Errorf("sync managed image: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return managedImageImport{}, fmt.Errorf("close managed image: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return managedImageImport{}, fmt.Errorf("publish managed image: %w", err)
	}
	keepTemporary = false
	return managedImageImport{RelativePath: filepath.ToSlash(relative), Created: true}, nil
}

func managedImageExtension(content []byte) (string, error) {
	switch http.DetectContentType(content) {
	case "image/jpeg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	case "image/webp":
		return ".webp", nil
	default:
		return "", errors.New("image must be JPEG, PNG, or WebP")
	}
}

func (s *ManagedImageService) Remove(relativePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.pathWithinRoot(relativePath, true)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove managed image: %w", err)
	}
	return nil
}

func (s *ManagedImageService) verifyExistingPathWithinRoot(path string) error {
	resolvedRoot, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return fmt.Errorf("resolve managed image root: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve managed image directory: %w", err)
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("managed image directory escapes storage root")
	}
	return nil
}

func (s *ManagedImageService) Resolve(relativePath string) (ManagedImageAsset, error) {
	path, err := s.pathWithinRoot(relativePath, true)
	if err != nil {
		return ManagedImageAsset{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return ManagedImageAsset{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ManagedImageAsset{}, err
	}
	if !info.Mode().IsRegular() {
		return ManagedImageAsset{}, os.ErrNotExist
	}
	header := make([]byte, 512)
	read, _ := file.Read(header)
	mime := http.DetectContentType(header[:read])
	if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" {
		return ManagedImageAsset{}, os.ErrNotExist
	}
	return ManagedImageAsset{
		Path:        path,
		DisplayName: filepath.Base(path),
		MIME:        mime,
		ModTime:     info.ModTime(),
	}, nil
}

func (s *ManagedImageService) pathWithinRoot(relativePath string, requireExisting bool) (string, error) {
	if strings.TrimSpace(relativePath) == "" || filepath.IsAbs(relativePath) {
		return "", os.ErrNotExist
	}
	clean := filepath.Clean(filepath.FromSlash(relativePath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", os.ErrNotExist
	}
	target := filepath.Join(s.root, clean)
	relative, err := filepath.Rel(s.root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", os.ErrNotExist
	}
	if !requireExisting {
		return target, nil
	}
	resolvedRoot, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return "", os.ErrNotExist
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", os.ErrNotExist
	}
	resolvedRelative, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
		return "", os.ErrNotExist
	}
	return resolvedTarget, nil
}
