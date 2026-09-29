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
        <p v-if="scanProgress.skipDetail" class="scan-result__detail" data-test="scan-skip-breakdown">{{ scanProgress.skipDetail }}</p>
        <TaskFailureList :failures="scanFailures" fallback-label="扫描" data-test="scan-dialog-failures" />
      </div>

      <!-- 加入扫描目录之前先问（D-PC09、LIB-14）：所选目录与已有扫描目录嵌套时说清楚，可以只扫描不加入。 -->
      <div v-if="rootConfirm" class="scan-root-confirm" role="group" aria-label="是否加入扫描目录" data-test="scan-root-confirm">
        <p>扫描完成后，要把「{{ rootConfirm.label }}」加入扫描目录吗？加入后启动扫描与实时同步也会覆盖它。</p>
        <p v-if="rootConfirm.nestedIn" class="scan-root-confirm__hint" data-test="scan-root-nested">它位于已有扫描目录「{{ rootConfirm.nestedIn }}」之内，里面的视频本来就会被扫描，一般不必再加入。</p>
        <p v-if="rootConfirm.contains.length" class="scan-root-confirm__hint" data-test="scan-root-contains">它包含已有扫描目录「{{ rootConfirm.contains.join('」「') }}」，加入后这些目录会被重复覆盖。</p>
        <div class="modal-actions">
          <button type="button" class="btn-secondary" data-test="scan-only" @click="chooseRoot(false)">仅扫描不加入</button>
          <button type="button" class="btn-primary" data-test="scan-and-add" @click="chooseRoot(true)">扫描并加入扫描目录</button>
        </div>
      </div>
      <p v-if="validationError" class="scan-result scan-result--error" role="alert" data-test="scan-validation-error">{{ validationError }}</p>

      <div class="modal-actions">
        <button @click="startScan" :disabled="!scanDirectory || scanProgress.scanning || !!rootConfirm || validating" class="btn-primary">
          {{ scanProgress.statusMessage ? '重新扫描' : '开始扫描' }}
        </button>
        <button @click="$emit('close')" class="btn-secondary">
          {{ scanProgress.scanning ? '后台继续' : scanProgress.statusMessage ? '关闭' : '取消' }}
        </button>
      </div>
  </BaseModal>
</template>

<script>
import { SelectDirectory, SyncDirectoryWithProgress, AddDirectory, ValidateScanDirectory } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import TaskFailureList from './video-list/TaskFailureList.vue';
import { formatSkipBreakdown } from './video-list/IncrementalScanBar.vue';
import { notify, notifyError } from '../utils/feedback.js';

// 界面上只显示目录名（G-3）。
function directoryBaseName(path) {
  return String(path || '').split(/[\\/]/).filter(Boolean).pop() || String(path || '');
}

let scanRequestSequence = 0;

export default {
  name: 'ScanDialog',
  components: { BaseModal, TaskFailureList },
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
      // 加入扫描目录的确认（null 表示不用问）；addToRoots 是用户这次的选择，换目录后作废。
      rootConfirm: null,
      rootDecision: null,
      validating: false,
      validationError: '',
      scanErrors: [],
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
        skipDetail: '',
        failed: false
      }
    };
  },
  computed: {
    scanFailures() {
      return this.scanErrors.slice(0, 50).map(item => ({
        name: item.path ? directoryBaseName(item.path) : (item.directory ? directoryBaseName(item.directory) : (item.operation || '扫描')),
        error: String(item.error || '扫描失败').slice(0, 1000)
      }));
    },
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
        this.resetRootDecision();
      }
    },
    scanDirectory() {
      if (!this.scanProgress.scanning) this.resetRootDecision();
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
      this.scanErrors = [];
      this.scanProgress = {
        scanning: false, found: 0, processed: 0,
        phase: '', visited: 0, currentPath: '',
        imported: 0,
        restored: 0, deleted: 0, skipped: 0, total: 0,
        statusMessage: '', skipDetail: '', failed: false
      };
    },
    resetRootDecision() {
      this.rootConfirm = null;
      this.rootDecision = null;
      this.validationError = '';
    },
    // 已有扫描目录的别名；没有别名时用目录名。
    rootLabel(path) {
      const dir = (this.directories || []).find(item => String(item?.path || '') === String(path || ''));
      return String(dir?.alias || '').trim() || directoryBaseName(path);
    },
    isConfiguredRoot(path) {
      return (this.directories || []).some(dir => dir.path === path);
    },
    // 要不要把所选目录加入扫描目录：已经是扫描目录就不问；否则预检一次，由用户在弹窗里选。
    // 返回 { addToRoots } 表示可以开始扫描；null 表示在等用户选择或预检失败。
    async ensureRootDecision() {
      if (this.isConfiguredRoot(this.scanDirectory)) return { addToRoots: false };
      if (this.rootDecision) return this.rootDecision;
      this.validating = true;
      this.validationError = '';
      try {
        const validation = await ValidateScanDirectory(this.scanDirectory);
        if (!validation?.exists) {
          this.validationError = '所选目录不存在或无法访问，请确认磁盘已连接。';
          return null;
        }
        if (validation.duplicate_of) return { addToRoots: false };
        this.rootConfirm = {
          label: directoryBaseName(this.scanDirectory),
          nestedIn: validation.nested_in ? this.rootLabel(validation.nested_in) : '',
          contains: (validation.contains || []).map(path => this.rootLabel(path))
        };
        return null;
      } catch (err) {
        this.validationError = '检查所选目录失败：' + err;
        return null;
      } finally {
        this.validating = false;
      }
    },
    chooseRoot(addToRoots) {
      this.rootDecision = { addToRoots: !!addToRoots };
      this.rootConfirm = null;
      return this.startScan();
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
      const decision = await this.ensureRootDecision();
      if (!decision) return;
      const addToRoots = !!decision.addToRoots;

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
        const errors = [...(result.errors || [])];
        // 用户选了「扫描并加入」才加入扫描目录；目录读取整体失败（一个都没扫到且有错）时不加。
        let added = false;
        if (addToRoots && !this.isConfiguredRoot(this.scanDirectory) && (result.scanned > 0 || !errors.length)) {
          this.setPhase('saving');
          try {
            await AddDirectory(this.scanDirectory, directoryBaseName(this.scanDirectory));
            added = true;
          } catch (err) {
            console.warn('保存扫描目录失败:', err);
            errors.push({ operation: '加入扫描目录', error: '加入扫描目录失败: ' + err });
          }
        }

        this.scanProgress.statusMessage = `扫描完成：新增 ${this.scanProgress.imported} 个，恢复 ${this.scanProgress.restored} 个，删除 ${this.scanProgress.deleted} 个，暂不可用 ${Number(result.stale || 0)} 个，跳过 ${this.scanProgress.skipped} 个。${added ? '已加入扫描目录。' : ''}`;
        this.scanProgress.skipDetail = formatSkipBreakdown(result);
        this.scanErrors = errors;
        if (errors.length) {
          this.scanProgress.failed = true;
          this.scanProgress.statusMessage += ` 有 ${errors.length} 项失败，明细见下方。`;
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

.scan-result .scan-result__detail {
  margin-top: 4px;
  color: var(--text-secondary);
  font-weight: 400;
}

.scan-root-confirm {
  margin-top: 14px;
  padding: 10px 12px;
  border: 1px solid var(--accent-border);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}

.scan-root-confirm p { margin: 0 0 6px; }

.scan-root-confirm__hint { color: var(--warning-color); }

.scan-result--error { color: var(--danger-color); }
</style>
