<template>
  <BaseModal class="photo-cleanup-modal" close-on-overlay stop-modal-clicks data-test="photo-cleanup-panel" @close="$emit('close')">
    <div class="photo-cleanup__header">
      <div>
        <h2>清理审阅</h2>
        <p class="photo-cleanup__intro">
          精确重复（大小 + 采样哈希）与近似重复（感知哈希）两类候选。所有候选默认不勾选，勾选后移入回收站，可随时恢复。
        </p>
      </div>
      <button type="button" class="btn-secondary" @click="$emit('close')">关闭</button>
    </div>

    <p
      v-if="analysis && analysis.stale_hash_count > 0"
      class="photo-cleanup__stale"
      data-test="cleanup-stale-hint"
    >
      有 {{ analysis.stale_hash_count }} 张图片的指纹已过期，暂未参与近似重复检测。部分图片指纹已过期，浏览图片可自动刷新缩略图与指纹。
    </p>

    <div class="photo-cleanup__body">
      <div v-if="running" class="photo-cleanup__progress" data-test="cleanup-progress">
        <p>分析进行中 · {{ stageLabel }}</p>
        <p v-if="progress.total > 0">已处理 {{ progress.current }} / {{ progress.total }}</p>
        <p v-if="progress.message" class="photo-cleanup__muted">{{ progress.message }}</p>
        <p v-if="progress.path" class="photo-cleanup__path">当前文件：{{ progress.path }}</p>
      </div>

      <p v-else-if="displayError" class="photo-cleanup__error" role="alert">{{ displayError }}</p>

      <template v-else-if="analysis">
        <section v-if="analysis.duplicate_groups?.length" class="photo-cleanup__section" data-test="cleanup-exact-section">
          <h3>精确重复（{{ analysis.duplicate_groups.length }} 组）</h3>
          <article v-for="group in analysis.duplicate_groups" :key="`exact-${group.original?.id}`" class="photo-cleanup-card glass-surface">
            <p class="photo-cleanup-card__reason">{{ group.reason }}</p>
            <div class="photo-cleanup-row photo-cleanup-row--original">
              <img class="photo-cleanup-thumb" :src="`/preview/image-thumbnail/${group.original.id}`" :alt="group.original.name" loading="lazy" />
              <div class="photo-cleanup-meta">
                <span :title="group.original.path">{{ group.original.name }}</span>
                <small>{{ describeImage(group.original) }}</small>
              </div>
              <span class="photo-cleanup-keep">建议保留</span>
            </div>
            <label v-for="candidate in group.candidates" :key="candidate.id" class="photo-cleanup-row">
              <input
                type="checkbox"
                :checked="selection.includes(Number(candidate.id))"
                data-test="cleanup-candidate-toggle"
                @change="toggleSelection(candidate.id)"
              />
              <img class="photo-cleanup-thumb" :src="`/preview/image-thumbnail/${candidate.id}`" :alt="candidate.name" loading="lazy" />
              <div class="photo-cleanup-meta">
                <span :title="candidate.path">{{ candidate.name }}</span>
                <small>{{ describeImage(candidate) }}</small>
              </div>
            </label>
          </article>
        </section>

        <section v-if="analysis.near_duplicate_groups?.length" class="photo-cleanup__section" data-test="cleanup-near-section">
          <h3>近似重复（{{ analysis.near_duplicate_groups.length }} 组）</h3>
          <article v-for="group in analysis.near_duplicate_groups" :key="`near-${group.original?.id}`" class="photo-cleanup-card glass-surface">
            <p class="photo-cleanup-card__reason">{{ group.reason }}</p>
            <div class="photo-cleanup-row photo-cleanup-row--original">
              <img class="photo-cleanup-thumb" :src="`/preview/image-thumbnail/${group.original.id}`" :alt="group.original.name" loading="lazy" />
              <div class="photo-cleanup-meta">
                <span :title="group.original.path">{{ group.original.name }}</span>
                <small>{{ describeImage(group.original) }}</small>
              </div>
              <span class="photo-cleanup-keep">建议保留</span>
            </div>
            <label v-for="candidate in group.candidates" :key="candidate.id" class="photo-cleanup-row">
              <input
                type="checkbox"
                :checked="selection.includes(Number(candidate.id))"
                data-test="cleanup-candidate-toggle"
                @change="toggleSelection(candidate.id)"
              />
              <img class="photo-cleanup-thumb" :src="`/preview/image-thumbnail/${candidate.id}`" :alt="candidate.name" loading="lazy" />
              <div class="photo-cleanup-meta">
                <span :title="candidate.path">{{ candidate.name }}</span>
                <small>{{ describeImage(candidate) }}</small>
              </div>
            </label>
            <div class="photo-cleanup-card__actions">
              <button
                type="button"
                class="btn-secondary btn-compact"
                :disabled="dismissing"
                data-test="cleanup-dismiss-group"
                @click="dismissGroup(group)"
              >忽略此组</button>
            </div>
          </article>
        </section>

        <p v-if="!hasGroups" class="photo-cleanup__empty" data-test="cleanup-empty">
          没有发现重复或近似重复的图片。
        </p>
      </template>

      <p v-else class="photo-cleanup__muted" data-test="cleanup-idle">
        尚未分析。点击"开始分析"扫描库内的精确重复与近似重复图片。
      </p>
    </div>

    <div class="photo-cleanup__footer">
      <button
        type="button"
        class="btn-secondary"
        :disabled="running || processing"
        data-test="cleanup-start"
        @click="startAnalysis"
      >
        {{ running ? '分析中...' : (analysis ? '重新分析' : '开始分析') }}
      </button>
      <button
        type="button"
        class="btn-danger"
        :disabled="selection.length === 0 || running || processing"
        data-test="cleanup-delete-selected"
        @click="deleteSelected"
      >
        {{ processing ? '处理中...' : `删除所选 (${selection.length})` }}
      </button>
    </div>
  </BaseModal>
</template>

<script>
import {
  StartImageCleanupAnalysis,
  GetImageCleanupStatus,
  DismissImageNearDuplicateGroup,
  BatchDeleteImages
} from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import { formatBytes } from '../utils/mediaDetails.js';

const STAGE_LABELS = {
  load: '读取图片记录',
  group: '按文件大小聚合',
  hash: '读取采样哈希',
  near: '比对感知哈希',
  done: '完成'
};

const POLL_INTERVAL_MS = 1000;

export default {
  name: 'PhotoCleanupPanel',
  components: { BaseModal },
  emits: ['close', 'deleted'],
  data() {
    return {
      status: null,
      localError: '',
      selection: [],
      processing: false,
      dismissing: false
    };
  },
  computed: {
    running() { return !!this.status?.running; },
    analysis() { return (this.status?.completed && this.status.analysis) || null; },
    progress() { return this.status?.progress || {}; },
    stageLabel() { return STAGE_LABELS[this.progress.stage] || '准备中'; },
    displayError() { return this.localError || this.status?.error || ''; },
    hasGroups() {
      return Boolean(this.analysis?.duplicate_groups?.length || this.analysis?.near_duplicate_groups?.length);
    }
  },
  mounted() {
    this._alive = true;
    this.pollStatus();
  },
  beforeUnmount() {
    this._alive = false;
    clearTimeout(this._pollTimer);
  },
  methods: {
    formatBytes,
    describeImage(image) {
      const dims = image?.width && image?.height ? `${image.width}×${image.height}` : '尺寸未探测';
      return `${dims} · ${formatBytes(image?.size || 0)}`;
    },
    async pollStatus() {
      if (!this._alive) return;
      try {
        const status = await GetImageCleanupStatus();
        if (!this._alive) return;
        this.status = status;
      } catch (err) {
        if (!this._alive) return;
        this.localError = `读取分析状态失败：${err}`;
        return;
      }
      if (this.status?.running) {
        clearTimeout(this._pollTimer);
        this._pollTimer = setTimeout(() => this.pollStatus(), POLL_INTERVAL_MS);
      }
    },
    async startAnalysis() {
      if (this.running || this.processing) return;
      this.localError = '';
      this.selection = [];
      try {
        this.status = await StartImageCleanupAnalysis();
      } catch (err) {
        this.localError = `启动分析失败：${err}`;
        return;
      }
      await this.pollStatus();
    },
    toggleSelection(imageID) {
      const id = Number(imageID);
      this.selection = this.selection.includes(id)
        ? this.selection.filter(item => item !== id)
        : [...this.selection, id];
    },
    async deleteSelected() {
      if (this.selection.length === 0 || this.processing || this.running) return;
      this.processing = true;
      this.localError = '';
      let deleted = false;
      let failureNotice = '';
      try {
        const result = await BatchDeleteImages([...this.selection], true);
        if (result?.failed > 0) {
          failureNotice = `有 ${result.failed} 张图片删除失败，其余已移入回收站。`;
        }
        this.selection = [];
        deleted = true;
      } catch (err) {
        this.localError = `删除所选图片失败：${err}`;
      } finally {
        this.processing = false;
      }
      if (!deleted) return;
      this.$emit('deleted');
      // 删除后后端分析结果已失效，立即重跑一次分析刷新候选。
      await this.startAnalysis();
      if (failureNotice) this.localError = failureNotice;
    },
    async dismissGroup(group) {
      if (this.dismissing) return;
      const ids = [group.original, ...(group.candidates || [])]
        .filter(Boolean)
        .map(image => Number(image.id));
      this.dismissing = true;
      this.localError = '';
      try {
        await DismissImageNearDuplicateGroup(ids);
        if (this.status?.analysis?.near_duplicate_groups) {
          this.status.analysis.near_duplicate_groups = this.status.analysis.near_duplicate_groups.filter(item => item !== group);
        }
        this.selection = this.selection.filter(id => !ids.includes(id));
      } catch (err) {
        this.localError = `忽略近似重复组失败：${err}`;
      } finally {
        this.dismissing = false;
      }
    }
  }
};
</script>

<style scoped>
:deep(.photo-cleanup-modal) { width: min(860px, 92vw); max-height: 84vh; display: flex; flex-direction: column; }
.photo-cleanup__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.photo-cleanup__header h2 { margin: 0 0 4px; }
.photo-cleanup__intro { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-cleanup__stale { margin: 12px 0 0; padding: 8px 12px; border: 1px solid var(--border-color); border-radius: 10px; background: var(--control-bg); color: var(--text-secondary); font-size: 12px; }
.photo-cleanup__body { flex: 1; min-height: 120px; margin-top: 12px; overflow-y: auto; display: flex; flex-direction: column; gap: 14px; }
.photo-cleanup__progress { display: grid; gap: 6px; padding: 16px 4px; color: var(--text-primary); font-size: 13px; }
.photo-cleanup__progress p { margin: 0; }
.photo-cleanup__path { color: var(--text-muted); font-size: 11px; word-break: break-all; }
.photo-cleanup__error { margin: 0; color: var(--danger-color); }
.photo-cleanup__empty,
.photo-cleanup__muted { margin: 0; padding: 20px 4px; color: var(--text-muted); font-size: 13px; }
.photo-cleanup__section { display: grid; gap: 10px; }
.photo-cleanup__section h3 { margin: 0; font-size: 14px; color: var(--text-secondary); }
.photo-cleanup-card { display: grid; gap: 8px; padding: 12px; border-radius: 12px; }
.photo-cleanup-card__reason { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-cleanup-row { display: flex; align-items: center; gap: 10px; padding: 6px 8px; border-radius: 10px; }
.photo-cleanup-row--original { background: var(--control-bg); }
label.photo-cleanup-row { cursor: pointer; }
label.photo-cleanup-row:hover { background: var(--control-bg); }
.photo-cleanup-thumb { width: 56px; height: 56px; flex: none; border-radius: 8px; object-fit: cover; background: var(--thumb-bg); }
.photo-cleanup-meta { display: grid; gap: 2px; min-width: 0; flex: 1; }
.photo-cleanup-meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; color: var(--text-primary); }
.photo-cleanup-meta small { color: var(--text-muted); font-size: 11px; }
.photo-cleanup-keep { flex: none; padding: 2px 10px; border: 1px solid var(--border-color); border-radius: 999px; color: var(--text-secondary); font-size: 11px; white-space: nowrap; }
.photo-cleanup-card__actions { display: flex; justify-content: flex-end; }
.photo-cleanup__footer { display: flex; justify-content: flex-end; gap: 10px; margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--border-color); }
</style>
