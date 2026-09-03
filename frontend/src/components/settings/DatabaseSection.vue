<template>
  <div :id="`settings-database`" class="settings-section">
    <h3>数据库</h3>
    <div class="database-status">
      <div class="database-status__row">
        <span>当前后端</span>
        <strong data-test="db-backend">{{ backendLabel(databaseStatus.backend) }}</strong>
      </div>
      <div class="database-status__row">
        <span>库位置</span>
        <code>{{ databaseStatus.location || '—' }}</code>
      </div>
      <div class="database-status__row">
        <span>语义检索</span>
        <strong :class="{ 'database-status__off': !databaseStatus.semantic_available }">
          {{ databaseStatus.semantic_available ? '可用' : '不可用' }}
        </strong>
        <small v-if="!databaseStatus.semantic_available && databaseStatus.semantic_reason">{{ databaseStatus.semantic_reason }}</small>
      </div>
    </div>

    <p v-if="databaseStatus.pending_restart" class="database-restart-notice" role="status" data-test="db-pending-restart">
      已切换到 {{ backendLabel(databaseStatus.backend) }}，但当前仍在使用切换前的库。<strong>重启应用后生效。</strong>
    </p>

    <div class="setting-item">
      <label>切换后端</label>
      <div class="database-switch-row">
        <select v-model="switchTarget" class="select-input" data-test="db-switch-target">
          <option value="sqlite">SQLite（单文件，无需额外安装）</option>
          <option value="postgres">PostgreSQL（支持语义检索）</option>
        </select>
        <button
          type="button"
          class="btn-secondary"
          :disabled="switchBusy || switchTarget === databaseStatus.backend"
          data-test="db-preflight"
          @click="preflightDatabaseSwitch"
        >检查目标库</button>
        <button
          type="button"
          class="btn-secondary btn-danger-outline"
          :disabled="switchBusy || !switchPreflight || !switchPreflight.empty"
          data-test="db-switch-start"
          @click="startDatabaseSwitch"
        >{{ switchBusy ? '迁移中...' : '迁移并切换' }}</button>
      </div>
      <p v-if="switchPreflight" class="help-text" data-test="db-preflight-result">{{ switchPreflight.message }}</p>
      <p v-if="switchProgressText" class="help-text" data-test="db-switch-progress">{{ switchProgressText }}</p>
      <p class="help-text">
        迁移是<strong>复制</strong>：原来的库不会被清空，切换后想改回去只要把后端选回来重启即可。
        代价是切换之后在新库里产生的改动不会回到旧库。
      </p>
      <p class="help-text">SQLite 下语义检索不可用——它依赖 PostgreSQL 的 pgvector 扩展。</p>
    </div>
  </div>

  <div :id="`settings-backup`" class="settings-section backup-settings-section">
    <h3>数据库备份</h3>
    <div class="backup-status" :class="{ 'backup-status--error': backupStatus && !backupStatus.available }">
      <strong>{{ backupStatusText }}</strong>
      <span v-if="backupStatus?.last_success_at">最近成功：{{ formatBackupTime(backupStatus.last_success_at) }}</span>
      <span v-if="backupStatus?.last_error">最近失败：{{ backupStatus.last_error }}</span>
      <span v-else-if="backupStatus?.reason">{{ backupStatus.reason }}</span>
    </div>
    <div class="setting-item">
      <label>备份目录</label>
      <div class="directory-dialog-row">
        <input type="text" v-model.trim="form.backup_directory" class="text-input" placeholder="留空使用应用数据目录" />
        <button type="button" class="btn-secondary" @click="selectBackupDirectory">选择</button>
      </div>
      <p class="help-text">留空时保存到应用数据目录下的 backups 文件夹。</p>
    </div>
    <div class="setting-grid backup-setting-grid">
      <div class="setting-item">
        <label>保留份数</label>
        <input type="number" min="1" max="100" v-model.number="form.backup_retention_count" class="number-input" />
      </div>
      <div class="setting-item">
        <label>自动备份间隔（小时）</label>
        <input type="number" min="0" max="8760" v-model.number="form.backup_interval_hours" class="number-input" />
        <p class="help-text">0 表示关闭启动时自动备份。</p>
      </div>
    </div>
    <div class="backup-actions">
      <button type="button" class="btn-primary" :disabled="backupBusy || !backupStatus?.backup_available" @click="createBackupNow">
        {{ backupBusy ? '处理中...' : '立即备份' }}
      </button>
      <button type="button" class="btn-secondary" :disabled="backupBusy || !backupStatus?.restore_available" @click="openBackupDialog">从备份恢复</button>
      <button type="button" class="btn-secondary" :disabled="backupBusy" @click="loadBackupStatus">刷新状态</button>
    </div>
    <p v-if="backupMessage" class="help-text" :class="{ 'backup-message--error': backupMessageIsError }">{{ backupMessage }}</p>
  </div>

  <BaseModal v-if="showBackupDialog" stop-modal-clicks @close="closeBackupDialog">
    <template v-if="!selectedBackup">
      <h2>选择数据库备份</h2>
      <p class="help-text">恢复会替换当前数据库。执行前系统会先自动创建一份当前数据库的安全备份。</p>
      <div v-if="backupFiles.length" class="backup-file-list">
        <div v-for="backup in backupFiles" :key="backup.name" class="backup-file-item">
          <div>
            <strong>{{ formatBackupTime(backup.created_at) }}</strong>
            <span>{{ backup.name }} · {{ formatBackupSize(backup.size) }}</span>
          </div>
          <button type="button" class="btn-secondary btn-danger-outline" @click="selectedBackup = backup">恢复</button>
        </div>
      </div>
      <p v-else class="empty-hint">当前目录没有可用备份。</p>
      <div class="modal-actions"><button type="button" class="btn-secondary" @click="closeBackupDialog">关闭</button></div>
    </template>
    <template v-else>
      <h2>确认恢复数据库</h2>
      <p>将恢复到 <strong>{{ formatBackupTime(selectedBackup.created_at) }}</strong> 的数据状态。</p>
      <p class="backup-danger-text">这是破坏性操作。当前数据库会先自动备份；恢复期间请勿关闭应用。</p>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" :disabled="backupBusy" @click="selectedBackup = null">返回</button>
        <button type="button" class="btn-primary btn-danger" :disabled="backupBusy" @click="confirmRestoreBackup">
          {{ backupBusy ? '正在恢复...' : '确认恢复' }}
        </button>
      </div>
    </template>
  </BaseModal>
</template>

<script>
import { GetDatabaseBackendStatus, PreflightDatabaseSwitch, StartDatabaseSwitch, SelectDirectory, GetBackupStatus, ListDatabaseBackups, CreateDatabaseBackup, RestoreDatabaseBackup } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import { confirmAction } from '../../utils/feedback.js';

// 数据库后端切换与数据库备份两个分区，连同「选择备份 / 确认恢复」弹窗。
// 备份目录、保留份数与间隔仍是设置表单字段，由设置页统一保存。
export default {
  name: 'DatabaseSection',
  components: { BaseModal },
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return {
      databaseStatus: { backend: '', location: '', semantic_available: false, semantic_reason: '', pending_restart: false },
      switchTarget: 'sqlite',
      switchPreflight: null,
      switchStatus: null,
      switchBusy: false,
      switchStatusOff: null,
      backupStatus: null,
      backupFiles: [],
      showBackupDialog: false,
      selectedBackup: null,
      backupBusy: false,
      backupMessage: '',
      backupMessageIsError: false
    };
  },
  mounted() {
    this.loadBackupStatus();
    this.loadDatabaseStatus();
    if (window.runtime?.EventsOn) {
      const switchOff = window.runtime.EventsOn('database-switch-state', (status) => {
        this.switchStatus = status || null;
        this.switchBusy = Boolean(status?.running);
        if (status && !status.running) this.loadDatabaseStatus();
      });
      if (typeof switchOff === 'function') this.switchStatusOff = switchOff;
    }
  },
  beforeUnmount() {
    this.switchStatusOff?.();
  },
  computed: {
    switchProgressText() {
      const status = this.switchStatus;
      if (!status) return '';
      if (status.failed) return `迁移失败：${status.message}`;
      if (status.completed) return status.message;
      if (!status.running) return '';
      const scope = status.table_total ? `（${status.table_index}/${status.table_total}）` : '';
      return `${status.message}${scope}`;
    },
    backupStatusText() {
      if (!this.backupStatus) return '正在读取备份状态...';
      if (this.backupStatus.running) return '备份任务运行中';
      return this.backupStatus.available ? '备份功能可用' : '备份功能不可用';
    },
  },
  methods: {
    backendLabel(backend) {
      if (backend === 'sqlite') return 'SQLite';
      if (backend === 'postgres') return 'PostgreSQL';
      return backend || '未知';
    },
    async loadDatabaseStatus() {
      try {
        this.databaseStatus = await GetDatabaseBackendStatus();
        // 目标默认选成另一个后端：选中当前后端没有意义，切换按钮也会禁用。
        this.switchTarget = this.databaseStatus.backend === 'sqlite' ? 'postgres' : 'sqlite';
      } catch (err) {
        this.databaseStatus = { backend: '', location: '', semantic_available: false, semantic_reason: '', pending_restart: false };
      }
    },
    async preflightDatabaseSwitch() {
      this.switchPreflight = null;
      try {
        this.switchPreflight = await PreflightDatabaseSwitch(this.switchTarget);
      } catch (err) {
        this.switchPreflight = { empty: false, reachable: false, message: String(err) };
      }
    },
    async startDatabaseSwitch() {
      if (!await confirmAction({
        title: '切换数据库后端',
        message: '迁移会把当前库的全部数据复制到目标库，原库保持不变。完成后需要重启应用才生效。\n\n'
          + '注意：切换之后在新库里产生的改动不会回到旧库。继续吗？',
        confirmText: '开始迁移'
      })) return;
      this.switchBusy = true;
      this.switchStatus = null;
      try {
        await StartDatabaseSwitch(this.switchTarget);
      } catch (err) {
        this.switchBusy = false;
        this.switchStatus = { failed: true, message: String(err) };
      }
    },
    async loadBackupStatus() {
      try {
        this.backupStatus = await GetBackupStatus();
      } catch (err) {
        this.backupStatus = { available: false, backup_available: false, restore_available: false, reason: String(err) };
      }
    },
    async selectBackupDirectory() {
      try {
        const directory = await SelectDirectory();
        if (directory) this.form.backup_directory = directory;
      } catch (err) {
        this.backupMessageIsError = true;
        this.backupMessage = '选择备份目录失败：' + err;
      }
    },
    async createBackupNow() {
      if (this.backupBusy) return;
      this.backupBusy = true;
      this.backupMessage = '正在创建并校验数据库备份...';
      this.backupMessageIsError = false;
      try {
        const backup = await CreateDatabaseBackup();
        this.backupMessage = `备份成功：${backup.name}`;
      } catch (err) {
        this.backupMessageIsError = true;
        this.backupMessage = '备份失败：' + err;
      } finally {
        this.backupBusy = false;
        await this.loadBackupStatus();
      }
    },
    async openBackupDialog() {
      this.backupMessage = '';
      this.backupMessageIsError = false;
      try {
        this.backupFiles = await ListDatabaseBackups();
        this.selectedBackup = null;
        this.showBackupDialog = true;
      } catch (err) {
        this.backupMessageIsError = true;
        this.backupMessage = '读取备份列表失败：' + err;
      }
    },
    closeBackupDialog() {
      if (this.backupBusy) return;
      this.showBackupDialog = false;
      this.selectedBackup = null;
    },
    async confirmRestoreBackup() {
      if (!this.selectedBackup || this.backupBusy) return;
      this.backupBusy = true;
      try {
        await RestoreDatabaseBackup({
          name: this.selectedBackup.name,
          size: this.selectedBackup.size,
          fingerprint: this.selectedBackup.fingerprint
        });
        this.backupMessage = '数据库恢复成功，应用将自动退出；重新打开后即可使用恢复的数据。';
        this.backupMessageIsError = false;
        this.showBackupDialog = false;
        this.selectedBackup = null;
      } catch (err) {
        this.backupMessageIsError = true;
        this.backupMessage = '数据库恢复失败：' + err;
      } finally {
        this.backupBusy = false;
        await this.loadBackupStatus();
      }
    },
    formatBackupTime(value) {
      if (!value) return '未知时间';
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
    },
    formatBackupSize(value) {
      const bytes = Number(value) || 0;
      if (bytes < 1024) return `${bytes} B`;
      if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
      return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
    },
  }
};
</script>

<style scoped>
.database-status {
  display: grid;
  gap: 6px;
  padding: 12px 14px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius-md);
  background: var(--panel-subtle-bg);
  font-size: 13px;
}

.database-status__row { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
.database-status__row > span:first-child { min-width: 72px; color: var(--text-muted); font-size: 12px; }
.database-status__row code { font-family: var(--font-mono); font-size: 12px; color: var(--text-secondary); word-break: break-all; }
.database-status__row small { color: var(--text-muted); font-size: 11.5px; }
.database-status__off { color: var(--warning-text); }

.database-restart-notice {
  margin: 10px 0 0;
  padding: 8px 12px;
  border: 1px solid var(--warning-border);
  border-radius: var(--radius);
  background: var(--warning-soft);
  color: var(--warning-text);
  font-size: 12.5px;
}

.database-switch-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.database-switch-row .select-input { width: auto; min-width: 240px; }

.backup-settings-section {
  grid-column: 1 / -1;
}

.backup-setting-grid {
  grid-template-columns: repeat(2, minmax(180px, 1fr));
}

.backup-message--error,
.backup-danger-text {
  color: var(--danger-color);
}

.backup-file-list {
  display: grid;
  gap: 8px;
  max-height: 360px;
  margin-top: 16px;
  overflow-y: auto;
}

.backup-file-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
}

.backup-file-item div {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.backup-file-item span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

</style>
