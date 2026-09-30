import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetSubtitleSegments: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);

import SubtitlePreviewModal from './SubtitlePreviewModal.vue';

beforeEach(() => {
  vi.clearAllMocks();
  document.body.innerHTML = '';
});

describe('字幕预览弹窗', () => {
  it('MEDIA-08 没有同名 .srt 时说明预览只认同名 .srt，不把系统错误和路径抛给用户', async () => {
    api.GetSubtitleSegments.mockRejectedValue(new Error('open /Users/me/Movies/movie.srt: no such file or directory'));
    const wrapper = mount(SubtitlePreviewModal, { attachTo: document.body });
    await wrapper.vm.open({ id: 7, name: 'movie.mkv' });
    await flushPromises();

    const text = wrapper.find('[data-test="subtitle-preview-error"]').text();
    expect(text).toContain('没有同名 .srt 字幕');
    expect(text).toContain('内嵌字幕或其他格式');
    expect(text).not.toContain('/Users/');
    wrapper.unmount();
  });

  it('MEDIA-08 其他读取错误擦掉路径后再显示；按 Esc 可关闭', async () => {
    api.GetSubtitleSegments.mockRejectedValue(new Error('read /Volumes/盘/片/movie.srt: input/output error'));
    const wrapper = mount(SubtitlePreviewModal, { attachTo: document.body });
    await wrapper.vm.open({ id: 7, name: 'movie.mkv' });
    await flushPromises();

    const text = wrapper.find('[data-test="subtitle-preview-error"]').text();
    expect(text).toBe('读取字幕片段失败：read movie.srt: input/output error');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-preview-error"]').exists()).toBe(false);
    wrapper.unmount();
  });
});
