import { reactive } from 'vue';

// Wails 的 macOS webview 没有实现 WKUIDelegate 的 JS 弹框代理，window.alert 静默丢弃、
// window.confirm 恒返回 false——错误提示看不见、危险操作的确认永远等于"取消"。
// 这里用应用内组件把这两件事接管掉：notify* 出提示条，confirmAction 返回 Promise<boolean>。
export const feedbackState = reactive({
  toasts: [],
  confirm: null
});

const AUTO_DISMISS_MS = 4500;
export const ACTION_DISMISS_MS = 8000;
let nextToastID = 1;
let pendingConfirmResolve = null;

// 后端少数错误没有中文文案（来自数据库层的哨兵错误），在展示前统一换成中文（G-3）。
const BACKEND_ERROR_TRANSLATIONS = [
  ['database is in maintenance mode', '数据库正在恢复备份或切换后端，暂时无法操作；如已提示需要重启，请重启应用'],
  ['sql: database is closed', '数据库连接已关闭，请重启应用'],
];

export function translateBackendError(message) {
  let text = String(message ?? '');
  for (const [source, target] of BACKEND_ERROR_TRANSLATIONS) {
    text = text.split(source).join(target);
  }
  return text;
}

// 错误不自动消失：用户可能正盯着别处，错过了就等于没提示。
// action（D-UD-07/12）：提示里最多一个动作 { label, run }，例如「撤销」「重试」「查看」；点了先收起提示再执行，
// run 抛错或返回的 Promise 失败时改出一条错误提示。带动作的非错误提示默认停留 8 秒（错误照旧常驻）。
export function notify(message, { level = 'info', timeout, action = null } = {}) {
  const text = translateBackendError(message).trim();
  if (!text) return null;
  const toastAction = normalizeAction(action);
  const delay = timeout ?? (level === 'error' ? 0 : toastAction ? ACTION_DISMISS_MS : AUTO_DISMISS_MS);
  const id = nextToastID++;
  feedbackState.toasts.push({ id, level, message: text, action: toastAction });
  if (delay > 0) {
    setTimeout(() => dismissToast(id), delay);
  }
  return id;
}

function normalizeAction(action) {
  if (!action || typeof action.run !== 'function') return null;
  const label = String(action.label ?? '').trim();
  return label ? { label, run: action.run } : null;
}

export function notifyError(message, options = {}) {
  return notify(message, { ...options, level: 'error' });
}

export function notifySuccess(message, options = {}) {
  return notify(message, { ...options, level: 'success' });
}

// 可撤销操作完成后的提示（D-UD-07 第 1 级）：带「撤销」，停留 8 秒。run 调用既有的恢复接口。
export function notifyUndo(message, run) {
  return notify(message, { level: 'success', action: { label: '撤销', run } });
}

// 点提示里的动作：同一条提示只执行一次（先收起），失败另出错误提示。
export async function runToastAction(id) {
  const toast = feedbackState.toasts.find(item => item.id === id);
  if (!toast?.action) return false;
  const { label, run } = toast.action;
  dismissToast(id);
  try {
    await run();
    return true;
  } catch (err) {
    notifyError(`${label}失败：${err?.message || err}`);
    return false;
  }
}

export function dismissToast(id) {
  const index = feedbackState.toasts.findIndex(toast => toast.id === id);
  if (index !== -1) feedbackState.toasts.splice(index, 1);
}

export function clearToasts() {
  feedbackState.toasts.splice(0, feedbackState.toasts.length);
}

// 同一时刻只允许一个确认框：叠加确认没有真实场景，反而容易让用户答错问题。
// 已有确认未决时再发起会先把旧的按"取消"结掉。
// 三级确认（D-UD-07）：可撤销的不确认（做完用 notifyUndo）；level 'confirm'（默认）是普通确认框，写清数量与影响；
// level 'irreversible' 是不可逆操作，确认框多一个输入框，要原样输入按钮上的文字（confirmText）才能点。
// impact 是一行影响说明（例如「文件不动，只删记录」）。danger 不传时由 level 推出：不可逆的一律按危险样式。
export const CONFIRM_LEVELS = ['confirm', 'irreversible'];

export function confirmAction({
  title = '确认操作',
  message = '',
  confirmText = '确定',
  cancelText = '取消',
  level = 'confirm',
  impact = '',
  danger
} = {}) {
  if (pendingConfirmResolve) resolveConfirm(false);
  const confirmLevel = CONFIRM_LEVELS.includes(level) ? level : 'confirm';
  return new Promise(resolve => {
    pendingConfirmResolve = resolve;
    feedbackState.confirm = {
      title,
      message: String(message ?? ''),
      confirmText,
      cancelText,
      level: confirmLevel,
      impact: String(impact ?? ''),
      danger: danger ?? confirmLevel === 'irreversible',
      // 不可逆时要原样输入的文字：就是按钮上的字，全站同一规则。
      requiredText: confirmLevel === 'irreversible' ? String(confirmText) : ''
    };
  });
}

// 不可逆确认的输入是否已经对上（首尾空白不计）。
export function confirmTextMatches(confirm, typed) {
  if (!confirm?.requiredText) return true;
  return String(typed ?? '').trim() === confirm.requiredText;
}

export function resolveConfirm(result) {
  const resolve = pendingConfirmResolve;
  pendingConfirmResolve = null;
  feedbackState.confirm = null;
  if (resolve) resolve(Boolean(result));
}

// 仅测试用：让每个用例从干净状态开始。
export function resetFeedback() {
  clearToasts();
  pendingConfirmResolve = null;
  feedbackState.confirm = null;
}
