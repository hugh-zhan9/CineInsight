// 只登记当前编辑窗口的关闭能力；参数和草稿仍由窗口自己持有。
const editors = new Set();
export function registerImageEditor(close) { editors.add(close); return () => editors.delete(close); }
export function requestImageEditorsLeave() {
  if (!editors.size) return true;
  return (async () => { for (const close of [...editors]) if (!await close()) return false; return true; })();
}
