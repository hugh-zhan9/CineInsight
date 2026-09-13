package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"video-master/services/subtitleparser"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// 对已经生成好的外挂 SRT 单独发起翻译：目标语言按次指定，产物覆盖同名 .srt。
// 复用生成流程那一套翻译器、术语表与滑动窗口上文，区别只在于不碰 ASR，
// 也就不必排进转写队列。

type SubtitleTranslateMode string

const (
	// SubtitleTranslateModeTranslatedOnly 只保留译文，原文被替换掉。
	SubtitleTranslateModeTranslatedOnly SubtitleTranslateMode = "translated_only"
	// SubtitleTranslateModeBilingual 上行原文、下行译文，与生成流程的双语产物一致。
	SubtitleTranslateModeBilingual SubtitleTranslateMode = "bilingual"
)

// errSubtitleTranslationCancelled 是用户主动取消的统一措辞，前端据此区分取消与失败。
var errSubtitleTranslationCancelled = errors.New("字幕翻译已取消，原字幕未改动")

// translationCancelEntry 让同一个视频可以同时存在多条登记（例如一条在等锁、
// 一条在翻译），取消时一并叫停，注销时只摘掉自己那条。
type translationCancelEntry struct {
	cancel context.CancelFunc
}

type SubtitleTranslateRequest struct {
	VideoID    uint                  `json:"video_id"`
	SourceLang string                `json:"source_lang"`
	TargetLang string                `json:"target_lang"`
	Mode       SubtitleTranslateMode `json:"mode"`
}

type SubtitleTranslateResult struct {
	VideoID    uint                  `json:"video_id"`
	Path       string                `json:"path"`
	Mode       SubtitleTranslateMode `json:"mode"`
	TargetLang string                `json:"target_lang"`
	Entries    int                   `json:"entries"`
	Warnings   []string              `json:"warnings,omitempty"`
}

// CancelSubtitleTranslation 叫停某个视频正在跑的字幕翻译；没有在跑就什么都不做。
func (s *SubtitleService) CancelSubtitleTranslation(videoID uint) {
	s.mu.Lock()
	entries := append([]*translationCancelEntry(nil), s.translationCancels[videoID]...)
	s.mu.Unlock()
	for _, entry := range entries {
		entry.cancel()
	}
}

// registerTranslationCancel 登记一个可取消的翻译任务。登记发生在抢字幕文件锁之前，
// 这样「排队等锁」期间点取消也算数；等锁本身不认 ctx，所以拿到锁后要立刻复查一次。
func (s *SubtitleService) registerTranslationCancel(ctx context.Context, videoID uint) (context.Context, func()) {
	translationCtx, cancel := context.WithCancel(ctx)
	entry := &translationCancelEntry{cancel: cancel}
	s.mu.Lock()
	if s.translationCancels == nil {
		s.translationCancels = make(map[uint][]*translationCancelEntry)
	}
	s.translationCancels[videoID] = append(s.translationCancels[videoID], entry)
	s.mu.Unlock()
	return translationCtx, func() {
		s.mu.Lock()
		remaining := s.translationCancels[videoID][:0]
		for _, candidate := range s.translationCancels[videoID] {
			if candidate != entry {
				remaining = append(remaining, candidate)
			}
		}
		if len(remaining) == 0 {
			delete(s.translationCancels, videoID)
		} else {
			s.translationCancels[videoID] = remaining
		}
		s.mu.Unlock()
		cancel()
	}
}

// TranslateSubtitleFile 翻译视频同目录下已有的 .srt，并按 mode 覆盖回同一个文件。
// 翻译先落临时文件，只有整轮成功才替换原字幕，失败时原文件保持不动。
func (s *SubtitleService) TranslateSubtitleFile(ctx context.Context, videoPath string, request SubtitleTranslateRequest, config SubtitleTranslationConfig) (*SubtitleTranslateResult, error) {
	if request.VideoID == 0 {
		return nil, fmt.Errorf("字幕翻译缺少视频 ID")
	}
	if strings.TrimSpace(videoPath) == "" {
		return nil, fmt.Errorf("字幕翻译缺少视频路径")
	}
	mode, err := normalizeSubtitleTranslateMode(request.Mode)
	if err != nil {
		return nil, err
	}
	targetLang := normalizeSubtitleLanguageCode(request.TargetLang)
	if targetLang == "" || targetLang == "auto" {
		return nil, fmt.Errorf("请选择字幕翻译的目标语言")
	}
	sourceLang := normalizeSubtitleLanguageCode(request.SourceLang)
	if sourceLang == "auto" || sourceLang == "unknown" {
		sourceLang = ""
	}
	if sourceLang != "" && sourceLang == targetLang {
		return nil, fmt.Errorf("源语言与目标语言相同，无需翻译")
	}

	srtPath := subtitleparser.SRTPathForVideo(videoPath)
	ctx, unregisterCancel := s.registerTranslationCancel(ctx, request.VideoID)
	defer unregisterCancel()

	// 等锁可能要等上几分钟（同余视频的字幕生成也持这把锁），先说一声，
	// 否则用户对着一个毫无解释的 0% 进度条。
	s.emitTranslateProgress(request.VideoID, 0, "排队等待该视频的字幕任务...")
	unlock := lockSubtitleFile(request.VideoID)
	defer unlock()
	if ctx.Err() != nil {
		return nil, errSubtitleTranslationCancelled
	}

	info, err := os.Stat(srtPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("该视频还没有外挂字幕，请先生成字幕")
		}
		return nil, fmt.Errorf("读取字幕文件失败: %w", err)
	}
	entries, err := parseSRTEntries(srtPath)
	if err != nil {
		return nil, fmt.Errorf("读取字幕文件失败: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("字幕文件为空，无法翻译")
	}

	provider := normalizeSubtitleTranslationProvider(config.Provider)
	translator, err := s.subtitleTranslator(provider, config)
	if err != nil {
		return nil, err
	}
	// 术语表只对吃得下它的翻译器解析，DeepL 用不上就不该多出一条读库失败路径（D-034）。
	var glossary []GlossaryTerm
	if _, injectable := translator.(ContextualTranslator); injectable {
		resolved, resolveErr := s.glossaryResolver(request.VideoID)
		if resolveErr != nil {
			return nil, fmt.Errorf("读取术语表失败: %w", resolveErr)
		}
		glossary = resolved
	}

	log.Printf("[Subtitle] translate existing subtitle video_id=%d entries=%d source=%q target=%s mode=%s provider=%s",
		request.VideoID, len(entries), sourceLang, targetLang, mode, provider)

	s.emitTranslateProgress(request.VideoID, 0, fmt.Sprintf("准备翻译 %d 条字幕...", len(entries)))

	translatedPath := strings.TrimSuffix(srtPath, filepath.Ext(srtPath)) + "_translated_temp.srt"
	defer os.Remove(translatedPath)

	onBatch := func(done, total int) {
		if total <= 0 {
			return
		}
		percent := done * 90 / total
		s.emitTranslateProgress(request.VideoID, percent, fmt.Sprintf("已翻译 %d/%d 条字幕...", done, total))
	}
	fallbackCount, err := s.translateSRTWithProgress(ctx, srtPath, translatedPath, sourceLang, targetLang, translator, glossary, onBatch)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errSubtitleTranslationCancelled
		}
		return nil, fmt.Errorf("字幕翻译失败，已保留原字幕: %w", err)
	}

	warnings := []string{}
	if fallbackCount > 0 {
		warnings = append(warnings, subtitleFallbackWarning(fallbackCount))
	}

	s.emitTranslateProgress(request.VideoID, 92, "写回字幕文件...")
	if mode == SubtitleTranslateModeBilingual {
		if err := s.mergeBilingualSRT(srtPath, translatedPath, srtPath); err != nil {
			return nil, fmt.Errorf("双语字幕合并失败，已保留原字幕: %w", err)
		}
		// mergeBilingualSRT 是给「新生成的字幕」写的，权限固定 0644；这条路径改的
		// 是用户已有的文件，不该顺手放宽它的权限。
		if err := os.Chmod(srtPath, info.Mode().Perm()); err != nil {
			log.Printf("[Subtitle] restore subtitle permission failed path=%s err=%v", srtPath, err)
		}
	} else {
		translated, readErr := os.ReadFile(translatedPath)
		if readErr != nil {
			return nil, fmt.Errorf("读取译文字幕失败，已保留原字幕: %w", readErr)
		}
		if err := writeFileAtomically(srtPath, translated, info.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("写入译文字幕失败，已保留原字幕: %w", err)
		}
	}

	if err := indexSubtitleFileForVideoID(request.VideoID, srtPath); err != nil {
		log.Printf("[Subtitle] index translated subtitle failed video_id=%d path=%s err=%v", request.VideoID, srtPath, err)
		warnings = append(warnings, fmt.Sprintf("字幕索引更新失败：%v", err))
	}

	s.emitTranslateProgress(request.VideoID, 100, "字幕翻译完成")
	return &SubtitleTranslateResult{
		VideoID:    request.VideoID,
		Path:       srtPath,
		Mode:       mode,
		TargetLang: targetLang,
		Entries:    len(entries),
		Warnings:   warnings,
	}, nil
}

func normalizeSubtitleTranslateMode(mode SubtitleTranslateMode) (SubtitleTranslateMode, error) {
	switch SubtitleTranslateMode(strings.TrimSpace(string(mode))) {
	case SubtitleTranslateModeTranslatedOnly:
		return SubtitleTranslateModeTranslatedOnly, nil
	case SubtitleTranslateModeBilingual:
		return SubtitleTranslateModeBilingual, nil
	default:
		return "", fmt.Errorf("不支持的字幕翻译输出形态: %q", string(mode))
	}
}

func (s *SubtitleService) emitTranslateProgress(videoID uint, percent int, message string) {
	if s.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(s.ctx, "subtitle-translate-progress", map[string]interface{}{
		"videoID": videoID,
		"percent": percent,
		"message": message,
	})
}
