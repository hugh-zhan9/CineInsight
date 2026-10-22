<template>
  <div class="subtitle-extract" role="dialog" aria-label="提取内嵌文字字幕">
    <h3>提取内嵌文字字幕</h3>
    <p>选择一条文字字幕轨，预览后保存为同名 SRT。</p>
    <label>字幕轨 <select v-model.number="track" class="select-input" :disabled="busy"><option :value="-1">请选择</option><option v-for="item in tracks" :key="item.index" :value="item.index" :disabled="!item.supported">{{ item.index }} · {{ item.language || '未知语言' }} · {{ item.title || item.codec }}{{ item.supported ? '' : '（不支持）' }}</option></select></label>
    <p v-if="selected?.reason">{{ selected.reason }}</p>
    <button type="button" class="btn btn--secondary" :disabled="busy || track < 0" @click="extract">{{ busy ? '正在提取…' : '提取并预览' }}</button>
    <template v-if="preview">
      <p>{{ preview.entries.length }} 条字幕；预览前 20 条。</p>
      <ol><li v-for="entry in preview.entries.slice(0, 20)" :key="entry.client_id"><span class="subtitle-extract__text">{{ entry.text }}</span></li></ol>
      <p v-if="overwrite?.exists">将覆盖现有字幕（会先备份，可恢复）。</p>
      <p v-if="overwrite?.shared_with?.length">这些视频共用同一字幕：{{ overwrite.shared_with.map(item => item.name).join('、') }}</p>
      <button type="button" class="btn btn--primary" :disabled="busy" @click="save">确认保存同名 SRT</button>
    </template>
    <p v-if="error" role="alert">{{ error }}</p>
    <button type="button" class="btn btn--secondary" @click="close">取消</button>
  </div>
</template>
<script>
import { ListExtractableSubtitleTracks, PreviewSubtitleExtraction, ConfirmSubtitleExtraction, CancelSubtitleExtraction, GetSubtitleOverwriteInfo } from '../../wailsjs/go/main/App';
export default {
  name: 'SubtitleExtractDialog', props: { videoId: { type: Number, required: true } }, emits: ['close', 'saved'],
  data: () => ({ tracks: [], source: '', track: -1, busy: false, error: '', preview: null, overwrite: null, retired: false }),
  computed: { selected() { return this.tracks.find(item => item.index === this.track); } },
  watch: { track() { this.preview = null; } },
  async mounted() {
    try { const result = await ListExtractableSubtitleTracks(this.videoId); if (this.retired) return; this.tracks = result.tracks; this.source = result.source_version; if (!result.tracks.length) this.error = '没有内嵌字幕轨'; } catch (error) { if (!this.retired) this.error = String(error); }
  },
  beforeUnmount() { this.retired = true; CancelSubtitleExtraction(this.videoId).catch(() => {}); },
  methods: {
    async extract() {
      this.busy = true;
      this.error = '';
      this.preview = null;
      try {
        const preview = await PreviewSubtitleExtraction({ video_id: this.videoId, source_version: this.source, track_index: this.track });
        const overwrite = await GetSubtitleOverwriteInfo(this.videoId);
        if (!this.retired) {
          this.preview = preview;
          this.overwrite = overwrite;
        }
      } catch (error) {
        if (!this.retired) this.error = String(error).replace(/^[a-z_]+:\s*/, '');
      } finally {
        this.busy = false;
      }
    },
    async save() {
      if (!this.preview) return;
      this.busy = true;
      this.error = '';
      try {
        const { token, video_id, source_version, track_index, target_fingerprint } = this.preview;
        const result = await ConfirmSubtitleExtraction({ token, video_id, source_version, track_index, target_fingerprint });
        if (this.retired) return;
        if (result.status === 'rejected') {
          this.error = result.message;
          this.preview = null;
          return;
        }
        this.$emit('saved', result);
      } catch (error) {
        if (!this.retired) {
          this.error = String(error).replace(/^[a-z_]+:\s*/, '');
          this.preview = null;
        }
      } finally {
        this.busy = false;
      }
    },
    close() {
      this.retired = true;
      CancelSubtitleExtraction(this.videoId).catch(() => {});
      this.$emit('close');
    }
  }
};
</script>
<style scoped>
.subtitle-extract { padding: var(--space-4); max-height: 70vh; overflow: auto; background: var(--bg-secondary); }
.subtitle-extract__text { white-space: pre-wrap; }
</style>
