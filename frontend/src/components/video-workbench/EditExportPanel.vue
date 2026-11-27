<template>
  <section class="edit-section edit-export" data-test="edit-export">
    <header class="edit-inline export-heading">
      <h3>导出</h3>
      <AppBadge :tone="preflightStatus.tone">{{ preflightStatus.label }}</AppBadge>
    </header>
    <p v-if="project.kind === 'crop'" class="help-text">按裁剪尺寸重新编码。{{ recipe.crop?.publish_policy === 'replace_full' ? '整片裁剪保留原文件名和容器并替换原片，选定时间段仍另存。' : '另存为新视频。' }}点击导出会自动检查参数、轨道和可用空间。</p>
    <div v-if="project.kind !== 'crop' || project.mode === 'fast'" class="export-modes" role="radiogroup" aria-label="导出模式">
      <label class="export-mode" :class="{ 'export-mode--selected': project.mode !== 'fast' }">
        <input type="radio" value="precise" :checked="project.mode !== 'fast'" :disabled="!editable" data-test="edit-mode-precise" @change="$emit('set-mode', 'precise')" />
        <span class="export-mode__copy"><strong>精确</strong><span class="help-text">重新编码，切点到帧</span></span>
      </label>
      <label class="export-mode" :class="{ 'export-mode--selected': project.mode === 'fast' }">
        <input type="radio" value="fast" :checked="project.mode === 'fast'" :disabled="!editable || fastUnavailable" data-test="edit-mode-fast" @change="$emit('set-mode', 'fast')" />
        <span class="export-mode__copy"><strong>快速</strong><span class="help-text">不重编码，按关键帧切</span></span>
      </label>
    </div>
    <p v-if="project.kind !== 'crop' && fastReasons.length" class="export-reasons" data-test="edit-fast-reasons"><AppIcon name="warning" />快速模式不可用：{{ fastReasons.join('；') }}</p>

    <div class="edit-inline export-preflight">
      <button type="button" class="btn btn--secondary btn--sm" :disabled="loading" data-test="edit-run-preflight" @click="$emit('run-preflight')">{{ loading ? '正在预检…' : (preflight ? '重新预检' : '预检') }}</button>
      <span v-if="preflight && !fresh" class="edit-flag" data-test="edit-preflight-stale">{{ project.kind === 'crop' ? '裁剪已修改，导出时会重新检查。' : '配方或模式改过了，预检结果已过期，请重新预检。' }}</span>
      <span v-if="error" class="edit-error" role="alert" data-test="edit-preflight-error">{{ error }}</span>
    </div>

    <template v-if="preflight">
      <div v-if="sourceErrors.length" class="export-blockers">
        <h4><AppIcon name="warning" />来源读取失败</h4>
        <ul class="edit-errors">
          <li v-for="source in sourceErrors" :key="source.video_id">《{{ source.name || source.video_id }}》：{{ source.error }}</li>
        </ul>
      </div>

      <details v-for="output in outputs" :key="output.seq" class="edit-output" :open="outputs.length <= 3" :data-test="`edit-output-${output.seq}`">
        <summary>{{ output.planned_name || `第 ${output.seq} 项` }} · {{ outputDuration(output.duration_ms) }}</summary>
        <div class="export-output__body">
          <p class="help-text">{{ specText(output) }}</p>
          <p :class="['help-text', { 'export-space--short': spaceShort(output) }]" data-test="edit-output-space">
            <AppBadge v-if="spaceShort(output)" tone="danger">空间不足</AppBadge>
            预计 {{ bytes(output.estimated_bytes) }}，输出目录可用 {{ bytes(output.free_bytes) }}<template v-if="output.sidecar">；旁挂字幕：{{ sidecarText(output.sidecar) }}</template>
          </p>
          <table class="edit-tracks">
            <thead><tr><th scope="col">输出轨</th><th scope="col">各来源对应</th></tr></thead>
            <tbody>
              <tr v-for="track in tracksOf(output)" :key="`${track.kind}-${track.output_index}`" :class="{ 'edit-tracks__dropped': track.dropped }">
                <td>
                  <span class="export-track__name">{{ track.kind === 'audio' ? '音轨' : '字幕' }} {{ track.output_index + 1 }} · {{ track.language || '未标语言' }}{{ track.title ? ` · ${track.title}` : '' }}</span>
                  <AppBadge v-if="track.dropped">不导出</AppBadge>
                </td>
                <td>
                  <span v-for="mapping in track.mappings" :key="mapping.video_id" :class="['edit-mapping', `edit-mapping--${mapping.status}`]">
                    {{ videoName(mapping.video_id) }}：{{ mappingText(mapping) }}
                  </span>
                </td>
              </tr>
              <tr v-if="!tracksOf(output).length"><td colspan="2" class="help-text">没有音轨或字幕轨。</td></tr>
            </tbody>
          </table>
        </div>
      </details>

      <div v-if="conflicts.length" class="edit-conflicts" data-test="edit-conflicts">
        <h4>需要你选择的轨道对应</h4>
        <label v-for="conflict in conflicts" :key="conflictKey(conflict)" class="edit-conflict" :data-test="`edit-conflict-${conflictKey(conflict)}`">
          <span>{{ conflict.kind === 'audio' ? '音轨' : '字幕轨' }} {{ conflict.output_index + 1 }} 在《{{ videoName(conflict.video_id) }}》上：{{ conflict.reason }}</span>
          <select class="select-input edit-select" :disabled="!editable" @change="chooseTrack(conflict, $event.target.value)">
            <option value="" selected disabled>请选择</option>
            <option v-for="stream in conflict.candidates" :key="stream.index" :value="`stream:${stream.index}`">{{ streamLabel(stream) }}</option>
            <option v-for="fill in fillsOf(conflict)" :key="fill" :value="fill">{{ fillLabel(fill) }}</option>
          </select>
        </label>
      </div>

      <div v-if="warnings.length" class="edit-warnings" data-test="edit-warnings">
        <h4><AppIcon name="warning" />导出前请逐条确认</h4>
        <label v-for="warning in warnings" :key="warning.key" :class="['edit-check', { 'edit-check--missing': missingKeys.includes(warning.key) }]">
          <input type="checkbox" :checked="acknowledged.includes(warning.key)" :data-test="`edit-ack-${warning.key}`" @change="toggleAck(warning.key, $event.target.checked)" />
          <span>{{ warning.message }}</span>
        </label>
      </div>

      <div v-if="preflight.errors && preflight.errors.length" class="export-blockers">
        <h4><AppIcon name="warning" />阻止导出的问题</h4>
        <ul class="edit-errors" data-test="edit-errors">
          <li v-for="issue in preflight.errors" :key="issue.key">{{ issue.message }}</li>
        </ul>
      </div>
    </template>

    <div class="edit-inline export-actions">
      <button type="button" class="btn btn--primary" :disabled="!canQueue" data-test="edit-queue" @click="$emit('queue', acknowledged.filter(key => warningKeys.includes(key)))">{{ busy ? '处理中…' : (requeue ? '继续未完成项' : (project.kind === 'crop' ? '导出' : '排队导出')) }}</button>
      <span v-if="queueHint" class="help-text" data-test="edit-queue-hint">{{ queueHint }}</span>
    </div>
  </section>
</template>

<script>
import AppBadge from '../ui/AppBadge.vue';
import AppIcon from '../ui/AppIcon.vue';
import { workbenchVideoMixin } from './workbenchVideo.js';
import { formatBytes } from '../../utils/mediaDetails.js';
import { TRACK_FILL_LABELS, formatEditTime, outputSpecText, preflightFresh, setTrackChoice, streamOptionLabel } from '../../utils/videoEdit.js';

const SIDECAR_TEXT = { copy: '复制长版 .srt', retime: '按新时间线重写 .srt', none: '无' };

// 导出面板：模式切换（快速不可用时给原因）、预检结果（规格、轨道映射、空间、警告与错误）、
// 轨道冲突的明确选择（Q9），以及「排队导出 / 继续未完成项」。警告必须逐条勾选才放行（后端同样校验）；
// 裁剪允许直接发起导出，复用后端排队前预检；其他类型仍要求有效预检。
export default {
  name: 'EditExportPanel',
  components: { AppBadge, AppIcon },
  mixins: [workbenchVideoMixin],
  props: {
    project: { type: Object, required: true },
    recipe: { type: Object, required: true },
    preflight: { type: Object, default: null },
    loading: { type: Boolean, default: false },
    error: { type: String, default: '' },
    editable: { type: Boolean, default: false },
    requeue: { type: Boolean, default: false },
    busy: { type: Boolean, default: false },
    missingKeys: { type: Array, default: () => [] }
  },
  emits: ['run-preflight', 'set-mode', 'change', 'queue'],
  data() {
    return { acknowledged: [...(this.project?.acknowledged_warnings || [])] };
  },
  computed: {
    fresh() {
      return preflightFresh(this.preflight, this.project);
    },
    preflightStatus() {
      if (this.error) return { tone: 'danger', label: '预检出错' };
      if (!this.preflight) return { tone: 'neutral', label: '未预检' };
      if (!this.fresh) return { tone: 'warning', label: '预检已过期' };
      if (!this.preflight.ready) return { tone: 'danger', label: '预检有错误' };
      return { tone: 'success', label: '预检就绪' };
    },
    fastReasons() {
      return this.preflight && !this.preflight.fast?.available ? (this.preflight.fast?.reasons || []) : [];
    },
    fastUnavailable() {
      return this.fastReasons.length > 0 && this.project.mode !== 'fast';
    },
    outputs() {
      return this.preflight?.outputs || [];
    },
    sourceErrors() {
      return (this.preflight?.sources || []).filter(source => source.error);
    },
    warnings() {
      return this.preflight?.warnings || [];
    },
    warningKeys() {
      return this.warnings.map(warning => warning.key);
    },
    // 同一（种类、输出轨、来源）的冲突在多个导出项里重复出现时只问一次：选择按输出轨与来源生效。
    conflicts() {
      const seen = new Set();
      return (this.preflight?.conflicts || []).filter(conflict => {
        const key = this.conflictKey(conflict);
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      });
    },
    allAcknowledged() {
      return this.warningKeys.every(key => this.acknowledged.includes(key));
    },
    canQueue() {
      if (this.project.kind === 'crop') return !this.busy && !this.loading && (!this.fresh || (this.preflight.ready && this.allAcknowledged));
      return Boolean(this.fresh && this.preflight.ready && this.allAcknowledged && !this.busy && !this.loading);
    },
    queueHint() {
      if (this.project.kind === 'crop' && !this.fresh) return '导出时自动检查，无需先点预检。';
      if (!this.preflight) return '先预检，确认规格、轨道与空间后再导出。';
      if (!this.fresh) return '';
      if (!this.preflight.ready) return '还有阻止导出的问题，见上方红字。';
      if (!this.allAcknowledged) return '还有警告没有确认。';
      return '';
    }
  },
  watch: {
    // 换了项目时从该项目已确认的警告重新开始；同一项目重新预检时保留已勾选的。
    'project.id'() {
      this.acknowledged = [...(this.project?.acknowledged_warnings || [])];
    }
  },
  methods: {
    outputDuration(ms) {
      return `成品时长 ${formatEditTime(ms)}`;
    },
    bytes: formatBytes,
    specText: outputSpecText,
    streamLabel: streamOptionLabel,
    spaceShort(output) {
      return Number(output.free_bytes) > 0 && Number(output.free_bytes) < Number(output.estimated_bytes) * 1.2;
    },
    sidecarText(mode) {
      return SIDECAR_TEXT[mode] || mode;
    },
    tracksOf(output) {
      return [
        ...(output.audio_tracks || []).map(track => ({ ...track, kind: 'audio' })),
        ...(output.subtitle_tracks || []).map(track => ({ ...track, kind: 'subtitle' }))
      ];
    },
    mappingText(mapping) {
      if (mapping.status === 'conflict') return '待选择';
      if (mapping.fill) return TRACK_FILL_LABELS[mapping.fill] || mapping.fill;
      return `流 #${mapping.stream_index}${mapping.explicit ? '（已选）' : ''}`;
    },
    conflictKey(conflict) {
      return `${conflict.kind}-${conflict.output_index}-${conflict.video_id}`;
    },
    fillsOf(conflict) {
      return (conflict.allowed_fills || []).filter(fill => fill !== 'stream');
    },
    fillLabel(fill) {
      return TRACK_FILL_LABELS[fill] || fill;
    },
    chooseTrack(conflict, value) {
      if (!value) return;
      const [choice, index] = value.startsWith('stream:') ? ['stream', Number(value.slice(7))] : [value, 0];
      this.$emit('change', setTrackChoice(this.recipe, conflict.kind, conflict.output_index, Number(conflict.video_id), choice, index, Number(conflict.scope_video_id || 0)));
    },
    toggleAck(key, checked) {
      const rest = this.acknowledged.filter(existing => existing !== key);
      this.acknowledged = checked ? [...rest, key] : rest;
    }
  }
};
</script>

<style scoped>
.edit-export { min-width: 0; grid-template-columns: minmax(0, 1fr); gap: var(--space-3); }
.export-heading h3 { margin: 0; font-size: var(--fs-section); }
.export-modes { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--space-2); }
.export-mode {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-subtle-bg);
  font-size: var(--fs-secondary);
}
.export-mode--selected { border-color: var(--accent-color); background: var(--accent-soft); }
.export-mode:has(input:enabled) { cursor: pointer; }
.export-mode input, .edit-warnings input {
  flex: none;
  margin-top: var(--space-1);
  accent-color: var(--accent-color);
}
.export-mode__copy { display: grid; gap: var(--space-1); min-width: 0; }
.export-reasons, .export-space--short {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--warning-border);
  border-radius: var(--radius-sm);
  background: var(--warning-soft);
  color: var(--warning-text);
  font-size: var(--fs-secondary);
}
.export-reasons { display: flex; align-items: flex-start; gap: var(--space-2); }
.export-preflight { gap: var(--space-1) var(--space-3); }
.edit-output {
  min-width: 0;
  padding: 0;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-subtle-bg);
}
.edit-output summary { padding: var(--space-2) var(--space-3); overflow-wrap: anywhere; }
.edit-output[open] summary { border-bottom: 1px solid var(--hairline); }
.export-output__body { display: grid; gap: var(--space-2); padding: var(--space-3); }
.export-space--short { border-color: var(--danger-border); background: var(--danger-soft); color: var(--danger-text); }
.export-space--short .ui-badge { margin-inline-end: var(--space-1); }
.edit-tracks { overflow-wrap: anywhere; }
.edit-tracks th, .edit-tracks td { padding: var(--space-2); }
.edit-tracks thead { background: var(--panel-muted-bg); color: var(--text-secondary); }
.edit-tracks__dropped { background: var(--panel-bg); color: var(--text-secondary); opacity: 1; text-decoration: none; }
.edit-tracks__dropped .export-track__name, .edit-tracks__dropped .edit-mapping { text-decoration: line-through; }
.export-track__name + .ui-badge { margin-inline-start: var(--space-1); }
.edit-conflicts, .edit-warnings, .export-blockers {
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-subtle-bg);
}
.edit-conflict { justify-content: space-between; overflow-wrap: anywhere; }
.edit-conflict + .edit-conflict { border-top: 1px solid var(--hairline); padding-top: var(--space-2); }
.edit-conflict .edit-select { max-width: 100%; }
.edit-warnings { border-color: var(--warning-border); border-inline-start: 3px solid var(--warning-color); background: var(--warning-soft); }
.edit-warnings h4, .export-blockers h4 { display: flex; align-items: center; gap: var(--space-2); margin: 0; }
.edit-warnings h4 { color: var(--warning-text); }
.edit-warnings .edit-check { align-items: flex-start; overflow-wrap: anywhere; }
.edit-warnings .edit-check--missing { padding: var(--space-1) var(--space-2); border-radius: var(--radius-sm); background: var(--danger-soft); color: var(--danger-text); }
.export-blockers { border-color: var(--danger-border); background: var(--danger-soft); color: var(--danger-text); overflow-wrap: anywhere; }
.export-blockers .edit-errors { margin-bottom: 0; padding-inline-start: var(--space-4); }
.export-actions {
  gap: var(--space-2) var(--space-3);
  padding: var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-subtle-bg);
}
</style>
