// 界面文案里的路径处理（G-3）：只显示文件名，不显示绝对路径。

export function fileBaseName(path) {
  const parts = String(path || '').split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] || '';
}

// 把 POSIX 绝对路径（至少两段）与 Windows 盘符路径换成文件名。只作兜底——后端带错误码的文案
// 本来就不含路径，这里挡的是系统错误原样透上来的情况。路径里含空格时只能擦到空格之前那段。
export function scrubAbsolutePaths(text) {
  return String(text ?? '')
    .replace(/(^|[\s(（:：「"'=])((?:\/[^\s/"'「」()（）:：]+){2,}\/?)/g, (_match, lead, path) => lead + fileBaseName(path))
    .replace(/(^|[\s(（:：「"'=])([A-Za-z]:\\[^\s"'「」()（）:：]*)/g, (_match, lead, path) => lead + fileBaseName(path));
}
