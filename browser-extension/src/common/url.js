// URL 规范化、扩展名推断与文件名清洗。纯函数，不碰 chrome API，可直接单测。

// 去重键：只去掉 fragment。查询串一律保留——CDN 的鉴权令牌就在查询串里，
// 剥掉之后地址还在但取不到内容，比不去重更糟。
export function dedupeKey(rawUrl) {
  try {
    const parsed = new URL(rawUrl);
    parsed.hash = '';
    return parsed.toString();
  } catch {
    return String(rawUrl || '').split('#')[0];
  }
}

// 把播放列表里的相对 URI 按基址解析成绝对地址。
export function resolveUrl(base, relative) {
  return new URL(relative, base).toString();
}

// 取路径末段的小写扩展名（不含点）。查询串与 fragment 不参与。
export function extensionOf(rawUrl) {
  const pathname = pathnameOf(rawUrl);
  const name = lastSegment(pathname);
  const dot = name.lastIndexOf('.');
  if (dot <= 0 || dot === name.length - 1) return '';
  return name.slice(dot + 1).toLowerCase();
}

// 取路径末段去掉扩展名的部分，供"实在没标题时用文件名"兜底。
export function basenameOf(rawUrl) {
  const name = lastSegment(safeDecode(pathnameOf(rawUrl)));
  const dot = name.lastIndexOf('.');
  return dot > 0 ? name.slice(0, dot) : name;
}

export function hostOf(rawUrl) {
  try {
    return new URL(rawUrl).host;
  } catch {
    return '';
  }
}

function pathnameOf(rawUrl) {
  try {
    return new URL(rawUrl).pathname;
  } catch {
    return String(rawUrl || '').split('#')[0].split('?')[0];
  }
}

function lastSegment(pathname) {
  const lastSlash = pathname.lastIndexOf('/');
  return lastSlash >= 0 ? pathname.slice(lastSlash + 1) : pathname;
}

function safeDecode(value) {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

// Windows 与 macOS 都不接受的字符，加上 C0/C1 控制字符。
const ILLEGAL_FILENAME = /[\\/:*?"<>|\u0000-\u001f\u007f]/g;
// Windows 保留设备名，带扩展名也一样不行。
const RESERVED_WINDOWS = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/i;
const MAX_STEM_LENGTH = 120;

// 文件名清洗：去非法字符、压空白、去首尾的点与空格、限长。
// 结果为空时回退 'video'——返回空串会让下游拼出以点开头的隐藏文件。
export function sanitizeFilename(raw) {
  let name = String(raw ?? '')
    .replace(ILLEGAL_FILENAME, ' ')
    .replace(/\s+/g, ' ')
    .trim();
  // 尾部的点在 Windows 上会被静默吃掉，先去干净再限长。
  name = name.replace(/^[.\s]+/, '').replace(/[.\s]+$/, '');
  if (name.length > MAX_STEM_LENGTH) {
    name = name.slice(0, MAX_STEM_LENGTH).replace(/[.\s]+$/, '');
  }
  if (!name || RESERVED_WINDOWS.test(name)) return 'video';
  return name;
}

// 组装最终文件名：标题 + 可选码率后缀 + 扩展名。
// 扩展名只留字母数字，免得把查询串带进文件名。
export function buildFilename({ title, url, variantLabel, extension }) {
  const stem = sanitizeFilename(title || basenameOf(url) || 'video');
  const suffix = variantLabel ? `_${sanitizeFilename(variantLabel)}` : '';
  const ext = String(extension || '').replace(/[^a-z0-9]/gi, '').toLowerCase();
  const base = `${stem}${suffix}`.slice(0, MAX_STEM_LENGTH).replace(/[.\s]+$/, '') || 'video';
  return ext ? `${base}.${ext}` : base;
}

// 同名时追加序号而不是覆盖。existing 是已占用的文件名集合。
export function uniqueFilename(filename, existing) {
  const taken = existing instanceof Set ? existing : new Set(existing || []);
  if (!taken.has(filename)) return filename;
  const dot = filename.lastIndexOf('.');
  const stem = dot > 0 ? filename.slice(0, dot) : filename;
  const ext = dot > 0 ? filename.slice(dot) : '';
  for (let index = 2; index < 10000; index += 1) {
    const candidate = `${stem} (${index})${ext}`;
    if (!taken.has(candidate)) return candidate;
  }
  return `${stem} (${Date.now()})${ext}`;
}
