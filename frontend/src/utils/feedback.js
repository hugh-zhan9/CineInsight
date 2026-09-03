import { reactive } from 'vue';

// Wails 的 macOS webview 没有实现 WKUIDelegate 的 JS 弹框代理，window.alert 静默丢弃、
// window.confirm 恒返回 false——错误提示看不见、危险操作的确认永远等于"取消"。
// 这里用应用内组件把这两件事接管掉：notify* 出提示条，confirmAction 返回 Promise<boolean>。
export const feedbackState = reactive({
  toasts: [],
  confirm: null
});

const AUTO_DISMISS_MS = 4500;
let nextToastID = 1;
let pendingConfirmResolve = null;

// 错误不自动消失：用户可能正盯着别处，错过了就等于没提示。
export function notify(message, { level = 'info', timeout = level === 'error' ? 0 : AUTO_DISMISS_MS } = {}) {
  const text = String(message ?? '').trim();
  if (!text) return null;
  const id = nextToastID++;
  feedbackState.toasts.push({ id, level, message: text });
  if (timeout > 0) {
    setTimeout(() => dismissToast(id), timeout);
  }
  return id;
}

export function notifyError(message) {
  return notify(message, { level: 'error' });
}

export function notifySuccess(message) {
  return notify(message, { level: 'success' });
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
export function confirmAction({ title = '确认操作', message = '', confirmText = '确定', cancelText = '取消', danger = false } = {}) {
  if (pendingConfirmResolve) resolveConfirm(false);
  return new Promise(resolve => {
    pendingConfirmResolve = resolve;
    feedbackState.confirm = { title, message: String(message ?? ''), confirmText, cancelText, danger };
  });
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
