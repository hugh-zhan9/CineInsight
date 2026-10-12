<template>
  <Teleport to="body">
    <BaseModal class="quote-card-dialog" aria-label="台词卡" data-test="quote-card-dialog" @close="$emit('close')">
      <div class="quote-card-dialog__header">
        <h3>台词卡</h3>
        <div class="quote-card-dialog__templates" role="radiogroup" aria-label="版式">
          <button
            v-for="option in templates"
            :key="option.value"
            type="button"
            role="radio"
            :aria-checked="template === option.value"
            :class="['btn-secondary', 'btn-compact', { 'quote-card-dialog__template--active': template === option.value }]"
            :data-test="`quote-card-template-${option.value}`"
            @click="template = option.value"
          >{{ option.label }}</button>
        </div>
        <button type="button" class="btn-secondary btn-compact" data-test="quote-card-close" @click="$emit('close')">关闭</button>
      </div>

      <div class="quote-card-dialog__stage">
        <canvas v-show="frameState === 'ready'" ref="canvas" class="quote-card-dialog__canvas" data-test="quote-card-canvas" :data-template="template"></canvas>
        <p v-if="frameState === 'loading'" class="quote-card-dialog__hint" data-test="quote-card-loading">正在读取这一刻的画面…</p>
        <p v-else-if="frameState === 'error'" class="quote-card-dialog__error" role="alert" data-test="quote-card-frame-error">{{ frameError }}</p>
      </div>

      <label class="quote-card-dialog__field">
        <span>台词</span>
        <textarea
          v-model="text"
          class="text-input settings-textarea quote-card-dialog__quote"
          rows="3"
          :maxlength="maxLength"
          placeholder="输入或修改要放在卡片上的台词，可以换行"
          data-test="quote-card-quote"
        ></textarea>
      </label>
      <p class="quote-card-dialog__meta" data-test="quote-card-meta">
        <span>{{ metaText }}</span>
        <span>{{ text.length }}/{{ maxLength }}</span>
      </p>
      <p v-if="truncated" class="quote-card-dialog__hint" data-test="quote-card-truncated">台词太长，卡片上只显示前 {{ maxLines }} 行。</p>
      <p
        v-if="status"
        :class="['quote-card-dialog__status', `quote-card-dialog__status--${status.kind}`]"
        :role="status.kind === 'error' ? 'alert' : 'status'"
        data-test="quote-card-status"
      >{{ status.text }}</p>

      <div class="modal-actions">
        <button type="button" class="btn-primary" data-test="quote-card-copy" :disabled="!canExport" @click="copyImage">{{ busy === 'copy' ? '复制中…' : '复制图片' }}</button>
        <button type="button" class="btn-secondary" data-test="quote-card-save" :disabled="!canExport" @click="saveImage">{{ busy === 'save' ? '保存中…' : '保存为 PNG' }}</button>
      </div>
    </BaseModal>
  </Teleport>
</template>

<script>
import { markRaw } from 'vue';
import BaseModal from './ui/BaseModal.vue';
import { SaveQuoteCard } from '../../wailsjs/go/main/App';
import { formatSceneTime } from '../utils/sceneSearch.js';
import {
  QUOTE_CARD_TEMPLATES,
  QUOTE_MAX_LENGTH,
  canvasToPNGBlob,
  cleanQuoteText,
  layoutQuoteCard,
  loadQuoteCardFrame,
  paintQuoteCard,
  quoteCardFileName,
  quoteCardFrameURL,
  quoteCardTemplate
} from '../utils/quoteCard.js';

function errorText(err) {
  return String(err?.message || err || '').trim() || '未知错误';
}

// 台词卡弹窗（2026-10-12）：取那一刻的单帧（/preview/frame，同源，画到 canvas 上不会污染画布），
// 按所选版式实时重画预览；「复制图片」写系统剪贴板，「保存为 PNG」交给 App.SaveQuoteCard 弹原生保存框。
// 画面读不出来时只报错并禁用导出，不出纯文字卡。宿主每次打开都重新挂载本组件（v-if）。
export default {
  name: 'QuoteCardDialog',
  components: { BaseModal },
  props: {
    videoID: { type: Number, required: true },
    atMS: { type: Number, default: 0 },
    quote: { type: String, default: '' },
    title: { type: String, default: '' }
  },
  emits: ['close'],
  data() {
    return {
      templates: QUOTE_CARD_TEMPLATES,
      template: QUOTE_CARD_TEMPLATES[0].value,
      text: cleanQuoteText(this.quote),
      maxLength: QUOTE_MAX_LENGTH,
      frame: null,
      frameState: 'loading',
      frameError: '',
      truncated: false,
      busy: '',
      // { kind: success | error, text }
      status: null
    };
  },
  computed: {
    atTime() {
      return Math.max(0, Math.round(Number(this.atMS) || 0));
    },
    metaText() {
      const title = this.title.trim();
      return `${title ? `《${title}》 · ` : ''}${formatSceneTime(this.atTime)}`;
    },
    maxLines() {
      return quoteCardTemplate(this.template).maxLines;
    },
    canExport() {
      return this.frameState === 'ready' && !this.busy;
    }
  },
  watch: {
    template() {
      this.render();
    },
    text() {
      this.render();
    }
  },
  mounted() {
    this.loadFrame();
  },
  beforeUnmount() {
    this.unmounted = true;
  },
  methods: {
    async loadFrame() {
      try {
        const image = await loadQuoteCardFrame(quoteCardFrameURL(this.videoID, this.atTime));
        if (this.unmounted) return;
        this.frame = markRaw(image);
        this.frameState = 'ready';
        this.render();
      } catch (_err) {
        if (this.unmounted) return;
        this.frameState = 'error';
        this.frameError = '读取这一刻的画面失败：文件可能离线、已移动，或这个时间点超出了片长。无法生成台词卡。';
      }
    },
    render() {
      if (this.frameState !== 'ready' || !this.frame) return;
      const canvas = this.$refs.canvas;
      const ctx = canvas?.getContext('2d');
      if (!ctx) {
        this.frameState = 'error';
        this.frameError = '当前环境无法绘制卡片。';
        return;
      }
      const layout = layoutQuoteCard({
        template: this.template,
        frameWidth: this.frame.naturalWidth || this.frame.width,
        frameHeight: this.frame.naturalHeight || this.frame.height,
        quote: this.text,
        title: this.title,
        atMS: this.atTime,
        measure: (value, font) => {
          ctx.font = font;
          return ctx.measureText(value).width;
        }
      });
      this.truncated = layout.truncated;
      paintQuoteCard(canvas, ctx, this.frame, layout);
    },
    // 剪贴板写入必须在这次点击里同步发起（WebKit 要求用户手势仍有效），所以不先 await 出 PNG，
    // 而是把「生成 PNG」的 Promise 直接交给 ClipboardItem。不支持或失败时只报错，保存按钮照常可用，不悄悄改走别的路。
    copyImage() {
      if (!this.canExport) return undefined;
      this.status = null;
      const clipboard = navigator.clipboard;
      if (typeof clipboard?.write !== 'function' || typeof window.ClipboardItem !== 'function') {
        this.status = { kind: 'error', text: '当前环境不支持复制图片，可以改用「保存为 PNG」。' };
        return undefined;
      }
      this.busy = 'copy';
      let pending;
      try {
        pending = clipboard.write([new window.ClipboardItem({ 'image/png': canvasToPNGBlob(this.$refs.canvas) })]);
      } catch (err) {
        pending = Promise.reject(err);
      }
      return Promise.resolve(pending).then(
        () => { this.status = { kind: 'success', text: '已复制到剪贴板。' }; },
        err => { this.status = { kind: 'error', text: `复制失败：${errorText(err)}。可以改用「保存为 PNG」。` }; }
      ).finally(() => { this.busy = ''; });
    },
    async saveImage() {
      if (!this.canExport) return;
      this.status = null;
      this.busy = 'save';
      try {
        const dataURL = this.$refs.canvas.toDataURL('image/png');
        const encoded = dataURL.slice(dataURL.indexOf(',') + 1);
        const saved = await SaveQuoteCard(encoded, quoteCardFileName(this.title, this.atTime));
        // 用户在保存框里点了取消：什么也不提示。
        if (saved) this.status = { kind: 'success', text: `已保存到 ${saved}` };
      } catch (err) {
        this.status = { kind: 'error', text: `保存失败：${errorText(err)}` };
      } finally {
        this.busy = '';
      }
    }
  }
};
</script>

<style scoped>
:deep(.quote-card-dialog) { width: min(880px, 94vw); max-width: 94vw; max-height: 92vh; display: flex; flex-direction: column; gap: 10px; overflow: auto; }
.quote-card-dialog__header { display: flex; align-items: center; gap: 12px; }
.quote-card-dialog__header h3 { margin: 0; margin-right: auto; }
.quote-card-dialog__templates { display: inline-flex; gap: 6px; }
.quote-card-dialog__template--active { border-color: var(--accent-color); color: var(--accent-text); background: var(--accent-soft); }
.quote-card-dialog__stage { display: grid; place-items: center; min-height: 200px; border-radius: var(--radius); background: var(--thumb-bg); overflow: hidden; }
.quote-card-dialog__canvas { display: block; max-width: 100%; max-height: 56vh; width: auto; height: auto; }
.quote-card-dialog__field { display: grid; gap: 6px; font-size: 13px; color: var(--text-secondary); }
.quote-card-dialog__quote { line-height: 1.5; }
.quote-card-dialog__meta { display: flex; justify-content: space-between; gap: 12px; margin: 0; font-size: 12px; color: var(--text-muted); font-variant-numeric: tabular-nums; }
.quote-card-dialog__hint { margin: 0; font-size: 12px; color: var(--text-secondary); }
.quote-card-dialog__error { margin: 0; padding: 0 16px; text-align: center; color: var(--danger-color); }
.quote-card-dialog__status { margin: 0; font-size: 13px; overflow-wrap: anywhere; }
.quote-card-dialog__status--success { color: var(--text-secondary); }
.quote-card-dialog__status--error { color: var(--danger-color); }
</style>
