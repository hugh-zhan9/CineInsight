import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ GetAllTags: vi.fn(), AddTagToVideo: vi.fn(), CreateTag: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
import Picker from './DetailTagPicker.vue';
enableAutoUnmount(afterEach);
const tag = (id, name) => ({ id, name });
beforeEach(() => { vi.resetAllMocks(); api.GetAllTags.mockResolvedValue([tag(1, '已有'), tag(2, '剧情')]); api.AddTagToVideo.mockResolvedValue(); });
async function open() { const wrapper = mount(Picker, { props: { videoId: 5, tags: [tag(1, '已有')] }, global: { stubs: { BasePopover: { template: '<div><slot /></div>' } } } }); await wrapper.get('button').trigger('click'); await flushPromises(); return wrapper; }
describe('detail tag picker', () => {
  it('filters linked tags, searches and adds an existing tag once', async () => {
    const wrapper = await open();
    expect(wrapper.findAll('.ui-tag-pill').map(item => item.text())).toEqual(['剧情']);
    await wrapper.get('input').setValue('剧');
    let resolve;
    api.AddTagToVideo.mockReturnValueOnce(new Promise(ok => { resolve = ok; }));
    await wrapper.get('.ui-tag-pill').trigger('click'); await wrapper.get('.ui-tag-pill').trigger('click');
    expect(api.AddTagToVideo).toHaveBeenCalledTimes(1);
    resolve(); await flushPromises();
    expect(wrapper.emitted('added')).toEqual([[{ videoId: 5, tag: tag(2, '剧情') }]]);
  });
  it('keeps a created tag after association failure and retries without creating again', async () => {
    const wrapper = await open();
    api.CreateTag.mockResolvedValue(tag(3, '新标签'));
    api.AddTagToVideo.mockRejectedValueOnce(new Error('写入失败'));
    await wrapper.get('input').setValue('新标签');
    await wrapper.get('[data-test="detail-create-tag"]').trigger('click'); await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain('写入失败');
    expect(wrapper.emitted('added')).toBeUndefined();
    await wrapper.get('.ui-tag-pill').trigger('click'); await flushPromises();
    expect(api.CreateTag).toHaveBeenCalledTimes(1);
    expect(api.AddTagToVideo).toHaveBeenLastCalledWith(5, 3);
    expect(wrapper.emitted('added')[0][0].tag.id).toBe(3);
  });
  it('shows load failure with retry and prevents creation with an incomplete list', async () => {
    api.GetAllTags.mockRejectedValueOnce(new Error('读取失败'));
    const wrapper = await open();
    await wrapper.get('input').setValue('新');
    expect(wrapper.find('[data-test="detail-create-tag"]').exists()).toBe(false);
    await wrapper.get('.btn-link').trigger('click'); await flushPromises();
    expect(wrapper.find('[data-test="detail-create-tag"]').exists()).toBe(true);
  });
});
