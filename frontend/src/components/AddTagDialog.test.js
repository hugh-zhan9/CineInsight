import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  CreateTag: vi.fn(), AddTagToVideo: vi.fn(),
  BatchAddTagToVideos: vi.fn(), BatchRemoveTagFromVideos: vi.fn(),
  GetVideoAutomaticTagOverrides: vi.fn(), ClearVideoAutomaticTagOverride: vi.fn()
}));
vi.mock('../../wailsjs/go/main/App', () => api);
import AddTagDialog from './AddTagDialog.vue';

const tags = [{ id: 1, name: '旅行' }, { id: 2, name: '动作' }];
let wrapper;
beforeEach(() => {
  vi.resetAllMocks();
  api.GetVideoAutomaticTagOverrides.mockResolvedValue([]);
});
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

// META-13（D-PC36）：自动标签被手动覆盖时在添加标签弹窗里亮出「手动」角标，并能「恢复自动」。
describe('META-13 自动标签的手动覆盖', () => {
  const autoTags = [
    { id: 3, name: '短视频', automatic_kind: 'short_video' },
    { id: 4, name: '低清', automatic_kind: 'low_resolution' }
  ];

  it('META-13 shows manual overrides with a badge and restores automatic tagging', async () => {
    api.GetVideoAutomaticTagOverrides.mockResolvedValue([
      { video_id: 10, automatic_kind: 'short_video', present: true },
      { video_id: 10, automatic_kind: 'low_resolution', present: false }
    ]);
    api.ClearVideoAutomaticTagOverride.mockResolvedValue();
    wrapper = mount(AddTagDialog, { props: { visible: true, tags: autoTags, video: { id: 10, tags: [autoTags[0]] } } });
    await flushPromises();

    expect(api.GetVideoAutomaticTagOverrides).toHaveBeenCalledWith(10);
    const shortRow = wrapper.get('[data-test="automatic-override-short_video"]');
    expect(shortRow.text()).toContain('短视频');
    expect(shortRow.find('.tag-manual-badge').text()).toBe('手动');
    expect(shortRow.text()).toContain('已手动加上');
    expect(wrapper.get('[data-test="automatic-override-low_resolution"]').text()).toContain('已手动去掉');

    await wrapper.get('[data-test="automatic-override-restore-short_video"]').trigger('click');
    await flushPromises();
    expect(api.ClearVideoAutomaticTagOverride).toHaveBeenCalledWith(10, 'short_video');
    expect(wrapper.find('[data-test="automatic-override-short_video"]').exists()).toBe(false);
    expect(wrapper.emitted('tag-added')).toEqual([[{ videoIds: [10], tagIds: [], tags: [], restoredKind: 'short_video' }]]);
  });

  it('META-13 keeps the override when restoring fails and hides the section in batch mode', async () => {
    api.GetVideoAutomaticTagOverrides.mockResolvedValue([{ video_id: 10, automatic_kind: 'low_resolution', present: true }]);
    api.ClearVideoAutomaticTagOverride.mockRejectedValue(new Error('数据库繁忙'));
    wrapper = mount(AddTagDialog, { props: { visible: true, tags: autoTags, video: { id: 10, tags: [] } } });
    await flushPromises();
    await wrapper.get('[data-test="automatic-override-restore-low_resolution"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="automatic-override-low_resolution"]').exists()).toBe(true);
    expect(wrapper.get('.automatic-override-error').text()).toContain('恢复自动失败');
    expect(wrapper.emitted('tag-added')).toBeUndefined();
    wrapper.unmount();

    api.GetVideoAutomaticTagOverrides.mockClear();
    wrapper = mount(AddTagDialog, { props: { visible: true, tags: autoTags, mode: 'batch', videoIds: [10, 11] } });
    await flushPromises();
    expect(api.GetVideoAutomaticTagOverrides).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="automatic-override-section"]').exists()).toBe(false);
  });

  // META-06：宿主据载荷按 (video_id, tag_id) 局部移除待审候选。
  it('META-06 reports the video and tag ids that were added', async () => {
    open();
    await wrapper.findAll('.clickable-tag-item')[1].trigger('click');
    await wrapper.get('.modal-actions .btn-primary').trigger('click');
    await flushPromises();
    expect(wrapper.emitted('tag-added')[0][0]).toEqual({ videoIds: [10], tagIds: [2], tags: [{ id: 2, name: '动作' }] });
  });
});
