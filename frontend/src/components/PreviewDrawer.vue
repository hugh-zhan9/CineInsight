<template>
  <aside class="preview-drawer" role="dialog" aria-label="视频预览抽屉">
    <div class="preview-drawer__header">
      <div>
        <p class="preview-drawer__eyebrow">Preview</p>
        <h3>{{ video ? video.name : '视频预览' }}</h3>
      </div>
      <button type="button" class="preview-drawer__close" @click="$emit('close')">关闭</button>
    </div>

    <div class="preview-drawer__body">
      <div v-if="!session" class="preview-drawer__placeholder">
        正在准备预览...
      </div>

      <template v-else-if="session.mode === 'inline' && session.inline_source">
        <div class="preview-drawer__player-shell">
          <video
            ref="videoElement"
            class="preview-drawer__video"
            controls
            playsinline
            preload="metadata"
            :muted="true"
            @loadedmetadata="handleLoadedMetadata"
          >
            <source :src="session.inline_source.locator_value" :type="session.inline_source.mime" />
          </video>
        </div>
        <p class="preview-drawer__hint">
          预览默认静音，可使用播放器控件开启声音。关闭抽屉后会停止并重置，不计入正式播放统计。
        </p>
      </template>

      <template v-else-if="session.mode === 'external-preview' && session.external_action">
        <div class="preview-drawer__placeholder">
          <p class="preview-drawer__message">{{ session.reason_message }}</p>
          <p class="preview-drawer__hint">{{ session.external_action.hint }}</p>
          <button type="button" class="btn-primary" @click="$emit('preview-externally', video)">
            {{ session.external_action.button_label }}
          </button>
        </div>
      </template>

      <template v-else>
        <div class="preview-drawer__placeholder">
          <p class="preview-drawer__message">{{ session.reason_message || '当前视频暂不支持预览。' }}</p>
        </div>
      </template>
    </div>
  </aside>
</template>

<script>
export default {
  name: 'PreviewDrawer',
  props: {
    video: { type: Object, default: null },
    session: { type: Object, default: null }
  },
  emits: ['close', 'preview-externally'],
  watch: {
    session: {
      immediate: true,
      handler(newSession, oldSession) {
        if (oldSession) {
          this.resetVideoElement();
        }
        this.$nextTick(() => {
          this.configureVideoElement();
        });
      }
    }
  },
  beforeUnmount() {
    this.resetVideoElement();
  },
  methods: {
    handleLoadedMetadata() {
      this.configureVideoElement();
    },
    configureVideoElement() {
      const video = this.$refs.videoElement;
      if (!video) return;
      video.defaultMuted = true;
      video.muted = true;
    },
    resetVideoElement() {
      const video = this.$refs.videoElement;
      if (!video) return;
      try {
        video.pause();
      } catch (err) {}
      try {
        video.currentTime = 0;
      } catch (err) {}
      video.defaultMuted = true;
      video.muted = true;
      video.removeAttribute('src');
      const source = video.querySelector('source');
      if (source) {
        source.removeAttribute('src');
      }
      video.load();
    }
  }
};
</script>

<style scoped>
.preview-drawer {
  position: fixed;
  top: 60px;
  right: 0;
  bottom: 0;
  width: min(420px, 38vw);
  min-width: 320px;
  background: var(--panel-bg);
  border-left: 1px solid var(--border-color);
  box-shadow: -16px 0 40px rgba(15, 23, 42, 0.16);
  display: flex;
  flex-direction: column;
  z-index: 140;
}

.preview-drawer__header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  padding: 18px 20px;
  border-bottom: 1px solid var(--border-color);
}

.preview-drawer__eyebrow {
  margin: 0 0 6px;
  font-size: 11px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--text-muted);
}

.preview-drawer__header h3 {
  font-size: 16px;
  line-height: 1.4;
}

.preview-drawer__close {
  border: 1px solid var(--border-color);
  background: transparent;
  color: var(--text-primary);
  border-radius: 999px;
  padding: 6px 12px;
  cursor: pointer;
}

.preview-drawer__body {
  flex: 1;
  overflow-y: auto;
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.preview-drawer__player-shell {
  background: #020617;
  border-radius: 14px;
  overflow: hidden;
  min-height: 220px;
}

.preview-drawer__video {
  width: 100%;
  display: block;
  background: #020617;
}

.preview-drawer__placeholder {
  min-height: 220px;
  border: 1px dashed var(--border-color);
  border-radius: 14px;
  padding: 24px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 12px;
  color: var(--text-secondary);
}

.preview-drawer__message {
  color: var(--text-primary);
  line-height: 1.6;
}

.preview-drawer__hint {
  font-size: 13px;
  line-height: 1.6;
  color: var(--text-secondary);
}

@media (max-width: 1180px) {
  .preview-drawer {
    width: min(460px, 52vw);
  }
}

@media (max-width: 860px) {
  .preview-drawer {
    width: 100vw;
    min-width: 0;
  }
}
</style>
