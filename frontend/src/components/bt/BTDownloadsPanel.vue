<template>
  <section class="bt-downloads-panel" aria-label="资源搜索与下载">
    <div class="bt-panel-heading"><h2>资源与下载</h2><button class="btn btn--secondary" type="button" data-test="bt-settings" @click="$emit('settings')">连接设置</button></div>
    <p class="help-text">搜索 Prowlarr 中的站点，选择文件后交给本机 qBittorrent 下载。退出应用后下载和做种会继续。</p>
    <p v-if="connectionError" role="alert">{{ connectionError }}</p>
    <p v-else-if="connection && !connection.enabled" class="help-text">请在连接设置中启用资源下载。</p>
    <BTResourceSearch :active="active" :initial-query="initialQuery" :indexers="indexers" :result="result" :result-query="resultQuery" :result-indexer-ids="resultIndexerIds" :busy="searchBusy" :preview-busy="previewBusy || Boolean(draft) || !active" :error="searchError" @search="search" @cancel="cancelSearch" @preview="preview" />
    <p v-if="notice" class="help-text" role="status">{{ notice }}</p>
    <p v-if="!tasks.length && taskError" role="alert">{{ taskError }}</p>
    <BTTaskList v-if="tasks.length || !taskError" :tasks="taskViews" :detail="detailView" :loading="listBusy" :readonly="!active" :has-more="Boolean(nextCursor) && tasks.length < 200" :busy-ids="busyIds" :error="controlError || taskError" @refresh="refresh" @load-more="loadMore" @detail="showDetail" @control="control" @play="play" />
    <div class="bt-panel-pages"><button v-if="previousWindows.length" class="btn btn--secondary btn--sm" type="button" :disabled="listBusy" data-test="bt-previous-window" @click="previousWindow">上一组任务</button><button v-if="nextCursor && tasks.length >= 200" class="btn btn--secondary btn--sm" type="button" :disabled="listBusy" data-test="bt-next-window" @click="nextWindow">查看更早任务</button><button v-if="windowCursor" class="btn btn--secondary btn--sm" type="button" :disabled="listBusy" data-test="bt-latest-window" @click="latestWindow">返回最新任务</button></div>
    <p v-if="detailBusy" role="status" class="help-text">正在读取文件明细…</p>
    <div v-if="detail && ['draft', 'metadata_ready'].includes(detail.stage)"><button class="btn btn--secondary" type="button" data-test="bt-open-selection" @click="openSelection(detail)">选择文件…</button></div>
    <BTFileSelectionDialog :visible="Boolean(draft) && active" :draft="draftView" :busy="draftBusy" :readonly="!active || cancelFailed" :cancel-label="cancelFailed ? '关闭窗口' : '取消'" :error="draftError" @close="cancelDraft" @submit="submit" />
  </section>
</template>
<script>
import BTResourceSearch from './BTResourceSearch.vue';
import BTTaskList from './BTTaskList.vue';
import BTFileSelectionDialog from './BTFileSelectionDialog.vue';
import { btFileErrorText } from '../../utils/btFormat.js';

// App supplies this facade; this component never imports Wails or owns downloads.
// GetBTConnection(), ListBTIndexers(), SearchBTResources(query, indexerIds),
// PreviewBTResource(token), SubmitBTDownload(request), GetBTTask(id),
// ControlBTTask(request) return the sanitized D-PT-08 DTOs.
// ListBTTasks(beforeID, limit) returns { tasks: BTTaskView[], next_cursor: string }.
const PREPARING = ['metadata_adding', 'metadata_wait', 'queued_metadata'];
const DRAFTS = ['draft', 'metadata_ready', ...PREPARING];
const RISKY = ['release', 'reconfirm_submit', 'abandon', 'cancel_draft'];
const ERROR_TEXT = {
  bt_restore_pending: '下载任务的恢复核实尚未完成，请稍后重试；如持续未完成，请重新打开应用。',
  bt_connection_unavailable: '请先在连接设置中配置并启用 Prowlarr 和 qBittorrent。',
  bt_connection_active: '仍有受管理任务，暂不能更换服务地址或下载目录。',
  bt_auth_failed: '连接认证失败，请检查已保存的账号和密钥。',
  bt_service_unavailable: '服务暂时不可用，请确认本机 Prowlarr 和 qBittorrent 已启动。',
  bt_credentials_unavailable: '系统钥匙串不可用，请检查系统权限后重新连接。',
  bt_credentials_busy: '系统凭据正在被另一操作使用，请稍后重试。',
  bt_endpoint_invalid: '服务地址无效，请填写本机服务的 HTTP 或 HTTPS 地址。',
  bt_response_invalid: '服务返回的数据无法识别，请检查连接和版本。',
  bt_response_too_large: '服务返回的数据超过处理上限，请缩小搜索范围。',
  bt_task_unavailable: '暂时无法读取此下载任务，请刷新后重试。',
  write_history_unresolved: '先前已发送的操作仍未核实，应用暂停后续操作。请在 qBittorrent 中核实。',
  restart_reconcile: '应用重新打开后，需要核实上次操作结果，不会自动重发。',
  connection_required: '请先在连接设置中连接 Prowlarr 和 qBittorrent。',
  auth_failed: '连接认证失败，请检查连接设置。', service_unavailable: '下载服务暂时不可用，请稍后重试。',
  version_unsupported: '当前下载器版本尚不支持此操作。', search_failed: '搜索失败，请检查站点连接后重试。',
  result_expired: '资源清单已过期，请重新搜索后选择。', metadata_preview_unsupported: '此资源暂不能安全准备文件清单，请选择其他资源。',
  torrent_invalid: '资源的文件清单无效，请选择其他资源。', selection_invalid: '请选择有效文件后再开始下载。',
  existing_external_torrent: '下载器中已有该资源，应用不会接管现有任务。', bt_conflict: '任务状态已变化，请刷新后重试。',
  submission_uncertain: '提交结果尚不确定，请在任务中重新核实。', path_unsafe: '下载保存位置不可用，请检查连接设置。',
  external_changed: '文件或下载设置已变化，请重新核实。', download_missing: '下载器中未找到此任务，请重新核实。',
  import_failed: '部分文件入库失败，请查看文件明细。', user_deleted: '文件已从片库移除，不会自动重新收录。',
  path_not_in_library: '下载位置尚未加入片库扫描范围，请前往设置。', maintenance: '片库维护中，请稍后重试。',
};
function errorText(error) {
  const code = typeof error?.code === 'string' ? error.code : String(error?.message || error || '').split(':', 1)[0];
  return ERROR_TEXT[code] || '操作未完成，请刷新任务后重试。';
}
function taskView(task) {
  return { ...task, allowed_actions: task.actions || [], error_message: task.error_code ? errorText({ code: task.error_code }) : '',
    selected_video_count: task.selected_video_count ?? (task.files ? task.files.filter(f => f.selected && f.kind === 'video').length : undefined),
    files: (task.files || []).map(f => ({ ...f, video_id: f.import_state === 'imported' ? f.video_id : null,
      error_message: f.error_code ? btFileErrorText({ error_code: f.error_code }) : '' })) };
}
export default {
  name: 'BTDownloadsPanel', components: { BTResourceSearch, BTTaskList, BTFileSelectionDialog },
  props: { api: { type: Object, required: true }, active: { type: Boolean, default: true }, initialQuery: { type: String, default: '' }, refreshKey: { type: [String, Number], default: 0 } },
  emits: ['settings', 'play', 'changed'],
  data: () => ({ readFlight: null, pendingRefresh: false, pendingPage: null, pendingDetail: null, alive: true, generation: 0, requests: {}, timer: null, connection: null, indexers: [], connectionError: '',
    result: null, resultQuery: '', resultIndexerIds: [], searchError: '', searchBusy: false, previewBusy: false,
    tasks: [], nextCursor: '', windowCursor: '', previousWindows: [], listBusy: false, taskError: '', controlError: '', detail: null, detailID: '', detailBusy: false,
    draft: null, draftBusy: false, cancelFailed: false, draftError: '', busyIds: [], notice: '' }),
  computed: {
    taskViews() { return this.tasks.map(taskView); }, detailView() { return this.detail ? taskView(this.detail) : null; },
    draftView() { return this.draft ? { ...this.draft, draft_id: this.draft.id, save_root: this.draft.save_path,
      preparation: PREPARING.includes(this.draft.stage) ? 'metadata_pending' : this.draft.stage } : null; },
  },
  watch: {
    active(value) { this.invalidate(); if (value) this.activate(); },
    api() { this.invalidate(); this.result = null; this.draft = null; this.cancelFailed = false; this.detail = null; this.detailID = ''; this.tasks = []; this.nextCursor = ''; this.windowCursor = ''; this.previousWindows = []; if (this.active) this.activate(); },
    initialQuery() { this.cancelSearch(); this.result = null; },
    refreshKey() { if (this.active) this.refresh(); },
  },
  mounted() { if (this.active) this.activate(); },
  beforeUnmount() { this.alive = false; this.invalidate(); },
  methods: {
    token(kind) { const serial = (this.requests[kind] || 0) + 1; this.requests[kind] = serial; return { kind, serial, generation: this.generation }; },
    current(t) { return this.alive && this.active && t.generation === this.generation && this.requests[t.kind] === t.serial; },
    invalidate() { this.generation++; this.pendingRefresh = false; this.pendingPage = null; this.pendingDetail = null; clearTimeout(this.timer); this.timer = null; this.searchBusy = false; this.previewBusy = false; this.listBusy = false; this.detailBusy = false; this.draftBusy = false; this.busyIds = []; },
    async activate() {
      const t = this.token('configuration');
      const results = await Promise.allSettled([this.api.GetBTConnection(), this.api.ListBTIndexers()]);
      if (!this.current(t)) return;
      this.connectionError = '';
      if (results[0].status === 'fulfilled') this.connection = results[0].value;
      else this.connectionError = errorText(results[0].reason);
      if (results[1].status === 'fulfilled') this.indexers = results[1].value;
      else this.connectionError = errorText(results[1].reason);
      if (!this.connectionError && this.connection?.enabled && this.indexers.some(s => s.enabled !== false) && !this.initialQuery.trim() && !this.result && !this.searchBusy) this.search({ query: '', indexer_ids: [] });
      await this.refresh(); this.schedule();
    },
    schedule() { clearTimeout(this.timer); if (!this.alive || !this.active) return; this.timer = setTimeout(async () => { await this.refresh(); this.schedule(); }, 5000); },
    cancelSearch() { this.token('search'); this.token('preview'); this.searchBusy = false; this.previewBusy = false; },
    async search(request) {
      if (!this.active || typeof request.query !== 'string' || request.query.trim().length > 200) return;
      this.token('preview'); this.previewBusy = false;
      const t = this.token('search'); this.searchBusy = true; this.searchError = ''; this.result = null;
      const query = request.query.trim(); const ids = [...request.indexer_ids];
      try { const result = await this.api.SearchBTResources(query, ids); if (this.current(t)) { this.result = result; this.resultQuery = query; this.resultIndexerIds = ids; } }
      catch (error) { if (this.current(t)) this.searchError = errorText(error); }
      finally { if (this.current(t)) this.searchBusy = false; }
    },
    async preview(token) {
      if (!this.active || this.previewBusy || this.draft) return;
      const t = this.token('preview'); this.previewBusy = true; this.searchError = '';
      try { const task = await this.api.PreviewBTResource(token); if (!this.current(t)) return; this.token('list'); this.upsert(task); if (DRAFTS.includes(task.stage)) this.openSelection(task); else { this.detailID = task.id; this.detail = task; this.notice = '该资源已有任务，可在下面查看当前状态。'; } }
      catch (error) { if (this.current(t)) this.searchError = errorText(error); }
      finally { if (this.current(t)) this.previewBusy = false; }
    },
    openSelection(task) { if (this.active && !this.cancelFailed && DRAFTS.includes(task.stage)) { this.draft = task; this.draftError = ''; } },
    async submit(request) {
      const draft = this.draft;
      if (!this.active || this.draftBusy || this.cancelFailed || !draft || !['draft', 'metadata_ready'].includes(draft.stage)) return;
      if (request.draft_id !== draft.id || request.expected_revision !== draft.revision || !(Date.parse(draft.expires_at) > Date.now())) { this.draftError = ERROR_TEXT.result_expired; return; }
      const selected = new Set(request.selected_file_ids);
      if (!selected.size || selected.size !== request.selected_file_ids.length || [...selected].some(id => !(draft.files || []).some(f => f.id === id && f.selectable !== false && !f.padding))) { this.draftError = ERROR_TEXT.selection_invalid; return; }
      const t = this.token('draftWrite'); this.draftBusy = true; this.draftError = ''; this.token('list'); this.token('detail');
      try { const task = await this.api.SubmitBTDownload({ ...request, selected_file_ids: [...selected] }); if (this.current(t)) { this.upsert(task); this.draft = null; this.windowCursor = ''; this.previousWindows = []; this.$emit('changed', task.id); this.notice = '任务已提交，请在下载列表查看进度。'; } }
      catch (error) { if (this.current(t)) this.draftError = errorText(error); }
      finally { if (this.current(t)) { this.draftBusy = false; await this.refresh(); } }
    },
    async cancelDraft() {
      if (!this.active || !this.draft || this.draftBusy) return;
      if (this.cancelFailed) { this.token('draftRead'); this.token('detail'); this.draft = null; this.detail = null; this.detailID = ''; this.cancelFailed = false; this.notice = '窗口已关闭，任务仍保留在列表中。请重新读取任务状态后再操作。'; return; }
      if (!DRAFTS.includes(this.draft.stage)) { this.draft = null; return; }
      this.token('draftRead'); this.token('list');
      const t = this.token('draftWrite'); this.draftBusy = true; this.draftError = '';
      try { const task = await this.api.ControlBTTask({ task_id: this.draft.id, expected_revision: this.draft.revision, action: 'cancel_draft', confirmed: true }); if (this.current(t)) { this.upsert(task); this.draft = null; this.$emit('changed', task.id); } }
      catch (error) { if (this.current(t)) { this.cancelFailed = true; this.draftError = `${errorText(error)} 未能确认取消，任务仍保留在列表中。可以关闭窗口，重新读取任务状态后再操作。`; } }
      finally { if (this.current(t)) this.draftBusy = false; }
    },
    upsert(task) { const old = this.tasks.find(t => t.id === task.id); if (old && old.revision > task.revision) return; this.tasks = [task, ...this.tasks.filter(t => t.id !== task.id)].slice(0, 200); if (this.detailID === task.id) this.detail = task; },
    refresh() { if (!this.alive || !this.active) return Promise.resolve(); this.pendingRefresh = true; return this.drainReads(); },
    drainReads() {
      if (this.readFlight) return this.readFlight;
      this.readFlight = this.runReads().finally(() => { this.readFlight = null; });
      return this.readFlight;
    },
    async runReads() {
      while (this.alive && this.active) {
        if (this.busyIds.length || this.draftBusy) return;
        if (this.pendingPage) {
          const page = this.pendingPage; this.pendingPage = null;
          if (await this.fetchTasks(page.append, page.cursor)) {
            if (page.history) { this.windowCursor = page.cursor; this.previousWindows = page.history; }
          }
        } else if (this.pendingDetail !== null) {
          const id = this.pendingDetail; this.pendingDetail = null; await this.fetchDetail(id);
        } else if (this.pendingRefresh) {
          this.pendingRefresh = false; await this.refreshOnce();
        } else return;
      }
    },
    async refreshOnce() {
      const generation = this.generation;
      await this.fetchTasks(false, this.windowCursor);
      if (!this.alive || !this.active || generation !== this.generation) return;
      if (this.detailID) {
        const id = this.detailID; if (this.pendingDetail === id) this.pendingDetail = null;
        await this.fetchDetail(id);
      }
      if (!this.alive || !this.active || generation !== this.generation) return;
      if (this.draft && !this.draftBusy && !this.cancelFailed) {
        const id = this.draft.id; const t = this.token('draftRead');
        try { const task = await this.api.GetBTTask(id); if (this.current(t) && this.draft?.id === id && !this.draftBusy) { this.draft = DRAFTS.includes(task.stage) ? task : null; this.upsert(task); } }
        catch (error) { if (this.current(t) && this.draft?.id === id) this.draftError = errorText(error); }
      }
    },
    pageRead(page) { if (!this.alive || !this.active) return Promise.resolve(); this.pendingPage = page; return this.drainReads(); },
    loadMore() { if (this.nextCursor && this.tasks.length < 200 && !this.listBusy) return this.pageRead({ append: true, cursor: this.nextCursor }); },
    nextWindow() { if (!this.nextCursor || this.listBusy) return; return this.pageRead({ append: false, cursor: this.nextCursor, history: [...this.previousWindows, this.windowCursor].slice(-20) }); },
    previousWindow() { if (!this.previousWindows.length || this.listBusy) return; const history = [...this.previousWindows]; return this.pageRead({ append: false, cursor: history.pop(), history }); },
    latestWindow() { return this.pageRead({ append: false, cursor: '', history: [] }); },
    async fetchTasks(append, cursor = this.windowCursor) {
      if (!this.active) return;
      const t = this.token('list'); this.listReadOwner = t.serial; this.listBusy = true; this.taskError = '';
      try { const page = await this.api.ListBTTasks(cursor, append ? 50 : Math.max(50, Math.min(200, this.tasks.length))); if (!this.current(t)) return;
        const rows = append ? [...this.tasks, ...page.tasks] : page.tasks;
        this.tasks = [...new Map(rows.map(task => [task.id, task])).values()].slice(0, 200); this.nextCursor = page.next_cursor; return true;
      } catch (error) { if (this.current(t)) this.taskError = errorText(error); }
      finally { if (this.listReadOwner === t.serial) { this.listBusy = false; this.listReadOwner = null; } }
    },
    showDetail(id) { if (!this.alive || !this.active) return Promise.resolve(); this.detailID = id; this.token('detail'); if (this.detail?.id !== id) this.detail = null; this.pendingDetail = id; return this.drainReads(); },
    async fetchDetail(id) {
      if (!this.active) return;
      const t = this.token('detail'); this.detailReadOwner = t.serial; this.detailBusy = true;
      if (this.detail?.id !== id) this.detail = null;
      try { const task = await this.api.GetBTTask(id); if (this.current(t) && this.detailID === id) this.detail = task; }
      catch (error) { if (this.current(t)) this.taskError = errorText(error); }
      finally { if (this.detailReadOwner === t.serial) { this.detailBusy = false; this.detailReadOwner = null; } }
    },
    async control(request) {
      const task = this.tasks.find(t => t.id === request.task_id);
      if (!this.active || !task || task.revision !== request.expected_revision || !task.actions?.includes(request.action) || this.busyIds.includes(task.id) || (RISKY.includes(request.action) && !request.confirmed)) return;
      const t = this.token(`control:${task.id}`); this.busyIds.push(task.id); this.controlError = ''; this.token('list'); this.token('detail');
      try { const changed = await this.api.ControlBTTask({ ...request }); if (this.current(t)) { this.upsert(changed); this.$emit('changed', changed.id); } }
      catch (error) { if (this.current(t)) this.controlError = errorText(error); }
      finally { if (this.current(t)) { this.busyIds = this.busyIds.filter(id => id !== task.id); await this.refresh(); } }
    },
    play(id) { if (this.active && this.detail?.files?.some(f => f.import_state === 'imported' && f.video_id === id)) this.$emit('play', id); },
  },
};
</script>
<style scoped>
.bt-downloads-panel { display: grid; gap: var(--space-4); }
.bt-panel-heading { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--space-2); }
.bt-panel-heading h2, .bt-downloads-panel > p { margin: 0; }
</style>
