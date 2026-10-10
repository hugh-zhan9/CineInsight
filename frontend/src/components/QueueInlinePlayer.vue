<template>
  <div class="queue-inline-player">
    <video ref="video" :src="identity.locator" controls playsinline preload="metadata" data-test="queue-inline-video"
      @loadedmetadata="loaded" @playing="playing" @pause="paused" @seeking="seeking" @seeked="seeked"
      @timeupdate="progress" @ended="ended" @error="failed" />
    <p v-if="error" role="alert" data-test="queue-inline-error">{{ error }}</p>
  </div>
</template>
<script>
import { GetPlaybackQueue, ReportInlineQueuePlayback } from '../../wailsjs/go/main/App';
const DESYNC_MESSAGE = '播放状态与待播队列不同步，已暂停。请停止队列后重新播放此项。';
const STALE_MESSAGE = '播放器已重新载入，已暂停。请在队列中重新播放此项。';
export default {
  name: 'QueueInlinePlayer',
  props: { session: { type: Object, required: true } },
  emits: ['error'],
  data() {
    return { identity: { ...this.session }, seq: 0, cycle: Number(this.session.view_cycle || 1), wasEnded: false, endConsumed: false, initialized: false, seekingNow: false, lastProgressAt: 0, error: '', retired: false, desynced: false };
  },
  created() { this._reports = Promise.resolve(); },
  watch: {
    'session.control_sequence'() {
      const video = this.$refs.video;
      if (!this.initialized || !video) return;
      if (this.session.desired_paused) video.pause(); else this.play();
    }
  },
  beforeUnmount() {
    this.retired = true;
    const video = this.$refs.video;
    if (video) { video.pause(); video.removeAttribute('src'); video.load(); }
  },
  methods: {
    report(kind) {
      const video = this.$refs.video;
      if (this.retired || this.desynced || !video) return;
      const position = Number(video.currentTime);
      const duration = Number(video.duration);
      const fact = { kind, position: Number.isFinite(position) ? Math.max(0, position) : 0, duration: Number.isFinite(duration) ? Math.max(0, duration) : 0, message: kind === 'error' ? '当前文件无法在应用内播放，请检查原片或生成播放代理后重试。' : '' };
      // Keep reports ordered even when bridge calls complete at different speeds.
      this._reports = this._reports.then(() => this.deliver(fact));
    },
    async deliver(fact) {
      if (this.retired || this.desynced) return;
      // A replay opens a new view cycle only after the backend consumed the
      // previous natural end; an end it refused keeps the current cycle.
      if (fact.kind === 'playing' && this.endConsumed) { this.cycle++; this.endConsumed = false; }
      const event = { token: this.identity.token, video_id: this.identity.video_id, source_version: this.identity.source_version, seq: ++this.seq, view_cycle: this.cycle, ...fact };
      try {
        await ReportInlineQueuePlayback(event);
        if (fact.kind === 'ended') this.endConsumed = true;
      } catch (error) {
        const text = String(error);
        if (this.retired || /playback_quiesced|queue_end_ignored/.test(text)) return;
        if (text.includes('queue_changed')) return this.verifyCurrent();
        // The backend already accepted a higher seq for this token (e.g. this is
        // a remount); continuing would mix two players' facts.
        if (text.includes('queue_stale_event')) return this.desync(STALE_MESSAGE);
        this.showError(text);
      }
    },
    // queue_changed is expected for a token that was just replaced or stopped;
    // for the still-current token it is a desync that must be visible.
    async verifyCurrent() {
      let snapshot;
      try { snapshot = await GetPlaybackQueue({ limit: 1 }); } catch { return; }
      if (this.retired || snapshot?.session?.token !== this.identity.token) return;
      this.desync(DESYNC_MESSAGE);
    },
    desync(message) {
      if (this.desynced) return;
      this.desynced = true;
      this.$refs.video?.pause();
      this.showError(message);
    },
    showError(text) { this.error = text; this.$emit('error', text); },
    loaded() {
      if (this.initialized) return;
      const video = this.$refs.video;
      const start = Number(this.identity.start_seconds || 0);
      video.currentTime = Number.isFinite(video.duration) ? Math.min(start, Math.max(0, video.duration - 0.01)) : start;
      this.initialized = true; this.report('loaded');
      if (!this.session.desired_paused) this.play();
    },
    play() {
      Promise.resolve(this.$refs.video?.play()).catch(error => {
        // A pause or source change interrupting play() is not a media failure.
        if (this.retired || error?.name === 'AbortError') return;
        this.failed();
      });
    },
    playing() { if (!this.initialized || this.retired) return; this.wasEnded = false; this.seekingNow = false; this.report('playing'); },
    paused() { if (this.initialized && !this.wasEnded) this.report('paused'); },
    seeking() { if (!this.initialized) return; this.seekingNow = true; this.report('seeking'); },
    seeked() { this.seekingNow = false; if (!this.initialized) return; this.report('seeked'); if (!this.$refs.video.paused) this.playing(); },
    progress() { if (!this.initialized || this.seekingNow || this.$refs.video?.paused || performance.now() - this.lastProgressAt < 1000) return; this.lastProgressAt = performance.now(); this.report('progress'); },
    ended() { if (!this.initialized || this.retired || this.seekingNow || this.wasEnded) return; this.wasEnded = true; this.report('ended'); },
    failed() { if (this.retired) return; this.error = '无法播放当前文件，可在详情中生成播放代理后重试。'; this.report('error'); this.$emit('error', this.error); }
  }
};
</script>
<style scoped>
.queue-inline-player { background: #111; border-radius: 10px; overflow: hidden; }
.queue-inline-player video { display: block; width: 100%; height: min(34vh, 300px); object-fit: contain; }
.queue-inline-player p { color: #fff; padding: 8px; margin: 0; }
</style>
