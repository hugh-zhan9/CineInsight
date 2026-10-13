// 清理审阅「不是重复 / 移出本组 / 忽略」的撤销（D-P3-06）：视频与图片两个标签共用。
// 忽略接口（DismissNearDuplicateGroup 等）不回记录 ID，撤销接口 UndoCleanupDismissals 要的是记录 ID，所以撤销时
// 按设计用 ListCleanupDismissals 找回这一步写下的记录：每条记录由它涉及的媒体唯一确定（同一对 / 同一个视频同一类
// 只有一条，重复忽略只刷新指纹），按媒体键对上就是它。找不到任何一条说明已经在「已忽略」里撤销过了。
import { ListCleanupDismissals, UndoCleanupDismissals } from '../../wailsjs/go/main/App';

const PAGE_SIZE = 200;
// 新写的记录排在最前（按记录 ID 倒序）；重复忽略刷新的旧记录可能在后面，最多往后翻这么多页。
const MAX_PAGES = 25;

function pairKey(a, b) {
  const [low, high] = Number(a) < Number(b) ? [Number(a), Number(b)] : [Number(b), Number(a)];
  return `${low}:${high}`;
}

// 一组媒体两两之间的配对（「不是重复」整组）。
export function groupPairKeys(ids) {
  const unique = [...new Set((ids || []).map(Number).filter(Boolean))];
  const keys = [];
  for (let i = 0; i < unique.length; i++) {
    for (let j = i + 1; j < unique.length; j++) keys.push(pairKey(unique[i], unique[j]));
  }
  return keys;
}

// 一个成员与组内其他成员的配对（「移出本组」）。
export function memberPairKeys(ids, memberID) {
  const member = Number(memberID);
  return [...new Set((ids || []).map(Number).filter(id => id && id !== member))].map(id => pairKey(member, id));
}

// 截取片段：（完整片, 片段）有方向；极短 / 低清：单个视频。
export function clipKey(fullID, clipID) {
  return `${Number(fullID)}>${Number(clipID)}`;
}
export function videoKey(videoID) {
  return String(Number(videoID));
}

// ListCleanupDismissals 一条记录的媒体键，与上面几种写法对得上。
export function dismissalItemKey(kind, item) {
  const media = item?.media || [];
  if (kind === 'clip') return clipKey(media[0]?.id, media[1]?.id);
  if (media.length >= 2) return pairKey(media[0]?.id, media[1]?.id);
  return videoKey(media[0]?.id);
}

// 找回 keys 对应的忽略记录并撤销。返回 { removed }：0 表示一条都没找到（已经撤销过了）。失败原样抛出。
export async function undoCleanupDismissalRecords(kind, keys) {
  const wanted = new Set(keys || []);
  const ids = [];
  let cursor = 0;
  for (let page = 0; page < MAX_PAGES && ids.length < wanted.size; page++) {
    const result = await ListCleanupDismissals(kind, cursor, PAGE_SIZE);
    for (const item of result?.items || []) {
      if (wanted.has(dismissalItemKey(kind, item))) ids.push(Number(item.id));
    }
    cursor = Number(result?.next_cursor || 0);
    if (!result?.has_more || !cursor) break;
  }
  if (!ids.length) return { removed: 0 };
  const undone = await UndoCleanupDismissals(kind, ids);
  return { removed: Number(undone?.removed || 0) };
}
