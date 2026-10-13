// 统一设计守卫（D-UD-15，详细设计 §5）：防止换装后回退。七条规则，每条带一份「待迁移」清单：
//   colors       1. .vue / .css 里不许写 hex 颜色与 rgb()/rgba()/hsl() 字面量，只用 var(--…)（tokens.css 除外）
//   font-scale   2. font-size 只用 var(--fs-*)；font-weight 只用 var(--fw-*) 或 400/500/600/700（normal、bold、inherit 同义放行）
//   z-index      3. z-index 只用 var(--z-*)、0、1（局部叠放）或 auto
//   vars         4. 引用的 CSS 变量必须在 tokens.css 里定义（本文件自己声明的局部变量、运行期 setProperty 写的除外）
//   dialogs      5. 不许有 class="modal-overlay" 的自制遮罩；BaseModal 只给 ui/DialogShell 内部用，页面一律用 DialogShell
//   page-header  6. 顶层页面（顶栏导航列出的各页）模板里必须有 PageHeader 或 HeroHeader
//   wording      7. 模板文本里不许出现用词表（详细设计 §4）的旧词：...、选择本页、打开所在目录 等
// 手机端 short-feed/ 不在统一设计范围内（受保护，见概要设计「受保护的旧行为」），整个目录不查。
//
// 待迁移清单在 scripts/design-system-pending/ 下，一个文件对应一个负责组（B1…B4 按详细设计 §6 的文件归属，
// shared 是 App 外壳与公共层，第 0 步 C 组清零），各组只改自己的文件，并行换装不会互相冲突。
// 清单只减不增，守卫双向检查：
//   - 不在清单里的文件违反了规则 → 失败（新代码必须直接合规，不许往清单里加）；
//   - 清单里的文件已经不违反该规则 → 也失败，提示把它从清单里划掉（清单不留过期项）。
// 各组换装一个文件后：跑 `node scripts/design-system.test.mjs`，按「已合规，请从清单删除」的提示删掉对应行，
// 直到通过；想看某条规则当前的全部违规明细，跑 `node scripts/design-system.test.mjs --report <规则名>`。
// 同一份 JSON 里的 buttons 清单由 src/components/unstyledControls.test.js（vitest）检查，规则见那个文件。
import assert from 'node:assert/strict';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const SRC = fileURLToPath(new URL('../src', import.meta.url));
const PENDING_DIR = fileURLToPath(new URL('./design-system-pending', import.meta.url));
const RULES = ['colors', 'font-scale', 'z-index', 'vars', 'dialogs', 'page-header', 'wording'];
const LIST_KEYS = new Set([...RULES, 'buttons']);

function walk(dir) {
  return readdirSync(dir).sort().flatMap(name => {
    const full = join(dir, name);
    return statSync(full).isDirectory() ? walk(full) : [full];
  });
}

const ALL_FILES = walk(SRC).map(file => relative(SRC, file));
const STYLE_FILES = ALL_FILES.filter(file => /\.(vue|css)$/.test(file) && !file.startsWith('short-feed/'));
const read = file => readFileSync(join(SRC, file), 'utf8');

// 注释里提到旧值是允许的（说明「原来写死 #fff」之类），只查真正生效的代码。
// 行注释只认行首或空白、分号、括号后面的 //，避免把 url(http://…) 截断。
function stripComments(text) {
  return text
    .replace(/\/\*[\s\S]*?\*\//g, match => match.replace(/[^\n]/g, ' '))
    .replace(/<!--[\s\S]*?-->/g, match => match.replace(/[^\n]/g, ' '))
    .replace(/(^|[\s;{}(),])\/\/[^\n]*/gm, '$1');
}

function templateOf(source) {
  return (source.match(/<template>([\s\S]*)<\/template>/) || [, ''])[1];
}

const lineOf = (text, index) => text.slice(0, index).split('\n').length;

function declarations(text, property) {
  return [...text.matchAll(new RegExp(`(?<![\\w-])${property}\\s*:\\s*([^;{}"\\n]+)`, 'g'))]
    .map(match => ({ value: match[1].trim().replace(/\s*!important$/, ''), line: lineOf(text, match.index) }));
}

const TOKENS = read('styles/tokens.css');
const TOKEN_NAMES = new Set([...TOKENS.matchAll(/(--[\w-]+)\s*:/g)].map(match => match[1]));
// 运行期由脚本写到元素上的变量（如 recap/reveal.js 的 --reveal-delay）。
const RUNTIME_VARS = new Set(ALL_FILES.filter(file => /\.(js|vue)$/.test(file) && !file.includes('.test.'))
  .flatMap(file => [...read(file).matchAll(/setProperty\(\s*['"](--[\w-]+)['"]/g)].map(match => match[1])));

const HEX = /(?<![\w&#%])#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{4}|[0-9a-fA-F]{3})(?![\w-])/g;
const FONT_SIZE_OK = /^(var\(--fs-[\w-]+\)|inherit)$/;
const FONT_WEIGHT_OK = /^(var\(--fw-[\w-]+\)|400|500|600|700|normal|bold|inherit)$/;
const Z_INDEX_OK = /^(var\(--z-[\w-]+\)|0|1|auto)$/;
// 用词表（详细设计 §4）里能无歧义查出的旧词。「删除」「完成」「预览」「移入回收站」要看语境，靠各组人工改。
const OLD_WORDS = ['选择本页', '全选已加载', '全选已列出的', '清除选择', '打开所在目录', '打开目录', '在文件夹里找',
  '重新读取', '重新加载', '重新载入', '重新统计', '彻底删除', '只删记录', '移除记录', '保留这份', '批量删除', '删除所选', '✕'];
// 三个英文句点当省略号；JS 展开语法（...items、...[、...{、...(）不算。
const ASCII_ELLIPSIS = /\.\.\.(?![\w$[({])/g;

// 顶栏导航列出的页面 key → 页面组件（同一个常挂载实例的两个 key 指向同一文件）。新增导航页必须在这里登记。
const PAGE_FILES = {
  home: 'components/viewer/HomePage.vue',
  videos: 'components/VideoListPage.vue',
  'library-manage': 'components/VideoListPage.vue',
  people: 'components/EntityLibraryPage.vue',
  collections: 'components/EntityLibraryPage.vue',
  photos: 'components/PhotoLibraryPage.vue',
  'photos-manage': 'components/PhotoLibraryPage.vue',
  watchlist: 'components/WatchlistPage.vue',
  'movie-chart': 'components/MovieChartPage.vue',
  'watched-movies': 'components/WatchedMoviesPage.vue',
  'viewing-notes': 'components/ViewingNotesPage.vue',
  recap: 'components/RecapPage.vue',
  scenes: 'components/ScenesPage.vue',
  'manage-home': 'components/manage/ManageHomePage.vue',
  downloads: 'components/DownloadsPage.vue',
  'video-workbench': 'components/VideoWorkbenchPage.vue',
  insights: 'components/InsightsPage.vue',
  'data-care': 'components/DataCarePage.vue',
  settings: 'components/SettingsPage.vue'
};

function navPageKeys() {
  const source = read('utils/appCommands.js');
  const start = source.indexOf('export const VIEWER_NAV_GROUPS');
  const end = source.indexOf('export const VIDEO_DETAIL_PAGE');
  assert.ok(start !== -1 && end > start, 'appCommands.js 的导航清单位置变了，守卫 6 需要跟着改');
  const keys = [];
  for (const match of source.slice(start, end).matchAll(/\{\s*key:\s*'([\w-]+)',\s*label:\s*'[^']*'(?:,\s*(command|keywords|requiresBrowserBridge)\b[^}]*)?\}/g)) {
    if (match[2] !== 'command') keys.push(match[1]);
  }
  return keys;
}

function scan() {
  const found = new Map(RULES.map(rule => [rule, new Map()]));
  const add = (rule, file, detail) => {
    const files = found.get(rule);
    if (!files.has(file)) files.set(file, []);
    files.get(file).push(detail);
  };
  for (const file of STYLE_FILES) {
    const source = read(file);
    const code = stripComments(source);
    if (file !== 'styles/tokens.css') {
      for (const match of code.matchAll(HEX)) {
        // <template #slot> 的插槽简写不是颜色。
        if (/<template\b[^>]*$/.test(code.slice(Math.max(0, match.index - 200), match.index))) continue;
        add('colors', file, `${lineOf(code, match.index)}: ${match[0]}`);
      }
      for (const match of code.matchAll(/\b(?:rgba?|hsla?)\(/g)) add('colors', file, `${lineOf(code, match.index)}: ${match[0]}…)`);
      for (const { value, line } of declarations(code, 'font-size')) if (!FONT_SIZE_OK.test(value)) add('font-scale', file, `${line}: font-size ${value}`);
      for (const { value, line } of declarations(code, 'font-weight')) if (!FONT_WEIGHT_OK.test(value)) add('font-scale', file, `${line}: font-weight ${value}`);
      for (const { value, line } of declarations(code, 'z-index')) if (!Z_INDEX_OK.test(value)) add('z-index', file, `${line}: z-index ${value}`);
    }
    const local = new Set([...code.matchAll(/(--[\w-]+)\s*:/g), ...code.matchAll(/['"](--[\w-]+)['"]/g)].map(match => match[1]));
    for (const match of code.matchAll(/var\(\s*(--[\w-]+)/g)) {
      const name = match[1];
      if (!TOKEN_NAMES.has(name) && !local.has(name) && !RUNTIME_VARS.has(name)) add('vars', file, `${lineOf(code, match.index)}: ${name}`);
    }
    if (!file.endsWith('.vue')) continue;
    const template = stripComments(templateOf(source));
    if (file !== 'components/ui/DialogShell.vue' && file !== 'components/ui/BaseModal.vue') {
      for (const match of template.matchAll(/<BaseModal\b/g)) add('dialogs', file, `${lineOf(template, match.index)}: <BaseModal>`);
      for (const match of template.matchAll(/(?<![:\w-])class="[^"]*\bmodal-overlay\b/g)) add('dialogs', file, `${lineOf(template, match.index)}: class="modal-overlay"`);
    }
    for (const word of OLD_WORDS) {
      let index = template.indexOf(word);
      while (index !== -1) {
        add('wording', file, `${lineOf(template, index)}: ${word}`);
        index = template.indexOf(word, index + word.length);
      }
    }
    for (const match of template.matchAll(ASCII_ELLIPSIS)) add('wording', file, `${lineOf(template, match.index)}: ...`);
  }
  for (const file of new Set(Object.values(PAGE_FILES))) {
    if (!/<(PageHeader|HeroHeader)\b/.test(stripComments(templateOf(read(file))))) add('page-header', file, '模板里没有 PageHeader / HeroHeader');
  }
  return found;
}

function loadPendingLists() {
  const lists = new Map(RULES.map(rule => [rule, new Map()]));
  const problems = [];
  for (const name of readdirSync(PENDING_DIR).filter(entry => entry.endsWith('.json')).sort()) {
    const data = JSON.parse(readFileSync(join(PENDING_DIR, name), 'utf8'));
    for (const [key, files] of Object.entries(data.pending || {})) {
      if (!LIST_KEYS.has(key)) problems.push(`${name}: 未知的清单 ${key}`);
      if (!Array.isArray(files)) continue;
      const sorted = [...files].sort();
      if (files.some((file, index) => file !== sorted[index]) || new Set(files).size !== files.length) problems.push(`${name}: ${key} 要按字母序排列且不重复`);
      if (!lists.has(key)) continue;
      for (const file of files) {
        if (!existsSync(join(SRC, file))) problems.push(`${name}: ${key} 里的 ${file} 不存在（已删除的文件请从清单划掉）`);
        if (lists.get(key).has(file)) problems.push(`${name}: ${key} 里的 ${file} 同时出现在 ${lists.get(key).get(file)}`);
        lists.get(key).set(file, name);
      }
    }
  }
  return { lists, problems };
}

const found = scan();

// --report [规则名]：打印当前违规明细（按文件），不做断言，给换装时对照用。
const reportIndex = process.argv.indexOf('--report');
if (reportIndex !== -1) {
  const only = process.argv[reportIndex + 1];
  const report = {};
  for (const [rule, files] of found) {
    if (only && rule !== only) continue;
    report[rule] = Object.fromEntries([...files].sort(([a], [b]) => a.localeCompare(b)));
  }
  console.log(JSON.stringify(report, null, 2));
  process.exit(0);
}

const navKeys = navPageKeys();
assert.deepEqual(
  [...new Set(navKeys)].sort(),
  Object.keys(PAGE_FILES).sort(),
  '顶栏导航的页面与守卫 6 的 PAGE_FILES 对不上：新增或改名的页面要在 PAGE_FILES 里登记它的组件文件'
);

const { lists, problems } = loadPendingLists();
const failures = [...problems];
const summary = [];
for (const rule of RULES) {
  const violating = found.get(rule);
  const pending = lists.get(rule);
  for (const [file, details] of violating) {
    if (!pending.has(file)) failures.push(`[${rule}] ${file} 不合规（新代码不能进待迁移清单，请直接改）：\n    ${details.slice(0, 8).join('\n    ')}`);
  }
  for (const [file, owner] of pending) {
    if (!violating.has(file)) failures.push(`[${rule}] ${file} 已合规，请从 design-system-pending/${owner} 的 ${rule} 清单删除`);
  }
  summary.push(`${rule} ${pending.size}`);
}

assert.deepEqual(failures, [], `统一设计守卫未通过（${failures.length} 项）：\n${failures.join('\n')}`);
console.log(`design-system guard passed (pending: ${summary.join(', ')})`);
