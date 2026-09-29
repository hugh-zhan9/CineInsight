package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"
)

// 字幕写入器（D-PC13）：所有对用户 .srt 的覆盖都从这里过。
//
// 流程：同目录临时文件 → fsync → 目标存在则备份到
// <dataDir>/subtitle-backups/<videoID>/<unixNano>.srt（每个视频只留最新 5 份）→ 原子替换。
// 调用方在外层持有 lockSubtitleFile(目标 .srt 路径)，写入器自己不加锁。

const (
	subtitleBackupDirName = "subtitle-backups"
	subtitleBackupKeep    = 5
	// subtitlePendingSuffix 是字幕生成落临时文件时用的后缀：校验通过前，原 .srt 一个字节都不动。
	subtitlePendingSuffix = ".cineinsight-pending.srt"
)

// 面向前端的字幕错误码（G-3）。
const (
	SubtitleErrorMissing         = "subtitle_missing"
	SubtitleErrorEncodingNotUTF8 = "subtitle_encoding_not_utf8"
	SubtitleErrorNotSidecarSRT   = "subtitle_not_sidecar_srt"
)

// SubtitleCodedError 带错误码的字幕错误。Error() 是不含绝对路径的中文文案。
type SubtitleCodedError struct {
	Code             string
	Message          string
	DetectedEncoding string
	// Candidates 只在编码歧义（GB18030 与 Big5 都能干净解码）时非空，带各自的前 3 条字幕预览。
	Candidates []subtitleparser.EncodingCandidate
}

func (e *SubtitleCodedError) Error() string { return e.Message }

type SubtitleWriteResult struct {
	BackupID string `json:"backup_id"`
	Replaced bool   `json:"replaced"`
}

type SubtitleBackup struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
	Size      int64     `json:"size"`
}

type SubtitleFileWriter struct {
	dataDir     string
	replaceFile func(temporaryPath, targetPath string) error
}

func NewSubtitleFileWriter(dataDir string) *SubtitleFileWriter {
	return &SubtitleFileWriter{
		dataDir:     strings.TrimSpace(dataDir),
		replaceFile: replaceSubtitleFileAtomically,
	}
}

func (w *SubtitleFileWriter) backupDir(videoID uint) (string, error) {
	if w.dataDir == "" {
		return "", errors.New("应用数据目录不可用，无法备份现有字幕")
	}
	return filepath.Join(w.dataDir, subtitleBackupDirName, strconv.FormatUint(uint64(videoID), 10)), nil
}

// Replace 用 content 原子地替换 target。目标已存在时先备份；任何一步失败，目标保持原样。
func (w *SubtitleFileWriter) Replace(ctx context.Context, videoID uint, target string, content []byte) (SubtitleWriteResult, error) {
	return w.replaceWithMode(ctx, videoID, target, content, 0644)
}

// ensureSubtitleTargetReplaceable 在生成/翻译开始前确认目标 .srt（若存在）是普通文件，
// 否则尽早报错，免得跑完识别/翻译才在写入器里失败。
func ensureSubtitleTargetReplaceable(target string) error {
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取字幕文件状态失败: %s", subtitleIOReason(err))
	}
	if !info.Mode().IsRegular() {
		return errors.New("字幕文件不是普通文件，已拒绝覆盖")
	}
	return nil
}

// replaceWithMode 是 Replace 的实现；defaultMode 只在目标不存在时使用（恢复备份时取备份记录的权限）。
func (w *SubtitleFileWriter) replaceWithMode(ctx context.Context, videoID uint, target string, content []byte, defaultMode os.FileMode) (SubtitleWriteResult, error) {
	if videoID == 0 {
		return SubtitleWriteResult{}, errors.New("字幕写入缺少视频 ID")
	}
	if strings.TrimSpace(target) == "" {
		return SubtitleWriteResult{}, errors.New("字幕写入缺少目标路径")
	}
	if err := ctx.Err(); err != nil {
		return SubtitleWriteResult{}, err
	}

	mode := defaultMode
	exists := false
	// Lstat 而不是 Stat：原子替换会把符号链接本身换掉，真正的字幕文件反而留着旧内容。
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return SubtitleWriteResult{}, errors.New("字幕文件不是普通文件，已拒绝覆盖")
		}
		mode = info.Mode().Perm()
		exists = true
	} else if !os.IsNotExist(err) {
		return SubtitleWriteResult{}, fmt.Errorf("读取字幕文件状态失败: %s", subtitleIOReason(err))
	}

	temporaryPath, err := writeSubtitleTemporary(target, content, mode)
	if err != nil {
		return SubtitleWriteResult{}, err
	}
	defer os.Remove(temporaryPath)

	result := SubtitleWriteResult{Replaced: exists}
	if exists {
		backupID, err := w.backupCurrent(videoID, target, mode)
		if err != nil {
			return SubtitleWriteResult{}, err
		}
		result.BackupID = backupID
	}
	if err := ctx.Err(); err != nil {
		w.discardBackup(videoID, result.BackupID)
		return SubtitleWriteResult{}, err
	}
	if err := w.replaceFile(temporaryPath, target); err != nil {
		w.discardBackup(videoID, result.BackupID)
		return SubtitleWriteResult{}, fmt.Errorf("替换字幕文件失败: %s", subtitleIOReason(err))
	}
	_ = syncSubtitleParentDirectory(filepath.Dir(target))
	if result.BackupID != "" {
		// 字幕已经换好，清理旧备份失败不该让这次写入报失败。
		_ = w.pruneBackups(videoID)
	}
	return result, nil
}

// writeSubtitleTemporary 在目标同目录写好临时文件并 fsync，权限已设成 mode，
// 这样 rename 之后新文件沿用原文件的权限。
func writeSubtitleTemporary(target string, content []byte, mode os.FileMode) (string, error) {
	directory := filepath.Dir(target)
	base := filepath.Base(target)
	for attempt := 0; attempt < 8; attempt++ {
		suffix := make([]byte, 6)
		if _, err := rand.Read(suffix); err != nil {
			return "", fmt.Errorf("创建临时字幕文件失败: %s", subtitleIOReason(err))
		}
		temporaryPath := filepath.Join(directory, "."+base+".cineinsight-tmp-"+hex.EncodeToString(suffix))
		file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", fmt.Errorf("创建临时字幕文件失败: %s", subtitleIOReason(err))
		}
		fail := func(step string, cause error) (string, error) {
			_ = file.Close()
			_ = os.Remove(temporaryPath)
			return "", fmt.Errorf("%s: %s", step, subtitleIOReason(cause))
		}
		if _, err := file.Write(content); err != nil {
			return fail("写入临时字幕文件失败", err)
		}
		if err := file.Chmod(mode); err != nil {
			return fail("设置临时字幕权限失败", err)
		}
		if err := file.Sync(); err != nil {
			return fail("同步临时字幕文件失败", err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(temporaryPath)
			return "", fmt.Errorf("关闭临时字幕文件失败: %s", subtitleIOReason(err))
		}
		return temporaryPath, nil
	}
	return "", errors.New("创建临时字幕文件失败: 名称冲突")
}

// backupCurrent 把 target 当前内容复制成一份新备份，返回备份 ID（unixNano 字符串）。
// 备份文件继承原文件的权限（mode），恢复到已被删除的目标时就用它作为原权限。
func (w *SubtitleFileWriter) backupCurrent(videoID uint, target string, mode os.FileMode) (string, error) {
	directory, err := w.backupDir(videoID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return "", fmt.Errorf("创建字幕备份目录失败: %s", subtitleIOReason(err))
	}
	source, err := os.Open(target)
	if err != nil {
		return "", fmt.Errorf("读取现有字幕失败: %s", subtitleIOReason(err))
	}
	defer source.Close()

	stamp := time.Now().UnixNano()
	// 同一纳秒内已有备份（时钟粗糙的平台）就顺延，绝不覆盖旧备份。
	for attempt := 0; attempt < 1000; attempt, stamp = attempt+1, stamp+1 {
		id := strconv.FormatInt(stamp, 10)
		backupPath := filepath.Join(directory, id+".srt")
		out, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", fmt.Errorf("创建字幕备份失败: %s", subtitleIOReason(err))
		}
		// 先按 0600 创建再 Chmod：不受 umask 影响，备份权限与原文件严格一致。
		copyErr := out.Chmod(mode)
		if copyErr == nil {
			_, copyErr = io.Copy(out, source)
		}
		if copyErr == nil {
			copyErr = out.Sync()
		}
		closeErr := out.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			_ = os.Remove(backupPath)
			return "", fmt.Errorf("写入字幕备份失败: %s", subtitleIOReason(copyErr))
		}
		return id, nil
	}
	return "", errors.New("创建字幕备份失败: 名称冲突")
}

func (w *SubtitleFileWriter) discardBackup(videoID uint, backupID string) {
	if backupID == "" {
		return
	}
	if directory, err := w.backupDir(videoID); err == nil {
		_ = os.Remove(filepath.Join(directory, backupID+".srt"))
	}
}

func (w *SubtitleFileWriter) pruneBackups(videoID uint) error {
	backups, err := w.ListBackups(videoID)
	if err != nil {
		return err
	}
	if len(backups) <= subtitleBackupKeep {
		return nil
	}
	directory, err := w.backupDir(videoID)
	if err != nil {
		return err
	}
	var firstErr error
	for _, backup := range backups[subtitleBackupKeep:] {
		if err := os.Remove(filepath.Join(directory, backup.ID+".srt")); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ListBackups 按时间从新到旧列出某个视频的字幕备份；没有备份目录时返回空列表。
func (w *SubtitleFileWriter) ListBackups(videoID uint) ([]SubtitleBackup, error) {
	directory, err := w.backupDir(videoID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return []SubtitleBackup{}, nil
		}
		return nil, fmt.Errorf("读取字幕备份目录失败: %s", subtitleIOReason(err))
	}
	backups := make([]SubtitleBackup, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".srt") {
			continue
		}
		id := strings.TrimSuffix(name, ".srt")
		nanos, err := strconv.ParseInt(id, 10, 64)
		if err != nil || nanos <= 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		backups = append(backups, SubtitleBackup{ID: id, CreatedAt: time.Unix(0, nanos), Size: info.Size()})
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].CreatedAt.After(backups[j].CreatedAt) })
	return backups, nil
}

// RestoreBackup 把某份备份写回 target。恢复前 Replace 会先备份当前文件，所以恢复本身也可以撤销。
func (w *SubtitleFileWriter) RestoreBackup(ctx context.Context, videoID uint, target string, backupID string) (SubtitleWriteResult, error) {
	if backupID == "" || strings.Trim(backupID, "0123456789") != "" {
		return SubtitleWriteResult{}, errors.New("字幕备份编号无效")
	}
	directory, err := w.backupDir(videoID)
	if err != nil {
		return SubtitleWriteResult{}, err
	}
	backupPath := filepath.Join(directory, backupID+".srt")
	content, err := os.ReadFile(backupPath)
	if err != nil {
		if os.IsNotExist(err) {
			return SubtitleWriteResult{}, errors.New("这份字幕备份已不存在")
		}
		return SubtitleWriteResult{}, fmt.Errorf("读取字幕备份失败: %s", subtitleIOReason(err))
	}
	// 备份文件的权限就是备份时原文件的权限：目标还在时沿用目标权限，已被删除时用它。
	mode := os.FileMode(0644)
	if info, statErr := os.Stat(backupPath); statErr == nil {
		mode = info.Mode().Perm()
	}
	return w.replaceWithMode(ctx, videoID, target, content, mode)
}

// SubtitleOverwriteInfo 是「生成字幕前告知会覆盖什么」所需的信息（D-PC13）。
type SubtitleOverwriteInfo struct {
	// Exists 表示同名 .srt 已经存在，生成会覆盖它（会先备份，可恢复）。
	Exists bool `json:"exists"`
	// SharedWith 是同目录、同基本名、扩展名不同的其他活跃视频：它们共用同一个 .srt，覆盖会一并影响。
	SharedWith []SubtitleOverwriteSharedVideo `json:"shared_with"`
}

type SubtitleOverwriteSharedVideo struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

type SubtitleRestoreResult struct {
	// BackupID 是恢复前对当前字幕做的备份，恢复本身因此也可以撤销。
	BackupID string   `json:"backup_id,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// GetSubtitleOverwriteInfo 报告视频的同名 .srt 是否存在，以及还有哪些活跃视频共用它。
func (s *SubtitleService) GetSubtitleOverwriteInfo(video models.Video) (*SubtitleOverwriteInfo, error) {
	info := &SubtitleOverwriteInfo{SharedWith: []SubtitleOverwriteSharedVideo{}}
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	if _, err := os.Lstat(srtPath); err == nil {
		info.Exists = true
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取字幕文件状态失败: %s", subtitleIOReason(err))
	}

	ext := filepath.Ext(video.Path)
	stem := strings.TrimSuffix(video.Path, ext)
	directory := filepath.Dir(video.Path)
	var candidates []models.Video
	if err := database.DB.
		Where("id <> ? AND is_stale = ? AND path LIKE ? ESCAPE '\\'", video.ID, false, escapeSubtitleLikePattern(stem)+".%").
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		candidateExt := filepath.Ext(candidate.Path)
		if filepath.Dir(candidate.Path) != directory || strings.TrimSuffix(candidate.Path, candidateExt) != stem || candidateExt == ext {
			continue
		}
		info.SharedWith = append(info.SharedWith, SubtitleOverwriteSharedVideo{ID: candidate.ID, Name: candidate.Name})
	}
	return info, nil
}

func escapeSubtitleLikePattern(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// ListSubtitleBackups 列出视频的字幕备份（新到旧，最多 5 份）。
func (s *SubtitleService) ListSubtitleBackups(video models.Video) ([]SubtitleBackup, error) {
	return s.subtitleWriter().ListBackups(video.ID)
}

// RestoreSubtitleBackup 把某份备份写回同名 .srt，并刷新字幕索引。恢复前先备份当前文件。
func (s *SubtitleService) RestoreSubtitleBackup(video models.Video, backupID string) (*SubtitleRestoreResult, error) {
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	unlock := lockSubtitleFile(srtPath)
	defer unlock()

	writeResult, err := s.subtitleWriter().RestoreBackup(context.Background(), video.ID, srtPath, backupID)
	if err != nil {
		return nil, err
	}
	result := &SubtitleRestoreResult{BackupID: writeResult.BackupID}
	if err := indexSubtitleFileForVideoID(video.ID, srtPath); err != nil {
		log.Printf("[Subtitle] index restored subtitle failed video_id=%d err=%v", video.ID, err)
		result.Warnings = append(result.Warnings, "字幕已恢复，但搜索索引刷新失败")
	}
	return result, nil
}

// subtitleIOReason 把文件系统错误翻成不含本地路径的中文原因，
// 让保存失败可以放心回给 WebView 与日志，而不暴露媒体库位置。
func subtitleIOReason(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "文件或目录不存在"
	case errors.Is(err, os.ErrPermission):
		return "没有权限"
	case errors.Is(err, syscall.ENOSPC):
		return "磁盘空间不足"
	case errors.Is(err, syscall.EROFS):
		return "磁盘是只读的"
	case errors.Is(err, os.ErrExist):
		return "文件已存在"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return "系统错误：" + errno.Error()
	}
	return "文件系统操作失败"
}
