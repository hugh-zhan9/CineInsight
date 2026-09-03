// 按文件名推断 MIME 类型。纯函数。
//
// 为什么必须有它：OPFS 取回来的 File 没有 type，空类型的 blob 地址会被浏览器
// 按 text/plain 对待，chrome.downloads 于是把产物命名成 .txt，我们指定的文件名
// 和扩展名一起被改掉。给 blob 一个正确的类型，这两件事就都对了。
const MIME_BY_EXTENSION = {
  ts: 'video/mp2t',
  mp4: 'video/mp4',
  m4v: 'video/mp4',
  mkv: 'video/x-matroska',
  webm: 'video/webm',
  mov: 'video/quicktime',
  avi: 'video/x-msvideo',
  flv: 'video/x-flv',
  wmv: 'video/x-ms-wmv',
  ogv: 'video/ogg',
  '3gp': 'video/3gpp',
  mpg: 'video/mpeg',
  mpeg: 'video/mpeg',
  m4s: 'video/iso.segment',
  aac: 'audio/aac',
  mp3: 'audio/mpeg',
  m4a: 'audio/mp4'
};

export function mimeForFilename(filename) {
  const name = String(filename || '');
  const dot = name.lastIndexOf('.');
  const extension = dot >= 0 ? name.slice(dot + 1).toLowerCase() : '';
  // 认不出来时给 application/octet-stream 而不是空串：空串会被当成 text/plain，
  // 那正是产物变成 .txt 的原因。octet-stream 让浏览器老实按我们给的文件名保存。
  return MIME_BY_EXTENSION[extension] || 'application/octet-stream';
}
