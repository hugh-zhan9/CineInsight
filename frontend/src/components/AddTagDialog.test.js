import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  CreateTag: vi.fn(), AddTagToVideo: vi.fn(),
  BatchAddTagToVideos: vi.fn(), BatchRemoveTagFromVideos: vi.fn()
}));
vi.mock('../../wailsjs/go/main/App', () => api);
import AddTagDialog from './AddTagDialog.vue';

const tags = [{ id: 1, name: '旅行' }, { id: 2, name: '动作' }];
let wrapper;
beforeEach(() => vi.resetAllMocks());
afterEach(() => { wrapper?.unmount(); vi.restoreAllMocks(); });

function open(mode = 'single') {
  wrapper = mount(AddTagDialog, {
    props: { visible: true, tags, mode, video: { id: 10, tags: [] }, videoIds: [10, 11] }
  });
  return wrapper.get('input');
}

describe('添加标签输入', () => {
  it.each(['single', 'batch'])('%s 选择后清空搜索并能继续选下一个标签', async mode => {
    const input = open(mode);
    await input.setValue('旅');
    expect(wrapper.findAll('.clickable-tag-item')).toHaveLength(1);
    await wrapper.get('.clickable-tag-item').trigger('click');
    expect(input.element.value).toBe('');
    expect(wrapper.findAll('.clickable-tag-item')).toHaveLength(2);
    await input.setValue('动作');
    await wrapper.get('.clickable-tag-item').trigger('click');
    expect(input.element.value).toBe('');
    expect(wrapper.findAll('.selected-tag-pill').map(tag => tag.text())).toEqual(['旅行×', '动作×']);
    await wrapper.get('.modal-actions .btn-primary').trigger('click');
    await flushPromises();
    const calls = mode === 'batch' ? api.BatchAddTagToVideos : api.AddTagToVideo;
    expect(calls.mock.calls).toEqual(mode === 'batch' ? [[[10, 11], 1], [[10, 11], 2]] : [[10, 1], [10, 2]]);
    expect(wrapper.emitted('tag-added')).toHaveLength(1);
  });

  it('取消选择保留正在输入的搜索词', async () => {
    const input = open();
    await wrapper.get('.clickable-tag-item').trigger('click');
    await input.setValue('动作');
    await wrapper.get('.remove-selected-tag').trigger('click');
    expect(input.element.value).toBe('动作');
    expect(wrapper.findAll('.selected-tag-pill')).toHaveLength(0);
  });

  it('创建成功清空，创建失败或空输入保留原状', async () => {
    const input = open();
    await input.setValue('  ');
    await input.trigger('keyup.enter');
    expect(api.CreateTag).not.toHaveBeenCalled();
    api.CreateTag.mockRejectedValueOnce(new Error('创建失败'));
    vi.spyOn(console, 'error').mockImplementation(() => {});
    await input.setValue('新标签');
    await input.trigger('keyup.enter');
    await flushPromises();
    expect(input.element.value).toBe('新标签');
    api.CreateTag.mockResolvedValueOnce({ id: 3, name: '新标签' });
    await input.trigger('keyup.enter');
    await flushPromises();
    expect(input.element.value).toBe('');
    expect(wrapper.get('.selected-tag-pill').text()).toContain('新标签');
  });

  it('offers short-video and low-resolution automatic tags for manual video assignment', async () => {
    wrapper = mount(AddTagDialog, { props: {
      visible: true,
      video: { id: 10, tags: [] },
      tags: [
        { id: 3, name: '短视频', automatic_kind: 'short_video' },
        { id: 4, name: '低清', automatic_kind: 'low_resolution' },
        { id: 5, name: '其他自动', automatic_kind: 'other' }
      ]
    } });
    expect(wrapper.findAll('.clickable-tag-item').map(tag => tag.text())).toEqual(['短视频+', '低清+']);
    await wrapper.findAll('.clickable-tag-item')[1].trigger('click');
    await wrapper.get('.modal-actions .btn-primary').trigger('click');
    await flushPromises();
    expect(api.AddTagToVideo).toHaveBeenCalledWith(10, 4);
  });
});
