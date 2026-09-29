// 清理中心统一的勾选与锁定规则（D-PC49），视频清理面板与图片清理页共用。
//
// 组的统一形状（cleanupGroup 负责归一）：
//   { key, kind, keeperId, memberIds, switchable }
//   - kind：exact / near / same-source / clip / low-resolution / low-duration（图片只有前两种）；
//   - keeperId：后端按整理成果排序给出的建议保留项；极短、极低这类单条候选没有保留项，为 null；
//   - switchable：能否用「设为保留」换保留项。精确 / 近似重复可以；同源与截取片段的保留项固定。
//
// 规则：
//   - 默认只勾精确重复的非保留项；近似重复、同源、截取片段默认不勾（恢复图片设计「近似重复一律不勾」）；
//   - 任何一组的保留项都锁定，在别的组里也不能勾——否则会出现「这组要留、那组要删」，
//     合并元数据也会合进一个马上要删掉的保留项；
//   - 「按建议勾选本组」只作用于传入的这一组；
//   - 换保留项后新保留项移出勾选，原保留项只解除锁定；本组原先是「按建议全勾」时按新保留项重新全勾。

export const DEFAULT_SELECTED_KINDS = Object.freeze(['exact']);

// 删除确认里各类别的叫法（与类别标题一致，不带阈值）。
export const CLEANUP_KIND_LABELS = Object.freeze({
  exact: '精确重复',
  near: '近似重复',
  'same-source': '同源视频',
  clip: '截取片段',
  'low-resolution': '极低分辨率',
  'low-duration': '极短片段'
});

// 按相似度判断、需要人工逐组确认的类别：删除确认里单独提醒。
export const SIMILARITY_KINDS = Object.freeze(['near', 'same-source', 'clip']);

function toID(value) {
  const id = Number(value);
  return Number.isFinite(id) && id > 0 ? id : null;
}

function uniqueIDs(values) {
  const seen = new Set();
  const ids = [];
  for (const value of values || []) {
    const id = toID(value);
    if (id === null || seen.has(id)) continue;
    seen.add(id);
    ids.push(id);
  }
  return ids;
}

export function cleanupGroup({ key, kind, keeperId = null, memberIds = [], switchable = false }) {
  const members = uniqueIDs(memberIds);
  const keeper = toID(keeperId);
  return {
    key: String(key),
    kind,
    keeperId: keeper !== null && members.includes(keeper) ? keeper : null,
    memberIds: members,
    switchable: Boolean(switchable)
  };
}

// 当前保留项：用户「设为保留」过就用它，否则用建议保留项。
export function keeperOf(group, overrides = {}) {
  if (!group || group.keeperId === null) return null;
  const override = toID(overrides?.[group.key]);
  if (group.switchable && override !== null && group.memberIds.includes(override)) return override;
  return group.keeperId;
}

export function lockedIDs(groups, overrides = {}) {
  const locked = new Set();
  for (const group of groups || []) {
    const keeper = keeperOf(group, overrides);
    if (keeper !== null) locked.add(keeper);
  }
  return locked;
}

export function isLocked(id, locked) {
  const value = toID(id);
  return value !== null && Boolean(locked?.has(value));
}

function normalizedOptions(groups, options = {}) {
  const overrides = options.overrides || {};
  return {
    overrides,
    locked: options.locked || lockedIDs(groups, overrides),
    excluded: new Set(uniqueIDs(options.excluded || [])),
    filter: typeof options.filter === 'function' ? options.filter : null
  };
}

// 本组「按建议」该勾的 ID：保留项之外、没被任何一组锁定、没排除（已删除）、并且通过
// filter(id, group, overrides)（例如只勾与保留项同目录的副本）。
// options.locked 要用全部组算出的 lockedIDs 传进来；不传时只按本组的保留项锁定。
export function suggestedIDs(group, options = {}) {
  if (!group) return [];
  const { overrides, locked, excluded, filter } = normalizedOptions([group], options);
  const keeper = keeperOf(group, overrides);
  return group.memberIds.filter(id => id !== keeper
    && !locked.has(id)
    && !excluded.has(id)
    && (!filter || filter(id, group, overrides)));
}

export function defaultSelection(groups, options = {}) {
  const resolved = normalizedOptions(groups, options);
  const ids = [];
  for (const group of groups || []) {
    if (!DEFAULT_SELECTED_KINDS.includes(group.kind)) continue;
    ids.push(...suggestedIDs(group, resolved));
  }
  return uniqueIDs(ids);
}

export function isGroupFullySuggested(selection, group, options = {}) {
  const suggested = suggestedIDs(group, options);
  const selected = new Set(uniqueIDs(selection));
  return suggested.length > 0 && suggested.every(id => selected.has(id));
}

export function applySuggestion(selection, group, options = {}) {
  return uniqueIDs([...(selection || []), ...suggestedIDs(group, options)]);
}

// 取消本组勾选：只放掉本组成员。
export function clearGroupSelection(selection, group) {
  const members = new Set(group?.memberIds || []);
  return uniqueIDs(selection).filter(id => !members.has(id));
}

export function toggleSuggestion(selection, group, options = {}) {
  return isGroupFullySuggested(selection, group, options)
    ? clearGroupSelection(selection, group)
    : applySuggestion(selection, group, options);
}

// 切换保留项。groups 是全部组（锁定要跨组算），返回新的 overrides 与 selection，不改入参。
export function setKeeper({ groups, overrides = {}, selection = [], excluded = [], filter = null }, group, id) {
  const target = toID(id);
  if (!group || !group.switchable || target === null || !group.memberIds.includes(target)) {
    return { overrides, selection };
  }
  const wasFull = isGroupFullySuggested(selection, group, { overrides, excluded, filter, locked: lockedIDs(groups, overrides) });
  const nextOverrides = { ...overrides, [group.key]: target };
  const nextLocked = lockedIDs(groups, nextOverrides);
  let nextSelection = uniqueIDs(selection).filter(item => !nextLocked.has(item));
  if (wasFull) {
    // 本组按新保留项重算：先放掉本组成员，再按建议全勾（filter 可能因保留项换目录而收窄）。
    nextSelection = applySuggestion(clearGroupSelection(nextSelection, group), group, { overrides: nextOverrides, locked: nextLocked, excluded, filter });
  }
  return { overrides: nextOverrides, selection: nextSelection };
}

// 结果或保留项变化之后收窄勾选：去掉被锁定、已排除、不再出现在任何组里的 ID。
export function pruneSelection(selection, groups, options = {}) {
  const { locked, excluded } = normalizedOptions(groups, options);
  const present = new Set((groups || []).flatMap(group => group.memberIds));
  return uniqueIDs(selection).filter(id => present.has(id) && !locked.has(id) && !excluded.has(id));
}

// 删除前的合并计划（D-PC48）：每个有保留项的组里被勾选的成员合并到该组当前保留项。
// 同一个保留项出现在几组里时合成一次调用。极短、极低这类没有保留项的候选不参与。
export function mergePlan(groups, selection, overrides = {}) {
  const selected = new Set(uniqueIDs(selection));
  const byKeeper = new Map();
  for (const group of groups || []) {
    const keeper = keeperOf(group, overrides);
    if (keeper === null || selected.has(keeper)) continue;
    const sources = group.memberIds.filter(id => id !== keeper && selected.has(id));
    if (!sources.length) continue;
    const entry = byKeeper.get(keeper) || { keeperId: keeper, sourceIds: [], kinds: [] };
    entry.sourceIds = uniqueIDs([...entry.sourceIds, ...sources]);
    if (!entry.kinds.includes(group.kind)) entry.kinds.push(group.kind);
    byKeeper.set(keeper, entry);
  }
  return [...byKeeper.values()];
}

// 删除前汇总：条数、总大小、各类别条数。一条可能同时落在几个类别里，类别计数因此可以重叠，总条数不重。
export function selectionSummary(groups, selection, sizeOf = () => 0) {
  const ids = uniqueIDs(selection);
  const selected = new Set(ids);
  const byKind = {};
  for (const group of groups || []) {
    const hits = group.memberIds.filter(id => selected.has(id));
    if (!hits.length) continue;
    byKind[group.kind] = byKind[group.kind] || new Set();
    for (const id of hits) byKind[group.kind].add(id);
  }
  const counts = {};
  for (const [kind, set] of Object.entries(byKind)) counts[kind] = set.size;
  const bytes = ids.reduce((sum, id) => sum + (Number(sizeOf(id)) || 0), 0);
  return { count: ids.length, bytes, byKind: counts };
}

// 「其中精确重复 2 个、近似重复 1 个」：按固定类别顺序，只列非零项。
export function describeSelectionKinds(summary, unit = '个') {
  const order = ['exact', 'near', 'same-source', 'clip', 'low-resolution', 'low-duration'];
  return order
    .filter(kind => summary?.byKind?.[kind] > 0)
    .map(kind => `${CLEANUP_KIND_LABELS[kind]} ${summary.byKind[kind]} ${unit}`)
    .join('、');
}

export function similarityCount(summary) {
  return SIMILARITY_KINDS.reduce((sum, kind) => sum + (summary?.byKind?.[kind] || 0), 0);
}

// 卡片上的整理成果标记（D-PC48）：视频 8 项，图片只有收藏、评分、人物、标签四项。
// text 是卡片上的短标记，title 是悬停说明；保留建议优先留整理成果多的那份。
const CURATION_ITEMS = [
  ['favorite', '★ 收藏', '已收藏'],
  ['liked', '♥ 点赞', '已点赞'],
  ['rating', '评分', '有个人评分'],
  ['people', '人物', '关联了人物'],
  ['tags', '标签', '有手动标签（自动标签不算）'],
  ['subtitle', '字幕', '有同名外挂字幕'],
  ['collections', '作品集', '在作品集里'],
  ['progress', '进度', '看过或有观看进度']
];

export function curationBadges(curation) {
  if (!curation) return [];
  return CURATION_ITEMS
    .filter(([key]) => Boolean(curation[key]))
    .map(([key, text, title]) => ({ key, text, title }));
}
