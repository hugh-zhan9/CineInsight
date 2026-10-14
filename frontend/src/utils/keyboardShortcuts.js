const actionByKey = new Map([
  ['j', 'next'],
  ['arrowdown', 'next'],
  ['k', 'previous'],
  ['arrowup', 'previous'],
  [' ', 'preview'],
  ['spacebar', 'preview'],
  ['f', 'favorite'],
  ['w', 'watched'],
  ['t', 'tag'],
  ['enter', 'play']
]);

// 不收文字的 input 类型：勾选框、单选、按钮类、滑杆、取色、选文件、隐藏。其余（text、search、number、日期时间……
// 浏览器把写错的 type 也当 text）都是文字输入框。
const NON_TEXT_INPUT_TYPES = new Set(['checkbox', 'radio', 'button', 'submit', 'reset', 'image', 'range', 'color', 'file', 'hidden']);
// contenteditable 算「可编辑」的取值；"false" 与写错的值不算。
const EDITABLE_CONTENT_VALUES = new Set(['', 'true', 'plaintext-only']);

// 焦点在文本框里（全站只此一份判定，2026-10-14 合并 SelectionBar、片库页与单键快捷键各自的写法）：文字输入框、多行文本、
// 下拉选择、可编辑区域（contenteditable 为空、true 或 plaintext-only，以离焦点最近的那一层为准，区域里的子节点也算）、
// role=textbox 及其里面。这时按键归那个控件：页面级的 Esc 取消选择与单键快捷键都不接。
// 勾选框、单选、按钮、滑杆这类不收文字的控件不算——焦点在列表行的勾选框上时 Esc 照旧取消选择。
export function isTextEntryTarget(target) {
  if (!target || typeof target.closest !== 'function') return false;
  if (target.closest('textarea, select, [role="textbox"]')) return true;
  const editable = target.closest('[contenteditable]');
  if (editable && EDITABLE_CONTENT_VALUES.has(String(editable.getAttribute('contenteditable')).toLowerCase())) return true;
  return target.tagName === 'INPUT' && !NON_TEXT_INPUT_TYPES.has(String(target.type || '').toLowerCase());
}

// 单键快捷键还要让开自己吃这些键的原生控件：按钮（空格、回车按下它）、勾选框等不收文字的 input（空格勾选、方向键调滑杆）、
// 内嵌播放器 video/audio（空格交给原生播放/暂停，而不是被快捷键劫持去关抽屉）。
const NATIVE_KEY_CONTROLS = 'input, button, video, audio';

export function shortcutActionForEvent(event) {
  if (!event || event.defaultPrevented || event.isComposing || event.metaKey || event.ctrlKey || event.altKey) return '';
  const target = event.target;
  if (isTextEntryTarget(target) || target?.closest?.(NATIVE_KEY_CONTROLS)) return '';
  const action = actionByKey.get(String(event.key || '').toLowerCase()) || '';
  if (event.repeat && ['preview', 'favorite', 'watched', 'tag', 'play'].includes(action)) return '';
  return action;
}
