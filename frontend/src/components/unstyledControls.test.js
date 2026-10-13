import { readFileSync, readdirSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

import { loadPendingLists, ratchetFailures } from '../../scripts/design-system-ratchet.mjs';

// 表单控件必须接入项目的样式原语（.btn / .btn-link / .btn-icon / .btn-plain / .text-input / .select-input /
// .number-input / .search-input 等）、组件自己的类，或本组件样式里不带属性限定的标签规则；否则在 WKWebView 里就是浏览器
// 原生外观。2026-10-11 观看笔记、待播队列、今晚看什么等新页面就是这样“样式丢失”的。
const SRC = join(process.cwd(), 'src');
const EXEMPT_INPUT_TYPES = /type="(checkbox|radio|range|file|hidden|color)"/;

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
      const controls = unstyledControls(path);
      if (controls.length) offenders.push(`${file}: ${controls.join(' ')}`);
    }
    expect(offenders).toEqual([]);
  });
});

// 统一设计守卫（D-UD-15，详细设计 §5 末段）：按钮必须带 .btn / .btn-link / .btn-icon 之一，整张卡片、海报、头像这类
// 可点区域带 .btn-plain（components.css：只清掉浏览器按钮外观、给焦点环，外形由组件自己的类决定），
// 或是 components/ui 组件内部已登记的例外（下面 INTERNAL_BUTTONS，按「文件:类名」登记）。旧按钮类（btn-primary、row-btn、toolbar-btn…）
// 已在第 0 步全部换掉并从 components.css 删除；scripts/design-system-pending/*.json 的 buttons 清单随之清零。清单按文件记「还剩几个不合规按钮」，
// 是棘轮（与 design-system.test.mjs 共用 scripts/design-system-ratchet.mjs）：不在清单里的文件出现不合规按钮、
// 或个数比清单多都会失败；个数比清单少会失败并提示把数字改小，清零了提示把这一项删掉。手机端 short-feed/ 不查。
const BUTTON_CLASSES = new Set(['btn', 'btn-link', 'btn-icon', 'btn-plain']);
const INTERNAL_BUTTONS = new Set([
  'components/ui/AppTabs.vue:ui-tabs__tab',
  'components/ui/SegmentedControl.vue:ui-segmented__option',
  'components/ui/FilterChip.vue:ui-filter-chip__toggle',
  'components/ui/FilterChip.vue:ui-filter-chip__remove',
  'components/ui/TagPill.vue:ui-tag-pill'
]);

function buttonClasses(attrs) {
  const names = new Set();
  for (const match of attrs.matchAll(/(?<![\w-])(:?)class="([^"]*)"/g)) {
    if (match[1] === ':') for (const literal of match[2].matchAll(/'([A-Za-z0-9_-]+)'/g)) names.add(literal[1]);
    else for (const name of match[2].split(/\s+/)) if (name) names.add(name);
  }
  return names;
}

function offStyleButtons(path, file) {
  const source = readFileSync(path, 'utf8');
  const template = (source.match(/<template>([\s\S]*)<\/template>/) || [, ''])[1].replace(/<!--[\s\S]*?-->/g, '');
  const found = [];
  for (const match of template.matchAll(/<button\b((?:[^>"]|"[^"]*")*)>/g)) {
    const names = buttonClasses(match[1]);
    if ([...names].some(name => BUTTON_CLASSES.has(name) || INTERNAL_BUTTONS.has(`${file}:${name}`))) continue;
    found.push(`button@${template.slice(0, match.index).split('\n').length}`);
  }
  return found;
}

describe('buttons use the unified button classes (D-UD-15)', () => {
  it('every button has .btn / .btn-link / .btn-icon or a registered internal exception; the pending counts only go down', () => {
    const { lists, problems } = loadPendingLists(SRC);
    const actual = new Map();
    for (const path of listVueFiles(SRC)) {
      const file = relative(SRC, path);
      if (file.startsWith('short-feed/')) continue;
      const buttons = offStyleButtons(path, file);
      if (buttons.length) actual.set(file, buttons);
    }
    expect([...problems, ...ratchetFailures('buttons', actual, lists.get('buttons'))]).toEqual([]);
  });

  it('registered internal exceptions still exist in their component', () => {
    for (const entry of INTERNAL_BUTTONS) {
      const [file, name] = entry.split(':');
      expect(readFileSync(join(SRC, file), 'utf8'), entry).toContain(name);
    }
  });
});
