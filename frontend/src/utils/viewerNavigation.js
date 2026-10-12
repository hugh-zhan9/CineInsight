// 观看区的两条路由规则，App.vue 用：「打开一部片」按调用方区域分流（D-UI-07），
// 以及整页详情的返回栈（D-UI-10）。都是纯函数，状态在 App.vue。
import { COMMAND_PALETTE_SOURCE, pageArea, VIDEO_DETAIL_PAGE } from './appCommands.js';

// 不是页面的调用方（常挂载的面板）各自归属的区域；页面调用方按页面 key 所在的区域。
const PANEL_SOURCE_AREAS = {
  'playback-queue': 'viewer',
  'task-center': 'manage'
};

// from：调用方的页面 key（'home'、'recap'、'insights'…）或上面的面板名；
// 命令面板（COMMAND_PALETTE_SOURCE）两侧都能打开，不属于哪一侧，跟着当前页面所在的区域走。
// 观看区的调用方进整页详情（返回 true）；管理区以及认不出的来源维持现在的「切到片库管理 + 抽屉」。
export function opensVideoDetail(from, currentPage) {
  const area = from === COMMAND_PALETTE_SOURCE
    ? pageArea(currentPage)
    : (PANEL_SOURCE_AREAS[from] || pageArea(from));
  return area === 'viewer';
}

export const DETAIL_RETURN_LIMIT = 20;

function isDetailOf(entry, videoID) {
  return entry?.page === VIDEO_DETAIL_PAGE && Number(entry.params?.videoId) === Number(videoID);
}

// 打开 videoID 的整页详情前把来源压栈。entry = { page, params, scrollTop }。
// - 只记观看区的来源：从管理区或面板进来时没有可回的观看页，栈保持原样（App 让「返回」回首页）；
// - 同一部片不重复压栈：来源就是这部片自己时不动；这部片已在栈里（之前看过它的详情）就退回到它
//   压栈前的那一层，等于沿原路回到它，返回时回到当初打开它的地方；
// - 最多 DETAIL_RETURN_LIMIT 层，超出丢最早的。
export function pushDetailReturn(stack, entry, videoID) {
  if (pageArea(entry?.page) !== 'viewer' || isDetailOf(entry, videoID)) return stack;
  const existing = stack.findIndex(item => isDetailOf(item, videoID));
  if (existing >= 0) return stack.slice(0, existing);
  const next = [...stack, entry];
  return next.length > DETAIL_RETURN_LIMIT ? next.slice(next.length - DETAIL_RETURN_LIMIT) : next;
}

// 「返回」：弹出最近一层。栈空时 entry 为 null。
export function popDetailReturn(stack) {
  if (!stack.length) return { entry: null, stack };
  return { entry: stack[stack.length - 1], stack: stack.slice(0, -1) };
}

export const SCROLL_RESTORE_TIMEOUT_MS = 3000;

// 返回到重新挂载的页面时，内容是异步取回来的：每帧看一次滚动容器，等它长到能滚到目标位置再放回去；
// 最多等 SCROLL_RESTORE_TIMEOUT_MS，到点还不够就作罢（不滚到半截）。返回取消函数（切页时调用）。
export function restoreScrollWhenReachable(el, top, timeoutMs = SCROLL_RESTORE_TIMEOUT_MS) {
  const target = Math.max(0, Number(top) || 0);
  const deadline = Date.now() + timeoutMs;
  let frame = 0;
  const attempt = () => {
    frame = 0;
    if (Math.max(0, el.scrollHeight - el.clientHeight) >= target) {
      el.scrollTop = target;
      return;
    }
    if (Date.now() >= deadline) return;
    frame = requestAnimationFrame(attempt);
  };
  attempt();
  return () => {
    if (frame) cancelAnimationFrame(frame);
    frame = 0;
  };
}
