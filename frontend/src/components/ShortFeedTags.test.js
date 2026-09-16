import { shallowMount, flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ getFeedTags: vi.fn(), createFeedTag: vi.fn(), setItemTag: vi.fn() }));
vi.mock('../short-feed/api.js', async original => ({ ...(await original()), ...api }));
import ShortFeedApp from '../short-feed/ShortFeedApp.vue';

const tags = [{ id: 1, name: '旅行' }, { id: 2, name: '动作' }];
let wrapper;
beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers();
  api.getFeedTags.mockResolvedValue({ tags });
});
afterEach(() => { wrapper?.unmount(); vi.clearAllTimers(); vi.useRealTimers(); });

function open(attached = [], mediaKind = 'video') {
  wrapper = shallowMount({ ...ShortFeedApp, mounted() {} }, {
    data: () => ({ sheet: 'tags', feedTags: tags, items: [{ id: 10, media_kind: mediaKind, tags: attached }], index: 0 }),
    global: { stubs: { FeedSheet: { template: '<div><slot /><slot name="footer" /></div>' } } }
  });
  return wrapper.get('input[aria-label="搜索或新建标签"]');
}

describe('网页标签输入', () => {
  it.each(['video', 'image'])('%s 添加成功后清空并重新加载全部候选', async mediaKind => {
    const input = open([], mediaKind);
    let finish;
    api.setItemTag.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await input.setValue('旅');
    await wrapper.get('.tag-option').trigger('click');
    expect(input.element.value).toBe('旅');
    finish({ id: 10, media_kind: mediaKind, tags: [tags[0]] });
    await flushPromises();
    expect(input.element.value).toBe('');
    expect(wrapper.findAll('.tag-option')).toHaveLength(2);
    expect(wrapper.get('.tag-option.active').text()).toContain('旅行');
    await vi.advanceTimersByTimeAsync(100);
    expect(api.getFeedTags).toHaveBeenLastCalledWith('');
  });

  it('添加失败保留搜索词，移除已有标签也不清空搜索词', async () => {
    const input = open([tags[0]]);
    await input.setValue('动作');
    api.setItemTag.mockRejectedValueOnce(new Error('写入失败'));
    await wrapper.get('.tag-option').trigger('click');
    await flushPromises();
    expect(input.element.value).toBe('动作');
    expect(wrapper.vm.toast.message).toContain('标签写入失败');
    await input.setValue('旅');
    api.setItemTag.mockResolvedValueOnce({ id: 10, tags: [] });
    await wrapper.get('.tag-option').trigger('click');
    await flushPromises();
    expect(api.setItemTag.mock.calls.at(-1)[2]).toBe(false);
    expect(input.element.value).toBe('旅');
  });

  it.each(['success', 'create-failed', 'attach-failed'])('创建并添加：%s', async outcome => {
    const input = open();
    const tag = { id: 3, name: '新标签' };
    api.createFeedTag.mockResolvedValue(tag);
    api.setItemTag.mockResolvedValue({ id: 10, tags: [tag] });
    if (outcome === 'create-failed') api.createFeedTag.mockRejectedValue(new Error('创建失败'));
    if (outcome === 'attach-failed') api.setItemTag.mockRejectedValue(new Error('写入失败'));
    await input.setValue('新标签');
    await wrapper.get('[data-test="short-feed-create-tag"]').trigger('click');
    await flushPromises();
    expect(input.element.value).toBe(outcome === 'success' ? '' : '新标签');
    expect(wrapper.vm.currentVideo.tags).toEqual(outcome === 'success' ? [tag] : []);
    expect(wrapper.vm.creatingTag).toBe(false);
  });
});
