<template>
  <section class="edit-section" data-test="hd-editor">
    <h3>来源</h3>
    <dl class="edit-sources">
      <dt>长版（主时间线、音轨与字幕默认来源）</dt>
      <dd data-test="hd-long">{{ videoName(hd.long_video_id) }} <span class="help-text">{{ durationText(hd.long_video_id) }}</span></dd>
      <dt>高清版</dt>
      <dd data-test="hd-high">{{ videoName(hd.hd_video_id) }} <span class="help-text">{{ durationText(hd.hd_video_id) }}</span></dd>
    </dl>
    <button type="button" class="btn-secondary btn-compact" :disabled="!editable" data-test="hd-swap" @click="swapSources">互换长版与高清版</button>

    <h3>自动对齐</h3>
    <div class="edit-inline">
      <button v-if="!analyzing" type="button" class="btn-secondary btn-compact" :disabled="!editable" data-test="hd-analyze" @click="$emit('analyze', 0)">自动对齐</button>
      <template v-else>
        <span class="edit-progress" role="progressbar" :aria-valuenow="percent" aria-valuemin="0" aria-valuemax="100" data-test="hd-analysis-progress"><i :style="{ width: `${percent}%` }"></i></span>
        <span class="help-text">正在对齐 {{ percent }}%</span>
        <button type="button" class="btn-secondary btn-compact" data-test="hd-cancel-analysis" @click="$emit('cancel-analysis')">取消对齐</button>
      </template>
      <span class="help-text">已确认的段保留；变速、镜像、裁剪的画面不做匹配。</span>
    </div>
    <p v-if="analysisText" class="help-text" data-test="hd-analysis-result">{{ analysisText }}</p>

    <div class="edit-inline edit-inline--spread">
      <h3>替换段（已确认 {{ confirmedCount }}/{{ segments.length }}）</h3>
      <button type="button" class="btn-secondary btn-compact" :disabled="!editable || !canAdd" data-test="hd-add-segment" @click="$emit('change', addSegment())">添加替换段</button>
    </div>
    <p v-if="!segments.length" class="help-text" data-test="hd-empty">还没有替换段：先「自动对齐」，或手动添加。</p>
    <ol class="edit-list">
      <li v-for="(segment, index) in segments" :key="index" :class="['edit-list__item', { 'edit-list__item--flagged': segment.status === 'conflict' }]" :data-test="`hd-segment-${index}`">
        <div class="edit-list__row">
          <span class="edit-list__index">{{ index + 1 }}</span>
          <span>长版 {{ time(segment.long_start_ms) }} – {{ time(segment.long_end_ms) }} ↔ 高清 {{ time(segment.hd_start_ms) }} – {{ time(segment.hd_end_ms) }}</span>
          <span class="edit-badge">{{ segment.origin === 'detected' ? `自动对齐 ${Math.round(segment.match_rate * 100)}%` : '手动' }}</span>
          <span class="edit-list__spacer"></span>
          <button type="button" class="row-btn" :disabled="!editable" :data-test="`hd-remove-${index}`" @click="$emit('change', removeAt(index))">删除</button>
        </div>
        <p v-if="segment.status === 'conflict'" class="edit-flag">与另一段在长版上冲突（匹配率较低的一段），请调整或删除。</p>
        <div v-for="edge in edges" :key="edge.key" class="edit-inline">
          <span class="edit-inline__label">{{ edge.label }}</span>
          <input type="number" min="0" step="1" class="text-input edit-ms" :value="segment[`long_${edge.key}_ms`]" :disabled="!editable" :aria-label="`${edge.label}（毫秒）`" :data-test="`hd-${edge.key}-ms-${index}`" @change="setEdge(index, edge.key, $event.target.value)" />
          <span class="help-text">毫秒</span>
          <button v-for="step in steps" :key="step.label" type="button" class="row-btn" :disabled="!editable || stepDelta(segment[`long_${edge.key}_ms`], step, longFps) === null" :data-test="`hd-${edge.key}-${step.key}-${index}`" @click="shiftEdge(index, edge.key, segment[`long_${edge.key}_ms`], step)">{{ step.label }}</button>
        </div>
        <div class="edit-inline">
          <span class="edit-inline__label">高清对应起点</span>
          <input type="number" min="0" step="1" class="text-input edit-ms" :value="segment.hd_start_ms" :disabled="!editable" aria-label="高清对应起点（毫秒）" :data-test="`hd-hdstart-ms-${index}`" @change="setHDStart(index, $event.target.value)" />
          <span class="help-text">毫秒</span>
          <button v-for="step in steps" :key="step.label" type="button" class="row-btn" :disabled="!editable || stepDelta(segment.hd_start_ms, step, hdFps) === null" :data-test="`hd-hdstart-${step.key}-${index}`" @click="shiftHD(index, segment.hd_start_ms, step)">{{ step.label }}</button>
        </div>
        <EditFrameStrip title="段起点：长版接到高清" :frames="startFrames(segment)" :actual-ms="fastActual(hd.hd_video_id, segment.hd_start_ms)" />
        <EditFrameStrip title="段终点：高清接回长版" :frames="endFrames(segment)" :actual-ms="fastActual(hd.long_video_id, segment.long_end_ms)" />
        <div class="edit-inline">
          <label>音频来源
            <select class="text-input edit-select" :value="segment.audio_source || 'long'" :disabled="!editable" :data-test="`hd-audio-${index}`" @change="$emit('change', setAudio(index, $event.target.value))">
              <option value="long">长版音轨</option>
              <option value="hd">高清版音轨</option>
            </select>
          </label>
          <label class="edit-check">
            <input type="checkbox" :checked="segment.confirmed" :disabled="!editable" :data-test="`hd-confirm-${index}`" @change="$emit('change', setConfirmed(index, $event.target.checked))" />
            确认这一段
          </label>
        </div>
      </li>
    </ol>

    <h3>保留长版的区间</h3>
    <ul class="edit-unmatched" data-test="hd-unmatched">
      <li v-for="range in unmatched" :key="range.start_ms">保留长版：{{ time(range.start_ms) }} – {{ time(range.end_ms) }}</li>
      <li v-if="!unmatched.length" class="help-text">长版全部被替换段覆盖。</li>
    </ul>
    <p class="help-text">字幕始终沿长版时间线。</p>
  </section>
</template>

<script>
import EditFrameStrip from './EditFrameStrip.vue';
import { workbenchVideoMixin } from './workbenchVideo.js';
import {
  addManualSegment, cloneRecipe, fastCutActual, formatEditTime, frameBefore, removeSegment, setSegmentAudio, setSegmentConfirmed,
  setSegmentEdge, setSegmentHDStart, shiftSegmentEdge, shiftSegmentHD, sourceFrameRate, stepByFrames, unmatchedLongRanges
} from '../../utils/videoEdit.js';
import { confirmAction } from '../../utils/feedback.js';

const STEPS = [
  { key: 'minus-second', label: '−1秒', ms: -1000 },
  { key: 'minus-frame', label: '−1帧', frames: -1 },
  { key: 'plus-frame', label: '+1帧', frames: 1 },
  { key: 'plus-second', label: '+1秒', ms: 1000 }
];

// 高清替换编辑器（Q2/Q3/Q7/Q9）：自动对齐后逐段预览（长版与高清首尾帧并排）、按帧/秒/毫秒微调
// （长版与高清两侧时长始终相等）、选音频来源并确认；未被覆盖的长版区间明确显示为「保留长版」。
export default {
  name: 'HDReplaceEditor',
  components: { EditFrameStrip },
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
    return {
      steps: STEPS,
      edges: [{ key: 'start', label: '长版起点' }, { key: 'end', label: '长版终点' }]
    };
  },
  computed: {
    hd() {
      return this.recipe?.hd_replace || { segments: [] };
    },
    segments() {
      return this.hd.segments || [];
    },
    confirmedCount() {
      return this.segments.filter(segment => segment.confirmed).length;
    },
    longFps() {
      return sourceFrameRate(this.preflight, this.hd.long_video_id);
    },
    hdFps() {
      return sourceFrameRate(this.preflight, this.hd.hd_video_id);
    },
    longDuration() {
      return this.durationMs(this.hd.long_video_id);
    },
    unmatched() {
      return unmatchedLongRanges(this.segments, this.longDuration);
    },
    canAdd() {
      return this.longDuration > 0 && this.unmatched.length > 0;
    },
    percent() {
      return Math.max(0, Math.min(100, Math.round(Number(this.analysisProgress || 0) * 100)));
    },
    analysisText() {
      const analysis = this.analysis;
      if (!analysis || analysis.kind !== 'hd_replace' || this.analyzing) return '';
      if (analysis.status === 'failed') return `上次对齐失败：${analysis.error || '原因未知'}；已确认的段没有改动。`;
      if (analysis.status === 'cancelled') return '上次对齐已取消，配方没有改动。';
      const found = analysis.segments || [];
      const conflicts = found.filter(segment => segment.status === 'conflict').length;
      return `上次对齐找到 ${found.length} 段${conflicts ? `（其中 ${conflicts} 段冲突）` : ''}。请逐段预览、调整后确认。`;
    }
  },
  methods: {
    time: formatEditTime,
    // 一步的毫秒增量；按帧但帧率未知时返回 null（按钮禁用，不编默认帧率）。
    stepDelta(current, step, fps) {
      if (step.ms) return step.ms;
      const next = stepByFrames(current, step.frames, fps);
      return next === null ? null : next - current;
    },
    shiftEdge(index, edge, current, step) {
      const delta = this.stepDelta(current, step, this.longFps);
      if (delta !== null) this.$emit('change', shiftSegmentEdge(this.recipe, index, edge, delta));
    },
    shiftHD(index, current, step) {
      const delta = this.stepDelta(current, step, this.hdFps);
      if (delta !== null) this.$emit('change', shiftSegmentHD(this.recipe, index, delta));
    },
    setEdge(index, edge, value) {
      const ms = Math.round(Number(value));
      if (Number.isFinite(ms) && ms >= 0) this.$emit('change', setSegmentEdge(this.recipe, index, edge, ms));
    },
    setHDStart(index, value) {
      const ms = Math.round(Number(value));
      if (Number.isFinite(ms) && ms >= 0) this.$emit('change', setSegmentHDStart(this.recipe, index, ms));
    },
    setAudio(index, source) {
      return setSegmentAudio(this.recipe, index, source);
    },
    setConfirmed(index, confirmed) {
      return setSegmentConfirmed(this.recipe, index, confirmed);
    },
    removeAt(index) {
      return removeSegment(this.recipe, index);
    },
    addSegment() {
      return addManualSegment(this.recipe, this.longDuration);
    },
    async swapSources() {
      if (this.segments.length) {
        const ok = await confirmAction({
          title: '互换长版与高清版',
          message: '互换后现有替换段的时间不再对应，会全部清空，需要重新对齐。继续吗？',
          confirmText: '互换并清空'
        });
        if (!ok) return;
      }
      const next = cloneRecipe(this.recipe);
      const { long_video_id: long, hd_video_id: high } = next.hd_replace;
      Object.assign(next.hd_replace, { long_video_id: high, hd_video_id: long, segments: [] });
      next.tracks = { audio: [], subtitle: [] };
      this.$emit('change', next);
    },
    fastActual(videoID, requestedMs) {
      if (!this.fastPreflight) return null;
      return fastCutActual(this.fastPreflight, 1, videoID, requestedMs);
    },
    // 段起点：长版切出前一帧、长版起点、高清起点（快速模式取实际关键帧）并排。
    startFrames(segment) {
      const actual = this.fastActual(this.hd.hd_video_id, segment.hd_start_ms);
      return [
        { videoID: this.hd.long_video_id, ms: frameBefore(segment.long_start_ms, this.longFps), label: '切点前·长版' },
        { videoID: this.hd.long_video_id, ms: segment.long_start_ms, label: '长版起点' },
        { videoID: this.hd.hd_video_id, ms: actual ?? segment.hd_start_ms, label: actual === null ? '高清起点' : '高清起点（关键帧）' }
      ];
    },
    // 段终点：长版与高清的末帧并排，再接回长版的首帧。
    endFrames(segment) {
      const actual = this.fastActual(this.hd.long_video_id, segment.long_end_ms);
      return [
        { videoID: this.hd.long_video_id, ms: frameBefore(segment.long_end_ms, this.longFps), label: '长版终点' },
        { videoID: this.hd.hd_video_id, ms: frameBefore(segment.hd_end_ms, this.hdFps), label: '高清终点' },
        { videoID: this.hd.long_video_id, ms: actual ?? segment.long_end_ms, label: actual === null ? '切点后·长版' : '切点后·长版（关键帧）' }
      ];
    }
  }
};
</script>
