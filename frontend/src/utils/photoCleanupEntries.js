import { cleanupGroup, keeperOf } from './cleanupSelection.js';

// 原图片审阅与只读空间方案共用同一份成员投影，保留决定仍由photoCleanupStore持有。
export function photoCleanupEntries(analysis, review, directoryOf = image => String(image?.directory || '').trim() || '未知目录') {
  if (!analysis) return [];
  const dismissed = new Set(review.dismissedKeys || []);
  const flatten = (groups, kind) => (groups || []).flatMap(group => {
    const key = `${kind}-${group.original?.id ?? 'nogroup'}`;
    if (dismissed.has(key)) return [];
    const removed = new Set((review.removedMembers?.[key] || []).map(Number));
    const members = [group.original, ...(group.candidates || [])].filter(Boolean).filter(member => !removed.has(Number(member.id)));
    if (members.length < 2) return [];
    const directories = new Set(members.map(directoryOf));
    const suggestedKeeperID = members.some(member => Number(member.id) === Number(group.original?.id)) ? Number(group.original.id) : Number(members[0].id);
    return [{ kind, group, members, key, suggestedKeeperID, directoryCount: directories.size, spansDirectories: directories.size > 1 }];
  });
  return [...flatten(analysis.duplicate_groups, 'exact'), ...flatten(analysis.near_duplicate_groups, 'near')];
}

export function photoCleanupGroups(entries, deletedIDs) {
  const deleted = new Set(deletedIDs.map(Number));
  return entries.map(entry => cleanupGroup({ key: entry.key, kind: entry.kind, keeperId: entry.suggestedKeeperID, memberIds: entry.members.map(member => member.id), switchable: true, unavailable: entry.members.filter(member => deleted.has(Number(member.id))).map(member => member.id) }));
}

export function photoCleanupSelectionFilter({ entryByKey, membersByID, sameDirOnly, skippedGroups, overrides, directoryOf }) {
  const directories = new WeakMap();
  return (id, group, currentOverrides = overrides) => {
    if (skippedGroups[group.key]) return false;
    if (!sameDirOnly) return true;
    const entry = entryByKey.get(group.key); if (!entry) return false;
    if (!directories.has(currentOverrides)) directories.set(currentOverrides, new Map());
    const byGroup = directories.get(currentOverrides);
    if (!byGroup.has(group.key)) byGroup.set(group.key, directoryOf(membersByID.get(keeperOf(group, currentOverrides)) || entry.group.original));
    return directoryOf(membersByID.get(Number(id))) === byGroup.get(group.key);
  };
}
