import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join, relative } from 'node:path';

import AppFeedback from './AppFeedback.vue';
import { confirmAction, feedbackState, notify, notifyError, resetFeedback } from '../utils/feedback.js';

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
    const wrapper = mount(AppFeedback);

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
describe('提示 API 必须显式导入', () => {
  it('每个用到 notify/confirmAction 的文件都从 utils/feedback.js 导入了它们', () => {
    const missing = [];
    for (const file of listSourceFiles(SRC)) {
      if (file.endsWith('utils/feedback.js')) continue;
      const source = readFileSync(file, 'utf8');
      const importLine = source.match(/import \{([^}]*)\} from '[^']*utils\/feedback\.js';/);
      const imported = new Set((importLine?.[1] || '').split(',').map(name => name.trim()).filter(Boolean));
      for (const api of ['notify', 'notifyError', 'notifySuccess', 'confirmAction']) {
        const used = new RegExp(`(?<![\\w.])${api}\\(`).test(source);
        if (used && !imported.has(api)) missing.push(`${relative(SRC, file)} 用了 ${api}() 但没导入`);
      }
    }
    expect(missing).toEqual([]);
  });
});
