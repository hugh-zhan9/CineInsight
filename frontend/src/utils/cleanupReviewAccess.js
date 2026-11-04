import { shallowReactive } from 'vue';
import { keeperOf, lockedIDs } from './cleanupSelection.js';
import { photoCleanupStore } from './photoCleanupStore.js';
import { photoCleanupEntries, photoCleanupGroups, photoCleanupSelectionFilter } from './photoCleanupEntries.js';

const providers = shallowReactive({ video: null });
const consumers = new Map(), pending = new Map();

export function cleanupReviewSnapshot(analysisKey, groups, overrides, skipped, unavailable = [], extraLocks = [], filter = null, selection = []) {
  if (!analysisKey) return null;
  const locked = new Set([...lockedIDs(groups, overrides, skipped), ...extraLocks]);
  const gone = new Set(unavailable.map(Number));
  const candidates = groups.flatMap(group => group.memberIds.filter(id => !locked.has(id) && !gone.has(id) && (!filter || filter(id, group, overrides))).map(id => ({ id, kind: group.kind, keeper_id: keeperOf(group, overrides) || 0 })));
  const data = { analysis_key: String(analysisKey), locked_ids: [...locked].sort((a, b) => a - b), candidates };
  return { ...data, revision: JSON.stringify([data, [...selection].sort((a, b) => a - b)]) };
}

export function readCleanupReview(kind) {
  if (kind === 'video') return providers.video?.() || null;
  const { status, review } = photoCleanupStore;
  if (!status?.completed || status.running || status.stale || !status.analysis || String(status.started_at || '') !== review.key) return null;
  const entries = photoCleanupEntries(status.analysis, review), groups = photoCleanupGroups(entries, review.deletedIDs || []);
  const members = new Map(entries.flatMap(entry => entry.members.map(member => [Number(member.id), member])));
  const filter = photoCleanupSelectionFilter({ entryByKey: new Map(entries.map(entry => [entry.key, entry])), membersByID: members, sameDirOnly: review.sameDirOnly, skippedGroups: review.skippedGroups || {}, overrides: review.keepOverrides, directoryOf: member => String(member?.directory || '').trim() || '未知目录' });
  return cleanupReviewSnapshot(review.key, groups, review.keepOverrides, Object.keys(review.skippedGroups || {}).filter(key => review.skippedGroups[key]), review.deletedIDs || [], [], filter, review.selection || []);
}

export function registerCleanupReview(kind, read, apply) {
  if (read && kind === 'video') providers.video = read;
  consumers.set(kind, apply);
  const draft = pending.get(kind); if (draft) { pending.delete(kind); apply(draft); }
  return () => { if (providers.video === read) providers.video = null; if (consumers.get(kind) === apply) consumers.delete(kind); };
}

export function handoffCleanupReview(kind, revision, ids) {
  const draft = { revision, ids: [...new Set(ids.map(Number))] };
  pending.set(kind, draft);
  const consumer = consumers.get(kind);
  if (consumer) { pending.delete(kind); consumer(draft); }
}
