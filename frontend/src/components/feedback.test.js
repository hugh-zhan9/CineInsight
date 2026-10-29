import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';

import AppFeedback from './AppFeedback.vue';
import DialogShell from './ui/DialogShell.vue';
import { confirmAction, feedbackState, notify, notifyError, notifyUndo, resetFeedback } from '../utils/feedback.js';

const SRC = join(process.cwd(), 'src');

function listSourceFiles(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) return listSourceFiles(full);
    if (!/\.(vue|js)$/.test(entry.name) || entry.name.includes('.test.')) return [];
    return [full];
  });
}

afterEach(() => resetFeedback());

// 确认框打开时 teleport 到 body 末尾（层级按打开先后，见 tokens.css「层级」与 layering.test.js）。
// 只看确认框本身的用例用 teleport 桩，让它留在组件子树里查。
const KEEP_CONFIRM_INSIDE = { stubs: { teleport: true } };

describe('应用内提示', () => {
  it('错误提示常驻直到手动关闭，普通提示会自己消失', async () => {
    const wrapper = mount(AppFeedback);

    notifyError('删除失败: 原路径与回收站路径均不存在文件');
    notify('已加入队列', { timeout: 0 });
    await flushPromises();

    const toasts = wrapper.findAll('[data-test="app-toast"]');
    expect(toasts).toHaveLength(2);
    expect(toasts[0].text()).toContain('原路径与回收站路径均不存在文件');

    await toasts[0].find('[data-test="app-toast-close"]').trigger('click');
    expect(wrapper.findAll('[data-test="app-toast"]')).toHaveLength(1);

    wrapper.unmount();
  });

  it('确认框返回用户的选择，而不是像 window.confirm 那样恒为 false', async () => {
    const wrapper = mount(AppFeedback, { global: KEEP_CONFIRM_INSIDE });

    const accepted = confirmAction({ title: '删除扫描目录', message: '确定要删除此目录配置吗？' });
    await flushPromises();
    expect(wrapper.find('[data-test="app-confirm-ok"]').exists()).toBe(true);
    await wrapper.find('[data-test="app-confirm-ok"]').trigger('click');
    expect(await accepted).toBe(true);

    const rejected = confirmAction({ message: '再来一次' });
    await flushPromises();
    await wrapper.find('[data-test="app-confirm-cancel"]').trigger('click');
    expect(await rejected).toBe(false);
    expect(feedbackState.confirm).toBe(null);

    wrapper.unmount();
  });

  it('D-UD-07 提示里的「撤销」按钮执行一次并收起提示；关闭按钮带读屏名', async () => {
    const wrapper = mount(AppFeedback);
    let undone = 0;
    notifyUndo('已移到废纸篓：2 个文件', () => { undone += 1; });
    notify('已保存', { timeout: 0 });
    await flushPromises();
    const toasts = wrapper.findAll('[data-test="app-toast"]');
    expect(toasts[0].get('[data-test="app-toast-action"]').text()).toBe('撤销');
    expect(toasts[1].find('[data-test="app-toast-action"]').exists()).toBe(false);
    expect(toasts[0].get('[data-test="app-toast-close"]').attributes('aria-label')).toBe('关闭提示');

    await toasts[0].get('[data-test="app-toast-action"]').trigger('click');
    await flushPromises();
    expect(undone).toBe(1);
    expect(wrapper.findAll('[data-test="app-toast"]').map(toast => toast.text())).toEqual(['已保存']);
    wrapper.unmount();
  });

  it('D-UD-07 确认框是 DialogShell：标题、影响说明、取消在左主操作在右；普通确认不要输入', async () => {
    const wrapper = mount(AppFeedback, { attachTo: document.body, global: KEEP_CONFIRM_INSIDE });
    const answer = confirmAction({ title: '从片库移除', message: '移除 3 部视频的记录。', confirmText: '从片库移除', impact: '文件不动，只删记录', danger: true });
    await flushPromises();
    const dialog = wrapper.get('[data-test="app-confirm"]');
    expect(dialog.attributes('role')).toBe('dialog');
    expect(dialog.get('h2').text()).toBe('从片库移除');
    expect(dialog.get('[data-test="app-confirm-impact"]').text()).toBe('文件不动，只删记录');
    expect(dialog.find('[data-test="app-confirm-input"]').exists()).toBe(false);
    expect(dialog.findAll('.ui-dialog__footer button').map(button => button.attributes('data-test'))).toEqual(['app-confirm-cancel', 'app-confirm-ok']);
    expect(dialog.get('[data-test="app-confirm-ok"]').classes()).toContain('btn--danger');
    await dialog.get('[data-test="dialog-shell-close"]').trigger('click');
    expect(await answer).toBe(false);
    wrapper.unmount();
  });

  it('D-UD-07 不可逆确认：输入按钮上的原文前不能点，回车也不放行；对上后才能确认', async () => {
    const wrapper = mount(AppFeedback, { attachTo: document.body, global: KEEP_CONFIRM_INSIDE });
    const answer = confirmAction({ title: '永久删除', message: '这 2 条记录会被永久删除。', confirmText: '永久删除', level: 'irreversible' });
    await flushPromises();
    const ok = wrapper.get('[data-test="app-confirm-ok"]');
    const input = wrapper.get('[data-test="app-confirm-input"]');
    expect(ok.attributes('disabled')).toBeDefined();
    expect(ok.classes()).toContain('btn--danger');
    expect(wrapper.text()).toContain('输入「永久删除」以确认');
    expect(document.activeElement).toBe(input.element);

    await input.setValue('永久');
    await input.trigger('keydown', { key: 'Enter' });
    await ok.trigger('click');
    expect(feedbackState.confirm).not.toBe(null);

    await input.setValue('永久删除');
    expect(ok.attributes('disabled')).toBeUndefined();
    await input.trigger('keydown', { key: 'Enter' });
    expect(await answer).toBe(true);
    wrapper.unmount();
  });

  it('确认框叠在对话框上时，Esc 只关确认框（按「取消」结掉），下面的对话框不动', async () => {
    const page = mount(DialogShell, { props: { title: '回收站' }, attachTo: document.body });
    const wrapper = mount(AppFeedback, { attachTo: document.body });
    const answer = confirmAction({ title: '从片库移除', message: '移除 1 部视频的记录。' });
    await flushPromises();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(await answer).toBe(false);
    expect(page.emitted('close')).toBeUndefined();

    await flushPromises();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(page.emitted('close')).toHaveLength(1);
    wrapper.unmount();
    page.unmount();
  });

  it('D-UD-07 不可逆确认：输入法组字中的回车不算确认', async () => {
    const wrapper = mount(AppFeedback, { attachTo: document.body, global: KEEP_CONFIRM_INSIDE });
    const answer = confirmAction({ title: '永久删除', confirmText: '永久删除', level: 'irreversible' });
    await flushPromises();
    const input = wrapper.get('[data-test="app-confirm-input"]');
    await input.setValue('永久删除');

    await input.trigger('keydown', { key: 'Enter', isComposing: true });
    await input.trigger('keydown', { key: 'Enter', keyCode: 229 });
    expect(feedbackState.confirm).not.toBe(null);

    await input.trigger('keydown', { key: 'Enter' });
    expect(await answer).toBe(true);
    wrapper.unmount();
  });

  it('D-UD-13 不含表单的普通确认点遮罩等于取消；不可逆确认有输入框，点遮罩不关', async () => {
    const wrapper = mount(AppFeedback, { attachTo: document.body, global: KEEP_CONFIRM_INSIDE });
    const plain = confirmAction({ title: '从片库移除', message: '移除 3 部视频的记录。' });
    await flushPromises();
    await wrapper.get('.modal-overlay').trigger('click');
    expect(await plain).toBe(false);
    expect(feedbackState.confirm).toBe(null);

    const typed = confirmAction({ title: '永久删除', confirmText: '永久删除', level: 'irreversible' });
    await flushPromises();
    await wrapper.get('.modal-overlay').trigger('click');
    expect(feedbackState.confirm).not.toBe(null);
    resetFeedback();
    void typed;
    wrapper.unmount();
  });

  it('换一个确认框时输入清空', async () => {
    const wrapper = mount(AppFeedback, { attachTo: document.body, global: KEEP_CONFIRM_INSIDE });
    const first = confirmAction({ confirmText: '清空', level: 'irreversible' });
    await flushPromises();
    await wrapper.get('[data-test="app-confirm-input"]').setValue('清空');
    const second = confirmAction({ confirmText: '永久删除', level: 'irreversible' });
    await flushPromises();
    expect(await first).toBe(false);
    expect(wrapper.get('[data-test="app-confirm-input"]').element.value).toBe('');
    expect(wrapper.get('[data-test="app-confirm-ok"]').attributes('disabled')).toBeDefined();
    resetFeedback();
    await flushPromises();
    void second;
    wrapper.unmount();
  });
});

describe('禁止再用 webview 里失效的原生弹框', () => {
  it('源码里不再出现 window.alert / window.confirm / window.prompt', () => {
    const offenders = [];
    for (const file of listSourceFiles(SRC)) {
      const source = readFileSync(file, 'utf8');
      // 说明性注释里提到这些名字是允许的，只揪真正的调用。
      for (const match of source.matchAll(/(?<![\w.])(?:window\.)?(alert|confirm|prompt)\s*\(/g)) {
        const line = source.slice(0, match.index).split('\n').length;
        const text = source.split('\n')[line - 1].trim();
        if (text.startsWith('//') || text.startsWith('*')) continue;
        if (/confirmAction|confirmDelete|confirmBatch|\bconfirm\(\)/.test(text)) continue;
        offenders.push(`${relative(SRC, file)}:${line} ${text.slice(0, 80)}`);
      }
    }
    expect(offenders).toEqual([]);
  });
});

// 用了提示 API 却忘了 import：Vite 构建不报错，点下去才在运行期炸成一次
// 静默的 unhandled rejection（按钮"点了没反应"）。静态钉住。
function feedbackImportNames(source, file) {
  const match = [...source.matchAll(/import \{([^}]*)\} from '([^']+)';/g)]
    .find(item => resolve(dirname(file), item[2]) === join(SRC, 'utils/feedback.js'));
  return new Set((match?.[1] || '').split(',').map(name => name.trim()).filter(Boolean));
}
describe('提示 API 必须显式导入', () => {
  it('resolves local utility imports without accepting an unrelated feedback module', () => {
    expect(feedbackImportNames("import { notifyError } from './feedback.js';", join(SRC, 'utils/example.js')).has('notifyError')).toBe(true);
    expect(feedbackImportNames("import { notifyError } from '../utils/feedback.js';", join(SRC, 'components/example.vue')).has('notifyError')).toBe(true);
    expect(feedbackImportNames("import { notifyError } from './feedback.js';", join(SRC, 'components/example.vue')).size).toBe(0);
    expect(feedbackImportNames('', join(SRC, 'utils/example.js')).size).toBe(0);
  });
  it('每个用到 notify/confirmAction 的文件都从 utils/feedback.js 导入了它们', () => {
    const missing = [];
    for (const file of listSourceFiles(SRC)) {
      if (file.endsWith('utils/feedback.js')) continue;
      const source = readFileSync(file, 'utf8');
      const imported = feedbackImportNames(source, file);
      for (const api of ['notify', 'notifyError', 'notifySuccess', 'notifyUndo', 'notifyUndoable', 'confirmAction']) {
        const used = new RegExp(`(?<![\\w.])${api}\\(`).test(source);
        if (used && !imported.has(api)) missing.push(`${relative(SRC, file)} 用了 ${api}() 但没导入`);
      }
    }
    expect(missing).toEqual([]);
  });
});
