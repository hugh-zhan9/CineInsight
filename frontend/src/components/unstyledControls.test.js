import { readFileSync, readdirSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

// 表单控件必须接入项目的样式原语（.btn-* / .row-btn / .toolbar-btn / .text-input / .select-input /
// .number-input / .search-input 等）、组件自己的类，或本组件样式里不带属性限定的标签规则；否则在 WKWebView 里就是浏览器
// 原生外观。2026-10-11 观看笔记、待播队列、今晚看什么等新页面就是这样“样式丢失”的。
const SRC = join(process.cwd(), 'src');
const EXEMPT_INPUT_TYPES = /type="(checkbox|radio|range|file|hidden|color)"/;
// 由全局后代选择器（components.css 的 `.setting-item input`）统一着样式的控件。
const ALLOWED = new Set(['components/settings/JellyfinSection.vue:input']);

function listVueFiles(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) return listVueFiles(full);
    return entry.name.endsWith('.vue') ? [full] : [];
  });
}

function unstyledControls(path) {
  const source = readFileSync(path, 'utf8');
  const template = (source.match(/<template>([\s\S]*)<\/template>/) || [, ''])[1];
  const css = [...source.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map(match => match[1]).join('\n');
  const found = [];
  for (const match of template.matchAll(/<(button|input|select|textarea)\b((?:[^>"]|"[^"]*")*)>/g)) {
    const [, tag, attrs] = match;
    if (tag === 'input' && EXEMPT_INPUT_TYPES.test(attrs)) continue;
    if (/(?<![:\w-])class="|:class="/.test(attrs)) continue;
    // 本组件样式里直接写到这个标签（如 `.form-row input {`、`input:focus`）才算覆盖；
    // `input[type=checkbox]` 这类带属性限定的规则管不到文本框，不算。
    if (new RegExp(`(^|[\\s,}>(])${tag}(?=\\s*[,{:>+~.]|\\s+[^\\s{])`).test(css)) continue;
    found.push(`${tag}@${template.slice(0, match.index).split('\n').length}`);
  }
  return found;
}

describe('form controls use the shared style primitives', () => {
  it('has no bare button/input/select/textarea in component templates', () => {
    const offenders = [];
    for (const path of listVueFiles(SRC)) {
      const file = relative(SRC, path);
      const controls = unstyledControls(path).filter(item => !ALLOWED.has(`${file}:${item.split('@')[0]}`));
      if (controls.length) offenders.push(`${file}: ${controls.join(' ')}`);
    }
    expect(offenders).toEqual([]);
  });
});
