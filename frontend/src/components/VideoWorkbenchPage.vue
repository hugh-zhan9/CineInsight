<template>
  <section class="page-content workbench-page" data-test="workbench-page">
    <div class="workbench-heading">
      <h2>视频工作台</h2>
      <p>顺序合并、批量去片头与高清替换。成品另存入库，原片保持不变；确认成品无误后才提供原片清理入口。</p>
    </div>
    <div class="workbench-layout">
      <EditProjectList
        :projects="projects"
        :selected-id="selectedId"
        :busy="busy"
        :error="listError"
        @select="selectProject"
        @refresh="refreshAll"
        @cancel="cancelExport"
        @requeue="requeueFromList"
        @delete="deleteProject"
      />
      <div class="workbench-main">
        <p v-if="!selectedId" class="workbench-placeholder" data-test="workbench-placeholder">从左侧选择一个编辑项目。</p>
        <p v-else-if="!project" class="workbench-placeholder">正在读取项目…</p>
        <template v-else>
          <header class="workbench-project" data-test="workbench-project">
            <div>
              <h3>{{ project.title || '未命名项目' }}</h3>
              <span class="help-text">{{ kindLabel(project.kind) }} · {{ statusLabel(project.status) }} · 版本 {{ project.revision }}</span>
            </div>
            <span class="edit-list__spacer"></span>
            <span v-if="saving" class="help-text" data-test="workbench-saving">正在保存…</span>
            <button v-if="cancellable" type="button" class="btn-secondary btn-compact" data-test="workbench-cancel-export" @click="cancelExport(project)">取消导出</button>
            <button v-if="duplicable" type="button" class="btn-secondary btn-compact" data-test="workbench-duplicate" :disabled="Boolean(busy[project.id])" @click="duplicateProject(project)">复制为新草稿</button>
          </header>
          <p v-if="project.error_message && project.status !== 'completed'" class="edit-error" data-test="workbench-project-error">{{ project.error_message }}</p>
          <p v-if="!editable && project.status !== 'analyzing'" class="help-text" data-test="workbench-readonly">项目已排队或已导出，配方只读。</p>

          <component
            :is="editorComponent"
            :key="`editor-${project.id}`"
            :recipe="recipe"
            :editable="editable"
            :preflight="preflight"
            :fast-preflight="fastPreflight"
            :analysis="project.analysis"
            :analyzing="analyzing"
            :analysis-progress="analysisProgress"
            :videos="videos"
            @change="onRecipeChange"
            @analyze="analyze"
            @cancel-analysis="cancelAnalysis"
          />

          <EditExportPanel
            v-if="showExport"
            :key="`export-${project.id}`"
            :project="project"
            :recipe="recipe"
            :preflight="preflight"
            :loading="preflightLoading"
            :error="preflightError"
            :editable="editable"
            :requeue="requeueable"
            :busy="queueBusy || saving"
            :missing-keys="missingKeys"
            :videos="videos"
            @run-preflight="runPreflight"
            @set-mode="setMode"
            @change="onRecipeChange"
            @queue="queue"
          />

          <EditResults
            v-if="project.items && project.items.length"
            :key="`results-${project.id}`"
            :project="project"
            @open-video="$emit('open-video', $event)"
            @cleanup-sources="$emit('cleanup-sources', $event)"
          />
        </template>
      </div>
    </div>
  </section>
</template>

<script>
import {
  AnalyzeEditProject, CancelEditAnalysis, CancelEditProject, DeleteEditProject, DuplicateEditProject, GetEditProject, GetVideoEditStatus, GetVideosByIDs,
  ListEditProjects, PreflightEditProject, QueueEditProject, RequeueEditProject, UpdateEditRecipe
} from '../../wailsjs/go/main/App';
import EditExportPanel from './video-workbench/EditExportPanel.vue';
import EditProjectList from './video-workbench/EditProjectList.vue';
import EditResults from './video-workbench/EditResults.vue';
import HDReplaceEditor from './video-workbench/HDReplaceEditor.vue';
import MergeEditor from './video-workbench/MergeEditor.vue';
import TrimIntroEditor from './video-workbench/TrimIntroEditor.vue';
import { runtimeEventsMixin } from './video-list/runtimeEvents.js';
import { confirmAction, notify, notifyError, notifySuccess } from '../utils/feedback.js';
import {
  EDIT_ACTIVE, EDIT_CANCELLABLE, EDIT_REQUEUEABLE, cloneRecipe, createRequestGeneration, editErrorCode, editErrorText, editKindLabel,
  editRecipeVideoIDs, editStatusLabel, isEditConflict, preflightFresh
} from '../utils/videoEdit.js';
import './video-workbench/workbench.css';

const EDITORS = { merge: 'MergeEditor', trim_intro: 'TrimIntroEditor', hd_replace: 'HDReplaceEditor' };
const LIST_LIMIT = 50;
const LIST_REFRESH_DELAY_MS = 800;

// 视频工作台页（视频编辑合同「界面」）。左栏项目列表，右栏按类型编辑、预检导出与结果。
// 配方改动先落在本地副本，按顺序以 expected_revision 保存（CAS）；冲突时重读并丢弃本地改动。
// 所有读项目的请求取一个代次，写操作的响应也推进代次，晚到的旧读结果一律丢弃。
// 进度与状态跟 video-edit-state 事件走：同状态的进度事件就地更新，状态变化时重读。
export default {
  name: 'VideoWorkbenchPage',
  components: { EditExportPanel, EditProjectList, EditResults, HDReplaceEditor, MergeEditor, TrimIntroEditor },
  mixins: [runtimeEventsMixin],
  props: {
    pageActive: { type: Boolean, default: true },
    // 宿主要求打开某个项目：{ projectID, token }（片库新建项目、任务中心「在工作台中打开」）。
    openRequest: { type: Object, default: null }
  },
  emits: ['open-video', 'cleanup-sources'],
  data() {
    return {
      projects: [],
      listError: '',
      selectedId: 0,
      project: null,
      recipe: null,
      baseRevision: 0,
      videos: {},
      saving: false,
      pendingRecipe: null,
      pendingMode: '',
      preflight: null,
      preflightLoading: false,
      preflightError: '',
      missingKeys: [],
      queueBusy: false,
      analysisProgress: 0,
      busy: {}
    };
  },
  computed: {
    editorComponent() {
      return EDITORS[this.project?.kind] || 'MergeEditor';
    },
    analyzing() {
      return this.project?.status === 'analyzing';
    },
    editable() {
      return this.project?.status === 'draft';
    },
    cancellable() {
      return EDIT_CANCELLABLE.includes(this.project?.status);
    },
    requeueable() {
      return EDIT_REQUEUEABLE.includes(this.project?.status);
    },
    // 排队后配方只读；失败、部分成功、取消、中断或已完成的项目可复制出草稿重新编辑（原项目不动）。
    duplicable() {
      return Boolean(this.project) && !this.editable && !EDIT_ACTIVE.includes(this.project.status);
    },
    showExport() {
      return this.editable || this.requeueable;
    },
    fastPreflight() {
      const preflight = this.preflight;
      return preflightFresh(preflight, this.project) && this.project.mode === 'fast' && preflight.fast?.available ? preflight : null;
    }
  },
  watch: {
    openRequest: {
      immediate: true,
      handler(request) {
        const id = Number(request?.projectID || 0);
        if (id > 0) {
          this.loadList();
          this.selectProject(id);
        }
      }
    },
    pageActive(active) {
      if (!active) return;
      this.loadList();
      if (this.selectedId) this.loadProject(this.selectedId);
    }
  },
  // 代次计数器放在 beforeCreate：openRequest 的 immediate 监听在 created 之前就会触发。
  beforeCreate() {
    this.listGeneration = createRequestGeneration();
    this.projectGeneration = createRequestGeneration();
    this.preflightGeneration = createRequestGeneration();
  },
  mounted() {
    if (!this.openRequest?.projectID) this.loadList();
    this.loadAnalysisStatus();
    this.registerRuntimeEvent('video-edit-state', event => this.handleEditEvent(event));
  },
  beforeUnmount() {
    clearTimeout(this.listTimer);
  },
  methods: {
    kindLabel: editKindLabel,
    statusLabel: editStatusLabel,
    async loadList() {
      const generation = this.listGeneration.next();
      try {
        const list = await ListEditProjects(LIST_LIMIT);
        if (!this.listGeneration.isCurrent(generation)) return;
        this.projects = Array.isArray(list) ? list : [];
        this.listError = '';
      } catch (err) {
        if (this.listGeneration.isCurrent(generation)) this.listError = `读取编辑项目失败：${editErrorText(err)}`;
      }
    },
    // 进度事件可能很密：列表最多每 800 毫秒重读一次。
    scheduleListRefresh() {
      if (this.listTimer) return;
      this.listTimer = setTimeout(() => {
        this.listTimer = null;
        this.loadList();
      }, LIST_REFRESH_DELAY_MS);
    },
    refreshAll() {
      this.loadList();
      if (this.selectedId) this.loadProject(this.selectedId);
    },
    async loadAnalysisStatus() {
      try {
        const status = await GetVideoEditStatus();
        if (status?.analyzing_project_id && status.analyzing_project_id === this.selectedId) {
          this.analysisProgress = Number(status.analysis_progress || 0);
        }
      } catch (_err) {
        // 读不到时进度条从 0 开始，事件到了再更新。
      }
    },
    async selectProject(id) {
      const projectID = Number(id);
      if (projectID !== this.selectedId) {
        this.selectedId = projectID;
        this.project = null;
        this.recipe = null;
        this.pendingRecipe = null;
        this.pendingMode = '';
        this.preflight = null;
        this.preflightError = '';
        this.preflightGeneration.next();
        this.missingKeys = [];
        this.analysisProgress = 0;
      }
      const view = await this.loadProject(projectID);
      if (view && (view.status === 'draft' || EDIT_REQUEUEABLE.includes(view.status))) this.runPreflight();
      if (view?.status === 'analyzing') this.loadAnalysisStatus();
      return view;
    },
    // 读项目：取代次，晚到的（或已切到别的项目的）结果丢弃。
    async loadProject(id) {
      const generation = this.projectGeneration.next();
      try {
        const view = await GetEditProject(id);
        if (!this.projectGeneration.isCurrent(generation) || id !== this.selectedId) return null;
        this.applyProject(view);
        return view;
      } catch (err) {
        if (!this.projectGeneration.isCurrent(generation) || id !== this.selectedId) return null;
        if (editErrorCode(err) === 'edit_project_not_found') {
          this.clearSelection();
          this.loadList();
          notify('这个编辑项目已不存在。');
        } else {
          notifyError(`读取编辑项目失败：${editErrorText(err)}`);
        }
        return null;
      }
    },
    // 写操作（保存、分析、排队）返回的项目：推进代次，让此前发出、还没回来的读请求作废。
    acceptProject(view) {
      if (!view || view.id !== this.selectedId) return;
      this.projectGeneration.next();
      this.applyProject(view);
    },
    // 有本地改动在保存时只更新项目状态，不覆盖正在编辑的本地配方。
    applyProject(view) {
      this.project = view;
      if (!this.saving && !this.pendingRecipe) {
        this.recipe = cloneRecipe(view.recipe);
        this.baseRevision = view.revision;
      }
      if (view.status !== 'analyzing') this.analysisProgress = 0;
      this.ensureVideos(view.recipe);
    },
    clearSelection() {
      this.selectedId = 0;
      this.project = null;
      this.recipe = null;
      this.preflight = null;
      this.pendingRecipe = null;
    },
    async ensureVideos(recipe) {
      const missing = editRecipeVideoIDs(recipe).filter(id => !this.videos[id]);
      if (!missing.length) return;
      try {
        const list = await GetVideosByIDs(missing);
        const next = { ...this.videos };
        for (const video of list || []) next[video.id] = video;
        this.videos = next;
      } catch (_err) {
        // 取不到名字时编辑器显示「视频 #id」，不影响编辑。
      }
    },
    onRecipeChange(next) {
      if (!this.editable) return;
      this.recipe = next;
      this.pendingRecipe = next;
      this.flushSaves();
    },
    setMode(mode) {
      if (!this.editable || mode === this.project.mode) return;
      this.pendingMode = mode;
      this.pendingRecipe = this.recipe;
      this.flushSaves();
    },
    // 按顺序保存本地改动：每次带上一次保存成功后的 revision（CAS）。冲突说明项目在别处被改过
    // （例如分析刚写回结果、或已被排队），重读服务器上的版本并丢弃本地改动。
    async flushSaves() {
      if (this.saving) return;
      this.saving = true;
      const projectID = this.selectedId;
      let failure = null;
      try {
        while (this.pendingRecipe && projectID === this.selectedId) {
          const recipe = this.pendingRecipe;
          const mode = this.pendingMode;
          this.pendingRecipe = null;
          this.pendingMode = '';
          const view = await UpdateEditRecipe({ project_id: projectID, expected_revision: this.baseRevision, recipe, mode, title: '' });
          if (projectID !== this.selectedId) return;
          this.baseRevision = view.revision;
          this.acceptProject(view);
        }
      } catch (err) {
        failure = err;
        this.pendingRecipe = null;
        this.pendingMode = '';
      } finally {
        this.saving = false;
      }
      if (projectID !== this.selectedId) return;
      if (failure) {
        if (isEditConflict(failure)) notify('项目已在别处被修改（例如自动识别刚写回结果），已重新载入；刚才的修改没有保存。');
        else notifyError(`保存失败：${editErrorText(failure)}`);
        await this.loadProject(projectID);
      } else if (this.project) {
        this.recipe = cloneRecipe(this.project.recipe);
        this.baseRevision = this.project.revision;
      }
    },
    async analyze(windowMs) {
      if (!this.editable) return;
      if (this.saving || this.pendingRecipe) {
        notify('正在保存修改，稍后再开始分析。');
        return;
      }
      const projectID = this.selectedId;
      try {
        const view = await AnalyzeEditProject({ project_id: projectID, expected_revision: this.project.revision, window_ms: Number(windowMs) || 0 });
        this.analysisProgress = 0;
        this.acceptProject(view);
      } catch (err) {
        notifyError(`无法开始分析：${editErrorText(err)}`);
        if (isEditConflict(err)) await this.loadProject(projectID);
      }
    },
    async cancelAnalysis() {
      const projectID = this.selectedId;
      try {
        await CancelEditAnalysis(projectID);
      } catch (err) {
        notifyError(`取消分析失败：${editErrorText(err)}`);
      }
      await this.loadProject(projectID);
    },
    async runPreflight() {
      const projectID = this.selectedId;
      const generation = this.preflightGeneration.next();
      this.preflightLoading = true;
      this.preflightError = '';
      try {
        const result = await PreflightEditProject(projectID);
        if (!this.preflightGeneration.isCurrent(generation) || projectID !== this.selectedId) return;
        this.preflight = result;
        this.missingKeys = [];
      } catch (err) {
        if (this.preflightGeneration.isCurrent(generation)) this.preflightError = `预检失败：${editErrorText(err)}`;
      } finally {
        if (this.preflightGeneration.isCurrent(generation)) this.preflightLoading = false;
      }
    },
    // 排队导出（draft）或继续未完成项（中断/失败/部分成功）。后端会重新预检：有错误或还有没确认的警告时
    // 不排队，把新的预检结果与缺的警告标出来。
    async queue(acknowledged) {
      const project = this.project;
      if (!project || this.queueBusy) return;
      const requeue = EDIT_REQUEUEABLE.includes(project.status);
      const request = { project_id: project.id, expected_revision: project.revision, acknowledged_warnings: [...(acknowledged || [])] };
      this.queueBusy = true;
      try {
        const result = requeue ? await RequeueEditProject(request) : await QueueEditProject(request);
        if (project.id !== this.selectedId) return;
        if (result?.queued) {
          this.missingKeys = [];
          this.acceptProject(result.project);
          notifySuccess(requeue ? '已重新排队未完成的项' : '已排队导出');
          this.loadList();
          return;
        }
        this.preflightGeneration.next();
        this.preflightLoading = false;
        if (result?.preflight) this.preflight = result.preflight;
        this.missingKeys = result?.unacknowledged || [];
        notify(this.missingKeys.length ? '还有警告没有确认，请逐条勾选后再导出。' : '预检有阻止导出的问题，请先处理。');
      } catch (err) {
        notifyError(`${requeue ? '继续' : '排队'}失败：${editErrorText(err)}`);
        if (isEditConflict(err)) await this.loadProject(project.id);
      } finally {
        this.queueBusy = false;
      }
    },
    async withBusy(projectID, task) {
      if (this.busy[projectID]) return;
      this.busy = { ...this.busy, [projectID]: true };
      try {
        await task();
      } finally {
        const { [projectID]: _done, ...rest } = this.busy;
        this.busy = rest;
      }
    },
    cancelExport(summary) {
      return this.withBusy(summary.id, async () => {
        const ok = await confirmAction({
          title: '取消导出',
          message: '排队的项不再导出，进行中的项会停止并清理临时文件；已经完成入库的成品保留。',
          confirmText: '取消导出',
          cancelText: '继续导出',
          danger: true
        });
        if (!ok) return;
        try {
          await CancelEditProject(summary.id);
        } catch (err) {
          notifyError(`取消失败：${editErrorText(err)}`);
        }
        this.loadList();
        if (summary.id === this.selectedId) await this.loadProject(summary.id);
      });
    },
    // 列表里的「继续」：打开项目并直接用它已确认过的警告重新排队；预检有变化时停在导出面板。
    requeueFromList(summary) {
      return this.withBusy(summary.id, async () => {
        const view = await this.selectProject(summary.id);
        if (view && EDIT_REQUEUEABLE.includes(view.status)) await this.queue(view.acknowledged_warnings || []);
      });
    },
    duplicateProject(summary) {
      return this.withBusy(summary.id, async () => {
        try {
          const copied = await DuplicateEditProject(summary.id);
          this.loadList();
          if (copied?.id) await this.selectProject(copied.id);
        } catch (err) {
          notifyError(`复制失败：${editErrorText(err)}`);
        }
      });
    },
    deleteProject(summary) {
      return this.withBusy(summary.id, async () => {
        const ok = await confirmAction({
          title: '删除编辑项目',
          message: `删除「${summary.title || '未命名项目'}」的编辑记录？来源视频与已入库的成品都不会被删除。`,
          confirmText: '删除记录',
          danger: true
        });
        if (!ok) return;
        try {
          await DeleteEditProject(summary.id);
          if (summary.id === this.selectedId) this.clearSelection();
        } catch (err) {
          notifyError(`删除失败：${editErrorText(err)}`);
        }
        this.loadList();
      });
    },
    // video-edit-state：{ project_id, item_id, status, item_status, phase, progress }。
    handleEditEvent(event) {
      const projectID = Number(event?.project_id || 0);
      if (!projectID) return;
      this.scheduleListRefresh();
      if (projectID !== this.selectedId || !this.project) return;
      if (event.status === 'deleted') {
        this.clearSelection();
        return;
      }
      if (event.status === 'analyzing' && !event.item_id && this.project.status === 'analyzing') {
        this.analysisProgress = Number(event.progress || 0);
        return;
      }
      const item = event.item_id ? (this.project.items || []).find(entry => entry.id === event.item_id) : null;
      if (item && item.status === event.item_status && this.project.status === event.status) {
        item.progress = Number(event.progress || 0);
        if (event.phase) item.phase = event.phase;
        return;
      }
      this.loadProject(projectID);
    }
  }
};
</script>

<style scoped>
.workbench-page { padding: 20px 24px 40px; }
.workbench-heading { margin-bottom: 14px; }
.workbench-heading h2 { margin: 0 0 4px; }
.workbench-heading p { margin: 0; color: var(--text-secondary); font-size: 13px; }
.workbench-layout { display: grid; grid-template-columns: minmax(220px, 280px) minmax(0, 1fr); gap: 18px; align-items: start; }
.workbench-main { min-width: 0; display: grid; gap: 14px; }
.workbench-placeholder { color: var(--text-secondary); margin-top: 24px; text-align: center; }
.workbench-project { display: flex; align-items: center; gap: 10px; }
.workbench-project h3 { margin: 0 0 2px; font-size: 16px; }
@media (max-width: 760px) {
  .workbench-page { padding: 16px; }
  .workbench-layout { grid-template-columns: minmax(0, 1fr); }
}
</style>
