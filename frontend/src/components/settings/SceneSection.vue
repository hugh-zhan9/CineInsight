<template>
  <div id="settings-scenes" class="settings-section">
    <h3>场景检索</h3>

    <p class="help-text scene-privacy" data-test="scene-privacy-note">
      「准备模型」会联网：从 PyPI 安装 Python 依赖、必要时下载一份托管 Python 运行时，并从
      Hugging Face（或下面填写的镜像主机）下载 Chinese-CLIP 模型文件（逐个校验 sha256）。准备完成后，
      本地画面索引与检索全程离线，采样帧只写入 {{ runtimeStatus?.runtime_dir || '应用数据目录' }} 下的临时目录、
      用完即删，画面向量只存在本机数据库里。
    </p>

    <div class="short-feed-status">
      <div class="short-feed-status-main">
        <strong data-test="scene-runtime-state">{{ runtimeStateText }}</strong>
        <span data-test="scene-runtime-reason">{{ runtimeReasonText }}</span>
      </div>
      <div class="short-feed-actions">
        <button v-if="runtimeStatus && runtimeStatus.preparing" type="button" class="btn-secondary" data-test="scene-cancel-prepare" @click="cancelPrepare">取消准备</button>
        <button v-else-if="runtimeCanPrepare" type="button" class="btn-primary" data-test="scene-prepare-runtime" @click="prepareRuntime">
          准备模型（约 {{ runtimeStatus?.model_size || '182 MB' }}）
        </button>
      </div>
    </div>
    <div v-if="runtimeStatus && runtimeStatus.preparing" class="scene-progress" data-test="scene-runtime-progress">
      <div class="scene-progress__bar"><i :style="{ width: `${preparePercent}%` }"></i></div>
      <span>{{ runtimeStatus.message }} {{ preparePercent }}%</span>
    </div>
    <p v-if="runtimeStatus && runtimeStatus.error" class="help-text scene-error" data-test="scene-runtime-error">{{ runtimeStatus.error }}</p>

    <div class="setting-item">
      <label>模型镜像主机</label>
      <input data-test="scene-mirror-url" type="text" class="text-input" placeholder="https://hf-mirror.com" v-model="form.scene_model_mirror_url" />
      <p class="help-text">留空时从 huggingface.co 下载；直连不通时填镜像主机，路径与固定提交不变，sha256 照样校验。</p>
    </div>

    <div class="setting-item">
      <label>画面采样间隔（秒）</label>
      <input data-test="scene-interval" type="number" class="text-input scene-interval" min="2" max="30" step="1" v-model.number="form.scene_visual_interval_seconds" />
      <p class="help-text">2–30 秒，默认 5。间隔越小越精细、建索引越慢；改了之后已建的画面索引不再参与检索，需要重新建立。</p>
    </div>

    <div class="setting-item">
      <label>画面检索提供方</label>
      <div class="scene-provider" role="radiogroup">
        <label class="scene-provider__option">
          <input ref="localRadio" type="radio" data-test="scene-provider-local" value="local" :checked="form.scene_visual_provider !== 'external'" @change="chooseProvider('local')" />
          本地模型（默认，不外发）
        </label>
        <label class="scene-provider__option">
          <input ref="externalRadio" type="radio" data-test="scene-provider-external" value="external" :checked="form.scene_visual_provider === 'external'" @change="chooseProvider('external')" />
          外部描述（使用「AI 标签」设置的接口）
        </label>
      </div>
      <p v-if="form.scene_visual_provider === 'external'" class="help-text scene-external-note" data-test="scene-external-disclosure">
        已选择外部描述：建立画面索引时，会把每个视频的采样帧缩略图（224×224）分批发送到「AI 标签」设置里的接口地址与模型，
        由它生成中文描述后按字面检索。本地模型不可用时也不会自动改用外部，反之亦然。
      </p>
    </div>
  </div>
</template>

<script>
import { CancelSceneRuntimePrepare, GetSceneRuntimeStatus, PrepareSceneRuntime } from '../../../wailsjs/go/main/App';
import { confirmAction, notifyError } from '../../utils/feedback.js';
import { sceneErrorText } from '../../utils/sceneSearch.js';

const RUNTIME_STATE_TEXT = {
  available: '已就绪',
  missing_python: '未就绪',
  missing_venv: '未就绪',
  missing_model: '未就绪',
  download_failed: '下载失败',
  incompatible: '当前平台不支持'
};

// 设置页「场景检索」分区（D-MW-SCENES）：本地运行时状态与准备、模型镜像、采样间隔、提供方。
// 切到外部描述前必须先确认披露；不确认就保持本地，表单里不会出现一个没被确认过的 external。
export default {
  name: 'SceneSection',
  props: {
    form: { type: Object, required: true },
    ensureSaved: { type: Function, default: null }
  },
  data() {
    return { runtimeStatus: null };
  },
  computed: {
    runtimeCanPrepare() {
      return ['missing_python', 'missing_venv', 'missing_model', 'download_failed'].includes(this.runtimeStatus?.state);
    },
    runtimeStateText() {
      if (!this.runtimeStatus) return '正在检查…';
      if (this.runtimeStatus.preparing) return '正在准备';
      return RUNTIME_STATE_TEXT[this.runtimeStatus.state] || '未就绪';
    },
    runtimeReasonText() {
      if (!this.runtimeStatus) return '';
      if (this.runtimeStatus.state === 'available') return `本地模型 ${this.runtimeStatus.model_id} 已就绪。`;
      return this.runtimeStatus.reason || '';
    },
    preparePercent() {
      const total = Number(this.runtimeStatus?.total_bytes || 0);
      if (total <= 0) return 0;
      return Math.min(100, Math.round((Number(this.runtimeStatus?.downloaded_bytes || 0) / total) * 100));
    }
  },
  mounted() {
    this.loadRuntimeStatus();
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn('scene-runtime-state', status => {
        if (status) this.runtimeStatus = status;
      });
      if (typeof off === 'function') this.runtimeOff = off;
    }
  },
  beforeUnmount() {
    this.runtimeOff?.();
  },
  methods: {
    async loadRuntimeStatus() {
      try {
        this.runtimeStatus = await GetSceneRuntimeStatus();
      } catch (err) {
        this.runtimeStatus = { state: 'incompatible', reason: String(err) };
      }
    },
    async prepareRuntime() {
      // 准备按已保存的镜像主机下载（D-PC57）：改了没保存就先提示保存。
      if (typeof this.ensureSaved === 'function' && !await this.ensureSaved(['scene_model_mirror_url'], '准备模型')) return;
      try {
        this.runtimeStatus = await PrepareSceneRuntime();
      } catch (err) {
        notifyError(`准备场景检索模型失败：${sceneErrorText(err)}`);
      }
    },
    async cancelPrepare() {
      try {
        await CancelSceneRuntimePrepare();
      } catch (err) {
        notifyError(`取消准备失败：${sceneErrorText(err)}`);
      }
      await this.loadRuntimeStatus();
    },
    async chooseProvider(provider) {
      if (provider !== 'external') {
        this.form.scene_visual_provider = 'local';
        return;
      }
      if (this.form.scene_visual_provider === 'external') return;
      const confirmed = await confirmAction({
        title: '启用外部描述',
        message: '建立画面索引时，会把采样帧缩略图（224×224）发送到「AI 标签」设置里的接口地址与模型，由它生成中文描述。确认启用吗？',
        confirmText: '确认启用',
        cancelText: '保持本地'
      });
      this.form.scene_visual_provider = confirmed ? 'external' : 'local';
      if (!confirmed) {
        // 浏览器已经把单选框翻到了「外部」，而表单值没变，Vue 不会重绘：手动拨回本地。
        if (this.$refs.externalRadio) this.$refs.externalRadio.checked = false;
        if (this.$refs.localRadio) this.$refs.localRadio.checked = true;
      }
    }
  }
};
</script>

<style scoped>
.scene-privacy { margin-bottom: 12px; }
.scene-error { color: var(--danger-color); }
.scene-progress { display: grid; gap: 6px; margin-top: 10px; color: var(--text-secondary); font-size: 12px; }
.scene-progress__bar { height: 6px; border-radius: 3px; background: var(--control-bg); overflow: hidden; }
.scene-progress__bar i { display: block; height: 100%; background: var(--accent-color); transition: width var(--transition); }
.scene-interval { width: 120px; }
.scene-provider { display: grid; gap: 6px; }
.scene-provider__option { display: flex; align-items: center; gap: 6px; font-weight: normal; }
.scene-external-note { color: var(--text-primary); }
</style>
