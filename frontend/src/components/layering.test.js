import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { h, vShow, withDirectives } from 'vue';

const api = vi.hoisted(() => ({
  GetSettings: vi.fn(), LogFrontend: vi.fn(), ConfirmQuit: vi.fn(), RevealImage: vi.fn(), SearchLibraryVideoPage: vi.fn()
}));
vi.mock('../../wailsjs/go/main/App', async (importOriginal) => ({ ...(await importOriginal()), ...api }));

import AppFeedback from './AppFeedback.vue';
import CommandPalette from './CommandPalette.vue';
import ImageSourceDialog from './ImageSourceDialog.vue';
import PersonMediaDeleteDialog from './PersonMediaDeleteDialog.vue';
import QuitConfirmDialog from './QuitConfirmDialog.vue';
import TrashUnsupportedDialog from './TrashUnsupportedDialog.vue';
import DialogShell from './ui/DialogShell.vue';
import { confirmAction, feedbackState, notifyUndo, resetFeedback } from '../utils/feedback.js';

// 层级（tokens.css「层级」）：对话框、确认框与全屏查看器同在 --z-modal，谁在上面看 DOM 顺序。
// 这里按应用里的真实位置搭一个外壳：#app 里先是页面（main-view），后是 App 级的浮层（AppFeedback、命令面板、
// 退出确认）；teleport 的对话框挂到 body 末尾。验证「后打开的排在后面」在今天存在的几种叠放里都成立。
let shell;
let mainView;
let appLayer;
const wrappers = [];
const keep = wrapper => {
  wrappers.push(wrapper);
  return wrapper;
};

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  api.GetSettings.mockResolvedValue({ confirm_before_delete: true, delete_original_file: false });
  shell = document.createElement('div');
  shell.id = 'layering-app';
  mainView = document.createElement('div');
  appLayer = document.createElement('div');
  shell.append(mainView, appLayer);
  document.body.append(shell);
});

afterEach(() => {
  resetFeedback();
  while (wrappers.length) wrappers.pop().unmount();
  shell.remove();
  delete window.runtime;
});

// 在 host 里挂一个组件（attachTo 会替换掉挂载点，所以每次给一个新的子节点）。
function mountIn(host, component, options = {}) {
  const slot = document.createElement('div');
  host.append(slot);
  return keep(mount(component, { ...options, attachTo: slot }));
}

const overlayOf = selector => document.querySelector(selector)?.closest('.modal-overlay');
// b 在 DOM 里排在 a 后面：同一层级时 b 画在 a 上面。
const follows = (a, b) => Boolean(a && b && (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING));

describe('同在 --z-modal 的叠放按打开先后（DOM 顺序）', () => {
  it('确认框叠在 teleport 到 body 的对话框（原图）上：打开时才 teleport，排在它后面', async () => {
    mountIn(appLayer, AppFeedback);
    mountIn(mainView, ImageSourceDialog, { props: { image: { id: 1, name: 'a.jpg' }, allowUnlink: true } });
    const answer = confirmAction({ title: '解除关联', message: '解除最后一条关联？' });
    await flushPromises();

    const image = overlayOf('.image-source-dialog');
    const confirm = overlayOf('[data-test="app-confirm"]');
    expect(image).toBeTruthy();
    expect(confirm).toBeTruthy();
    expect(follows(image, confirm)).toBe(true);
    resetFeedback();
    void answer;
  });

  it('确认框叠在页面里的表单对话框上', async () => {
    mountIn(appLayer, AppFeedback);
    mountIn(mainView, DialogShell, { props: { title: '批量编辑' }, slots: { default: '<input class="text-input" />' } });
    confirmAction({ title: '放弃修改', message: '放弃未保存的修改？' });
    await flushPromises();
    expect(follows(overlayOf('.ui-dialog'), overlayOf('[data-test="app-confirm"]'))).toBe(true);
  });

  it('「不支持废纸篓」二选一叠在发起删除的对话框或查看器上，即使宿主在页面里排在它前面', async () => {
    // 宿主（页面里的 TrashUndoBanner）先挂载，清理审阅 / 查看器后打开、排在它后面。
    const unsupported = mountIn(mainView, TrashUnsupportedDialog, { props: { visible: false, items: [{ id: 1, name: 'a.mp4' }] } });
    mountIn(mainView, DialogShell, { props: { title: '清理中心' }, attrs: { 'data-test': 'cleanup-review' } });
    await unsupported.setProps({ visible: true });
    await flushPromises();
    const names = document.querySelector('[data-test="trash-unsupported-names"]');
    expect(names?.textContent).toContain('a.mp4');
    expect(follows(overlayOf('[data-test="cleanup-review"]'), names.closest('.modal-overlay'))).toBe(true);
  });

  it('人物媒体删除确认叠在原图上（两者都在打开时 teleport，后开的在后）', async () => {
    mountIn(mainView, ImageSourceDialog, { props: { image: { id: 11, name: 'photo.jpg' }, allowDelete: true } });
    mountIn(mainView, PersonMediaDeleteDialog, { props: { target: { kind: 'image', personID: 7, media: { id: 11, name: 'photo.jpg', size: 1024 } } } });
    await flushPromises();
    const layer = document.querySelector('.person-media-delete-layer');
    expect(document.querySelector('[data-test="person-media-delete-dialog"]')).toBeTruthy();
    expect(follows(overlayOf('.image-source-dialog'), layer)).toBe(true);
  });

  it('退出确认叠在 teleport 到 body 的对话框上：宿主在 App 里，打开时才 teleport', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (event, handler) => { handlers[event] = handler; return () => {}; } };
    mountIn(appLayer, QuitConfirmDialog);
    mountIn(mainView, ImageSourceDialog, { props: { image: { id: 2, name: 'b.jpg' } } });
    handlers['quit-confirm-required']({ tasks: [{ key: 'subtitle', running: 1, queued: 0, names: [] }] });
    await flushPromises();
    expect(follows(overlayOf('.image-source-dialog'), overlayOf('[data-test="quit-confirm"]'))).toBe(true);
  });

  // M-5：删除后的「撤销」提示和随后弹出的确认框同时在。提示不在确认框的遮罩里（层级 --z-toast 在它之上），
  // 点「撤销」执行撤销，确认框原样留着等用户回答。
  it('确认框开着时点提示里的「撤销」：撤销执行，确认框不被关掉', async () => {
    const feedback = mountIn(appLayer, AppFeedback);
    const undo = vi.fn();
    notifyUndo('已移到废纸篓：3 个视频', undo);
    const answer = confirmAction({ title: '重新分析清理候选', message: '删除改变了重复组，是否重新分析？' });
    await flushPromises();
    const action = feedback.get('[data-test="app-toast-action"]');
    expect(action.element.closest('.modal-overlay')).toBeNull();
    await action.trigger('click');
    await flushPromises();
    expect(undo).toHaveBeenCalledTimes(1);
    expect(feedbackState.confirm).not.toBeNull();
    expect(document.querySelector('[data-test="app-confirm"]')).toBeTruthy();
    resetFeedback();
    void answer;
  });

  it('命令面板开着时弹出的确认框排在面板后面', async () => {
    mountIn(appLayer, CommandPalette, { props: { open: true, openVideo: vi.fn() } });
    mountIn(appLayer, AppFeedback);
    confirmAction({ title: '离开设置页', message: '放弃未保存的设置修改？' });
    await flushPromises();
    expect(follows(overlayOf('[data-test="command-palette"]'), overlayOf('[data-test="app-confirm"]'))).toBe(true);
  });
});

// 刻度本身与各层用到哪一档：样式里写死的，读源码核对（jsdom 不算层叠）。
describe('层级刻度（D-UD-05）', () => {
  const SRC = join(process.cwd(), 'src');
  const read = file => readFileSync(join(SRC, file), 'utf8');
  // 取「selector {」后面那一块里的 z-index 值。
  function zIndexOf(file, selector) {
    const source = read(file);
    const start = source.indexOf(`${selector} {`);
    expect(start, `${file} 里找不到 ${selector}`).toBeGreaterThan(-1);
    const block = source.slice(start, source.indexOf('}', start));
    return (block.match(/z-index:\s*([^;]+);/) || [])[1];
  }

  // 菜单与浮层（teleport 到 body）可能从抽屉或对话框里点开，所以排在对话框之上、全局提示之下。
  it('从低到高：吸顶 < 抽屉 < 对话框与查看器 < 菜单与浮层 < 全局提示 < 关键遮罩', () => {
    const tokens = read('styles/tokens.css');
    const value = name => Number((tokens.match(new RegExp(`${name}:\\s*(\\d+);`)) || [])[1]);
    const scale = ['--z-sticky', '--z-drawer', '--z-modal', '--z-menu', '--z-toast', '--z-critical'].map(value);
    expect(scale.every(Number.isFinite)).toBe(true);
    expect([...scale].sort((a, b) => a - b)).toEqual(scale);
    expect(new Set(scale).size).toBe(scale.length);
  });

  it('全局提示与删除进度压在对话框与确认框之上；确认框不另抬层级；只有待重启遮罩在最上', () => {
    expect(zIndexOf('components/AppFeedback.vue', '.app-toasts')).toBe('var(--z-toast)');
    expect(read('components/AppFeedback.vue').match(/z-index:/g)).toHaveLength(1);
    expect(zIndexOf('components/video-list/TrashUndoBanner.vue', '.delete-progress-float')).toBe('var(--z-toast)');
    expect(zIndexOf('App.vue', '.relaunch-overlay')).toBe('var(--z-critical)');
    expect(zIndexOf('App.vue', '.header')).toBe('var(--z-sticky)');
  });

  it('对话框遮罩与各个全屏层同在 --z-modal', () => {
    expect(zIndexOf('styles/components.css', '.modal-overlay')).toBe('var(--z-modal)');
    for (const [file, selector] of [
      ['components/PersonMediaDeleteDialog.vue', '.person-media-delete-layer'],
      ['components/FaceClusterDetailDialog.vue', '.face-source-video'],
      ['components/PhotoLibraryPage.vue', '.photo-viewer'],
      ['components/PhotoCleanupPage.vue', '.cleanup-viewer'],
      ['components/VersionCompareDialog.vue', '.version-compare'],
      ['components/SubtitleWorkbench.vue', '.subtitle-workbench-overlay']
    ]) {
      expect(zIndexOf(file, selector), `${file} ${selector}`).toBe('var(--z-modal)');
    }
  });
});

// 对话框开着时提示让开对话框的动作（2026-10-14 UI 遗留 F2）：几条提示叠起来（全局提示在 --z-toast，压在对话框之上）
// 会盖住居中对话框右下角的主操作，高的对话框连一条都会盖住。对话框开着时提示挪到左上角、只占窗口上半截：
// DialogShell 的动作是底部一排和标题行右端的 ×，居中时底部一排总在中线以下、× 总在右半边。
// 位置写在样式里，jsdom 不算布局：读源码核对规则本身与它依赖的前提，选择器是否跟着对话框开关在 jsdom 里验。
describe('对话框开着时全局提示让开对话框的动作', () => {
  const SRC = join(process.cwd(), 'src');
  const read = file => readFileSync(join(SRC, file), 'utf8');
  // 2026-10-14 G1 起条件带上「不在 v-show 隐藏的页面里」（见下一组），开着的对话框照旧命中。
  const MODAL_OPEN = 'body:has(.modal-overlay:where(:not([style*="display: none"] *)))';
  // 取「selector {」那一块的声明，{ 属性: 值 }。
  function declarations(file, selector) {
    const source = read(file);
    const start = source.indexOf(`${selector} {`);
    expect(start, `${file} 里找不到 ${selector}`).toBeGreaterThan(-1);
    const block = source.slice(source.indexOf('{', start) + 1, source.indexOf('}', start));
    return Object.fromEntries(block.split(';').map(item => item.trim()).filter(Boolean).map(item => {
      const colon = item.indexOf(':');
      return [item.slice(0, colon).trim(), item.slice(colon + 1).trim()];
    }));
  }
  const tokenPx = name => Number((read('styles/tokens.css').match(new RegExp(`${name}:\\s*(\\d+)px;`)) || [])[1]);

  it('没有对话框时提示在右下角', () => {
    expect(declarations('components/AppFeedback.vue', '.app-toasts')).toMatchObject({ right: 'var(--space-4)', bottom: 'var(--space-4)' });
  });

  it('有对话框开着时提示贴左上角，整条最多到窗口中线以上', () => {
    const rule = declarations('components/AppFeedback.vue', `${MODAL_OPEN} .app-toasts`);
    expect(rule).toMatchObject({ top: 'var(--space-4)', left: 'var(--space-4)', right: 'auto', bottom: 'auto' });
    // 下沿 = top + max-height = 16 + (50vh - n)，n 不小于 top 才不越过中线。
    const [, viewport, offset] = rule['max-height'].match(/^calc\((\d+)vh - (\d+)px\)$/) || [];
    expect(Number(viewport)).toBe(50);
    expect(Number(offset)).toBeGreaterThanOrEqual(tokenPx('--space-4'));
    // 宽度不超过窗口一半减去左边距：× 在居中对话框的右半边，提示够不着。
    expect(declarations('components/AppFeedback.vue', '.app-toasts')['max-width']).toBe('min(420px, calc(100vw - 32px))');
    expect(420 + tokenPx('--space-4')).toBeLessThan(1024 / 2);
  });

  it('规则依赖的前提：遮罩只由 BaseModal 画并让对话框居中；DialogShell 的动作在底部一排与标题行右端', () => {
    expect(read('components/ui/BaseModal.vue')).toContain(`:class="['modal-overlay', overlayClass]"`);
    expect(declarations('styles/components.css', '.modal-overlay')).toMatchObject({ 'align-items': 'center', 'justify-content': 'center' });
    const shell = read('components/ui/DialogShell.vue');
    expect(shell).toMatch(/<\/header>\s*<div ref="body" class="ui-dialog__body"><slot \/><\/div>\s*<footer v-if="\$slots.footer" class="ui-dialog__footer">/);
    expect(shell).toMatch(/<h2 [^>]*class="ui-dialog__title"[\s\S]*class="btn-icon btn--sm ui-dialog__close"[\s\S]*<\/header>/);
  });

  it('选择器只在有对话框开着时命中', async () => {
    const modalOpen = () => document.body.matches(MODAL_OPEN);
    expect(modalOpen()).toBe(false);
    mountIn(appLayer, AppFeedback);
    notifyUndo('已移到废纸篓：3 个视频', vi.fn());
    await flushPromises();
    expect(modalOpen()).toBe(false);
    const answer = confirmAction({ title: '永久删除 4 个文件', message: '无法恢复', confirmText: '永久删除', level: 'irreversible' });
    await flushPromises();
    expect(modalOpen()).toBe(true);
    resetFeedback();
    await flushPromises();
    expect(modalOpen()).toBe(false);
    void answer;
  });
});

// 全窗口层与抽屉开着时提示让开它们的控件（2026-10-14 G1）：对话框那条只管 BaseModal 的遮罩，图片管理查看器、清理查看器、
// 版本对比、字幕工作台、详情抽屉与任务中心都不是 BaseModal，提示照旧在右下角，压住了「从图片库移除」、播放进度条、
// 「保存字幕」与抽屉底部；观看侧看图（DialogShell 全窗口）跟着对话框那条去了左上角，压住文件名。
// 每种层按自己的布局挑一块没有控件的地方（截图台在 1024×768 与 1440×900 下量过）。这里读源码核对：每种层的规则、
// 规则之间的先后与轻重、位置依赖的布局前提（按两种窗口尺寸算），选择器是否只在层开着且没被 v-show 藏起来时命中在 jsdom 里验。
describe('全窗口层与抽屉开着时全局提示让开它们的控件', () => {
  const SRC = join(process.cwd(), 'src');
  const read = file => readFileSync(join(SRC, file), 'utf8');
  const FEEDBACK = 'components/AppFeedback.vue';
  const SIZES = [{ w: 1024, h: 768 }, { w: 1440, h: 900 }];
  // 「层开着且不在 v-show 隐藏的页面里」：:where 不计特异性，各条与对话框那条一样重。
  const VISIBLE = ':where(:not([style*="display: none"] *))';
  const openCondition = root => `body:has(${root}${VISIBLE})`;
  const toastsWhen = root => `${openCondition(root)} .app-toasts`;
  const DIALOG = toastsWhen('.modal-overlay');
  // 写在对话框那条之前、对话框开着时让给它的；写在之后、在对话框那条范围以内的（字幕工作台按编辑布局判断，见下）。
  const YIELDING = ['.preview-drawer', '.task-center-overlay', '.version-compare'];
  const INSIDE_DIALOG_AREA = ['.photo-viewer', '.cleanup-viewer', '.photo-lightbox', '.subtitle-workbench__layout'];

  // 样式里的规则（去掉注释），按出现先后：{ selectors, decls, media }，media 是外层 @media 的条件（没有为空串）。
  function styleRules(file) {
    const text = read(file).replace(/\/\*[\s\S]*?\*\//g, '');
    const css = file.endsWith('.vue') ? text.slice(text.indexOf('<style')).replace(/<\/?style[^>]*>/g, '') : text;
    const rules = [];
    let media = '';
    let start = 0;
    for (let i = 0; i < css.length; i++) {
      if (css[i] === '}') {
        media = '';
        start = i + 1;
      } else if (css[i] === '{') {
        const head = css.slice(start, i).trim();
        start = i + 1;
        if (head.startsWith('@media')) {
          media = head;
          continue;
        }
        const end = css.indexOf('}', i);
        const decls = Object.fromEntries(css.slice(i + 1, end).split(';').map(item => item.trim()).filter(Boolean).map(item => {
          const colon = item.indexOf(':');
          return [item.slice(0, colon).trim(), item.slice(colon + 1).trim()];
        }));
        rules.push({ selectors: head.split(',').map(item => item.trim().replace(/\s+/g, ' ')), decls, media });
        i = end;
        start = end + 1;
      }
    }
    return rules;
  }
  // 某个选择器（不在 @media 里）的全部声明，后写的覆盖先写的。
  function declarationsOf(file, selector) {
    const matched = styleRules(file).filter(rule => !rule.media && rule.selectors.includes(selector));
    expect(matched.length, `${file} 里找不到 ${selector}`).toBeGreaterThan(0);
    return Object.assign({}, ...matched.map(rule => rule.decls));
  }
  // 长度换成像素：var(--…)（tokens.css 里的 px）、vh / vw、px、calc()、min()。
  function px(value, { w, h }) {
    const tokens = read('styles/tokens.css');
    const expr = value
      .replace(/var\((--[\w-]+)\)/g, (_, name) => (tokens.match(new RegExp(`${name}:\\s*(\\d+)px;`)) || [, 'NaN'])[1])
      .replace(/(\d+(?:\.\d+)?)vh/g, (_, n) => String(Number(n) * h / 100))
      .replace(/(\d+(?:\.\d+)?)vw/g, (_, n) => String(Number(n) * w / 100))
      .replace(/(\d+(?:\.\d+)?)px/g, '$1')
      .replace(/calc\(/g, '(')
      .replace(/min\(/g, 'M(');
    expect(expr, value).toMatch(/^[\d\s+\-*/().,M]+$/);
    return Function('M', `return (${expr});`)(Math.min);
  }
  // 提示条在窗口里的范围（按 top/left 或 bottom/right 定位，宽取 max-width、高取 max-height 的上限）。
  function toastArea(selector, size) {
    const base = declarationsOf(FEEDBACK, '.app-toasts');
    const rule = { ...base, ...declarationsOf(FEEDBACK, selector) };
    const width = px(rule['max-width'], size);
    const height = px(rule['max-height'], size);
    const left = rule.left !== 'auto' ? px(rule.left, size) : size.w - px(rule.right, size) - width;
    const top = rule.top !== 'auto' ? px(rule.top, size) : size.h - px(rule.bottom, size) - height;
    return { left, top, right: left + width, bottom: top + height };
  }

  it('每种层一条规则，把 top / right / bottom / left / max-height 写全，层级不变', () => {
    const POSITION = ['top', 'right', 'bottom', 'left', 'max-height'];
    for (const root of [...YIELDING, ...INSIDE_DIALOG_AREA]) {
      const rule = declarationsOf(FEEDBACK, toastsWhen(root));
      expect(Object.keys(rule).filter(key => POSITION.includes(key)).sort(), root).toEqual([...POSITION].sort());
      expect(rule['z-index'], root).toBeUndefined();
    }
    expect(declarationsOf(FEEDBACK, toastsWhen('.preview-drawer'))).toMatchObject({ top: 'auto', right: 'auto', bottom: 'var(--space-4)', left: 'var(--space-4)' });
    expect(declarationsOf(FEEDBACK, toastsWhen('.task-center-overlay'))).toEqual(declarationsOf(FEEDBACK, toastsWhen('.preview-drawer')));
    expect(declarationsOf(FEEDBACK, toastsWhen('.version-compare'))).toMatchObject({ top: 'auto', right: 'var(--space-4)', left: 'auto' });
    for (const root of INSIDE_DIALOG_AREA) {
      expect(declarationsOf(FEEDBACK, toastsWhen(root)), root).toMatchObject({ right: 'auto', bottom: 'auto' });
    }
    // 对话框那条原样（F2）：左上角、窗口上半截。
    expect(declarationsOf(FEEDBACK, DIALOG)).toEqual({ top: 'var(--space-4)', right: 'auto', bottom: 'auto', left: 'var(--space-4)', 'max-height': 'calc(50vh - 24px)' });
  });

  it('先后：让给对话框的写在对话框那条之前，在对话框范围以内的写在之后；各条一样重（只差一个类），谁生效只看先后', () => {
    const rules = styleRules(FEEDBACK);
    const indexOf = selector => rules.findIndex(rule => rule.selectors.includes(selector));
    const dialog = indexOf(DIALOG);
    expect(dialog).toBeGreaterThan(-1);
    for (const root of YIELDING) expect(indexOf(toastsWhen(root)), root).toBeLessThan(dialog);
    for (const root of INSIDE_DIALOG_AREA) expect(indexOf(toastsWhen(root)), root).toBeGreaterThan(dialog);
    // 版本对比全窗口盖住抽屉，写在抽屉之后。
    expect(indexOf(toastsWhen('.version-compare'))).toBeGreaterThan(indexOf(toastsWhen('.preview-drawer')));
    // 提示的位置规则只有这几条（含对话框那条），条件都是「一个类 + 可见」的写法：一样重。
    const placements = rules.flatMap(rule => rule.selectors).filter(selector => selector.endsWith(' .app-toasts'));
    expect(placements.sort()).toEqual([DIALOG, ...[...YIELDING, ...INSIDE_DIALOG_AREA].map(toastsWhen)].sort());
    for (const selector of placements) {
      expect(selector).toMatch(/^body:has\(\.[\w-]+:where\(:not\(\[style\*="display: none"\] \*\)\)\) \.app-toasts$/);
    }
  });

  it('写在对话框那条之后的几条都在它的范围以内（左半边、中线以上）：上面再开对话框也让得开对话框的动作', () => {
    for (const size of SIZES) {
      const dialog = toastArea(DIALOG, size);
      for (const root of INSIDE_DIALOG_AREA) {
        const area = toastArea(toastsWhen(root), size);
        expect(area.left, `${root} ${size.w}`).toBeGreaterThanOrEqual(dialog.left);
        expect(area.right, `${root} ${size.w}`).toBeLessThanOrEqual(size.w / 2);
        expect(area.bottom, `${root} ${size.w}`).toBeLessThanOrEqual(dialog.bottom);
      }
    }
  });

  it('抽屉：提示在左下角，够不着靠右停靠的详情抽屉（520）与任务中心（460）', () => {
    const drawer = declarationsOf('components/PreviewDrawer.vue', '.preview-drawer');
    expect(drawer).toMatchObject({ position: 'fixed', right: '0', width: '520px' });
    expect(declarationsOf('components/TaskCenterDrawer.vue', '.task-center-overlay')).toMatchObject({ 'justify-content': 'flex-end' });
    expect(declarationsOf('components/TaskCenterDrawer.vue', '.task-center')).toMatchObject({ width: '460px' });
    for (const size of SIZES) {
      const area = toastArea(toastsWhen('.preview-drawer'), size);
      expect(area.right + 8, `${size.w}`).toBeLessThanOrEqual(size.w - 520);
      expect(area.bottom).toBe(size.h - 16);
    }
  });

  // 人脸来源的「查看视频片段」把同一个抽屉加宽看画面；以前固定 900，窗口 1024 宽时左边只剩 124，提示压住播放器控制条。
  it('人脸来源的视频片段：加宽的抽屉给左下角的提示留出一列，又不比一般的详情抽屉窄', () => {
    const wide = declarationsOf('components/FaceClusterDetailDialog.vue', '.face-source-video :deep(.preview-drawer)');
    for (const size of SIZES) {
      const area = toastArea(toastsWhen('.preview-drawer'), size);
      const width = Math.min(px(wide.width, size), px(wide['max-width'], size));
      expect(area.right + 8, `${size.w}`).toBeLessThanOrEqual(size.w - width);
      expect(width, `${size.w}`).toBeGreaterThanOrEqual(520);
    }
    // 窗口够宽时照旧 900。
    expect(px(wide.width, { w: 1440, h: 900 })).toBe(900);
  });

  it('版本对比：提示在一行播放条之上、整条在下半截，够不着顶栏、两侧标注与舞台正中的分界线把手', () => {
    const transport = declarationsOf('components/VersionCompareDialog.vue', '.version-compare__transport');
    expect(transport).toMatchObject({ padding: 'var(--space-2) var(--space-4)', 'flex-wrap': 'nowrap', 'border-top': '1px solid var(--hairline)' });
    expect(read('components/VersionCompareDialog.vue')).toMatch(/<footer class="version-compare__transport">\s*<button type="button" class="btn btn--primary btn--sm version-compare__play"/);
    // 一行播放条 = 上下内边距 + 紧凑控件高 + 上边线（45）。
    const transportHeight = (size) => 2 * px('var(--space-2)', size) + px('var(--h-compact)', size) + 1;
    for (const size of SIZES) {
      const area = toastArea(toastsWhen('.version-compare'), size);
      expect(size.h - area.bottom - transportHeight(size), `${size.w}`).toBeGreaterThanOrEqual(8);
      // 把手（30 高）在舞台正中，顶栏与播放条都是一行、高度只差几像素，舞台正中离窗口中线不到 8：上沿再往下留 24。
      expect(area.top - size.h / 2, `${size.w}`).toBeGreaterThanOrEqual(24);
    }
  });

  it('图片管理查看器、清理查看器：提示贴左上角，下沿在「上一张」之上，右边够不着「下一张」与信息栏', () => {
    const photoNav = declarationsOf('components/PhotoLibraryPage.vue', '.photo-viewer__nav');
    expect(photoNav).toMatchObject({ position: 'absolute', top: '50%', height: '40px', transform: 'translateY(-50%)' });
    const cleanup = declarationsOf('components/PhotoCleanupPage.vue', '.cleanup-viewer');
    expect(cleanup).toMatchObject({ 'align-items': 'center', padding: 'var(--space-7) var(--space-5) 72px' });
    expect(declarationsOf('components/PhotoCleanupPage.vue', '.cleanup-viewer__nav')).toMatchObject({ height: '40px' });
    const nextRight = declarationsOf('components/PhotoLibraryPage.vue', '.photo-viewer__nav--next').right;
    for (const size of SIZES) {
      const photo = toastArea(toastsWhen('.photo-viewer'), size);
      expect(photo.bottom + 8, `${size.w}`).toBeLessThanOrEqual(size.h / 2 - 20);
      expect(photo.right + 8, `${size.w}`).toBeLessThanOrEqual(size.w - px(nextRight, size) - 40);
      const viewer = toastArea(toastsWhen('.cleanup-viewer'), size);
      // 清理查看器的按钮在上 48、下 72 的内边距里竖直居中。
      const navTop = (px('var(--space-7)', size) + size.h - 72) / 2 - 20;
      expect(viewer.bottom + 8, `${size.w}`).toBeLessThanOrEqual(navTop);
    }
  });

  it('观看侧看图：提示从标题栏（文件名与 ×）下面开始，下沿在「上一张」之上，够不着信息栏', () => {
    // 标题栏 = 上内边距 + 一行（× 是紧凑按钮、上下各收 2px）+ 下内边距 + 下边线（53）；文件名折成两行时文字到 61。
    const head = declarationsOf('components/ui/DialogShell.vue', '.ui-dialog__head');
    expect(head.padding).toBe('var(--space-4) var(--space-4) var(--space-3) var(--space-5)');
    expect(declarationsOf('components/ui/DialogShell.vue', '.ui-dialog__close').margin).toBe('-2px 0');
    expect(read('components/viewer/PhotoLightbox.vue')).toContain(':deep(.photo-lightbox .ui-dialog__head) { border-bottom: 1px solid');
    expect(read('components/viewer/PhotoLightbox.vue')).toMatch(/<DialogShell[^>]*class="photo-lightbox"[^>]*size="full"/);
    const nav = declarationsOf('components/viewer/PhotoLightbox.vue', '.photo-lightbox__nav');
    expect(nav).toMatchObject({ position: 'absolute', top: '50%', height: '44px', transform: 'translateY(-50%)' });
    const columns = declarationsOf('components/viewer/PhotoLightbox.vue', '.photo-lightbox__layout')['grid-template-columns'];
    expect(columns).toBe('minmax(0, 1fr) min(360px, 38vw)');
    for (const size of SIZES) {
      const headHeight = px('var(--space-4)', size) + px('var(--h-compact)', size) - 4 + px('var(--space-3)', size) + 1;
      const area = toastArea(toastsWhen('.photo-lightbox'), size);
      expect(area.top, `${size.w}`).toBeGreaterThanOrEqual(px('var(--space-4)', size) + 2 * 16 * 1.4 + 8);
      expect(area.top - headHeight, `${size.w}`).toBeGreaterThanOrEqual(16);
      // 「上一张」在舞台（标题栏以下）竖直正中。
      const navTop = headHeight + (size.h - headHeight) / 2 - 22;
      expect(area.bottom + 8, `${size.w}`).toBeLessThanOrEqual(navTop);
      // 「下一张」贴舞台右边（12 + 44），舞台右边是信息栏。
      expect(area.right + 8, `${size.w}`).toBeLessThanOrEqual(size.w - px('min(360px, 38vw)', size) - 12 - 44);
    }
  });

  it('字幕工作台：编辑布局在时提示在左栏顶上（宽不出左栏、下沿不到视频控制条），状态页上照常在右下角', () => {
    const source = read('components/SubtitleWorkbench.vue');
    // 编辑布局与底栏只在读到字幕之后出现；状态页（读取中、没有字幕、编码待转换、读取失败）在它之前的分支里。
    expect(source).toMatch(/<template v-else>\s*<div class="subtitle-workbench__layout">\s*<aside class="subtitle-workbench__preview">/);
    expect(source.indexOf('class="subtitle-workbench__state"')).toBeLessThan(source.indexOf('class="subtitle-workbench__layout"'));
    expect(declarationsOf('components/SubtitleWorkbench.vue', '.subtitle-workbench-overlay')).toMatchObject({ padding: '16px' });
    expect(declarationsOf('components/SubtitleWorkbench.vue', '.subtitle-workbench__layout')).toMatchObject({ 'grid-template-columns': 'minmax(300px, 390px) minmax(0, 1fr)' });
    expect(declarationsOf('components/SubtitleWorkbench.vue', '.subtitle-workbench__preview')).toMatchObject({ padding: '14px' });
    for (const size of SIZES) {
      // 左栏 390（窗口最窄 1024 时编辑区也还有 600 多）：从 16 + 1 到 407；编辑区的「撤销」从 407 + 14 起。
      expect(size.w - 32 - 390).toBeGreaterThan(300);
      const area = toastArea(toastsWhen('.subtitle-workbench__layout'), size);
      expect(area.left, `${size.w}`).toBeGreaterThanOrEqual(17);
      expect(area.right + 8, `${size.w}`).toBeLessThanOrEqual(17 + 390);
      // 视频：标题栏实测 63 高，画面从 16 + 1 + 63 + 14 = 94 起，宽 390 - 28；最矮的 2.39:1 片子画面到 245，
      // 原生控制条的播放键约在画面底部 64 以内。
      const videoTop = 16 + 1 + 63 + 14;
      const controlsTop = videoTop + (390 - 28) / 2.39 - 64;
      expect(area.bottom + 8, `${size.w}`).toBeLessThanOrEqual(controlsTop);
    }
  });

  // 页面切走时只隐藏（v-show）不卸载，里面开着的层与对话框还在 DOM 里。
  const Page = {
    props: { show: { type: Boolean, default: true }, layer: { type: String, default: '' } },
    render() {
      return withDirectives(h('section', [this.layer ? h('div', { class: this.layer }) : null]), [[vShow, this.show]]);
    }
  };
  // AppFeedback 里写着的条件（「body:has(…)」那一段），拿来在 jsdom 里比对。
  const conditionInStyle = root => {
    const selector = styleRules(FEEDBACK).flatMap(rule => rule.selectors).find(item => item.startsWith(`body:has(${root}`) && item.endsWith(' .app-toasts'));
    expect(selector, `AppFeedback 里找不到 ${root} 的条件`).toBeTruthy();
    return selector.slice(0, -' .app-toasts'.length);
  };

  it('选择器只在层开着、且所在页面没有被 v-show 藏起来时命中', async () => {
    const roots = [...YIELDING, ...INSIDE_DIALOG_AREA].map(root => root.slice(1));
    for (const root of roots) {
      const condition = conditionInStyle(`.${root}`);
      const open = () => document.body.matches(condition);
      expect(open(), root).toBe(false);
      const page = mountIn(mainView, Page, { props: { layer: root } });
      await flushPromises();
      expect(open(), root).toBe(true);
      await page.setProps({ show: false });
      expect(open(), `${root} 所在页面隐藏`).toBe(false);
      await page.setProps({ show: true });
      expect(open(), `${root} 页面回来`).toBe(true);
      await page.setProps({ layer: '' });
      expect(open(), `${root} 关上`).toBe(false);
    }
  });

  // 片库、图片管理、清理中心里的对话框不 teleport，页面切走时跟着页面藏起来但还开着：以前对话框那条照样命中，
  // 别的页面上的提示也挪到左上角。可见的对话框（含 teleport 到 body 的确认框）照旧命中。
  it('对话框那条同样不算 v-show 隐藏的页面里开着的对话框', async () => {
    const condition = conditionInStyle('.modal-overlay');
    const dialogOpen = () => document.body.matches(condition);
    const DialogPage = {
      props: { show: { type: Boolean, default: true } },
      render() {
        return withDirectives(h('section', [h(DialogShell, { title: '批量编辑' }, () => '正文')]), [[vShow, this.show]]);
      }
    };
    const page = mountIn(mainView, DialogPage);
    await flushPromises();
    expect(document.querySelector('.modal-overlay')).toBeTruthy();
    expect(dialogOpen()).toBe(true);
    await page.setProps({ show: false });
    expect(document.querySelector('.modal-overlay')).toBeTruthy();
    expect(dialogOpen(), '对话框所在页面隐藏').toBe(false);
    // 隐藏页面里的对话框还在，可见的确认框照旧让提示挪开。
    mountIn(appLayer, AppFeedback);
    const answer = confirmAction({ title: '离开设置页', message: '放弃未保存的设置修改？' });
    await flushPromises();
    expect(dialogOpen(), '可见的确认框').toBe(true);
    resetFeedback();
    await flushPromises();
    expect(dialogOpen()).toBe(false);
    await page.setProps({ show: true });
    expect(dialogOpen(), '页面回来').toBe(true);
    void answer;
  });

  it('条件用的类就在各层的根节点上（抽屉、任务中心、版本对比、两个查看器、看图），层关上时整个不渲染', () => {
    expect(read('components/PreviewDrawer.vue')).toMatch(/^<template>\s*<aside class="preview-drawer"/);
    expect(read('components/VideoListPage.vue')).toMatch(/<PreviewDrawer\s+ref="previewDrawer"\s+v-if="previewOpen && selectedPreviewVideo"/);
    expect(read('components/TaskCenterDrawer.vue')).toMatch(/^<template>\s*<div\s+v-if="open"\s+class="task-center-overlay"/);
    expect(read('components/VersionCompareDialog.vue')).toMatch(/^<template>\s*<div class="version-compare" role="dialog"/);
    expect(read('components/PhotoLibraryPage.vue')).toMatch(/<div v-if="viewerImage" class="photo-viewer" role="dialog"/);
    expect(read('components/PhotoCleanupPage.vue')).toMatch(/<div v-if="viewer.member" class="cleanup-viewer" role="dialog"/);
    expect(read('components/viewer/PhotoBrowsePage.vue')).toMatch(/<PhotoLightbox\s+v-if="lightboxImage"/);
  });
});
