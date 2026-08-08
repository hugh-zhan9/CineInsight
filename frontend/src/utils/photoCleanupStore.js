import { reactive } from 'vue';
import { GetImageCleanupStatus } from '../../wailsjs/go/main/App';

// 图片清理审阅的共享状态：由图片库页持有，清理面板只读写它。
// 这样关闭面板不会中断跟踪，后端分析继续在后台跑，工具栏徽标仍可见进度，
// 重新打开面板即从当前状态恢复。
// review 保存"这一批分析结果"的审阅进度（key 取分析的 started_at）。放在 store 里而不是
// 面板组件里，关闭面板再打开才能接着上次的勾选继续，而不是从头再来一遍。
function emptyReview(key = '') {
  return { key, keepOverrides: {}, skippedGroups: {}, collapsedDirs: {}, selection: [], deletedIDs: [] };
}

export const photoCleanupStore = reactive({
  status: null,
  polling: false,
  review: emptyReview()
});

// 清空审阅进度并绑定到新的分析批次。是否该清空由调用方比较 key 决定。
export function resetPhotoCleanupReview(key) {
  photoCleanupStore.review = emptyReview(key);
}

const POLL_INTERVAL_MS = 1000;

let timer = null;

async function tick() {
  try {
    photoCleanupStore.status = await GetImageCleanupStatus();
  } catch (err) {
    // 读取失败不打断轮询；下一轮再试。
  }
  schedule();
}

// 只在分析运行期间轮询。分析结束后结果不会自己变化，继续轮询只会用新对象
// 覆盖 status，把用户正在审阅的勾选/保留项/折叠状态重置掉。
function schedule() {
  clearTimeout(timer);
  if (!photoCleanupStore.polling) return;
  if (!photoCleanupStore.status?.running) return;
  timer = setTimeout(tick, POLL_INTERVAL_MS);
}

// startPolling 幂等：已在跟踪时不重复启动。
export function startPhotoCleanupPolling() {
  if (photoCleanupStore.polling) return;
  photoCleanupStore.polling = true;
  tick();
}

// 启动新分析后调用，让已停在空闲态的轮询重新跟上。
export function resumePhotoCleanupPolling() {
  schedule();
}

// 主动拉一次状态。空闲时不轮询，所以打开面板、删除或恢复图片之后要显式调一次，
// 否则后端标记的"结果已过期"和待审阅计数永远传不到界面上。
export async function refreshPhotoCleanupStatus() {
  await tick();
}

export function stopPhotoCleanupPolling() {
  photoCleanupStore.polling = false;
  clearTimeout(timer);
}
