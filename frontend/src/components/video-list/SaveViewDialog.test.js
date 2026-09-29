import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ SaveLibraryView: vi.fn(), UpdateSavedLibraryView: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);

import SaveViewDialog, { parseSavedViewIDs, savedViewFilter } from './SaveViewDialog.vue';

const views = [
  { id: 4, name: '收藏大文件', search_mode: 'file', keyword: 'sea', smart_view: 'favorites', tag_ids_json: '[3, 99]', person_ids_json: '[9]', min_size: 1, max_size: 0, min_height: 0, max_height: 0, min_rating: null, max_rating: 8, sort_mode: 'balanced' },
  { id: 5, name: '张三', search_mode: 'file', keyword: '', smart_view: '', tag_ids_json: '[]', person_ids_json: '[]', min_size: 0, max_size: 0, min_height: 0, max_height: 0, sort_mode: 'balanced' }
];
const currentFilter = { search_mode: 'file', keyword: '', smart_view: 'liked', tag_ids: [1], person_ids: [9], min_size: 0, max_size: 0, min_height: 0, max_height: 0, min_rating: null, max_rating: null, sort_mode: 'balanced' };

function mountDialog(props = {}) {
  return mount(SaveViewDialog, {
    props: { currentLibraryFilter: () => currentFilter, afterSaved: vi.fn().mockResolvedValue(), savedViews: views, activeViewId: 4, ...props }
  });
}

beforeEach(() => vi.clearAllMocks());

describe('保存视图弹窗（D-PC35）', () => {
  it('另存为新视图带上当前条件（含人物）', async () => {
    api.SaveLibraryView.mockResolvedValueOnce({ id: 6, name: '新视图' });
    const afterSaved = vi.fn().mockResolvedValue();
    const wrapper = mountDialog({ afterSaved });
    wrapper.vm.open();
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-test="save-view-name"]').setValue(' 新视图 ');
    await wrapper.get('[data-test="save-view-submit"]').trigger('click');
    await flushPromises();
    expect(api.SaveLibraryView).toHaveBeenCalledWith({ name: '新视图', ...currentFilter });
    expect(afterSaved).toHaveBeenCalledWith({ id: 6, name: '新视图' });
    expect(wrapper.vm.saveViewDialog.show).toBe(false);
    wrapper.unmount();
  });

  it('LIB-15 「用当前条件更新」默认选中最近应用的视图，名字可以顺便改', async () => {
    api.UpdateSavedLibraryView.mockResolvedValueOnce({ id: 4, name: '收藏大文件' });
    const wrapper = mountDialog();
    wrapper.vm.open('update');
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="save-view-target"]').element.value).toBe('4');
    expect(wrapper.vm.saveViewDialog.name).toBe('收藏大文件');
    await wrapper.get('[data-test="save-view-submit"]').trigger('click');
    await flushPromises();
    expect(api.UpdateSavedLibraryView).toHaveBeenCalledWith(4, '收藏大文件', currentFilter);
    expect(api.SaveLibraryView).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('LIB-15 「重命名」只改名字，原样保留视图里存的条件（包括已被删除的标签 ID）', async () => {
    api.UpdateSavedLibraryView.mockResolvedValueOnce({ id: 4, name: '海边' });
    const wrapper = mountDialog();
    wrapper.vm.open('rename');
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-test="save-view-name"]').setValue('海边');
    await wrapper.get('[data-test="save-view-submit"]').trigger('click');
    await flushPromises();
    expect(api.UpdateSavedLibraryView).toHaveBeenCalledWith(4, '海边', expect.objectContaining({
      keyword: 'sea', smart_view: 'favorites', tag_ids: [3, 99], person_ids: [9], min_size: 1, max_rating: 8, min_rating: null
    }));
    wrapper.unmount();
  });

  it('META-07 重名时说人话，弹窗留着可以改', async () => {
    api.UpdateSavedLibraryView.mockRejectedValueOnce(new Error('saved_view_name_taken'));
    const wrapper = mountDialog();
    wrapper.vm.open('rename');
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-test="save-view-name"]').setValue('张三');
    await wrapper.get('[data-test="save-view-submit"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toBe('已经有同名的视图了，换一个名字吧。');
    expect(wrapper.vm.saveViewDialog.show).toBe(true);
    wrapper.unmount();
  });

  it('还没有保存视图时只能新建，不显示模式切换', async () => {
    const wrapper = mountDialog({ savedViews: [], activeViewId: 0 });
    wrapper.vm.open('update');
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.saveViewDialog.mode).toBe('create');
    expect(wrapper.find('[data-test="save-view-mode-update"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('保存视图里的 ID 数组读不出来时按空处理', () => {
    expect(parseSavedViewIDs('[3, "4", 3, -1, 0]')).toEqual([3, 4]);
    expect(parseSavedViewIDs('not json')).toEqual([]);
    expect(parseSavedViewIDs(null)).toEqual([]);
    expect(savedViewFilter(views[1]).person_ids).toEqual([]);
  });
});
