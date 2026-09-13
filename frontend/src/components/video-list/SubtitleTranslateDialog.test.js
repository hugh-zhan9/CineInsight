// 翻译已有字幕会直接覆盖用户的 .srt，所以这里盯的是三件不能出错的事：
// 长任务要给得出退出口、叠加翻译要先警告、并发点击不能张冠李戴。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));

vi.mock('../../../wailsjs/go/main/App', () => api);

import SubtitleTranslateDialog from './SubtitleTranslateDialog.vue';

const video = { id: 7, name: '黑客帝国' };

beforeEach(() => {
  vi.clearAllMocks();
  document.body.innerHTML = '';
});

describe('字幕翻译弹窗', () => {
  it('翻译进行中给得出取消出口，并把取消透到后端', async () => {
    let resolveTranslate;
    api.GetSubtitleSegments.mockResolvedValue([]);
    api.TranslateSubtitle.mockReturnValue(new Promise((resolve) => { resolveTranslate = resolve; }));

    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await wrapper.vm.translate(video);
    await flushPromises();
    await wrapper.find('[data-test="subtitle-translate-start"]').trigger('click');
    await flushPromises();

    const cancel = wrapper.find('[data-test="subtitle-translate-cancel"]');
    expect(cancel.exists()).toBe(true);
    await cancel.trigger('click');
    await flushPromises();
    expect(api.CancelSubtitleTranslation).toHaveBeenCalledWith(7);

    // 行菜单靠这个事件置灰入口，漏掉它菜单会悄悄不再禁用。
    expect(wrapper.emitted('translating-change')?.[0]).toEqual([7]);

    resolveTranslate({ entries: 0, path: '' });
    await flushPromises();
    expect(wrapper.emitted('translating-change')?.at(-1)).toEqual([null]);
    wrapper.unmount();
  });

  it('当前字幕已经是双语时先给出叠加警告', async () => {
    api.GetSubtitleSegments.mockResolvedValue([
      { lines: ['Hello', '你好'] },
      { lines: ['World', '世界'] },
      { lines: ['Solo'] }
    ]);

    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await wrapper.vm.translate(video);
    await flushPromises();

    expect(wrapper.find('[data-test="subtitle-translate-existing-notice"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('单语字幕不报叠加警告', async () => {
    api.GetSubtitleSegments.mockResolvedValue([{ lines: ['Hello'] }, { lines: ['World'] }]);

    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await wrapper.vm.translate(video);
    await flushPromises();

    expect(wrapper.find('[data-test="subtitle-translate-existing-notice"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('翻译进行中不被另一个视频挤掉，避免结果张冠李戴', async () => {
    api.GetSubtitleSegments.mockResolvedValue([]);
    api.TranslateSubtitle.mockReturnValue(new Promise(() => {}));

    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await wrapper.vm.translate(video);
    await flushPromises();
    await wrapper.find('[data-test="subtitle-translate-start"]').trigger('click');
    await flushPromises();

    await wrapper.vm.translate({ id: 9, name: '另一部片子' });
    await flushPromises();

    expect(wrapper.vm.video.id).toBe(7);
    expect(wrapper.vm.dialog.mode).toBe('progress');
    wrapper.unmount();
  });

  it('原语言与目标语言相同时不让发起翻译', async () => {
    api.GetSubtitleSegments.mockResolvedValue([]);

    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await wrapper.vm.translate(video);
    await flushPromises();
    wrapper.vm.sourceLang = 'zh';
    wrapper.vm.targetLang = 'zh';
    await flushPromises();

    expect(wrapper.find('[data-test="subtitle-translate-start"]').attributes('disabled')).toBeDefined();
    await wrapper.find('[data-test="subtitle-translate-start"]').trigger('click');
    await flushPromises();
    expect(api.TranslateSubtitle).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});
