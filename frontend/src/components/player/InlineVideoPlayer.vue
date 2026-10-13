<!--
  InlineVideoPlayer：应用内嵌播放器（D-UI-11）。从 PreviewDrawer 抽出，详情抽屉与观看侧整页详情共用。

  Props
  - videoId（Number，必填）：要播的视频。
  - session（Object，可不传）：预览会话（GetPreviewSession 的结果）。mode 为 inline（源文件或代理）、proxy-preparing
      （不能直放，等自动代理）、proxy-manual（不能直放且预计代理太大，只给手动生成）、external-preview（判不出编码、
      没有 ffmpeg 时的旧行为）或 unsupported。
      传了就是受控：会话归宿主，null 表示宿主还在取；代理生成好或删掉后会话要换，组件发 preview-session-stale，
      宿主重取再传进来（抽屉走这一种）。不传则组件按 videoId 自己取，代理就绪后自己重取（整页详情可以走这一种）。
  - startTimeMs（Number|null）：指定起播时刻（毫秒：字幕命中、场景检索、书签）。有值时这一场按 jump 上报，只允许往前写断点。
  - resumePositionSeconds（Number）：续播断点（秒），没有 startTimeMs 时从这里起播（>0 为 resume，否则 start）。
      宿主用 utils/watchState.js 的 resumePosition(video) 算好再传；播放中它变了不会把播放器拉回去。
  - bookmarkRange（Object|null）：{ startMS, endMS, requestID, sourceToken?, videoID? }。定位到 startMS，播到 endMS 暂停一次并发
      bookmark-range-end；requestID 换了（新对象）就再定位一次。带 sourceToken 时与会话的 source_version 不一致就不套用任何起播位置
      （原片在解析书签之后变了）。它只管定位与区间末尾暂停：origin 只看 startTimeMs / resumePositionSeconds，从书签起播时请把
      startTimeMs 也设成书签起点；播放中换了书签，这一场改按 jump 上报。
  - autoplay（Boolean）：每个会话读到元数据、定好位置后自动开始播放一次。起播一律静音（沿用抽屉），用户自己开声音。
  - durationSeconds（Number）：片库记录的时长（秒）。播放器还读不到时长时，悬停预览图与有效观看判定用它。

  Events
  - watch-progress：{ videoID, positionSeconds, completed, origin: resume|start|jump, durationSeconds（读不到为 0）}（D-PC42）。
      播放中每 10 秒最多一次；暂停、播完、换会话、卸载时立即补一次。
  - preview-session-stale（videoID）：只在受控时发，宿主应重取会话。
  - preview-externally（videoID）：用户点了「用系统播放器预览」，宿主调 PreviewExternally。
  - bookmark-range-end（bookmarkRange）：播到书签区间末尾，已暂停。

  组件内部负责：有效观看（越过 viewThreshold 或看完时 RecordViewEvent inline_view，每个观看会话一次）、悬停进度条的
  seek 预览图、内嵌报错时给出系统播放器与生成播放代理两条路、代理排队进度与完成后切回内嵌（playback-proxy-state）。
  自动代理（2026-10-13「直接播 + 自动代理」）：会话为 proxy-preparing、或直放的源解码失败时，组件以自己的 owner 调
  RequestPreviewProxy(id, owner)；换片或卸载（宿主关闭预览）时 CancelPreviewProxy(id, owner) 只撤销自己的请求——别的宿主
  还在等同一部时后端不取消。会话为 proxy-manual（预计产物太大）时不自动请求，只给手动「生成播放代理」。
  同时在等的自动代理已有 2 部时请求被拒，显示一句提示。用户在这次预览里删掉代理后不当场自动重建。
  同一时间只有一个内嵌播放器在播（utils/activePlayer.js）：本组件开始播放时会暂停上一个。

  宿主可调的方法：play()（返回 video.play() 的 Promise）、pause()、currentTime()（秒）、scrollIntoView(options)、
  createPlaybackProxy()、deletePlaybackProxy()（先确认）。
  宿主可读的状态：playbackProxy、proxyBusy、proxyError、proxyNotice、proxyTracking、proxyPending、proxyProgressText、
  proxyCreateLabel（生成失败或取消的提示由宿主决定放在哪里，抽屉放在技术信息区块）；seekPreview、viewSession、bookmarkRangeActive。
  proxyStatusLine（进度与提示，role=status）/ proxyAlertLine（失败，role=alert）是播放器框里显示的两行；
  showsProxyState 为真时框里已经在显示它们，宿主可不再重复。
  其余成员是内部实现。类名沿用抽屉里的 preview-drawer__*：抽屉既有测试与样式守卫按这些名字找播放器。
-->
<template>
  <div class="inline-player">
    <div v-if="!activeSession" class="preview-drawer__placeholder">正在准备预览...</div>
    <template v-else-if="activeSession.mode === 'inline' && activeSession.inline_source && !inlinePlaybackFailed">
      <div class="preview-drawer__player-shell">
        <video
          ref="videoElement"
          :key="previewMediaGeneration"
          class="preview-drawer__video"
          controls
          playsinline
          preload="metadata"
          :muted="true"
          @loadedmetadata="handleLoadedMetadata"
          @play="handlePlay"
          @timeupdate="handleTimeUpdate"
          @seeking="handleSeeking"
          @pause="handlePause"
          @ended="handleEnded"
          @error="handleVideoError"
        >
          <source :src="activeSession.inline_source.locator_value" :type="activeSession.inline_source.mime" @error="handleSourceError" />
        </video>
        <div
          v-if="activeSeekSprite"
          class="preview-drawer__seek-track"
          aria-hidden="true"
          @pointermove="handleSeekPointerMove"
          @pointerleave="seekPreview = null"
        >
          <div
            v-if="seekPreview"
            class="preview-drawer__seek-preview"
            :style="{ left: `${seekPreview.clampedPercent}%` }"
          >
            <span class="preview-drawer__seek-image">
              <img
                :src="seekSpriteSrc"
                alt=""
                :style="seekSpriteImageStyle"
                @error="handleSeekSpriteError"
              />
            </span>
            <time>{{ formatDuration(seekPreview.seconds) }}</time>
          </div>
        </div>
      </div>
    </template>
    <!-- 内嵌播放器报错（D-PC26、PLAY-05）：解码失败不再是一块黑屏，给出系统播放器与播放代理两条路。
         直放的源解码失败时还会自动排一项代理（2026-10-13），就绪后自动切回内嵌。
         已经在经代理播放时再生成一份也没用，只留系统播放器。 -->
    <div v-else-if="activeSession.mode === 'inline' && inlinePlaybackFailed" class="preview-drawer__placeholder" role="alert" data-test="preview-inline-error">
      <p>无法内嵌播放此文件</p>
      <div class="detail-action-row">
        <button type="button" class="btn-primary" data-test="preview-inline-error-external" @click="$emit('preview-externally', Number(videoId))">用系统播放器预览</button>
        <button v-if="!playingThroughProxy && proxyBlocked !== 'unavailable'" type="button" class="btn-secondary" data-test="preview-inline-error-proxy" :disabled="proxyBusy || proxyPending" @click="createPlaybackProxy">{{ proxyCreateLabel }}</button>
      </div>
      <p v-if="proxyStatusLine" class="preview-drawer__proxy-progress" role="status" data-test="preview-inline-error-progress">{{ proxyStatusLine }}</p>
      <p v-if="proxyAlertLine" class="preview-drawer__proxy-progress" role="alert" data-test="preview-inline-error-alert">{{ proxyAlertLine }}</p>
    </div>
    <!-- 不能直放（2026-10-13「直接播 + 自动代理」）：proxy-preparing 打开即按 owner 自动请求代理，这里显示排位与
         生成状态，就绪后自动切到内嵌；proxy-manual（预计产物太大）不自动生成，只给手动按钮。都可以用系统播放器预览。 -->
    <div v-else-if="activeSession.mode === 'proxy-preparing' || activeSession.mode === 'proxy-manual'" class="preview-drawer__placeholder" :data-mode="activeSession.mode" data-test="preview-proxy-preparing">
      <p>{{ activeSession.reason_message || '这个文件需要先生成播放代理才能在应用内播放。' }}</p>
      <div class="detail-action-row">
        <button type="button" class="btn-primary" data-test="preview-proxy-preparing-external" @click="$emit('preview-externally', Number(videoId))">用系统播放器预览</button>
        <button v-if="proxyBlocked !== 'unavailable'" type="button" class="btn-secondary" data-test="preview-proxy-preparing-proxy" :disabled="proxyBusy || proxyPending" @click="createPlaybackProxy">{{ proxyCreateLabel }}</button>
      </div>
      <p v-if="proxyStatusLine" class="preview-drawer__proxy-progress" role="status" data-test="preview-proxy-preparing-status">{{ proxyStatusLine }}</p>
      <p v-if="proxyAlertLine" class="preview-drawer__proxy-progress" role="alert" data-test="preview-proxy-preparing-alert">{{ proxyAlertLine }}</p>
    </div>
    <template v-else-if="activeSession.mode === 'external-preview' && activeSession.external_action">
      <div class="preview-drawer__placeholder">
        <p>{{ activeSession.reason_message }}</p>
        <button type="button" class="btn-primary" @click="$emit('preview-externally', Number(videoId))">{{ activeSession.external_action.button_label }}</button>
        <p v-if="proxyProgressText" class="preview-drawer__proxy-progress" data-test="preview-player-proxy-progress">{{ proxyProgressText }}</p>
      </div>
    </template>
    <div v-else class="preview-drawer__placeholder">{{ activeSession.reason_message || '当前视频暂不支持预览。' }}</div>
  </div>
</template>

<script>
import { markRaw } from 'vue';
import { CancelPreviewProxy, CreatePlaybackProxy, DeletePlaybackProxy, GetPlaybackProxy, GetPlaybackProxyStatus, GetPreviewSession, RecordViewEvent, RequestPreviewProxy } from '../../../wailsjs/go/main/App';
import { detailPlaybackOrigin, detailPlaybackStartMs, formatDuration } from '../../utils/mediaDetails.js';
import { isWatchCompleted } from '../../utils/watchState.js';
import { createPlaybackAccumulator, newViewSessionID, viewThreshold } from '../../utils/viewThreshold.js';
import { confirmAction } from '../../utils/feedback.js';
import { PLAYBACK_PROXY_CODE_LABELS, advanceProxyTracking, playbackProxyItemStateText, proxyRequestErrorText } from '../../utils/playbackProxy.js';
import { newPreviewProxyOwner } from '../../utils/previewProxy.js';
import { claimActivePlayer, releaseActivePlayer } from '../../utils/activePlayer.js';

export default {
  name: 'InlineVideoPlayer',
  props: {
    videoId: { type: Number, required: true },
    // 不传（undefined）表示由组件自己取会话；传 null 表示宿主还在取。
    session: { type: Object, default: undefined },
    startTimeMs: { type: Number, default: null },
    resumePositionSeconds: { type: Number, default: 0 },
    bookmarkRange: { type: Object, default: null },
    autoplay: { type: Boolean, default: false },
    durationSeconds: { type: Number, default: 0 }
  },
  emits: ['watch-progress', 'preview-session-stale', 'preview-externally', 'bookmark-range-end'],
  data() {
    return {
      ownSession: null,
      previewMediaGeneration: 0,
      appliedSeekKey: '', lastProgressEmittedAt: 0, resettingVideo: false, hasPlaybackStarted: false, autoplayPending: false,
      // 本次观看会话（D-PC43）：整个对象换掉时可被宿主读到，里面的累计量高频变化，不做响应式。
      viewSession: null,
      bookmarkRangeActive: false,
      seekPreview: null, seekSpriteUnavailable: false, seekSpriteRetryCount: 0, seekSpriteRetryTimer: null,
      inlinePlaybackFailed: false,
      playbackProxy: null, proxyBusy: false, proxyError: '', proxyNotice: '',
      // 自动请求被后端挡下的原因：too_large（只给手动生成）、unavailable（没有 ffmpeg，只给系统播放器）。
      proxyBlocked: '',
      // 正在等的那一项代理（D-PC26）：{ videoID, state }，state 为 playbackProxyItemState 的结果，
      // 入队请求还没返回时 phase 为 submitting。只有它完成时才自动切换，旧结果不会触发。
      proxyTracking: null
    };
  },
  computed: {
    controlled() { return this.session !== undefined; },
    activeSession() { return this.controlled ? this.session : this.ownSession; },
    // 会话里带 proxy 就说明这次内嵌播放走的是代理（后端 D-004 标注）。
    playingThroughProxy() { return Boolean(this.activeSession?.proxy); },
    bookmarkForThisVideo() {
      const range = this.bookmarkRange;
      return range && (range.videoID === undefined || range.videoID === null || Number(range.videoID) === Number(this.videoId)) ? range : null;
    },
    // 书签区间只在会话属于本片、源版本与解析书签时一致时生效；不一致时什么位置都不套用（原片已变）。
    readyBookmark() {
      const range = this.bookmarkForThisVideo;
      const session = this.activeSession;
      if (!range || Number(session?.video_id) !== Number(this.videoId)) return null;
      return !('sourceToken' in range) || session.source_version === range.sourceToken ? range : null;
    },
    bookmarkSourceMismatch() {
      const range = this.bookmarkForThisVideo;
      return !!(range && this.activeSession && 'sourceToken' in range && this.activeSession.source_version !== range.sourceToken);
    },
    activeSeekSprite() {
      const sprite = this.activeSession?.seek_sprite;
      if (this.seekSpriteUnavailable || !sprite?.locator_value) return null;
      const numericFields = ['frame_width', 'frame_height', 'columns', 'rows', 'frame_count', 'interval_seconds'];
      return numericFields.every(field => Number.isFinite(Number(sprite[field])) && Number(sprite[field]) > 0) ? sprite : null;
    },
    seekSpriteSrc() {
      if (!this.activeSeekSprite) return '';
      const base = this.activeSeekSprite.locator_value;
      return this.seekSpriteRetryCount > 0 ? `${base}${base.includes('?') ? '&' : '?'}r=${this.seekSpriteRetryCount}` : base;
    },
    seekSpriteImageStyle() {
      if (!this.activeSeekSprite || !this.seekPreview) return {};
      const columns = Number(this.activeSeekSprite.columns);
      const rows = Number(this.activeSeekSprite.rows);
      const column = this.seekPreview.frameIndex % columns;
      const row = Math.floor(this.seekPreview.frameIndex / columns);
      return {
        width: `${columns * 100}%`,
        height: `${rows * 100}%`,
        left: `${-column * 100}%`,
        top: `${-row * 100}%`
      };
    },
    proxyPending() { return !!this.proxyTracking && this.proxyTracking.videoID === Number(this.videoId); },
    // 「排队中（第 N 个）」「生成中」（D-PC26、PLAY-04）。入队请求还没回来时说「正在加入队列」。
    proxyProgressText() {
      if (!this.proxyPending) return '';
      return playbackProxyItemStateText(this.proxyTracking.state) || '正在加入队列';
    },
    proxyCreateLabel() {
      if (this.proxyPending) return this.proxyTracking.state?.phase === 'queued' ? '排队中...' : '生成中...';
      return this.proxyBusy ? '处理中...' : '生成播放代理';
    },
    // 播放器框里（等代理、内嵌报错）的两行：进度与提示放 role=status，失败放 role=alert，分成两个元素。
    proxyStatusLine() { return this.proxyPending ? this.proxyProgressText : this.proxyNotice; },
    proxyAlertLine() { return this.proxyPending ? '' : this.proxyError; },
    // 播放器框里正在显示代理状态（等代理、只能手动生成或内嵌报错）：宿主据此不再在框外重复同一句。
    showsProxyState() { return ['proxy-preparing', 'proxy-manual'].includes(this.activeSession?.mode) || this.inlinePlaybackFailed; }
  },
  watch: {
    // 书签先于会话处理：创建时还没有观看会话，不会提前把起播来源定成 jump（与抽屉原先的顺序一致）。
    bookmarkRange: {
      immediate: true,
      handler(range) {
        this.bookmarkRangeActive = range?.endMS != null;
        this.appliedSeekKey = '';
        if (range && this.viewSession) this.viewSession.origin = 'jump';
        this.$nextTick(() => this.configureVideoElement());
      }
    },
    activeSession: {
      immediate: true,
      handler(newSession, oldSession) {
        if (oldSession) { this.emitWatchProgress(true, false, oldSession?.video_id); this.resetVideoElement(); }
        this.previewMediaGeneration++;
        this.appliedSeekKey = '';
        this.seekPreview = null; this.seekSpriteUnavailable = false; this.clearSeekSpriteRetry();
        // 换了会话就是换了一次播放：报错状态清掉，重新开一个观看会话（D-PC43）。
        this.inlinePlaybackFailed = false;
        this.autoplayPending = this.autoplay;
        this.startViewSession(newSession);
        // 不能直放的会话：自动排代理。等到下一拍再发——挂载时 resetForVideo 还没跑，换片时 videoId 的监听可能排在后面。
        this.$nextTick(() => { this.configureVideoElement(); this.maybeRequestAutoProxy(); });
      }
    },
    startTimeMs(value) {
      // 播放中又指定了一次起播时刻（字幕命中等）：这一场从此按 jump 上报，只允许往前写断点（D-PC42）。
      if (value !== null && value !== undefined && this.viewSession) this.viewSession.origin = 'jump';
      this.appliedSeekKey = ''; this.$nextTick(() => this.configureVideoElement());
    },
    videoId() { this.resetForVideo(); }
  },
  created() {
    this._activeHandle = { pause: () => this.pause() };
    // 自动代理按宿主实例记账（后端 owner）：抽屉与整页详情看同一部时互不取消。
    this._proxyOwner = newPreviewProxyOwner('player');
    this._proxyOwnerSeq = 0;
    this._autoProxy = null;
  },
  mounted() {
    // 代理队列的排位与完成（D-PC26）：本项完成后自动重取预览会话。
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn('playback-proxy-state', status => this.applyProxyStatus(status, { fromEvent: true }));
      if (typeof off === 'function') this._proxyStateOff = off;
    }
    this.resetForVideo();
  },
  beforeUnmount() {
    this.clearSeekSpriteRetry(); this.emitWatchProgress(true, false); this.resetVideoElement();
    this.cancelAutoProxy();
    releaseActivePlayer(this._activeHandle);
    this._proxyStateOff?.(); this._proxyStateOff = null;
  },
  methods: {
    formatDuration,
    // ===== 宿主可调 =====
    play() { return this.$refs.videoElement?.play(); },
    pause() { this.$refs.videoElement?.pause(); },
    currentTime() { return Number(this.$refs.videoElement?.currentTime || 0); },
    scrollIntoView(options) { this.$refs.videoElement?.scrollIntoView?.(options); },
    isCurrentRequest(videoID, token) {
      return this._loadToken === token && Number(this.videoId) === Number(videoID);
    },
    // 换片（含初次挂载）：之前那部的代理请求与结果一律作废，重读这一部的代理与队列排位；不受控时重取会话。
    resetForVideo() {
      const videoID = Number(this.videoId);
      // 换了片：上一部的自动代理跟着预览走，取消掉。
      this.cancelAutoProxy();
      this._autoProxySuppressedFor = 0;
      const token = Symbol('inline-player-video');
      this._loadToken = token;
      this.playbackProxy = null; this.proxyError = ''; this.proxyNotice = ''; this.proxyBlocked = ''; this.proxyBusy = false; this.proxyTracking = null;
      if (!videoID) return;
      this.loadPlaybackProxy(videoID, token);
      this.loadProxyQueueState(videoID, token);
      if (!this.controlled) { this.ownSession = null; this.loadOwnSession(videoID, token, false); }
    },
    // 不受控时自己取会话。第一次取不到时与片库页同一个形状（unsupported），显示失败说明；
    // 代理增删之后的重取失败则保留原会话，只给出提示。说明里不带原始错误（G-3）。
    async loadOwnSession(videoID, token, refresh) {
      try {
        const session = await GetPreviewSession(videoID);
        if (this.isCurrentRequest(videoID, token)) this.ownSession = session;
      } catch (err) {
        console.error('读取预览会话失败:', err);
        if (!this.isCurrentRequest(videoID, token)) return;
        if (refresh) this.proxyError = '刷新预览会话失败，请稍后重试。';
        else this.ownSession = { video_id: videoID, mode: 'unsupported', reason_code: 'preview_failed', reason_message: '准备预览失败，请稍后重试。' };
      }
    },
    // 代理增删之后预览会话的 mode 会变（外部预览 <-> 内嵌）：受控时交给宿主重取，否则自己重取。
    async refreshSession(videoID, token) {
      if (this.controlled) {
        this.$emit('preview-session-stale', videoID);
        return;
      }
      if (this.isCurrentRequest(videoID, token)) await this.loadOwnSession(videoID, token, true);
    },
    // ===== 播放代理（D-004、D-006、D-PC26）=====
    // 生成是后台单 worker 的 FIFO 队列：CreatePlaybackProxy 只负责入队，立即返回，不等这一项跑完。
    // 排位与完成靠 playback-proxy-state 事件：本项在队列里显示「排队中（第 N 个）」、处理中显示「生成中」，
    // 本项有了结果再回读代理元数据；结果为可用时重取预览会话，自动切到内嵌播放。
    async loadPlaybackProxy(videoID, token) {
      try {
        const proxy = await GetPlaybackProxy(videoID);
        if (this.isCurrentRequest(videoID, token)) this.playbackProxy = proxy || null;
      } catch (err) {
        if (this.isCurrentRequest(videoID, token)) this.proxyError = `读取播放代理状态失败：${err}`;
      }
    },
    // 打开时这一项可能已经被别处（行菜单、批量、自动）排进队列：读一次当前快照补上排位，
    // 之后跟着事件走。只认排队与处理中，已有的旧结果不触发切换。
    async loadProxyQueueState(videoID, token) {
      try {
        const status = await GetPlaybackProxyStatus();
        if (this.isCurrentRequest(videoID, token)) this.applyProxyStatus(status, { fromEvent: true });
      } catch (err) {
        // 队列快照只用来显示排位，读不到时等下一次事件即可，不打扰用户。
      }
    },
    createPlaybackProxy() { return this.submitPlaybackProxy(CreatePlaybackProxy); },
    // 自动代理（2026-10-13「直接播 + 自动代理」）：会话为 proxy-preparing、或直放的源解码失败时请求。
    maybeRequestAutoProxy() {
      const session = this.activeSession;
      if (session?.mode === 'proxy-preparing' && Number(session.video_id) === Number(this.videoId)) this.requestAutoProxy();
    },
    // 每次请求带一个新的 owner 令牌（实例 id + 序号）。别的宿主已经排进去的一部，这里也照样登记自己的令牌（后端去重）：
    // 不登记的话别人一关就把它取消了。撤销只撤自己这一次的令牌，快速 A→B→A 时迟到的撤销碰不到新的登记。
    requestAutoProxy() {
      const videoID = Number(this.videoId);
      // 用户刚在这次预览里删掉了这一部的代理：不当场又生成一份（换片或重新打开后照常自动）。
      if (!videoID || this._autoProxySuppressedFor === videoID || this._autoProxy?.videoID === videoID) return;
      this._proxyOwnerSeq += 1;
      const owner = `${this._proxyOwner}#${this._proxyOwnerSeq}`;
      const token = this._loadToken;
      this.proxyError = ''; this.proxyNotice = ''; this.proxyBlocked = '';
      if (!this.proxyPending) this.proxyTracking = { videoID, state: { phase: 'submitting' } };
      const request = Promise.resolve()
        .then(() => RequestPreviewProxy(videoID, owner))
        .then(status => {
          if (this.isCurrentRequest(videoID, token)) this.applyProxyStatus(status || { running: false }, { fromEvent: false });
          return true;
        }, err => {
          if (this._autoProxy?.owner === owner) this._autoProxy = null;
          if (this.isCurrentRequest(videoID, token)) {
            // 后端拒绝（同时在等的已满、预计太大、没有 ffmpeg）是提示，不是失败；太大的可手动生成。
            const { notice, error, blocked } = proxyRequestErrorText(err);
            this.proxyNotice = notice; this.proxyError = error; this.proxyBlocked = blocked;
            if (this.proxyTracking?.state?.phase === 'submitting') this.proxyTracking = null;
          }
          return false;
        });
      this._autoProxy = { videoID, owner, request };
    },
    // 换片或卸载：等这次请求回来再撤销自己的令牌（请求没登记成功就不撤）。
    cancelAutoProxy() {
      const auto = this._autoProxy;
      if (!auto) return;
      this._autoProxy = null;
      auto.request
        .then(registered => (registered ? CancelPreviewProxy(auto.videoID, auto.owner) : null))
        .catch(err => console.warn('取消自动播放代理失败:', err));
    },
    // 入队（手动 CreatePlaybackProxy 或自动 RequestPreviewProxy）只负责排进队列、立即返回，进度跟着事件走。
    async submitPlaybackProxy(request) {
      if (this.proxyBusy || this.proxyPending || !Number(this.videoId)) return;
      const videoID = Number(this.videoId); const token = this._loadToken;
      this.proxyBusy = true; this.proxyError = ''; this.proxyNotice = '';
      this.proxyTracking = { videoID, state: { phase: 'submitting' } };
      let status = null;
      let enqueued = false;
      try {
        status = await request(videoID);
        enqueued = true;
      } catch (err) {
        if (this.isCurrentRequest(videoID, token)) {
          // 被「同时最多 2 部」拒绝是提示，不是失败；可以手动生成。
          const { notice, error } = proxyRequestErrorText(err);
          this.proxyNotice = notice; this.proxyError = error;
        }
        if (this.proxyTracking?.videoID === videoID) this.proxyTracking = null;
      } finally {
        if (this.isCurrentRequest(videoID, token)) this.proxyBusy = false;
      }
      // 入队的回执本身就是一份快照：已经在排队就接着等，已经有结果（或快照里没有这一项）就当场收尾。
      if (enqueued && this.isCurrentRequest(videoID, token)) this.applyProxyStatus(status || { running: false }, { fromEvent: false });
    },
    // 按一份代理状态快照推进本片的那一项（D-PC26、PLAY-04）。排队与处理中只更新显示；
    // 只有正在等的那一项（proxyTracking）有了结果、或整轮结束时，才收尾一次。
    // 入队请求还没返回时，事件里的空白或旧结果都不算数（那一刻队列里还没有这一项）。
    applyProxyStatus(status, { fromEvent = true } = {}) {
      const videoID = Number(this.videoId);
      if (!status || !videoID) return;
      const tracking = this.proxyTracking?.videoID === videoID ? this.proxyTracking : null;
      const next = advanceProxyTracking(tracking, status, videoID, { fromEvent });
      this.proxyTracking = next.tracking;
      if (next.finished) this.finishProxyTask(videoID, this._loadToken, next.finished);
    },
    async finishProxyTask(videoID, token, state) {
      // 这一项有了结果：自动代理已经不在队列里，关预览时不必再取消。
      if (this._autoProxy?.videoID === videoID) this._autoProxy = null;
      const outcome = state.phase === 'done' ? state.outcome : '';
      if (outcome === 'failed') {
        this.proxyError = `生成播放代理失败：${PLAYBACK_PROXY_CODE_LABELS[state.result.code] || state.result.code}`;
      } else if (outcome === 'cancelled') {
        this.proxyNotice = '播放代理任务已取消。';
      }
      await this.loadPlaybackProxy(videoID, token);
      if (!this.isCurrentRequest(videoID, token)) return;
      // 结果里有这一项就以结果为准；整轮结束却没有这一项的结果（结果条数有上限），以回读的代理状态为准。
      const ready = outcome ? outcome === 'ready' : this.playbackProxy?.status === 'ready';
      if (ready) await this.refreshSession(videoID, token);
    },
    async deletePlaybackProxy() {
      if (this.proxyBusy || !Number(this.videoId)) return;
      if (!await confirmAction({ title: '删除播放代理', message: '删除这份播放代理？源文件不受影响，需要时可以重新生成。', confirmText: '删除', danger: true })) return;
      const videoID = Number(this.videoId); const token = this._loadToken;
      this.proxyBusy = true; this.proxyError = '';
      this._autoProxySuppressedFor = videoID;
      try {
        await DeletePlaybackProxy(videoID);
      } catch (err) {
        if (this.isCurrentRequest(videoID, token)) this.proxyError = `删除播放代理失败：${err}`;
      } finally {
        if (this.isCurrentRequest(videoID, token)) this.proxyBusy = false;
        await this.loadPlaybackProxy(videoID, token);
        await this.refreshSession(videoID, token);
      }
    },
    // ===== 起播位置 =====
    handleLoadedMetadata() { this.configureVideoElement(); this.maybeAutoplay(); },
    configureVideoElement() { const video = this.$refs.videoElement; if (!video) return; video.defaultMuted = true; video.muted = true; this.applyStartTime(video); },
    // 起播参数（PLAY-10）：指定时刻优先，否则用宿主按 watchState 算好的续播断点；与 detailPlaybackStartMs 同一套判断。
    playbackStartOptions() {
      const videoID = Number(this.videoId);
      return { entryID: videoID, rootVideoID: videoID, explicitStartTimeMs: this.startTimeMs, rootResumePositionSeconds: this.resumePositionSeconds };
    },
    applyStartTime(video) {
      const bookmark = this.readyBookmark;
      if (this.bookmarkSourceMismatch) return;
      const startTimeMs = bookmark ? bookmark.startMS : detailPlaybackStartMs(this.playbackStartOptions());
      if (video.readyState < 1 || (startTimeMs === 0 && !bookmark)) return;
      let seekSeconds = startTimeMs / 1000;
      if (Number.isFinite(video.duration) && video.duration > 0) seekSeconds = Math.min(seekSeconds, Math.max(video.duration - 0.001, 0));
      const seekKey = `${this.activeSession?.video_id || ''}:${seekSeconds}:${bookmark?.requestID || ''}`;
      if (seekKey === this.appliedSeekKey) return;
      video.currentTime = seekSeconds; this.appliedSeekKey = seekKey;
    },
    // autoplay：每个会话在读到元数据、定好位置之后自动开始一次；被拦下或被换源打断时停在可手动播放的状态。
    maybeAutoplay() {
      const video = this.$refs.videoElement;
      if (!this.autoplayPending || !video) return;
      this.autoplayPending = false;
      Promise.resolve(video.play()).catch(err => console.warn('自动播放没有开始:', err));
    },
    // ===== 观看会话（D-PC43、PLAY-07）=====
    // 每次打开内嵌播放器是一个会话：会话标识每次新生成，累计播放首次越过 viewThreshold、或判定看完时
    // 调一次 RecordViewEvent(inline_view)。起播来源（D-PC42）在第一次开始播放时定下，整场播放不变。
    startViewSession(session) {
      const inline = session?.mode === 'inline' && session?.inline_source;
      const videoID = Number(session?.video_id || this.videoId || 0);
      this.viewSession = inline && videoID
        ? markRaw({ id: newViewSessionID(), videoID, origin: '', accumulator: createPlaybackAccumulator(), recorded: false })
        : null;
    },
    playerDurationSeconds(video = this.$refs.videoElement) {
      const duration = Number(video?.duration);
      return Number.isFinite(duration) && duration > 0 ? duration : 0;
    },
    handleEnded() {
      this.emitWatchProgress(true, true);
      if (this.viewSession) this.viewSession.ended = true;
    },
    handlePlay() {
      claimActivePlayer(this._activeHandle);
      if (this.viewSession?.ended) this.startViewSession(this.activeSession);
      this.hasPlaybackStarted = true;
      const session = this.viewSession;
      if (!session) return;
      if (!session.origin) session.origin = detailPlaybackOrigin(this.playbackStartOptions());
      session.accumulator.breakSegment();
    },
    // 暂停与拖动都断开当前一段累计：之后的一大步不能被墙钟放行成「播放」。
    handlePause() {
      releaseActivePlayer(this._activeHandle);
      this.viewSession?.accumulator.breakSegment();
      this.emitWatchProgress(true, false);
    },
    handleSeeking() { this.viewSession?.accumulator.breakSegment(); },
    handleTimeUpdate() {
      const video = this.$refs.videoElement;
      if (video && !video.paused && !video.seeking) this.viewSession?.accumulator.sample(video.currentTime, Date.now(), video.playbackRate);
      if (video && this.readyBookmark && this.bookmarkRangeActive && Number(video.currentTime) * 1000 >= this.readyBookmark.endMS) {
        this.bookmarkRangeActive = false;
        this.$emit('bookmark-range-end', this.readyBookmark);
        video.pause();
      }
      this.maybeRecordView(false);
      this.emitWatchProgress(false, false);
    },
    maybeRecordView(completedHint) {
      const session = this.viewSession;
      if (!session || session.recorded || (!this.hasPlaybackStarted && !completedHint)) return;
      const video = this.$refs.videoElement;
      const libraryDuration = Number(this.durationSeconds) > 0 ? Number(this.durationSeconds) : 0;
      const duration = this.playerDurationSeconds(video) || libraryDuration;
      const completed = !!completedHint || isWatchCompleted(Number(video?.currentTime || 0), duration);
      if (!completed && session.accumulator.seconds < viewThreshold(duration)) return;
      session.recorded = true;
      // 只写一次；失败不重试，后端也不会把失败的这次记进去重表。
      Promise.resolve()
        .then(() => RecordViewEvent(session.videoID, 'inline_view', session.id))
        .catch(err => console.error('记录有效观看失败:', err));
    },
    emitWatchProgress(force, completed, videoID = null) {
      if (this.resettingVideo) return;
      this.maybeRecordView(completed);
      if (!this.hasPlaybackStarted && !completed) return; const video = this.$refs.videoElement; const positionSeconds = Number(video?.currentTime || 0);
      if (!Number.isFinite(positionSeconds) || positionSeconds <= 0) return; const now = Date.now(); if (!force && now - this.lastProgressEmittedAt < 10000) return;
      this.lastProgressEmittedAt = now;
      // 起播来源（D-PC42）：从断点 resume、从片头 start、从字幕命中或指定时间 jump；时长未知传 0，
      // 后端只在库内时长未知时采用它。
      const origin = this.viewSession?.origin || detailPlaybackOrigin(this.playbackStartOptions());
      this.$emit('watch-progress', {
        videoID: Number(videoID || this.viewSession?.videoID || this.activeSession?.video_id || this.videoId || 0),
        positionSeconds,
        completed: !!completed,
        origin,
        durationSeconds: this.playerDurationSeconds(video)
      });
    },
    // 内嵌播放器报错（PLAY-05）：<video> 自己的 error 带 MediaError（解码失败、源不支持）；
    // <source> 的 error 只在它还带着地址时才算数——换会话时清掉地址再 load() 也会触发一次。
    handleVideoError(event) {
      if (this.resettingVideo || !event?.target?.error) return;
      this.markInlinePlaybackFailed();
    },
    handleSourceError(event) {
      if (this.resettingVideo || !event?.target?.getAttribute?.('src')) return;
      this.markInlinePlaybackFailed();
    },
    // 直放的源解码失败（2026-10-13）：两个按钮照留，同时自动排一项代理；已经在经代理播放就不再生成。
    markInlinePlaybackFailed() {
      if (this.inlinePlaybackFailed) return;
      this.inlinePlaybackFailed = true;
      if (!this.playingThroughProxy) this.requestAutoProxy();
    },
    resetVideoElement() {
      const video = this.$refs.videoElement; if (!video) return; this.resettingVideo = true;
      try { video.pause(); } catch (err) {} try { video.currentTime = 0; } catch (err) {}
      video.defaultMuted = true; video.muted = true; this.appliedSeekKey = ''; video.removeAttribute('src'); const source = video.querySelector('source'); if (source) source.removeAttribute('src'); video.load();
      this.lastProgressEmittedAt = 0; this.hasPlaybackStarted = false; this.seekPreview = null; this.resettingVideo = false;
    },
    // ===== 悬停进度条的 seek 预览图 =====
    handleSeekPointerMove(event) {
      if (!this.activeSeekSprite) return;
      const bounds = event.currentTarget?.getBoundingClientRect?.();
      if (!bounds || bounds.width <= 0) return;
      const ratio = Math.min(Math.max((Number(event.clientX) - bounds.left) / bounds.width, 0), 0.999999);
      const videoDuration = Number(this.$refs.videoElement?.duration);
      const libraryDuration = Number(this.durationSeconds);
      const duration = Number.isFinite(videoDuration) && videoDuration > 0 ? videoDuration : libraryDuration;
      if (!Number.isFinite(duration) || duration <= 0) return;
      const interval = Number(this.activeSeekSprite.interval_seconds);
      const frameCount = Number(this.activeSeekSprite.frame_count);
      const seconds = ratio * duration;
      const frameIndex = Math.min(Math.floor(seconds / interval), frameCount - 1);
      this.seekPreview = {
        frameIndex,
        seconds,
        clampedPercent: Math.min(Math.max(ratio * 100, 18), 82)
      };
    },
    handleSeekSpriteError() {
      // sprite 现在在后台异步生成，404 通常表示"尚未就绪"而非永久失败：
      // 有限次数地稍后重试，重试串上参数避免命中已缓存的失败响应。
      this.seekPreview = null;
      this.seekSpriteUnavailable = true;
      if (this.seekSpriteRetryCount >= 15 || this.seekSpriteRetryTimer) return;
      this.seekSpriteRetryTimer = setTimeout(() => {
        this.seekSpriteRetryTimer = null;
        this.seekSpriteRetryCount += 1;
        this.seekSpriteUnavailable = false;
      }, 4000);
    },
    clearSeekSpriteRetry() {
      if (this.seekSpriteRetryTimer) {
        clearTimeout(this.seekSpriteRetryTimer);
        this.seekSpriteRetryTimer = null;
      }
      this.seekSpriteRetryCount = 0;
    }
  }
};
</script>

<style scoped>
/* 根是一层网格：放进固定最小高度的容器（抽屉的播放器区块）时，占位框与以前一样撑满整块。 */
.inline-player { display: grid; }
.preview-drawer__player-shell { position: relative; width: 100%; aspect-ratio: 16 / 9; min-height: 220px; background: var(--player-bg); border-radius: 0; overflow: hidden; }.preview-drawer__video { position: absolute; inset: 0; width: 100%; height: 100%; min-height: 0; display: block; object-fit: contain; background: var(--player-bg); }
.preview-drawer__seek-track { position: absolute; z-index: 2; right: 12px; bottom: 42px; left: 12px; height: 12px; border-radius: 999px; background: color-mix(in srgb, var(--text-primary) 28%, transparent); cursor: default; }
.preview-drawer__seek-track::after { position: absolute; inset: 4px 0; border-radius: inherit; background: color-mix(in srgb, var(--text-primary) 58%, transparent); content: ''; }
.preview-drawer__seek-preview { position: absolute; bottom: 18px; width: 160px; transform: translateX(-50%); display: grid; gap: 4px; justify-items: center; pointer-events: none; }
.preview-drawer__seek-image { position: relative; width: 160px; height: 90px; overflow: hidden; border: 2px solid rgba(255, 255, 255, .9); border-radius: 8px; background: var(--player-bg); box-shadow: 0 6px 20px rgba(0, 0, 0, .45); }
.preview-drawer__seek-image img { position: absolute; max-width: none; object-fit: fill; }
.preview-drawer__seek-preview time { padding: 2px 6px; border-radius: 5px; color: #fff; background: rgba(0, 0, 0, .78); font-size: 11px; font-variant-numeric: tabular-nums; }
/* 下面两条与抽屉里的同名规则一致：抽屉自己的「正在读取」占位和动作行仍用它们。 */
.preview-drawer__placeholder { min-height: 150px; border: 1px dashed var(--border-color); border-radius: 14px; padding: 22px; display: flex; flex-direction: column; justify-content: center; gap: 12px; color: var(--text-secondary); }
.detail-action-row { display: flex; gap: 8px; flex-wrap: wrap; }
.preview-drawer__proxy-progress { margin: 0; color: var(--text-secondary); font-size: 12px; }
</style>
