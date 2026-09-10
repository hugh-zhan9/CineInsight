<template>
  <BaseModal v-if="visible" close-on-overlay stop-modal-clicks @close="$emit('close')">
      <h2>扫描视频目录</h2>
      <div class="scan-dir-group">
        <button @click="selectDir" :disabled="scanProgress.scanning" class="btn-primary">选择目录</button>
        <p v-if="scanDirectory" class="selected-dir">{{ scanDirectory }}</p>
      </div>

      <div v-if="scanProgress.scanning" class="scan-progress">
        <p data-test="scan-phase">{{ phaseLabel }} · 已用时 {{ elapsedSeconds }} 秒</p>
        <template v-if="scanProgress.phase === 'reading'">
          <p data-test="scan-discovery">已检查 {{ scanProgress.visited }} 个文件，已发现 {{ scanProgress.found }} 个视频</p>
          <p class="scan-current-path">当前目录：{{ scanProgress.currentPath }}</p>
        </template>
        <p v-if="scanProgress.phase === 'reconciling'">共发现 {{ scanProgress.found }} 个视频，正在核对已有记录。</p>
        <p v-if="waitingForProgress" data-test="scan-waiting">{{ waitingMessage }}</p>
        <template v-if="scanProgress.phase === 'processing'">
          <p>正在处理 {{ scanProgress.processed }}/{{ scanProgress.total }} · 共发现 {{ scanProgress.found }} 个视频</p>
          <p>新增 {{ scanProgress.imported }} 个，恢复 {{ scanProgress.restored }} 个，删除 {{ scanProgress.deleted }} 个，跳过 {{ scanProgress.skipped }} 个</p>
        </template>
      </div>
      <div v-if="!scanProgress.scanning && scanProgress.statusMessage" :class="['scan-result', { 'scan-result--error': scanProgress.failed }]" :role="scanProgress.failed ? 'alert' : 'status'" data-test="scan-result">
        <p>{{ scanProgress.statusMessage }}</p>
      </div>
      <div class="modal-actions">
        <button @click="startScan" :disabled="!scanDirectory || scanProgress.scanning" class="btn-primary">
          {{ scanProgress.statusMessage ? '重新扫描' : '开始扫描' }}
        </button>
        <button @click="$emit('close')" class="btn-secondary">
          {{ scanProgress.scanning ? '后台继续' : scanProgress.statusMessage ? '关闭' : '取消' }}
        </button>
      </div>
  </BaseModal>
</template>

<script>
import { SelectDirectory, SyncDirectoryWithProgress, AddDirectory } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import { notify, notifyError } from '../utils/feedback.js';

let scanRequestSequence = 0;

export default {
  name: 'ScanDialog',
  components: { BaseModal },
  props: {
    visible: { type: Boolean, default: false },
    directories: { type: Array, default: () => [] },
    settings: { type: Object, default: () => ({}) }
  },
  emits: ['close', 'scan-complete'],
  data() {
    return {
      scanDirectory: '',
      scanRequestID: '',
      scanStartedAt: 0,
      lastProgressAt: 0,
      clockNow: 0,
      scanProgress: {
        scanning: false,
        phase: '',
        visited: 0,
        currentPath: '',
        found: 0,
        processed: 0,
        imported: 0,
        restored: 0,
        deleted: 0,
        skipped: 0,
        total: 0,
        statusMessage: '',
        failed: false
      }
    };
  },
  computed: {
    phaseLabel() {
      return ({ checking: '正在检查目录', settings: '正在读取扫描设置', reading: '正在读取目录', reconciling: '正在核对片库记录', processing: '正在处理文件', saving: '正在保存扫描目录' })[this.scanProgress.phase] || '正在启动扫描';
    },
    elapsedSeconds() {
      return Math.max(0, Math.floor((this.clockNow - this.scanStartedAt) / 1000));
    },
    waitingForProgress() {
      return this.clockNow - this.lastProgressAt >= 8000;
    },
    waitingMessage() {
      if (['checking', 'reading'].includes(this.scanProgress.phase)) {
        return '仍在等待目录读取返回，暂未收到新进度；当前计数不是最终结果。';
      }
      return '当前操作尚未返回，正在等待新进度。';
    }
  },
  watch: {
    visible(val) {
      if (val && !this.scanProgress.scanning) {
        this.scanDirectory = '';
        this.resetProgress();
      }
    }
  },
  beforeUnmount() {
    this.stopProgressUpdates();
  },
  methods: {
    stopProgressUpdates() {
      this.scanProgressOff?.();
      this.scanProgressOff = null;
      clearInterval(this.scanClock);
      this.scanClock = null;
    },
    setPhase(phase) {
      this.scanProgress.phase = phase;
      this.lastProgressAt = Date.now();
    },
    resetProgress() {
      this.scanProgress = {
        scanning: false, found: 0, processed: 0,
        phase: '', visited: 0, currentPath: '',
        imported: 0,
        restored: 0, deleted: 0, skipped: 0, total: 0,
        statusMessage: '', failed: false
      };
    },
    async selectDir() {
      try {
        this.scanDirectory = await SelectDirectory();
      } catch (err) {
        console.error('选择目录失败:', err);
        notifyError('选择目录失败: ' + err);
      }
    },
    async startScan() {
      if (this.scanProgress.scanning) return;
      if (!this.scanDirectory) {
        notify('请先选择目录');
        return;
      }
      if (this.isExcludedPath(this.scanDirectory)) {
        notify('所选目录位于扫描黑名单中，请先从设置中移除后再扫描。');
        return;
      }

      this.resetProgress();
      this.scanProgress.scanning = true;

      try {
        this.scanRequestID = `${Date.now()}-${++scanRequestSequence}`;
        this.scanStartedAt = this.clockNow = Date.now();
        this.setPhase('checking');
        this.scanProgress.currentPath = this.scanDirectory;
        this.scanProgressOff = window.runtime.EventsOn('directory-scan-progress', progress => {
          if (progress.request_id !== this.scanRequestID || !this.scanProgress.scanning) return;
          const phases = ['checking', 'settings', 'reading', 'reconciling', 'processing', 'saving'];
          if (phases.indexOf(progress.phase) < phases.indexOf(this.scanProgress.phase)) return;
          this.setPhase(progress.phase);
          this.scanProgress.visited = progress.visited;
          this.scanProgress.found = progress.found;
          this.scanProgress.currentPath = progress.current_path;
          this.scanProgress.processed = progress.processed || 0;
          this.scanProgress.total = progress.total || 0;
          this.scanProgress.imported = progress.added || 0;
          this.scanProgress.restored = progress.restored || 0;
          this.scanProgress.deleted = progress.deleted || 0;
          this.scanProgress.skipped = progress.skipped || 0;
        });
        this.scanClock = setInterval(() => { this.clockNow = Date.now(); }, 1000);
        const result = await SyncDirectoryWithProgress(this.scanDirectory, this.scanRequestID);
        this.scanProgress.found = Number(result.scanned || 0);
        this.scanProgress.imported = Number(result.added || 0);
        this.scanProgress.restored = Number(result.restored || 0);
        this.scanProgress.deleted = Number(result.deleted || 0);
        this.scanProgress.skipped = Number(result.skipped || 0);
        const errors = result.errors || [];
        // 自动加入目录配置
        this.setPhase('saving');
        const exists = (this.directories || []).some(d => d.path === this.scanDirectory);
        if (!exists && (result.scanned > 0 || !errors.length)) {
          const alias = this.scanDirectory.split(/[/\\]/).filter(Boolean).pop() || this.scanDirectory;
          try {
            await AddDirectory(this.scanDirectory, alias);
          } catch (err) {
            console.warn('保存扫描目录失败:', err);
            errors.push({ error: '保存扫描目录失败: ' + err });
          }
        }

        this.scanProgress.statusMessage = `扫描完成：新增 ${this.scanProgress.imported} 个，恢复 ${this.scanProgress.restored} 个，删除 ${this.scanProgress.deleted} 个，暂不可用 ${Number(result.stale || 0)} 个。`;
        if (errors.length) {
          this.scanProgress.failed = true;
          this.scanProgress.statusMessage += ` 有 ${errors.length} 项失败：${errors[0].error}`;
        }
        this.$emit('scan-complete');
        // 不自动关闭，让用户确认结果
      } catch (err) {
        this.scanProgress.statusMessage = '扫描失败: ' + err;
        this.scanProgress.failed = true;
        console.error('扫描失败:', err);
      } finally {
        this.scanProgress.scanning = false;
        this.stopProgressUpdates();
      }
    },
    excludedPaths() {
      return String(this.settings?.scan_exclude_paths || '').split(/\r?\n/).map(path => path.trim()).filter(Boolean);
    },
    isExcludedPath(path) {
      const normalize = value => String(value || '').replace(/\\/g, '/').replace(/\/+$/, '').toLocaleLowerCase();
      const candidate = normalize(path);
      return this.excludedPaths().some(excluded => {
        const root = normalize(excluded);
        return candidate === root || candidate.startsWith(root + '/');
      });
    }
  }
};
</script>

<style scoped>
.scan-dir-group {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
}

.selected-dir {
  margin: 0;
  min-width: 0;
  color: var(--text-secondary);
  font-size: 12px;
  overflow-wrap: anywhere;
}

.scan-progress {
  display: grid;
  gap: 4px;
  margin-top: 14px;
  color: var(--text-secondary);
  font-size: 13px;
}

.scan-progress p { margin: 0; }

.scan-current-path { overflow-wrap: anywhere; }

.scan-result {
  margin-top: 14px;
  color: var(--success-color);
  font-weight: 600;
  overflow-wrap: anywhere;
}

.scan-result p { margin: 0; }

.scan-result--error { color: var(--danger-color); }
</style>
