<template>
  <div v-if="visible" class="modal-overlay">
    <div class="modal ai-tag-review-modal">
      <div class="ai-tag-review-header">
        <div>
          <h3>AI 标签管理</h3>
          <p class="help-text">AI 标签待审 {{ candidates.length }} 条，视频同源待审 {{ sameSourceRelations.length }} 条<span v-if="reviewSection === 'tags' && reviewSearch.trim()">，当前显示 {{ filteredCandidates.length }} 条标签</span>。</p>
          <p v-if="summary && !summary.config_available" class="ai-tag-warning">AI 配置不可用，后台分析已暂停。</p>
        </div>
        <button type="button" class="btn-secondary" @click="$emit('close')">关闭</button>
      </div>

      <nav class="ai-tag-review-tabs" aria-label="AI 管理视图">
        <button type="button" :class="{ active: activeTab === 'review' }" data-test="ai-review-tab" @click="activeTab = 'review'">待审工作台</button>
        <button v-if="qualityEnabled" type="button" :class="{ active: activeTab === 'quality' }" data-test="ai-quality-tab" @click="activeTab = 'quality'">质量评估</button>
      </nav>

      <template v-if="activeTab === 'review'">
        <nav class="ai-review-type-tabs" aria-label="待审工作台类型">
          <button type="button" :class="{ active: reviewSection === 'tags' }" data-test="ai-candidate-review-tab" @click="reviewSection = 'tags'">AI 标签待审 <span>{{ candidates.length }}</span></button>
          <button type="button" :class="{ active: reviewSection === 'same-source' }" data-test="same-source-review-tab" @click="reviewSection = 'same-source'">视频同源待审 <span>{{ sameSourceRelations.length }}</span></button>
        </nav>

        <div class="ai-tag-review-actions">
          <input
            v-if="reviewSection === 'tags'"
            v-model="reviewSearch"
            type="search"
            class="search-input ai-tag-review-search"
            placeholder="搜索视频、路径、候选标签"
          />
          <p v-else class="same-source-help">同源判断只提供证据；确认或删除后会退出当前待审列表。</p>
          <button type="button" class="btn-secondary" @click="loadCandidates" :disabled="loading">刷新</button>
        </div>

        <div class="ai-review-workbench-content" data-test="ai-review-scroll-area">
          <div v-if="loading" class="ai-tag-review-empty">加载中...</div>
          <div v-else-if="error" class="ai-tag-review-error">{{ error }}</div>
          <template v-else-if="reviewSection === 'same-source'">
            <section v-if="sameSourceRelations.length" class="same-source-review-section">
              <div v-for="relation in sameSourceRelations" :key="relation.id" class="same-source-row">
                <div class="same-source-main">
                  <div class="same-source-pair">
                    <article class="same-source-video-card">
                      <div :class="['same-source-thumbnail', { 'same-source-thumbnail--failed': thumbnailFailed(relation.video_a_id) }]">
                        <img
                          v-if="!thumbnailFailed(relation.video_a_id)"
                          :src="thumbnailURL(relation.video_a_id)"
                          :alt="`${relation.video_a?.name || `视频 ${relation.video_a_id}`} 缩略图`"
                          loading="lazy"
                          @error="markThumbnailFailed(relation.video_a_id)"
                        />
                        <span v-else aria-hidden="true">▶</span>
                      </div>
                      <div class="same-source-video-title"><span>A</span><strong>{{ relation.video_a?.name || `视频 ${relation.video_a_id}` }}</strong></div>
                      <p class="same-source-path" :title="relation.video_a?.path || ''">{{ relation.video_a?.path || '原始路径不可用' }}</p>
                    </article>
                    <span class="same-source-link" aria-hidden="true">↔</span>
                    <article class="same-source-video-card">
                      <div :class="['same-source-thumbnail', { 'same-source-thumbnail--failed': thumbnailFailed(relation.video_b_id) }]">
                        <img
                          v-if="!thumbnailFailed(relation.video_b_id)"
                          :src="thumbnailURL(relation.video_b_id)"
                          :alt="`${relation.video_b?.name || `视频 ${relation.video_b_id}`} 缩略图`"
                          loading="lazy"
                          @error="markThumbnailFailed(relation.video_b_id)"
                        />
                        <span v-else aria-hidden="true">▶</span>
                      </div>
                      <div class="same-source-video-title"><span>B</span><strong>{{ relation.video_b?.name || `视频 ${relation.video_b_id}` }}</strong></div>
                      <p class="same-source-path" :title="relation.video_b?.path || ''">{{ relation.video_b?.path || '原始路径不可用' }}</p>
                    </article>
                  </div>
                  <div class="same-source-evidence">
                    <span class="ai-confidence ai-confidence--high">高置信</span>
                    <p v-if="relation.reasoning" class="ai-candidate-reason">{{ relation.reasoning }}</p>
                  </div>
                </div>
                <div class="ai-candidate-actions">
                  <button type="button" class="btn-action btn-compact" title="同时打开两个视频" @click="previewSameSource(relation)" :disabled="processingIds.includes(`same-source-preview-${relation.id}`)">预览</button>
                  <button type="button" class="btn-danger btn-compact" @click="openSameSourceDelete(relation, 'a')" :disabled="processingIds.includes(`same-source-delete-${relation.video_a_id}`)">删除 A</button>
                  <button type="button" class="btn-danger btn-compact" @click="openSameSourceDelete(relation, 'b')" :disabled="processingIds.includes(`same-source-delete-${relation.video_b_id}`)">删除 B</button>
                  <button type="button" class="btn-primary btn-compact" @click="confirmSameSource(relation)" :disabled="processingIds.includes(`same-source-${relation.id}`)">确认同源</button>
                  <button type="button" class="btn-secondary btn-compact" @click="rejectSameSource(relation)" :disabled="processingIds.includes(`same-source-${relation.id}`)">不是同源</button>
                </div>
              </div>
            </section>
            <div v-else class="ai-tag-review-empty">暂无待处理的视频同源关系</div>
          </template>
          <template v-else>
            <div v-if="groups.length === 0" class="ai-tag-review-empty">{{ reviewSearch.trim() ? '没有匹配的待审 AI 标签' : '暂无待审 AI 标签' }}</div>
            <div v-else class="ai-tag-review-list">
              <section v-for="group in groups" :key="group.videoId" class="ai-video-group">
                <div class="ai-video-title">
                  <div class="ai-video-name">
                    <span>{{ group.videoName }}</span>
                    <span v-if="group.videoDeleted" class="ai-video-deleted-badge">已删除</span>
                  </div>
                  <div class="ai-video-actions">
                    <button type="button" class="btn-action btn-compact" @click="previewVideo(group.videoId)" :disabled="group.videoDeleted || processingIds.includes(`preview-${group.videoId}`)">预览视频</button>
                    <button type="button" class="btn-secondary btn-compact" @click="openRenameDialog(group)" :disabled="group.videoDeleted || processingIds.includes(`rename-${group.videoId}`)">重命名</button>
                    <button type="button" class="btn-secondary btn-compact" @click="openManualTagDialog(group)" :disabled="group.videoDeleted">手动添加标签</button>
                    <button type="button" class="btn-secondary btn-compact" @click="rejectVideoGroup(group)" :disabled="processingIds.includes(`reject-video-${group.videoId}`)">全部拒绝</button>
                    <button type="button" class="btn-action btn-compact" @click="retryVideo(group.videoId)" :disabled="group.videoDeleted || processingIds.includes(group.videoId)">重新分析</button>
                  </div>
                </div>
                <div v-if="group.videoPath" class="ai-video-path">{{ group.videoPath }}</div>
                <div class="ai-video-existing-tags">
                  <span class="ai-video-existing-tags-label">已有标签</span>
                  <div v-if="group.videoTags.length" class="ai-video-existing-tag-list">
                    <span
                      v-for="tag in group.videoTags"
                      :key="tag.id"
                      class="tag-badge ai-video-existing-tag"
                      :style="{ backgroundColor: tagBgColor(tag.color) }"
                    >
                      {{ tag.name }}
                    </span>
                  </div>
                  <span v-else class="ai-video-no-tags">暂无</span>
                </div>

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
                    <button type="button" class="btn-primary" @click="approve(candidate)" :disabled="candidate.video_deleted || processingIds.includes(candidate.id)">批准</button>
                    <button type="button" class="btn-secondary" @click="reject(candidate)" :disabled="processingIds.includes(candidate.id)">拒绝</button>
                  </div>
                </div>
              </section>
            </div>
          </template>
        </div>
      </template>

      <AIQualityPanel v-if="activeTab === 'quality'" :tags="tags" />

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

      <div v-if="sameSourceDeleteConfirm.show" class="ai-confirm-overlay">
        <div class="ai-confirm-dialog glass-surface">
          <h4>删除视频 {{ sameSourceDeleteConfirm.side.toUpperCase() }}</h4>
          <p>将从片库删除这条视频记录，磁盘上的原文件会保留；记录可在回收站中恢复。</p>
          <p class="ai-confirm-video">{{ sameSourceDeleteConfirm.videoName }}</p>
          <p class="ai-confirm-path">{{ sameSourceDeleteConfirm.videoPath || '原始路径不可用' }}</p>
          <div class="ai-confirm-actions">
            <button type="button" class="btn-secondary" @click="cancelSameSourceDelete">取消</button>
            <button type="button" class="btn-danger" @click="confirmSameSourceDelete" :disabled="processingIds.includes(`same-source-delete-${sameSourceDeleteConfirm.videoId}`)">确认删除记录</button>
          </div>
        </div>
      </div>

      <AddTagDialog
        :visible="manualTagDialog.show"
        :video="manualTagDialog.video"
        :tags="tags"
        @close="closeManualTagDialog"
        @tag-added="handleManualTagAdded"
      />
    </div>
  </div>
</template>

<script>
import { ApproveAITagCandidate, ConfirmSameSourceRelation, DeleteVideo, GetAITaggingStatusSummary, ListAITagCandidates, ListSameSourceRelations, MarkSameSourceRelationRead, PreviewExternally, RejectAITagCandidate, RejectAITagCandidatesByVideo, RejectSameSourceRelation, RenameVideo, RetryAITagging } from '../../wailsjs/go/main/App';
import AddTagDialog from './AddTagDialog.vue';
import AIQualityPanel from './AIQualityPanel.vue';
import { confidenceMeta, createRejectVideoConfirm, filterCandidatesForReview, groupCandidatesByVideo, removeCandidateById } from '../utils/aiTagReview.js';

export default {
  name: 'AITagReviewDialog',
  components: { AddTagDialog, AIQualityPanel },
  props: {
    visible: { type: Boolean, default: false },
    tags: { type: Array, default: () => [] },
    qualityEnabled: { type: Boolean, default: true },
  },
  emits: ['close', 'changed'],
  data() {
    return {
      candidates: [],
      activeTab: 'review',
      reviewSection: 'tags',
      sameSourceRelations: [],
      summary: null,
      loading: false,
      error: '',
      reviewSearch: '',
      processingIds: [],
      thumbnailFailures: {},
      manualTagDialog: { show: false, video: null },
      rejectConfirm: { show: false, videoId: 0, videoName: '', count: 0, candidateIds: [] },
      renameConfirm: { show: false, videoId: 0, videoName: '', videoPath: '', newName: '', ext: '(无)' },
      sameSourceDeleteConfirm: { show: false, relationId: 0, side: 'a', videoId: 0, videoName: '', videoPath: '' },
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
	qualityEnabled(value) {
	  if (!value && this.activeTab === 'quality') this.activeTab = 'review';
	},
    visible(value) {
      if (value) {
		this.activeTab = 'review';
        this.reviewSection = 'tags';
        this.reviewSearch = '';
        this.loadCandidates();
      }
    },
  },
  methods: {
    confidenceMeta,
    thumbnailURL(videoId) {
      return `/preview/thumbnail/${videoId}`;
    },
    thumbnailFailed(videoId) {
      return !!this.thumbnailFailures[videoId];
    },
    markThumbnailFailed(videoId) {
      this.thumbnailFailures = { ...this.thumbnailFailures, [videoId]: true };
    },
    async loadCandidates(options = {}) {
      const silent = !!options.silent;
      if (!silent) this.loading = true;
      this.error = '';
      try {
        const [summary, candidates, relations] = await Promise.all([
          GetAITaggingStatusSummary(),
          ListAITagCandidates(0, '', 'pending'),
          ListSameSourceRelations('detected', false),
        ]);
        this.summary = summary;
        this.candidates = Array.isArray(candidates) ? candidates : [];
        this.sameSourceRelations = Array.isArray(relations) ? relations : [];
        const unread = this.sameSourceRelations.filter(relation => relation.is_unread);
        if (unread.length) {
          await Promise.all(unread.map(relation => MarkSameSourceRelationRead(relation.id)));
          this.sameSourceRelations = this.sameSourceRelations.map(relation => ({ ...relation, is_unread: false }));
          if (this.summary) this.summary.same_source_unread = 0;
          this.$emit('changed');
        }
      } catch (err) {
        this.error = '加载 AI 标签候选失败: ' + err;
      } finally {
        if (!silent) this.loading = false;
      }
    },
    async approve(candidate) {
      await this.withProcessing(candidate.id, async () => {
        const item = await ApproveAITagCandidate(candidate.id);
        this.candidates = removeCandidateById(this.candidates, candidate.id);
        if (item?.status === 'superseded') {
          await this.loadCandidates({ silent: true });
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
    async rejectSameSource(relation) {
      await this.withProcessing(`same-source-${relation.id}`, async () => {
        await RejectSameSourceRelation(relation.id);
        this.sameSourceRelations = this.sameSourceRelations.filter(item => Number(item.id) !== Number(relation.id));
        this.$emit('changed');
      });
    },
    async confirmSameSource(relation) {
      await this.withProcessing(`same-source-${relation.id}`, async () => {
        await ConfirmSameSourceRelation(relation.id);
        this.sameSourceRelations = this.sameSourceRelations.filter(item => Number(item.id) !== Number(relation.id));
        this.$emit('changed');
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
        await this.loadCandidates({ silent: true });
      });
    },
    async previewVideo(videoId) {
      await this.withProcessing(`preview-${videoId}`, async () => {
        await PreviewExternally(videoId);
      });
    },
    async previewSameSource(relation) {
      if (!relation || relation.video_a_deleted || relation.video_b_deleted) return;
      await this.withProcessing(`same-source-preview-${relation.id}`, async () => {
        await Promise.all([
          PreviewExternally(relation.video_a_id),
          PreviewExternally(relation.video_b_id),
        ]);
      });
    },
    openSameSourceDelete(relation, side) {
      const normalizedSide = side === 'b' ? 'b' : 'a';
      const video = normalizedSide === 'a' ? relation?.video_a : relation?.video_b;
      const videoId = Number(video?.id || (normalizedSide === 'a' ? relation?.video_a_id : relation?.video_b_id) || 0);
      if (!relation?.id || !videoId) return;
      this.sameSourceDeleteConfirm = {
        show: true,
        relationId: Number(relation.id),
        side: normalizedSide,
        videoId,
        videoName: video?.name || `视频 ${videoId}`,
        videoPath: video?.path || '',
      };
    },
    cancelSameSourceDelete() {
      this.sameSourceDeleteConfirm = { show: false, relationId: 0, side: 'a', videoId: 0, videoName: '', videoPath: '' };
    },
    async confirmSameSourceDelete() {
      const videoId = Number(this.sameSourceDeleteConfirm.videoId || 0);
      if (!videoId) return;
      await this.withProcessing(`same-source-delete-${videoId}`, async () => {
        await DeleteVideo(videoId, false);
        this.sameSourceRelations = this.sameSourceRelations.filter(relation => Number(relation.video_a_id) !== videoId && Number(relation.video_b_id) !== videoId);
        this.cancelSameSourceDelete();
        await this.loadCandidates({ silent: true });
        this.$emit('changed');
      });
    },
    openManualTagDialog(group) {
      if (!group?.videoId || group.videoDeleted) return;
      this.manualTagDialog = {
        show: true,
        video: {
          ...(group.video || {}),
          id: group.videoId,
          name: group.videoName,
          path: group.videoPath,
          tags: group.videoTags,
        },
      };
    },
    closeManualTagDialog() {
      this.manualTagDialog = { show: false, video: null };
    },
    async handleManualTagAdded() {
      await this.loadCandidates({ silent: true });
      this.$emit('changed');
    },
    tagBgColor(hex) {
      if (!hex || !hex.startsWith('#')) return hex;
      const r = parseInt(hex.slice(1, 3), 16);
      const g = parseInt(hex.slice(3, 5), 16);
      const b = parseInt(hex.slice(5, 7), 16);
      return `rgba(${r},${g},${b},0.35)`;
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
          await this.loadCandidates({ silent: true });
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
  height: min(720px, calc(100vh - 48px));
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

.ai-review-type-tabs {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
  padding-top: 12px;
}

.ai-review-type-tabs button {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  min-height: 38px;
  border: 1px solid var(--border-color);
  border-radius: 9px;
  background: var(--control-bg);
  color: var(--text-secondary);
  cursor: pointer;
}

.ai-review-type-tabs button.active {
  border-color: color-mix(in srgb, var(--accent-color) 45%, var(--border-color));
  background: var(--accent-soft);
  color: var(--accent-color);
  font-weight: 700;
}

.ai-review-type-tabs span {
  min-width: 22px;
  padding: 1px 6px;
  border-radius: 999px;
  background: color-mix(in srgb, currentColor 12%, transparent);
  font-size: 11px;
}

.same-source-help {
  flex: 1;
  margin: 0;
  color: var(--text-muted);
  font-size: 12px;
}

.ai-review-workbench-content {
  flex: 1 1 auto;
  min-height: 0;
  overflow-x: hidden;
  overflow-y: auto;
  padding-right: 4px;
  overscroll-behavior: contain;
}

.ai-tag-review-tabs {
  display: flex;
  gap: 6px;
  padding-top: 12px;
  border-bottom: 1px solid var(--border-color);
}

.ai-tag-review-tabs button {
  padding: 8px 12px;
  border: 0;
  border-bottom: 2px solid transparent;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
}

.ai-tag-review-tabs button.active {
  border-bottom-color: var(--accent-color);
  color: var(--text-primary);
  font-weight: 700;
}

.ai-tag-review-search {
  flex: 1 1 auto;
  min-width: 180px;
}

.ai-tag-review-list {
  overflow-y: visible;
  overflow-x: hidden;
  padding-right: 4px;
  min-width: 0;
}

.same-source-review-section {
  margin-bottom: 16px;
  padding: 14px;
  border: 1px solid color-mix(in srgb, var(--accent-color) 28%, transparent);
  border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--accent-color) 7%, var(--panel-bg));
}

.same-source-review-section h4 {
  margin: 0 0 10px;
}

.same-source-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
  padding: 10px 0;
  border-top: 1px solid var(--border-color);
}

.same-source-row .ai-candidate-actions {
  max-width: 320px;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.same-source-row:first-of-type {
  border-top: 0;
}

.same-source-main {
  flex: 1 1 auto;
  min-width: 0;
}

.same-source-link {
  align-self: center;
  color: var(--text-secondary);
}

.same-source-pair {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
  align-items: stretch;
  gap: 10px;
}

.same-source-video-card {
  min-width: 0;
  padding: 10px;
  border: 1px solid var(--border-color);
  border-radius: 9px;
  background: var(--control-bg);
}

.same-source-thumbnail {
  display: grid;
  width: 100%;
  aspect-ratio: 16 / 9;
  margin-bottom: 10px;
  place-items: center;
  overflow: hidden;
  border-radius: 8px;
  background: #0f172a;
  color: rgba(255, 255, 255, 0.68);
  font-size: 24px;
}

.same-source-thumbnail img {
  width: 100%;
  height: 100%;
  display: block;
  object-fit: cover;
}

.same-source-thumbnail--failed {
  background: linear-gradient(135deg, #1e293b, #334155);
}

.same-source-video-title {
  display: flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
}

.same-source-video-title > span {
  flex: 0 0 auto;
  display: grid;
  place-items: center;
  width: 22px;
  height: 22px;
  border-radius: 6px;
  background: var(--accent-soft);
  color: var(--accent-color);
  font-size: 11px;
  font-weight: 800;
}

.same-source-video-title strong {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.same-source-path,
.ai-confirm-path {
  margin: 7px 0 0;
  color: var(--text-muted);
  font-size: 11px;
  overflow-wrap: anywhere;
}

.same-source-evidence {
  display: flex;
  align-items: flex-start;
  gap: 2px;
  margin-top: 9px;
}

.same-source-evidence .ai-candidate-reason {
  flex: 1;
  margin-top: 2px;
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

.ai-video-name {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.ai-video-name > span:first-child {
  min-width: 0;
  overflow-wrap: anywhere;
}

.ai-video-deleted-badge {
  flex: 0 0 auto;
  padding: 2px 6px;
  border: 1px solid rgba(229, 72, 77, 0.4);
  border-radius: 6px;
  background: rgba(229, 72, 77, 0.12);
  color: var(--danger-color);
  font-size: 11px;
  font-weight: 700;
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

.ai-video-existing-tags {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
  min-width: 0;
}

.ai-video-existing-tags-label {
  flex: 0 0 auto;
  color: var(--text-muted);
  font-size: 12px;
}

.ai-video-existing-tag-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  min-width: 0;
}

.ai-video-existing-tag {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
}

.ai-video-no-tags {
  color: var(--text-muted);
  font-size: 12px;
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

  .same-source-row {
    flex-direction: column;
  }

  .same-source-pair {
    grid-template-columns: minmax(0, 1fr);
  }

  .same-source-link {
    display: none;
  }

  .same-source-row .ai-candidate-actions {
    max-width: 100%;
  }
}
</style>
