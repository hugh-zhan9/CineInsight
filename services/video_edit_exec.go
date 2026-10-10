package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
)

// 执行与发布（合同「执行与发布」1–6 步）。

const (
	videoEditStderrTailBytes = 2 << 10
	editProgressInterval     = time.Second
)

// scrubEditPaths 先把已知的绝对路径（来源、工作目录、输出目录及其上一级）整体换成 <path>，再交给
// scrubEditMessage 擦剩下的。路径里常有空格，只靠正则会在第一个空格处断开、漏出后半截。
func scrubEditPaths(message string, paths ...string) string {
	known := []string{}
	for _, path := range paths {
		for depth := 0; depth < 3 && filepath.IsAbs(path) && len(path) > 1; depth++ {
			known = append(known, path)
			path = filepath.Dir(path)
		}
	}
	sort.Slice(known, func(i, j int) bool { return len(known[i]) > len(known[j]) })
	for _, path := range known {
		message = strings.ReplaceAll(message, path, "<path>")
	}
	return scrubEditMessage(message)
}

// editPlanPaths 是计划涉及的全部绝对路径：来源、输出目录与工作目录。
func editPlanPaths(plan editPlan, workdir string) []string {
	paths := []string{plan.OutputDir, workdir}
	for _, source := range plan.Sources {
		paths = append(paths, source.Path)
	}
	return paths
}

// scrubEditMessage 擦掉绝对路径并只留尾部 2 KB（ffmpeg stderr 与系统错误都可能带完整路径）。
func scrubEditMessage(message string) string {
	message = scrubAbsolutePaths(strings.TrimSpace(message))
	if len(message) > videoEditStderrTailBytes {
		trimmed := message[len(message)-videoEditStderrTailBytes:]
		for len(trimmed) > 0 && !utf8.RuneStart(trimmed[0]) {
			trimmed = trimmed[1:]
		}
		message = trimmed
	}
	return message
}

func runVideoEditOutput(ctx context.Context, name string, args []string) (string, string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.WaitDelay = 5 * time.Second
	stdout := &boundedBuffer{limit: mediaProbeMaxOutputBytes}
	stderr := &tailBuffer{limit: videoEditStderrTailBytes}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return stdout.String(), stderr.String(), ctxErr
		}
		return stdout.String(), stderr.String(), fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return stdout.String(), stderr.String(), nil
}

// runVideoEditProgress 运行带 -progress pipe:1 的 ffmpeg，逐行解析 out_time_us 回调毫秒进度。
func runVideoEditProgress(ctx context.Context, name string, args []string, onProgress func(outMS int64)) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.WaitDelay = 5 * time.Second
	stderr := &tailBuffer{limit: videoEditStderrTailBytes}
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !found || onProgress == nil || (key != "out_time_us" && key != "out_time_ms") {
			continue
		}
		if micros, err := strconv.ParseInt(value, 10, 64); err == nil && micros >= 0 {
			onProgress(micros / 1000)
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	if err := command.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return stderr.String(), ctxErr
		}
		return stderr.String(), fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return stderr.String(), nil
}

// editWorkdirItemID 只认「前缀 + 规范十进制 id」的目录名（与超分工作目录同一口径）。
func editWorkdirItemID(name string) (uint, bool) {
	suffix, found := strings.CutPrefix(name, editWorkdirPrefix)
	if !found {
		return 0, false
	}
	id, err := strconv.ParseUint(suffix, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != suffix || uint64(uint(id)) != id {
		return 0, false
	}
	return uint(id), true
}

// removeEditWorkdir 只删名字严格匹配的工作目录；路径为空或名字不符时什么也不做。
func removeEditWorkdir(path string) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return
	}
	if _, ok := editWorkdirItemID(filepath.Base(path)); !ok {
		return
	}
	_ = os.RemoveAll(path)
}

const editMaxNameAttempts = 99

// editOutputLstat 是检查成品名占用时的 Lstat（单测替换它来模拟权限错误）。
var editOutputLstat = os.Lstat

func editCandidateName(base, ext string, attempt int) string {
	if attempt <= 1 {
		return base + ext
	}
	return fmt.Sprintf("%s (%d)%s", base, attempt, ext)
}

// editOutputOccupied：成品路径被文件（任意类型）、同名 .srt 或库记录（含回收站，墓碑除外）占用。
func editOutputOccupied(ctx context.Context, path string) (bool, error) {
	for _, candidate := range []string{path, subtitleparser.SRTPathForVideo(path)} {
		if _, err := editOutputLstat(candidate); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return true, err
		}
	}
	db, err := editDB(ctx)
	if err != nil {
		return true, err
	}
	var rows []models.Video
	if err := withoutTrashTombstones(db.Unscoped().Where("path = ?", path), videoTrashKind).Limit(1).Find(&rows).Error; err != nil {
		return true, err
	}
	return len(rows) > 0, nil
}

// firstFreeEditOutputName 依次试 `<名称>.<ext>`、` (2)`…` (99)`，返回第一个空闲的文件名。
func firstFreeEditOutputName(ctx context.Context, dir, base, ext string) (string, bool, error) {
	for attempt := 1; attempt <= editMaxNameAttempts; attempt++ {
		name := editCandidateName(base, ext, attempt)
		occupied, err := editOutputOccupied(ctx, filepath.Join(dir, name))
		if err != nil {
			return base + ext, false, err
		}
		if !occupied {
			return name, true, nil
		}
	}
	return base + ext, false, nil
}

// editSourcesUnchanged 核对计划里的来源指纹（size:mtimeNS）。缺失报 source_missing，变化报 source_changed。
func editSourcesUnchanged(plan editPlan) error {
	for _, source := range plan.Sources {
		size, modTimeNS, err := editFileFingerprint(source.Path)
		if err != nil {
			return editError("source_missing", "来源视频 %d 的文件不可访问", source.VideoID)
		}
		if size != source.Size || modTimeNS != source.ModTimeNS {
			return editError("source_changed", "来源视频 %d 在排队后被修改", source.VideoID)
		}
	}
	return nil
}

// updateEditItem 写导出项字段（只在仍为 running 时生效）。
func (s *VideoEditService) updateEditItem(ctx context.Context, itemID uint, fields map[string]any) error {
	db, err := editDB(ctx)
	if err != nil {
		return err
	}
	return db.Model(&models.VideoEditItem{}).Where("id = ? AND status = ?", itemID, models.VideoEditStatusRunning).Updates(fields).Error
}

// editProgressReporter 把阶段进度节流写库并发事件（每秒至多一次）。
type editProgressReporter struct {
	service *VideoEditService
	item    models.VideoEditItem
	last    time.Time
}

func (r *editProgressReporter) report(ctx context.Context, phase string, progress float64, force bool) {
	now := time.Now()
	if !force && now.Sub(r.last) < editProgressInterval {
		return
	}
	r.last = now
	progress = min(max(progress, 0), 1)
	bg, cancel := editBackground(ctx)
	defer cancel()
	_ = r.service.updateEditItem(bg, r.item.ID, map[string]any{"phase": phase, "progress": progress})
	r.service.emit(VideoEditStateEvent{ProjectID: r.item.ProjectID, ItemID: r.item.ID, Status: models.VideoEditStatusRunning,
		ItemStatus: models.VideoEditStatusRunning, Phase: phase, Progress: progress})
}

// processItem 执行一个导出项：取媒体槽 → 核对来源 → 空间检查 → 逐段编码/复制 → 拼接 → 校验 → 发布。
// 返回 nil 表示已发布入库；返回错误时由 finishItem 写终态并删工作目录。
func (s *VideoEditService) processItem(ctx context.Context, item models.VideoEditItem) error {
	var plan editPlan
	if err := json.Unmarshal([]byte(item.PlanJSON), &plan); err != nil || plan.V != editPlanVersion || len(plan.Segments) == 0 {
		return editError("encode_failed", "导出计划不可读，请重新排队")
	}
	reporter := &editProgressReporter{service: s, item: item}
	if err := s.slot.Acquire(ctx); err != nil {
		return err
	}
	defer s.slot.Release()
	if err := editSourcesUnchanged(plan); err != nil {
		return err
	}
	workdir := editWorkdirFor(plan, item.ID)
	free, err := s.diskFree(plan.OutputDir)
	required := uint64(float64(plan.EstimatedBytes+plan.WorkBytes) * 1.2)
	if err != nil {
		return editError("disk_full", "无法检查输出目录的可用空间")
	}
	if free < required {
		return editError("disk_full", "输出目录可用空间不足：需要约 %s，可用 %s", formatEnhancementBytes(required), formatEnhancementBytes(free))
	}
	_ = os.RemoveAll(workdir)
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return editError("encode_failed", "无法创建工作目录：%s", err.Error())
	}
	bg, cancel := editBackground(ctx)
	err = s.updateEditItem(bg, item.ID, map[string]any{"work_dir": workdir, "phase": models.VideoEditPhaseEncode})
	cancel()
	if err != nil {
		return err
	}
	ffmpeg, err := s.findFFmpeg()
	if err != nil {
		return editError("encoder_unavailable", "找不到 ffmpeg")
	}
	staging, err := s.encodeEditSegments(ctx, ffmpeg, plan, workdir, reporter)
	if err != nil {
		return err
	}
	reporter.report(ctx, models.VideoEditPhaseVerify, 0.95, true)
	if err := s.verifyEditOutput(ctx, plan, staging); err != nil {
		return err
	}
	if err := editSourcesUnchanged(plan); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	reporter.report(ctx, models.VideoEditPhasePublish, 0.97, true)
	return s.publishEditOutput(ctx, item, plan, staging)
}

// encodeEditSegments 逐段生成 mkv 片段，再用 concat demuxer -c copy 拼成 final.part。
func (s *VideoEditService) encodeEditSegments(ctx context.Context, ffmpeg string, plan editPlan, workdir string, reporter *editProgressReporter) (string, error) {
	blank := filepath.Join(workdir, "blank.srt")
	if err := os.WriteFile(blank, []byte("1\n00:00:00,000 --> 00:00:00,040\n​\n"), 0o644); err != nil {
		return "", editError("encode_failed", "无法写入工作目录")
	}
	names := make([]string, 0, len(plan.Segments))
	done := int64(0)
	total := max(plan.DurationMS, 1)
	for index, segment := range plan.Segments {
		name := fmt.Sprintf("seg_%04d.mkv", index)
		names = append(names, name)
		args := editSegmentArgs(plan, segment, filepath.Join(workdir, name), blank)
		tail, err := s.runProgress(ctx, ffmpeg, args, func(outMS int64) {
			reporter.report(ctx, models.VideoEditPhaseEncode, 0.85*float64(done+min(outMS, segment.DurMS))/float64(total), false)
		})
		if err != nil {
			return "", editFFmpegError(ctx, err, tail, "encode_failed")
		}
		if info, statErr := os.Stat(filepath.Join(workdir, name)); statErr != nil || info.Size() == 0 {
			return "", editError("encode_failed", "第 %d 段输出为空", index+1)
		}
		done += segment.DurMS
	}
	list := filepath.Join(workdir, "segments.ffconcat")
	if err := os.WriteFile(list, []byte(editConcatList(names)), 0o644); err != nil {
		return "", editError("encode_failed", "无法写入拼接列表")
	}
	staging := filepath.Join(workdir, "final.part")
	reporter.report(ctx, models.VideoEditPhaseConcat, 0.85, true)
	tail, err := s.runProgress(ctx, ffmpeg, editConcatArgs(plan, list, staging), func(outMS int64) {
		reporter.report(ctx, models.VideoEditPhaseConcat, 0.85+0.1*float64(min(outMS, total))/float64(total), false)
	})
	if err != nil {
		return "", editFFmpegError(ctx, err, tail, "encode_failed")
	}
	return staging, nil
}

// editFFmpegError 把 ffmpeg 失败翻成错误码：取消原样返回 ctx 错误，空间不足为 disk_full。
func editFFmpegError(ctx context.Context, err error, tail, code string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if enhancementNoSpace(err) || strings.Contains(strings.ToLower(tail), "no space left on device") {
		return editError("disk_full", "磁盘空间不足")
	}
	// 原文留给 finishItem 按计划里的已知路径擦除（这里先擦会把带空格的路径截成半截）。
	return editError(code, "%v: %s", err, tail)
}

// verifyEditOutput：ffprobe 读成品，时长与计划差 ≤ max(0.5 秒, 0.5%)，各类流数量与计划一致。
func (s *VideoEditService) verifyEditOutput(ctx context.Context, plan editPlan, staging string) error {
	probe, err := s.probeEditSource(ctx, staging)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return editError("verify_failed", "成品不可探测：%s", err.Error())
	}
	if probe.Video == nil {
		return editError("verify_failed", "成品没有视频流")
	}
	tolerance := max(int64(500), plan.DurationMS/200)
	if diff := probe.DurationMS - plan.DurationMS; diff > tolerance || diff < -tolerance {
		return editError("verify_failed", "成品时长 %s 与计划 %s 相差过大", editClock(probe.DurationMS), editClock(plan.DurationMS))
	}
	if len(probe.Audio) != len(plan.Audio) || len(probe.Subtitles) != len(plan.Subtitles) {
		return editError("verify_failed", "成品音轨/字幕轨数量（%d/%d）与计划（%d/%d）不一致",
			len(probe.Audio), len(probe.Subtitles), len(plan.Audio), len(plan.Subtitles))
	}
	if plan.Container == "mkv" && len(probe.Attachments) != len(plan.Attachments) {
		return editError("verify_failed", "成品附件数量与计划不一致")
	}
	return nil
}

// publishEditOutput 不覆盖地发布并入库（合同第 5 步）：在 lockLibraryPaths 内选不冲突的文件名，先把
// publish_target 与 staged_size 写入项行，再 rename，然后单事务建 videos 行、同步短视频标签并把项置
// completed。事务失败把文件移回工作目录并报 publish_failed。发布一旦开始不再响应取消。
func (s *VideoEditService) publishEditOutput(ctx context.Context, item models.VideoEditItem, plan editPlan, staging string) error {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := fsyncEnhancementFile(staging); err != nil {
		return editError("publish_failed", "发布前同步成品失败")
	}
	info, err := os.Stat(staging)
	if err != nil {
		return editError("publish_failed", "成品不可读")
	}
	release, err := lockLibraryPaths()
	if err != nil {
		return editError("publish_failed", "片库正在维护，暂时无法发布")
	}
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			release()
		}
	}
	defer releaseOnce()
	name, target, err := s.renameEditOutputExclusive(bg, item.ID, plan, staging, info.Size())
	if err != nil {
		return err
	}
	_ = syncSubtitleParentDirectory(plan.OutputDir)
	output, err := s.ingestEditOutput(bg, item.ID, target, name, info.Size())
	if err != nil {
		if moveErr := os.Rename(target, staging); moveErr != nil {
			_ = os.Remove(target)
		}
		_ = s.updateEditItem(bg, item.ID, map[string]any{"publish_target": "", "staged_size": 0})
		return editError("publish_failed", "成品入库失败：%s", err.Error())
	}
	releaseOnce()
	s.afterEditPublish(bg, item.ID, plan, output)
	return nil
}

// ingestEditOutput 是发布的单一事务：建 videos 行、同步短视频标签、项置 completed。不建同源关系、不复制元数据。
func (s *VideoEditService) ingestEditOutput(ctx context.Context, itemID uint, target, name string, size int64) (models.Video, error) {
	output := models.Video{Name: name, Path: target, Directory: filepath.Dir(target), Size: size}
	now := s.now()
	err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		if err := tx.Create(&output).Error; err != nil {
			return err
		}
		if err := syncShortVideoTagForVideo(tx, output.ID); err != nil {
			return err
		}
		if s.publishTxHook != nil {
			if err := s.publishTxHook(tx); err != nil {
				return err
			}
		}
		outputID := output.ID
		result := tx.Model(&models.VideoEditItem{}).Where("id = ? AND status = ?", itemID, models.VideoEditStatusRunning).
			Updates(map[string]any{"status": models.VideoEditStatusCompleted, "phase": models.VideoEditPhaseDone, "progress": 1.0,
				"output_video_id": &outputID, "output_path": target, "output_name": name, "finished_at": &now,
				"error_code": "", "error_message": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("导出项状态已变化")
		}
		return nil
	})
	return output, err
}

// afterEditPublish 在事务外刷新技术快照（再同步短视频标签）、写旁挂字幕（失败只写警告）并删工作目录。
func (s *VideoEditService) afterEditPublish(ctx context.Context, itemID uint, plan editPlan, output models.Video) {
	if s.probe != nil {
		if err := s.probe.Refresh(ctx, output.ID); err != nil {
			logVideoEdit("item=%d output=%d technical snapshot deferred: %s", itemID, output.ID, scrubEditMessage(err.Error()))
		} else if err := database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
			return syncShortVideoTagForVideo(tx, output.ID)
		}); err != nil {
			logVideoEdit("item=%d output=%d short-video tag sync deferred: %s", itemID, output.ID, scrubEditMessage(err.Error()))
		}
	}
	warning := s.writeEditSidecar(ctx, plan, output)
	removeEditWorkdir(editWorkdirFor(plan, itemID))
	db, err := editDB(ctx)
	if err != nil {
		return
	}
	_ = db.Model(&models.VideoEditItem{}).Where("id = ? AND status = ?", itemID, models.VideoEditStatusCompleted).
		Updates(map[string]any{"work_dir": "", "publish_target": "", "staged_size": 0, "error_message": warning}).Error
	logVideoEdit("item=%d published output=%d", itemID, output.ID)
}

// reconcilePublishedItem 处理 rename 之后、入库事务之前崩溃的项：publish_target 存在、大小等于
// staged_size 且库里没有该路径的记录时补做入库事务并返回 true；否则返回 false（调用方删工作目录、
// 标中断，publish_target 指向的文件不删）。
func (s *VideoEditService) reconcilePublishedItem(ctx context.Context, item models.VideoEditItem) bool {
	info, err := os.Lstat(item.PublishTarget)
	if err != nil || !info.Mode().IsRegular() || info.Size() != item.StagedSize || !filepath.IsAbs(item.PublishTarget) {
		return false
	}
	var plan editPlan
	if err := json.Unmarshal([]byte(item.PlanJSON), &plan); err != nil {
		return false
	}
	release, err := lockLibraryPaths()
	if err != nil {
		return false
	}
	db, err := editDB(ctx)
	if err != nil {
		release()
		return false
	}
	var rows []models.Video
	if err := withoutTrashTombstones(db.Unscoped().Where("path = ?", item.PublishTarget), videoTrashKind).Limit(1).Find(&rows).Error; err != nil || len(rows) > 0 {
		release()
		return false
	}
	output, err := s.ingestEditOutput(ctx, item.ID, item.PublishTarget, filepath.Base(item.PublishTarget), info.Size())
	release()
	if err != nil {
		logVideoEdit("item=%d reconcile ingest failed: %s", item.ID, scrubEditMessage(err.Error()))
		return false
	}
	s.afterEditPublish(ctx, item.ID, plan, output)
	logVideoEdit("item=%d reconciled after crash output=%d", item.ID, output.ID)
	return true
}

// SweepOrphanWorkdirs 走遍扫描根，删掉名字严格匹配 .cineinsight-edit-<id> 且对应项不在运行的目录
// （启动对账之后调用一次）。名字不合规的同前缀目录不碰；读库失败时保留。
func (s *VideoEditService) SweepOrphanWorkdirs(ctx context.Context) (removed int, err error) {
	db, err := editDB(ctx)
	if err != nil {
		return 0, err
	}
	var dirs []models.ScanDirectory
	if err := db.Find(&dirs).Error; err != nil {
		return 0, err
	}
	for _, root := range cleanScanRoots(dirs) {
		walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if walkErr != nil {
				if entry != nil && entry.IsDir() && path != root {
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), editWorkdirPrefix) {
				return nil
			}
			id, ok := editWorkdirItemID(entry.Name())
			if !ok || s.editWorkdirInUse(ctx, id) {
				return filepath.SkipDir
			}
			if os.RemoveAll(path) == nil {
				removed++
			}
			return filepath.SkipDir
		})
		if walkErr != nil && ctx.Err() != nil {
			return removed, ctx.Err()
		}
	}
	if removed > 0 {
		logVideoEdit("orphan workdir sweep removed=%d", removed)
	}
	return removed, nil
}

func (s *VideoEditService) editWorkdirInUse(ctx context.Context, itemID uint) bool {
	s.mu.Lock()
	current := s.currentItemID == itemID
	s.mu.Unlock()
	if current {
		return true
	}
	item, err := loadEditItemByID(ctx, itemID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false
	}
	return err != nil || item.Status == models.VideoEditStatusRunning
}

// editPublishRenameAttempts 是排他 rename 撞上「刚被占用」时重新选名的次数上限。
const editPublishRenameAttempts = 5

// renameEditOutputExclusive 选一个空闲的成品名、先把 publish_target/staged_size 写入项行，再用排他 rename
// （editRenameNoReplace：darwin/linux 用与集中整理相同的排他 rename，其他平台在路径写锁内 Lstat 后 rename）落位：
// 选名与 rename 之间目标被别人占用时返回 EEXIST，绝不覆盖，换下一个空闲名字重试。
func (s *VideoEditService) renameEditOutputExclusive(ctx context.Context, itemID uint, plan editPlan, staging string, size int64) (string, string, error) {
	for attempt := 1; ; attempt++ {
		name, free, err := firstFreeEditOutputName(ctx, plan.OutputDir, plan.OutputBase, plan.OutputExt)
		if err != nil {
			return "", "", editError("publish_failed", "无法检查输出文件名：%s", err.Error())
		}
		if !free {
			return "", "", editError("output_conflict", "输出文件名「%s%s」及 (2)…(99) 均已被占用", plan.OutputBase, plan.OutputExt)
		}
		target := filepath.Join(plan.OutputDir, name)
		if err := s.updateEditItem(ctx, itemID, map[string]any{"publish_target": target, "staged_size": size,
			"output_name": name, "phase": models.VideoEditPhasePublish}); err != nil {
			return "", "", editError("publish_failed", "无法记录发布目标")
		}
		if s.beforePublishRename != nil {
			s.beforePublishRename(target)
		}
		err = editRenameNoReplace(staging, target)
		if err == nil {
			return name, target, nil
		}
		_ = s.updateEditItem(ctx, itemID, map[string]any{"publish_target": "", "staged_size": 0})
		if !errors.Is(err, os.ErrExist) {
			return "", "", editError("publish_failed", "移动成品失败：%s", err.Error())
		}
		if attempt >= editPublishRenameAttempts {
			return "", "", editError("output_conflict", "输出文件名在发布时反复被占用，已放弃以免覆盖")
		}
	}
}
