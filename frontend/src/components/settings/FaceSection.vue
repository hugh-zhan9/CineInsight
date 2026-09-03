<template>
  <div id="settings-face" class="settings-section">
    <h3>人脸识别</h3>

    <!-- 隐私披露放在最前面：这是本地跑的人脸识别，用户在动手之前就该知道
         哪一步要联网、向量与裁剪图存在哪、会不会外发。
         「准备运行时」这一步确实联网（PyPI 装依赖、必要时下载 Python、下载模型包），
         写成「模型下载之后不再联网」是不实的。 -->
    <p class="help-text face-privacy" data-test="face-privacy-note">
      「准备运行时」这一步会联网：从 PyPI 安装 Python 依赖、必要时下载一份托管 Python 运行时、
      并下载人脸模型包（校验 sha256）。准备完成之后，人脸检测与向量计算全程离线，
      不再发起任何网络请求；人脸向量与裁剪小图只写入本机的
      {{ runtimeStatus?.runtime_dir || '应用数据目录' }} 与 faces 目录，绝不外发到任何服务，
      也不会随日志或通知带出去。识别结果只生成人物候选，任何人物关联都要你在待审工作台里确认。
    </p>

    <div class="short-feed-status">
      <div class="short-feed-status-main">
        <strong data-test="face-runtime-state">{{ runtimeStateText }}</strong>
        <span data-test="face-runtime-reason">{{ runtimeReasonText }}</span>
      </div>
      <div class="short-feed-actions">
        <button
          v-if="runtimeStatus && runtimeStatus.preparing"
          type="button"
          class="btn-secondary"
          data-test="face-cancel-prepare"
          @click="cancelPrepare"
        >取消准备</button>
        <button
          v-else-if="runtimeCanPrepare"
          type="button"
          class="btn-primary"
          data-test="face-prepare-runtime"
          @click="prepareRuntime"
        >准备运行时（约 {{ runtimeStatus?.model_archive_size || '275 MB' }} 模型）</button>
      </div>
    </div>

    <div v-if="runtimeStatus && runtimeStatus.preparing" class="face-progress" data-test="face-runtime-progress">
      <div class="face-progress__bar"><i :style="{ width: `${preparePercent}%` }"></i></div>
      <span>{{ runtimeStatus.message }} {{ prepareStageText }} {{ preparePercent }}%</span>
    </div>
    <p v-if="runtimeStatus && runtimeStatus.error" class="help-text face-error" data-test="face-runtime-error">
      {{ runtimeStatus.error }}
    </p>

    <div class="setting-item">
      <label>模型下载镜像前缀</label>
      <input
        data-test="face-mirror-url"
        type="text"
        class="text-input"
        placeholder="https://镜像地址/"
        v-model="form.face_model_mirror_url"
      />
      <p class="help-text">
        留空时直接从官方地址下载。直连不通时填一个代理前缀，实际地址会拼成「前缀 + 官方地址」。
        模型包的 sha256 固定校验，镜像给错东西会被拒绝。
      </p>
      <p class="help-text face-source" data-test="face-model-source">来源：{{ runtimeStatus?.model_source_url || '' }}</p>
    </div>

    <div class="setting-item">
      <label>分析范围</label>
      <div class="face-analysis-row">
        <select data-test="face-analysis-scope" class="text-input face-scope-select" v-model="scope">
          <option value="all">视频与图片</option>
          <option value="videos">只分析视频</option>
          <option value="images">只分析图片</option>
        </select>
        <button
          v-if="analysisStatus && analysisStatus.running"
          type="button"
          class="btn-secondary"
          data-test="face-analysis-cancel"
          @click="cancelAnalysis"
        >取消分析</button>
        <button
          v-else
          type="button"
          class="btn-primary"
          data-test="face-analysis-start"
          :disabled="!runtimeAvailable"
          :title="runtimeAvailable ? '' : runtimeReasonText"
          @click="startAnalysis"
        >开始分析</button>
      </div>
      <p v-if="!runtimeAvailable" class="help-text" data-test="face-analysis-disabled-reason">
        运行时未就绪，分析入口暂不可用：{{ runtimeReasonText }}
      </p>
      <p class="help-text">
        只处理没有观测或源文件已经变化的媒体；视频按分钟抽帧，图片直接读解码后的大图。
      </p>
    </div>

    <div class="face-analysis-status" data-test="face-analysis-status" role="status">
      <span data-test="face-analysis-progress">{{ analysisText }}</span>
      <button
        v-if="analysisWaitingIdle"
        type="button"
        class="btn-secondary btn-compact"
        data-test="face-run-now"
        @click="runNow"
      >忽略空闲立即运行</button>
      <ul v-if="analysisFailures.length" class="face-failures" data-test="face-analysis-failures">
        <li v-for="failure in analysisFailures" :key="`${failure.media_kind}-${failure.media_id}`">
          {{ failure.name }}：{{ failure.error }}
        </li>
      </ul>
    </div>

    <div class="setting-item">
      <label class="switch">
        <input data-test="face-auto-toggle" type="checkbox" v-model="form.auto_face_analysis" />
        <span class="slider"></span>
        <span>扫描后自动分析人脸</span>
      </label>
      <p class="help-text">
        默认关闭。开启后扫描发现新媒体时自动排一轮分析，经后台任务空闲调度；
        运行时没准备好时自动跳过，不会反复报错。
      </p>
    </div>

    <div class="setting-item">
      <label>数据占用</label>
      <p class="help-text" data-test="face-usage">{{ usageText }}</p>
      <button type="button" class="btn-secondary" data-test="face-clear-data" @click="clearData">清除全部人脸数据</button>
      <p class="help-text">
        清除会删掉全部人脸观测、簇与裁剪小图，但不会动人物本身与你已经确认过的人物关联。
        清除之后重新分析可以完全重建。
      </p>
    </div>
  </div>
</template>

<script>
import {
  GetFaceRuntimeStatus,
  PrepareFaceRuntime,
  CancelFaceRuntimePrepare,
  StartFaceAnalysis,
  GetFaceAnalysisStatus,
  CancelFaceAnalysis,
  ClearFaceData,
  GetFaceDataUsage,
  RunGatedTaskNow
} from '../../../wailsjs/go/main/App';
import { confirmAction, notifyError, notifySuccess } from '../../utils/feedback.js';
import { idleWaitReasonLabel, isIdleGateNotWaitingError } from '../../utils/idleScheduling.js';

const RUNTIME_STATE_TEXT = {
  available: '已就绪',
  missing_python: '未就绪',
  missing_venv: '未就绪',
  missing_model: '未就绪',
  download_failed: '下载失败',
  incompatible: '当前平台不支持'
};

const PREPARE_STAGE_TEXT = {
  python: '准备 Python',
  venv: '创建虚拟环境',
  deps: '安装依赖',
  model: '下载模型',
  verify: '校验',
  install: '安装',
  done: '完成',
  failed: '失败',
  cancelled: '已取消'
};

// 人脸识别分区（D-016..D-022）：运行时状态与准备、镜像前缀、分析启动与进度、
// 自动开关、数据占用与清除，外加隐私披露。
//
// 分析的启动/取消入口只在这里（设计 V1.0.1 把视频列表与照片页的独立入口去掉了），
// 因此运行时不可用时这里必须把原因说清楚，而不是只把按钮置灰。
export default {
  name: 'FaceSection',
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return {
      runtimeStatus: null,
      analysisStatus: null,
      usage: null,
      scope: 'all',
      runtimeOff: null,
      analysisOff: null
    };
  },
  mounted() {
    this.loadRuntimeStatus();
    this.loadAnalysisStatus();
    this.loadUsage();
    if (window.runtime?.EventsOn) {
      const runtimeOff = window.runtime.EventsOn('face-runtime-state', (status) => {
        if (status) this.runtimeStatus = status;
      });
      if (typeof runtimeOff === 'function') this.runtimeOff = runtimeOff;
      const analysisOff = window.runtime.EventsOn('face-analysis-state', (status) => {
        if (!status) return;
        const finished = this.analysisStatus?.running && !status.running;
        this.analysisStatus = status;
        // 一轮跑完之后占用会变，顺手刷一次，用户不用自己点。
        if (finished) this.loadUsage();
      });
      if (typeof analysisOff === 'function') this.analysisOff = analysisOff;
    }
  },
  beforeUnmount() {
    this.runtimeOff?.();
    this.analysisOff?.();
  },
  computed: {
    runtimeAvailable() {
      return this.runtimeStatus?.state === 'available';
    },
    runtimeCanPrepare() {
      const state = this.runtimeStatus?.state;
      return state === 'missing_python' || state === 'missing_venv' || state === 'missing_model' || state === 'download_failed';
    },
    runtimeStateText() {
      if (!this.runtimeStatus) return '正在检查…';
      if (this.runtimeStatus.preparing) return '正在准备';
      return RUNTIME_STATE_TEXT[this.runtimeStatus.state] || '未就绪';
    },
    runtimeReasonText() {
      if (!this.runtimeStatus) return '';
      if (this.runtimeStatus.state === 'available') {
        return `运行时 ${this.runtimeStatus.identity}，模型已就绪。`;
      }
      return this.runtimeStatus.reason || '';
    },
    prepareStageText() {
      return PREPARE_STAGE_TEXT[this.runtimeStatus?.stage] || '';
    },
    preparePercent() {
      const total = Number(this.runtimeStatus?.total_bytes || 0);
      const done = Number(this.runtimeStatus?.downloaded_bytes || 0);
      if (total <= 0) return 0;
      return Math.min(100, Math.round((done / total) * 100));
    },
    analysisWaitingIdle() {
      return Boolean(this.analysisStatus?.running && this.analysisStatus?.gate?.waiting_idle);
    },
    analysisFailures() {
      return this.analysisStatus?.failures || [];
    },
    analysisText() {
      const status = this.analysisStatus;
      if (!status) return '正在读取分析状态…';
      if (status.running) {
        if (status.gate?.waiting_idle) {
          return `等待空闲：${idleWaitReasonLabel(status.gate.reason)}（已处理 ${status.processed}/${status.total}）`;
        }
        if (status.preparing) return '正在整理候选媒体…';
        const current = status.current_media ? `，当前 ${status.current_media}` : '';
        return `正在分析 ${status.processed}/${status.total}${current}`;
      }
      if (status.cancelled) return `已取消，本轮处理了 ${status.processed} 项`;
      if (status.interrupted) return `已中断（sidecar 退出），已处理 ${status.processed} 项，重新开始会接着跑`;
      if (status.last_error) return `上一轮失败：${status.last_error}`;
      if (status.completed) {
        return `上一轮完成：分析 ${status.succeeded} 项，检出 ${status.faces_detected} 张人脸，新建 ${status.clusters_created} 个人脸分组，失败 ${status.failed} 项`;
      }
      return '尚未运行过人脸分析';
    },
    usageText() {
      if (!this.usage) return '正在读取…';
      const megabytes = (Number(this.usage.crop_bytes || 0) / (1024 * 1024)).toFixed(1);
      return `${this.usage.observation_count} 条人脸观测、${this.usage.cluster_count} 个人脸分组、${this.usage.candidate_count} 条人物候选；裁剪小图 ${this.usage.crop_file_count} 个，占用 ${megabytes} MB`;
    }
  },
  methods: {
    async loadRuntimeStatus() {
      try {
        this.runtimeStatus = await GetFaceRuntimeStatus();
      } catch (err) {
        this.runtimeStatus = { state: 'incompatible', reason: String(err) };
      }
    },
    async loadAnalysisStatus() {
      try {
        this.analysisStatus = await GetFaceAnalysisStatus();
      } catch (_err) {
        this.analysisStatus = null;
      }
    },
    async loadUsage() {
      try {
        this.usage = await GetFaceDataUsage();
      } catch (_err) {
        this.usage = null;
      }
    },
    async prepareRuntime() {
      try {
        this.runtimeStatus = await PrepareFaceRuntime();
      } catch (err) {
        notifyError(`准备人脸运行时失败：${err}`);
      }
    },
    async cancelPrepare() {
      try {
        await CancelFaceRuntimePrepare();
      } catch (err) {
        notifyError(`取消准备失败：${err}`);
      }
      await this.loadRuntimeStatus();
    },
    async startAnalysis() {
      try {
        this.analysisStatus = await StartFaceAnalysis(this.scope);
      } catch (err) {
        notifyError(`启动人脸分析失败：${err}`);
      }
    },
    async cancelAnalysis() {
      try {
        await CancelFaceAnalysis();
      } catch (err) {
        notifyError(`取消人脸分析失败：${err}`);
      }
      await this.loadAnalysisStatus();
    },
    async runNow() {
      try {
        await RunGatedTaskNow('face');
      } catch (err) {
        // 任务刚好已经被放行：没什么可豁免的了，静默刷新即可。
        if (!isIdleGateNotWaitingError(err)) {
          notifyError(`立即运行失败：${err}`);
        }
      }
      await this.loadAnalysisStatus();
    },
    async clearData() {
      const confirmed = await confirmAction({
        title: '清除全部人脸数据',
        message: '这会删除全部人脸观测、人脸分组与裁剪小图。人物本身与你已确认的人物关联不受影响。确认继续吗？',
        confirmText: '清除',
        danger: true
      });
      if (!confirmed) return;
      try {
        this.usage = await ClearFaceData();
        notifySuccess('人脸数据已清除');
      } catch (err) {
        notifyError(`清除人脸数据失败：${err}`);
      }
    }
  }
};
</script>

<style scoped>
.face-privacy { margin-bottom: 12px; }
/* 报错要看得出是报错：`settings-error` 在样式表里并不存在（超分分区那处也是空引用），
   这里直接用危险色令牌。 */
.face-error { color: var(--danger-color); }
.face-source { word-break: break-all; }
.face-progress { display: grid; gap: 6px; margin-top: 10px; color: var(--text-secondary); font-size: 12px; }
.face-progress__bar { height: 6px; border-radius: 3px; background: var(--control-bg); overflow: hidden; }
.face-progress__bar i { display: block; height: 100%; background: var(--accent-color); transition: width var(--transition); }
.face-analysis-row { display: flex; align-items: center; gap: 8px; }
.face-scope-select { width: 160px; }
.face-analysis-status {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}
.face-failures { display: grid; gap: 4px; margin: 0; padding-left: 18px; }
.btn-compact { height: 28px; padding: 0 10px; font-size: 12px; }
</style>
