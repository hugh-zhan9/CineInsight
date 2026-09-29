import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetTagUsageCounts: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import TagDeleteDialog from './TagDeleteDialog.vue';

beforeEach(() => vi.resetAllMocks());

// META-14（D-PC37）：删除标签的确认框显示受影响的视频与图片数，回收站里的另列。
describe('TagDeleteDialog META-14 影响范围', () => {
  it('META-14 shows how many videos and images lose the tag, including trashed ones', async () => {
    api.GetTagUsageCounts.mockResolvedValue({ 7: { videos: 12, images: 4, trashed_videos: 2, trashed_images: 1 } });
    const wrapper = mount(TagDeleteDialog, { props: { visible: true, tag: { id: 7, name: '旅行' } } });
    await flushPromises();
    expect(api.GetTagUsageCounts).toHaveBeenCalledWith([7]);
    const impact = wrapper.get('[data-test="tag-delete-impact"]').text();
    expect(impact).toContain('将从 12 部视频、4 张图片上移除这个标签');
    expect(impact).toContain('回收站中另有 2 部视频、1 张图片');

    await wrapper.findAll('.modal-actions button').find(button => button.text() === '确认删除').trigger('click');
    expect(wrapper.emitted('confirm-delete')[0][0]).toEqual({ id: 7, name: '旅行' });
  });

  it('META-14 says so when nothing uses the tag, and reloads for another tag', async () => {
    api.GetTagUsageCounts.mockResolvedValueOnce({ 7: { videos: 0, images: 0, trashed_videos: 0, trashed_images: 0 } })
      .mockResolvedValueOnce({ 8: { videos: 1, images: 0 } });
    const wrapper = mount(TagDeleteDialog, { props: { visible: true, tag: { id: 7, name: '旅行' } } });
    await flushPromises();
    expect(wrapper.get('[data-test="tag-delete-impact"]').text()).toContain('目前没有视频或图片使用这个标签');

    await wrapper.setProps({ tag: { id: 8, name: '动作' } });
    await flushPromises();
    expect(api.GetTagUsageCounts).toHaveBeenLastCalledWith([8]);
    expect(wrapper.get('[data-test="tag-delete-impact"]').text()).toContain('将从 1 部视频、0 张图片');
  });

  it('META-14 still allows deleting when the count cannot be read, and says why', async () => {
    api.GetTagUsageCounts.mockRejectedValue(new Error('数据库繁忙'));
    const wrapper = mount(TagDeleteDialog, { props: { visible: true, tag: { id: 7, name: '旅行' } } });
    await flushPromises();
    expect(wrapper.get('[data-test="tag-delete-impact"]').text()).toContain('统计受影响的媒体失败');
    await wrapper.findAll('.modal-actions button').find(button => button.text() === '确认删除').trigger('click');
    expect(wrapper.emitted('confirm-delete')).toHaveLength(1);
  });
});
