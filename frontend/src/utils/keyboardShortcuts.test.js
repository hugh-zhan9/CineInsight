import { describe, expect, it } from 'vitest';
import { isTextEntryTarget, shortcutActionForEvent } from './keyboardShortcuts.js';

const element = html => {
  const holder = document.createElement('div');
  holder.innerHTML = html;
  return holder.firstElementChild;
};

describe('keyboard shortcuts', () => {
  it('maps review keys and arrows', () => {
    expect(shortcutActionForEvent({ key: 'j' })).toBe('next');
    expect(shortcutActionForEvent({ key: 'ArrowUp' })).toBe('previous');
    expect(shortcutActionForEvent({ key: ' ' })).toBe('preview');
    expect(shortcutActionForEvent({ key: 'Enter' })).toBe('play');
  });

  it('does not hijack typing, modified, or repeated action keys', () => {
    const input = document.createElement('input');
    expect(shortcutActionForEvent({ key: 'f', target: input })).toBe('');
    expect(shortcutActionForEvent({ key: 'f', ctrlKey: true })).toBe('');
    expect(shortcutActionForEvent({ key: 'f', repeat: true })).toBe('');
  });

  // 单键快捷键除了让开文本框，还让开自己吃空格 / 回车 / 方向键的原生控件（与合并前一样）；可编辑区域 2026-10-14 起
  // 认 contenteditable 的全部可编辑取值（以前只认 "true"）。
  it('leaves native key controls alone and treats every editable contenteditable value as typing', () => {
    for (const html of [
      '<input type="checkbox">', '<input type="range">', '<button type="button"><span>播放</span></button>', '<video></video>', '<audio></audio>',
      '<textarea></textarea>', '<div contenteditable="plaintext-only"></div>', '<div contenteditable=""></div>', '<div role="textbox"></div>'
    ]) {
      const target = element(html);
      expect(shortcutActionForEvent({ key: 'f', target: target.querySelector('span') || target }), html).toBe('');
    }
    expect(shortcutActionForEvent({ key: 'f', target: document.body })).toBe('favorite');
    expect(shortcutActionForEvent({ key: 'f', target: element('<div contenteditable="false"></div>') })).toBe('favorite');
    expect(shortcutActionForEvent({ key: 'f', target: window })).toBe('favorite');
  });
});

// 全站唯一的「焦点在文本框里」判定（SelectionBar、片库页 Esc、单键快捷键共用）。
describe('isTextEntryTarget', () => {
  it('counts text-like inputs, textarea, select, editable regions and role=textbox', () => {
    for (const html of [
      '<input>', '<input type="text">', '<input type="search">', '<input type="number">', '<input type="email">', '<input type="password">',
      '<input type="date">', '<input type="bogus">', '<textarea></textarea>', '<select><option>a</option></select>',
      '<div contenteditable="true"></div>', '<div contenteditable=""></div>', '<div contenteditable="plaintext-only"></div>',
      '<div contenteditable="TRUE"></div>', '<div role="textbox"></div>'
    ]) {
      expect(isTextEntryTarget(element(html)), html).toBe(true);
    }
  });

  it('counts what sits inside an editable region or a textbox, with the nearest contenteditable layer deciding', () => {
    const editor = element('<div contenteditable="true"><p><b>粗体</b><span contenteditable="false"><i>不可改</i></span></p></div>');
    expect(isTextEntryTarget(editor.querySelector('b'))).toBe(true);
    expect(isTextEntryTarget(editor.querySelector('i'))).toBe(false);
    const box = element('<div role="textbox"><span>字</span></div>');
    expect(isTextEntryTarget(box.querySelector('span'))).toBe(true);
  });

  it('does not count controls that take no text, plain elements or non-elements', () => {
    for (const html of [
      '<input type="checkbox">', '<input type="radio">', '<input type="button">', '<input type="submit">', '<input type="reset">',
      '<input type="image">', '<input type="range">', '<input type="color">', '<input type="file">', '<input type="hidden">',
      '<button type="button">播放</button>', '<video></video>', '<div tabindex="0"></div>', '<div contenteditable="false"></div>',
      '<div contenteditable="nope"></div>', '<a href="#">链接</a>'
    ]) {
      expect(isTextEntryTarget(element(html)), html).toBe(false);
    }
    expect(isTextEntryTarget(null)).toBe(false);
    expect(isTextEntryTarget(undefined)).toBe(false);
    expect(isTextEntryTarget(window)).toBe(false);
    expect(isTextEntryTarget(document)).toBe(false);
  });
});
