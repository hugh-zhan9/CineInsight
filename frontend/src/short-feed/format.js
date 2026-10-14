// 手机页的展示格式（D-MW-02、D-MW-04、D-MW-05）：只放纯函数，node 脚本测试可以直接调用。

// 秒数写成 mm:ss；拿不到时长时写 00:00，不写 NaN。
export function formatClock(seconds) {
  const value = Number(seconds);
  if (!Number.isFinite(value) || value <= 0) return '00:00';
  const total = Math.floor(value);
  return `${String(Math.floor(total / 60)).padStart(2, '0')}:${String(total % 60).padStart(2, '0')}`;
}

// 个人评分写成一位小数（「8.5」「9.0」）；没评过返回空串，由调用方决定写不写「未评分」。
export function formatRating(rating) {
  if (rating === null || rating === undefined || rating === '') return '';
  const value = Number(rating);
  return Number.isFinite(value) ? value.toFixed(1) : '';
}

// 条数带千分位（「1,284」）：范围面板的数字靠右对齐，长数字分组才好读。
export function formatCount(count) {
  const value = Number(count);
  return Number.isFinite(value) ? Math.max(0, Math.floor(value)).toLocaleString('en-US') : '0';
}

// 类型与规格：视频写时长，图片写尺寸；「更多」面板的副标题与左下信息行共用。
export function kindLine(item) {
  if (!item) return '';
  if (item.media_kind === 'image') {
    const width = Number(item.width) || 0;
    const height = Number(item.height) || 0;
    return width > 0 && height > 0 ? `图片 · ${width} × ${height}` : '图片';
  }
  return Number(item.duration) > 0 ? `视频 · ${formatClock(item.duration)}` : '视频';
}

// 左下信息行：类型 · 时长（或尺寸） · 评分 X · 已看，没有的项不写（D-MW-02）。
export function metaLine(item) {
  if (!item) return '';
  const parts = [kindLine(item)];
  const rating = formatRating(item.personal_rating);
  if (rating) parts.push(`评分 ${rating}`);
  if (item.media_kind !== 'image' && item.watched) parts.push('已看');
  return parts.join(' · ');
}

// 第 star 颗星（1–10）在 value 分时的样子：满、半（半分）、空。
export function starFill(value, star) {
  const rating = Number(value);
  if (value === null || value === undefined || !Number.isFinite(rating)) return 'empty';
  if (rating >= star) return 'full';
  if (rating >= star - 0.5) return 'half';
  return 'empty';
}
