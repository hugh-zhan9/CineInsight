// 一条内容拿不到可播/可显示地址时，给出可解释的说明。文案按媒体类型区分：
// 图片条目说"视频"会让人以为拿错了内容。
export function unsupportedStatusText(item) {
  if (!item) return '加载中';
  if (item.media_url) return '';
  if (item.reason_message) return item.reason_message;
  return item.media_kind === 'image' ? '当前图片暂不支持浏览器显示' : '当前视频暂不支持浏览器播放';
}
