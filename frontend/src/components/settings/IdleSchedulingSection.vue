<template>
  <div id="settings-idle-scheduling" class="settings-section">
    <h3>后台任务调度</h3>
    <div class="setting-item">
      <label class="switch">
        <input data-test="idle-scheduling-toggle" type="checkbox" v-model="form.idle_scheduling_enabled" />
        <span class="slider"></span>
        <span>空闲时才跑自动后台任务</span>
      </label>
      <p class="help-text">
        只影响自动触发的任务（扫描后的技术信息、近重复指纹、清理分析，以及图片 EXIF 与 AI 打标）。
        你自己点的「开始」按钮任何时候都立刻执行，不受这里影响。
      </p>
      <p class="help-text">空闲判定依赖 macOS 的 ioreg 与 pmset；其他平台一律视为空闲，等同于关掉这个开关。</p>
    </div>

    <div class="setting-grid idle-scheduling-grid">
      <div class="setting-item">
        <label>多久没动算空闲（分钟）</label>
        <input
          data-test="idle-threshold-minutes"
          type="number"
          min="1"
          max="120"
          step="1"
          class="number-input"
          v-model.number="form.idle_threshold_minutes"
          :disabled="!form.idle_scheduling_enabled"
        />
        <p class="help-text">1–120 分钟，默认 5。超出范围保存时会自动收进区间。</p>
      </div>
      <div class="setting-item">
        <label class="switch">
          <input
            data-test="idle-require-ac-power"
            type="checkbox"
            v-model="form.idle_require_ac_power"
            :disabled="!form.idle_scheduling_enabled"
          />
          <span class="slider"></span>
          <span>只在接着电源时跑</span>
        </label>
        <p class="help-text">笔记本靠电池时不启动自动任务。</p>
      </div>
    </div>

    <div class="setting-item">
      <label>只在这个时间段内跑</label>
      <div class="idle-window-row">
        <input
          data-test="idle-window-start"
          type="time"
          class="text-input idle-window-input"
          v-model="form.idle_window_start"
          :disabled="!form.idle_scheduling_enabled"
        />
        <span class="idle-window-sep">至</span>
        <input
          data-test="idle-window-end"
          type="time"
          class="text-input idle-window-input"
          v-model="form.idle_window_end"
          :disabled="!form.idle_scheduling_enabled"
        />
        <button
          type="button"
          class="btn-secondary btn-compact"
          data-test="idle-window-clear"
          :disabled="!form.idle_scheduling_enabled || (!form.idle_window_start && !form.idle_window_end)"
          @click="clearWindow"
        >清除</button>
      </div>
      <p class="help-text">两端都留空表示不限时段。支持跨午夜，例如 22:00 至 06:00。</p>
    </div>

    <div class="idle-scheduler-status" data-test="idle-scheduler-status" role="status">
      <template v-if="statusError">
        <span>读取空闲状态失败：{{ statusError }}</span>
      </template>
      <template v-else-if="!status">
        <span>正在读取空闲状态…</span>
      </template>
      <template v-else>
        <span data-test="idle-scheduler-state-text">{{ stateText }}</span>
        <ul v-if="status.waiting?.length" class="idle-waiting-list" data-test="idle-waiting-list">
          <li v-for="item in status.waiting" :key="item.task_key">
            {{ taskLabel(item.task_key) }}：{{ reasonLabel(item.reason) }}
            <button
              type="button"
              class="btn-secondary btn-compact"
              :data-test="`idle-run-now-${item.task_key}`"
              @click="runNow(item.task_key)"
            >忽略空闲立即运行</button>
          </li>
        </ul>
      </template>
      <button type="button" class="btn-secondary btn-compact" data-test="idle-status-refresh" @click="loadStatus">刷新</button>
    </div>
  </div>
</template>

<script>
import { GetIdleSchedulerStatus, RunGatedTaskNow } from '../../../wailsjs/go/main/App';
import { notifyError } from '../../utils/feedback.js';
import { BACKGROUND_TASK_LABELS, idleWaitReasonLabel, isIdleGateNotWaitingError } from '../../utils/idleScheduling.js';

// 后台任务调度分区：开关、阈值、电源与时间窗，外加一块当前空闲状态与等待清单。
// 等待清单里的每一项都能直接「忽略空闲立即运行」——设置页是用户找得到的地方，
// 片库页的任务状态条上也有同一个按钮。
export default {
  name: 'IdleSchedulingSection',
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return {
      status: null,
      statusError: '',
      idleStateOff: null
    };
  },
  mounted() {
    this.loadStatus();
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn('idle-scheduler-state', (status) => {
        if (status) {
          this.status = status;
          this.statusError = '';
        }
      });
      if (typeof off === 'function') this.idleStateOff = off;
    }
  },
  beforeUnmount() {
    this.idleStateOff?.();
  },
  computed: {
    stateText() {
      const status = this.status;
      if (!status) return '';
      if (status.settings_error) return `读取空闲调度设置失败：${status.settings_error}（本轮按关闭处理，自动任务照常执行）`;
      if (!status.enabled) return '空闲调度已关闭：自动任务会立即执行。';
      if (!status.supports_probe) return '当前平台没有空闲探测，自动任务会立即执行。';
      if (status.probe_error) return `空闲探测失败：${status.probe_error}`;
      if (!status.probed) return '正在探测空闲状态…';
      const idleText = `已空闲 ${this.formatDuration(status.idle_seconds)}`;
      const powerText = status.on_ac_power ? '接着电源' : '使用电池';
      const waitingText = status.waiting?.length ? `${status.waiting.length} 个任务在等空闲` : '当前没有任务在等空闲';
      return `${idleText}，${powerText}；${waitingText}。`;
    }
  },
  methods: {
    async loadStatus() {
      try {
        this.status = await GetIdleSchedulerStatus();
        this.statusError = '';
      } catch (err) {
        this.statusError = String(err);
      }
    },
    async runNow(taskKey) {
      try {
        await RunGatedTaskNow(taskKey);
      } catch (err) {
        // 任务刚好已经被放行：没什么可豁免的了，静默刷新即可，不该弹红条。
        if (!isIdleGateNotWaitingError(err)) {
          notifyError('立即运行失败: ' + err);
        }
      }
      await this.loadStatus();
    },
    clearWindow() {
      this.form.idle_window_start = '';
      this.form.idle_window_end = '';
    },
    taskLabel(taskKey) {
      return BACKGROUND_TASK_LABELS[taskKey] || taskKey;
    },
    reasonLabel(reason) {
      return idleWaitReasonLabel(reason);
    },
    formatDuration(seconds) {
      const total = Math.max(0, Number(seconds) || 0);
      if (total < 60) return `${Math.round(total)} 秒`;
      const minutes = Math.floor(total / 60);
      if (minutes < 60) return `${minutes} 分钟`;
      const hours = Math.floor(minutes / 60);
      return `${hours} 小时 ${minutes % 60} 分钟`;
    }
  }
};
</script>

<style scoped>
.idle-scheduling-grid { display: grid; gap: 16px; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); }
.idle-window-row { display: flex; align-items: center; gap: 8px; }
.idle-window-input { width: 120px; }
.idle-window-sep { color: var(--text-secondary); font-size: 13px; }
.idle-scheduler-status {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 16px;
  padding: 10px 12px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}
.idle-waiting-list { display: grid; gap: 6px; margin: 0; padding-left: 18px; }
.idle-waiting-list li { display: flex; align-items: center; gap: 8px; }
.btn-compact { height: 28px; padding: 0 10px; font-size: 12px; }
</style>
