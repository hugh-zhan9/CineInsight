// 翻译已有字幕会直接覆盖用户的 .srt，所以这里盯的是三件不能出错的事：
// 长任务要给得出退出口、叠加翻译要先警告、并发点击不能张冠李戴。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));

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
  delete window.runtime;
});

async function startTranslating(wrapper) {
  await wrapper.vm.translate(video);
  await flushPromises();
  await wrapper.find('[data-test="subtitle-translate-start"]').trigger('click');
  await flushPromises();
}

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

describe('字幕翻译：后台继续与结果', () => {
  it('MEDIA-12 翻译中可「后台继续」：弹窗收起，片库页留一条进度，完成后用提示条告知', async () => {
    const handlers = {};
    window.runtime = { EventsOn: vi.fn((name, handler) => { handlers[name] = handler; return () => {}; }) };
    let resolveTranslate;
    api.GetSubtitleSegments.mockResolvedValue([]);
    api.TranslateSubtitle.mockReturnValue(new Promise(resolve => { resolveTranslate = resolve; }));
    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await startTranslating(wrapper);

    await wrapper.find('[data-test="subtitle-translate-background"]').trigger('click');
    expect(wrapper.find('[data-test="subtitle-translate-dialog"]').exists()).toBe(false);
    handlers['subtitle-translate-progress']({ videoID: 7, percent: 45, message: '已翻译 45%' });
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-translate-bar"]').text()).toContain('「黑客帝国」的字幕 · 45%');
    expect(wrapper.emitted('translate-progress').at(-1)).toEqual([{ videoId: 7, percent: 45 }]);

    resolveTranslate({ entries: 12, path: '/Users/me/Movies/黑客帝国.srt', backup_id: 'b1' });
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-translate-bar"]').exists()).toBe(false);
    const text = feedback.notify.mock.calls.at(-1)[0];
    expect(text).toContain('字幕翻译完成：「黑客帝国」');
    expect(text).toContain('原字幕已备份');
    expect(text).not.toContain('/Users/');
    expect(wrapper.emitted('translated')).toHaveLength(1);
    wrapper.unmount();
  });

  it('MEDIA-12 翻译中按 Esc 等同后台继续；进度条上也能取消，「查看进度」把弹窗叫回来', async () => {
    api.GetSubtitleSegments.mockResolvedValue([]);
    api.TranslateSubtitle.mockReturnValue(new Promise(() => {}));
    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await startTranslating(wrapper);

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-translate-dialog"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="subtitle-translate-bar"]').exists()).toBe(true);
    expect(api.CancelSubtitleTranslation).not.toHaveBeenCalled();

    await wrapper.find('[data-test="subtitle-translate-bar-cancel"]').trigger('click');
    await flushPromises();
    expect(api.CancelSubtitleTranslation).toHaveBeenCalledWith(7);

    await wrapper.find('[data-test="subtitle-translate-show"]').trigger('click');
    expect(wrapper.find('[data-test="subtitle-translate-dialog"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('MEDIA-05 覆盖成功后说明原字幕已备份，结果里不出现绝对路径', async () => {
    api.GetSubtitleSegments.mockResolvedValue([]);
    api.TranslateSubtitle.mockResolvedValue({ entries: 3, path: '/Volumes/盘/片/a.srt', backup_id: 'b9' });
    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await startTranslating(wrapper);

    const message = wrapper.find('[data-test="subtitle-translate-msg"]').text();
    expect(message).toContain('已翻译 3 条字幕，写回同名字幕文件。');
    expect(message).toContain('原字幕已备份');
    expect(message).not.toContain('/Volumes/');
    wrapper.unmount();
  });

  it('MEDIA-08 只有内嵌/其他格式字幕时说明原因，不再引导去生成', async () => {
    api.GetSubtitleSegments.mockRejectedValue(new Error('open /x/a.srt: no such file or directory'));
    api.TranslateSubtitle.mockResolvedValue({ error_code: 'subtitle_not_sidecar_srt', message: '该视频只有内嵌/其他格式字幕，暂不支持编辑或翻译' });
    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await startTranslating(wrapper);

    expect(wrapper.text()).toContain('无法翻译这个字幕');
    expect(wrapper.find('[data-test="subtitle-translate-msg"]').text()).toBe('该视频只有内嵌/其他格式字幕，暂不支持编辑或翻译');
    expect(wrapper.emitted('translated')).toBeUndefined();
    wrapper.unmount();
  });
});

describe('字幕翻译：非 UTF-8 字幕（MEDIA-02）', () => {
  it('MEDIA-02 GBK 字幕先提示一键转换（会先备份），转换后回到翻译确认', async () => {
    api.GetSubtitleSegments.mockResolvedValue([]);
    api.TranslateSubtitle.mockResolvedValue({ error_code: 'subtitle_encoding_not_utf8', detected_encoding: 'gb18030', message: '字幕文件不是 UTF-8 编码（检测为 GB18030）' });
    api.ConvertSubtitleToUTF8.mockResolvedValue({ encoding: 'gb18030', backup_id: 'b1' });
    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await startTranslating(wrapper);

    expect(wrapper.find('[data-test="subtitle-translate-encoding"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="subtitle-translate-msg"]').text()).toBe('字幕编码为 GBK（推测），可一键转换为 UTF-8（会先备份）。');
    await wrapper.find('[data-test="subtitle-translate-convert"]').trigger('click');
    await flushPromises();

    expect(api.ConvertSubtitleToUTF8).toHaveBeenCalledWith(7, 'gb18030');
    expect(wrapper.find('[data-test="subtitle-translate-start"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="subtitle-translate-msg"]').text()).toContain('已转换为 UTF-8（原文件已备份）');
    wrapper.unmount();
  });

  it('MEDIA-02 编码有歧义时按预览选定再转换，传所选的编码', async () => {
    api.GetSubtitleSegments.mockResolvedValue([]);
    api.TranslateSubtitle.mockResolvedValue({
      error_code: 'subtitle_encoding_not_utf8', detected_encoding: 'gb18030',
      candidates: [{ encoding: 'gb18030', preview: '乱码预览' }, { encoding: 'big5', preview: '繁體預覽' }]
    });
    api.ConvertSubtitleToUTF8.mockResolvedValue({ encoding: 'big5' });
    const wrapper = mount(SubtitleTranslateDialog, { attachTo: document.body });
    await startTranslating(wrapper);

    expect(wrapper.text()).toContain('繁體預覽');
    await wrapper.find('[data-test="subtitle-translate-encoding-big5"]').setValue(true);
    await wrapper.find('[data-test="subtitle-translate-convert"]').trigger('click');
    await flushPromises();
    expect(api.ConvertSubtitleToUTF8).toHaveBeenCalledWith(7, 'big5');
    wrapper.unmount();
  });
});
