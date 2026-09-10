<template>
  <div
    v-if="incrementalScan.message"
    class="scan-sync-status"
    :class="`scan-sync-status--${incrementalScan.state}`"
    :role="incrementalScan.state === 'error' ? 'alert' : 'status'"
  >
    <span>{{ incrementalScan.message }}</span>
    <TaskFailureList :failures="scanFailures" fallback-label="扫描" data-test="scan-failures" />
    <span v-if="scanErrors.length > 50">仅展示前 50 条失败明细</span>
  </div>
</template>

<script>
import { SyncScanDirectories } from '../../../wailsjs/go/main/App';
import TaskFailureList from './TaskFailureList.vue';

// 手动增量扫描的入口状态条。扫描完要刷新扫描目录并重载列表，这两件事仍归片库页：
// 目录用 emit，列表用 reloadView 函数 prop（保持「先发目录、再等重载」的顺序）。
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
      try {
        const result = await SyncScanDirectories();
        const errors = Array.isArray(result?.errors) ? result.errors : [];
        this.scanErrors = errors;
        const summary = [
          `扫描 ${Number(result?.scanned || 0)} 个文件`,
          `新增 ${Number(result?.added || 0)}`,
          `恢复显示 ${Number(result?.restored || 0)}`,
          `迁移 ${Number(result?.relocated || 0)}`,
          `移除记录 ${Number(result?.deleted || 0)}`,
          `补全元数据 ${Number(result?.metadata_refreshed || 0)}`,
          `跳过 ${Number(result?.skipped || 0)}`
        ];
        if (errors.length > 0) {
          summary.push(`失败 ${errors.length}`);
        }
        this.incrementalScan = {
          running: false,
          state: errors.length > 0 ? 'warning' : 'success',
          message: `增量扫描完成：${summary.join('，')}`
        };
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
  }
};
</script>

<style scoped>
.scan-sync-status {
  margin: 10px 0 0;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
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
