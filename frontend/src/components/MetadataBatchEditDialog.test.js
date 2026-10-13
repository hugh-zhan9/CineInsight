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
import { feedbackState, resetFeedback, runToastAction } from '../utils/feedback.js';
import * as recentOperations from '../utils/recentOperations.js';

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
  resetFeedback();
  recentOperations.clear();
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

  it('没有选中时只有「当前筛选的全部结果」；列表折叠了版本时说明总数含折叠起来的其他版本', async () => {
    const wrapper = mountDialog({ selectedIds: [], collapsedVersions: true });
    expect(wrapper.find('[data-test="batch-edit-scope-selected"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="batch-edit-scope-filter"]').element.checked).toBe(true);
    await setYear(wrapper, 2001);
    await wrapper.get('[data-test="batch-edit-preview-btn"]').trigger('click');
    await flushPromises();
    expect(api.PreviewMetadataBatchEdit.mock.calls[0][0].target).toEqual({ ids: [], filter: libraryFilter });
    expect(wrapper.get('[data-test="batch-edit-preview"]').text()).toContain('共 2 项（含折叠起来的其他版本），其中 1 项会改变');

    // 按所选执行时不涉及折叠的版本，不加这句。
    const selected = mountDialog({ collapsedVersions: true });
    await setYear(selected, 2001);
    await selected.get('[data-test="batch-edit-preview-btn"]').trigger('click');
    await flushPromises();
    expect(selected.get('[data-test="batch-edit-preview"]').text()).not.toContain('折叠');
  });

  it('有「移出作品集」时说明撤销会把视频加回作品集末尾', async () => {
    const wrapper = mountDialog();
    expect(wrapper.find('[data-test="batch-edit-collection-note"]').exists()).toBe(false);
    await wrapper.get('[data-test="batch-edit-field"]').setValue('collection');
    await wrapper.get('[data-test="batch-edit-action"]').setValue('remove');
    expect(wrapper.get('[data-test="batch-edit-collection-note"]').text()).toContain('加回作品集末尾');
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

// 可撤销的标准出口（D-UD-07、D-UD-09）：执行成功后出带「撤销」的全局提示，同一条记进任务中心「最近操作」；
// 提示、最近操作与结果页的「撤销」是同一批，哪边先撤都只调一次 UndoMetadataBatchEdit。
describe('批量编辑的撤销入口（D-UD-09）', () => {
  async function applyYear(wrapper) {
    await setYear(wrapper, 2001);
    await wrapper.get('[data-test="batch-edit-preview-btn"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="batch-edit-apply"]').trigger('click');
    await flushPromises();
  }
  const undoToast = () => feedbackState.toasts.find(toast => toast.action?.label === '撤销');

  it('执行成功后出带「撤销」的提示并记进最近操作；从提示撤销调用既有接口并更新结果页', async () => {
    const wrapper = mountDialog();
    await applyYear(wrapper);
    const toast = undoToast();
    expect(toast.message).toBe('已批量编辑 1 项视频');
    expect(recentOperations.list().map(item => item.label)).toEqual(['批量编辑视频（1 项）']);

    expect(await runToastAction(toast.id)).toBe(true);
    await flushPromises();
    expect(api.UndoMetadataBatchEdit).toHaveBeenCalledTimes(1);
    expect(api.UndoMetadataBatchEdit).toHaveBeenCalledWith(7);
    expect(wrapper.emitted('undone')).toHaveLength(1);
    expect(wrapper.get('[data-test="batch-edit-undo-result"]').text()).toBe('已撤销：恢复 1 处。');
    expect(wrapper.find('[data-test="batch-edit-undo"]').exists()).toBe(false);
    expect(recentOperations.list()).toEqual([]);
    expect(feedbackState.toasts.map(item => item.message)).toContain('已撤销：恢复 1 处。');
  });

  it('结果页已经撤销过：最近操作里再撤销不再调接口，记录随之移除', async () => {
    const wrapper = mountDialog();
    await applyYear(wrapper);
    await wrapper.get('[data-test="batch-edit-undo"]').trigger('click');
    await flushPromises();
    expect(api.UndoMetadataBatchEdit).toHaveBeenCalledTimes(1);

    const [entry] = recentOperations.list();
    expect(await recentOperations.undo(entry.id)).toBe(true);
    expect(api.UndoMetadataBatchEdit).toHaveBeenCalledTimes(1);
    expect(recentOperations.list()).toEqual([]);
  });

  it('对话框关掉后从「最近操作」撤销：接口照常只调一次，宿主照样收到 undone', async () => {
    const onUndone = vi.fn();
    const wrapper = mountDialog({ onUndone });
    await applyYear(wrapper);
    wrappers.splice(wrappers.indexOf(wrapper), 1);
    wrapper.unmount();

    const [entry] = recentOperations.list();
    expect(await recentOperations.undo(entry.id)).toBe(true);
    expect(api.UndoMetadataBatchEdit).toHaveBeenCalledTimes(1);
    expect(api.UndoMetadataBatchEdit).toHaveBeenCalledWith(7);
    expect(onUndone).toHaveBeenCalledTimes(1);
    expect(onUndone).toHaveBeenCalledWith(expect.objectContaining({ batch_id: 7, restored: 1 }));
    expect(recentOperations.list()).toEqual([]);
  });

  // M-4：这批已经在别处（完整度页的最近批量编辑）撤销过了：后端回 batch_already_undone。当作撤销完成——
  // 「最近操作」移除这一条并说一声，不再每次点都报错；结果页也不再给「撤销」。
  it('这批在别处已经撤销过：从「最近操作」撤销时提示「这次批量编辑已经撤销过了」，记录移除', async () => {
    const wrapper = mountDialog();
    await applyYear(wrapper);
    api.UndoMetadataBatchEdit.mockRejectedValueOnce('batch_already_undone');
    const [entry] = recentOperations.list();
    expect(await recentOperations.undo(entry.id)).toBe(true);
    await flushPromises();
    expect(api.UndoMetadataBatchEdit).toHaveBeenCalledWith(7);
    expect(recentOperations.list()).toEqual([]);
    expect(feedbackState.toasts.map(item => item.message)).toContain('这次批量编辑已经撤销过了。');
    expect(feedbackState.toasts.filter(item => item.level === 'error')).toEqual([]);
    expect(wrapper.find('[data-test="batch-edit-undo"]').exists()).toBe(false);
  });

  it('撤销时别的错误照旧报「撤销失败」，记录留着', async () => {
    const wrapper = mountDialog();
    await applyYear(wrapper);
    api.UndoMetadataBatchEdit.mockRejectedValueOnce('batch_edit_busy');
    expect(await runToastAction(undoToast().id)).toBe(false);
    expect(feedbackState.toasts.filter(item => item.level === 'error').map(item => item.message))
      .toEqual(['撤销失败：另一个批量编辑或撤销正在进行，请稍后再试。']);
    expect(recentOperations.list()).toHaveLength(1);
    wrapper.unmount();
    wrappers.splice(wrappers.indexOf(wrapper), 1);
  });

  it('撤销中途停止时最近操作留着这一条，提示说明可以再撤销', async () => {
    api.UndoMetadataBatchEdit.mockResolvedValueOnce({ batch_id: 7, restored: 1, changed_since: 0, cancelled: true });
    const wrapper = mountDialog();
    await applyYear(wrapper);
    expect(await runToastAction(undoToast().id)).toBe(false);
    expect(recentOperations.list()).toHaveLength(1);
    expect(feedbackState.toasts.at(-1)).toMatchObject({ level: 'error', message: '撤销失败：撤销中途停止：已恢复 1 处，可以再点撤销继续。' });
    expect(wrapper.find('[data-test="batch-edit-undo"]').exists()).toBe(true);
  });

  it('撤销接口报错时给可读的原因，不露错误码，记录留着', async () => {
    api.UndoMetadataBatchEdit.mockRejectedValueOnce(new Error('batch_edit_busy'));
    const wrapper = mountDialog();
    await applyYear(wrapper);
    expect(await runToastAction(undoToast().id)).toBe(false);
    expect(feedbackState.toasts.at(-1).message).toBe('撤销失败：另一个批量编辑或撤销正在进行，请稍后再试。');
    expect(recentOperations.list()).toHaveLength(1);
  });

  it('什么也没改（没有批次）时不出撤销提示，也不记最近操作', async () => {
    api.ApplyMetadataBatchEdit.mockResolvedValueOnce({ batch_id: 0, kind: 'video', target_count: 2, processed: 2, changed_count: 0, cancelled: false, ops: [] });
    const wrapper = mountDialog();
    await applyYear(wrapper);
    expect(undoToast()).toBeUndefined();
    expect(recentOperations.list()).toEqual([]);
  });
});
