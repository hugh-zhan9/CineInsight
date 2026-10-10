// 多版本聚合（D-MW-VERSIONS）的前端纯函数：本机「合并版本」开关、错误码解析与汇总文案。
// 组与成员的真值都在后端；这里不缓存组，只把后端给的汇总翻成文字。
import { formatBytes, formatDuration } from './mediaDetails.js';
import { resumePosition } from './watchState.js';

export const COLLAPSE_VERSIONS_STORAGE_KEY = 'library-collapse-versions';
export const VERSION_GROUP_MAX_MEMBERS = 20;

function localStore() {
  try {
    return window.localStorage || null;
  } catch (_err) {
    return null;
  }
}

// 默认开；只有明确存过 '0' 才关。存储不可用时照常默认开，只是不记忆。
export function loadCollapseVersions() {
  try {
    return localStore()?.getItem(COLLAPSE_VERSIONS_STORAGE_KEY) !== '0';
  } catch (_err) {
    return true;
  }
}

export function saveCollapseVersions(value) {
  try {
    localStore()?.setItem(COLLAPSE_VERSIONS_STORAGE_KEY, value ? '1' : '0');
  } catch (_err) {}
}

export function isVersionGroupConflict(err) {
  return String(err ?? '').includes('version_group_conflict');
}

// 组已不存在（被解散，或只剩一个版本而被清理）：重读也没用，界面按已解散处理。
export function isVersionGroupNotFound(err) {
  return String(err ?? '').includes('version_group_not_found');
}

// 后端成员冲突文案：「version_member_conflict: N 个视频已在版本组中 {"video_ids":[..],"group_ids":[..]}」。
export function parseVersionMemberConflict(err) {
  const text = String(err ?? '');
  const index = text.indexOf('version_member_conflict');
  if (index < 0) return null;
  const brace = text.indexOf('{', index);
  if (brace < 0) return { videoIDs: [], groupIDs: [] };
  try {
    const payload = JSON.parse(text.slice(brace));
    const ids = list => (Array.isArray(list) ? list.map(Number).filter(id => id > 0) : []);
    return { videoIDs: ids(payload.video_ids), groupIDs: ids(payload.group_ids) };
  } catch (_err) {
    return { videoIDs: [], groupIDs: [] };
  }
}

// 把「错误码: 中文说明」去掉错误码，供提示条展示；成员冲突给固定说法。
export function versionGroupErrorText(err) {
  const conflict = parseVersionMemberConflict(err);
  if (conflict) return `有 ${conflict.videoIDs.length || '部分'} 个视频已在其他版本组中`;
  const text = String(err ?? '').replace(/^Error:\s*/, '');
  const match = text.match(/^version_group_[a-z_]+:\s*(.+)$/s);
  return match ? match[1] : text;
}

export function formatVersionRating(value) {
  const number = Number(value);
  return Number.isInteger(number) ? String(number) : number.toFixed(1);
}

// 卡片上的汇总：「已看 1/3 · 评分 7–8.5」；没有评分时不提评分，不报 0。
export function versionGroupSummaryText(summary) {
  if (!summary) return '';
  const parts = [`已看 ${Number(summary.watched_count || 0)}/${Number(summary.member_count || 0)}`];
  const min = summary.min_rating;
  const max = summary.max_rating;
  if (min !== null && min !== undefined && max !== null && max !== undefined) {
    parts.push(Number(min) === Number(max) ? `评分 ${formatVersionRating(min)}` : `评分 ${formatVersionRating(min)}–${formatVersionRating(max)}`);
  }
  return parts.join(' · ');
}

export function versionMemberTitle(member) {
  return String(member?.display_title || member?.name || `视频 #${member?.video_id || ''}`);
}

// 组标题为空时界面显示主版本（第一位活跃成员）的标题。
export function versionGroupTitle(group) {
  const title = String(group?.title || '').trim();
  if (title) return title;
  const primary = (group?.members || [])[0];
  return primary ? versionMemberTitle(primary) : '版本组';
}

// 展开列表里一个版本的一行事实：分辨率 · 大小 · 时长 · 已看/进度 · 评分。缺哪项省哪项。
export function versionMemberFacts(member) {
  const parts = [];
  if (member?.resolution) parts.push(member.resolution);
  else if (member?.width && member?.height) parts.push(`${member.width}x${member.height}`);
  if (Number(member?.size) > 0) parts.push(formatBytes(member.size));
  const duration = formatDuration(member?.duration);
  if (duration) parts.push(duration);
  // 成员只带已看与进度秒数，不带已看时间：已看的版本只标「已看」，不猜是不是重看中的断点。
  const position = member?.is_watched ? 0 : resumePosition(member);
  if (member?.is_watched) parts.push('已看');
  else if (position > 0) parts.push(`看到 ${formatDuration(position)}`);
  if (member?.personal_rating !== null && member?.personal_rating !== undefined) {
    parts.push(`评分 ${formatVersionRating(member.personal_rating)}`);
  }
  return parts.join(' · ');
}

// 把 videoID 挪到第一位、其余保持原顺序，得到「设为主版本」要提交的排列。
export function primaryFirstOrder(members, videoID) {
  const ids = (members || []).map(member => Number(member.video_id));
  const target = Number(videoID);
  if (!ids.includes(target)) return ids;
  return [target, ...ids.filter(id => id !== target)];
}

// 上移 / 下移一位；越界时原样返回。
export function moveVersionMember(ids, index, delta) {
  const next = [...ids];
  const target = index + delta;
  if (index < 0 || index >= next.length || target < 0 || target >= next.length) return next;
  [next[index], next[target]] = [next[target], next[index]];
  return next;
}
