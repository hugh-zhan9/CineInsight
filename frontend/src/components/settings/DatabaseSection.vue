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

    <!-- 「待重启」终态（APP-02）：迁移或切回已经成功，重启前库保持只读，唯一出口是立即重启。
         挡住全部操作的全局遮罩由 App 层统一做（主代理裁决 2026-09-30），设置页这里只说明并给按钮，
         同时停用本页其余数据库操作。 -->
    <div v-if="relaunchRequired" class="database-relaunch" data-test="db-relaunch-panel" role="status">
      <strong>需要重启应用</strong>
      <p>{{ relaunchMessage }}</p>
      <p class="help-text">重启前数据库保持只读，其他数据库操作都已停用；重启之后才会使用新的数据库。</p>
      <p v-if="relaunch.result" class="help-text" data-test="db-relaunch-result">{{ relaunch.result.message || '应用即将重新启动。' }}</p>
      <p v-if="relaunch.error" class="backup-danger-text" role="alert" data-test="db-relaunch-error">{{ relaunch.error }}</p>
      <div>
        <button type="button" class="btn-primary" data-test="db-relaunch-now" :disabled="relaunch.busy || Boolean(relaunch.result)" @click="relaunchNow">
          {{ relaunch.busy ? '正在重启...' : '立即重启' }}
        </button>
      </div>
    </div>

    <div class="setting-item">
      <label>切换后端</label>
      <div class="database-switch-row">
        <select v-model="switchTarget" class="select-input" data-test="db-switch-target" :disabled="databaseLocked" @change="resetSwitchChecks">
          <option value="sqlite">SQLite（单文件，无需额外安装）</option>
          <option value="postgres">PostgreSQL（支持语义检索）</option>
        </select>
        <button
          type="button"
          class="btn-secondary"
          :disabled="databaseLocked || switchBusy || switchTarget === databaseStatus.backend"
          data-test="db-preflight"
          @click="preflightDatabaseSwitch"
        >检查目标库</button>
        <button
          type="button"
          class="btn-secondary btn-danger-outline"
          :disabled="!canMigrate"
          data-test="db-switch-start"
          @click="startDatabaseSwitch"
        >{{ switchBusy ? '迁移中...' : '迁移并切换' }}</button>
      </div>
      <p v-if="switchPreflight" class="help-text" data-test="db-preflight-result">{{ preflightText }}</p>
      <!-- 目标库里已经有数据：多半就是之前用过的那个后端，直接切回去（只改配置、不迁移）。 -->
      <div v-if="canConfigOnly || canClearTarget" class="database-switch-row database-switch-row--secondary">
        <button
          v-if="canConfigOnly"
          type="button"
          class="btn-secondary"
          :disabled="configOnlyBusy"
          data-test="db-switch-config-only"
          @click="switchBackendConfigOnly"
        >{{ configOnlyBusy ? '正在切换...' : '切回之前的后端（只改配置）' }}</button>
        <button
          v-if="canClearTarget"
          type="button"
          class="btn-secondary btn-danger-outline"
          data-test="db-clear-target"
          @click="openClearDialog"
        >清空目标库…</button>
      </div>
      <p v-if="switchProgressText" class="help-text" data-test="db-switch-progress">{{ switchProgressText }}</p>
      <p v-if="switchRunning" class="help-text database-switch-running" data-test="db-switch-running-hint">迁移期间后台任务已暂停、数据库拒绝写入。请不要关闭应用；完成后需要重启应用。</p>
      <p v-if="failedTargetLocation" class="help-text" data-test="db-failed-location">
        目标库位置：<code>{{ failedTargetLocation }}</code>。目标库里可能留着没迁移完的数据，清空后可以重新迁移。
      </p>
      <p
        v-if="switchNotice"
        :class="['help-text', 'database-switch-notice', `database-switch-notice--${switchNotice.level}`]"
        data-test="db-switch-notice"
        :role="switchNotice.level === 'error' ? 'alert' : 'status'"
      >{{ switchNotice.text }}</p>
      <p class="help-text">
        迁移是<strong>复制</strong>：原来的库原样保留。之后想回到原来的后端，把它选成目标、点「检查目标库」，
        再用「切回之前的后端」——只改配置、不再迁移。代价是切换之后在新库里产生的改动不会带回旧库。
      </p>
      <p class="help-text">迁移期间会暂停后台任务并拒绝写入；迁移或切回完成后必须重启应用，重启前数据库保持只读。</p>
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
      <!-- 实际解析后的位置（APP-01）：留空、相对路径都会被解析成这里显示的目录。 -->
      <div v-if="backupStatus?.backup_directory" class="backup-directory-actual" data-test="backup-directory-actual">
        <span>当前备份位置：<code>{{ backupStatus.backup_directory }}</code></span>
        <button type="button" class="btn-secondary btn-compact" data-test="backup-reveal" :disabled="databaseLocked" @click="revealBackupDirectory">在访达中显示</button>
      </div>
    </div>
    <div class="setting-grid backup-setting-grid">
      <div class="setting-item">
        <label>保留份数</label>
        <input type="number" min="1" max="100" v-model.number="form.backup_retention_count" class="number-input" />
      </div>
      <div class="setting-item">
        <label>自动备份间隔（小时）</label>
        <input type="number" min="0" max="8760" v-model.number="form.backup_interval_hours" class="number-input" />
        <!-- 自动备份不只在启动时检查（D-PC56 / APP-09）：运行期间每小时看一次是否到期。 -->
        <p class="help-text" data-test="backup-interval-help">0 表示关闭自动备份。应用运行期间每小时检查一次，距上次备份满这个间隔就自动备份。</p>
      </div>
    </div>
    <div class="backup-actions">
      <button type="button" class="btn-primary" data-test="backup-now" :disabled="backupBusy || databaseLocked || !backupStatus?.backup_available" @click="createBackupNow">
        {{ backupBusy ? '处理中...' : '立即备份' }}
      </button>
      <button type="button" class="btn-secondary" data-test="backup-restore-open" :disabled="backupBusy || databaseLocked || !backupStatus?.restore_available" @click="openBackupDialog">从备份恢复</button>
      <button type="button" class="btn-secondary" :disabled="backupBusy" @click="loadBackupStatus">刷新状态</button>
    </div>
    <p v-if="switchRunning" class="help-text" data-test="backup-maintenance-hint">数据库正在迁移，迁移结束前不能备份或恢复。</p>
    <p v-if="backupMessage" class="help-text" data-test="backup-message" :class="{ 'backup-message--error': backupMessageIsError }">{{ backupMessage }}</p>
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
      <p class="backup-danger-text">这是破坏性操作。当前数据库会先自动备份；恢复完成后应用会自动退出，重新打开即可使用恢复的数据。恢复期间请勿关闭应用。</p>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" :disabled="backupBusy" @click="selectedBackup = null">返回</button>
        <button type="button" class="btn-primary btn-danger" :disabled="backupBusy" @click="confirmRestoreBackup">
          {{ backupBusy ? '正在恢复...' : '确认恢复' }}
        </button>
      </div>
    </template>
  </BaseModal>

  <!-- 清空目标库（D-PC55）：必须手输「清空」才放行，输入框不预填。 -->
  <BaseModal v-if="clearDialog.show" stop-modal-clicks data-test="db-clear-dialog" @close="closeClearDialog">
    <h2>清空目标库</h2>
    <p>将清空 <strong>{{ backendLabel(clearDialog.target) }}</strong> 目标库：</p>
    <p><code class="database-clear-location">{{ clearDialog.location || '—' }}</code></p>
    <p class="backup-danger-text">
      {{ clearDialog.target === 'sqlite' ? '会删除这个库文件（只限应用数据目录里的库文件）。' : '会删除这个库里本应用建的全部表，库里的其他表不动。' }}
      目标库里现有的数据会全部丢失，无法撤销。如果这是你以前用过的片库，请改用「切回之前的后端」。当前正在使用的库不受影响。
    </p>
    <label class="database-clear-label" for="db-clear-confirm-input">输入「清空」确认</label>
    <input
      id="db-clear-confirm-input"
      v-model="clearDialog.confirmText"
      type="text"
      class="text-input"
      autocomplete="off"
      data-test="db-clear-confirm-input"
      :disabled="clearDialog.busy"
    />
    <p v-if="clearDialog.error" class="backup-danger-text" role="alert" data-test="db-clear-error">{{ clearDialog.error }}</p>
    <div class="modal-actions">
      <button type="button" class="btn-secondary" :disabled="clearDialog.busy" @click="closeClearDialog">取消</button>
      <button
        type="button"
        class="btn-primary btn-danger"
        data-test="db-clear-confirm"
        :disabled="clearDialog.busy || clearDialog.confirmText.trim() !== CLEAR_CONFIRM_TEXT"
        @click="confirmClearTarget"
      >{{ clearDialog.busy ? '正在清空...' : '清空目标库' }}</button>
    </div>
  </BaseModal>

</template>

<script>
import {
  ClearMigrationTarget, CreateDatabaseBackup, GetBackupStatus, GetDatabaseBackendStatus, GetDatabaseSwitchStatus,
  ListDatabaseBackups, PreflightDatabaseSwitch, RelaunchApp, RestoreDatabaseBackup, RevealBackupDirectory,
  SelectDirectory, StartDatabaseSwitch, SwitchBackendConfigOnly
} from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import { confirmAction } from '../../utils/feedback.js';
import { formatBytes } from '../../utils/mediaDetails.js';

// 与后端 services.ClearMigrationTargetConfirmText 一致。
const CLEAR_CONFIRM_TEXT = '清空';

const RELAUNCH_PENDING = 'relaunch_pending';
const BACKEND_ENV_LOCKED = 'backend_env_locked';
const BACKEND_ENV_LOCKED_TEXT = '当前后端由环境变量 DB_BACKEND 指定，应用内切换在重启后不会生效；请修改这个环境变量后重启应用。';

// 只返回 error 的入口（迁移并切换、恢复备份）用消息开头的原因码表达两种特殊拒绝：
// 「relaunch_pending: …」「backend_env_locked: …」（§1.2b / 修复 I-2）。
export function databaseErrorReason(err) {
  const text = String(err?.message || err || '').trim();
  const match = /^(relaunch_pending|backend_env_locked)\s*:\s*/.exec(text);
  if (!match) return { code: '', text };
  return { code: match[1], text: text.slice(match[0].length) };
}

// SQLite 恢复的两类失败（修复 I-2）：空间不足、复制临时库失败等发生在关闭数据库之前，
// 是普通失败，当前库没被动过；关闭之后才发现 WAL 没写回之类属于致命错误，
// 后端会中止恢复并让应用退出。后者的消息都带「应用必须重启 / 请重启应用」。
export function restoreErrorKind(err) {
  const text = String(err?.message || err || '').trim();
  const fatal = /应用必须重启|请重启应用/.test(text);
  return { fatal, text };
}

// 数据库后端切换与数据库备份两个分区，连同「选择备份 / 确认恢复」「清空目标库」弹窗与
// 「立即重启」终态。备份目录、保留份数与间隔仍是设置表单字段，由设置页统一保存；
// 依赖这些字段的即时动作先经 ensureSaved 提示保存（D-PC57）。
export default {
  name: 'DatabaseSection',
  components: { BaseModal },
  props: {
    form: { type: Object, required: true },
    // (fields, actionLabel) => Promise<boolean>：相关字段有未保存修改时提示先保存。
    ensureSaved: { type: Function, default: null }
  },
  // relaunch-required({ message })：迁移并切换或切回之前的后端成功、或被 relaunch_pending 拒绝时发出，
  // 经 SettingsPage 交给 App 层挂全局「立即重启」遮罩（主代理裁决 2026-09-30）。
  emits: ['relaunch-required'],
  data() {
    return {
      CLEAR_CONFIRM_TEXT,
      databaseStatus: { backend: '', location: '', semantic_available: false, semantic_reason: '', pending_restart: false },
      switchTarget: 'sqlite',
      switchPreflight: null,
      switchStatus: null,
      switchBusy: false,
      switchStatusOff: null,
      switchNotice: null,
      configOnlyBusy: false,
      halfMigratedTarget: '',
      clearDialog: { show: false, target: '', location: '', confirmText: '', busy: false, error: '' },
      relaunch: { required: false, message: '', busy: false, error: '', result: null },
      restoreExit: null,
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
    this.loadSwitchStatus();
    if (window.runtime?.EventsOn) {
      const switchOff = window.runtime.EventsOn('database-switch-state', (status) => {
        this.applySwitchStatus(status);
      });
      if (typeof switchOff === 'function') this.switchStatusOff = switchOff;
    }
  },
  beforeUnmount() {
    this.switchStatusOff?.();
  },
  computed: {
    switchRunning() {
      return Boolean(this.switchStatus?.running);
    },
    relaunchRequired() {
      return this.relaunch.required || Boolean(this.switchStatus?.completed && this.switchStatus?.relaunch_required);
    },
    relaunchMessage() {
      if (this.relaunch.message) return this.relaunch.message;
      if (this.switchStatus?.completed && this.switchStatus?.message) return this.switchStatus.message;
      return '数据库后端已切换完成，请立即重启应用。';
    },
    // 维护或终态下，所有会碰数据库的动作都停用。
    databaseLocked() {
      return this.switchRunning || this.relaunchRequired || Boolean(this.restoreExit);
    },
    // 换目标时 resetSwitchChecks 会清掉上一次的检查结果，所以有结果就是当前目标的结果。
    preflightMatchesTarget() {
      return Boolean(this.switchPreflight);
    },
    envLocked() {
      return this.switchPreflight?.reason_code === BACKEND_ENV_LOCKED;
    },
    canMigrate() {
      return !this.databaseLocked && !this.switchBusy && this.preflightMatchesTarget && Boolean(this.switchPreflight?.empty);
    },
    targetHasData() {
      const preflight = this.switchPreflight;
      return this.preflightMatchesTarget && Boolean(preflight?.reachable) && !preflight?.empty && !this.envLocked;
    },
    canConfigOnly() {
      return !this.databaseLocked && this.targetHasData;
    },
    failedTarget() {
      return this.switchStatus?.failed ? (this.switchStatus.target || '') : '';
    },
    failedTargetLocation() {
      return this.failedTarget ? (this.switchStatus?.location || '') : '';
    },
    // 迁移失败、目标库残留半迁移、或检查发现目标库非空时才给「清空目标库」。
    canClearTarget() {
      if (this.databaseLocked) return false;
      return Boolean(this.failedTarget) || this.halfMigratedTarget === this.switchTarget || this.targetHasData;
    },
    preflightText() {
      if (!this.switchPreflight) return '';
      if (this.envLocked) return BACKEND_ENV_LOCKED_TEXT;
      return this.switchPreflight.message || '';
    },
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
    async gate(fields, actionLabel) {
      if (typeof this.ensureSaved !== 'function') return true;
      return this.ensureSaved(fields, actionLabel);
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
    // 页面重新挂载时补上错过的迁移终态：迁移在后台跑，事件可能在别的页面时就到了。
    async loadSwitchStatus() {
      try {
        const status = await GetDatabaseSwitchStatus();
        if (status && (status.running || status.completed || status.failed)) this.applySwitchStatus(status);
      } catch {
        // 读不到迁移进度不影响其余设置；事件仍是主通道。
      }
    },
    applySwitchStatus(status) {
      this.switchStatus = status || null;
      this.switchBusy = Boolean(status?.running);
      if (status?.completed && status?.relaunch_required) {
        this.openRelaunch(status.message);
      }
      if (status && !status.running) this.loadDatabaseStatus();
    },
    resetSwitchChecks() {
      this.switchPreflight = null;
      this.switchNotice = null;
    },
    openRelaunch(message) {
      const first = !this.relaunch.required;
      this.relaunch = { ...this.relaunch, required: true, message: message || this.relaunch.message || '' };
      this.showBackupDialog = false;
      this.clearDialog = { ...this.clearDialog, show: false };
      // 同一次终态只往上报一次（事件与挂载时的补读可能各到一次）。
      if (first) this.$emit('relaunch-required', { message: this.relaunchMessage });
    },
    // 两种带原因码的拒绝统一在这里处理；返回 true 表示已经处理掉了。
    handleReasonCode(code, text) {
      if (code === RELAUNCH_PENDING) {
        this.openRelaunch(text || '数据库后端已切换完成，请先重启应用。');
        return true;
      }
      if (code === BACKEND_ENV_LOCKED) {
        this.switchNotice = { level: 'warn', text: BACKEND_ENV_LOCKED_TEXT };
        return true;
      }
      return false;
    },
    async preflightDatabaseSwitch() {
      this.switchPreflight = null;
      this.switchNotice = null;
      try {
        this.switchPreflight = await PreflightDatabaseSwitch(this.switchTarget);
      } catch (err) {
        this.switchPreflight = { target: this.switchTarget, empty: false, reachable: false, message: String(err) };
      }
    },
    async startDatabaseSwitch() {
      if (!await confirmAction({
        title: '迁移并切换数据库后端',
        message: `迁移会把当前库的全部数据复制到 ${this.backendLabel(this.switchTarget)}，原库保持不变。`
          + '迁移期间会暂停后台任务并拒绝写入；完成后必须重启应用才会使用新库，重启前数据库保持只读。\n\n'
          + '注意：切换之后在新库里产生的改动不会回到旧库。继续吗？',
        confirmText: '开始迁移'
      })) return;
      this.switchBusy = true;
      this.switchStatus = null;
      this.switchNotice = null;
      try {
        await StartDatabaseSwitch(this.switchTarget);
      } catch (err) {
        this.switchBusy = false;
        const reason = databaseErrorReason(err);
        if (this.handleReasonCode(reason.code, reason.text)) return;
        this.switchStatus = { failed: true, target: '', message: reason.text };
      }
    },
    // 切回之前的后端（D-PC55）：只改配置、不迁移，切换之后的改动不会带回，成功即进入「待重启」。
    async switchBackendConfigOnly() {
      const target = this.switchTarget;
      const label = this.backendLabel(target);
      if (!await confirmAction({
        title: '切回之前的后端',
        message: `只改配置、不迁移数据：重启后改用 ${label} 里原有的数据。\n\n`
          + `注意：切换到当前后端之后产生的改动（新加的视频、标签、观看进度等）不会带回 ${label}。`
          + '确认后应用立即进入只读状态，需要重启才能继续使用。',
        confirmText: '切回并准备重启',
        danger: true
      })) return;
      this.configOnlyBusy = true;
      this.switchNotice = null;
      try {
        const result = await SwitchBackendConfigOnly(target);
        if (result?.switched) {
          this.openRelaunch(result.message);
          return;
        }
        const code = result?.reason_code || '';
        if (this.handleReasonCode(code, result?.message)) return;
        if (code === 'target_half_migrated') this.halfMigratedTarget = target;
        this.switchNotice = { level: 'warn', text: result?.message || '没有切换。' };
      } catch (err) {
        const reason = databaseErrorReason(err);
        if (this.handleReasonCode(reason.code, reason.text)) return;
        this.switchNotice = { level: 'error', text: `切换失败：${reason.text}` };
      } finally {
        this.configOnlyBusy = false;
      }
    },
    openClearDialog() {
      const target = this.failedTarget || this.switchTarget;
      const location = this.failedTarget ? this.failedTargetLocation : (this.switchPreflight?.location || '');
      this.clearDialog = { show: true, target, location, confirmText: '', busy: false, error: '' };
    },
    closeClearDialog() {
      if (this.clearDialog.busy) return;
      this.clearDialog = { ...this.clearDialog, show: false };
    },
    async confirmClearTarget() {
      const dialog = this.clearDialog;
      if (dialog.busy || dialog.confirmText.trim() !== CLEAR_CONFIRM_TEXT) return;
      this.clearDialog = { ...dialog, busy: true, error: '' };
      try {
        const result = await ClearMigrationTarget(dialog.target, dialog.confirmText.trim());
        if (result?.cleared) {
          this.clearDialog = { ...this.clearDialog, show: false, busy: false };
          this.halfMigratedTarget = '';
          if (this.switchStatus?.failed) this.switchStatus = null;
          this.switchPreflight = null;
          this.switchNotice = { level: 'ok', text: `已清空目标库。${result.message || ''}请重新点「检查目标库」再迁移。` };
          return;
        }
        if (result?.reason_code === RELAUNCH_PENDING) {
          this.clearDialog = { ...this.clearDialog, show: false, busy: false };
          this.openRelaunch(result.message);
          return;
        }
        this.clearDialog = { ...this.clearDialog, busy: false, error: result?.message || '没有清空目标库。' };
      } catch (err) {
        this.clearDialog = { ...this.clearDialog, busy: false, error: `清空目标库失败：${err}` };
      }
    },
    async relaunchNow() {
      if (this.relaunch.busy || this.relaunch.result) return;
      this.relaunch = { ...this.relaunch, busy: true, error: '' };
      try {
        const result = await RelaunchApp();
        this.relaunch = { ...this.relaunch, busy: false, result: result || { relaunched: false, message: '' } };
      } catch (err) {
        this.relaunch = { ...this.relaunch, busy: false, error: `重启失败：${err}` };
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
    async revealBackupDirectory() {
      if (!await this.gate(['backup_directory'], '在访达中显示')) return;
      try {
        await RevealBackupDirectory();
      } catch (err) {
        this.backupMessageIsError = true;
        this.backupMessage = '打开备份目录失败：' + err;
      }
    },
    async createBackupNow() {
      if (this.backupBusy || this.databaseLocked) return;
      if (!await this.gate(['backup_directory', 'backup_retention_count'], '立即备份')) return;
      this.backupBusy = true;
      this.backupMessage = '正在创建并校验数据库备份...';
      this.backupMessageIsError = false;
      try {
        const backup = await CreateDatabaseBackup();
        this.backupMessage = `备份成功：${backup.name}`;
      } catch (err) {
        this.backupMessageIsError = true;
        // 维护期间（迁移或恢复进行中）备份会被拒绝，后端给的就是一句中文原因。
        this.backupMessage = '备份失败：' + databaseErrorReason(err).text;
      } finally {
        this.backupBusy = false;
        await this.loadBackupStatus();
      }
    },
    async openBackupDialog() {
      if (this.databaseLocked) return;
      if (!await this.gate(['backup_directory'], '从备份恢复')) return;
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
        // 恢复成功返回成功（§1.2b），后端随后自行放行退出。
        this.backupMessage = '数据库恢复成功，应用将自动退出；重新打开后即可使用恢复的数据。';
        this.backupMessageIsError = false;
        this.showBackupDialog = false;
        this.selectedBackup = null;
        // 应用即将退出：停用本页其余数据库操作，不再发起任何读写。
        this.restoreExit = { fatal: false };
      } catch (err) {
        const reason = databaseErrorReason(err);
        if (this.handleReasonCode(reason.code, reason.text)) return;
        const kind = restoreErrorKind(reason.text);
        this.showBackupDialog = false;
        this.selectedBackup = null;
        if (kind.fatal) {
          // 致命错误后端同样会让应用退出（正式库没被替换）。
          this.restoreExit = { fatal: true };
          this.backupMessageIsError = true;
          this.backupMessage = `数据库恢复已中止，应用即将退出：${kind.text}。重新打开应用后可以再次恢复。`;
        } else {
          this.backupMessageIsError = true;
          this.backupMessage = '数据库恢复失败，当前数据库没有被改动：' + kind.text;
        }
      } finally {
        this.backupBusy = false;
        if (!this.restoreExit) await this.loadBackupStatus();
      }
    },
    formatBackupTime(value) {
      if (!value) return '未知时间';
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
    },
    formatBackupSize(value) {
      return formatBytes(Number(value) || 0);
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
.database-switch-row--secondary { margin-top: 10px; }

.database-relaunch {
  display: grid;
  gap: 6px;
  margin: 10px 0 0;
  padding: 12px 14px;
  border: 1px solid var(--warning-border);
  border-radius: var(--radius);
  background: var(--warning-soft);
  color: var(--warning-text);
  font-size: 13px;
}
.database-relaunch p { margin: 0; }
.database-switch-running { color: var(--warning-text); }

.database-switch-notice { overflow-wrap: anywhere; }
.database-switch-notice--ok { color: var(--success-color); }
.database-switch-notice--warn { color: var(--warning-text); }
.database-switch-notice--error { color: var(--danger-color); }

.database-clear-location,
.backup-directory-actual code { font-family: var(--font-mono); font-size: 12px; word-break: break-all; }
.database-clear-label { display: block; margin: 12px 0 6px; font-size: 13px; font-weight: 600; }

.backup-directory-actual {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 8px;
  color: var(--text-secondary);
  font-size: 12.5px;
}

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
