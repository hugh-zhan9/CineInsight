// 观看侧整页详情（D-UI-05，详细设计「整页详情」）的纯函数：背景怎么选、事实行、作品集里的下一部、相关推荐合并、看过的记录。
// 组件只负责取数与渲染，这些规则单独测。
import { formatDuration } from '../../utils/mediaDetails.js';
import { heroFrameURLs, videoArtwork, videoThumbnail } from '../../utils/recap.js';

// 相关推荐：同一批人物的其他作品（抽屉人物详情里 RelatedVideoItem 列的那份数据，GetPersonDetail）。
export const RELATED_PEOPLE_LIMIT = 4;
export const RELATED_PER_PERSON = 12;
export const RELATED_LIMIT = 12;
// 一部片挂在很多作品集里时只读前几个的成员顺序。
export const COLLECTION_DETAIL_LIMIT = 6;

export function detailTitle(details) {
  const video = details?.video || {};
  return String(details?.effective_title || video.display_title || video.name || '未命名视频');
}

// 背景：有背景图（详情接口的 has_fanart）就只用背景图；没有用画面帧慢速轮播（时长未知时没有帧），
// 都失败时 RecapBackdrop 退回缩略图。减少动态效果时只显示第一帧由 RecapBackdrop 处理。
export function detailBackdrop(details) {
  const id = Number(details?.video?.id || 0);
  if (!id) return { frames: [], fallback: '' };
  const frames = details?.artwork?.has_fanart ? [videoArtwork(id, 'fanart')] : heroFrameURLs(details.video);
  return { frames, fallback: videoThumbnail(id) };
}

export function ratingOf(video) {
  const rating = video?.personal_rating;
  if (rating === null || rating === undefined || rating === '') return null;
  const value = Number(rating);
  return Number.isFinite(value) ? value : null;
}

export function resolutionText(video) {
  const width = Number(video?.width || 0);
  const height = Number(video?.height || 0);
  if (width > 0 && height > 0) return `${width}×${height}`;
  return String(video?.resolution || '').trim();
}

// 片名下面的一行事实：年份、时长、分辨率、版本数、评分。缺哪项省哪项，不用「未知」占位。
export function detailFacts(video, versionCount = 0) {
  const facts = [];
  if (Number(video?.year) > 0) facts.push({ key: 'year', text: `${video.year} 年` });
  const duration = formatDuration(video?.duration);
  if (duration) facts.push({ key: 'duration', text: duration });
  const resolution = resolutionText(video);
  if (resolution) facts.push({ key: 'resolution', text: resolution });
  if (Number(versionCount) >= 2) facts.push({ key: 'versions', text: `${versionCount} 个版本` });
  const rating = ratingOf(video);
  if (rating !== null) facts.push({ key: 'rating', text: `评分 ${rating.toFixed(1)}` });
  return facts;
}

// 这部片在作品集里的位置与下一部：成员按 position 排，找不到本片（刚被移出）时不给下一部。
export function collectionProgress(detail, videoID) {
  const members = [...(detail?.videos || [])].sort((a, b) => Number(a.position) - Number(b.position));
  const index = members.findIndex(item => Number(item?.video?.id) === Number(videoID));
  return {
    total: members.length,
    position: index >= 0 ? index + 1 : 0,
    next: index >= 0 ? members[index + 1]?.video || null : null
  };
}

// 相关推荐：按人物顺序合并各自的作品，去掉本片与重复的，理由记第一个带出它的人物。
export function mergeRelatedVideos(groups, currentID, limit = RELATED_LIMIT) {
  const seen = new Set([Number(currentID)]);
  const items = [];
  for (const group of groups || []) {
    for (const video of group?.videos || []) {
      const id = Number(video?.id || 0);
      if (!id || seen.has(id)) continue;
      seen.add(id);
      items.push({ video, reason: group.name ? `同为 ${group.name} 的作品` : '' });
      if (items.length >= limit) return items;
    }
  }
  return items;
}

export function formatRecordDate(value) {
  if (!value) return '';
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '';
  return `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
}

// 看过的记录：都来自详情里的视频行——外部播放器的正式播放次数与最近一次、标记已看的时间、应用内看到哪里。
// 一项都没有（没播过、没标已看、没有断点）时返回空数组，整节不显示。
export function viewingRecord(video) {
  if (!video) return [];
  const tiles = [];
  const plays = Number(video.play_count || 0);
  if (plays > 0) tiles.push({ key: 'plays', icon: 'plays', label: '正式播放', value: plays, unit: '次' });
  const lastPlayed = formatRecordDate(video.last_played_at);
  if (lastPlayed) tiles.push({ key: 'last-played', icon: 'clock', label: '最近播放', value: lastPlayed, unit: '' });
  if (video.is_watched) tiles.push({ key: 'watched', icon: 'days', label: '标记已看', value: formatRecordDate(video.watched_at) || '已看', unit: '' });
  const position = formatDuration(video.watch_position_seconds);
  if (position) {
    const total = formatDuration(video.duration);
    tiles.push({ key: 'progress', icon: 'play', label: '应用内看到', value: total ? `${position} / ${total}` : position, unit: '' });
  }
  return tiles;
}
