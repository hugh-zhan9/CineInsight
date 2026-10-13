<template>
  <DialogShell v-if="subtitlePreview.show" title="字幕预览" size="lg" class="subtitle-preview-modal" @close="subtitlePreview.show = false">
      <p v-if="subtitlePreview.video" class="subtitle-preview-name">{{ subtitlePreview.video.name }}</p>

      <SkeletonBlock v-if="subtitlePreview.loading" class="subtitle-preview-loading" :lines="4" />
      <p v-else-if="subtitlePreview.error" class="subtitle-preview-error" role="alert" data-test="subtitle-preview-error">{{ subtitlePreview.error }}</p>
      <div v-else-if="subtitlePreview.segments.length" class="subtitle-preview-list">
        <div
          v-for="segment in subtitlePreview.segments"
          :key="`${segment.index}-${segment.start_time_ms}`"
          :class="['subtitle-segment', { 'subtitle-segment-match': segmentMatchesKeyword(segment) }]"
        >
          <div class="subtitle-segment-time">
            {{ formatTimestamp(segment.start_time_ms) }} - {{ formatTimestamp(segment.end_time_ms) }}
            <AppBadge v-if="segmentMatchesKeyword(segment)" tone="accent" class="subtitle-match-badge">命中</AppBadge>
          </div>
          <div class="subtitle-segment-text">{{ segment.text }}</div>
        </div>
      </div>
      <EmptyState v-else title="当前视频还没有可预览的字幕片段" />

      <template #footer>
        <button type="button" class="btn btn--secondary" @click="subtitlePreview.show = false">关闭</button>
      </template>
  </DialogShell>
</template>

<script>
import { GetSubtitleSegments } from '../../../wailsjs/go/main/App';
import AppBadge from '../ui/AppBadge.vue';
import DialogShell from '../ui/DialogShell.vue';
import EmptyState from '../ui/EmptyState.vue';
import SkeletonBlock from '../ui/SkeletonBlock.vue';
import { subtitleExceptionText } from '../../utils/subtitleTools.js';

const MISSING_SIDECAR_TEXT = '这个视频没有同名 .srt 字幕。内嵌字幕或其他格式（.ass、.vtt 等）的字幕暂不支持在这里预览、编辑或翻译。';

// 字幕预览弹窗（行菜单的「预览字幕」）。命中高亮跟着片库页当前的搜索词与搜索模式走，
// 所以这两项作为 prop 传进来。
export default {
  name: 'SubtitlePreviewModal',
  components: { AppBadge, DialogShell, EmptyState, SkeletonBlock },
  props: {
    searchKeyword: { type: String, default: '' },
    searchMode: { type: String, default: 'file' }
  },
  data() {
    return {
      subtitlePreview: { show: false, loading: false, error: '', video: null, segments: [] }
    };
  },
  methods: {
    open(video) {
      return this.openSubtitlePreview(video);
    },
    async openSubtitlePreview(video) {
      this.subtitlePreview = { show: true, loading: true, error: '', video, segments: [] };
      try {
        const segments = await GetSubtitleSegments(video.id);
        this.subtitlePreview.segments = segments || [];
      } catch (err) {
        console.error('读取字幕片段失败:', err);
        // 没有同名 .srt 是最常见的原因：说清预览只认同名 .srt（D-PC17），不把系统错误与路径原样抛给用户（G-3）。
        const raw = String(err?.message || err || '');
        this.subtitlePreview.error = /no such file or directory/i.test(raw)
          ? MISSING_SIDECAR_TEXT
          : `读取字幕片段失败：${subtitleExceptionText(err)}`;
      } finally {
        this.subtitlePreview.loading = false;
      }
    },
    segmentMatchesKeyword(segment) {
      const keyword = this.searchKeyword.trim().toLowerCase();
      if (!keyword || this.searchMode !== 'subtitle') {
        return false;
      }
      return (segment?.text || '').toLowerCase().includes(keyword);
    },
    formatTimestamp(ms) {
      if (ms === null || ms === undefined) return '00:00:00';
      const totalSeconds = Math.floor(ms / 1000);
      const hours = Math.floor(totalSeconds / 3600);
      const minutes = Math.floor((totalSeconds % 3600) / 60);
      const seconds = totalSeconds % 60;
      return [hours, minutes, seconds].map(value => String(value).padStart(2, '0')).join(':');
    },
  }
};
</script>

<style scoped>
.subtitle-preview-name {
  margin: 0 0 var(--space-3);
  color: var(--text-secondary);
  font-size: var(--fs-secondary);
  overflow-wrap: anywhere;
}
.subtitle-preview-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.subtitle-segment {
  border: 1px solid var(--review-border-color);
  border-radius: var(--radius);
  padding: var(--space-3) 14px;
  background: var(--review-solid-bg);
}
.subtitle-segment-match {
  border-color: var(--accent-deep);
  background: var(--review-accent-soft);
}
.subtitle-segment-time {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--fs-caption);
  color: var(--review-text-muted);
  margin-bottom: 6px;
}
.subtitle-segment-text {
  white-space: pre-wrap;
  line-height: 1.5;
}
.subtitle-preview-error {
  margin: 0;
  color: var(--danger-text);
  font-size: var(--fs-secondary);
}
</style>
