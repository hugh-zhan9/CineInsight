<template>
  <section class="edit-section" data-test="merge-editor">
    <header class="merge-heading">
      <h3>来源顺序</h3>
      <AppBadge>{{ sources.length }} 段 · {{ totalDuration }}</AppBadge>
    </header>
    <ol class="edit-list">
      <li v-for="(source, index) in sources" :key="source.video_id" :data-test="`merge-source-${source.video_id}`">
        <div class="merge-source">
          <AppBadge class="merge-index" tone="accent">{{ index + 1 }}</AppBadge>
          <div class="merge-source__info">
            <span class="edit-list__name" :title="videoName(source.video_id)">{{ videoName(source.video_id) }}</span>
            <div class="merge-source__facts">
              <AppBadge v-for="fact in sourceFacts(source.video_id)" :key="fact">{{ fact }}</AppBadge>
            </div>
          </div>
          <div class="merge-source__actions" role="group" aria-label="来源操作">
            <button type="button" class="btn-icon btn--sm" aria-label="上移" title="上移" :disabled="!editable || index === 0" :data-test="`merge-up-${source.video_id}`" @click="move(index, -1)"><AppIcon name="chevron-up" :size="16" /></button>
            <button type="button" class="btn-icon btn--sm" aria-label="下移" title="下移" :disabled="!editable || index === sources.length - 1" @click="move(index, 1)"><AppIcon name="chevron-down" :size="16" /></button>
            <button type="button" class="btn btn--secondary btn--sm" :disabled="!editable || sources.length <= 2" :data-test="`merge-remove-${source.video_id}`" @click="remove(index)">移出</button>
          </div>
        </div>
        <div v-if="index < sources.length - 1" class="merge-seam">
          <EditFrameStrip
            :title="`第 ${index + 1} 段与第 ${index + 2} 段的接缝`"
            :frames="seamFrames(index)"
          />
        </div>
      </li>
    </ol>

    <template v-if="specOptions.length">
      <h3>输出规格</h3>
      <p class="help-text">来源的分辨率、帧率或动态范围不同，必须选定按哪个来源的规格导出；其他来源会缩放并居中加黑边，不裁切。</p>
      <div class="merge-specs" role="radiogroup" aria-label="输出规格">
        <label v-for="option in specOptions" :key="option.video_id" class="merge-spec" :class="{ 'merge-spec--selected': specSelected(option) }" :data-test="`merge-spec-${option.video_id}`">
          <input type="radio" name="merge-spec" :value="option.video_id" :checked="specSelected(option)" :disabled="!editable" @change="chooseSpec(option.video_id)" />
          <span class="merge-spec__info">
            <strong class="edit-list__name" :title="videoName(option.video_id)">{{ videoName(option.video_id) }}</strong>
            <span class="help-text">{{ option.width }}×{{ option.height }} · {{ fpsText(option.frame_rate) }}{{ option.hdr ? ' · HDR' : '' }}</span>
          </span>
        </label>
      </div>
    </template>
  </section>
</template>

<script>
import AppBadge from '../ui/AppBadge.vue';
import AppIcon from '../ui/AppIcon.vue';
import EditFrameStrip from './EditFrameStrip.vue';
import { formatEditTime, frameBefore, moveMergeSource, parseFrameRate, preflightSource, removeMergeSource, setMergeSpec, sourceFrameRate } from '../../utils/videoEdit.js';
import { workbenchVideoMixin } from './workbenchVideo.js';

// 顺序合并编辑器：来源排序/移出、接缝帧、规格选择（只在预检报规格不同时出现）。改动以 change 交给页面保存。
export default {
  name: 'MergeEditor',
  components: { AppBadge, AppIcon, EditFrameStrip },
  mixins: [workbenchVideoMixin],
  // 页面用同一组属性挂三种编辑器；合并不需要分析相关的那几个，不让它们落到根元素上。
  inheritAttrs: false,
  props: {
    recipe: { type: Object, required: true },
    editable: { type: Boolean, default: false },
    preflight: { type: Object, default: null }
  },
  emits: ['change'],
  computed: {
    sources() {
      return this.recipe?.merge?.sources || [];
    },
    totalDuration() {
      const durations = this.sources.map(source => this.durationMs(source.video_id));
      return durations.some(ms => ms <= 0) ? '总时长未知' : `总时长 ${formatEditTime(durations.reduce((total, ms) => total + ms, 0))}`;
    },
    specOptions() {
      return this.preflight?.spec_options || [];
    }
  },
  methods: {
    move(index, delta) {
      this.$emit('change', moveMergeSource(this.recipe, index, delta));
    },
    remove(index) {
      this.$emit('change', removeMergeSource(this.recipe, index));
    },
    chooseSpec(videoID) {
      this.$emit('change', setMergeSpec(this.recipe, videoID));
    },
    specSelected(option) {
      return Number(this.recipe.merge.spec_source_video_id) === Number(option.video_id);
    },
    fpsText(rate) {
      const fps = parseFrameRate(rate);
      return fps > 0 ? `${Math.round(fps * 1000) / 1000} fps` : '帧率未知';
    },
    // 接缝：上一段的末帧与下一段的首帧。
    seamFrames(index) {
      const previous = this.sources[index].video_id;
      const next = this.sources[index + 1].video_id;
      const end = this.durationMs(previous);
      return [
        { videoID: previous, ms: frameBefore(end, sourceFrameRate(this.preflight, previous)), label: '上一段末帧' },
        { videoID: next, ms: 0, label: '下一段首帧' }
      ];
    },
    sourceFacts(videoID) {
      const source = preflightSource(this.preflight, videoID);
      const parts = [this.durationText(videoID)];
      if (source?.video?.width) parts.push(`${source.video.width}×${source.video.height}`);
      return parts.filter(Boolean);
    }
  }
};
</script>

<style scoped>
.merge-heading, .merge-source__facts, .merge-source__actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-2);
}
.merge-heading h3 { margin: 0; font-size: var(--fs-section); }
.merge-source {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-subtle-bg);
}
.merge-index {
  justify-content: center;
  min-width: var(--h-compact);
  height: var(--h-compact);
  font-family: var(--font-mono);
}
.merge-source__info, .merge-spec__info { display: grid; min-width: 0; gap: var(--space-1); }
.merge-source__facts, .merge-source__actions { gap: var(--space-1); }
.merge-seam {
  margin-inline-start: var(--space-5);
  padding: var(--space-2) 0 0 var(--space-3);
  border-inline-start: 1px solid var(--accent-border);
}
.merge-specs {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 230px), 1fr));
  gap: var(--space-2);
}
.merge-spec {
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-subtle-bg);
  font-size: var(--fs-secondary);
}
.merge-spec--selected { border-color: var(--accent-color); background: var(--accent-soft); }
.merge-spec:has(input:enabled) { cursor: pointer; }
.merge-spec input { flex: none; margin-top: var(--space-1); accent-color: var(--accent-color); }
</style>
