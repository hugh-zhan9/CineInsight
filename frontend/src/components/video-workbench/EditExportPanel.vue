<template>
  <section class="edit-section edit-export" data-test="edit-export">
    <h3>导出</h3>
    <div class="edit-inline" role="radiogroup" aria-label="导出模式">
      <label class="edit-radio"><input type="radio" value="precise" :checked="project.mode !== 'fast'" :disabled="!editable" data-test="edit-mode-precise" @change="$emit('set-mode', 'precise')" /> 精确（重新编码，切点到帧）</label>
      <label class="edit-radio"><input type="radio" value="fast" :checked="project.mode === 'fast'" :disabled="!editable || fastUnavailable" data-test="edit-mode-fast" @change="$emit('set-mode', 'fast')" /> 快速（不重编码，按关键帧切）</label>
    </div>
    <p v-if="fastReasons.length" class="help-text" data-test="edit-fast-reasons">快速模式不可用：{{ fastReasons.join('；') }}</p>

    <div class="edit-inline">
      <button type="button" class="btn-secondary btn-compact" :disabled="loading" data-test="edit-run-preflight" @click="$emit('run-preflight')">{{ loading ? '正在预检…' : (preflight ? '重新预检' : '预检') }}</button>
      <span v-if="preflight && !fresh" class="edit-flag" data-test="edit-preflight-stale">配方或模式改过了，预检结果已过期，请重新预检。</span>
      <span v-if="error" class="edit-error" role="alert" data-test="edit-preflight-error">{{ error }}</span>
    </div>

    <template v-if="preflight">
      <ul v-if="sourceErrors.length" class="edit-errors">
        <li v-for="source in sourceErrors" :key="source.video_id">《{{ source.name || source.video_id }}》：{{ source.error }}</li>
      </ul>

      <details v-for="output in outputs" :key="output.seq" class="edit-output" :open="outputs.length <= 3" :data-test="`edit-output-${output.seq}`">
        <summary>{{ output.planned_name || `第 ${output.seq} 项` }} · {{ outputDuration(output.duration_ms) }}</summary>
        <p class="help-text">{{ specText(output) }}</p>
        <p :class="['help-text', { 'edit-error': spaceShort(output) }]" data-test="edit-output-space">
          预计 {{ bytes(output.estimated_bytes) }}，输出目录可用 {{ bytes(output.free_bytes) }}<template v-if="output.sidecar">；旁挂字幕：{{ sidecarText(output.sidecar) }}</template>
        </p>
        <table class="edit-tracks">
          <thead><tr><th>输出轨</th><th>各来源对应</th></tr></thead>
          <tbody>
            <tr v-for="track in tracksOf(output)" :key="`${track.kind}-${track.output_index}`" :class="{ 'edit-tracks__dropped': track.dropped }">
              <td>{{ track.kind === 'audio' ? '音轨' : '字幕' }} {{ track.output_index + 1 }} · {{ track.language || '未标语言' }}{{ track.title ? ` · ${track.title}` : '' }}{{ track.dropped ? '（不导出）' : '' }}</td>
              <td>
                <span v-for="mapping in track.mappings" :key="mapping.video_id" :class="['edit-mapping', `edit-mapping--${mapping.status}`]">
                  {{ videoName(mapping.video_id) }}：{{ mappingText(mapping) }}
                </span>
              </td>
            </tr>
            <tr v-if="!tracksOf(output).length"><td colspan="2" class="help-text">没有音轨或字幕轨。</td></tr>
          </tbody>
        </table>
      </details>

      <div v-if="conflicts.length" class="edit-conflicts" data-test="edit-conflicts">
        <h4>需要你选择的轨道对应</h4>
        <label v-for="conflict in conflicts" :key="conflictKey(conflict)" class="edit-conflict" :data-test="`edit-conflict-${conflictKey(conflict)}`">
          <span>{{ conflict.kind === 'audio' ? '音轨' : '字幕轨' }} {{ conflict.output_index + 1 }} 在《{{ videoName(conflict.video_id) }}》上：{{ conflict.reason }}</span>
          <select class="text-input edit-select" :disabled="!editable" @change="chooseTrack(conflict, $event.target.value)">
            <option value="" selected disabled>请选择</option>
            <option v-for="stream in conflict.candidates" :key="stream.index" :value="`stream:${stream.index}`">{{ streamLabel(stream) }}</option>
            <option v-for="fill in fillsOf(conflict)" :key="fill" :value="fill">{{ fillLabel(fill) }}</option>
          </select>
        </label>
      </div>

      <div v-if="warnings.length" class="edit-warnings" data-test="edit-warnings">
        <h4>导出前请逐条确认</h4>
        <label v-for="warning in warnings" :key="warning.key" :class="['edit-check', { 'edit-check--missing': missingKeys.includes(warning.key) }]">
          <input type="checkbox" :checked="acknowledged.includes(warning.key)" :data-test="`edit-ack-${warning.key}`" @change="toggleAck(warning.key, $event.target.checked)" />
          {{ warning.message }}
        </label>
      </div>

      <ul v-if="preflight.errors && preflight.errors.length" class="edit-errors" data-test="edit-errors">
        <li v-for="issue in preflight.errors" :key="issue.key">{{ issue.message }}</li>
      </ul>
    </template>

    <div class="edit-inline">
      <button type="button" class="btn-primary" :disabled="!canQueue" data-test="edit-queue" @click="$emit('queue', acknowledged.filter(key => warningKeys.includes(key)))">{{ requeue ? '继续未完成项' : '排队导出' }}</button>
      <span v-if="queueHint" class="help-text" data-test="edit-queue-hint">{{ queueHint }}</span>
    </div>
  </section>
</template>

<script>
import { workbenchVideoMixin } from './workbenchVideo.js';
import { formatBytes } from '../../utils/mediaDetails.js';
import { TRACK_FILL_LABELS, formatEditTime, outputSpecText, preflightFresh, setTrackChoice, streamOptionLabel } from '../../utils/videoEdit.js';

const SIDECAR_TEXT = { copy: '复制长版 .srt', retime: '按新时间线重写 .srt', none: '无' };

// 导出面板：模式切换（快速不可用时给原因）、预检结果（规格、轨道映射、空间、警告与错误）、
// 轨道冲突的明确选择（Q9），以及「排队导出 / 继续未完成项」。警告必须逐条勾选才放行（后端同样校验）；
// 预检过期（配方或模式改过）时不放行。
export default {
  name: 'EditExportPanel',
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
      return Boolean(this.fresh && this.preflight.ready && this.allAcknowledged && !this.busy && !this.loading);
    },
    queueHint() {
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
      this.$emit('change', setTrackChoice(this.recipe, conflict.kind, conflict.output_index, Number(conflict.video_id), choice, index));
    },
    toggleAck(key, checked) {
      const rest = this.acknowledged.filter(existing => existing !== key);
      this.acknowledged = checked ? [...rest, key] : rest;
    }
  }
};
</script>
