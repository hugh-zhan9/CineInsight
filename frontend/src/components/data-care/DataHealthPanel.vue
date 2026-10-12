<template>
  <!-- 数据体检（D-DH-04 / D-DH-05）：手动运行、全程只读、可取消；修复只给结果确定的三项，逐项确认后执行并重跑。 -->
  <section class="data-care-panel data-health" data-test="data-care-panel-health">
    <header class="data-health__head">
      <div class="data-health__intro">
        <h3>数据体检</h3>
        <p>检查数据库的一致性、托管图片与备份状态。体检只读，修复只在你逐项确认后执行。</p>
        <small v-if="report" data-test="data-health-last-run">上次体检：{{ formatTime(report.finished_at) }}</small>
        <small v-else-if="!loading" data-test="data-health-never">还没有体检过</small>
      </div>
      <div class="data-health__actions">
        <button type="button" class="btn-primary" :disabled="running || loading" data-test="data-health-run" @click="run">
          {{ running && progress.phase !== 'repair' ? '体检中…' : '开始体检' }}
        </button>
        <button v-if="running" type="button" class="btn-secondary" data-test="data-health-cancel" @click="cancel">取消</button>
      </div>
    </header>

    <p v-if="running" class="data-health__progress" role="status" data-test="data-health-progress">{{ progressText }}</p>
    <p v-if="error" class="data-health__error" role="alert" data-test="data-health-error">{{ error }}</p>
    <p v-if="notice" class="data-health__notice" role="status" data-test="data-health-notice">{{ notice }}</p>

    <ul v-if="report" class="data-health__checks">
      <li v-for="check in report.checks" :key="check.check" class="data-health__check" :data-test="`data-health-check-${check.check}`">
        <div class="data-health__check-head">
          <span :class="['data-health__status', `data-health__status--${check.status}`]" data-test="data-health-status">{{ statusLabel(check.status) }}</span>
          <strong>{{ checkLabel(check.check) }}</strong>
          <small v-if="check.count" class="data-health__count">{{ check.count }}{{ check.bytes ? ` · ${formatBytes(check.bytes)}` : '' }}</small>
          <span class="data-health__spacer"></span>
          <button
            v-if="check.repairable"
            type="button"
            class="btn-secondary btn-compact"
            :disabled="running"
            :data-test="`data-health-repair-${check.check}`"
            @click="repair(check)"
          >{{ repairLabel(check.check) }}</button>
          <button
            v-if="check.link"
            type="button"
            class="btn-secondary btn-compact"
            :data-test="`data-health-link-${check.check}`"
            @click="follow(check.link)"
          >{{ linkLabel(check.link) }}</button>
        </div>
        <p class="data-health__summary" data-test="data-health-summary">{{ check.summary }}</p>
        <p class="data-health__explain">{{ explanation(check.check) }}</p>
        <ul v-if="check.groups && check.groups.length" class="data-health__groups">
          <li v-for="group in check.groups" :key="group.key" :data-test="`data-health-group-${group.key}`">
            <span class="data-health__group-label">{{ group.label }}</span>
            <small>{{ groupCountText(check, group) }}</small>
            <small v-if="group.examples && group.examples.length" class="data-health__examples">{{ group.examples.join('、') }}</small>
          </li>
        </ul>
        <p v-if="check.examples && check.examples.length" class="data-health__examples">示例：{{ check.examples.join('、') }}</p>
      </li>
    </ul>
    <p v-else-if="!loading && !running" class="data-health__empty" data-test="data-health-empty">点「开始体检」检查数据库与备份。</p>
  </section>
</template>

<script>
import {
  CancelDataHealthCheck, GetLatestDataHealthReport, RepairDataHealth, RunDataHealthCheck
} from '../../../wailsjs/go/main/App';
import { confirmAction } from '../../utils/feedback.js';
import { formatBytes } from '../../utils/mediaDetails.js';

// 各检查项的名称、说明与修复按钮（结论与数量来自后端报告）。
export const DATA_HEALTH_CHECKS = {
  sqlite_integrity: { label: '数据库文件完整性', explain: 'SQLite 的快速完整性检查。有问题时没有自动修复，建议从备份恢复。' },
  foreign_keys: { label: '外键悬挂', explain: '子记录指向的父记录已不存在（多见于老库在关闭外键检查时写入的行）。只报告，不修复。' },
  dangling_refs: {
    label: '引用悬挂',
    explain: '没有外键的引用（各类忽略记录、人脸观测与关系记账、待播队列、规则账本、批量编辑明细）所指的记录已彻底不存在，回收站里的不算。修复删除这些引用行；工作台成品与迁移暂存来源是历史记录，只报告。观看笔记本就比视频保留得久，不算悬挂。',
    repair: '清理悬挂引用',
    danger: true,
    confirm: count => `将删除 ${count} 行可清理的悬挂引用（历史记录不动）。删除后不能撤销，确定继续吗？`,
    done: count => `已删除 ${count} 行悬挂引用，已重新检查这一项`
  },
  missing_unique_indexes: { label: '唯一索引', explain: '启动时应建好的唯一索引。缺失时同一路径或同一组合可能重复；去重要人来判断，这里只报告。' },
  derived_counts: {
    label: '派生计数',
    explain: '字幕与场景索引段数、人脸簇观测数、帧哈希帧数应与明细一致，可按明细重算。播放次数、最近播放时间与图片查看次数不由明细推出，不在检查内。',
    repair: '按明细重算',
    danger: true,
    confirm: count => `将按明细重算 ${count} 行计数（观测已全部移除的人脸簇会一并删除），确定继续吗？`,
    done: count => `已按明细重算 ${count} 行，已重新检查这一项`
  },
  managed_paths: {
    label: '托管图片路径',
    explain: '海报副本、人物头像、作品集封面、想看封面等记录指向数据目录里已经不存在的文件。修复会清空这些路径，之后可以重新补全或导入。整个目录不可用（不存在或链接失效）时其下的记录不检查、也不会被清空。',
    repair: '清空失效路径',
    danger: true,
    confirm: count => `将清空 ${count} 条指向不存在文件的图片路径，清空后要重新补全或导入才有图。确定继续吗？`,
    done: count => `已清空 ${count} 条失效路径，已重新检查这一项`
  },
  backups: { label: '备份状态', explain: '最近一次成功备份、最近一次校验结果与旧格式备份数。备份、校验与恢复都在设置页的「数据库备份」分区。' },
  orphan_files: { label: '孤儿文件', explain: '缩略图、海报副本、字幕备份等所属记录已不存在的文件。回收在「失联」页签。' }
};

const STATUS_LABELS = { ok: '正常', warning: '提醒', problem: '有问题', timeout: '超时', error: '未完成' };
// 跳转：经 App 的命令表打开（设置页分区、数据维护页签）。
const LINKS = {
  'settings:backup': { command: 'action:settings:backup', label: '数据库备份', button: '去设置 · 备份' },
  'data-care:lost': { command: 'action:data-care:lost', label: '失联', button: '去「失联」回收' }
};
const PROGRESS_EVENT = 'data-health-progress';

// 只认后端的固定中文提示；其余错误（含数据库原始报错、路径）一律换成固定说法（G-3）。
const KNOWN_ERRORS = ['数据体检或修复正在进行，请等它结束', '数据体检已取消'];
function errorText(err) {
  const text = String(err?.message || err || '').trim();
  const known = KNOWN_ERRORS.find(message => text.includes(message));
  if (known) return known;
  if (/maintenance|维护/i.test(text)) return '数据库正在维护，请稍后再试';
  return '操作没有完成，请稍后重试';
}

export default {
  name: 'DataHealthPanel',
  props: { directories: { type: Array, default: () => [] } },
  emits: ['open-video-id', 'run-command', 'videos-changed', 'open-library-missing', 'navigate'],
  data() {
    return {
      loading: true,
      running: false,
      progress: { phase: 'idle', check: '', done: 0, total: 0 },
      report: null,
      error: '',
      notice: ''
    };
  },
  computed: {
    progressText() {
      const label = this.checkLabel(this.progress.check);
      if (this.progress.phase === 'repair') return label ? `正在修复：${label}…` : '正在修复…';
      if (!this.progress.total) return '正在准备体检…';
      return label ? `正在检查：${label}（${this.progress.done + 1}/${this.progress.total}）` : `正在收尾（${this.progress.done}/${this.progress.total}）`;
    }
  },
  mounted() {
    // 离开页签再回来时体检可能还在跑：进度跟着推送走，跑完（idle）再读一次报告。
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn(PROGRESS_EVENT, progress => this.applyProgress(progress));
      if (typeof off === 'function') this.progressOff = off;
    }
    this.loadState();
  },
  beforeUnmount() {
    this.progressOff?.();
  },
  methods: {
    formatBytes,
    async loadState() {
      this.loading = true;
      try {
        const state = await GetLatestDataHealthReport();
        this.report = state?.report || null;
        this.running = Boolean(state?.running);
        if (state?.progress) this.progress = { ...this.progress, ...state.progress };
      } catch (err) {
        this.error = `读取体检报告失败：${errorText(err)}`;
      } finally {
        this.loading = false;
      }
    },
    applyProgress(progress) {
      if (!progress) return;
      const wasRunning = this.running;
      this.progress = { ...this.progress, ...progress };
      this.running = progress.phase !== 'idle';
      // 别的实例（或离开前的这一页）发起的体检跑完了：读回新报告。自己发起的由 run() 收尾。
      if (wasRunning && !this.running && !this.ownRun) this.loadState();
    },
    async run() {
      if (this.running) return;
      this.error = '';
      this.notice = '';
      this.running = true;
      this.ownRun = true;
      this.progress = { phase: 'check', check: '', done: 0, total: 0 };
      try {
        this.report = await RunDataHealthCheck();
        this.notice = '体检完成';
      } catch (err) {
        const text = errorText(err);
        if (text.includes('已取消')) this.notice = '已取消体检，这次没有保存报告';
        else this.error = `体检失败：${text}`;
      } finally {
        this.running = false;
        this.ownRun = false;
      }
    },
    async cancel() {
      try {
        await CancelDataHealthCheck();
      } catch (err) {
        this.error = `取消失败：${errorText(err)}`;
      }
    },
    repairableCount(check) {
      return (check.groups || []).filter(group => group.repairable).reduce((sum, group) => sum + Number(group.count || 0), 0);
    },
    async repair(check) {
      const meta = DATA_HEALTH_CHECKS[check.check];
      if (!meta?.repair || this.running) return;
      const confirmed = await confirmAction({
        title: meta.repair, message: meta.confirm(this.repairableCount(check)), confirmText: meta.repair, danger: Boolean(meta.danger)
      });
      if (!confirmed) return;
      this.error = '';
      this.notice = '';
      this.running = true;
      this.ownRun = true;
      this.progress = { phase: 'repair', check: check.check, done: 0, total: 1 };
      try {
        const result = await RepairDataHealth(check.check);
        if (result?.cancelled) {
          this.notice = `已取消，已处理 ${result.repaired} 条`;
        } else {
          // 数字取后端实际改动的行数，不是确认框里预估的数。
          this.notice = meta.done(Number(result?.repaired || 0));
          if (result?.result && this.report) {
            this.report = { ...this.report, checks: this.report.checks.map(item => (item.check === check.check ? result.result : item)) };
          }
        }
      } catch (err) {
        this.error = `${meta.repair}失败：${errorText(err)}`;
      } finally {
        this.running = false;
        this.ownRun = false;
      }
    },
    follow(link) {
      const target = LINKS[link];
      if (target) this.$emit('run-command', target.command, target.label);
    },
    checkLabel(key) {
      return DATA_HEALTH_CHECKS[key]?.label || '';
    },
    explanation(key) {
      return DATA_HEALTH_CHECKS[key]?.explain || '';
    },
    repairLabel(key) {
      return DATA_HEALTH_CHECKS[key]?.repair || '修复';
    },
    linkLabel(link) {
      return LINKS[link]?.button || '查看';
    },
    statusLabel(status) {
      return STATUS_LABELS[status] || status;
    },
    groupCountText(check, group) {
      if (group.detail) return group.detail;
      const suffix = check.check === 'dangling_refs' && !group.repairable ? '（只报告）' : '';
      return `${group.count} 条${suffix}`;
    },
    formatTime(value) {
      if (!value) return '未知时间';
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
    }
  }
};
</script>

<style scoped>
.data-health { display: grid; gap: 14px; }
.data-health__head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
.data-health__intro { display: grid; gap: 4px; min-width: 0; }
.data-health__intro p, .data-health__intro small { color: var(--text-secondary); }
.data-health__actions { display: flex; gap: 8px; }
.data-health__progress, .data-health__notice { color: var(--text-secondary); font-size: 13px; }
.data-health__error { color: var(--danger-text); font-size: 13px; }
.data-health__checks { display: grid; gap: 10px; margin: 0; padding: 0; list-style: none; }
.data-health__check { display: grid; gap: 6px; padding: 12px 14px; border: 1px solid var(--hairline); border-radius: var(--radius-lg); background: var(--panel-bg); }
.data-health__check-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.data-health__spacer { flex: 1 1 auto; }
.data-health__count { color: var(--text-secondary); font-variant-numeric: tabular-nums; }
.data-health__status { padding: 1px 8px; border-radius: 999px; background: var(--control-bg); color: var(--text-secondary); font-size: 11.5px; white-space: nowrap; }
.data-health__status--ok { background: var(--accent-soft); color: var(--accent-text); }
.data-health__status--warning, .data-health__status--timeout { background: var(--warning-soft); color: var(--warning-text); }
.data-health__status--problem, .data-health__status--error { background: var(--danger-soft); color: var(--danger-text); }
.data-health__summary { font-size: 13px; }
.data-health__explain { color: var(--text-muted); font-size: 12px; }
.data-health__groups { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; font-size: 12.5px; }
.data-health__groups li { display: grid; grid-template-columns: minmax(0, 14em) auto minmax(0, 1fr); gap: 10px; align-items: baseline; }
.data-health__group-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.data-health__examples { overflow-wrap: anywhere; color: var(--text-muted); font-size: 12px; }
.data-health__empty { color: var(--text-muted); }
</style>
