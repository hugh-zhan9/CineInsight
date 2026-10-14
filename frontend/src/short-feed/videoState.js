// 一条内容拿不到可播/可显示地址时，给出可解释的说明。文案按媒体类型区分：
// 图片条目说"视频"会让人以为拿错了内容。
export function unsupportedStatusText(item) {
  if (!item) return '加载中';
  if (item.media_url) return '';
  if (item.reason_message) return item.reason_message;
  return item.media_kind === 'image' ? '当前图片暂不支持浏览器显示' : '当前视频暂不支持浏览器播放';
}

// 范围里一条能播的都没有时，服务端会把一条放不了的视频（reason_code inline_not_supported）当说明回过来，
// 而不是 404。手机页把它和 404 no_eligible_videos 一样当作「空范围」（D-MW-07），不把它插进浏览历史。
export function isScopeExhaustedHint(item) {
  return !!item && !item.media_url && item.reason_code === 'inline_not_supported';
}
