import { readFileSync, readdirSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const SRC = join(process.cwd(), 'src');
const GLOBAL_CSS = ['styles/tokens.css', 'styles/components.css', 'short-feed/short-feed.css'];

function listVueFiles(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) return listVueFiles(full);
    return entry.name.endsWith('.vue') ? [full] : [];
  });
}

function classNames(css) {
  return new Set([...css.matchAll(/\.([A-Za-z][A-Za-z0-9_-]*)/g)].map(m => m[1]));
}

// 模板里出现的类名：静态 class="a b" 和 :class="['a', { 'b': cond }]" 里的字面量。
function templateClasses(template) {
  const found = new Set();
  for (const match of template.matchAll(/(:?)class="([^"]+)"/g)) {
    if (match[1] === ':') {
      for (const name of match[2].matchAll(/'([A-Za-z0-9_-]+)'/g)) found.add(name[1]);
    } else {
      for (const name of match[2].split(/\s+/)) if (name) found.add(name);
    }
  }
  return found;
}

function parseComponent(path) {
  const source = readFileSync(path, 'utf8');
  const blocks = [...source.matchAll(/<style([^>]*)>([\s\S]*?)<\/style>/g)];
  const firstStyle = blocks.length ? source.indexOf(blocks[0][0]) : source.length;
  const scoped = new Set();
  let unscoped = '';
  for (const [, attrs, body] of blocks) {
    if (attrs.includes('scoped')) for (const name of classNames(body)) scoped.add(name);
    else unscoped += body;
  }
  return { template: source.slice(0, firstStyle), scoped, unscoped };
}

describe('组件间共享的类名不能只存在于某个组件的 scoped 样式里', () => {
  it('每个模板用到的类名，都不是别处 scoped 私有的', () => {
    const files = listVueFiles(SRC);
    const parsed = new Map(files.map(file => [file, parseComponent(file)]));
    let globalCss = GLOBAL_CSS.map(name => readFileSync(join(SRC, name), 'utf8')).join('\n');
    for (const { unscoped } of parsed.values()) globalCss += `\n${unscoped}`;
    const globalNames = classNames(globalCss);

    const leaks = [];
    for (const [file, { template, scoped }] of parsed) {
      for (const name of templateClasses(template)) {
        if (globalNames.has(name) || scoped.has(name)) continue;
        const owners = files.filter(other => other !== file && parsed.get(other).scoped.has(name));
        if (owners.length) {
          leaks.push(`${relative(SRC, file)} 用了 .${name}，但它只定义在 ${owners.map(o => relative(SRC, o)).join(', ')} 的 scoped 样式里`);
        }
      }
    }

    expect(leaks).toEqual([]);
  });
});

// BasePopover 默认把面板 teleport 到 body，组件 scoped 样式里的
// :deep(.面板类) 编译成 [data-v-x] .面板类，teleport 之后没有那个祖先，
// 面板级规则会整个失效（浮层看起来"样式全丢"）。所以面板类必须定义在全局样式里。
describe('teleport 的浮层面板类必须有全局样式', () => {
  it('每个 panel-class 都能在非 scoped 样式里找到定义', () => {
    const files = listVueFiles(SRC);
    const parsed = new Map(files.map(file => [file, parseComponent(file)]));
    let globalCss = GLOBAL_CSS.map(name => readFileSync(join(SRC, name), 'utf8')).join('\n');
    for (const { unscoped } of parsed.values()) globalCss += `\n${unscoped}`;
    const globalNames = classNames(globalCss);

    const problems = [];
    for (const [file, { template, scoped }] of parsed) {
      for (const match of template.matchAll(/panel-class="([^"]+)"/g)) {
        // 关掉 teleport 的浮层留在组件子树里，scoped 规则仍然有效。
        const block = template.slice(Math.max(0, match.index - 600), match.index + 200);
        if (/:teleport="false"/.test(block)) continue;
        for (const name of match[1].split(/\s+/).filter(Boolean)) {
          if (globalNames.has(name)) continue;
          const where = scoped.has(name) ? '只定义在本组件的 scoped 样式里' : '哪儿都没有定义';
          problems.push(`${relative(SRC, file)} 的 panel-class="${name}" ${where}`);
        }
      }
    }

    expect(problems).toEqual([]);
  });
});

// BaseModal 的 scope id 只挂在它的根节点（遮罩层），传进去的 class 却落在内层
// .modal 面板上。所以 <BaseModal class="x"> 的面板级规则写成普通 scoped 选择器
// （编译成 .x[data-v-y]）时整条失效——面板会退回全局 .modal 的 500px 宽且不封高，
// 内容一多就撑出应用窗口。这类规则必须写成 :deep(.x)。
describe('传给 BaseModal 的面板类必须用 :deep 才能落到内层面板上', () => {
  it('每个 <BaseModal class="x"> 的 .x 都不是普通 scoped 选择器', () => {
    const problems = [];
    for (const file of listVueFiles(SRC)) {
      const source = readFileSync(file, 'utf8');
      const firstStyle = source.indexOf('<style');
      const template = firstStyle === -1 ? source : source.slice(0, firstStyle);
      const styles = firstStyle === -1 ? '' : source.slice(firstStyle);
      for (const tag of template.matchAll(/<BaseModal\b[^>]*>/gs)) {
        const classAttr = /\sclass="([^"]+)"/.exec(tag[0]);
        if (!classAttr) continue;
        for (const name of classAttr[1].split(/\s+/).filter(Boolean)) {
          const escaped = name.replace(/[-]/g, '\\-');
          const plain = new RegExp(`(?<![\\w:(\\-])\\.${escaped}\\s*\\{`).test(styles);
          if (plain) {
            problems.push(`${relative(SRC, file)} 把 .${name} 写成了普通 scoped 选择器，应该用 :deep(.${name})`);
          }
        }
      }
    }

    expect(problems).toEqual([]);
  });
});
