<template>
  <!-- 清除确认（D-LD-01/04）：列出连带影响，输入「清除记录」才可执行；不可撤销，不进废纸篓。 -->
  <div class="modal-overlay" data-test="lost-purge-dialog" @click.self="close">
    <div class="modal lost-purge" role="dialog" aria-modal="true" aria-label="彻底清除">
      <h2>彻底清除 {{ kindNoun }}记录</h2>
      <p v-if="loading" class="lost-purge__muted">正在统计连带影响…</p>
      <template v-else-if="preview && !result">
        <p class="lost-purge__lead" data-test="lost-purge-count">
          将清除 {{ preview.count }} 条{{ kindNoun }}记录（原文件约 {{ formatBytes(preview.size) }}，已经找不到）。清除不可撤销，也不进废纸篓。
        </p>
        <p v-if="preview.not_purgeable" class="lost-purge__muted" data-test="lost-purge-not-purgeable">
          另有 {{ preview.not_purgeable }} 条现在不能清除（卷未挂载、读取出错或已不再失联），不会处理。
        </p>
        <ul class="lost-purge__impact" data-test="lost-purge-impact">
          <li v-for="row in impactRows" :key="row.key" :data-test="`lost-purge-impact-${row.key}`">
            <span>{{ row.label }}</span><strong>{{ row.value }}</strong>
          </li>
        </ul>
        <p class="lost-purge__muted">观看笔记（片段书签、观影日记）照常保留；执行时会逐条复核，卷没挂载、文件又出现或超分进行中的会跳过。</p>
        <div class="lost-purge__backup">
          <span>清除前建议先备份数据库。</span>
          <button type="button" class="btn-secondary btn-compact" :disabled="applying" data-test="lost-purge-backup" @click="$emit('backup')">先备份</button>
        </div>
        <label class="lost-purge__confirm">
          输入「{{ confirmText }}」确认
          <input v-model="typed" class="text-input" :disabled="applying" :placeholder="confirmText" data-test="lost-purge-confirm-input" />
        </label>
      </template>
      <div v-else-if="result" class="lost-purge__result" data-test="lost-purge-result">
        <p>已清除 {{ result.done }} 条<span v-if="result.skipped">，跳过 {{ result.skipped }} 条</span><span v-if="result.failed">，失败 {{ result.failed }} 条</span><span v-if="result.cancelled">（已取消，其余未处理）</span>。</p>
        <ul v-if="skipped.length" class="lost-purge__reasons">
          <li v-for="row in skipped" :key="row.reason" data-test="lost-purge-skip-reason">{{ row.label }}：{{ row.count }} 条</li>
        </ul>
      </div>
      <p v-if="error" class="lost-purge__error" role="alert" data-test="lost-purge-error">{{ error }}</p>
      <div class="modal-actions">
        <template v-if="!result">
          <button v-if="applying" type="button" class="btn-secondary" data-test="lost-purge-cancel-run" @click="cancelRun">取消执行</button>
          <button v-else type="button" class="btn-secondary" data-test="lost-purge-close" @click="close">取消</button>
          <button type="button" class="btn-danger" :disabled="!canApply" data-test="lost-purge-apply" @click="apply">
            {{ applying ? '清除中…' : '彻底清除' }}
          </button>
        </template>
        <button v-else type="button" class="btn-primary" data-test="lost-purge-done" @click="close">完成</button>
      </div>
    </div>
  </div>
</template>

<script>
import { ApplyLostPurge, CancelLostMediaTask, PreviewLostPurge } from '../../../wailsjs/go/main/App';
import { formatBytes } from '../../utils/mediaDetails.js';
import { LOST_PURGE_CONFIRM_TEXT, lostMediaErrorText, skippedSummary } from '../../utils/lostMedia.js';

export default {
  name: 'LostPurgeDialog',
  props: {
    kind: { type: String, required: true },
    ids: { type: Array, required: true }
  },
  emits: ['close', 'done', 'backup'],
  data() {
    return { loading: true, preview: null, typed: '', applying: false, result: null, error: '', confirmText: LOST_PURGE_CONFIRM_TEXT };
  },
  computed: {
    kindNoun() {
      return this.kind === 'image' ? '图片' : '视频';
    },
    canApply() {
      return Boolean(this.preview?.count) && this.typed === LOST_PURGE_CONFIRM_TEXT && !this.applying;
    },
    impactRows() {
      const p = this.preview || {};
      const rows = this.kind === 'image'
        ? [{ key: 'views', label: '查看记录（回顾里不再计入）', value: `${p.image_view_events || 0} 条` }]
        : [{ key: 'plays', label: '播放记录（回顾、洞察里不再计入）', value: `${p.play_events || 0} 条` }];
      rows.push(
        { key: 'tags', label: '标签关联', value: `${p.tag_links || 0} 个` },
        { key: 'people', label: '人物关联', value: `${p.person_links || 0} 个` }
      );
      if (this.kind !== 'image') {
        rows.push(
          { key: 'collections', label: '作品集关联', value: `${p.collection_links || 0} 个` },
          { key: 'versions', label: '版本组', value: `解散 ${p.version_groups_dissolved || 0} 个 · 缩小 ${p.version_groups_shrunk || 0} 个` },
          { key: 'notes', label: '观看笔记（保留）', value: `${(p.bookmarks || 0) + (p.diary_entries || 0)} 条` }
        );
      }
      rows.push({ key: 'derived', label: '缩略图等派生文件', value: formatBytes(p.derived_bytes || 0) });
      return rows;
    },
    skipped() {
      return skippedSummary(this.result);
    }
  },
  async mounted() {
    try {
      this.preview = await PreviewLostPurge({ kind: this.kind, ids: this.ids, confirm: '' });
    } catch (err) {
      this.error = lostMediaErrorText(err, '统计连带影响失败，请稍后重试。');
    } finally {
      this.loading = false;
    }
  },
  methods: {
    formatBytes,
    async apply() {
      if (!this.canApply) return;
      this.applying = true;
      this.error = '';
      try {
        this.result = await ApplyLostPurge({ kind: this.kind, ids: this.ids, confirm: this.typed });
        this.$emit('done', this.result);
      } catch (err) {
        this.error = lostMediaErrorText(err, '清除没有完成，请稍后重试。');
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
.lost-purge { display: grid; gap: 12px; width: min(560px, calc(100vw - 48px)); }
.lost-purge__lead { margin: 0; font-size: 13.5px; }
.lost-purge__muted { margin: 0; color: var(--text-secondary); font-size: 12.5px; }
.lost-purge__impact { display: grid; gap: 6px; margin: 0; padding: 10px 14px; border: 1px solid var(--hairline); border-radius: 10px; list-style: none; }
.lost-purge__impact li { display: flex; justify-content: space-between; gap: 12px; font-size: 13px; }
.lost-purge__impact strong { font-variant-numeric: tabular-nums; }
.lost-purge__backup { display: flex; align-items: center; gap: 10px; padding: 8px 12px; border-radius: 10px; background: var(--warning-soft); color: var(--warning-text); font-size: 12.5px; }
.lost-purge__backup span { flex: 1; }
.lost-purge__confirm { display: grid; gap: 6px; font-size: 13px; }
.lost-purge__result p { margin: 0; }
.lost-purge__reasons { margin: 6px 0 0; padding-left: 18px; color: var(--text-secondary); font-size: 13px; }
.lost-purge__error { margin: 0; color: var(--danger-color); font-size: 12.5px; }
</style>
