import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  PreviewMetadataBatchEdit: vi.fn(),
  ApplyMetadataBatchEdit: vi.fn(),
  UndoMetadataBatchEdit: vi.fn(),
  ListPeople: vi.fn(),
  ListCollections: vi.fn(),
  LogFrontend: vi.fn()
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import MetadataBatchEditDialog from './MetadataBatchEditDialog.vue';

const tags = [
  { id: 1, name: '剧情', automatic_kind: '' },
  { id: 2, name: '短视频', automatic_kind: 'short_video' },
  { id: 3, name: '剧集', automatic_kind: '' }
];
const libraryFilter = { keyword: '海', tag_ids: [], smart_view: '' };

const wrappers = [];
function mountDialog(props = {}) {
  const wrapper = mount(MetadataBatchEditDialog, { props: { kind: 'video', selectedIds: [11, 12], filter: libraryFilter, tags, ...props } });
  wrappers.push(wrapper);
  return wrapper;
}

async function setYear(wrapper, year, index = 0) {
  const row = wrapper.get(`[data-test="batch-edit-op-${index}"]`);
  await row.get('[data-test="batch-edit-field"]').setValue('year');
  await row.get('[data-test="batch-edit-action"]').setValue('set');
  await row.get('[data-test="batch-edit-year"]').setValue(String(year));
}

beforeEach(() => {
  vi.clearAllMocks();
  api.LogFrontend.mockResolvedValue(undefined);
  api.PreviewMetadataBatchEdit.mockResolvedValue({
    kind: 'video', target_count: 2, changed_count: 1,
    ops: [{ field: 'year', action: 'set', value: 2001, ids: [], names: [], changes: 1 }]
  });
  api.ApplyMetadataBatchEdit.mockResolvedValue({
    batch_id: 7, kind: 'video', target_count: 2, processed: 2, changed_count: 1, cancelled: false,
    ops: [{ field: 'year', action: 'set', value: 2001, ids: [], names: [], changes: 1 }]
  });
  api.UndoMetadataBatchEdit.mockResolvedValue({ batch_id: 7, restored: 1, changed_since: 0, cancelled: false });
});
afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
});

describe('批量编辑对话框（D-MC-04 / D-MC-05）', () => {
  it('按所选条目预览：每项显示会改变几项，预览前不能执行', async () => {
    const wrapper = mountDialog();
    expect(wrapper.get('[data-test="batch-edit-apply"]').attributes('disabled')).toBeDefined();
    await setYear(wrapper, 2001);
    await wrapper.get('[data-test="batch-edit-preview-btn"]').trigger('click');
    await flushPromises();
    expect(api.PreviewMetadataBatchEdit).toHaveBeenCalledWith({
      kind: 'video', target: { ids: [11, 12] },
      ops: [{ field: 'year', action: 'set', value: 2001, ids: [] }]
    });
    expect(wrapper.get('[data-test="batch-edit-preview"]').text()).toContain('共 2 项，其中 1 项会改变');
    expect(wrapper.get('[data-test="batch-edit-op-count"]').text()).toBe('会改变 1 项');
    expect(wrapper.get('[data-test="batch-edit-apply"]').attributes('disabled')).toBeUndefined();
    // 改了操作，旧预览作废，要重新预览才能执行。
    await wrapper.get('[data-test="batch-edit-year"]').setValue('2002');
    expect(wrapper.find('[data-test="batch-edit-preview"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="batch-edit-apply"]').attributes('disabled')).toBeDefined();
  });

  it('选「当前筛选的全部结果」时把筛选交给后端；没有筛选时不提供这个选项', async () => {
    const wrapper = mountDialog();
    await setYear(wrapper, 2001);
    await wrapper.get('[data-test="batch-edit-scope-filter"]').setValue(true);
    await wrapper.get('[data-test="batch-edit-preview-btn"]').trigger('click');
    await flushPromises();
    expect(api.PreviewMetadataBatchEdit.mock.calls[0][0].target).toEqual({ ids: [], filter: libraryFilter });

    const image = mountDialog({ kind: 'image', filter: { keyword: 'a' } });
    await image.get('[data-test="batch-edit-scope-filter"]').setValue(true);
    await image.get('[data-test="batch-edit-field"]').setValue('rating');
    await image.get('[data-test="batch-edit-action"]').setValue('clear');
    await image.get('[data-test="batch-edit-preview-btn"]').trigger('click');
    await flushPromises();
    expect(api.PreviewMetadataBatchEdit.mock.calls[1][0]).toMatchObject({ kind: 'image', target: { ids: [], image_filter: { keyword: 'a' } } });

    const noFilter = mountDialog({ filter: null });
    expect(noFilter.find('[data-test="batch-edit-scope-filter"]').exists()).toBe(false);
  });

  it('可加多行操作；标签候选不含自动标签，点选后随请求发出', async () => {
    const wrapper = mountDialog();
    await setYear(wrapper, 1999);
    await wrapper.get('[data-test="batch-edit-add-op"]').trigger('click');
    const row = wrapper.get('[data-test="batch-edit-op-1"]');
    await row.get('[data-test="batch-edit-field"]').setValue('tag');
    await row.get('[data-test="batch-edit-picker-input"]').trigger('focus');
    const options = row.findAll('[data-test^="batch-edit-option-"]').map(option => option.text());
    expect(options).toEqual(['剧情', '剧集']);
    await row.get('[data-test="batch-edit-option-3"]').trigger('mousedown');
    expect(row.get('[data-test="batch-edit-chosen-3"]').text()).toContain('剧集');
    await row.get('[data-test="batch-edit-action"]').setValue('remove');
    await wrapper.get('[data-test="batch-edit-preview-btn"]').trigger('click');
    await flushPromises();
    expect(api.PreviewMetadataBatchEdit.mock.calls[0][0].ops).toEqual([
      { field: 'year', action: 'set', value: 1999, ids: [] },
      { field: 'tag', action: 'remove', value: null, ids: [3] }
    ]);
    await wrapper.get('[data-test="batch-edit-op-1"] [data-test="batch-edit-remove-op"]').trigger('click');
    expect(wrapper.find('[data-test="batch-edit-op-1"]').exists()).toBe(false);
  });

  it('人物候选按关键字查，作品集同理', async () => {
    api.ListPeople.mockResolvedValue([{ person: { id: 5, display_name: '张三' } }]);
    const wrapper = mountDialog();
    await wrapper.get('[data-test="batch-edit-field"]').setValue('person');
    await wrapper.get('[data-test="batch-edit-picker-input"]').setValue('张');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenLastCalledWith('张', '', 0, 20);
    await wrapper.get('[data-test="batch-edit-option-5"]').trigger('mousedown');
    expect(wrapper.get('[data-test="batch-edit-chosen-5"]').text()).toContain('张三');
  });

  it('执行后显示结果，可撤销；撤销后告诉用户恢复了几处', async () => {
    const wrapper = mountDialog();
    await setYear(wrapper, 2001);
    await wrapper.get('[data-test="batch-edit-preview-btn"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="batch-edit-apply"]').trigger('click');
    await flushPromises();
    expect(api.ApplyMetadataBatchEdit).toHaveBeenCalledTimes(1);
    expect(wrapper.emitted('applied')[0][0].batch_id).toBe(7);
    expect(wrapper.get('[data-test="batch-edit-result"]').text()).toContain('2 项里 1 项有改动');
    await wrapper.get('[data-test="batch-edit-undo"]').trigger('click');
    await flushPromises();
    expect(api.UndoMetadataBatchEdit).toHaveBeenCalledWith(7);
    expect(wrapper.emitted('undone')).toHaveLength(1);
    expect(wrapper.get('[data-test="batch-edit-undo-result"]').text()).toBe('已撤销：恢复 1 处。');
    expect(wrapper.find('[data-test="batch-edit-undo"]').exists()).toBe(false);
  });

  it('视频提示 NFO 会填回清空的字段；图片不提示', () => {
    expect(mountDialog().get('[data-test="batch-edit-nfo-note"]').text()).toContain('NFO');
    expect(mountDialog({ kind: 'image' }).find('[data-test="batch-edit-nfo-note"]').exists()).toBe(false);
  });

  it('另一个批量编辑在跑时给出可读的提示', async () => {
    api.PreviewMetadataBatchEdit.mockRejectedValue(new Error('batch_edit_busy'));
    const wrapper = mountDialog();
    await setYear(wrapper, 2001);
    await wrapper.get('[data-test="batch-edit-preview-btn"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="batch-edit-error"]').text()).toContain('另一个批量编辑或撤销正在进行');
  });
});
