import { existsSync, readFileSync, readdirSync } from 'node:fs';
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

// 统一设计守卫（D-UD-15，详细设计 §5 末段）：按钮必须带 .btn / .btn-link / .btn-icon 之一，或是 components/ui 组件内部
// 已登记的例外（下面 INTERNAL_BUTTONS，按「文件:类名」登记）。旧按钮类（btn-primary、row-btn、toolbar-btn…）
// 换装前暂由 scripts/design-system-pending/*.json 的 buttons 清单放行：清单只减不增——不在清单里的文件出现
// 不合规按钮会失败，清单里的文件已经全部合规也会失败（提示把它划掉）。手机端 short-feed/ 不查。
const BUTTON_CLASSES = new Set(['btn', 'btn-link', 'btn-icon']);
const INTERNAL_BUTTONS = new Set([
  'components/ui/AppTabs.vue:ui-tabs__tab',
  'components/ui/SegmentedControl.vue:ui-segmented__option',
  'components/ui/FilterChip.vue:ui-filter-chip__toggle',
  'components/ui/FilterChip.vue:ui-filter-chip__remove',
  'components/ui/TagPill.vue:ui-tag-pill'
]);
const PENDING_DIR = join(process.cwd(), 'scripts', 'design-system-pending');

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

function pendingButtonFiles() {
  const pending = new Map();
  for (const name of readdirSync(PENDING_DIR).filter(entry => entry.endsWith('.json'))) {
    const data = JSON.parse(readFileSync(join(PENDING_DIR, name), 'utf8'));
    for (const file of data.pending?.buttons || []) pending.set(file, name);
  }
  return pending;
}

describe('buttons use the unified button classes (D-UD-15)', () => {
  it('every button has .btn / .btn-link / .btn-icon or a registered internal exception; the pending list only shrinks', () => {
    const pending = pendingButtonFiles();
    const offenders = [];
    const fixed = [];
    for (const path of listVueFiles(SRC)) {
      const file = relative(SRC, path);
      if (file.startsWith('short-feed/')) continue;
      const buttons = offStyleButtons(path, file);
      if (buttons.length && !pending.has(file)) offenders.push(`${file}: ${buttons.join(' ')}`);
      if (!buttons.length && pending.has(file)) fixed.push(`${file} 已合规，请从 design-system-pending/${pending.get(file)} 的 buttons 清单删除`);
    }
    for (const [file, owner] of pending) {
      if (!existsSync(join(SRC, file))) fixed.push(`${file} 不存在，请从 design-system-pending/${owner} 的 buttons 清单删除`);
    }
    expect(offenders).toEqual([]);
    expect(fixed).toEqual([]);
  });

  it('registered internal exceptions still exist in their component', () => {
    for (const entry of INTERNAL_BUTTONS) {
      const [file, name] = entry.split(':');
      expect(readFileSync(join(SRC, file), 'utf8'), entry).toContain(name);
    }
  });
});
