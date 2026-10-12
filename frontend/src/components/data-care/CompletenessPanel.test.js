import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetMetadataCompleteness: vi.fn(), GetMetadataCompletenessByDirectory: vi.fn(), LogFrontend: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);

import CompletenessPanel from './CompletenessPanel.vue';

const report = () => ({
  video_total: 200,
  videos: [
    { field: 'title', missing: 0, total: 200 },
    { field: 'year', missing: 50, total: 200 },
    { field: 'poster', missing: 3, total: 200 },
    { field: 'thumbnail', missing: 7, total: 200 }
  ],
  video_pending: [
    { field: 'nfo_update', missing: 4, total: 200 },
    { field: 'nfo_error', missing: 1, total: 200 }
  ],
  image_total: 30,
  image_exif_unparsed: 10,
  images: [
    { field: 'taken_at', missing: 5, total: 20 },
    { field: 'tags', missing: 30, total: 30 }
  ]
});

const wrappers = [];
async function mountPanel() {
  const wrapper = mount(CompletenessPanel);
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  api.GetMetadataCompleteness.mockResolvedValue(report());
  api.LogFrontend.mockResolvedValue(undefined);
});
afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
});

describe('完整度页签（D-MC-02）', () => {
  it('每项显示缺失数与占比，未解析 EXIF 的图片单独说明', async () => {
    const wrapper = await mountPanel();
    expect(api.GetMetadataCompleteness).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="completeness-item-video-year"]').text()).toContain('年份');
    expect(wrapper.get('[data-test="completeness-item-video-year"] [data-test="completeness-count"]').text()).toBe('缺 50 · 25%');
    expect(wrapper.get('[data-test="completeness-item-video-poster"] [data-test="completeness-count"]').text()).toBe('缺 3 · 1.5%');
    expect(wrapper.get('[data-test="completeness-item-image-taken_at"] [data-test="completeness-count"]').text()).toBe('缺 5 · 25%');
    expect(wrapper.get('[data-test="completeness-group-images"]').text()).toContain('10 张还没解析 EXIF');
    expect(wrapper.get('[data-test="completeness-item-video-year"] .completeness-bar__fill').attributes('style')).toContain('width: 25%');
    // 不缺的项没有按钮。
    expect(wrapper.find('[data-test="completeness-go-video-title"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="completeness-directories-video-title"]').exists()).toBe(false);
  });

  it('「去处理」带上这一项发 open-library-missing；缩略图不能去处理', async () => {
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="completeness-go-video-year"]').trigger('click');
    await wrapper.get('[data-test="completeness-go-image-tags"]').trigger('click');
    expect(wrapper.emitted('open-library-missing')).toEqual([[{ kind: 'video', field: 'year' }], [{ kind: 'image', field: 'tags' }]]);
    expect(wrapper.find('[data-test="completeness-go-video-thumbnail"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="completeness-item-video-thumbnail"]').text()).toContain('浏览片库时自动生成');
  });

  it('待处理：本地资料有更新跳到现有视图，读取出错只展示', async () => {
    const wrapper = await mountPanel();
    expect(wrapper.get('[data-test="completeness-item-video-nfo_update"] [data-test="completeness-count"]').text()).toBe('4 部 · 2%');
    await wrapper.get('[data-test="completeness-go-video-nfo_update"]').trigger('click');
    expect(wrapper.emitted('run-command')).toEqual([['library.openLocalMetadataUpdates', '查看本地资料有更新的视频']]);
    expect(wrapper.find('[data-test="completeness-go-video-nfo_error"]').exists()).toBe(false);
    expect(wrapper.emitted('open-library-missing')).toBeUndefined();
  });

  it('「缺得最多的目录」展开时才取，收起再展开不重复取', async () => {
    api.GetMetadataCompletenessByDirectory.mockResolvedValue([
      { directory: '/lib/a', missing: 30, total: 40 },
      { directory: '/lib/b', missing: 20, total: 20 }
    ]);
    const wrapper = await mountPanel();
    expect(api.GetMetadataCompletenessByDirectory).not.toHaveBeenCalled();
    const toggle = wrapper.get('[data-test="completeness-directories-video-year"]');
    await toggle.trigger('click');
    await flushPromises();
    expect(api.GetMetadataCompletenessByDirectory).toHaveBeenCalledWith('video', 'year', 10);
    const rows = wrapper.findAll('[data-test="completeness-dirs-video-year"] [data-test="completeness-dir-row"]');
    expect(rows.map(row => row.text())).toEqual(['/lib/a缺 30 / 共 40', '/lib/b缺 20 / 共 20']);
    await toggle.trigger('click');
    expect(wrapper.find('[data-test="completeness-dirs-video-year"]').exists()).toBe(false);
    await toggle.trigger('click');
    await flushPromises();
    expect(api.GetMetadataCompletenessByDirectory).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="completeness-dirs-video-year"]').exists()).toBe(true);
  });

  it('库是空的时只给说明，不列一排「缺 0」', async () => {
    api.GetMetadataCompleteness.mockResolvedValueOnce({ ...report(), video_total: 0, image_total: 0, image_exif_unparsed: 0 });
    const wrapper = await mountPanel();
    expect(wrapper.get('[data-test="completeness-group-videos"]').text()).toContain('片库里还没有视频。');
    expect(wrapper.get('[data-test="completeness-group-images"]').text()).toContain('图片库里还没有图片。');
    expect(wrapper.find('[data-test="completeness-item-video-year"]').exists()).toBe(false);
  });

  it('读取失败时给一句不带原始错误的提示，重新统计可恢复', async () => {
    api.GetMetadataCompleteness.mockRejectedValueOnce(new Error('sql: /secret/path broken'));
    const wrapper = await mountPanel();
    const alert = wrapper.get('[data-test="completeness-error"]');
    expect(alert.text()).toBe('读取完整度统计失败，请稍后重试。');
    await wrapper.get('[data-test="completeness-refresh"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="completeness-error"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="completeness-item-video-year"]').exists()).toBe(true);
  });
});
