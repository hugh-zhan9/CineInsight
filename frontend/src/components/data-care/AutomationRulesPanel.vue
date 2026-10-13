<template>
  <!-- 本地规则（D-RA-01..05）：新入库的条目按规则自动加标签、加入作品集；也可以先试运行再对现有内容运行。 -->
  <section class="data-care-panel rules-panel" data-test="data-care-panel-rules">
    <header class="rules-head">
      <div>
        <h3>本地规则</h3>
        <p class="rules-muted">新入库的条目自动套用启用的规则：只加标签、加入作品集，只加不撤，你手动去掉的不会再加回来；规则不移动、不改名、不删除文件。新建或修改规则不会自动处理已有内容，要点「对现有内容运行」。</p>
      </div>
      <div class="rules-head__actions">
        <button type="button" class="btn btn--secondary" :disabled="running || !enabledIDs.length" data-test="rules-run-all" @click="openRunAll">对现有内容运行…</button>
        <button type="button" class="btn btn--primary" data-test="rules-new" @click="openEditor(null)">新建规则</button>
      </div>
    </header>

    <div v-if="runText" :class="['rules-run', { 'rules-run--active': running }]" data-test="rules-run-status">
      <span>{{ runText }}</span>
      <button v-if="running" type="button" class="btn btn--secondary btn--sm" data-test="rules-run-cancel" @click="cancelRun">取消</button>
    </div>
    <p v-if="error" class="rules-alert" role="alert" data-test="rules-error">{{ error }}</p>
    <div v-if="loading && !rules.length" role="status" aria-label="正在读取规则" data-test="rules-loading"><SkeletonBlock :lines="3" /></div>
    <EmptyState v-else-if="!rules.length" title="还没有规则" message="比如「下载目录里的视频都打上『待整理』」，点「新建规则」开始。" data-test="rules-empty">
      <template #actions>
        <button type="button" class="btn btn--primary btn--sm" data-test="rules-empty-new" @click="openEditor(null)">新建规则</button>
      </template>
    </EmptyState>

    <ul v-else class="rules-list">
      <li v-for="(rule, index) in rules" :key="rule.id" :class="['rules-row', { 'rules-row--off': !rule.enabled }]" data-test="rules-row">
        <label class="switch rules-row__toggle" :title="rule.enabled ? '停用' : '启用'">
          <input type="checkbox" :checked="rule.enabled" :disabled="busy" data-test="rules-toggle" @change="setEnabled(rule, $event.target)" />
          <span class="slider"></span>
        </label>
        <div class="rules-row__main">
          <div class="rules-row__title">
            <strong>{{ rule.name }}</strong>
            <span class="rules-chip" data-test="rules-kind">{{ kindLabel(rule.media_kind) }}</span>
          </div>
          <small class="rules-row__line" data-test="rules-conditions">当{{ summary(rule).conditions }}</small>
          <small class="rules-row__line" data-test="rules-actions">就{{ summary(rule).actions || '（没有动作）' }}</small>
          <small v-if="rule.disabled_reason" class="rules-warning" data-test="rules-disabled-reason">{{ reasonLabel(rule.disabled_reason) }}</small>
          <small v-if="rule.last_run_at" class="rules-muted">上次对现有内容运行：{{ formatTime(rule.last_run_at) }}</small>
        </div>
        <div class="rules-row__actions">
          <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || index === 0" title="上移" data-test="rules-up" @click="move(index, -1)">上移</button>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || index === rules.length - 1" title="下移" data-test="rules-down" @click="move(index, 1)">下移</button>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="!rule.actions.length" title="先算会处理多少条，不改任何内容" data-test="rules-preview" @click="openPreview(rule)">试运行</button>
          <button type="button" class="btn btn--secondary btn--sm" data-test="rules-edit" @click="openEditor(rule)">编辑</button>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="busy" data-test="rules-delete" @click="remove(rule)">删除</button>
        </div>
      </li>
    </ul>

    <AutomationRuleEditor
      v-if="editor"
      :rule="editor.rule"
      :tags="tags"
      :collections="collections"
      :saving="editor.saving"
      :error="editor.error"
      @save="save"
      @cancel="editor = null"
    />

    <!-- 试运行（D-RA-01「先试运行再执行」）：只算会处理多少条、列出前几条，不改任何内容。 -->
    <DialogShell v-if="previewState" :title="`试运行：${previewState.rule.name}`" size="md" data-test="rules-preview-dialog" @close="previewState = null">
      <div class="rules-preview">
        <p v-if="previewState.loading" class="rules-muted" role="status">正在计算…</p>
        <p v-else-if="previewState.error" class="rules-alert" role="alert">{{ previewState.error }}</p>
        <template v-else>
          <p data-test="rules-preview-count">现有内容里有 <strong>{{ previewState.result.count }}</strong> 条会被处理（已经处理过的不算）。</p>
          <ul v-if="previewState.result.samples.length" class="rules-samples">
            <li v-for="sample in previewState.result.samples" :key="sample.id" data-test="rules-preview-sample">
              <button v-if="sample.media_kind === 'video'" type="button" class="btn-link rules-sample__name" :title="sample.name" @click="$emit('open-video-id', sample.id)">{{ sample.name }}</button>
              <span v-else class="rules-sample__name">{{ sample.name }}</span>
              <small class="rules-muted" :title="sample.directory">{{ sample.directory }}</small>
            </li>
          </ul>
          <p v-if="previewState.result.count > previewState.result.samples.length" class="rules-muted">只列出前 {{ previewState.result.samples.length }} 条。</p>
        </template>
      </div>
      <template #footer>
        <button type="button" class="btn btn--secondary" data-test="rules-preview-close" @click="previewState = null">关闭</button>
        <button type="button" class="btn btn--primary" :disabled="previewState.loading || !previewState.result || !previewState.result.count || running" data-test="rules-preview-run" @click="runFromPreview">执行</button>
      </template>
    </DialogShell>

    <!-- D-RA-01「先试运行再执行」：逐条规则先算会处理多少条，确认后才运行。 -->
    <DialogShell v-if="runAll" title="对现有内容运行" size="md" data-test="rules-run-all-dialog" @close="runAll = null">
      <div class="rules-preview">
        <p class="rules-muted">只处理还没被这些规则处理过的条目；已完成的部分在取消后保留。</p>
        <ul class="rules-samples">
          <li v-for="item in runAll.items" :key="item.rule.id" class="rules-run-all__row" data-test="rules-run-all-item">
            <span class="rules-sample__name">{{ item.rule.name }}</span>
            <small v-if="item.loading" class="rules-muted">正在计算…</small>
            <small v-else-if="item.error" class="rules-warning">{{ item.error }}</small>
            <small v-else data-test="rules-run-all-count">{{ item.count }} 条</small>
          </li>
        </ul>
        <p v-if="!runAllLoading" data-test="rules-run-all-total">合计 <strong>{{ runAllTotal }}</strong> 条<span v-if="runAll.items.length > 1" class="rules-muted">（同一条目符合几条规则时分别计数）</span></p>
      </div>
      <template #footer>
        <button type="button" class="btn btn--secondary" data-test="rules-run-all-cancel" @click="runAll = null">取消</button>
        <button type="button" class="btn btn--primary" :disabled="runAllLoading || !runAllTotal || running" data-test="rules-run-all-confirm" @click="confirmRunAll">执行</button>
      </template>
    </DialogShell>
  </section>
</template>

<script>
import {
  CancelAutomationRuleRun, DeleteAutomationRule, GetAllTags, GetAutomationRuleRunStatus, ListAutomationRules, ListCollections,
  PreviewAutomationRule, ReorderAutomationRules, RunAutomationRules, SaveAutomationRule, SetAutomationRuleEnabled
} from '../../../wailsjs/go/main/App';
import AutomationRuleEditor from './AutomationRuleEditor.vue';
import DialogShell from '../ui/DialogShell.vue';
import EmptyState from '../ui/EmptyState.vue';
import SkeletonBlock from '../ui/SkeletonBlock.vue';
import { DISABLED_REASON_LABELS, MEDIA_KIND_LABELS, parseRuleError, ruleSummary } from '../../utils/automationRules.js';
import { confirmAction, notifySuccess } from '../../utils/feedback.js';

export const AUTOMATION_RULES_RUN_EVENT = 'automation-rules-run';

export default {
  name: 'AutomationRulesPanel',
  components: { AutomationRuleEditor, DialogShell, EmptyState, SkeletonBlock },
  props: { directories: { type: Array, default: () => [] } },
  emits: ['open-video-id', 'run-command', 'videos-changed', 'open-library-missing', 'navigate'],
  data() {
    return {
      rules: [],
      loading: false,
      error: '',
      busy: false,
      tags: [],
      collections: [],
      editor: null,
      previewState: null,
      // 「对现有内容运行」的确认框：{ items: [{ rule, loading, count, error }] }。
      runAll: null,
      runStatus: null,
      runEventOff: null
    };
  },
  computed: {
    enabledIDs() {
      return this.rules.filter(rule => rule.enabled && rule.actions.length).map(rule => rule.id);
    },
    running() {
      return Boolean(this.runStatus?.running);
    },
    runAllLoading() {
      return Boolean(this.runAll?.items.some(item => item.loading));
    },
    runAllTotal() {
      return (this.runAll?.items || []).reduce((sum, item) => sum + (item.count || 0), 0);
    },
    runText() {
      const status = this.runStatus;
      if (!status || (!status.running && !status.completed)) return '';
      if (status.running) {
        return status.total ? `正在对现有内容运行规则：已处理 ${status.processed}/${status.total} 条，新增 ${status.added} 处标签或作品集关系` : '正在准备运行规则…';
      }
      if (status.error) return status.error;
      if (status.cancelled) return `已取消：处理了 ${status.processed}/${status.total} 条，新增 ${status.added} 处，已完成的部分保留`;
      return `运行完成：${status.matched} 条有改动，新增 ${status.added} 处标签或作品集关系`;
    }
  },
  mounted() {
    this.load();
    this.loadRunStatus();
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn(AUTOMATION_RULES_RUN_EVENT, status => this.onRunStatus(status));
      if (typeof off === 'function') this.runEventOff = off;
    }
  },
  beforeUnmount() {
    this.runEventOff?.();
  },
  methods: {
    summary: ruleSummary,
    kindLabel(kind) {
      return MEDIA_KIND_LABELS[kind] || kind;
    },
    reasonLabel(reason) {
      return DISABLED_REASON_LABELS[reason] || '已停用';
    },
    formatTime(value) {
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? '' : date.toLocaleString('zh-CN', { hour12: false });
    },
    async load() {
      this.loading = true;
      try {
        this.rules = (await ListAutomationRules()) || [];
        this.error = '';
      } catch (err) {
        this.error = `读取规则失败：${parseRuleError(err).message}`;
      } finally {
        this.loading = false;
      }
    },
    async loadRunStatus() {
      try {
        const status = await GetAutomationRuleRunStatus();
        if (status) this.runStatus = status;
      } catch {
        // 进度取不到不影响规则列表，之后的事件照样会到。
      }
    },
    onRunStatus(status) {
      if (!status) return;
      const wasRunning = this.running;
      this.runStatus = status;
      if (wasRunning && !status.running) {
        this.load();
        if (status.added > 0) this.$emit('videos-changed', {});
      }
    },
    async loadTargets() {
      try {
        const [tags, collections] = await Promise.all([GetAllTags(), ListCollections('', '', 0, 200)]);
        this.tags = tags || [];
        this.collections = (collections || []).map(item => item.collection);
      } catch (err) {
        this.error = `读取标签与作品集失败：${parseRuleError(err).message}`;
      }
    },
    async openEditor(rule) {
      this.editor = { rule, saving: false, error: null };
      await this.loadTargets();
    },
    async save(input) {
      if (!this.editor) return;
      this.editor.saving = true;
      this.editor.error = null;
      try {
        await SaveAutomationRule(input);
        this.editor = null;
        notifySuccess('规则已保存。要处理已有内容，请点「对现有内容运行」');
        await this.load();
      } catch (err) {
        if (this.editor) this.editor.error = parseRuleError(err);
      } finally {
        if (this.editor) this.editor.saving = false;
      }
    },
    // withBusy 执行一次改动；失败时显示原因并重取列表。返回是否成功。
    async withBusy(action) {
      this.busy = true;
      try {
        await action();
        this.error = '';
        return true;
      } catch (err) {
        // 失败后重取列表（复原开关与顺序），重取会清掉错误，所以错误在它之后再写。
        const message = parseRuleError(err).message;
        await this.load();
        this.error = message;
        return false;
      } finally {
        this.busy = false;
      }
    },
    async setEnabled(rule, input) {
      const enabled = input.checked;
      const ok = await this.withBusy(async () => {
        const updated = await SetAutomationRuleEnabled(rule.id, enabled);
        const index = this.rules.findIndex(item => item.id === rule.id);
        if (index !== -1 && updated) this.rules.splice(index, 1, updated);
      });
      // 绑定值没变时 Vue 不会改回勾选状态，失败了手动复原。
      if (!ok) input.checked = !enabled;
    },
    move(index, delta) {
      const ids = this.rules.map(rule => rule.id);
      const target = index + delta;
      if (target < 0 || target >= ids.length) return null;
      [ids[index], ids[target]] = [ids[target], ids[index]];
      return this.withBusy(async () => {
        await ReorderAutomationRules(ids);
        await this.load();
      });
    },
    async remove(rule) {
      const ok = await confirmAction({
        title: '删除规则',
        message: `删除「${rule.name}」？它加过的标签和作品集关系都会保留，只删除规则本身和它的处理记录。`,
        confirmText: '删除',
        danger: true
      });
      if (!ok) return;
      await this.withBusy(async () => {
        await DeleteAutomationRule(rule.id);
        await this.load();
      });
    },
    async openPreview(rule) {
      this.previewState = { rule, loading: true, error: '', result: null };
      const state = this.previewState;
      try {
        state.result = await PreviewAutomationRule(rule.id);
      } catch (err) {
        state.error = parseRuleError(err).message;
      } finally {
        state.loading = false;
      }
    },
    // openRunAll 先逐条试运行启用的规则（D-RA-01 先试运行再执行），确认后只跑有条目要处理的规则。
    async openRunAll() {
      const rules = this.rules.filter(rule => this.enabledIDs.includes(rule.id));
      this.runAll = { items: rules.map(rule => ({ rule, loading: true, count: 0, error: '' })) };
      const state = this.runAll;
      await Promise.all(state.items.map(async item => {
        try {
          const preview = await PreviewAutomationRule(item.rule.id);
          item.count = preview?.count || 0;
        } catch (err) {
          item.error = parseRuleError(err).message;
        } finally {
          item.loading = false;
        }
      }));
    },
    async confirmRunAll() {
      const ids = (this.runAll?.items || []).filter(item => !item.error && item.count > 0).map(item => item.rule.id);
      this.runAll = null;
      await this.runRules(ids);
    },
    async runFromPreview() {
      const rule = this.previewState?.rule;
      this.previewState = null;
      if (rule) await this.runRules([rule.id]);
    },
    async runRules(ids) {
      if (!ids.length) return;
      try {
        this.runStatus = await RunAutomationRules(ids);
        this.error = '';
      } catch (err) {
        this.error = parseRuleError(err).message;
      }
    },
    async cancelRun() {
      try {
        await CancelAutomationRuleRun();
      } catch (err) {
        this.error = parseRuleError(err).message;
      }
    }
  }
};
</script>

<style scoped>
.rules-panel { display: grid; gap: 14px; }
.rules-head { display: flex; align-items: flex-start; gap: var(--space-4); }
.rules-head > div:first-child { flex: 1 1 auto; }
.rules-head h3 { font-size: var(--fs-section); font-weight: var(--fw-bold); margin-bottom: var(--space-1); }
.rules-head__actions { display: flex; gap: var(--space-2); flex-shrink: 0; }
.rules-muted { color: var(--text-secondary); font-size: var(--fs-caption); line-height: 1.5; }
.rules-alert { color: var(--danger-text); font-size: var(--fs-secondary); }
.rules-run { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-3) 14px; border: 1px solid var(--hairline); border-radius: var(--radius-lg); background: var(--panel-bg); font-size: var(--fs-secondary); }
.rules-run--active { border-color: var(--accent-color); }
.rules-run span { flex: 1 1 auto; }
.rules-list { display: grid; gap: var(--space-2); list-style: none; margin: 0; padding: 0; }
.rules-row { display: flex; align-items: flex-start; gap: 14px; padding: var(--space-3) 14px; border: 1px solid var(--hairline); border-radius: var(--radius-lg); background: var(--panel-bg); }
.rules-row--off .rules-row__main { opacity: 0.7; }
.rules-row__toggle { flex-shrink: 0; }
.rules-row__main { flex: 1 1 auto; display: grid; gap: 3px; min-width: 0; }
.rules-row__title { display: flex; align-items: center; gap: var(--space-2); }
.rules-row__line { color: var(--text-secondary); font-size: var(--fs-caption); overflow-wrap: anywhere; }
.rules-row__actions { display: flex; flex-wrap: wrap; gap: 6px; flex-shrink: 0; justify-content: flex-end; max-width: 280px; }
.rules-chip { padding: 1px var(--space-2); border-radius: var(--radius-pill); border: 1px solid var(--hairline); color: var(--text-secondary); font-size: var(--fs-caption); }
.rules-warning { color: var(--danger-text); font-size: var(--fs-caption); }
.rules-preview { display: grid; gap: var(--space-2); }
.rules-samples { display: grid; gap: 6px; max-height: 50vh; overflow-y: auto; margin: var(--space-2) 0 0; padding: 0; list-style: none; }
.rules-samples li { display: grid; gap: 2px; min-width: 0; }
.rules-sample__name { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: left; font-size: var(--fs-secondary); color: var(--text-primary); }
.btn-link.rules-sample__name { color: var(--accent-text); }
.rules-samples small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rules-run-all__row { grid-template-columns: minmax(0, 1fr) auto; align-items: baseline; gap: var(--space-3); }
</style>
