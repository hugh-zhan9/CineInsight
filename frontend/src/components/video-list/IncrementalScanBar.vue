<template>
  <div
    v-if="incrementalScan.message"
    class="scan-sync-status"
    :class="`scan-sync-status--${incrementalScan.state}`"
    :role="incrementalScan.state === 'error' ? 'alert' : 'status'"
  >
    <span>{{ incrementalScan.message }}</span>
    <span v-if="skipDetail" class="scan-sync-status__skips" data-test="scan-skip-breakdown">{{ skipDetail }}</span>
    <TaskFailureList :failures="scanFailures" fallback-label="扫描" data-test="scan-failures" />
    <span v-if="scanErrors.length > 50">仅展示前 50 条失败明细</span>
    <button
      v-if="!incrementalScan.running"
      type="button"
      class="scan-sync-status__close"
      aria-label="关闭扫描结果"
      data-test="scan-summary-close"
      @click="dismiss"
    >×</button>
  </div>
</template>

<script>
import { SyncScanDirectories } from '../../../wailsjs/go/main/App';
import TaskFailureList from './TaskFailureList.vue';

// 跳过数的拆分（D-PC09、LIB-14）：后端 skip_breakdown 的七个键。手动目录扫描弹窗共用这份文案。
export const SKIP_BREAKDOWN_LABELS = {
  existing: '已在库',
  blocked_user_delete: '删除过、不再收录',
  recently_modified: '刚修改（5 分钟内）暂缓',
  temp_file: '临时文件',
  not_video: '不是视频',
  read_error: '读取失败',
  legacy_trash: '旧版回收站目录'
};

// 「跳过 N」后面括号里的明细，只列非零项；没有跳过或后端没带拆分时为空串。
export function formatSkipBreakdown(result) {
  const breakdown = result?.skip_breakdown || {};
  const parts = Object.keys(SKIP_BREAKDOWN_LABELS)
    .map(key => [SKIP_BREAKDOWN_LABELS[key], Number(breakdown[key] || 0)])
    .filter(([, count]) => count > 0)
    .map(([label, count]) => `${label} ${count}`);
  return parts.length ? `跳过明细：${parts.join('，')}` : '';
}

// 扫描来源（library-scan-summary 的 trigger）对应的标题。
const TRIGGER_TITLES = {
  startup: '启动扫描完成',
  directory_change: '扫描目录变更后的扫描完成',
  manual: '增量扫描完成'
};

// 手动增量扫描的入口状态条。扫描完要刷新扫描目录并重载列表，这两件事仍归片库页：
// 目录用 emit，列表用 reloadView 函数 prop（保持「先发目录、再等重载」的顺序）。
// 启动扫描、改目录触发的扫描不是这里发起的，片库页收到 library-scan-summary 后调 showSummary 交给这里显示。
export default {
  name: 'IncrementalScanBar',
  components: { TaskFailureList },
  props: {
    migrationRunning: { type: Boolean, default: false },
    directories: { type: Array, default: () => [] },
    reloadView: { type: Function, required: true }
  },
  emits: ['reload-directories', 'state-change'],
  data() {
    return {
      scanErrors: [],
      skipDetail: '',
      incrementalScan: { running: false, state: 'idle', message: '' }
    };
  },
  computed: {
    scanFailures() {
      return this.scanErrors.slice(0, 50).map(item => ({
        name: item.path || item.directory || item.operation || '扫描',
        error: String(item.error || '扫描失败').slice(0, 1000)
      }));
    }
  },
  // 「管理」菜单里的增量扫描项与 ⌘R 都要知道它跑没跑，把状态镜像回片库页。
  watch: {
    incrementalScan: { handler(state) { this.$emit('state-change', { ...state }); }, deep: true, immediate: true }
  },
  methods: {
    async runIncrementalScan() {
      if (this.migrationRunning || this.incrementalScan.running || this.directories.length === 0) {
        return;
      }

      this.incrementalScan = { running: true, state: 'running', message: '正在扫描已配置目录...' };
      this.scanErrors = [];
      this.skipDetail = '';
      try {
        // trigger=manual：后端据此发 library-scan-summary（§1.2b）；这次的结果由这里自己汇报。
        const result = await SyncScanDirectories('manual');
        this.applyResult('manual', result);
        this.$emit('reload-directories');
        await this.reloadView();
      } catch (err) {
        console.error('增量扫描失败:', err);
        this.incrementalScan = {
          running: false,
          state: 'error',
          message: `增量扫描失败：${String(err)}`
        };
      }
    },
    // 别处发起的扫描（启动、改目录）的摘要：只显示，列表是否重载由片库页决定。
    showSummary(event) {
      if (this.incrementalScan.running || !event?.result) return;
      this.applyResult(event.trigger, event.result);
    },
    applyResult(trigger, result) {
      const errors = Array.isArray(result?.errors) ? result.errors : [];
      this.scanErrors = errors;
      this.skipDetail = formatSkipBreakdown(result);
      const summary = [
        `扫描 ${Number(result?.scanned || 0)} 个文件`,
        `新增 ${Number(result?.added || 0)}`,
        `恢复显示 ${Number(result?.restored || 0)}`,
        `迁移 ${Number(result?.relocated || 0)}`,
        `移除记录 ${Number(result?.deleted || 0)}`,
        `暂不可用 ${Number(result?.stale || 0)}`,
        `补全元数据 ${Number(result?.metadata_refreshed || 0)}`,
        `跳过 ${Number(result?.skipped || 0)}`
      ];
      if (errors.length > 0) {
        summary.push(`失败 ${errors.length}`);
      }
      const title = TRIGGER_TITLES[trigger] || TRIGGER_TITLES.manual;
      this.incrementalScan = {
        running: false,
        state: errors.length > 0 ? 'warning' : 'success',
        message: trigger === 'manual' || !TRIGGER_TITLES[trigger] ? `增量扫描完成：${summary.join('，')}` : `${title}：${summary.join('，')}`
      };
    },
    dismiss() {
      this.scanErrors = [];
      this.skipDetail = '';
      this.incrementalScan = { running: false, state: 'idle', message: '' };
    },
  }
};
</script>

<style scoped>
.scan-sync-status {
  margin: 10px 0 0;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 10px;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}

.scan-sync-status__skips {
  color: var(--text-muted);
  font-size: 12px;
}

.scan-sync-status__close {
  margin-left: auto;
  border: 0;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 18px;
}

.scan-sync-status--running {
  border-color: var(--border-strong);
}

.scan-sync-status--success {
  border-color: var(--accent-border);
  color: var(--accent-color);
}

.scan-sync-status--warning {
  border-color: var(--warning-border);
  color: var(--warning-color);
}

.scan-sync-status--error {
  border-color: var(--danger-border);
  color: var(--danger-color);
}
</style>
