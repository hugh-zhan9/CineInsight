<template>
  <!-- 找回预览（D-LD-03）：按配对结果勾选，执行后原记录改路径、id 不变。 -->
  <div class="modal-overlay" data-test="lost-relink-dialog" @click.self="close">
    <div class="modal lost-dialog" role="dialog" aria-modal="true" :aria-label="title">
      <h2>{{ title }}</h2>
      <template v-if="!result">
        <p class="lost-dialog__hint" data-test="lost-relink-summary">
          找到 {{ preview.items.length }} 条候选，默认勾选可靠的配对；「可能」的要自己确认。
          <span v-if="preview.unmatched">另有 {{ preview.unmatched }} 条没找到对应文件。</span>
        </p>
        <p v-if="outsideCount" class="lost-dialog__warn" data-test="lost-relink-outside-hint">
          有 {{ outsideCount }} 条的新位置不在任何扫描目录内，不能找回：请先把这个文件夹加为扫描目录，否则下一次全量扫描又会把它们标成失联。
        </p>
        <ul class="lost-dialog__list">
          <li v-for="item in preview.items" :key="item.id" :class="['lost-pair', { 'lost-pair--off': !item.selectable }]" data-test="lost-relink-row">
            <input
              type="checkbox"
              :checked="selected.has(item.id)"
              :disabled="!item.selectable || applying"
              :aria-label="`找回 ${item.name}`"
              data-test="lost-relink-toggle"
              @change="toggle(item.id)"
            />
            <div class="lost-pair__main">
              <strong>{{ item.name }}</strong>
              <small class="lost-path" :title="item.old_path">原：{{ item.old_path }}</small>
              <small v-if="item.new_path" class="lost-path" :title="item.new_path">新：{{ item.new_path }}</small>
              <small v-for="candidate in item.match === 'ambiguous' ? item.candidates.slice(0, 3) : []" :key="candidate" class="lost-path">候选：{{ candidate }}</small>
            </div>
            <span :class="['lost-chip', `lost-chip--${item.match}`]" data-test="lost-relink-match">{{ matchLabel(item.match) }}</span>
            <span v-if="item.outside_roots" class="lost-chip lost-chip--warn">不在扫描目录</span>
            <span v-if="item.occupied" class="lost-chip lost-chip--warn">已被占用</span>
          </li>
        </ul>
      </template>
      <div v-else class="lost-dialog__result" data-test="lost-relink-result">
        <p>已找回 {{ result.done }} 条<span v-if="result.skipped">，跳过 {{ result.skipped }} 条</span><span v-if="result.failed">，失败 {{ result.failed }} 条</span><span v-if="result.cancelled">（已取消，其余未处理）</span>。</p>
        <ul v-if="skipped.length" class="lost-dialog__reasons">
          <li v-for="row in skipped" :key="row.reason">{{ row.label }}：{{ row.count }} 条</li>
        </ul>
      </div>
      <p v-if="error" class="lost-dialog__warn" role="alert" data-test="lost-relink-error">{{ error }}</p>
      <div class="modal-actions">
        <template v-if="!result">
          <button v-if="applying" type="button" class="btn-secondary" data-test="lost-relink-cancel-run" @click="cancelRun">取消执行</button>
          <button v-else type="button" class="btn-secondary" data-test="lost-relink-close" @click="close">取消</button>
          <button type="button" class="btn-primary" :disabled="selected.size === 0 || applying" data-test="lost-relink-apply" @click="apply">
            {{ applying ? '找回中…' : `找回所选 (${selected.size})` }}
          </button>
        </template>
        <button v-else type="button" class="btn-primary" data-test="lost-relink-done" @click="close">完成</button>
      </div>
    </div>
  </div>
</template>

<script>
import { ApplyLostRelink, CancelLostMediaTask } from '../../../wailsjs/go/main/App';
import { LOST_MATCH_LABELS, lostMediaErrorText, skippedSummary } from '../../utils/lostMedia.js';

export default {
  name: 'LostRelinkDialog',
  props: {
    kind: { type: String, required: true },
    title: { type: String, default: '找回' },
    preview: { type: Object, required: true }
  },
  emits: ['close', 'done'],
  data() {
    return {
      selected: new Set(this.preview.items.filter(item => item.selected && item.selectable).map(item => item.id)),
      applying: false,
      result: null,
      error: ''
    };
  },
  computed: {
    outsideCount() {
      return this.preview.items.filter(item => item.outside_roots).length;
    },
    skipped() {
      return skippedSummary(this.result);
    }
  },
  methods: {
    matchLabel(match) {
      return LOST_MATCH_LABELS[match] || match;
    },
    toggle(id) {
      const next = new Set(this.selected);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      this.selected = next;
    },
    async apply() {
      const pairs = this.preview.items
        .filter(item => this.selected.has(item.id) && item.selectable)
        .map(item => ({ id: item.id, new_path: item.new_path }));
      this.applying = true;
      this.error = '';
      try {
        this.result = await ApplyLostRelink({ kind: this.kind, pairs });
        this.$emit('done', this.result);
      } catch (err) {
        this.error = lostMediaErrorText(err, '找回没有完成，请稍后重试。');
      } finally {
        this.applying = false;
      }
    },
    cancelRun() {
      return CancelLostMediaTask();
    },
    close() {
      if (!this.applying) this.$emit('close');
    }
  }
};
</script>

<style scoped>
.lost-dialog { display: grid; gap: 12px; width: min(760px, calc(100vw - 48px)); max-height: calc(100vh - 80px); overflow: auto; }
.lost-dialog__hint { margin: 0; color: var(--text-secondary); font-size: 13px; }
.lost-dialog__warn { margin: 0; padding: 8px 12px; border-radius: 10px; background: var(--warning-soft); color: var(--warning-text); font-size: 12.5px; }
.lost-dialog__list { display: grid; gap: 6px; max-height: 50vh; overflow: auto; margin: 0; padding: 0; list-style: none; }
.lost-dialog__result p { margin: 0; }
.lost-dialog__reasons { margin: 6px 0 0; padding-left: 18px; color: var(--text-secondary); font-size: 13px; }
.lost-pair { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border: 1px solid var(--hairline); border-radius: 10px; }
.lost-pair--off { opacity: 0.7; }
.lost-pair__main { display: grid; flex: 1; gap: 2px; min-width: 0; }
.lost-pair__main strong { overflow: hidden; font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.lost-path { overflow: hidden; color: var(--text-muted); font-size: 11px; font-family: var(--font-mono, monospace); text-overflow: ellipsis; white-space: nowrap; }
.lost-chip { flex: none; padding: 1px 8px; border-radius: 999px; background: var(--control-bg); color: var(--text-secondary); font-size: 11px; white-space: nowrap; }
.lost-chip--exact, .lost-chip--name_size { background: var(--accent-soft); color: var(--accent-text); }
.lost-chip--size_only, .lost-chip--warn { background: var(--warning-soft); color: var(--warning-text); }
.lost-chip--size_mismatch, .lost-chip--ambiguous { color: var(--text-muted); }
</style>
