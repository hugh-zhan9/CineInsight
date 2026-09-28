import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ PreviewTagPersonConversion: vi.fn(), ConvertTagToPerson: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import TagPersonConversionPanel from './TagPersonConversionPanel.vue';

beforeEach(() => {
  vi.resetAllMocks();
  api.PreviewTagPersonConversion.mockResolvedValue({ tag_id: 4, tag_name: '张三', video_count: 3, image_count: 2, people: [] });
  api.ConvertTagToPerson.mockResolvedValue({ person: { id: 9, display_name: '张三' }, video_count: 3, image_count: 2 });
});

describe('标签转为人物', () => {
  it('有同名人物时必须明确选择，复用选中人物并说明原标签会被删除', async () => {
    api.PreviewTagPersonConversion.mockResolvedValue({ tag_id: 4, tag_name: '张三', video_count: 3, image_count: 2, people: [
      { id: 8, display_name: '张三', original_name: '甲' }, { id: 9, display_name: '张三', original_name: '乙' }
    ] });
    const wrapper = mount(TagPersonConversionPanel, { props: { tagId: 4 } });
    await flushPromises();
    expect(wrapper.get('.btn-primary').element.disabled).toBe(true);
    expect(wrapper.text()).toContain('成功后删除原标签');
    await wrapper.get('input[value="9"]').setValue(true);
    await wrapper.get('.btn-primary').trigger('click');
    await flushPromises();
    expect(api.ConvertTagToPerson).toHaveBeenCalledWith({ tag_id: 4, tag_name: '张三', target_person_id: 9, create_new: false });
    expect(wrapper.emitted('converted')[0][0]).toMatchObject({ tag_id: 4, person: { id: 9 } });
    wrapper.unmount();
  });

  it('没有同名人物时新建，提交期间不允许重复执行或返回', async () => {
    let finish;
    api.ConvertTagToPerson.mockReturnValue(new Promise(resolve => { finish = resolve; }));
    const wrapper = mount(TagPersonConversionPanel, { props: { tagId: 4 } });
    await flushPromises();
    await wrapper.get('.btn-primary').trigger('click');
    expect(wrapper.get('.btn-secondary').element.disabled).toBe(true);
    await wrapper.get('.btn-primary').trigger('click');
    expect(api.ConvertTagToPerson).toHaveBeenCalledTimes(1);
    expect(api.ConvertTagToPerson).toHaveBeenCalledWith({ tag_id: 4, tag_name: '张三', target_person_id: 0, create_new: true });
    finish({ person: { id: 9 }, video_count: 3, image_count: 2 });
    await flushPromises();
    expect(wrapper.emitted('busy')).toEqual([[true], [false]]);
    wrapper.unmount();
  });

  it('转换失败保留选择和预览供重试，不发成功事件', async () => {
    api.ConvertTagToPerson.mockRejectedValueOnce(new Error('写入失败'));
    const wrapper = mount(TagPersonConversionPanel, { props: { tagId: 4 } });
    await flushPromises();
    await wrapper.get('.btn-primary').trigger('click');
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain('写入失败');
    expect(wrapper.get('input[value="new"]').element.checked).toBe(true);
    expect(wrapper.emitted('converted')).toBeUndefined();
    await wrapper.get('.btn-primary').trigger('click');
    await flushPromises();
    expect(wrapper.emitted('converted')).toHaveLength(1);
    wrapper.unmount();
  });

  it('预览失败时不能提交，可重试加载', async () => {
    api.PreviewTagPersonConversion.mockRejectedValueOnce(new Error('读取失败'));
    const wrapper = mount(TagPersonConversionPanel, { props: { tagId: 4 } });
    await flushPromises();
    expect(wrapper.get('.btn-primary').element.disabled).toBe(true);
    await wrapper.findAll('button').find(button => button.text() === '重试').trigger('click');
    await flushPromises();
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
    expect(wrapper.get('.btn-primary').element.disabled).toBe(false);
    wrapper.unmount();
  });
});
