// 节奏归ShortFeedApp；媒体准备情况只向FeedStage查询，不另存媒体或列表。
export default {
  data: () => ({ autoBrowse: false, autoPaused: false, autoRemaining: 5000, autoReady: false, autoEnded: false, autoWaiting: false, autoError: '' }),
  computed: {
    autoBlocked() { return !this.autoBrowse || this.autoPaused || !!this.sheet || this.photoZoomed || this.seeking || this.authRequired || this.view === 'favorites'; },
    autoStatus() {
      if (this.autoError) return this.autoError;
      if (this.autoBlocked) return '已暂停';
      if (this.autoWaiting) return '正在准备下一项…';
      return this.isImageItem ? (this.autoReady ? `${Math.ceil(this.autoRemaining / 1000)} 秒后下一项` : '正在加载图片…') : '播完自动下一项';
    }
  },
  watch: {
    autoBlocked() { this.syncAutoClock(); },
    stagePrefetched() { if (this.autoWaiting) this.advanceAutomatically(); }
  },
  beforeUnmount() { this.stopAutoClock(); this._autoGeneration = (this._autoGeneration || 0) + 1; },
  methods: {
    stopAutoClock() { clearInterval(this._autoClock); this._autoClock = null; },
    syncAutoClock() {
      this.stopAutoClock();
      if (this.autoBlocked || document.hidden) return;
      this._autoLastTick = performance.now();
      this._autoClock = setInterval(() => {
        const now = performance.now(), elapsed = now - this._autoLastTick; this._autoLastTick = now;
        if (this.autoBlocked || document.hidden) return;
        if (this.autoReady && this.isImageItem) this.autoRemaining = Math.max(0, this.autoRemaining - elapsed);
        if ((this.isImageItem && this.autoReady && this.autoRemaining <= 0) || this.autoEnded) this.advanceAutomatically();
      }, 100);
    },
    resetAutoItem() {
      this._autoGeneration = (this._autoGeneration || 0) + 1;
      this.autoRemaining = 5000; this.autoReady = false; this.autoEnded = false; this.autoWaiting = false; this.autoError = '';
      this.syncAutoClock();
    },
    toggleAutoBrowse() {
      this.autoBrowse = !this.autoBrowse; this.autoPaused = false; this.autoError = ''; this.autoWaiting = false;
      this.autoRemaining = 5000; this.autoEnded = !!this.player()?.ended;
      this.autoReady = this.$refs.stage?.state(this.currentVideo) === 'ready';
      this.syncAutoClock();
    },
    toggleAutoPause() {
      this.autoPaused = !this.autoPaused;
      if (this.autoPaused) this.player()?.pause();
      else if (!this.isImageItem && !this.autoEnded) this.player()?.play()?.catch(() => this.failAuto('播放未开始，点继续重试'));
      this.syncAutoClock();
      if (!this.autoPaused && this.autoWaiting) this.advanceAutomatically();
    },
    onActiveReady() { this.autoReady = true; this._autoLastTick = performance.now(); },
    onAutoPrepared() { if (this.autoWaiting) this.advanceAutomatically(); },
    onVideoEnded() { if (this.autoBrowse) { this.prefetchPreload = 'auto'; this.autoEnded = true; this.advanceAutomatically(); } },
    failAuto(message) { this.autoError = message; this.autoPaused = true; this.autoWaiting = false; this.stopAutoClock(); },
    retryAuto() {
      this.autoError = ''; this.autoPaused = false; this.autoRemaining = this.autoReady ? this.autoRemaining : 5000;
      const target = this.$refs.stage?.state(this.currentVideo) === 'error' ? this.currentVideo : this.stagePrefetched;
      if (target) this.$refs.stage?.retry(target);
      if (!this.isImageItem && !this.autoEnded && this.player()?.paused) this.player().play().catch(() => this.failAuto('播放未开始，请重试'));
      this.syncAutoClock();
    },
    async advanceAutomatically() {
      if (this.autoBlocked || document.hidden || this._autoAdvancing) return;
      const generation = this._autoGeneration;
      this._autoAdvancing = true; this.autoWaiting = true;
      try {
        if (!this.stagePrefetched && !this.inFavoriteStream) await this.prefetchNextVideo();
        if (generation !== this._autoGeneration || this.autoBlocked || document.hidden) return;
        const next = this.stagePrefetched;
        if (!next) { this.failAuto(this.inFavoriteStream ? '已到最后一项' : '下一项未能加载，请重试'); return; }
        if (!next.media_url || this.$refs.stage?.state(next) === 'error') { this.failAuto('下一项加载失败，请重试或手动切换'); return; }
        const state = this.$refs.stage?.state(next);
        if (state !== 'ready') return;
        await this.nextVideo(1);
      } finally { this._autoAdvancing = false; }
    }
  }
};
