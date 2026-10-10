<template>
  <section class="edit-section" data-test="trim-editor">
    <h3>统一时间</h3>
    <div class="edit-inline">
      <span>移除</span>
      <EditTimeField :model-value="uniformStart" label="统一移除开始" :disabled="!editable" data-test="trim-uniform-start" @commit="uniformStart = $event" />
      <span>至</span>
      <EditTimeField :model-value="uniformEnd" label="统一移除结束" :disabled="!editable" data-test="trim-uniform-end" @commit="uniformEnd = $event" />
      <button type="button" class="btn-secondary btn-compact" :disabled="!editable || uniformEnd <= uniformStart" data-test="trim-apply-uniform" @click="applyUniform">应用到全部</button>
    </div>

    <h3>自动识别片头</h3>
    <div class="edit-inline">
      <label>分析每部开头
        <select v-model.number="windowMinutes" class="text-input edit-select" :disabled="!editable || analyzing" data-test="trim-window">
          <option v-for="minutes in windowOptions" :key="minutes" :value="minutes">{{ minutes }} 分钟</option>
        </select>
      </label>
      <button v-if="!analyzing" type="button" class="btn-secondary btn-compact" :disabled="!editable || items.length < 2" data-test="trim-analyze" @click="$emit('analyze', windowMinutes * 60000)">自动识别片头</button>
      <template v-else>
        <span class="edit-progress" role="progressbar" :aria-valuenow="percent" aria-valuemin="0" aria-valuemax="100" data-test="trim-analysis-progress"><i :style="{ width: `${percent}%` }"></i></span>
        <span class="help-text">正在识别 {{ percent }}%</span>
        <button type="button" class="btn-secondary btn-compact" data-test="trim-cancel-analysis" @click="$emit('cancel-analysis')">取消识别</button>
      </template>
      <span v-if="items.length < 2" class="help-text">至少两项才能比对重复片头。</span>
    </div>
    <p v-if="analysisText" class="help-text" data-test="trim-analysis-result">{{ analysisText }}</p>

    <div class="edit-inline edit-inline--spread">
      <h3>逐项区间（已确认 {{ confirmedCount }}/{{ items.length }}）</h3>
      <button type="button" class="btn-secondary btn-compact" :disabled="!editable" data-test="trim-confirm-all" @click="confirmAll">全部确认</button>
    </div>
    <ol class="edit-list">
      <li v-for="(item, index) in items" :key="item.video_id" :class="['edit-list__item', { 'edit-list__item--flagged': flag(item) }]" :data-test="`trim-item-${item.video_id}`">
        <div class="edit-list__row">
          <span class="edit-list__index">{{ index + 1 }}</span>
          <span class="edit-list__name" :title="videoName(item.video_id)">{{ videoName(item.video_id) }}</span>
          <span class="edit-badge">{{ originLabel(item) }}</span>
          <span class="help-text">{{ durationText(item.video_id) }}</span>
          <span class="edit-list__spacer"></span>
          <button type="button" class="row-btn" :disabled="!editable || items.length <= 1" :data-test="`trim-remove-${item.video_id}`" @click="$emit('change', removeItem(index))">移出</button>
        </div>
        <p v-if="flag(item)" class="edit-flag" :data-test="`trim-flag-${item.video_id}`">{{ flag(item) }}</p>
        <div class="edit-inline">
          <span>移除</span>
          <EditTimeField :model-value="item.remove_start_ms" label="移除开始" :disabled="!editable" :data-test="`trim-start-${item.video_id}`" @commit="setRange(index, $event, item.remove_end_ms)" />
          <span>至</span>
          <EditTimeField :model-value="item.remove_end_ms" label="移除结束" :disabled="!editable" :data-test="`trim-end-${item.video_id}`" @commit="setRange(index, item.remove_start_ms, $event)" />
          <label class="edit-check">
            <input type="checkbox" :checked="item.confirmed" :disabled="!editable || !rangeValid(item)" :data-test="`trim-confirm-${item.video_id}`" @change="setConfirmed(index, $event.target.checked)" />
            确认
          </label>
        </div>
        <template v-if="rangeValid(item)">
          <EditFrameStrip v-if="item.remove_start_ms > 0" title="移除起点" :frames="startFrames(item)" />
          <EditFrameStrip title="移除终点（成品从这里接上）" :frames="endFrames(item, index)" :actual-ms="fastActual(item, index)" />
        </template>
      </li>
    </ol>
  </section>
</template>

<script>
import EditFrameStrip from './EditFrameStrip.vue';
import EditTimeField from './EditTimeField.vue';
import { workbenchVideoMixin } from './workbenchVideo.js';
import {
  applyUniformTrim, confirmAllTrim, fastCutActual, frameBefore, removeTrimItem, setTrimConfirmed, setTrimRange, sourceFrameRate,
  trimItemFlag, trimRangeValid
} from '../../utils/videoEdit.js';
import { notify } from '../../utils/feedback.js';

const ORIGIN_LABELS = { uniform: '统一时间', manual: '手动', detected: '自动识别' };

// 批量去片头编辑器（Q1）：统一区间「应用到全部」后逐项调整；≥2 项可自动识别重复片头（进度与取消由页面
// 接事件）；未识别/有歧义的项明确标出；逐项或全部确认。fastPreflight 只在快速模式且预检有效时给出。
export default {
  name: 'TrimIntroEditor',
  components: { EditFrameStrip, EditTimeField },
  mixins: [workbenchVideoMixin],
  props: {
    recipe: { type: Object, required: true },
    editable: { type: Boolean, default: false },
    preflight: { type: Object, default: null },
    fastPreflight: { type: Object, default: null },
    analysis: { type: Object, default: null },
    analyzing: { type: Boolean, default: false },
    analysisProgress: { type: Number, default: 0 }
  },
  emits: ['change', 'analyze', 'cancel-analysis'],
  data() {
    const windowMs = Number(this.recipe?.trim_intro?.analysis_window_ms || 0);
    return {
      uniformStart: 0,
      uniformEnd: 0,
      windowMinutes: windowMs > 0 ? Math.round(windowMs / 60000) : 10,
      windowOptions: Array.from({ length: 20 }, (_, index) => index + 1)
    };
  },
  computed: {
    items() {
      return this.recipe?.trim_intro?.items || [];
    },
    confirmedCount() {
      return this.items.filter(item => item.confirmed).length;
    },
    percent() {
      return Math.max(0, Math.min(100, Math.round(Number(this.analysisProgress || 0) * 100)));
    },
    analysisText() {
      const analysis = this.analysis;
      if (!analysis || analysis.kind !== 'trim_intro' || this.analyzing) return '';
      if (analysis.status === 'failed') return `上次识别失败：${analysis.error || '原因未知'}；已确认的项没有改动。`;
      if (analysis.status === 'cancelled') return '上次识别已取消，配方没有改动。';
      const intros = analysis.intros || [];
      const detected = intros.filter(intro => intro.status === 'detected').length;
      return `上次识别：${detected} 项识别出片头，${intros.length - detected} 项未识别或有歧义。识别结果需要逐项预览后确认。`;
    }
  },
  methods: {
    flag: trimItemFlag,
    rangeValid: trimRangeValid,
    originLabel(item) {
      return ORIGIN_LABELS[item.origin] || '手动';
    },
    applyUniform() {
      this.$emit('change', applyUniformTrim(this.recipe, this.uniformStart, this.uniformEnd));
    },
    setRange(index, start, end) {
      this.$emit('change', setTrimRange(this.recipe, index, start, end));
    },
    setConfirmed(index, confirmed) {
      this.$emit('change', setTrimConfirmed(this.recipe, index, confirmed));
    },
    removeItem(index) {
      return removeTrimItem(this.recipe, index);
    },
    confirmAll() {
      const { recipe, skipped } = confirmAllTrim(this.recipe);
      if (skipped > 0) notify(`有 ${skipped} 项的区间无效（未识别或未填写），没有确认；请手填或移出。`);
      this.$emit('change', recipe);
    },
    fps(item) {
      return sourceFrameRate(this.preflight, item.video_id);
    },
    startFrames(item) {
      return [
        { videoID: item.video_id, ms: frameBefore(item.remove_start_ms, this.fps(item)), label: '保留的末帧' },
        { videoID: item.video_id, ms: item.remove_start_ms, label: '移除的首帧' }
      ];
    },
    fastActual(item, index) {
      if (!this.fastPreflight) return null;
      return fastCutActual(this.fastPreflight, index + 1, item.video_id, item.remove_end_ms);
    },
    endFrames(item, index) {
      const actual = this.fastActual(item, index);
      return [
        { videoID: item.video_id, ms: frameBefore(item.remove_end_ms, this.fps(item)), label: '移除的末帧' },
        { videoID: item.video_id, ms: actual ?? item.remove_end_ms, label: actual === null ? '保留的首帧' : '保留的首帧（关键帧）' }
      ];
    }
  }
};
</script>
