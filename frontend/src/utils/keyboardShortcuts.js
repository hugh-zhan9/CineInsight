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

export function isTypingShortcutTarget(target) {
  if (!target || typeof target.closest !== 'function') return false;
  // video/audio 也视为守卫目标：焦点在内嵌播放器上时，空格应交给原生
  // 播放/暂停，而不是被快捷键劫持去关抽屉。
  return !!target.closest('input, textarea, select, button, video, audio, [contenteditable="true"], [role="textbox"]');
}

export function shortcutActionForEvent(event) {
  if (!event || event.defaultPrevented || event.isComposing || event.metaKey || event.ctrlKey || event.altKey) return '';
  if (isTypingShortcutTarget(event.target)) return '';
  const action = actionByKey.get(String(event.key || '').toLowerCase()) || '';
  if (event.repeat && ['preview', 'favorite', 'watched', 'tag', 'play'].includes(action)) return '';
  return action;
}
