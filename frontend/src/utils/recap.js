// 观看回顾页的纯函数：期次切换、请求构造、文案与可视化的小换算。页面与子组件共用，单测直接覆盖。

export const RECAP_TABS = [
  { key: 'live', label: '实时' },
  { key: 'month', label: '月度' },
  { key: 'year', label: '年度' }
];

export function currentCursor(now = new Date()) {
  return { year: now.getFullYear(), month: now.getMonth() + 1 };
}

// 月度按月、年度按年前后移动；实时没有期次可移。
export function shiftCursor(tab, cursor, delta) {
  if (tab === 'year') return { year: cursor.year + delta, month: cursor.month };
  if (tab !== 'month') return { ...cursor };
  const index = cursor.year * 12 + (cursor.month - 1) + delta;
  return { year: Math.floor(index / 12), month: (index % 12) + 1 };
}

// 不能翻到还没开始的期次。
export function canShiftForward(tab, cursor, now = new Date()) {
  const current = currentCursor(now);
  if (tab === 'year') return cursor.year < current.year;
  if (tab === 'month') return cursor.year * 12 + cursor.month < current.year * 12 + current.month;
  return false;
}

export function canShiftBack(tab, cursor) {
  if (tab === 'year') return cursor.year > 1;
  if (tab === 'month') return cursor.year > 1 || cursor.month > 1;
  return false;
}

export function recapRequest(tab, cursor) {
  if (tab === 'live') return { period: 'live', year: 0, month: 0 };
  if (tab === 'year') return { period: 'year', year: cursor.year, month: 0 };
  return { period: 'month', year: cursor.year, month: cursor.month };
}

export function requestKey(request) {
  return `${request.period}:${request.year}:${request.month}`;
}

// 相对上一同期的变化：上一期没有数据时后端给 null，这里不显示百分比。
export function changeText(change) {
  if (change === null || change === undefined || !Number.isFinite(Number(change))) return '';
  const value = Math.round(Number(change));
  if (value === 0) return '持平';
  return value > 0 ? `↑ ${value}%` : `↓ ${Math.abs(value)}%`;
}

export function changeTone(change) {
  if (change === null || change === undefined) return 'none';
  const value = Math.round(Number(change));
  if (value === 0) return 'flat';
  return value > 0 ? 'up' : 'down';
}

export function videoThumbnail(id) {
  return id ? `/preview/thumbnail/${id}` : '';
}

// 视频的海报 / 背景图（D-UI-12）：海报找不到时服务端回落缩略图；背景图找不到是 404，先看 has_fanart 再用。
export function videoArtwork(id, kind) {
  return id && (kind === 'poster' || kind === 'fanart') ? `/preview/video-artwork/${id}?kind=${kind}` : '';
}

export function imageThumbnail(id) {
  return id ? `/preview/image-thumbnail/${id}` : '';
}

// 本期最爱的背景帧：时长的 10/30/50/70/90% 各取一帧（单帧路由宽度上限 960）。时长未知时没有帧，退回缩略图。
export function heroFrameURLs(video, width = 960) {
  const id = Number(video?.id || 0);
  const duration = Number(video?.duration || 0);
  if (!id || !(duration > 0)) return [];
  return [0.1, 0.3, 0.5, 0.7, 0.9].map(ratio => `/preview/frame/${id}?ms=${Math.floor(duration * ratio * 1000)}&w=${width}`);
}

const SOURCE_LABELS = {
  'video:desktop_play': '桌面播放',
  'video:desktop_random': '随机播放',
  'video:inline_view': '应用内观看',
  'video:queue_view': '待播队列观看',
  'video:mobile_feed': '手机端',
  'video:jellyfin_view': 'Jellyfin',
  'video:legacy': '历史补记',
  'image:desktop_viewer': '桌面看图',
  'image:mobile_feed': '手机看图'
};

export function sourceLabel(kind, source) {
  return SOURCE_LABELS[`${kind}:${source}`] || source || '其他';
}

export const SLOT_LABELS = { late_night: '深夜', morning: '上午', afternoon: '下午', evening: '晚上' };
export const SLOT_RANGES = { late_night: '0–5 点', morning: '6–11 点', afternoon: '12–17 点', evening: '18–23 点' };

export function percentText(share) {
  const value = Number(share || 0);
  if (!(value > 0)) return '0%';
  const percent = Math.round(value * 100);
  return percent === 0 ? '<1%' : `${percent}%`;
}

// 0–4 五档热度：0 为没有活动，其余按当期最大值等分。
export function heatLevel(count, max) {
  const value = Number(count || 0);
  if (value <= 0 || !(max > 0)) return 0;
  return Math.min(4, Math.max(1, Math.ceil((value / max) * 4)));
}

// 标签云字号：按权重在 0.9–2.1rem 之间线性取值。
export function tagCloudScale(weight, max) {
  if (!(max > 0)) return 1;
  return 0.9 + 1.2 * (Number(weight || 0) / max);
}

// 月历：周一开头，前面补空格。days 是后端给的当月每日桶（YYYY-MM-DD）。
export function monthCalendarCells(days) {
  if (!days?.length) return [];
  const [year, month, day] = days[0].date.split('-').map(Number);
  const lead = (new Date(year, month - 1, day).getDay() + 6) % 7;
  return [...Array.from({ length: lead }, (_, index) => ({ blank: true, key: `blank-${index}` })), ...days.map(item => ({ ...item, key: item.date }))];
}

// 年历：按周排成列（周一开头），每列 7 格；第一列前面补空格。
export function yearCalendarWeeks(days) {
  const cells = monthCalendarCells(days);
  const weeks = [];
  for (let index = 0; index < cells.length; index += 7) weeks.push(cells.slice(index, index + 7));
  return weeks;
}

export function formatClock(value) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '';
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
}

export function formatDay(value) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '';
  return `${date.getMonth() + 1}月${date.getDate()}日`;
}

export function formatDateLabel(isoDate) {
  const [, month, day] = String(isoDate || '').split('-').map(Number);
  return month && day ? `${month}月${day}日` : '';
}

export function relativeTime(value, now = Date.now()) {
  const at = new Date(value).getTime();
  if (!Number.isFinite(at)) return '';
  const minutes = Math.max(0, Math.round((now - at) / 60000));
  if (minutes < 1) return '刚刚';
  if (minutes < 60) return `${minutes} 分钟前`;
  return `${Math.floor(minutes / 60)} 小时前`;
}

export function prefersReducedMotion() {
  try {
    return typeof window === 'undefined' || typeof window.matchMedia !== 'function' || window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  } catch {
    return true;
  }
}
