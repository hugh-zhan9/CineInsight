// 统一设计守卫（D-UD-15，详细设计 §5）：防止换装后回退。
//
// 【待迁移清单是棘轮】清单在 scripts/design-system-pending/*.json，一个文件对应一个负责组（B1…B4 按详细设计 §6
// 的文件归属，shared 是 App 外壳与公共层，第 0 步 C 组清零），各组只改自己的 JSON，并行换装不会互相冲突。
// 每条规则下按文件记「还剩几处违规」，例如 "components/VideoListPage.vue": 14。守卫逐文件比对实际处数：
//   - 有违规但不在清单里 → 失败（新代码必须直接合规，不许往清单里加）；
//   - 实际处数比清单多 → 失败（这个文件又新增了违规，改掉新增的）；
//   - 实际处数比清单少 → 失败，提示把数字降到实际值（换掉一处就锁住一处，不会被别处的新增抵消）；
//   - 实际为 0 → 失败，提示把这一项删掉。
// 换装时：改完跑 `node scripts/design-system.test.mjs`，按提示把数字改小或删行，直到通过；
// 想看某条规则当前的全部违规明细，跑 `node scripts/design-system.test.mjs --report <规则名>`。
// 比对与清单格式在 scripts/design-system-ratchet.mjs；同一份 JSON 里的 buttons 清单由
// src/components/unstyledControls.test.js（vitest）按同一棘轮检查。
// 扫描前先跑两段自检（selfCheck：各条规则查得出该查的写法、不误报放行的写法；ratchetSelfCheck：棘轮的四种失败），
// 改规则或棘轮时一起改。
//
// 八条规则（前七条走待迁移清单的棘轮，第 8 条不走）：
//   colors       1. .vue / .css 里不许写颜色字面量，只用 var(--…)（tokens.css 除外，影院表面也在 tokens.css 里）：
//                   hex；rgb()/rgba()/hsl()/hsla()/hwb()/lab()/lch()/oklab()/oklch()；颜色属性里的 CSS 颜色名
//                   （white、black、red、gray… 全表；transparent、currentColor、inherit、initial、unset 放行）；
//                   模板 :style 对象里 color / background 的其他字面量
//   font-scale   2. font-size 只用 var(--fs-*)；font-weight 只用 var(--fw-*) 或 400/500/600/700（normal、bold、inherit
//                   同义放行）；font: 简写里不许带 px 字号；:style 对象里的 fontSize 字面量同样要用 var(--fs-*)
//   z-index      3. z-index（含 :style 对象里的 zIndex）只用 var(--z-*)、auto 或 -1 到 3 的局部叠放
//   vars         4. 引用的 CSS 变量必须在 tokens.css 里定义（本文件自己声明的局部变量、运行期 setProperty 写的除外）
//   dialogs      5. 模板 class 绑定（静态或 :class）里不许有 modal-overlay 的自制遮罩；BaseModal（含 <base-modal>
//                   写法与改名注册）只给 ui/DialogShell 内部用，页面一律用 DialogShell
//   page-header  6. 顶层页面（顶栏导航列出的各页）模板里必须有 PageHeader 或 HeroHeader
//   wording      7. 模板文本里不许出现用词表（详细设计 §4）的旧词：...、选择本页、打开所在目录 等
//   tab-panels   8. 用了 AppTabs 的模板要接上面板：AppTabs 传 id-prefix（页签才有 id 与 aria-controls），面板写
//                   role="tabpanel"、id（前缀-panel-…）与 aria-labelledby（前缀-tab-…）。这条不走待迁移清单：新代码直接
//                   接好；确实没有面板可接的，写进 TAB_PANEL_EXCEPTIONS 并附上理由
// 手机端 short-feed/ 不在统一设计范围内（受保护，见概要设计「受保护的旧行为」），整个目录不查。
import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

import { loadPendingLists, PENDING_KEYS, pendingTotal, ratchetFailures } from './design-system-ratchet.mjs';

const SRC = fileURLToPath(new URL('../src', import.meta.url));
const RULES = PENDING_KEYS.filter(key => key !== 'buttons');

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

// 把 <script> 块换成等长空白（保留换行与下标），只留样式与模板给「颜色名」检查：脚本里的普通对象
// （选项、图表配置）不是样式声明。
function blankScripts(text) {
  return text.replace(/<script\b[\s\S]*?<\/script>/g, match => match.replace(/[^\n]/g, ' '));
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
const COLOR_FUNCTION = /\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch)\(/g;
// CSS 颜色名全表（CSS Color 4）。transparent、currentColor 与全局关键字不在表里，自然放行。
const NAMED_COLORS = `aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown
  burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray
  darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue
  darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite
  forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred indigo ivory
  khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen
  lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime
  limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue
  mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive
  olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum
  powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue
  slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white whitesmoke
  yellow yellowgreen`.split(/\s+/);
const NAMED_COLOR = new RegExp(`(?<![\\w-])(?:${NAMED_COLORS.join('|')})(?![\\w-])`, 'gi');
// 取值是颜色的属性（CSS 写法，同时认 :style 对象里的驼峰写法与带引号的键）。
const COLOR_PROPERTIES = ['color', 'background', 'background-color', 'border', 'border-top', 'border-right', 'border-bottom',
  'border-left', 'border-color', 'border-top-color', 'border-right-color', 'border-bottom-color', 'border-left-color',
  'border-block', 'border-inline', 'outline', 'outline-color', 'box-shadow', 'text-shadow', 'caret-color', 'accent-color',
  'text-decoration', 'text-decoration-color', 'text-emphasis-color', 'column-rule', 'column-rule-color', 'fill', 'stroke',
  'stop-color', 'flood-color', 'lighting-color', 'scrollbar-color', '-webkit-text-fill-color', '-webkit-text-stroke-color'];
const camel = name => name.replace(/^-/, '').replace(/-(\w)/g, (_, letter) => letter.toUpperCase());
const COLOR_DECLARATION = new RegExp(
  `(?<![\\w$-])['"]?(${[...new Set([...COLOR_PROPERTIES, ...COLOR_PROPERTIES.map(camel)])].join('|')})['"]?\\s*:\\s*([^;{}"\\n]+)`, 'g');
// SVG 展示属性直接写颜色名（fill="white"）同样算写死。
const COLOR_ATTRIBUTE = /(?<![\w:.@-])(fill|stroke|color|stop-color|flood-color|lighting-color)="([^"]*)"/g;

const FONT_SIZE_OK = /^(var\(--fs-[\w-]+\)|inherit)$/;
const FONT_WEIGHT_OK = /^(var\(--fw-[\w-]+\)|400|500|600|700|normal|bold|inherit)$/;
const FONT_SHORTHAND_PX = /(?<![\w.-])\d+(?:\.\d+)?px\b/;
// 局部叠放（同一组件里谁压谁）用 -1 到 3 的小整数；跨组件的层级一律用 --z-* 刻度。
const Z_INDEX_OK = /^(var\(--z-[\w-]+\)|-1|0|1|2|3|auto)$/;
const STYLE_COLOR_OK = /^(var\(--[\w-]+\)|transparent|currentcolor|inherit|initial|unset|none)$/i;
const COUNTED_COLOR = new RegExp(`${HEX.source}|${COLOR_FUNCTION.source}|${NAMED_COLOR.source}`, 'i');
// 模板 :style 对象里的字面量：只看纯字符串（不含插值）与数字；变量与表达式是运行期的值，不归这里管。
const STYLE_BINDING = /(?<![\w-])(?:v-bind:|:)style="([^"]*)"/g;
const STYLE_LITERAL = /(?<![\w$-])['"]?(fontSize|font-size|zIndex|z-index|color|background|backgroundColor|background-color)['"]?\s*:\s*('[^'\\]*'|`[^`$\\]*`|-?\d+(?:\.\d+)?)(?=\s*[,}]|\s*$)/g;

// 用词表（详细设计 §4）里能无歧义查出的旧词。「删除」「完成」「预览」「移入回收站」要看语境，靠各组人工改。
const OLD_WORDS = ['选择本页', '全选已加载', '全选已列出的', '清除选择', '打开所在目录', '打开目录', '在文件夹里找',
  '重新读取', '重新加载', '重新载入', '重新统计', '彻底删除', '只删记录', '移除记录', '保留这份', '批量删除', '删除所选', '✕'];
// 三个英文句点当省略号；JS 展开语法（...items、...[、...{、...(）不算。
const ASCII_ELLIPSIS = /\.\.\.(?![\w$[({])/g;

// 顶栏导航列出的页面 key → 页面组件（同一个常挂载实例的两个 key 指向同一文件）。新增导航页必须在这里登记。
const PAGE_FILES = {
  home: 'components/viewer/HomePage.vue',
  videos: 'components/viewer/VideoBrowsePage.vue',
  'library-manage': 'components/VideoListPage.vue',
  people: 'components/viewer/EntityBrowsePage.vue',
  collections: 'components/viewer/EntityBrowsePage.vue',
  // 观看侧照片流（D-P2-08）：与图片管理各自一个实例。
  photos: 'components/viewer/PhotoBrowsePage.vue',
  'photos-manage': 'components/PhotoLibraryPage.vue',
  // 片单（D-P2-04）：想看、榜单、观影记录、观看笔记四页嵌在它的标签里，不再是顶层页面。
  lists: 'components/viewer/ListsPage.vue',
  'entities-manage': 'components/EntityLibraryPage.vue',
  // 清理中心（D-P3-02）：视频与图片两个标签，原来的清理弹窗与图片清理页嵌在里面，不再是顶层页面。
  cleanup: 'components/manage/CleanupCenterPage.vue',
  recap: 'components/RecapPage.vue',
  scenes: 'components/ScenesPage.vue',
  'manage-home': 'components/manage/ManageHomePage.vue',
  // 待处理（D-P3-01）：原先是弹窗动作，三期起是管理侧页面。
  'pending-work': 'components/manage/PendingWorkPage.vue',
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
  for (const match of source.slice(start, end).matchAll(/\{\s*key:\s*'([\w-]+)',\s*label:\s*'[^']*'(?:,\s*(command|keywords|requiresBrowserBridge)\b[^}]*)?\s*\}/g)) {
    if (match[2] !== 'command') keys.push(match[1]);
  }
  return keys;
}

const kebab = name => name.replace(/([a-z0-9])([A-Z])/g, '$1-$2').toLowerCase();
const pascal = name => name.replace(/(^|-)(\w)/g, (_, __, letter) => letter.toUpperCase());

// BaseModal 在本文件里的全部注册名：默认导入名与 components 里的改名注册（含 'base-modal' 这种键）。
function baseModalNames(code) {
  const names = new Map([['BaseModal', null]]);
  for (const match of code.matchAll(/import\s+(\w+)\s+from\s+['"][^'"]*\/BaseModal(?:\.vue)?['"]/g)) {
    if (!names.has(match[1])) names.set(match[1], match.index);
  }
  for (const match of code.matchAll(/(?<![\w$.])['"]?([\w-]+)['"]?\s*:\s*BaseModal\b/g)) {
    const name = pascal(match[1]);
    if (!names.has(name)) names.set(name, match.index);
  }
  return names;
}

// 一个 .vue / .css 文件的规则 1–5、7（规则 6 看导航清单，在 scan 里）。add(rule, detail) 记一处违规。
function scanSource(file, source, add) {
  const code = stripComments(source);
  const isVue = file.endsWith('.vue');
  const template = isVue ? stripComments(templateOf(source)) : '';
  if (file !== 'styles/tokens.css') {
    for (const match of code.matchAll(HEX)) {
      // <template #slot> 的插槽简写不是颜色。
      if (/<template\b[^>]*$/.test(code.slice(Math.max(0, match.index - 200), match.index))) continue;
      add('colors', `${lineOf(code, match.index)}: ${match[0]}`);
    }
    for (const match of code.matchAll(COLOR_FUNCTION)) add('colors', `${lineOf(code, match.index)}: ${match[0]}…)`);
    const styled = isVue ? blankScripts(code) : code;
    for (const match of styled.matchAll(COLOR_DECLARATION)) {
      const value = match[2].replace(/url\([^)]*\)/g, '');
      for (const name of value.matchAll(NAMED_COLOR)) add('colors', `${lineOf(styled, match.index)}: ${match[1]}: ${name[0]}`);
    }
    for (const match of template.matchAll(COLOR_ATTRIBUTE)) {
      for (const name of match[2].matchAll(NAMED_COLOR)) add('colors', `${lineOf(template, match.index)}: ${match[1]}="${name[0]}"`);
    }
    for (const { value, line } of declarations(code, 'font-size')) if (!FONT_SIZE_OK.test(value)) add('font-scale', `${line}: font-size ${value}`);
    for (const { value, line } of declarations(code, 'font-weight')) if (!FONT_WEIGHT_OK.test(value)) add('font-scale', `${line}: font-weight ${value}`);
    for (const { value, line } of declarations(code, 'font')) if (FONT_SHORTHAND_PX.test(value)) add('font-scale', `${line}: font ${value}`);
    for (const { value, line } of declarations(code, 'z-index')) if (!Z_INDEX_OK.test(value)) add('z-index', `${line}: z-index ${value}`);
    for (const binding of template.matchAll(STYLE_BINDING)) {
      const line = lineOf(template, binding.index);
      for (const literal of binding[1].matchAll(STYLE_LITERAL)) {
        const key = camel(literal[1]);
        const value = literal[2].replace(/^['`]|['`]$/g, '').trim();
        if (key === 'fontSize') {
          if (!FONT_SIZE_OK.test(value)) add('font-scale', `${line}: :style fontSize ${literal[2]}`);
        } else if (key === 'zIndex') {
          if (!Z_INDEX_OK.test(value)) add('z-index', `${line}: :style zIndex ${literal[2]}`);
        } else if (!STYLE_COLOR_OK.test(value) && !COUNTED_COLOR.test(value)) {
          // hex、颜色函数与颜色名上面已经按处数记过（COUNTED_COLOR），这里只补其他字面量，不重复计数。
          add('colors', `${line}: :style ${key} ${literal[2]}`);
        }
      }
    }
  }
  const local = new Set([...code.matchAll(/(--[\w-]+)\s*:/g), ...code.matchAll(/['"](--[\w-]+)['"]/g)].map(match => match[1]));
  for (const match of code.matchAll(/var\(\s*(--[\w-]+)/g)) {
    const name = match[1];
    if (!TOKEN_NAMES.has(name) && !local.has(name) && !RUNTIME_VARS.has(name)) add('vars', `${lineOf(code, match.index)}: ${name}`);
  }
  if (!isVue) return;
  if (file !== 'components/ui/DialogShell.vue' && file !== 'components/ui/BaseModal.vue') {
    for (const [name, index] of baseModalNames(code)) {
      if (index !== null) add('dialogs', `${lineOf(code, index)}: BaseModal 改名注册为 ${name}`);
      for (const match of template.matchAll(new RegExp(`<(?:${name}|${kebab(name)})(?![\\w-])`, 'g'))) {
        add('dialogs', `${lineOf(template, match.index)}: ${match[0]}>`);
      }
    }
    for (const match of template.matchAll(/(?<![\w-])(?:v-bind:|:)?class="[^"]*\bmodal-overlay\b/g)) {
      add('dialogs', `${lineOf(template, match.index)}: class 绑定里的 modal-overlay`);
    }
  }
  for (const word of OLD_WORDS) {
    let index = template.indexOf(word);
    while (index !== -1) {
      add('wording', `${lineOf(template, index)}: ${word}`);
      index = template.indexOf(word, index + word.length);
    }
  }
  for (const match of template.matchAll(ASCII_ELLIPSIS)) add('wording', `${lineOf(template, match.index)}: ...`);
}

// 规则 8 的例外：文件 → 为什么这里的 AppTabs 不需要面板（理由必填）。接好面板后守卫会提示把这一项删掉。
const TAB_PANEL_EXCEPTIONS = {};

// 模板里的开始标签：从 <名字 起按引号配对找到收尾的 >，属性值里的 =>、> 不会把标签截断。
function startTags(template) {
  const tags = [];
  const opener = /<([A-Za-z][\w-]*)/g;
  let match;
  while ((match = opener.exec(template))) {
    let quote = '';
    let end = opener.lastIndex;
    for (; end < template.length; end += 1) {
      const char = template[end];
      if (quote) {
        if (char === quote) quote = '';
      } else if (char === '"' || char === "'") {
        quote = char;
      } else if (char === '>') {
        break;
      }
    }
    tags.push({ name: match[1], text: template.slice(match.index, end + 1), index: match.index });
    opener.lastIndex = end + 1;
  }
  return tags;
}

// 标签上有没有这个属性（静态或 : / v-bind: 绑定）；name 可以是正则片段（id-prefix|idPrefix）。
const hasAttr = (tag, name) => new RegExp(`\\s(?::|v-bind:)?(?:${name})=`).test(tag);
const staticAttr = (tag, name) => (tag.match(new RegExp(`\\s(?:${name})="([^"]*)"`)) || [])[1];

// v-bind="名字" 整组绑定的面板属性（TrashCenterDialog 的 tabPanelAttrs）：取脚本里这个计算属性 / 方法的函数体。
function boundBody(script, name) {
  const start = script.search(new RegExp(`(?<![\\w$.])${name}\\s*\\([^)]*\\)\\s*\\{`));
  if (start === -1) return '';
  let depth = 0;
  for (let index = script.indexOf('{', start); index < script.length; index += 1) {
    if (script[index] === '{') depth += 1;
    else if (script[index] === '}' && (depth -= 1) === 0) return script.slice(start, index + 1);
  }
  return '';
}

// 一个 .vue 文件里 AppTabs 与面板的接线问题（规则 8），没用 AppTabs 的文件返回空。
function tabPanelProblems(source) {
  const template = stripComments(templateOf(source));
  const script = (stripComments(source).match(/<script\b[^>]*>([\s\S]*?)<\/script>/) || [, ''])[1];
  const tags = startTags(template);
  const tabs = tags.filter(tag => tag.name === 'AppTabs' || tag.name === 'app-tabs');
  if (!tabs.length) return [];
  const problems = [];
  const prefixes = [];
  for (const tag of tabs) {
    if (!hasAttr(tag.text, 'id-prefix|idPrefix')) problems.push(`${lineOf(template, tag.index)}: AppTabs 没传 id-prefix（页签没有 id 与 aria-controls，面板对不上）`);
    const prefix = staticAttr(tag.text, 'id-prefix|idPrefix');
    if (prefix) prefixes.push(prefix);
  }
  const panels = [];
  for (const tag of tags) {
    const line = lineOf(template, tag.index);
    if (/\srole="tabpanel"/.test(tag.text)) {
      panels.push({ line, text: tag.text, hasID: hasAttr(tag.text, 'id'), hasLabel: hasAttr(tag.text, 'aria-labelledby') });
    }
    const bound = tag.text.match(/\sv-bind="([\w$]+)(?:\([^"]*\))?"/);
    const body = bound ? boundBody(script, bound[1]) : '';
    if (/role:\s*['"]tabpanel['"]/.test(body)) {
      panels.push({ line, text: body, hasID: /(?<![\w-])['"]?id['"]?\s*:/.test(body), hasLabel: /['"]aria-labelledby['"]\s*:/.test(body) });
    }
  }
  if (!panels.length) problems.push('模板里没有 role="tabpanel" 的面板（AppTabs 的宿主要给面板写 role、id 与 aria-labelledby）');
  for (const panel of panels) {
    if (!panel.hasID) problems.push(`${panel.line}: 面板没有 id`);
    if (!panel.hasLabel) problems.push(`${panel.line}: 面板没有 aria-labelledby`);
  }
  for (const prefix of new Set(prefixes)) {
    if (!panels.some(panel => panel.text.includes(`${prefix}-panel-`) && panel.text.includes(`${prefix}-tab-`))) {
      problems.push(`id-prefix「${prefix}」没有对应的面板（面板 id 写 ${prefix}-panel-…，aria-labelledby 写 ${prefix}-tab-…）`);
    }
  }
  return problems;
}

function scan() {
  const found = new Map(RULES.map(rule => [rule, new Map()]));
  const record = (rule, file, detail) => {
    const files = found.get(rule);
    if (!files.has(file)) files.set(file, []);
    files.get(file).push(detail);
  };
  for (const file of STYLE_FILES) scanSource(file, read(file), (rule, detail) => record(rule, file, detail));
  for (const file of new Set(Object.values(PAGE_FILES))) {
    if (!/<(PageHeader|HeroHeader)\b/.test(stripComments(templateOf(read(file))))) record('page-header', file, '模板里没有 PageHeader / HeroHeader');
  }
  return found;
}

// 规则自检：树里暂时没有、但最容易绕过的写法（M-10 列出的那些）逐条喂给 scanSource，确认都能查出来，
// 放行的写法（-1 到 3 的叠放、currentColor、var()、脚本里的普通对象）不误报。改规则时这里跟着改。
function selfCheck() {
  const fixture = `<template>
  <div :class="['modal-overlay', extra]" :style="{ fontSize: '13px', zIndex: 9, color: 'white', background: 'url(a.png)', backgroundColor: 'var(--panel-bg)' }">
    <base-modal />
    <Dlg />
    <svg><path fill="black" stroke="currentColor" /></svg>
    <span style="z-index: 2" :style="{ zIndex: 3, fontSize: 'var(--fs-body)' }">ok</span>
  </div>
</template>
<script>
import Dlg from './ui/BaseModal.vue';
export default { components: { Dlg }, data: () => ({ color: 'red' }) };
</script>
<style scoped>
.a { color: white; border: 1px solid var(--panel-bg, grey); font: 600 13px/1.4 sans-serif; background: hsl(0 0% 0%); z-index: -1; }
.b { z-index: 4; background: oklch(0.5 0.1 200); box-shadow: 0 0 0 1px transparent; color: currentColor; font: inherit; }
</style>
`;
  const counts = {};
  scanSource('components/SelfCheck.vue', fixture, rule => { counts[rule] = (counts[rule] || 0) + 1; });
  assert.deepEqual(counts, { colors: 7, 'font-scale': 2, 'z-index': 2, dialogs: 4 }, '守卫规则自检没过：某条检查查不出它该查的写法，或误报了放行的写法');
}

// 规则 8 自检：没接面板、面板缺属性、前缀对不上都查得出；静态、动态前缀与 v-bind 整组绑定的写法放行。
function tabPanelSelfCheck() {
  const problems = source => tabPanelProblems(source).length;
  assert.equal(problems('<template><AppTabs v-model="tab" :tabs="tabs" /><div v-if="tab === \'a\'">A</div></template>'), 2,
    '规则 8 自检没过：没传 id-prefix、没有面板的 AppTabs 要报两处');
  assert.equal(problems('<template><app-tabs :tabs="tabs" id-prefix="demo" /><div id="other-panel-a" role="tabpanel">A</div></template>'), 2,
    '规则 8 自检没过：面板缺 aria-labelledby、id 与前缀对不上要报两处');
  assert.equal(problems(`<template>
  <AppTabs v-model="tab" :tabs="tabs" id-prefix="demo" @update:model-value="value => (tab = value)" />
  <section :id="\`demo-panel-\${tab}\`" role="tabpanel" :aria-labelledby="\`demo-tab-\${tab}\`">A</section>
  <AppTabs :tabs="tabs" :id-prefix="prefix" /><div :id="\`\${prefix}-panel-a\`" role="tabpanel" :aria-labelledby="\`\${prefix}-tab-a\`" />
</template>`), 0, '规则 8 自检没过：接好的静态 / 动态前缀被误报（属性值里的 => 不能截断标签）');
  assert.equal(problems(`<template><AppTabs :tabs="tabs" :model-value="tab" id-prefix="bin" /><section v-bind="panelAttrs">A</section></template>
<script>
export default { computed: { panelAttrs() { return { id: \`bin-panel-\${this.tab}\`, role: 'tabpanel', 'aria-labelledby': \`bin-tab-\${this.tab}\` }; } } };
</script>`), 0, '规则 8 自检没过：v-bind 整组绑定的面板属性被误报');
}

// 棘轮自检：四种失败各一条（没登记、比清单多、比清单少、已清零），处数相等的不报。buttons 用的是同一个函数。
function ratchetSelfCheck() {
  const recorded = new Map([
    ['same.vue', { count: 2, owner: 'B1.json' }],
    ['more.vue', { count: 1, owner: 'B1.json' }],
    ['fewer.vue', { count: 3, owner: 'B2.json' }],
    ['fixed.vue', { count: 1, owner: 'B3.json' }]
  ]);
  const actual = new Map([['same.vue', ['1: a', '2: b']], ['more.vue', ['1: a', '2: b']], ['fewer.vue', ['1: a']], ['new.vue', ['1: a']]]);
  assert.deepEqual(ratchetFailures('colors', actual, recorded).map(failure => failure.split('\n')[0]), [
    '[colors] more.vue 现有 2 处不合规，比 design-system-pending/B1.json 记的 1 处多，请改掉新增的（清单只减不增）：',
    '[colors] fewer.vue 只剩 1 处了，请把 design-system-pending/B2.json 里 colors 的这一项从 3 改成 1',
    '[colors] new.vue 有 1 处不合规（新代码不能进待迁移清单，请直接改）：',
    '[colors] fixed.vue 已合规，请从 design-system-pending/B3.json 的 colors 清单删除'
  ], '棘轮自检没过：ratchetFailures 的比对规则被改坏了');
}

selfCheck();
ratchetSelfCheck();
tabPanelSelfCheck();
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

// 影院表面（D-UD-02）不随系统主题变：深色主题改过的每个变量，.surface-cinema 都要跟着重映射，
// 否则浅色主题下观看侧页面会漏出浅色值（审阅、置信度、回顾这些变量就曾经漏过）。
function tokenBlock(selector) {
  const start = TOKENS.indexOf(selector);
  assert.ok(start !== -1, `tokens.css 里找不到 ${selector}`);
  const open = TOKENS.indexOf('{', start);
  return TOKENS.slice(open + 1, TOKENS.indexOf('\n}', open));
}
const darkOnly = [...tokenBlock("[data-theme='dark'] {").matchAll(/(--[\w-]+)\s*:/g)].map(match => match[1])
  .filter(name => !new RegExp(`(^|[\\s;{])${name}\\s*:`).test(tokenBlock('.surface-cinema {')));
assert.deepEqual(darkOnly, [], `.surface-cinema 漏了深色主题改过的变量：${darkOnly.join(' ')}`);

const navKeys = navPageKeys();
assert.deepEqual(
  [...new Set(navKeys)].sort(),
  Object.keys(PAGE_FILES).sort(),
  '顶栏导航的页面与守卫 6 的 PAGE_FILES 对不上：新增或改名的页面要在 PAGE_FILES 里登记它的组件文件'
);

const { lists, problems } = loadPendingLists(SRC);
const failures = [...problems];
// 规则 8 不走待迁移清单：任何问题直接失败；例外要写理由，接好面板或文件没了就要删掉例外。
for (const file of ALL_FILES.filter(file => file.endsWith('.vue') && !file.startsWith('short-feed/'))) {
  const tabProblems = tabPanelProblems(read(file));
  if (file in TAB_PANEL_EXCEPTIONS) {
    if (!String(TAB_PANEL_EXCEPTIONS[file] || '').trim()) failures.push(`[tab-panels] ${file} 的例外没写理由`);
    if (!tabProblems.length) failures.push(`[tab-panels] ${file} 已接好面板，请从 TAB_PANEL_EXCEPTIONS 删掉`);
    continue;
  }
  for (const problem of tabProblems) failures.push(`[tab-panels] ${file} ${problem}`);
}
for (const file of Object.keys(TAB_PANEL_EXCEPTIONS)) {
  if (!ALL_FILES.includes(file)) failures.push(`[tab-panels] TAB_PANEL_EXCEPTIONS 里的 ${file} 不存在，请删掉`);
}
const summary = [];
for (const rule of RULES) {
  failures.push(...ratchetFailures(rule, found.get(rule), lists.get(rule)));
  summary.push(`${rule} ${lists.get(rule).size} files/${pendingTotal(lists.get(rule))}`);
}

assert.deepEqual(failures, [], `统一设计守卫未通过（${failures.length} 项）：\n${failures.join('\n')}`);
console.log(`design-system guard passed (pending: ${summary.join(', ')})`);
