<template>
  <div v-if="visible" class="modal-overlay">
    <div class="modal ai-tag-review-modal">
      <div class="ai-tag-review-header">
        <div>
          <h3>AI 标签管理</h3>
          <p class="help-text">待审 {{ candidates.length }} 条<span v-if="reviewSearch.trim()">，当前显示 {{ filteredCandidates.length }} 条</span>，高置信和中置信需人工确认后才会写入正式标签。</p>
          <p v-if="summary && !summary.config_available" class="ai-tag-warning">AI 配置不可用，后台分析已暂停。</p>
        </div>
        <button type="button" class="btn-secondary" @click="$emit('close')">关闭</button>
      </div>

      <div class="ai-tag-review-actions">
        <input
          v-model="reviewSearch"
          type="search"
          class="search-input ai-tag-review-search"
          placeholder="搜索视频、路径、候选标签"
        />
        <button type="button" class="btn-secondary" @click="loadCandidates" :disabled="loading">刷新</button>
      </div>

      <div v-if="loading" class="ai-tag-review-empty">加载中...</div>
      <div v-else-if="error" class="ai-tag-review-error">{{ error }}</div>
      <div v-else-if="groups.length === 0" class="ai-tag-review-empty">{{ reviewSearch.trim() ? '没有匹配的待审 AI 标签' : '暂无待审 AI 标签' }}</div>
      <div v-else class="ai-tag-review-list">
        <section v-for="group in groups" :key="group.videoId" class="ai-video-group">
          <div class="ai-video-title">
            <span>{{ group.videoName }}</span>
            <div class="ai-video-actions">
              <button type="button" class="btn-action btn-compact" @click="previewVideo(group.videoId)" :disabled="processingIds.includes(`preview-${group.videoId}`)">预览视频</button>
              <button type="button" class="btn-secondary btn-compact" @click="openRenameDialog(group)" :disabled="processingIds.includes(`rename-${group.videoId}`)">重命名</button>
              <button type="button" class="btn-secondary btn-compact" @click="rejectVideoGroup(group)" :disabled="processingIds.includes(`reject-video-${group.videoId}`)">全部拒绝</button>
              <button type="button" class="btn-action btn-compact" @click="retryVideo(group.videoId)" :disabled="processingIds.includes(group.videoId)">重新分析</button>
            </div>
          </div>
          <div v-if="group.videoPath" class="ai-video-path">{{ group.videoPath }}</div>

          <div
            v-for="candidate in group.candidates"
            :key="candidate.id"
            class="ai-candidate-row"
          >
            <div class="ai-candidate-main">
              <span
                class="ai-confidence"
                :class="confidenceMeta(candidate.confidence).className"
                :data-confidence="candidate.confidence"
              >
                {{ confidenceMeta(candidate.confidence).label }}
              </span>
              <span class="ai-candidate-name">{{ candidate.suggested_name }}</span>
              <span v-if="candidate.matched_tag" class="ai-match-note">匹配已有：{{ candidate.matched_tag.name }}</span>
              <span v-else class="ai-match-note">新标签候选</span>
              <p v-if="candidate.reasoning" class="ai-candidate-reason">{{ candidate.reasoning }}</p>
            </div>
            <div class="ai-candidate-actions">
              <button type="button" class="btn-primary" @click="approve(candidate)" :disabled="processingIds.includes(candidate.id)">批准</button>
              <button type="button" class="btn-secondary" @click="reject(candidate)" :disabled="processingIds.includes(candidate.id)">拒绝</button>
            </div>
          </div>
        </section>
      </div>

      <div v-if="rejectConfirm.show" class="ai-confirm-overlay">
        <div class="ai-confirm-dialog glass-surface">
          <h4>确认全部拒绝</h4>
          <p>将拒绝这个视频下的 {{ rejectConfirm.count }} 个 AI 标签候选。</p>
          <p class="ai-confirm-video">{{ rejectConfirm.videoName }}</p>
          <div class="ai-confirm-actions">
            <button type="button" class="btn-secondary" @click="cancelRejectVideoGroup">取消</button>
            <button type="button" class="btn-danger" @click="confirmRejectVideoGroup" :disabled="processingIds.includes(`reject-video-${rejectConfirm.videoId}`)">全部拒绝</button>
          </div>
        </div>
      </div>

      <div v-if="renameConfirm.show" class="ai-confirm-overlay">
        <div class="ai-confirm-dialog glass-surface">
          <h4>重命名视频</h4>
          <input
            v-model="renameConfirm.newName"
            type="text"
            class="text-input ai-tag-rename-input"
            placeholder="输入新文件名"
            @keyup.enter="renameConfirmVideo"
            ref="renameInput"
          />
          <p>扩展名会自动保留（{{ renameConfirm.ext }}）</p>
          <p class="ai-confirm-video">{{ renameConfirm.videoName }}</p>
          <div class="ai-confirm-actions">
            <button type="button" class="btn-secondary" @click="cancelRenameDialog">取消</button>
            <button type="button" class="btn-primary" @click="renameConfirmVideo" :disabled="processingIds.includes(`rename-${renameConfirm.videoId}`)">确认</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { ApproveAITagCandidate, GetAITaggingStatusSummary, ListAITagCandidates, PreviewExternally, RejectAITagCandidate, RejectAITagCandidatesByVideo, RenameVideo, RetryAITagging } from '../../wailsjs/go/main/App';
import { confidenceMeta, createRejectVideoConfirm, filterCandidatesForReview, groupCandidatesByVideo, removeCandidateById } from '../utils/aiTagReview.js';

export default {
  name: 'AITagReviewDialog',
  props: {
    visible: { type: Boolean, default: false },
  },
  emits: ['close', 'changed'],
  data() {
    return {
      candidates: [],
      summary: null,
      loading: false,
      error: '',
      reviewSearch: '',
      processingIds: [],
      rejectConfirm: { show: false, videoId: 0, videoName: '', count: 0, candidateIds: [] },
      renameConfirm: { show: false, videoId: 0, videoName: '', videoPath: '', newName: '', ext: '(无)' },
    };
  },
  computed: {
    filteredCandidates() {
      return filterCandidatesForReview(this.candidates, this.reviewSearch);
    },
    groups() {
      return groupCandidatesByVideo(this.filteredCandidates);
    },
  },
  watch: {
    visible(value) {
      if (value) {
        this.loadCandidates();
      }
    },
  },
  methods: {
    confidenceMeta,
    async loadCandidates() {
      this.loading = true;
      this.error = '';
      try {
        const [summary, candidates] = await Promise.all([
          GetAITaggingStatusSummary(),
          ListAITagCandidates(0, '', 'pending'),
        ]);
        this.summary = summary;
        this.candidates = Array.isArray(candidates) ? candidates : [];
      } catch (err) {
        this.error = '加载 AI 标签候选失败: ' + err;
      } finally {
        this.loading = false;
      }
    },
    async approve(candidate) {
      await this.withProcessing(candidate.id, async () => {
        const item = await ApproveAITagCandidate(candidate.id);
        this.candidates = removeCandidateById(this.candidates, candidate.id);
        if (item?.status === 'superseded') {
          await this.loadCandidates();
        }
        this.$emit('changed');
      });
    },
    async reject(candidate) {
      await this.withProcessing(candidate.id, async () => {
        await RejectAITagCandidate(candidate.id);
        this.candidates = removeCandidateById(this.candidates, candidate.id);
      });
    },
    async rejectVideoGroup(group) {
      const confirmState = createRejectVideoConfirm(group);
      if (!confirmState) return;
      this.rejectConfirm = confirmState;
    },
    cancelRejectVideoGroup() {
      this.rejectConfirm = { show: false, videoId: 0, videoName: '', count: 0, candidateIds: [] };
    },
    async confirmRejectVideoGroup() {
      const videoId = this.rejectConfirm.videoId;
      if (!videoId) return;
      const candidateIds = [...this.rejectConfirm.candidateIds];
      await this.withProcessing(`reject-video-${videoId}`, async () => {
        await RejectAITagCandidatesByVideo(videoId);
        const rejectedIds = new Set(candidateIds);
        this.candidates = this.candidates.filter(candidate => !rejectedIds.has(Number(candidate.id)));
        this.cancelRejectVideoGroup();
        this.$emit('changed');
      });
    },
    async retryVideo(videoId) {
      await this.withProcessing(videoId, async () => {
        await RetryAITagging(videoId);
        await this.loadCandidates();
      });
    },
    async previewVideo(videoId) {
      await this.withProcessing(`preview-${videoId}`, async () => {
        await PreviewExternally(videoId);
      });
    },
    openRenameDialog(group) {
      if (!group?.videoId) return;
      const name = group.videoName || '';
      const dotIndex = name.lastIndexOf('.');
      const ext = dotIndex > 0 ? name.substring(dotIndex) : '';
      const baseName = ext ? name.slice(0, -ext.length) : name;
      this.renameConfirm = {
        show: true,
        videoId: group.videoId,
        videoName: name,
        videoPath: group.videoPath || '',
        newName: baseName,
        ext: ext || '(无)',
      };
      this.$nextTick(() => {
        if (this.$refs.renameInput) this.$refs.renameInput.focus();
      });
    },
    cancelRenameDialog() {
      this.renameConfirm = { show: false, videoId: 0, videoName: '', videoPath: '', newName: '', ext: '(无)' };
    },
    async renameConfirmVideo() {
      const { videoId, videoName, videoPath, newName, ext } = this.renameConfirm;
      const trimmedName = String(newName || '').trim();
      if (!videoId || !trimmedName) return;
      await this.withProcessing(`rename-${videoId}`, async () => {
        await RenameVideo(videoId, trimmedName);
        const finalName = trimmedName + (ext !== '(无)' ? ext : '');
        const finalPath = this.renamedPath(videoPath, videoName, finalName);
        this.candidates = this.candidates.map(candidate => {
          if (Number(candidate.video_id) !== Number(videoId)) return candidate;
          return {
            ...candidate,
            video: {
              ...(candidate.video || {}),
              id: videoId,
              name: finalName,
              path: finalPath,
            },
          };
        });
        this.cancelRenameDialog();
        this.$emit('changed');
      });
    },
    renamedPath(path, oldName, newName) {
      if (!path) return '';
      if (oldName && path.endsWith(oldName)) {
        return path.slice(0, -oldName.length) + newName;
      }
      const separatorIndex = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'));
      if (separatorIndex >= 0) {
        return path.slice(0, separatorIndex + 1) + newName;
      }
      return newName;
    },
    async withProcessing(id, action) {
      if (this.processingIds.includes(id)) return;
      this.processingIds = [...this.processingIds, id];
      this.error = '';
      try {
        await action();
      } catch (err) {
        if (this.isStaleCandidateError(err)) {
          await this.loadCandidates();
          this.error = '这条候选已被处理或已过期，列表已刷新。';
        } else {
          this.error = String(err);
        }
      } finally {
        this.processingIds = this.processingIds.filter(item => item !== id);
      }
    },
    isStaleCandidateError(err) {
      const message = String(err?.message || err || '').toLowerCase();
      return message.includes('candidate is not pending') || message.includes('candidate is no longer pending');
    },
  },
};
</script>

<style scoped>
.ai-tag-review-modal {
  position: relative;
  width: min(760px, calc(100vw - 40px));
  max-width: 760px;
  max-height: min(720px, calc(100vh - 48px));
  overflow: hidden;
  overflow-x: hidden;
  display: flex;
  flex-direction: column;
}

.ai-tag-review-header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: flex-start;
  border-bottom: 1px solid var(--border-color);
  padding-bottom: 14px;
  min-width: 0;
}

.ai-tag-review-header > div {
  min-width: 0;
}

.ai-tag-review-actions {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 0;
  min-width: 0;
}

.ai-tag-review-search {
  flex: 1 1 auto;
  min-width: 180px;
}

.ai-tag-review-list {
  overflow-y: auto;
  overflow-x: hidden;
  padding-right: 4px;
  min-width: 0;
}

.ai-video-group {
  border-top: 1px solid var(--border-color);
  padding: 14px 0;
}

.ai-video-title {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  font-weight: 700;
  color: var(--text-primary);
  min-width: 0;
}

.ai-video-title > span {
  min-width: 0;
  overflow-wrap: anywhere;
}

.ai-video-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
  flex: 0 0 auto;
  max-width: 320px;
}

.ai-video-path {
  margin-top: 4px;
  color: var(--text-muted);
  font-size: 12px;
  overflow-wrap: anywhere;
}

.ai-candidate-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: start;
  gap: 14px;
  padding: 12px 0;
  border-top: 1px solid rgba(148, 163, 184, 0.22);
  min-width: 0;
}

.ai-candidate-main {
  min-width: 0;
  overflow-wrap: anywhere;
}

.ai-confidence {
  display: inline-flex;
  align-items: center;
  height: 24px;
  padding: 0 8px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 700;
  margin-right: 8px;
}

.ai-confidence--high {
  color: #065f46;
  background: rgba(16, 185, 129, 0.16);
  border: 1px solid rgba(16, 185, 129, 0.35);
}

.ai-confidence--medium {
  color: #92400e;
  background: rgba(245, 158, 11, 0.16);
  border: 1px solid rgba(245, 158, 11, 0.35);
}

.ai-confidence--unknown {
  color: var(--text-secondary);
  background: rgba(148, 163, 184, 0.16);
  border: 1px solid rgba(148, 163, 184, 0.35);
}

.ai-candidate-name {
  font-weight: 700;
  color: var(--text-primary);
}

.ai-match-note {
  margin-left: 8px;
  color: var(--text-muted);
  font-size: 12px;
  overflow-wrap: anywhere;
}

.ai-candidate-reason {
  margin: 8px 0 0;
  color: var(--text-secondary);
  font-size: 13px;
  line-height: 1.45;
}

.ai-candidate-actions {
  display: flex;
  gap: 8px;
  flex: 0 0 auto;
  align-items: flex-start;
}

.ai-tag-warning,
.ai-tag-review-error {
  color: var(--danger-color);
}

.ai-tag-review-empty {
  padding: 32px 0;
  text-align: center;
  color: var(--text-muted);
}

.ai-confirm-overlay {
  position: absolute;
  inset: 0;
  z-index: 3;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: rgba(15, 23, 42, 0.34);
  -webkit-backdrop-filter: blur(14px);
  backdrop-filter: blur(14px);
}

.ai-confirm-dialog {
  width: min(420px, 100%);
  padding: 22px;
  border-radius: var(--radius-lg);
}

.ai-confirm-dialog h4 {
  margin: 0 0 10px;
  color: var(--text-primary);
  font-size: 17px;
}

.ai-confirm-dialog p {
  margin: 0 0 10px;
  color: var(--text-secondary);
  line-height: 1.5;
}

.ai-tag-rename-input {
  margin: 8px 0 10px;
}

.ai-confirm-video {
  max-height: 84px;
  overflow: auto;
  padding: 10px;
  border-radius: 6px;
  background: rgba(148, 163, 184, 0.12);
  color: var(--text-primary) !important;
  overflow-wrap: anywhere;
}

.ai-confirm-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 16px;
}

@media (max-width: 640px) {
  .ai-tag-review-modal {
    width: calc(100vw - 24px);
    padding: 18px;
  }

  .ai-tag-review-header,
  .ai-tag-review-actions,
  .ai-video-title {
    flex-wrap: wrap;
  }

  .ai-video-actions {
    justify-content: flex-start;
    max-width: 100%;
  }

  .ai-candidate-row {
    grid-template-columns: minmax(0, 1fr);
  }

  .ai-candidate-actions {
    justify-content: flex-start;
  }
}
</style>
