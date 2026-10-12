// 管理首页「最近的变化」（D-UI-03）：本次会话里已经收到的扫描摘要、下载查重与备份事件。
// 只在内存里，不持久化，重启应用即清空。数据由 App 在它已有的事件处理里喂进来：
//   library-scan-summary      → recordScanSummary(event)
//   download-duplicate-found  → recordDownloadDuplicate(payload)
//   background-tasks          → recordBackgroundTasks(running)（后端没有单独的备份事件，
//                               登记表里 backup 这个 key 消失就是一轮备份结束）
// 管理首页只读 sessionChanges()，新的在前，最多留 SESSION_CHANGE_LIMIT 条。
import { reactive } from 'vue';

export const SESSION_CHANGE_LIMIT = 50;

const SCAN_TRIGGER_LABELS = { startup: '启动时扫描', manual: '手动扫描', directory_change: '添加目录后扫描' };
const SCAN_COUNTS = [['added', '新增'], ['deleted', '移除'], ['stale', '失效'], ['restored', '恢复'], ['relocated', '移动']];

const state = reactive({ items: [] });
let sequence = 0;
let backupRunning = false;

export function sessionChanges() {
  return state.items;
}

// kind：scan / download-duplicate / backup；page：点开这一条时去的页面 key（可空）。
export function recordSessionChange({ kind, title, detail = '', page = '', at = Date.now() }) {
  if (!kind || !title) return null;
  const entry = { id: ++sequence, kind, title, detail, page, at };
  state.items.unshift(entry);
  if (state.items.length > SESSION_CHANGE_LIMIT) state.items.splice(SESSION_CHANGE_LIMIT);
  return entry;
}

export function scanSummaryDetail(result) {
  const parts = SCAN_COUNTS.filter(([key]) => Number(result?.[key] || 0) > 0).map(([key, label]) => `${label} ${Number(result[key])}`);
  const errors = Array.isArray(result?.errors) ? result.errors.length : 0;
  if (errors > 0) parts.push(`${errors} 处出错`);
  return parts.length ? parts.join(' · ') : '没有变化';
}

export function recordScanSummary(event) {
  const result = event?.result;
  if (!result) return null;
  return recordSessionChange({
    kind: 'scan',
    title: SCAN_TRIGGER_LABELS[event.trigger] || '扫描完成',
    detail: scanSummaryDetail(result),
    page: 'library-manage'
  });
}

export function recordDownloadDuplicate(payload) {
  const what = payload?.kind === 'exact' ? '完全相同的文件' : '同一部的其他版本';
  return recordSessionChange({
    kind: 'download-duplicate',
    title: '下载查重',
    detail: `《${payload?.title || '视频'}》片库里已有${what}`,
    page: 'downloads'
  });
}

// 运行中的任务 key 列表；backup 从「在跑」变成「不在」时记一条。只认得结束，不猜成败。
export function recordBackgroundTasks(running) {
  const now = Array.isArray(running) && running.includes('backup');
  const ended = backupRunning && !now;
  backupRunning = now;
  if (!ended) return null;
  return recordSessionChange({ kind: 'backup', title: '数据库备份', detail: '一轮备份已结束，结果在设置的数据库一节查看', page: 'settings' });
}

// 只给测试用：清空本次会话的记录。
export function resetSessionChanges() {
  state.items.splice(0);
  sequence = 0;
  backupRunning = false;
}
