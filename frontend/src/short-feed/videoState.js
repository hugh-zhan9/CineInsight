export function unsupportedStatusText(video) {
  if (!video) return '加载中';
  if (video.media_url) return '';
  return video.reason_message || '当前视频暂不支持浏览器播放';
}
