<template>
  <section class="page-content cleanup-center" data-test="cleanup-center-page">
    <PageHeader title="清理中心" :subtitle="analyzedText">
      <template #actions>
        <!-- 「重新分析」只管当前标签（D-P3-02）。删除确认框开着或删除进行中不能重来。 -->
        <template v-if="activeTab === 'video'">
          <button type="button" class="btn btn--ghost" data-test="cleanup-to-library" :disabled="busy" @click="$emit('navigate', 'library-manage')">去片库管理</button>
          <button
            type="button"
            :class="['btn', videoState.hasAnalysis ? 'btn--secondary' : 'btn--primary']"
            data-test="cleanup-reanalyze"
            :disabled="!videoState.statusLoaded || videoState.analyzing || videoState.locked"
            @click="reanalyzeVideo"
          >{{ videoState.analyzing ? '分析中…' : (videoState.hasAnalysis ? '重新分析' : '开始分析') }}</button>
        </template>
        <template v-else>
          <!-- 原图片清理页的「返回图片库」（钩子 cleanup-back）：删除进行中不能离开。 -->
          <button
            type="button"
            class="btn btn--ghost"
            data-test="cleanup-back"
            :disabled="busy"
            :title="busy ? '正在移到废纸篓，完成后再离开' : ''"
            @click="$emit('navigate', 'photos-manage')"
          >去图片管理</button>
          <button
            type="button"
            :class="['btn', imageAnalysis ? 'btn--secondary' : 'btn--primary']"
            data-test="cleanup-start"
            :disabled="!imageStatus || imageRunning || imageState.locked"
            @click="startImageAnalysis"
          >{{ imageRunning ? '分析中…' : (imageAnalysis ? '重新分析' : '开始分析') }}</button>
        </template>
      </template>
      <template #below>
        <AppTabs :tabs="tabs" :model-value="activeTab" id-prefix="cleanup" aria-label="清理对象" @update:model-value="selectTab" />
      </template>
    </PageHeader>

    <!-- 视频标签的删除宿主（D-P3-02）：进度与取消、磁盘不支持废纸篓时的二选一、撤销提示与「最近操作」。
         放在页上而不是标签里：本页首次进入后常挂载，离开这一页以后从「最近操作」撤销也能通知到片库。 -->
    <TrashUndoBanner ref="trashUndo" kind="video" :after-restore="afterVideoTrashRestore" />

    <!-- 视频：首次切到才挂载，之后只隐藏，审阅进度（勾选、保留项、已移到废纸篓的标记）留在面板里。 -->
    <div
      v-if="videoMounted"
      v-show="activeTab === 'video'"
      id="cleanup-panel-video"
      class="cleanup-center__panel"
      role="tabpanel"
      aria-labelledby="cleanup-tab-video"
    >
      <CleanupReviewPanel
        ref="videoPanel"
        :perceptual-hash-running="perceptualHash.running"
        :frame-hash-running="frameHash.running"
        :trash-videos="trashCleanupVideos"
        :after-trash-videos="afterTrashCleanupVideos"
        @start-perceptual-hash="startPerceptualHashBackfill"
        @start-frame-hash="startFrameHashBackfill"
        @state-change="videoState = $event"
      />
    </div>
    <!-- 图片：沿用原来的挂载方式——在这个标签上才挂载，每次进来读一次状态（没有结果时自动分析一次）；
         审阅进度在 photoCleanupStore 里，卸下也不丢。 -->
    <div
      v-if="imagePanelVisible"
      id="cleanup-panel-image"
      class="cleanup-center__panel"
      role="tabpanel"
      aria-labelledby="cleanup-tab-image"
    >
      <PhotoCleanupPage ref="imagePanel" @deleted="handleImagesDeleted" @state-change="imageState = $event" />
    </div>
  </section>
</template>

<script>
import {
  GetFrameHashBackfillStatus, GetPerceptualHashBackfillStatus, StartFrameHashBackfill, StartPerceptualHashBackfill
} from '../../../wailsjs/go/main/App';
import PhotoCleanupPage from '../PhotoCleanupPage.vue';
import AppTabs from '../ui/AppTabs.vue';
import PageHeader from '../ui/PageHeader.vue';
import CleanupReviewPanel from '../video-list/CleanupReviewPanel.vue';
import TrashUndoBanner from '../video-list/TrashUndoBanner.vue';
import { runtimeEventsMixin } from '../video-list/runtimeEvents.js';
import { CLEANUP_BUSY_NOTICE, CLEANUP_TABS, DEFAULT_CLEANUP_TAB, isCleanupTab } from '../../utils/appCommands.js';
import { registerCommands, unregisterCommands } from '../../utils/commandRegistry.js';
import { notify, notifyError } from '../../utils/feedback.js';
import { photoCleanupStore } from '../../utils/photoCleanupStore.js';
import { formatLocalTime } from '../../utils/subtitleTools.js';

const COMMAND_SCOPE = 'cleanup-center';

function idleReviewState() {
  return { analyzing: false, processing: false, locked: false, cancelled: false, failed: false, statusLoaded: false, hasAnalysis: false, startedAt: '' };
}

function idleImageState() {
  return { running: false, processing: false, locked: false, cancelled: false, hasAnalysis: false, startedAt: '' };
}

// 删除结果 → 视频审阅面板认识的形状 { requested, succeeded, failed, errors[{video_id, error}] }。
function cleanupBatchResult(requestedIDs, outcome) {
  const errors = [
    ...outcome.failures.map(item => ({ video_id: item.id, error: item.text })),
    ...outcome.kept.map(id => ({ video_id: id, error: '所在磁盘不支持废纸篓，已保留' })),
    ...outcome.cancelled.map(id => ({ video_id: id, error: '已取消，未处理' }))
  ];
  return { requested: requestedIDs.length, succeeded: outcome.removedIDs.length, failed: errors.length, errors };
}

// 清理中心（D-P3-02，详细设计 §3.7）：管理侧页面，两个标签——视频（原片库页里的清理弹窗）与图片（原图片库里的清理审阅页）。
// 当前标签是 App 的页面参数（prop tab，点标签发 update:tab，由 navigateTo('cleanup', { tab }) 落下，与片单同一做法）。
// request 是一次性的请求 { token, open?, relationId?, consolidationTaskID? }：「打开清理审阅」、AI 审阅「去清理」定位同源对、
// 任务中心打开集中整理任务，都落在视频标签上。
// 本页注册命令 library.openCleanup / photos.openCleanup（宿主页，见 appCommands.js 的 COMMAND_HOST_PAGES）。
// 删除之后：视频经 videos-changed（App 的 handleVideosChangedElsewhere，片库重取、顶栏计数刷新），
// 图片经 images-changed（图片库重载）。删除确认框开着或删除进行中发 busy-change(true)：App 不让离开这一页，标签也不能切。
export default {
  name: 'CleanupCenterPage',
  components: { AppTabs, CleanupReviewPanel, PageHeader, PhotoCleanupPage, TrashUndoBanner },
  mixins: [runtimeEventsMixin],
  props: {
    pageActive: { type: Boolean, default: true },
    tab: { type: String, default: DEFAULT_CLEANUP_TAB },
    request: { type: Object, default: null }
  },
  emits: ['update:tab', 'busy-change', 'videos-changed', 'images-changed', 'navigate'],
  data() {
    return {
      videoMounted: false,
      videoState: idleReviewState(),
      imageState: idleImageState(),
      // 「补全感知哈希 / 帧哈希」按钮的运行态（视频标签的两个补全入口）。
      perceptualHash: { running: false },
      frameHash: { running: false },
      pendingVideoRequest: null,
      lastRequestToken: 0
    };
  },
  computed: {
    activeTab() {
      return isCleanupTab(this.tab) ? this.tab : DEFAULT_CLEANUP_TAB;
    },
    imagePanelVisible() {
      return this.pageActive && this.activeTab === 'image';
    },
    // 删除流程中（视频：确认框开着或在删；图片：在删）：不能离开这一页，也不能切到另一个标签——
    // 「不支持废纸篓」的二选一与删除进度挂在这一页上。
    busy() {
      return Boolean(this.videoState.locked || this.imageState.processing);
    },
    tabs() {
      return CLEANUP_TABS.map(tab => ({
        key: tab.key,
        label: tab.label,
        dataTest: `cleanup-tab-${tab.key}`,
        disabled: this.busy && tab.key !== this.activeTab
      }));
    },
    imageStatus() { return photoCleanupStore.status; },
    imageRunning() { return Boolean(this.imageStatus?.running); },
    imageAnalysis() { return Boolean(this.imageStatus?.completed && this.imageStatus?.analysis); },
    // 页头副标题：当前标签的上次分析时间（D-P3-02）。
    analyzedText() {
      if (this.activeTab === 'image') {
        const status = this.imageStatus;
        if (!status) return '图片：正在读取…';
        if (status.running) return '图片：正在分析…';
        if (this.imageAnalysis) return `图片：上次分析 ${formatLocalTime(status.started_at)}`;
        if (status.cancelled) return '图片：上一轮分析已取消';
        if (status.error) return '图片：上一轮分析没有完成';
        return '图片：还没有分析过';
      }
      const state = this.videoState;
      if (!state.statusLoaded) return '视频：正在读取…';
      if (state.analyzing) return '视频：正在分析…';
      if (state.startedAt) return `视频：上次分析 ${formatLocalTime(state.startedAt)}`;
      if (state.cancelled) return '视频：上一轮分析已取消';
      if (state.failed) return '视频：上一轮分析没有完成';
      return '视频：还没有分析过';
    }
  },
  watch: {
    busy: { immediate: true, handler(busy) { this.$emit('busy-change', busy); } },
    // 进入这一页（首次挂载也算）：视频标签读一次状态，第一次打开时按「打开清理审阅」处理（没有结果就开始分析）。
    pageActive: {
      immediate: true,
      handler(active, previous) {
        if (active && !previous) this.queueVideoSync();
      }
    },
    activeTab(tab) {
      if (this.pageActive && tab === 'video') this.queueVideoSync();
    },
    // 图片标签卸下后它报过的状态作废（再挂上时会重新报）。
    imagePanelVisible(visible) {
      if (!visible) this.imageState = idleImageState();
    },
    request: {
      immediate: true,
      handler(request) {
        const token = Number(request?.token || 0);
        if (!token || token === this.lastRequestToken) return;
        this.lastRequestToken = token;
        this.pendingVideoRequest = request;
        this.queueVideoSync();
      }
    }
  },
  mounted() {
    registerCommands(COMMAND_SCOPE, [
      {
        id: 'library.openCleanup',
        group: 'action',
        label: '清理中心 · 视频',
        keywords: ['cleanup', '清理', '重复', 'video', '视频'],
        run: () => this.openFromCommand('video')
      },
      {
        id: 'photos.openCleanup',
        group: 'action',
        label: '清理中心 · 图片',
        keywords: ['cleanup', 'duplicate', '图片', '清理', '重复'],
        run: () => this.openFromCommand('image')
      }
    ]);
    this.registerRuntimeEvent('perceptual-hash-state', data => { this.perceptualHash = { ...this.perceptualHash, ...(data || {}) }; });
    this.registerRuntimeEvent('frame-hash-backfill-state', data => { this.frameHash = { ...this.frameHash, ...(data || {}) }; });
    this.refreshHashStatus();
  },
  beforeUnmount() {
    unregisterCommands(COMMAND_SCOPE);
  },
  methods: {
    selectTab(tab) {
      if (this.busy || !isCleanupTab(tab)) return;
      this.$emit('update:tab', tab);
    },
    // 命令（宿主页已由 App 切到这一页、这个标签）：视频按「打开清理审阅」处理；图片标签挂上时自己读状态。
    // 删除流程中（busy）一律不动：换标签会卸下另一个标签上正在删除的面板，同一标签再「打开」也不该在删除中途重读。
    openFromCommand(tab) {
      if (this.busy) {
        notify(CLEANUP_BUSY_NOTICE);
        return false;
      }
      if (this.activeTab !== tab) this.$emit('update:tab', tab);
      if (tab !== 'video') return true;
      this.pendingVideoRequest = { open: true };
      this.queueVideoSync();
      return true;
    },
    // 进入页面、切到视频标签、收到请求可能落在同一拍：合并成下一拍的一次处理。
    queueVideoSync() {
      if (this._videoSyncQueued) return;
      this._videoSyncQueued = true;
      this.$nextTick(() => {
        this._videoSyncQueued = false;
        this.syncVideo();
      });
    },
    // 视频标签要做的事：第一次挂上或有明确请求时「打开」（读状态，没有结果就开始分析；定位同源对；打开集中整理任务），
    // 其余（回到这一页、切回这个标签）只读一次状态。请求在视频标签可见之前先留着。
    async syncVideo() {
      if (!this.pageActive || this.activeTab !== 'video') return;
      const request = this.pendingVideoRequest;
      this.pendingVideoRequest = null;
      const firstMount = !this.videoMounted;
      this.videoMounted = true;
      await this.$nextTick();
      const panel = this.$refs.videoPanel;
      if (!panel) return;
      // 集中整理任务只打开它的对话框（与原来从片库页打开一样）：不为此发起一轮新的分析；面板挂上时已读过状态。
      const taskID = Number(request?.consolidationTaskID || 0);
      if (taskID > 0) return panel.openConsolidation(taskID);
      const relationId = Number(request?.relationId || 0);
      if (relationId > 0) return panel.open({ relationId });
      if (request || firstMount) return panel.open();
      return panel.refreshStatus();
    },
    reanalyzeVideo() {
      return this.$refs.videoPanel?.reanalyzeCleanupCandidates();
    },
    startImageAnalysis() {
      return this.$refs.imagePanel?.startAnalysis();
    },
    async refreshHashStatus() {
      try {
        this.perceptualHash = { ...this.perceptualHash, ...(await GetPerceptualHashBackfillStatus()) };
      } catch (err) {
        // 读不到只影响两个补全按钮的运行态，不挡清理审阅。
      }
      try {
        this.frameHash = { ...this.frameHash, ...(await GetFrameHashBackfillStatus()) };
      } catch (err) {
        // 同上。
      }
    },
    async startPerceptualHashBackfill() {
      try {
        this.perceptualHash = { ...this.perceptualHash, ...(await StartPerceptualHashBackfill()) };
      } catch (err) {
        notifyError('启动近重复指纹补全失败: ' + err);
      }
    },
    async startFrameHashBackfill() {
      try {
        this.frameHash = { ...this.frameHash, ...(await StartFrameHashBackfill()) };
      } catch (err) {
        notifyError('启动帧哈希补全失败: ' + err);
      }
    },
    // 视频审阅面板勾中的候选由这里删除（原片库页的 trashCleanupVideos）：经删除宿主执行（进度、取消、
    // 不支持废纸篓时的二选一），返回面板认识的结果形状。第一步只删除；撤销提示与通知片库是第二步
    // （afterTrashCleanupVideos），中间留给面板收窄勾选——失败重试时不会对着已进废纸篓的视频再删一次。
    // 清理永远是移到废纸篓（deleteFile: true），不看「删除原文件」设置；删除前的汇总确认在面板里，
    // 不看「删除前确认」设置（它带着「合并元数据」的选择，D-PC48 / D-PC49）。
    async trashCleanupVideos(selectedIDs, { names = {} } = {}) {
      const outcome = await this.$refs.trashUndo.runDelete({ ids: selectedIDs, deleteFile: true, names: { ...(names || {}) } });
      const succeededIDs = outcome.removedIDs;
      const failedIDs = new Set(outcome.remainingIDs);
      this._pendingCleanupDeleteOutcome = outcome;
      return { result: cleanupBatchResult(selectedIDs, outcome), failedIDs, succeededIDs };
    },
    // 面板收窄完勾选之后：给撤销提示（失败项由面板自己报告），再让片库按当前条件重取。
    afterTrashCleanupVideos(succeededIDs = []) {
      const outcome = this._pendingCleanupDeleteOutcome;
      this._pendingCleanupDeleteOutcome = null;
      if (outcome) this.$refs.trashUndo?.showDeleteNotice(outcome, { reportFailures: false });
      this.$emit('videos-changed', { removedIDs: [...(succeededIDs || [])] });
    },
    // 撤销提示或「最近操作」里撤销了清理中心的删除：放掉审阅里的「已移到废纸篓」标记、让片库重取；
    // 回收站对话框恢复的（或整批撤销只回来一部分的）再拉一次清理分析状态。
    async afterVideoTrashRestore(videoIDs, fromTrashDialog) {
      this.$emit('videos-changed', { removedIDs: [] });
      await this.handleVideosRestored(videoIDs, fromTrashDialog);
    },
    // 别处（片库管理的回收站、撤销）恢复了视频：同样放掉审阅里的标记。视频标签还没打开过时什么也不用做。
    async handleVideosRestored(videoIDs = [], fromTrashDialog = false) {
      const panel = this.$refs.videoPanel;
      if (!panel) return;
      for (const videoID of videoIDs || []) panel.forgetTrashed(videoID);
      if (fromTrashDialog) await panel.refreshStatus();
    },
    // 图片标签删除了图片，或撤销了删除（本标签卸下以后也会经 PhotoCleanupPage 的 onDeleted 到这里）：图片库重载。
    handleImagesDeleted() {
      this.$emit('images-changed');
    }
  }
};
</script>

<style scoped>
.cleanup-center { display: flex; flex-direction: column; }
.cleanup-center__panel { min-width: 0; }
</style>
