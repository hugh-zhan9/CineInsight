import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

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

  it('从低到高：吸顶 < 菜单 < 抽屉 < 对话框与查看器 < 全局提示 < 关键遮罩', () => {
    const tokens = read('styles/tokens.css');
    const value = name => Number((tokens.match(new RegExp(`${name}:\\s*(\\d+);`)) || [])[1]);
    const scale = ['--z-sticky', '--z-menu', '--z-drawer', '--z-modal', '--z-toast', '--z-critical'].map(value);
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
