<template>
  <div class="version-compare" role="dialog" aria-modal="true" aria-label="版本对比播放" data-test="version-compare">
    <header class="version-compare__bar">
      <h2 class="version-compare__title">版本对比</h2>
      <!-- 对比方式与声音来源（D-UD-06 SegmentedControl），每个选项带 version-compare-mode-* / audio-* 钩子。 -->
      <SegmentedControl v-model="mode" class="version-compare__segments" label="对比方式" :options="modeOptions" />
      <SegmentedControl v-model="audioSide" class="version-compare__segments" label="声音来源" :options="audioOptions" />
      <!-- 片头长短不一的两个版本：右侧整体提前或推后，只在这次打开内有效。 -->
      <div class="version-compare__group" role="group" aria-label="右侧偏移">
        <span class="version-compare__group-label">右侧偏移</span>
        <button type="button" class="btn btn--secondary btn--sm" data-test="version-compare-offset-minus-1" @click="adjustOffset(-1)">-1 秒</button>
        <button type="button" class="btn btn--secondary btn--sm" data-test="version-compare-offset-minus-01" @click="adjustOffset(-0.1)">-0.1</button>
        <span class="version-compare__offset" data-test="version-compare-offset">{{ offsetText }}</span>
        <button type="button" class="btn btn--secondary btn--sm" data-test="version-compare-offset-plus-01" @click="adjustOffset(0.1)">+0.1</button>
        <button type="button" class="btn btn--secondary btn--sm" data-test="version-compare-offset-plus-1" @click="adjustOffset(1)">+1 秒</button>
        <button type="button" class="btn btn--ghost btn--sm" :disabled="offset === 0" data-test="version-compare-offset-reset" @click="resetOffset">归零</button>
      </div>
      <div class="version-compare__spacer"></div>
      <button type="button" class="btn-icon" aria-label="关闭" title="关闭（Esc）" data-test="version-compare-close" @click="close"><AppIcon name="close" :size="20" /></button>
    </header>

    <!-- 两个 <video> 在两种方式下是同一对元素，只换布局，切换时不重新加载。
         滑动对比时两层叠放，上层左侧画面用 clip-path 只露出分界线左边。 -->
    <div
      ref="stage"
      :class="['version-compare__stage', `version-compare__stage--${mode}`]"
      data-test="version-compare-stage"
      @pointerdown="startDividerDrag"
      @pointermove="moveDivider"
      @pointerup="endDividerDrag"
      @pointercancel="endDividerDrag"
    >
      <div v-for="side in sides" :key="side" :class="['version-compare__pane', `version-compare__pane--${side}`]">
        <video
          :ref="el => setMedia(side, el)"
          class="version-compare__video"
          :data-test="`version-compare-video-${side}`"
          :src="mediaSrc(side)"
          :muted="mute[side]"
          :style="side === 'left' && mode === 'slider' ? { clipPath } : null"
          playsinline
          preload="auto"
          @loadedmetadata="onMetadata(side)"
          @durationchange="onMetadata(side)"
          @timeupdate="onTimeUpdate(side)"
          @seeking="state[side].seeking = true"
          @seeked="onSeeked(side)"
          @waiting="onWaiting(side)"
          @playing="onReady(side)"
          @canplay="onReady(side)"
          @pause="onPause(side)"
          @ended="onEnded(side)"
          @error="onMediaError(side, $event)"
        ></video>
        <div :class="['version-compare__label', `version-compare__label--${side}`]" :data-test="`version-compare-label-${side}`">
          <span class="version-compare__name" :title="labels[side].title"><span class="version-compare__side">{{ side === 'left' ? '左' : '右' }}</span>{{ labels[side].title }}</span>
          <span v-if="labels[side].facts" class="version-compare__facts" :data-test="`version-compare-facts-${side}`">{{ labels[side].facts }}</span>
          <span v-if="labels[side].proxyNote" class="version-compare__note">{{ labels[side].proxyNote }}</span>
          <span v-if="state[side].loading" class="version-compare__note">正在读取…</span>
          <span v-if="state[side].error" class="version-compare__error" role="alert" :data-test="`version-compare-error-${side}`">{{ state[side].error }}</span>
          <!-- 不能直放（2026-10-13）：proxy-preparing 按这一侧的 owner 自动请求代理，就绪后换成内嵌；proxy-manual 只给手动。 -->
          <span v-if="state[side].proxyStatus" class="version-compare__note" role="status" :data-test="`version-compare-proxy-status-${side}`">{{ state[side].proxyStatus }}</span>
          <span v-if="state[side].proxyAlert" class="version-compare__error" role="alert" :data-test="`version-compare-proxy-alert-${side}`">{{ state[side].proxyAlert }}</span>
          <button v-if="canGenerateProxy(side)" type="button" class="btn btn--secondary btn--sm version-compare__proxy-btn" :data-test="`version-compare-proxy-generate-${side}`" @pointerdown.stop @click="generateProxy(side)">生成播放代理</button>
        </div>
      </div>
      <div
        v-if="mode === 'slider'"
        class="version-compare__divider"
        :style="{ left: `${sliderPercent}%` }"
        role="slider"
        tabindex="0"
        aria-label="滑动对比分界线"
        aria-valuemin="0"
        aria-valuemax="100"
        :aria-valuenow="Math.round(sliderPercent)"
        data-test="version-compare-divider"
        @keydown="onDividerKey"
      ><span class="version-compare__handle" aria-hidden="true"><AppIcon name="chevron-left" :size="16" /><AppIcon name="chevron-right" :size="16" /></span></div>
    </div>

    <footer class="version-compare__transport">
      <button type="button" class="btn btn--primary btn--sm version-compare__play" :disabled="!leaderSide" data-test="version-compare-play" @click="togglePlay">{{ playing ? '暂停' : '播放' }}</button>
      <span class="version-compare__time" data-test="version-compare-time">{{ timeText }}</span>
      <input
        type="range"
        class="version-compare__seek"
        min="0"
        :max="duration || 0"
        step="0.1"
        :value="currentTime"
        :disabled="!leaderSide || !duration"
        aria-label="播放进度"
        data-test="version-compare-seek"
        @input="onScrub"
        @change="scrubbing = false"
      />
      <span v-if="transportError" class="version-compare__error" role="alert" data-test="version-compare-transport-error">{{ transportError }}</span>
    </footer>
  </div>
</template>

<script>
import { GetPreviewSession, GetVideoDetails } from '../../wailsjs/go/main/App';
import AppIcon from './ui/AppIcon.vue';
import SegmentedControl from './ui/SegmentedControl.vue';
import { closeModalLayer, isTopModalLayer, openModalLayer } from './ui/modalSignals.js';
import { createPreviewProxyFollower } from '../utils/previewProxy.js';
import {
  COMPARE_SYNC_INTERVAL_MS, applyCompareOffset, compareClipPath, compareDriftSeek, compareFollowerTarget, compareMediaErrorText,
  compareMuteState, compareSideLabel, compareSliderPercent, compareSourceFromSession, formatCompareOffset, formatCompareTime
} from '../utils/versionCompare.js';

const SIDES = ['left', 'right'];
const otherSide = side => (side === 'left' ? 'right' : 'left');
const emptySide = () => ({
  loading: true, details: null, session: null, src: '', proxy: null, error: '', manual: false, seeking: false, waiting: false,
  proxyPending: false, proxyStatus: '', proxyAlert: ''
});

// 版本对比播放器（2026-10-12）：两个版本左右同步播放比画质，由 App.vue 按 versionCompareState 常驻挂载。
// 左侧主导、右侧跟随（位置 = 左侧 + 偏移），定时纠偏的规则在 utils/versionCompare.js。
// 关闭时（按钮、Esc、宿主卸载）两个媒体元素都暂停并清掉 src，后台不留播放。
export default {
  name: 'VersionCompareDialog',
  components: { AppIcon, SegmentedControl },
  props: {
    leftVideoId: { type: Number, required: true },
    rightVideoId: { type: Number, required: true }
  },
  emits: ['close'],
  data() {
    return {
      sides: SIDES,
      mode: 'side',
      audioSide: 'left',
      offset: 0,
      sliderPercent: 50,
      dragging: false,
      // 用户的播放意图；缓冲时临时暂停另一侧不改它。
      playing: false,
      currentTime: 0,
      duration: 0,
      scrubbing: false,
      transportError: '',
      released: false,
      state: { left: emptySide(), right: emptySide() },
      modeOptions: [
        { value: 'side', label: '并排', dataTest: 'version-compare-mode-side' },
        { value: 'slider', label: '滑动对比', dataTest: 'version-compare-mode-slider' }
      ],
      audioOptions: [
        { value: 'left', label: '左侧声音', dataTest: 'version-compare-audio-left' },
        { value: 'right', label: '右侧声音', dataTest: 'version-compare-audio-right' }
      ]
    };
  },
  computed: {
    mute() { return compareMuteState(this.audioSide); },
    clipPath() { return compareClipPath(this.sliderPercent); },
    offsetText() { return formatCompareOffset(this.offset); },
    timeText() { return `${formatCompareTime(this.currentTime)} / ${formatCompareTime(this.duration)}`; },
    labels() {
      return {
        left: compareSideLabel(this.state.left.details, this.state.left.session, this.leftVideoId),
        right: compareSideLabel(this.state.right.details, this.state.right.session, this.rightVideoId)
      };
    },
    // 两侧都读完才定主导方；左侧放不了时由右侧单独主导（只有一侧能放就谈不上同步）。
    leaderSide() {
      if (this.state.left.loading || this.state.right.loading) return '';
      if (this.usable('left')) return 'left';
      return this.usable('right') ? 'right' : '';
    },
    followerSide() {
      return this.leaderSide === 'left' && this.usable('right') ? 'right' : '';
    }
  },
  watch: {
    // 主导方定下或换了（左侧中途报错）：进度条改读新主导方的时长与位置；出声的一侧放不了时改由主导方出声。
    leaderSide(side) {
      const el = this.media(side);
      const value = Number(el?.duration);
      this.duration = Number.isFinite(value) && value > 0 ? value : 0;
      this.currentTime = Number(el?.currentTime) || 0;
      if (side && !this.usable(this.audioSide)) this.audioSide = side;
    }
  },
  created() {
    this._media = { left: null, right: null };
    // 每一侧一个代理跟随器（各自的 owner）：关闭对话框时撤销还在等的请求。
    this._proxyFollowers = Object.fromEntries(SIDES.map(side => [side, createPreviewProxyFollower({
      kind: `compare-${side}`,
      onState: change => this.applyProxyState(side, change),
      onReady: videoID => this.loadSide(side, videoID, { follow: false })
    })]));
  },
  // 进模态层栈（ui/modalSignals.js）：叠在它上面的对话框（退出确认、应用内确认框…）先收 Esc，
  // 它从对话框里打开时又排在那个对话框上面。
  beforeMount() {
    openModalLayer(this);
  },
  mounted() {
    window.addEventListener('keydown', this.onKeydown, true);
    this._syncTimer = setInterval(() => this.syncTick(), COMPARE_SYNC_INTERVAL_MS);
    this.loadSide('left', this.leftVideoId);
    this.loadSide('right', this.rightVideoId);
  },
  beforeUnmount() {
    window.removeEventListener('keydown', this.onKeydown, true);
    closeModalLayer(this);
    this.release();
  },
  methods: {
    setMedia(side, el) { this._media[side] = el || null; },
    media(side) { return side ? this._media[side] : null; },
    usable(side) { return !!this.state[side].src && !this.state[side].error; },
    mediaSrc(side) { return this.released ? undefined : (this.state[side].src || undefined); },
    // 播放源与详情抽屉同一条路（GetPreviewSession，含播放代理）；标注用 GetVideoDetails。两者各自失败互不拖累。
    // follow：会话为 proxy-preparing 时自动请求代理。代理就绪后的重取不再自动请求，免得反复排队。
    async loadSide(side, videoID, { follow = true } = {}) {
      const [sessionResult, detailsResult] = await Promise.allSettled([GetPreviewSession(videoID), GetVideoDetails(videoID)]);
      if (this.released) return;
      const state = this.state[side];
      state.loading = false;
      if (detailsResult.status === 'fulfilled') state.details = detailsResult.value || null;
      if (sessionResult.status === 'rejected') {
        state.error = `读取播放源失败：${sessionResult.reason}`;
        return;
      }
      state.session = sessionResult.value || null;
      const source = compareSourceFromSession(state.session);
      state.src = source.src;
      state.proxy = source.proxy;
      state.error = source.error;
      state.manual = source.manual;
      if (follow) this._proxyFollowers[side].follow(state.session);
    },
    applyProxyState(side, change) {
      const state = this.state[side];
      state.proxyPending = change.phase === 'pending';
      state.proxyStatus = state.proxyPending || change.phase === 'notice' ? change.text : '';
      state.proxyAlert = change.phase === 'failed' ? change.text : '';
    },
    canGenerateProxy(side) {
      const state = this.state[side];
      return !this.released && !state.src && !state.proxyPending && ['proxy-preparing', 'proxy-manual'].includes(state.session?.mode);
    },
    generateProxy(side) {
      this._proxyFollowers[side].generate(side === 'left' ? this.leftVideoId : this.rightVideoId);
    },
    onKeydown(event) {
      // 上面叠着对话框时让给它；自己在最上面时 Esc 只关它，不让下面的弹窗、抽屉与片库再处理一次。
      if (event.key !== 'Escape' || !isTopModalLayer(this)) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      this.close();
    },
    close() {
      this.release();
      this.$emit('close');
    },
    release() {
      if (this.released) return;
      this.released = true;
      for (const side of SIDES) this._proxyFollowers[side].stop();
      this.playing = false;
      clearInterval(this._syncTimer);
      for (const side of SIDES) {
        const el = this._media[side];
        if (!el) continue;
        try { el.pause(); } catch (_err) { /* 元素已失效时无需处理 */ }
        el.removeAttribute('src');
        try { el.load(); } catch (_err) { /* 同上 */ }
      }
    },
    playMedia(side) {
      const el = this.media(side);
      if (!el) return;
      Promise.resolve(el.play()).catch(err => {
        // 暂停或换源打断 play() 不算失败。
        if (this.released || err?.name === 'AbortError') return;
        this.pauseAll();
        this.transportError = `无法开始播放：${err?.message || err}`;
      });
    },
    togglePlay() {
      if (this.playing) this.pauseAll();
      else this.playAll();
    },
    // 两侧的 play() 都在这次点击里调用，都算用户手势触发。
    playAll() {
      const leaderSide = this.leaderSide;
      if (!leaderSide || this.released) return;
      this.transportError = '';
      this.playing = true;
      // 播完再按播放：两侧一起回到开头，别让右侧先被对到片尾。
      if (this.media(leaderSide)?.ended) this.seekTo(0);
      const follower = this.media(this.followerSide);
      const target = this.followerTarget();
      if (follower && target) {
        const seekTo = compareDriftSeek({ leaderTime: this.media(leaderSide)?.currentTime, followerTime: follower.currentTime, offset: this.offset, followerDuration: follower.duration });
        if (seekTo !== null) follower.currentTime = seekTo;
      }
      this.playMedia(leaderSide);
      if (follower && target?.inRange) this.playMedia(this.followerSide);
    },
    pauseAll() {
      this.playing = false;
      for (const side of SIDES) {
        const el = this._media[side];
        if (el && !el.paused) el.pause();
      }
    },
    followerTarget() {
      const leader = this.media(this.leaderSide);
      const follower = this.media(this.followerSide);
      if (!leader || !follower) return null;
      return compareFollowerTarget(leader.currentTime, this.offset, follower.duration);
    },
    // 任一侧在拖动或缓冲时不纠偏、不拉起，等它就绪再说。
    syncBusy() {
      return SIDES.some(side => this.state[side].seeking || this.state[side].waiting);
    },
    // 定时纠偏：偏差超过阈值才拖右侧；右侧跟着左侧的实际播放状态走（左侧在放且右侧在范围内才放）。
    syncTick() {
      if (this.released || this.syncBusy()) return;
      const leader = this.media(this.leaderSide);
      const follower = this.media(this.followerSide);
      if (!leader || !follower) return;
      const target = compareFollowerTarget(leader.currentTime, this.offset, follower.duration);
      const seekTo = compareDriftSeek({ leaderTime: leader.currentTime, followerTime: follower.currentTime, offset: this.offset, followerDuration: follower.duration });
      if (seekTo !== null) follower.currentTime = seekTo;
      const shouldPlay = !leader.paused && target.inRange;
      if (shouldPlay && follower.paused) this.playMedia(this.followerSide);
      else if (!shouldPlay && !follower.paused) follower.pause();
    },
    // 一侧缓冲时先停另一侧，免得缓冲完差出一截再拖。
    onWaiting(side) {
      this.state[side].waiting = true;
      if (!this.playing) return;
      const other = this.media(otherSide(side));
      if (other && !other.paused) other.pause();
    },
    onReady(side) {
      this.state[side].waiting = false;
      this.resumeIfNeeded();
    },
    onSeeked(side) {
      this.state[side].seeking = false;
      this.resumeIfNeeded();
    },
    resumeIfNeeded() {
      if (!this.playing || this.released || this.syncBusy()) return;
      const leader = this.media(this.leaderSide);
      if (leader?.paused && !leader.ended) this.playMedia(this.leaderSide);
      this.syncTick();
    },
    // 左侧被系统或别处暂停（不是本组件缓冲时停的）：播放意图跟着变，右侧也停。
    onPause(side) {
      if (side !== this.leaderSide || this.released || !this.playing || this.syncBusy()) return;
      const leader = this.media(side);
      if (leader?.ended) return;
      this.pauseAll();
    },
    onEnded(side) {
      if (side !== this.leaderSide) return;
      this.pauseAll();
    },
    onMetadata(side) {
      if (side !== this.leaderSide) return;
      const value = Number(this.media(side)?.duration);
      if (Number.isFinite(value) && value > 0) this.duration = value;
    },
    onTimeUpdate(side) {
      if (side !== this.leaderSide || this.scrubbing) return;
      this.currentTime = Number(this.media(side)?.currentTime) || 0;
    },
    onMediaError(side, event) {
      if (this.released || !event?.target?.error) return;
      this.state[side].error = compareMediaErrorText(this.state[side].proxy);
      this.state[side].waiting = false;
      this.state[side].seeking = false;
    },
    // 拖进度条：左侧到所拖位置，右侧到对应的偏移位置。
    onScrub(event) {
      this.scrubbing = true;
      this.seekTo(Number(event.target.value));
    },
    seekTo(seconds) {
      const leader = this.media(this.leaderSide);
      if (!leader) return;
      const max = this.duration > 0 ? this.duration : Infinity;
      const time = Math.min(Math.max(Number(seconds) || 0, 0), max);
      leader.currentTime = time;
      this.currentTime = time;
      const follower = this.media(this.followerSide);
      if (follower) follower.currentTime = compareFollowerTarget(time, this.offset, follower.duration).time;
    },
    // 偏移改了就立刻把右侧对到新位置：0.1 秒的微调小于纠偏阈值，不能等定时纠偏。
    adjustOffset(delta) {
      this.offset = applyCompareOffset(this.offset, delta);
      this.realignFollower();
    },
    resetOffset() {
      this.offset = 0;
      this.realignFollower();
    },
    realignFollower() {
      const follower = this.media(this.followerSide);
      const target = this.followerTarget();
      if (!follower || !target) return;
      follower.currentTime = target.time;
    },
    startDividerDrag(event) {
      if (this.mode !== 'slider' || (event.button !== undefined && event.button > 0)) return;
      this.dragging = true;
      event.currentTarget?.setPointerCapture?.(event.pointerId);
      this.moveDivider(event);
    },
    moveDivider(event) {
      if (!this.dragging || !this.$refs.stage) return;
      this.sliderPercent = compareSliderPercent(event.clientX, this.$refs.stage.getBoundingClientRect());
    },
    endDividerDrag(event) {
      if (!this.dragging) return;
      this.dragging = false;
      event.currentTarget?.releasePointerCapture?.(event.pointerId);
    },
    onDividerKey(event) {
      const step = { ArrowLeft: -2, ArrowRight: 2 }[event.key];
      if (!step) return;
      event.preventDefault();
      this.sliderPercent = Math.min(Math.max(this.sliderPercent + step, 0), 100);
    }
  }
};
</script>

<style scoped>
/* 全窗口查看器：与对话框同一层（--z-modal，压在抽屉之上），全局提示在更上层。 */
.version-compare {
  position: fixed;
  inset: 0;
  z-index: var(--z-modal);
  display: flex;
  flex-direction: column;
  background: var(--player-bg);
  color: var(--text-primary);
}
.version-compare__bar,
.version-compare__transport {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px 14px;
  padding: var(--space-2) var(--space-4);
  background: var(--panel-bg);
}
.version-compare__bar { border-bottom: 1px solid var(--hairline); }
.version-compare__transport { border-top: 1px solid var(--hairline); flex-wrap: nowrap; }
.version-compare__title { margin: 0; font-size: var(--fs-section); font-weight: var(--fw-semibold); }
.version-compare__group { display: inline-flex; align-items: center; gap: var(--space-1); }
.version-compare__group-label { color: var(--text-secondary); font-size: var(--fs-caption); margin-right: 2px; }
.version-compare__offset { min-width: 64px; text-align: center; font-family: var(--font-mono); font-size: var(--fs-caption); }
.version-compare__spacer { flex: 1; }

.version-compare__stage { position: relative; flex: 1; min-height: 0; overflow: hidden; touch-action: none; }
.version-compare__stage--side { display: grid; grid-template-columns: 1fr 1fr; gap: 2px; }
.version-compare__stage--slider { cursor: ew-resize; }
.version-compare__pane { position: relative; min-width: 0; min-height: 0; }
.version-compare__stage--slider .version-compare__pane { position: absolute; inset: 0; }
.version-compare__stage--slider .version-compare__pane--left { z-index: 2; }
.version-compare__stage--slider .version-compare__pane--right { z-index: 1; }
.version-compare__video { display: block; width: 100%; height: 100%; object-fit: contain; }

.version-compare__label {
  position: absolute;
  top: 10px;
  z-index: 3;
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-width: min(46%, 460px);
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  background: var(--media-badge-bg);
  color: var(--media-badge-fg);
  font-size: var(--fs-caption);
  pointer-events: none;
}
.version-compare__label--left { left: 10px; }
.version-compare__proxy-btn { pointer-events: auto; align-self: flex-start; }
.version-compare__label--right .version-compare__proxy-btn { align-self: flex-end; }
.version-compare__label--right { right: 10px; text-align: right; }
.version-compare__name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: var(--fw-semibold); }
.version-compare__side { margin-right: 6px; padding: 0 5px; border-radius: var(--radius-sm); background: var(--accent-color); color: var(--accent-on); font-size: var(--fs-caption); }
.version-compare__facts { font-family: var(--font-mono); font-size: var(--fs-caption); opacity: 0.9; }
.version-compare__note { font-size: var(--fs-caption); opacity: 0.85; }
.version-compare__error { color: var(--danger-color); font-size: var(--fs-caption); white-space: normal; }
.version-compare__transport .version-compare__error { flex: none; max-width: 40%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--danger-text); }

.version-compare__divider {
  position: absolute;
  top: 0;
  bottom: 0;
  z-index: 3;
  width: 2px;
  margin-left: -1px;
  background: var(--media-badge-fg);
}
.version-compare__divider:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 2px; }
.version-compare__handle {
  position: absolute;
  top: 50%;
  left: 50%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 30px;
  border: 2px solid var(--media-badge-fg);
  border-radius: var(--radius-pill);
  background: var(--media-badge-bg);
  color: var(--media-badge-fg);
  transform: translate(-50%, -50%);
}
.version-compare__handle > svg + svg { margin-left: -6px; }

.version-compare__play { min-width: 64px; }
.version-compare__time { flex: none; font-family: var(--font-mono); font-size: var(--fs-caption); color: var(--text-secondary); }
.version-compare__seek { flex: 1; min-width: 120px; accent-color: var(--accent-color); }
</style>
