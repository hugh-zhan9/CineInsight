<template>
  <section class="edit-section" data-test="merge-editor">
    <h3>来源顺序</h3>
    <ol class="edit-list">
      <li v-for="(source, index) in sources" :key="source.video_id" class="edit-list__item" :data-test="`merge-source-${source.video_id}`">
        <div class="edit-list__row">
          <span class="edit-list__index">{{ index + 1 }}</span>
          <span class="edit-list__name" :title="videoName(source.video_id)">{{ videoName(source.video_id) }}</span>
          <span class="help-text">{{ sourceFacts(source.video_id) }}</span>
          <span class="edit-list__spacer"></span>
          <button type="button" class="row-btn" aria-label="上移" :disabled="!editable || index === 0" :data-test="`merge-up-${source.video_id}`" @click="move(index, -1)">↑</button>
          <button type="button" class="row-btn" aria-label="下移" :disabled="!editable || index === sources.length - 1" @click="move(index, 1)">↓</button>
          <button type="button" class="row-btn" :disabled="!editable || sources.length <= 2" :data-test="`merge-remove-${source.video_id}`" @click="remove(index)">移出</button>
        </div>
        <EditFrameStrip
          v-if="index < sources.length - 1"
          :title="`第 ${index + 1} 段与第 ${index + 2} 段的接缝`"
          :frames="seamFrames(index)"
        />
      </li>
    </ol>

    <template v-if="specOptions.length">
      <h3>输出规格</h3>
      <p class="help-text">来源的分辨率、帧率或动态范围不同，必须选定按哪个来源的规格导出；其他来源会缩放并居中加黑边，不裁切。</p>
      <label v-for="option in specOptions" :key="option.video_id" class="edit-radio" :data-test="`merge-spec-${option.video_id}`">
        <input type="radio" name="merge-spec" :value="option.video_id" :checked="specSelected(option)" :disabled="!editable" @change="chooseSpec(option.video_id)" />
        {{ videoName(option.video_id) }}：{{ option.width }}×{{ option.height }} · {{ fpsText(option.frame_rate) }}{{ option.hdr ? ' · HDR' : '' }}
      </label>
    </template>
  </section>
</template>

<script>
import EditFrameStrip from './EditFrameStrip.vue';
import { frameBefore, moveMergeSource, parseFrameRate, preflightSource, removeMergeSource, setMergeSpec, sourceFrameRate } from '../../utils/videoEdit.js';
import { workbenchVideoMixin } from './workbenchVideo.js';

// 顺序合并编辑器：来源排序/移出、接缝帧、规格选择（只在预检报规格不同时出现）。改动以 change 交给页面保存。
export default {
  name: 'MergeEditor',
  components: { EditFrameStrip },
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
      return parts.filter(Boolean).join(' · ');
    }
  }
};
</script>
