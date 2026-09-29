<template>
  <BaseModal v-if="enhanceDialog.show" stop-modal-clicks @close="enhanceDialog.show = false">
    <h2>视频超分（2×）</h2>
    <!-- 能力不可用 / 没带上视频这两条死胡同分支里原本没有任何按钮，
         这个弹窗又不响应点遮罩，用户会被困在里面出不来。 -->
    <template v-if="!enhanceCapability?.available">
      <!-- 未就绪要说清楚原因和下一步（MEDIA-11）：hint 是给用户看的一句话，message 留给排障。 -->
      <p class="help-text" data-test="enhance-unavailable">超分暂时不可用：{{ enhanceUnavailableText }}</p>
    </template>
    <template v-else-if="enhanceDialog.video">
      <p class="enhance-source-name" :title="enhanceDialog.video.path">{{ enhanceDialog.video.name }}</p>
      <!-- 2× 超分产物体积是源的数倍量级，源文件多大直接决定这次要吃多少磁盘。 -->
      <p v-if="formatMediaMeta(enhanceDialog.video).length" class="enhance-source-meta" data-test="enhance-source-meta">{{ formatMediaMeta(enhanceDialog.video).join(' · ') }}</p>
      <div class="setting-item">
        <label>内容类型（决定模型，不会自动判断）</label>
        <div class="merge-type-switch" role="group" aria-label="超分内容类型">
          <button type="button" :class="{ active: enhanceDialog.profile === 'general' }" @click="enhanceDialog.profile = 'general'">普通真人</button>
          <button type="button" :class="{ active: enhanceDialog.profile === 'anime' }" @click="enhanceDialog.profile = 'anime'">动漫</button>
        </div>
      </div>
      <p class="help-text">输出：<code>{{ enhanceOutputPreview }}</code>（与源同目录）</p>
      <p class="help-text">固定 2× 放大；原文件不会被修改；任务可随时取消，取消或空间不足时进度会保留，重试从断点接着做。运行需要同卷至少约 {{ enhanceDiskFloorText }} 可用空间。</p>
      <div class="setting-item enhance-copy-metadata">
        <label class="checkbox-label">
          <input v-model="enhanceDialog.copyMetadata" type="checkbox" data-test="enhance-copy-metadata" />
          <span>把原片的标签、人物、作品集与外挂字幕复制到产物</span>
        </label>
        <p class="help-text">只复制你自己整理的信息（自动标签不复制）；产物加入原片所在的作品集，排在末尾。</p>
      </div>
      <p v-if="retainedTaskForVideo" class="help-text enhance-retained-hint" data-test="enhance-retained-hint">
        这个视频有一个保留了进度的任务。新建任务会清掉那份进度；想接着做，请在下方任务列表里点它的「重试」。
      </p>
      <p v-if="enhanceDialog.error" class="cleanup-error">{{ enhanceDialog.error }}</p>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" @click="enhanceDialog.show = false">取消</button>
        <button type="button" class="btn-primary" :disabled="enhanceDialog.creating" @click="createEnhancementTask">
          {{ enhanceDialog.creating ? '创建中...' : '创建超分任务' }}
        </button>
      </div>
    </template>
    <div v-if="!enhanceCapability?.available || !enhanceDialog.video" class="modal-actions">
      <button type="button" class="btn-secondary" data-test="enhance-close" @click="enhanceDialog.show = false">关闭</button>
    </div>

    <template v-if="enhanceTasks.length">
      <div class="divider"></div>
      <h3 class="enhance-task-heading">任务</h3>
      <div v-for="task in enhanceTasks" :key="task.id" class="enhance-task-row" :data-test="`enhance-task-${task.id}`">
        <span class="enhance-task-main">
          <span class="enhance-task-title">{{ task.video_name }} · <span data-test="enhance-task-status">{{ enhanceStatusLabel(task) }}</span></span>
          <small v-if="enhanceDetailText(task)" class="enhance-task-detail" data-test="enhance-task-detail">{{ enhanceDetailText(task) }}</small>
        </span>
        <span class="enhance-task-actions">
          <button v-if="enhancementCancellable(task)" type="button" class="btn-secondary btn-compact" @click="cancelEnhancementTask(task)">取消</button>
          <button v-if="enhancementRetryable(task)" type="button" class="btn-secondary btn-compact" :data-test="`enhance-retry-${task.id}`" @click="retryEnhancementTask(task)">重试</button>
          <button
            v-if="enhancementRetainsProgress(task)"
            type="button"
            class="btn-secondary btn-compact btn-danger-outline"
            :data-test="`enhance-discard-${task.id}`"
            @click="discardEnhancementProgress(task)"
          >放弃保留的进度</button>
          <button
            v-if="task.status === 'completed' && task.output_video_id"
            type="button"
            class="btn-secondary btn-compact"
            :data-test="`enhance-reveal-${task.id}`"
            @click="revealEnhancementOutput(task)"
          >查看产物</button>
        </span>
      </div>
    </template>
  </BaseModal>
</template>

<script>
import { GetEnhancementCapability, GetEnhancementVideoPreflight, CreateEnhancementTask, ListEnhancementTasks, CancelEnhancementTask, RetryEnhancementTask, DiscardEnhancementProgress, OpenDirectory } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import { confirmAction, notifyError, notifySuccess } from '../../utils/feedback.js';
import { formatMediaMeta } from '../../utils/mediaDetails.js';
import {
  enhancementCancellable, enhancementDetailText, enhancementRetainsProgress, enhancementRetryable, enhancementStatusText
} from '../../utils/enhancement.js';
import { runtimeEventsMixin } from './runtimeEvents.js';

// 视频超分弹窗：能力探测、单个任务创建与最近任务列表。由父组件通过 ref 调用 open()
// 打开（行菜单的「视频超分…」与详情抽屉的 @enhance 两个入口）。
export default {
  name: 'EnhanceDialog',
  components: { BaseModal },
  mixins: [runtimeEventsMixin],
  data() {
    return {
      enhanceCapability: null,
      enhanceDialog: { show: false, video: null, profile: 'general', copyMetadata: true, creating: false, error: '', preflight: null },
      enhanceTasks: []
    };
  },
  mounted() {
    this.registerRuntimeEvent('video-enhancement-state', view => this.applyEnhancementState(view));
  },
  computed: {
    enhanceUnavailableText() {
      const capability = this.enhanceCapability;
      return capability?.hint || capability?.message || '超分组件没有随应用安装';
    },
    // 同一视频新建任务时后端会先清掉旧任务保留的检查点（主代理裁决 2026-09-29），
    // 所以在创建前把这件事说出来，想续跑的人去点那条任务的「重试」。
    retainedTaskForVideo() {
      const videoID = this.enhanceDialog.video?.id;
      if (!videoID) return null;
      return this.enhanceTasks.find(task => task.video_id === videoID && enhancementRetainsProgress(task)) || null;
    },
    enhanceOutputPreview() {
      const preflight = this.enhanceDialog.preflight;
      if (preflight) {
        return this.enhanceDialog.profile === 'anime' ? preflight.output_basename_anime : preflight.output_basename_general;
      }
      const name = this.enhanceDialog.video?.name || '';
      return `${name.replace(/\.[^.]+$/, '')}.enhanced-${this.enhanceDialog.profile}-2x.mkv`;
    },
    enhanceDiskFloorText() {
      const required = Number(this.enhanceDialog.preflight?.required_bytes || 0);
      if (!required) return '数 GiB';
      return `${(required / (1 << 30)).toFixed(1)} GiB`;
    }
  },
  methods: {
    formatMediaMeta,
    enhancementCancellable,
    enhancementRetryable,
    enhancementRetainsProgress,
    enhanceDetailText: enhancementDetailText,
    open(video) {
      return this.openEnhanceDialog(video);
    },
    async openEnhanceDialog(video) {
      this.enhanceDialog = { show: true, video, profile: 'general', copyMetadata: true, creating: false, error: '' };
      try {
        this.enhanceCapability = await GetEnhancementCapability();
      } catch (err) {
        this.enhanceCapability = { available: false, message: String(err) };
      }
      try {
        this.enhanceDialog.preflight = await GetEnhancementVideoPreflight(video.id);
      } catch (err) {
        this.enhanceDialog.preflight = null;
      }
      await this.refreshEnhancementTasks();
    },
    async refreshEnhancementTasks() {
      try {
        this.enhanceTasks = await ListEnhancementTasks(10) || [];
      } catch (err) {
        this.enhanceTasks = [];
      }
    },
    applyEnhancementState(view) {
      if (!view?.id) return;
      const index = this.enhanceTasks.findIndex(task => task.id === view.id);
      if (index >= 0) this.enhanceTasks.splice(index, 1, view);
      else this.enhanceTasks.unshift(view);
    },
    enhanceStatusLabel(task) {
      return enhancementStatusText(task);
    },
    async createEnhancementTask() {
      if (!this.enhanceDialog.video) return;
      this.enhanceDialog.creating = true;
      this.enhanceDialog.error = '';
      try {
        await CreateEnhancementTask({
          video_id: this.enhanceDialog.video.id,
          profile: this.enhanceDialog.profile,
          copy_metadata: this.enhanceDialog.copyMetadata !== false
        });
        await this.refreshEnhancementTasks();
      } catch (err) {
        this.enhanceDialog.error = String(err);
      } finally {
        this.enhanceDialog.creating = false;
      }
    },
    async cancelEnhancementTask(task) {
      try {
        await CancelEnhancementTask(task.id);
      } catch (err) {
        notifyError('取消超分任务失败: ' + err);
      }
      await this.refreshEnhancementTasks();
    },
    async retryEnhancementTask(task) {
      try {
        await RetryEnhancementTask(task.id);
      } catch (err) {
        notifyError('重试超分任务失败: ' + err);
      }
      await this.refreshEnhancementTasks();
    },
    // 放弃之后工作目录会被删掉、重试从头开始（MEDIA-03），这一步不可撤销，先确认。
    async discardEnhancementProgress(task) {
      const confirmed = await confirmAction({
        title: '放弃保留的进度',
        message: `「${task.video_name}」已处理的部分会被删除，腾出磁盘空间；之后重试将从头开始。确认放弃吗？`,
        confirmText: '放弃进度',
        danger: true
      });
      if (!confirmed) return;
      try {
        const view = await DiscardEnhancementProgress(task.id);
        if (view?.id) this.applyEnhancementState(view);
        notifySuccess('已放弃保留的进度');
      } catch (err) {
        notifyError('放弃保留的进度失败: ' + err);
      }
      await this.refreshEnhancementTasks();
    },
    async revealEnhancementOutput(task) {
      try {
        await OpenDirectory(task.output_video_id);
      } catch (err) {
        notifyError('打开产物所在位置失败: ' + err);
      }
    },
  }
};
</script>

<style scoped>
.enhance-source-name { font-weight: 650; margin-bottom: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.enhance-source-meta { margin-bottom: 10px; color: var(--text-secondary); font-size: 12px; font-variant-numeric: tabular-nums; }
.enhance-task-heading { font-size: 14px; margin-bottom: 8px; }
.enhance-task-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 6px 0; border-bottom: 1px solid var(--border-color); font-size: 12px; }
.enhance-task-main { min-width: 0; display: grid; gap: 2px; }
.enhance-task-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.enhance-task-detail { color: var(--text-secondary); font-size: 11.5px; overflow-wrap: anywhere; }
.enhance-task-actions { flex: 0 0 auto; display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; }
.enhance-copy-metadata { margin-bottom: 12px; }
.enhance-retained-hint { color: var(--warning-text); }
.cleanup-error {
  color: var(--review-text-muted);
  font-size: 13px;
}
.btn-compact {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
}
</style>
