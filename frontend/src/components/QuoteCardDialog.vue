<template>
  <Teleport to="body">
    <DialogShell title="台词卡" size="xl" data-test="quote-card-dialog" @close="$emit('close')">
      <div class="quote-card-dialog__content">
        <!-- 版式（D-UD-06 SegmentedControl），每项带 quote-card-template-* 钩子。 -->
        <SegmentedControl v-model="template" class="quote-card-dialog__templates" label="版式" :options="templateOptions" />

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
      </div>

      <template #footer>
        <button type="button" class="btn btn--secondary" data-test="quote-card-close" @click="$emit('close')">关闭</button>
        <button type="button" class="btn btn--secondary" data-test="quote-card-save" :disabled="!canExport" @click="saveImage">{{ busy === 'save' ? '保存中…' : '保存为 PNG' }}</button>
        <button type="button" class="btn btn--primary" data-test="quote-card-copy" :disabled="!canExport" @click="copyImage">{{ busy === 'copy' ? '复制中…' : '复制图片' }}</button>
      </template>
    </DialogShell>
  </Teleport>
</template>

<script>
import { markRaw } from 'vue';
import DialogShell from './ui/DialogShell.vue';
import SegmentedControl from './ui/SegmentedControl.vue';
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
  components: { DialogShell, SegmentedControl },
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
    templateOptions() {
      return this.templates.map(option => ({ value: option.value, label: option.label, dataTest: `quote-card-template-${option.value}` }));
    },
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
/* 尺寸由 DialogShell 的 xl（940）给出；正文纵向排开，DialogShell 的正文区负责滚动。 */
.quote-card-dialog__content { display: flex; flex-direction: column; gap: 10px; }
/* 外观照 SegmentedControl（选项要带各自的 data-test 钩子，组件还不能透传，先保留这组按钮的标记，只换外观；
   SegmentedControl 支持逐项钩子后换成组件，删掉这几条）。 */
.quote-card-dialog__templates { align-self: flex-start; }
.quote-card-dialog__stage { display: grid; place-items: center; min-height: 200px; border-radius: var(--radius); background: var(--thumb-bg); overflow: hidden; }
.quote-card-dialog__canvas { display: block; max-width: 100%; max-height: 56vh; width: auto; height: auto; }
.quote-card-dialog__field { display: grid; gap: 6px; font-size: var(--fs-secondary); color: var(--text-secondary); }
.quote-card-dialog__quote { line-height: 1.5; }
.quote-card-dialog__meta { display: flex; justify-content: space-between; gap: 12px; margin: 0; font-size: var(--fs-caption); color: var(--text-muted); font-variant-numeric: tabular-nums; }
.quote-card-dialog__hint { margin: 0; font-size: var(--fs-caption); color: var(--text-secondary); }
.quote-card-dialog__error { margin: 0; padding: 0 16px; text-align: center; color: var(--danger-text); }
.quote-card-dialog__status { margin: 0; font-size: var(--fs-secondary); overflow-wrap: anywhere; }
.quote-card-dialog__status--success { color: var(--text-secondary); }
.quote-card-dialog__status--error { color: var(--danger-text); }
</style>
