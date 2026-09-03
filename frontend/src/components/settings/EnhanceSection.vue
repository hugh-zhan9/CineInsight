<template>
  <div :id="`settings-enhance`" class="settings-section">
    <h3>视频超分</h3>
    <!-- 模型不随应用打包（约 35MB 权重要从上游下载），用不上的人不必为它付出体积。
         二进制随包，所以这里只是一次下载，不需要装任何开发工具。 -->
    <div class="short-feed-status">
      <div class="short-feed-status-main">
        <strong>{{ enhanceStatusText }}</strong>
        <span>{{ enhanceStatusDetail }}</span>
      </div>
      <div class="short-feed-actions">
        <button
          v-if="enhanceModelStatus && enhanceModelStatus.running"
          type="button"
          class="btn-secondary"
          data-test="enhance-model-cancel"
          @click="cancelEnhanceModels"
        >取消下载</button>
        <button
          v-else-if="enhanceCapability && enhanceCapability.models_installable"
          type="button"
          class="btn-primary"
          data-test="enhance-model-download"
          @click="downloadEnhanceModels"
        >{{ enhanceCapability.reason_code === 'models_corrupt' ? '重新下载模型' : '下载模型（约 52 MB）' }}</button>
      </div>
    </div>
    <div v-if="enhanceModelStatus && enhanceModelStatus.running" class="enhance-progress" data-test="enhance-model-progress">
      <div class="enhance-progress__bar"><i :style="{ width: `${enhanceDownloadPercent}%` }"></i></div>
      <span>{{ enhanceModelStatus.message }} {{ enhanceDownloadPercent }}%</span>
    </div>
    <p v-if="enhanceModelStatus && enhanceModelStatus.error" class="help-text settings-error" data-test="enhance-model-error">
      {{ enhanceModelStatus.error }}
    </p>
    <p class="help-text">超分只支持 Apple Silicon，模型装在 {{ enhanceModelStatus?.install_dir || '用户数据目录' }}；删掉该目录即可回到未下载状态。</p>
  </div>
</template>

<script>
import { GetEnhancementCapability, GetEnhancementModelStatus, StartEnhancementModelDownload, CancelEnhancementModelDownload } from '../../../wailsjs/go/main/App';
import { notifyError } from '../../utils/feedback.js';

// 视频超分分区：能力探测、模型按需下载与下载进度。状态与两条运行时事件都归本组件。
export default {
  name: 'EnhanceSection',
  data() {
    return {
      enhanceCapability: null,
      enhanceModelStatus: null,
      enhanceModelStatusOff: null,
      enhanceCapabilityOff: null
    };
  },
  mounted() {
    this.loadEnhanceStatus();
    if (window.runtime?.EventsOn) {
      const enhanceOff = window.runtime.EventsOn('enhancement-model-state', (status) => {
        this.enhanceModelStatus = status || null;
      });
      if (typeof enhanceOff === 'function') this.enhanceModelStatusOff = enhanceOff;
      // 模型装好后端会重探能力并广播，这里直接换上，不用重启也不用手动刷新。
      const capabilityOff = window.runtime.EventsOn('video-enhancement-capability', (capability) => {
        this.enhanceCapability = capability || null;
      });
      if (typeof capabilityOff === 'function') this.enhanceCapabilityOff = capabilityOff;
    }
  },
  beforeUnmount() {
    if (this.enhanceModelStatusOff) this.enhanceModelStatusOff();
    if (this.enhanceCapabilityOff) this.enhanceCapabilityOff();
  },
  computed: {
    enhanceStatusText() {
      if (!this.enhanceCapability) return '正在检查…';
      if (this.enhanceCapability.available) return '已就绪';
      if (this.enhanceModelStatus?.running) return '正在准备';
      return '未就绪';
    },
    enhanceStatusDetail() {
      if (!this.enhanceCapability) return '';
      if (this.enhanceCapability.available) {
        return `运行时 ${this.enhanceCapability.runtime_version}，模型已就绪，可在视频的 ⋯ 菜单里发起超分。`;
      }
      return this.enhanceCapability.message || '超分不可用';
    },
    enhanceDownloadPercent() {
      const total = Number(this.enhanceModelStatus?.total_bytes || 0);
      const done = Number(this.enhanceModelStatus?.downloaded_bytes || 0);
      if (total <= 0) return 0;
      return Math.min(100, Math.round((done / total) * 100));
    },
    // 手机要输的地址：后端已经把默认路由那块网卡排在 lan_urls 首位。
  },
  methods: {
    async loadEnhanceStatus() {
      try {
        this.enhanceCapability = await GetEnhancementCapability();
        this.enhanceModelStatus = await GetEnhancementModelStatus();
      } catch (err) {
        this.enhanceCapability = { available: false, message: String(err), models_installable: false };
      }
    },
    async downloadEnhanceModels() {
      try {
        this.enhanceModelStatus = await StartEnhancementModelDownload();
      } catch (err) {
        notifyError(`下载超分模型失败：${err}`);
      }
    },
    async cancelEnhanceModels() {
      try {
        this.enhanceModelStatus = await CancelEnhancementModelDownload();
      } catch (err) {
        notifyError(`取消下载失败：${err}`);
      }
    },
  }
};
</script>

<style scoped>
.enhance-progress { display: grid; gap: 6px; margin-top: 10px; color: var(--text-secondary); font-size: 12px; }
.enhance-progress__bar { height: 6px; border-radius: 3px; background: var(--control-bg); overflow: hidden; }
.enhance-progress__bar i { display: block; height: 100%; background: var(--accent-color); transition: width var(--transition); }
</style>
