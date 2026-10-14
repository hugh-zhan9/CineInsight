// P-002 的行为保持网：巨型组件拆分会把大段模板搬到子组件里，搬漏一段的最直接
// 症状就是某个 data-test 钩子消失，而挂在它上面的测试要么找不到元素、要么静默
// 变成空断言。这里把拆分前的钩子集合钉成基线：允许新增，不允许缺失。
import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const srcDir = fileURLToPath(new URL('../src', import.meta.url));
const baselinePath = fileURLToPath(
  new URL('../../.loopx/workspace/2026-09-02-capability-batch/baseline/data-test-set-2026-09-02.txt', import.meta.url)
);

function collectVueFiles(dir) {
  const found = [];
  for (const entry of readdirSync(dir).sort()) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      found.push(...collectVueFiles(full));
      continue;
    }
    if (entry.endsWith('.vue')) found.push(full);
  }
  return found;
}

// 钩子有两种写法：模板属性 data-test="…" / :data-test="…"（含共享组件的 select-all-data-test="…" 这类钩子属性），
// 以及交给 components/ui 的 AppTabs / SegmentedControl 的逐项字段 dataTest: '…'（组件把它渲染成这一项的
// data-test，见各自的单测）。后一种按前一种的样子记：dataTest: 'x' 记成 data-test="x"，
// dataTest: `x-${y}` 记成 data-test="`x-${y}`"（与 :data-test="`x-${y}`" 记法一致）。
const ITEM_HOOK = /(?<![\w$])dataTest:\s*(?:'([^'\n]*)'|"([^"\n]*)"|(`[^`\n]*`))/g;

// 注释里的钩子不算（2026-10-14）：只在注释里提到的钩子不能让基线通过。收集前先把注释换成空白（换行留着），
// 按 SFC 的块分别认：<script> 认 // 与 /* */，跳过字符串、模板字符串（含 ${} 里嵌套的）与正则字面量；
// <style> 认 /* */；其余（模板）只认 <!-- -->，模板里的 // 不是注释（例如 placeholder="https://…"）。
const SFC_BLOCK = /(<(script|style)\b[^>]*>)([\s\S]*?)(<\/\2>)/g;
const blank = text => text.replace(/[^\n]/g, ' ');
const stripHtmlComments = text => text.replace(/<!--[\s\S]*?-->/g, blank);
const stripCssComments = text => text.replace(/\/\*[\s\S]*?\*\//g, blank);
// / 跟在这些字符或关键字后面是正则字面量的开头，否则是除号。
const REGEX_AFTER = new Set([...'(,=:[!&|?{};+-*%<>~^']);
const REGEX_AFTER_WORDS = new Set(['return', 'typeof', 'case', 'do', 'else', 'in', 'of', 'void', 'delete', 'throw', 'new', 'yield', 'await']);

function stripScriptComments(code) {
  let out = '';
  let i = 0;
  let prev = ''; // 上一个非空白字符（标识符与数字记作 a）
  let word = ''; // 上一个标识符
  let depth = 0; // 花括号深度
  const templates = []; // 进 ${ 时的深度：回到这一层的 } 就回到模板字符串里
  const take = end => {
    out += code.slice(i, end);
    i = end;
  };
  // i 在模板字符串内容里：读到收尾的反引号，或读到 ${ 回到代码。
  const takeTemplate = () => {
    for (let j = i; j < code.length; j += 1) {
      if (code[j] === '\\') {
        j += 1;
      } else if (code[j] === '`') {
        take(j + 1);
        prev = '`';
        word = '';
        return;
      } else if (code[j] === '$' && code[j + 1] === '{') {
        templates.push(depth);
        depth += 1;
        take(j + 2);
        prev = '{';
        word = '';
        return;
      }
    }
    take(code.length);
  };
  while (i < code.length) {
    const c = code[i];
    if (c === '/' && (code[i + 1] === '/' || code[i + 1] === '*')) {
      const close = code[i + 1] === '/' ? code.indexOf('\n', i) : code.indexOf('*/', i + 2);
      const end = close === -1 ? code.length : code[i + 1] === '/' ? close : close + 2;
      out += blank(code.slice(i, end));
      i = end;
      continue;
    }
    // 字符串到同样的引号为止，换行处收住，不让一个没配对的引号吞掉后面的代码。
    if (c === "'" || c === '"') {
      let j = i + 1;
      while (j < code.length && code[j] !== c && code[j] !== '\n') j += code[j] === '\\' ? 2 : 1;
      take(code[j] === c ? j + 1 : Math.min(j, code.length));
      prev = c;
      word = '';
      continue;
    }
    if (c === '`') {
      take(i + 1);
      takeTemplate();
      continue;
    }
    if (c === '/' && (prev === '' || REGEX_AFTER.has(prev) || REGEX_AFTER_WORDS.has(word))) {
      let j = i + 1;
      let inClass = false;
      while (j < code.length && code[j] !== '\n' && (inClass || code[j] !== '/')) {
        if (code[j] === '[') inClass = true;
        else if (code[j] === ']') inClass = false;
        j += code[j] === '\\' ? 2 : 1;
      }
      take(code[j] === '/' ? j + 1 : Math.min(j, code.length));
      prev = '/';
      word = '';
      continue;
    }
    if (c === '}' && templates.at(-1) === depth - 1) {
      templates.pop();
      depth -= 1;
      take(i + 1);
      takeTemplate();
      continue;
    }
    if (c === '{') depth += 1;
    else if (c === '}') depth -= 1;
    take(i + 1);
    if (/\s/.test(c)) continue;
    if (/[\w$]/.test(c)) {
      word = prev === 'a' && /[\w$]/.test(code[i - 2] ?? '') ? word + c : c;
      prev = 'a';
    } else {
      prev = c;
      word = '';
    }
  }
  return out;
}

function stripComments(source) {
  let out = '';
  let last = 0;
  for (const match of source.matchAll(SFC_BLOCK)) {
    const [, open, kind, body, close] = match;
    out += stripHtmlComments(source.slice(last, match.index));
    out += open + (kind === 'script' ? stripScriptComments(body) : stripCssComments(body)) + close;
    last = match.index + match[0].length;
  }
  return out + stripHtmlComments(source.slice(last));
}

function collectHooks(source, file, into) {
  const code = stripComments(source);
  for (const match of code.matchAll(/data-test="[^"]*"/g)) {
    if (!into.has(match[0])) into.set(match[0], file);
  }
  for (const match of code.matchAll(ITEM_HOOK)) {
    const hook = `data-test="${match[1] ?? match[2] ?? match[3]}"`;
    if (!into.has(hook)) into.set(hook, file);
  }
  return into;
}

// 自检：注释（模板的 <!-- -->、脚本的 // 与 /* */、样式的 /* */）里的钩子不算；模板属性、:data-test 与逐项字段照样算，
// 字符串里的 //（网址）、模板字符串（含嵌套）、正则里的 /* 与除号后面的行注释都不会被认错。
const FIXTURE = [
  '<template>',
  '  <!-- <button data-test="commented-out-button">旧按钮</button> -->',
  '  <input data-test="real-attr" placeholder="https://example.com/a // b">',
  '  <div :data-test="`row-${id}`"><!-- data-test="html-comment-inline" --></div>',
  '</template>',
  '',
  '<script>',
  "// dataTest: 'line-comment-item'",
  '/* 旧写法 data-test="block-comment-attr" */',
  "const url = 'https://example.com/a'; const first = { dataTest: 'after-url' };",
  "const label = `a // b /* c ${cond ? 'x' : `y-${n}`} d`; const second = { dataTest: 'after-template' }; // dataTest: 'template-tail-comment'",
  "const hint = `${count} 项 // 已选`; const fourth = { dataTest: 'after-template-tail' };",
  "const pattern = /[/*]+/g; const third = { dataTest: 'after-regex' };",
  "const ratio = total / 2; // dataTest: 'after-division-comment'",
  "export default { data: () => ({ tabs: [{ key: 'a', dataTest: `tab-${key}` }] }) };",
  '</script>',
  '',
  '<style scoped>',
  '/* [data-test="css-comment"] */',
  '.row { color: var(--text-primary); }',
  '</style>'
].join('\n');
assert.deepEqual(
  [...collectHooks(FIXTURE, 'fixture.vue', new Map()).keys()].sort(),
  [
    'data-test="real-attr"',
    'data-test="`row-${id}`"',
    'data-test="after-url"',
    'data-test="after-template"',
    'data-test="after-template-tail"',
    'data-test="after-regex"',
    'data-test="`tab-${key}`"'
  ].sort(),
  '收集器自检：注释里的钩子不算，代码里的照样算'
);
// 换成空白不改变长度与换行：出错时报的位置还对得上原文件。
assert.equal(stripComments(FIXTURE).length, FIXTURE.length);
assert.equal(stripComments(FIXTURE).split('\n').length, FIXTURE.split('\n').length);

const present = new Map();
for (const file of collectVueFiles(srcDir)) {
  collectHooks(readFileSync(file, 'utf8'), file, present);
}

const baseline = readFileSync(baselinePath, 'utf8')
  .split('\n')
  .map(line => line.trim())
  .filter(Boolean);

assert.ok(baseline.length > 0, 'data-test baseline should not be empty');

// 2026-09-23 用户批准统一标签库，独立 AI 库编辑器及其重载入口已整体退役。
// 2026-09-29 P-033：设置页「反馈回流」开关删除（P-021 结论：它只控制已删掉的收藏 / 点赞投影）。
// 只豁免这些已删除的功能，其余基线钩子仍必须存在。
const retired = new Set([
  'data-test="reload-ai-tag-library"',
  'data-test="short-feed-feedback-sync-toggle"'
]);
for (const hook of retired) {
  assert.ok(baseline.includes(hook), `退役钩子必须来自基线: ${hook}`);
  assert.ok(!present.has(hook), `已退役的入口不应继续出现: ${hook}`);
}
const missing = baseline.filter(hook => !retired.has(hook) && !present.has(hook));
assert.deepEqual(
  missing,
  [],
  `拆分后丢失了 ${missing.length} 个 data-test 钩子（基线 ${baseline.length} 个）：\n${missing.join('\n')}`
);

console.log(`data-test set tests passed (${baseline.length} baseline hooks, ${present.size} present)`);
