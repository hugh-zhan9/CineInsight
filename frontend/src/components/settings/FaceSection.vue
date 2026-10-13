<template>
  <div id="settings-face" class="settings-section">
    <h3>人脸识别</h3>

    <p class="help-text face-privacy" data-test="face-privacy-note">
      使用 macOS 内置人脸能力，无需下载第三方模型。识别与特征计算均在本机运行，人脸向量与裁剪小图只保存在本机，不外发。
      识别结果只生成人物候选，人物关联仍需你确认。
      该能力使用苹果内部接口，系统升级后或在较早的 macOS（如 macOS 15）上可能不可用；届时会明确提示。
    </p>
    <div class="short-feed-status">
      <div class="short-feed-status-main">
        <strong data-test="face-runtime-state">{{ runtimeStateText }}</strong>
        <span data-test="face-runtime-reason">{{ runtimeReasonText }}</span>
      </div>
      <button type="button" class="btn btn--secondary" data-test="face-prepare-runtime" :disabled="checking" @click="prepareRuntime">{{ checking ? '正在检查…' : '重新检查系统能力' }}</button>
    </div>
    <p class="help-text face-migration-note">首次分析会用已有裁剪图迁移旧特征，保留命名、忽略和人工关联；缺失或无法识别的裁剪会保留原记录并提示。</p>
    <p v-if="analysisStatus?.migration_pending" class="help-text face-error" data-test="face-migration-pending">{{ analysisStatus.migration_pending }} 条旧观测未能迁移，原审阅记录仍保留，暂不参与新识别；重新分析会再尝试。</p>

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
          class="btn btn--secondary"
          data-test="face-analysis-cancel"
          @click="cancelAnalysis"
        >取消分析</button>
        <button
          v-else
          type="button"
          class="btn btn--primary"
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
        class="btn btn--secondary btn--sm"
        data-test="face-run-now"
        @click="runNow"
      >忽略空闲立即运行</button>
      <TaskFailureList :failures="analysisFailures" key-prefix="face-" data-test="face-analysis-failures" fallback-label="媒体" />
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
      <button type="button" class="btn btn--danger" data-test="face-clear-data" @click="clearData">清除全部人脸数据</button>
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
  StartFaceAnalysis,
  GetFaceAnalysisStatus,
  CancelFaceAnalysis,
  ClearFaceData,
  GetFaceDataUsage,
  RunGatedTaskNow
} from '../../../wailsjs/go/main/App';
import { confirmAction, notifyError, notifySuccess } from '../../utils/feedback.js';
import { idleWaitReasonLabel, isIdleGateNotWaitingError } from '../../utils/idleScheduling.js';
import TaskFailureList from '../video-list/TaskFailureList.vue';

const RUNTIME_STATE_TEXT = { available: '已就绪', incompatible: '系统能力不可用' };

export default {
  name: 'FaceSection',
  components: { TaskFailureList },
  props: {
    form: { type: Object, required: true },
    // (fields, actionLabel) => Promise<boolean>：相关字段有未保存修改时提示先保存（D-PC57）。
    ensureSaved: { type: Function, default: null }
  },
  data() {
    return {
      runtimeStatus: null,
      checking: false,
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
    runtimeStateText() {
      if (!this.runtimeStatus) return '正在检查…';
      return RUNTIME_STATE_TEXT[this.runtimeStatus.state] || '未就绪';
    },
    runtimeReasonText() {
      if (!this.runtimeStatus) return '';
      if (this.runtimeStatus.state === 'available') {
        return '苹果系统人脸特征已就绪。';
      }
      // 后端已只给中文说明；仍去掉可能残留的系统组件错误码前缀（如 `apple_face_version_changed: …`）。
      return String(this.runtimeStatus.reason || '').replace(/^[a-z][a-z0-9_]*:\s*/, '');
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
        if (status.migrating) return `正在迁移旧人脸特征 ${status.migration_processed || 0}/${status.migration_total || 0}，保留审阅记录`;
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
      this.checking = true;
      try { this.runtimeStatus = await PrepareFaceRuntime(); }
      catch (err) { notifyError(`系统人脸能力检查失败：${err}`); await this.loadRuntimeStatus(); }
      finally { this.checking = false; }
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
.face-migration-note { margin-bottom: var(--space-4); }
/* 报错要看得出是报错：直接用危险色令牌（公共的 .settings-error 同义）。 */
.face-error { color: var(--danger-color); }
.face-source { word-break: break-all; }
.face-analysis-row { display: flex; align-items: center; gap: 8px; }
.face-scope-select { width: 160px; }
.face-analysis-status {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
  margin-top: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: var(--fs-secondary);
}
</style>
