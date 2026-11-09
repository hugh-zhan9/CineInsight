<template>
  <dialog open ref="playerShell" class="queue-inline-player playback-shell surface-cinema" aria-label="队列播放器" tabindex="0" @keydown.capture="handlePlaybackKey">
    <component :is="'media-controller'" ref="playerSurface" :id="`queue-media-${$.uid}`" class="playback-surface" tabindex="0" lang="zh-CN" nohotkeys gesturesdisabled novolumepref nomutedpref nosubtitleslangpref noautoseektolive defaultstreamtype="on-demand" @mediaseekrequest.capture="seekRequest" @mediaplayrequest.capture="guardPlayRequest">
    <component :is="identity.media_mode==='native' ? 'cine-native-media' : 'video'" :playbackSource.prop="nativePlaybackSource" slot="media" ref="video" :src="identity.media_mode==='native'?undefined:identity.locator" playsinline preload="metadata" @click="togglePlayback" data-test="queue-inline-video"
      @volumechange="rememberAudio" @loadedmetadata="loaded" @play="claimActive" @playing="playing" @pause="paused" @seeking="seeking" @seeked="seeked"
      @ratechange="scheduleClipEnd" @timeupdate="progress" @ended="ended" @error="failed" />
    <PlaybackControls v-if="!retired && (!isClip || clipRange)" ref="controls" :seek-range="clipRange" :seek-disabled="isClip && (!initialized || clipCompleting)" @seek="seekClip" :video-element="() => $refs.video" :dialog-element="() => $refs.playerShell" :surface-element="() => $refs.playerSurface">
      <template #settings><PlaybackSubtitleControls :video-id="identity.video_id" :source-version="identity.source_version" :queue-token="identity.token" :playback-range="clipRange" :video-element="() => $refs.video" :leave-fullscreen="() => $refs.controls?.leaveFullscreen()" @menu-change="$refs.controls?.holdControls($event)" /></template>
    </PlaybackControls>
    <div slot="centered-chrome" class="queue-inline-player__ending"><slot name="ending" :replay="replayEnded" :retire="retireEnded" /></div>
    </component>
    <p v-if="isClip && clipRange" class="help-text" data-test="queue-clip-range">区间预览：{{ (clipRange.startMS / 1000).toFixed(3) }} — {{ (clipRange.endMS / 1000).toFixed(3) }} 秒；本批结束后停止，不计入观看记录。</p>
    <p v-if="error" role="alert" data-test="queue-inline-error">{{ error }}</p>
  </dialog>
</template>
<script>
import { restorePlaybackAudio, rememberPlaybackAudio } from '../utils/playbackAudio.js';
import { queueClipRange, clampClipSeconds } from '../utils/playbackClip.js';
import { isWatchCompleted } from '../utils/watchState.js';
import { handlePlaybackKey, isPlaybackKeyboardControl } from '../utils/playbackKeyboard.js';
import PlaybackSubtitleControls from './player/PlaybackSubtitleControls.vue';
import PlaybackControls from './player/PlaybackControls.vue';
import { GetPlaybackQueue, ReportInlineQueuePlayback } from '../../wailsjs/go/main/App';
import '../utils/nativeMedia.js';
import { installPlaybackBarrier, claimActivePlayer, releaseActivePlayer } from '../utils/activePlayer.js';
const DESYNC_MESSAGE = '播放状态与待播队列不同步，已暂停。请停止队列后重新播放此项。';
const STALE_MESSAGE = '播放器已重新载入，已暂停。请在队列中重新播放此项。';
export default {
  name: 'QueueInlinePlayer',
  components: { PlaybackControls, PlaybackSubtitleControls },
  props: { session: { type: Object, required: true } },
  emits: ['error'],
  data() {
    return { clipTimer: null, clipCompleting: false, identity: { ...this.session }, seq: 0, cycle: Number(this.session.view_cycle || 1), wasEnded: false, endConsumed: false, initialized: false, seekingNow: false, lastProgressAt: 0, error: '', retired: false, desynced: false };
  },
  // 同一时间只有一个内嵌播放器在播（D-UI-11，utils/activePlayer.js）：这里开始播放会暂停详情 / 抽屉里的播放器，
  // 反过来那边开始播放时暂停这里；被暂停照常按 paused 上报队列。
  computed: { nativePlaybackSource(){return this.identity.media_mode==='native'?{...this.identity.native_source,queue_token:this.identity.token,start_ms:Math.round(this.identity.start_seconds*1000)}:null;}, isClip() { return this.identity.entry_kind === 'clip'; }, clipRange() { return queueClipRange(this.identity); } },
  created() { this._reports = Promise.resolve(); this._activeHandle = { pause: () => this.$refs.video?.pause() }; },
  watch: {
    'session.control_sequence'() {
      const video = this.$refs.video;
      if (!this.initialized || !video) return;
      if (this.session.desired_paused) video.pause(); else this.play();
    }
  },
  mounted(){restorePlaybackAudio(this.$refs.video);installPlaybackBarrier(this.$refs.video,this._activeHandle);},
  updated(){installPlaybackBarrier(this.$refs.video,this._activeHandle);},
  beforeUnmount() {
    this.retired = true; this.clearClipTimer();
if(this.$refs.video?.nativePlayback)this.$refs.video.close('navigate').catch(()=>{});
    releaseActivePlayer(this._activeHandle);
    const video = this.$refs.video;
    if (video) { video.pause(); video.removeAttribute('src'); video.load(); }
  },
  methods: {
    rememberAudio(event) { if (!this.retired && event.target === this.$refs.video) rememberPlaybackAudio(event.target); },
    async replayEnded() { const video = this.$refs.video; if (!video || this.retired) throw new Error('播放器已关闭'); video.currentTime = 0; await video.play(); },
    async retireEnded() { await this.$refs.controls?.leaveFullscreen(); await this.$refs.video?.pause(); await this._reports; if(this.$refs.video?.nativePlayback)await this.$refs.video.retire();this.retired = true; },
    handlePlaybackKey(event) {
      if (this.retired) return;
      if (isPlaybackKeyboardControl(event)) return;
      if (this.isClip && (!this.initialized || this.clipCompleting || !this.clipRange) && [' ', 'ArrowLeft', 'ArrowRight'].includes(event.key)) { event.preventDefault(); event.stopPropagation(); return; }
      handlePlaybackKey(event, this.$refs.video, this.clipRange);
      if (this.isClip && ['ArrowLeft', 'ArrowRight'].includes(event.key)) this.checkClipBoundary();
    },
    togglePlayback() { if (this.retired) return; if (!this.isClip || (this.initialized && !this.clipCompleting)) this.$refs.controls?.togglePlayback(); },
    guardPlayRequest(event) { if (this.isClip && (!this.initialized || this.clipCompleting || !this.clipRange)) { event.preventDefault(); event.stopImmediatePropagation(); } },
    seekRequest(event) { if (!this.isClip) return; event.preventDefault(); event.stopImmediatePropagation(); this.seekClip(Number(event.detail)); },
    seekClip(seconds) {
      if (!this.initialized || this.clipCompleting || !this.clipRange) return;
      const target = clampClipSeconds(seconds, this.clipRange); if (!Number.isFinite(target)) return;
      this.$refs.video.currentTime = target; this.checkClipBoundary();
    },
    clearClipTimer() { if (this.clipTimer !== null) clearTimeout(this.clipTimer); this.clipTimer = null; },
    scheduleClipEnd() {
      this.clearClipTimer(); const video = this.$refs.video;
      if (!this.isClip || !this.clipRange || !this.initialized || this.clipCompleting || this.retired || this.desynced || !video || video.paused || this.seekingNow || !(video.playbackRate > 0)) return;
      const delay = (this.clipRange.endMS / 1000 - video.currentTime) / video.playbackRate * 1000;
      this.clipTimer = setTimeout(() => { this.clipTimer = null; if (!this.checkClipBoundary()) this.scheduleClipEnd(); }, Math.max(10, Math.min(2147483647, delay)));
    },
    checkClipBoundary() {
      if (!this.isClip || !this.initialized || this.retired || this.desynced || this.clipCompleting) return this.clipCompleting;
      const video = this.$refs.video;
      if (!this.clipRange) { this.desync('预览区间无效，请重新加入队列。'); return true; }
      if (video.currentTime >= this.clipRange.endMS / 1000) { this.completeClip(); return true; }
      if (video.currentTime < this.clipRange.startMS / 1000) video.currentTime = this.clipRange.startMS / 1000;
      return false;
    },
    completeClip() {
      if (this.clipCompleting || this.retired || !this.clipRange) return;
      this.clipCompleting = true; this.wasEnded = true; this.seekingNow = false; this.clearClipTimer();
      const video = this.$refs.video; video.pause(); video.currentTime = this.clipRange.endMS / 1000; this.report('ended');
    },
    report(kind) {
      const video = this.$refs.video;
      if (this.retired || (this.desynced && !(this.isClip && kind === 'error')) || !video) return;
      const position = Number(video.currentTime);
      const duration = Number(video.duration);
      const fact = { kind, position: Number.isFinite(position) ? Math.max(0, position) : 0, duration: Number.isFinite(duration) ? Math.max(0, duration) : 0, message: kind === 'error' ? '当前文件无法在应用内播放，请检查原片或生成播放代理后重试。' : '' };
      // Keep reports ordered even when bridge calls complete at different speeds.
      this._reports = this._reports.then(() => this.deliver(fact));
    },
    async deliver(fact) {
      if (this.retired || (this.desynced && !(this.isClip && fact.kind === 'error'))) return;
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
        if (text.includes('queue_changed')) { if (this.isClip) { this.clearClipTimer(); this.$refs.video?.pause(); } return this.verifyCurrent(); }
        if (this.isClip) return this.desync('预览已停止，请在队列中核对后重新播放。');
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
      this.desynced = true; this.clearClipTimer();
      this.$refs.video?.pause();
      this.showError(message);
    },
    showError(text) { this.error = text; this.$emit('error', text); },
    loaded() {
      restorePlaybackAudio(this.$refs.video);
      if (this.initialized) return;
      const video = this.$refs.video;
      const requested = Number(this.identity.start_seconds || 0);
      if (this.isClip && (!this.clipRange || !Number.isFinite(video.duration) || this.clipRange.endMS / 1000 > video.duration)) { this.desync('预览区间无效或超出当前片长，请重新核对。'); this.report('error'); return; }
      const start = this.isClip ? this.clipRange.startMS / 1000 : (isWatchCompleted(requested, video.duration) ? 0 : requested);
      video.currentTime = this.isClip ? start : (Number.isFinite(video.duration) ? Math.min(start, Math.max(0, video.duration - 0.01)) : start);
      this.initialized = true; this.report('loaded');
      if (!this.session.desired_paused) this.play();
    },
    play() {
      if (this.retired) return;
      if (this.isClip && (!this.initialized || this.clipCompleting || !this.clipRange)) return;
      Promise.resolve(this.$refs.video?.play()).catch(error => {
        // A pause or source change interrupting play() is not a media failure.
        if (this.retired || error?.name === 'AbortError') return;
        this.failed();
      });
    },
    playing() { if (this.isClip && (!this.initialized || this.clipCompleting || this.desynced)) { this.$refs.video?.pause(); return; } if (!this.initialized || this.retired) return; this.wasEnded = false; this.seekingNow = false; this.report('playing'); this.scheduleClipEnd(); },
    claimActive() { if (!this.retired) claimActivePlayer(this._activeHandle); },
    paused() { this.clearClipTimer(); releaseActivePlayer(this._activeHandle); if (this.initialized && !this.wasEnded) this.report('paused'); },
    seeking() { if (!this.initialized || this.clipCompleting) return; this.clearClipTimer(); this.seekingNow = true; if (this.isClip) { const target = clampClipSeconds(this.$refs.video.currentTime, this.clipRange); if (target !== this.$refs.video.currentTime) this.$refs.video.currentTime = target; } this.report('seeking'); this.checkClipBoundary(); },
    seeked() { this.seekingNow = false; if (!this.initialized || this.clipCompleting) return; if (this.checkClipBoundary()) return; this.report('seeked'); if (!this.$refs.video.paused) this.playing(); },
    progress() { if (this.checkClipBoundary()) return; this.scheduleClipEnd(); if (!this.initialized || this.seekingNow || this.$refs.video?.paused || performance.now() - this.lastProgressAt < 1000) return; this.lastProgressAt = performance.now(); this.report('progress'); },
    ended() { if (!this.initialized || this.retired || this.seekingNow || this.wasEnded) return; if (this.isClip) { if (this.$refs.video.currentTime < this.clipRange.endMS / 1000) { this.clearClipTimer(); this.report('error'); this.desync('原片在预览终点前结束，请重新核对。'); } else this.completeClip(); return; } this.wasEnded = true; this.report('ended'); },
    failed() { if (this.retired || (this.isClip && this.desynced)) return; this.error = '无法播放当前文件，可在详情中生成播放代理后重试。'; this.report('error'); this.$emit('error', this.error); }
  }
};
</script>
<style scoped>
.queue-inline-player { position: relative; width: 100%; background: var(--player-bg); border-radius: var(--radius); overflow: hidden; }
.queue-inline-player .playback-surface { height: min(34vh, 300px); }
.queue-inline-player:modal .playback-surface, .queue-inline-player .playback-surface:fullscreen { height: 100vh; }
.queue-inline-player:modal { position: fixed; inset: 0; width: 100vw; height: 100vh; margin: 0; border-radius: 0; }
.queue-inline-player video, .queue-inline-player cine-native-media { display: block; width: 100%; height: 100%; object-fit: contain; }
.queue-inline-player p { color: var(--media-badge-fg); padding: var(--space-2); margin: 0; }
.queue-inline-player__ending { width: min(900px, 100%); max-height: 100%; overflow-y: auto; pointer-events: auto; }
</style>
