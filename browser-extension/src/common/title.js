// 从网页信息里挑出一个像样的视频名。纯函数，可单测。
//
// 为什么不能直接用 document.title：
//   1. 它几乎总是带着站点后缀（「视频名 | 站点名」「视频名 - 站点名」）；
//   2. 没有 <title> 的页面，浏览器会把地址当标题，于是文件名变成一串 URL 片段。
//
// 更好的来源是页面自己声明的元数据（og:title / twitter:title），那通常就是干净的
// 视频名。取名的优先级在 pickMediaTitle 里。

// 常见的标题分隔符。全角半角都要认。
const SEPARATORS = ['|', '｜', ' - ', ' – ', ' — ', '_', '·', '«', '»'];
// 末段短于这个长度才当作站点名剥掉——太长的多半是标题本身的一部分。
const SITE_SUFFIX_MAX = 20;

// 标题实际上是个地址时判为不可用：宁可退到别的来源，也不要拿一串 URL 当文件名。
export function looksLikeUrlTitle(raw) {
  const text = String(raw || '').trim();
  if (!text) return true;
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(text)) return true;
  // 没有协议头但明显是域名加路径的：xxx.com/a/b、a.b.cn/x.html
  if (/^[\w-]+(\.[\w-]+){1,}(\/|$)/.test(text) && !/\s/.test(text)) return true;
  return false;
}

// 去掉站点后缀。知道站点名（og:site_name）时按它精确剥；不知道就用启发式：
// 有分隔符、且末段足够短，才当站点名剥掉。
export function stripSiteSuffix(rawTitle, siteName = '') {
  let title = String(rawTitle || '').trim();
  if (!title) return '';

  const site = String(siteName || '').trim();
  if (site) {
    // 知道站点名就精确剥，首尾都剥（有些站点习惯把站名放前面）。
    for (const separator of SEPARATORS) {
      const tail = `${separator}${site}`;
      if (title.toLowerCase().endsWith(tail.toLowerCase())) {
        title = title.slice(0, title.length - tail.length).trim();
      }
      const head = `${site}${separator}`;
      if (title.toLowerCase().startsWith(head.toLowerCase())) {
        title = title.slice(head.length).trim();
      }
    }
  }

  // 启发式：末段很短的话按站点名剥。只剥一次，避免把「S01 - E02 - 剧名」剥秃。
  for (const separator of SEPARATORS) {
    const index = title.lastIndexOf(separator);
    if (index <= 0) continue;
    const head = title.slice(0, index).trim();
    const tail = title.slice(index + separator.length).trim();
    if (head && tail && tail.length <= SITE_SUFFIX_MAX && head.length > tail.length) {
      title = head;
      break;
    }
  }

  // 有些站点用【站点名】开头
  title = title.replace(/^【[^】]{1,12}】\s*/, '').trim();
  return title;
}

// 从页面能提供的几个来源里挑一个最像视频名的。
//
// 顺序是有讲究的：og:title / twitter:title 是页面自己声明的"这条内容叫什么"，
// 通常已经不带站点后缀，最准；<h1> 次之；document.title 最后，而且要剥后缀。
// 全都不可用时返回空串，由调用方决定退到什么（而不是在这里硬塞一个 URL 片段）。
export function pickMediaTitle({ ogTitle = '', twitterTitle = '', heading = '', documentTitle = '', siteName = '' } = {}) {
  for (const candidate of [ogTitle, twitterTitle, heading]) {
    const cleaned = stripSiteSuffix(candidate, siteName);
    if (cleaned && !looksLikeUrlTitle(cleaned)) return cleaned;
  }
  const fromDocument = stripSiteSuffix(documentTitle, siteName);
  if (fromDocument && !looksLikeUrlTitle(fromDocument)) return fromDocument;
  return '';
}
