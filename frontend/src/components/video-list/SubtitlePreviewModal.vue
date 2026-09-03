<template>
  <BaseModal v-if="subtitlePreview.show" class="subtitle-preview-modal">
      <h3>字幕预览</h3>
      <p class="cleanup-intro" v-if="subtitlePreview.video">{{ subtitlePreview.video.name }}</p>

      <div v-if="subtitlePreview.loading" class="cleanup-loading">正在读取字幕片段...</div>
      <div v-else-if="subtitlePreview.error" class="cleanup-error">{{ subtitlePreview.error }}</div>
      <div v-else-if="subtitlePreview.segments.length" class="subtitle-preview-list">
        <div
          v-for="segment in subtitlePreview.segments"
          :key="`${segment.index}-${segment.start_time_ms}`"
          :class="['subtitle-segment', { 'subtitle-segment-match': segmentMatchesKeyword(segment) }]"
        >
          <div class="subtitle-segment-time">
            {{ formatTimestamp(segment.start_time_ms) }} - {{ formatTimestamp(segment.end_time_ms) }}
            <span v-if="segmentMatchesKeyword(segment)" class="subtitle-match-badge">命中</span>
          </div>
          <div class="subtitle-segment-text">{{ segment.text }}</div>
        </div>
      </div>
      <div v-else class="cleanup-empty">当前视频还没有可预览的字幕片段。</div>

      <div class="modal-actions">
        <button @click="subtitlePreview.show = false" class="btn-primary">关闭</button>
      </div>
  </BaseModal>
</template>

<script>
import { GetSubtitleSegments } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';

// 字幕预览弹窗（行菜单的「预览字幕」）。命中高亮跟着片库页当前的搜索词与搜索模式走，
// 所以这两项作为 prop 传进来。
export default {
  name: 'SubtitlePreviewModal',
  components: { BaseModal },
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
        this.subtitlePreview.error = '读取字幕片段失败: ' + err;
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
:deep(.subtitle-preview-modal) {
  width: 760px;
  max-width: calc(100vw - 32px);
  max-height: calc(100vh - 48px);
  overflow-y: auto;
  padding: 28px;
}
.subtitle-preview-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-top: 16px;
}
.subtitle-segment {
  border: 1px solid var(--review-border-color);
  border-radius: 10px;
  padding: 12px 14px;
  background: var(--review-solid-bg);
}
.subtitle-segment-match {
  border-color: var(--accent-deep);
  background: var(--review-accent-soft);
}
.subtitle-segment-time {
  font-size: 12px;
  color: var(--review-text-muted);
  margin-bottom: 6px;
}
.subtitle-segment-text {
  white-space: pre-wrap;
  line-height: 1.5;
}
.subtitle-match-badge {
  display: inline-block;
  margin-left: 8px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--review-accent-badge-bg);
  color: var(--accent-deep);
}
.cleanup-error,
.cleanup-intro,
.cleanup-loading,
.cleanup-empty {
  color: var(--review-text-muted);
  font-size: 13px;
}
</style>
