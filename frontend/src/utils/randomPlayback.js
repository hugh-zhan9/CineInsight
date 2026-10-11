// 片库随机播放的本机偏好（D-PC44、PLAY-08）：随机条件与「最近 12 次」排除表存在 localStorage，
// 重启后仍然生效。只存条件与 ID 数组，读写都包 try/catch——隐私模式或存储被禁用时照常可用，只是不记忆。
//
// 2026-10-11 随机播放与「今晚看什么」合并：两者共用一套随机条件（观看状态、仅收藏、整片时长上限），
// 存在 library-random-conditions；旧的 library-random-mode（均衡 / 仅未看 / 仅收藏）在新键缺失时迁移读入。

export const RANDOM_CONDITIONS_STORAGE_KEY = 'library-random-conditions';
export const LEGACY_RANDOM_MODE_STORAGE_KEY = 'library-random-mode';
export const RANDOM_RECENT_STORAGE_KEY = 'library-random-recent';
export const RANDOM_RECENT_LIMIT = 12;
// 随机播放启动后允许「换一个」（不计入统计）的时长，与后端 randomRerollWindow 一致。
export const RANDOM_REROLL_WINDOW_MS = 30 * 1000;
export const RANDOM_WATCH_STATES = ['unwatched', 'any', 'watched'];
export const RANDOM_WATCH_LABELS = { unwatched: '探索', any: '不限', watched: '重温' };
export const RANDOM_MAX_MINUTES = 1440;
export const DEFAULT_RANDOM_CONDITIONS = Object.freeze({ watchState: 'any', favoritesOnly: false, maxMinutes: 0 });

function localStore() {
  try {
    return window.localStorage || null;
  } catch (_err) {
    return null;
  }
}

export function validRandomMinutes(value) {
  return Number.isInteger(value) && value >= 0 && value <= RANDOM_MAX_MINUTES;
}

// 不认识的取值逐项回落到默认，不让一处坏值连带清掉其余条件。
export function normalizeRandomConditions(value) {
  const source = value && typeof value === 'object' ? value : {};
  return {
    watchState: RANDOM_WATCH_STATES.includes(source.watchState) ? source.watchState : DEFAULT_RANDOM_CONDITIONS.watchState,
    favoritesOnly: source.favoritesOnly === true,
    maxMinutes: validRandomMinutes(source.maxMinutes) ? source.maxMinutes : DEFAULT_RANDOM_CONDITIONS.maxMinutes
  };
}

function conditionsFromLegacyMode(mode) {
  if (mode === 'unwatched') return { ...DEFAULT_RANDOM_CONDITIONS, watchState: 'unwatched' };
  if (mode === 'favorites') return { ...DEFAULT_RANDOM_CONDITIONS, favoritesOnly: true };
  return { ...DEFAULT_RANDOM_CONDITIONS };
}

export function loadRandomConditions() {
  try {
    const store = localStore();
    const raw = store?.getItem(RANDOM_CONDITIONS_STORAGE_KEY);
    if (raw) return normalizeRandomConditions(JSON.parse(raw));
    return conditionsFromLegacyMode(store?.getItem(LEGACY_RANDOM_MODE_STORAGE_KEY));
  } catch (_err) {
    return { ...DEFAULT_RANDOM_CONDITIONS };
  }
}

export function saveRandomConditions(conditions) {
  try {
    localStore()?.setItem(RANDOM_CONDITIONS_STORAGE_KEY, JSON.stringify(normalizeRandomConditions(conditions)));
  } catch (_err) {}
}

// 发给后端 RandomPlayRequest 的条件字段（随机播放、随机 N 部、今晚看什么共用）。
export function randomConditionFields(conditions) {
  const normalized = normalizeRandomConditions(conditions);
  return {
    watch_state: normalized.watchState,
    favorites_only: normalized.favoritesOnly,
    max_duration_seconds: normalized.maxMinutes * 60
  };
}

// 生效条件的短标签，用在随机按钮上；不限、不限收藏、不限时长都不出现。
export function randomConditionTags(conditions) {
  const normalized = normalizeRandomConditions(conditions);
  const tags = [];
  if (normalized.watchState !== 'any') tags.push(RANDOM_WATCH_LABELS[normalized.watchState]);
  if (normalized.favoritesOnly) tags.push('收藏');
  if (normalized.maxMinutes > 0) tags.push(`≤${normalized.maxMinutes}分钟`);
  return tags;
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
