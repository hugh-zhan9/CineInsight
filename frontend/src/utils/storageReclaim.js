// 存储体检「可回收空间」（2026-10-12）：把视频与图片清理分析的缓存结果折成按类别的可释放空间。
// 口径与清理中心的默认建议一致：每组保留推荐项（original / preferred / full），其余成员算可回收；
// 极短、极低分辨率是单条候选，整条算。同一个文件落在几类里时，各类各算一次，总数只算一次。
// 精确重复是「确定可回收」，其余类别要在清理中心逐组确认，算「待确认」。
import { CLEANUP_KIND_LABELS } from './cleanupSelection.js';

// pick 返回 [{ keeper, items }]：keeper 是这一组建议保留的那份（单条候选为 null），items 是可回收的成员。
function groupsOf(groups) {
  return (groups || []).map(group => ({ keeper: group?.original, items: group?.candidates || [] }));
}

function singles(items) {
  return (items || []).map(item => ({ keeper: null, items: [item] }));
}

const VIDEO_KINDS = [
  { kind: 'exact', certain: true, pick: analysis => groupsOf(analysis.duplicate_groups) },
  { kind: 'near', pick: analysis => groupsOf(analysis.near_duplicate_groups) },
  { kind: 'same-source', pick: analysis => (analysis.same_source_groups || []).map(group => ({ keeper: group?.preferred, items: [group?.alternative] })) },
  { kind: 'clip', pick: analysis => (analysis.clip_groups || []).map(group => ({ keeper: group?.full, items: [group?.clip] })) },
  { kind: 'low-duration', pick: analysis => singles(analysis.low_duration) },
  { kind: 'low-resolution', pick: analysis => singles(analysis.low_resolution) }
];

// 图片的近似重复在图片清理审阅里叫「疑似重复」。
const IMAGE_KINDS = [
  { kind: 'exact', label: '精确重复', certain: true, pick: analysis => groupsOf(analysis.duplicate_groups) },
  { kind: 'near', label: '疑似重复', pick: analysis => groupsOf(analysis.near_duplicate_groups) }
];

// ready / stale（结果可能已过期）/ running / cancelled / missing（还没分析过）。
export function cleanupAnalysisState(status) {
  if (status?.running) return 'running';
  if (!status?.completed || !status?.analysis) return status?.cancelled ? 'cancelled' : 'missing';
  return status.stale ? 'stale' : 'ready';
}

function videoBytes(video) {
  return Number(video?.size || 0);
}

// 图片的 file_size 是分析时 os.Stat 的实测值，比库里的 size 更贴近磁盘现状。
function imageBytes(image) {
  return Number(image?.file_size || image?.size || 0);
}

function mediaRows(analysis, kinds, sizeOf, media, totals, excluded = new Set()) {
  if (!analysis) return [];
  const rows = [];
  for (const spec of kinds) {
    const seen = new Set();
    let bytes = 0;
    for (const group of spec.pick(analysis)) {
      // 保留的那份已经删了：剩下的成员可能成了唯一一份，这一组要重新分析才知道能回收什么，先不算。
      if (group.keeper && excluded.has(Number(group.keeper.id))) continue;
      for (const item of group.items) {
        const id = Number(item?.id);
        if (!id || seen.has(id) || excluded.has(id)) continue;
        seen.add(id);
        const size = sizeOf(item);
        bytes += size;
        const key = `${media}:${id}`;
        if (!totals.all.has(key)) totals.all.set(key, size);
        if (spec.certain) totals.certain.add(key);
      }
    }
    if (seen.size === 0) continue;
    rows.push({
      key: `${media}-${spec.kind}`,
      label: spec.label || CLEANUP_KIND_LABELS[spec.kind] || spec.kind,
      certain: Boolean(spec.certain),
      count: seen.size,
      bytes
    });
  }
  return rows;
}

// excludedVideoIDs：已经移到废纸篓、但清理分析还没重跑的视频，不再算可回收；
// 它是某组保留项的，那一组整组先不算。
export function reclaimSummary(videoStatus, imageStatus, { excludedVideoIDs = [] } = {}) {
  const totals = { all: new Map(), certain: new Set() };
  const videoState = cleanupAnalysisState(videoStatus);
  const imageState = cleanupAnalysisState(imageStatus);
  // 结果过期也照样算：数字仍有参考价值，界面另外标出「可能已过期」。
  const usable = state => state === 'ready' || state === 'stale';
  const video = {
    state: videoState,
    startedAt: videoStatus?.started_at || '',
    rows: usable(videoState)
      ? mediaRows(videoStatus.analysis, VIDEO_KINDS, videoBytes, 'video', totals, new Set(excludedVideoIDs.map(Number)))
      : []
  };
  const image = {
    state: imageState,
    startedAt: imageStatus?.started_at || '',
    rows: usable(imageState) ? mediaRows(imageStatus.analysis, IMAGE_KINDS, imageBytes, 'image', totals) : []
  };
  let totalBytes = 0;
  let certainBytes = 0;
  for (const [key, size] of totals.all) {
    totalBytes += size;
    if (totals.certain.has(key)) certainBytes += size;
  }
  return {
    video,
    image,
    analyzed: usable(videoState) || usable(imageState),
    totalCount: totals.all.size,
    totalBytes,
    certainBytes,
    reviewBytes: totalBytes - certainBytes
  };
}
