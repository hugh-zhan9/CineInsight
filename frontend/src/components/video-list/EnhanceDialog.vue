<template>
  <BaseModal v-if="enhanceDialog.show" stop-modal-clicks @close="enhanceDialog.show = false">
    <h2>视频超分（2×）</h2>
    <!-- 能力不可用 / 没带上视频这两条死胡同分支里原本没有任何按钮，
         这个弹窗又不响应点遮罩，用户会被困在里面出不来。 -->
    <template v-if="!enhanceCapability?.available">
      <p class="help-text">超分能力不可用：{{ enhanceCapability?.message || '运行时未打包' }}</p>
    </template>
    <template v-else-if="enhanceDialog.video">
      <p class="enhance-source-name" :title="enhanceDialog.video.path">{{ enhanceDialog.video.name }}</p>
      <div class="setting-item">
        <label>内容类型（决定模型，不会自动判断）</label>
        <div class="merge-type-switch" role="group" aria-label="超分内容类型">
          <button type="button" :class="{ active: enhanceDialog.profile === 'general' }" @click="enhanceDialog.profile = 'general'">普通真人</button>
          <button type="button" :class="{ active: enhanceDialog.profile === 'anime' }" @click="enhanceDialog.profile = 'anime'">动漫</button>
        </div>
      </div>
      <p class="help-text">输出：<code>{{ enhanceOutputPreview }}</code>（与源同目录）</p>
      <p class="help-text">固定 2× 放大；原文件不会被修改；任务可随时取消。运行需要同卷至少约 {{ enhanceDiskFloorText }} 可用空间。</p>
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
      <div v-for="task in enhanceTasks" :key="task.id" class="enhance-task-row">
        <span class="enhance-task-main">
          {{ task.video_name }} · {{ enhanceStatusLabel(task) }}
          <template v-if="task.status === 'running' && task.total_frames">（{{ task.committed_frames }}/{{ task.total_frames }} 帧）</template>
        </span>
        <span class="enhance-task-actions">
          <button v-if="['queued','running'].includes(task.status)" type="button" class="btn-secondary btn-compact" @click="cancelEnhancementTask(task)">取消</button>
          <button v-if="['failed','cancelled'].includes(task.status)" type="button" class="btn-secondary btn-compact" @click="retryEnhancementTask(task)">重试</button>
        </span>
      </div>
    </template>
  </BaseModal>
</template>

<script>
import { GetEnhancementCapability, GetEnhancementVideoPreflight, CreateEnhancementTask, ListEnhancementTasks, CancelEnhancementTask, RetryEnhancementTask } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import { notifyError } from '../../utils/feedback.js';
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
      enhanceDialog: { show: false, video: null, profile: 'general', creating: false, error: '', preflight: null },
      enhanceTasks: []
    };
  },
  mounted() {
    this.registerRuntimeEvent('video-enhancement-state', view => this.applyEnhancementState(view));
  },
  computed: {
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
    open(video) {
      return this.openEnhanceDialog(video);
    },
    async openEnhanceDialog(video) {
      this.enhanceDialog = { show: true, video, profile: 'general', creating: false, error: '' };
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
      const labels = { queued: '排队中', running: `处理中（${task.phase}）`, cancel_requested: '取消中', cancelled: '已取消', completed: '已完成', failed: `失败（${task.error_code}）` };
      return labels[task.status] || task.status;
    },
    async createEnhancementTask() {
      if (!this.enhanceDialog.video) return;
      this.enhanceDialog.creating = true;
      this.enhanceDialog.error = '';
      try {
        await CreateEnhancementTask({ video_id: this.enhanceDialog.video.id, profile: this.enhanceDialog.profile });
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
  }
};
</script>

<style scoped>
.enhance-source-name { font-weight: 650; margin-bottom: 10px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.enhance-task-heading { font-size: 14px; margin-bottom: 8px; }
.enhance-task-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 6px 0; border-bottom: 1px solid var(--border-color); font-size: 12px; }
.enhance-task-main { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.enhance-task-actions { flex: 0 0 auto; display: flex; gap: 6px; }
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
