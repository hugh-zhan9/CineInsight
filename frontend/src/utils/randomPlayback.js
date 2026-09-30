// 片库随机播放的本机偏好（D-PC44、PLAY-08）：随机模式与「最近 12 次」排除表存在 localStorage，
// 重启后仍然生效。只存 ID 数组，读写都包 try/catch——隐私模式或存储被禁用时照常可用，只是不记忆。

export const RANDOM_MODE_STORAGE_KEY = 'library-random-mode';
export const RANDOM_RECENT_STORAGE_KEY = 'library-random-recent';
export const RANDOM_RECENT_LIMIT = 12;
// 随机播放启动后允许「换一个」（不计入统计）的时长，与后端 randomRerollWindow 一致。
export const RANDOM_REROLL_WINDOW_MS = 30 * 1000;
export const RANDOM_MODES = ['balanced', 'unwatched', 'favorites'];

function localStore() {
  try {
    return window.localStorage || null;
  } catch (_err) {
    return null;
  }
}

export function loadRandomMode() {
  try {
    const value = localStore()?.getItem(RANDOM_MODE_STORAGE_KEY);
    return RANDOM_MODES.includes(value) ? value : 'balanced';
  } catch (_err) {
    return 'balanced';
  }
}

export function saveRandomMode(mode) {
  if (!RANDOM_MODES.includes(mode)) return;
  try {
    localStore()?.setItem(RANDOM_MODE_STORAGE_KEY, mode);
  } catch (_err) {}
}

// 去重、去掉非法值，保留最近的 12 个（后出现的算更近）。
export function normalizeRecentRandomIDs(ids) {
  const result = [];
  for (const value of Array.isArray(ids) ? ids : []) {
    const id = Number(value);
    if (!Number.isInteger(id) || id <= 0) continue;
    const existing = result.indexOf(id);
    if (existing !== -1) result.splice(existing, 1);
    result.push(id);
  }
  return result.slice(-RANDOM_RECENT_LIMIT);
}

export function loadRecentRandomIDs() {
  try {
    const raw = localStore()?.getItem(RANDOM_RECENT_STORAGE_KEY);
    if (!raw) return [];
    return normalizeRecentRandomIDs(JSON.parse(raw));
  } catch (_err) {
    return [];
  }
}

export function saveRecentRandomIDs(ids) {
  try {
    localStore()?.setItem(RANDOM_RECENT_STORAGE_KEY, JSON.stringify(normalizeRecentRandomIDs(ids)));
  } catch (_err) {}
}

export function rerollWindowOpen(startedAt, now = Date.now()) {
  const started = Number(startedAt) || 0;
  return started > 0 && now - started < RANDOM_REROLL_WINDOW_MS;
}
