import { reactive } from 'vue';
import { GetImageCleanupStatus } from '../../wailsjs/go/main/App';

// 图片清理审阅的共享状态：由图片库页持有并持续轮询，清理面板只读写它。
// 这样关闭面板不会中断轮询，后端分析继续在后台跑，工具栏徽标仍可见进度，
// 重新打开面板即从当前状态恢复。
export const photoCleanupStore = reactive({
  status: null,
  polling: false
});

const POLL_INTERVAL_MS = 1000;
const IDLE_INTERVAL_MS = 5000;

let timer = null;

async function tick() {
  try {
    photoCleanupStore.status = await GetImageCleanupStatus();
  } catch (err) {
    // 读取失败不打断轮询；下一轮再试。
  }
  schedule();
}

function schedule() {
  clearTimeout(timer);
  if (!photoCleanupStore.polling) return;
  const running = !!photoCleanupStore.status?.running;
  timer = setTimeout(tick, running ? POLL_INTERVAL_MS : IDLE_INTERVAL_MS);
}

// startPolling 幂等：已在轮询时不重复启动。
export function startPhotoCleanupPolling() {
  if (photoCleanupStore.polling) return;
  photoCleanupStore.polling = true;
  tick();
}

export function stopPhotoCleanupPolling() {
  photoCleanupStore.polling = false;
  clearTimeout(timer);
}
