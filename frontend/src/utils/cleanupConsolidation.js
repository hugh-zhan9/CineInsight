import { keeperOf, lockedIDs, pruneSelection } from './cleanupSelection.js';

export const CONSOLIDATION_KINDS = ['exact', 'near', 'same-source', 'clip'];
export const CONSOLIDATION_PAGE_SIZE = 40;
export const CONSOLIDATION_STATUS = { running: '整理中', completed: '已完成', failed: '失败', cancelled: '已取消', interrupted: '已中断' };
export const isConsolidationTerminal = status => Boolean(status && status !== 'running');

// 移动范围独立于删除勾选；保护始终包括整批分析，而不是筛选后的卡片。
export function consolidationRequest(groups, { scope = [], overrides = {}, skipped = [], selection = [], external = [] } = {}) {
  const included = new Set(scope);
  const skip = new Set(skipped);
  const locked = new Set([...lockedIDs(groups, overrides, skipped), ...external]);
  const selected = new Set(pruneSelection(selection, groups, { overrides, skipped, locked }));
  const protections = groups.map(group => ({
    kind: group.kind, member_ids: [...group.memberIds], keeper_id: keeperOf(group, overrides) || 0,
    keeper_pinned: Boolean(overrides[group.key]), skipped: skip.has(group.key)
  }));
  return {
    destination: '', protections,
    groups: protections.flatMap((protection, index) => included.has(groups[index].key) && !protection.skipped
      && CONSOLIDATION_KINDS.includes(protection.kind)
      ? [{ ...protection, selected_ids: protection.member_ids.filter(id => selected.has(id)) }].map(({ skipped: _skipped, ...group }) => group)
      : [])
  };
}

export function consolidationGroupIdentity(group) {
  return `${group.kind}:${[...(group.member_ids || group.memberIds || [])].sort((a, b) => a - b).join(',')}`;
}

// 只比较服务回读的组、保护和文件版本，不覆盖用户后来手选的保留项/删除意向。
export function consolidationReviewSignature(review) {
  const groups = (review?.groups || []).map(group => ({
    key: consolidationGroupIdentity(group), keeper: group.keeper_id,
    selected: [...(group.selected_ids || [])].sort((a, b) => a - b)
  })).sort((a, b) => a.key.localeCompare(b.key));
  const analysis = review?.analysis || {};
  const videos = new Map();
  for (const group of [...(analysis.duplicate_groups || []), ...(analysis.near_duplicate_groups || [])]) {
    for (const video of [group.original, ...(group.candidates || [])]) if (video) videos.set(video.id, video);
  }
  for (const group of analysis.same_source_groups || []) for (const video of [group.preferred, group.alternative]) if (video) videos.set(video.id, video);
  for (const group of analysis.clip_groups || []) for (const video of [group.full, group.clip]) if (video) videos.set(video.id, video);
  return JSON.stringify({ task: review?.task_id, groups, locks: [...(review?.locked_ids || [])].sort((a, b) => a - b),
    videos: [...videos.values()].sort((a, b) => a.id - b.id).map(video => [video.id, video.path, video.size, video.updated_at, video.modified_at]) });
}
