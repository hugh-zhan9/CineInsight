// 回收站中心（P-030，详细设计 §2.3）：四个页签、分页、多选、结果码文案与破坏性操作的二次确认。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));

const api = vi.hoisted(() => Object.fromEntries([
  'ListTrashEntriesPage', 'GetTrashUsage', 'RestoreTrashEntries', 'PurgeTrashEntries', 'RemoveGoneTrashEntries',
  'ForceRemoveTrashRecords', 'ListHiddenImages', 'RecheckImages', 'ListStagedSources', 'TrashStagedSources', 'DeleteStagedSources'
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);

import TrashCenterDialog, { cleanTrashMessage, trashResultText } from './TrashCenterDialog.vue';

function entry(id, overrides = {}) {
  return {
    id,
    kind: 'video',
    entity_id: id + 100,
    name: `video-${id}.mp4`,
    original_path: `/v/video-${id}.mp4`,
    trash_path: `/Users/me/.Trash/video-${id}.mp4`,
    file_moved: true,
    file_size: 0,
    state: 'deleted',
    mode: 'trash',
    deleted_by: 'user',
    delete_batch_id: 'b1',
    last_error: '',
    created_at: '2026-09-01T00:00:00Z',
    put_back: false,
    claimed_by_active: false,
    original_symlink: false,
    actions: ['restore', 'purge'],
    ...overrides
  };
}

function page(items, { hasMore = false, nextCursor = 0 } = {}) {
  return { items, has_more: hasMore, next_cursor: nextCursor };
}

function batch(items) {
  const ok = items.filter(item => item.code === 'ok').length;
  return { batch_id: '', requested: items.length, succeeded: ok, failed: items.length - ok, cancelled: 0, items };
}

async function mountDialog(props = {}) {
  const wrapper = mount(TrashCenterDialog, { props: { visible: true, ...props } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  feedback.confirmAction.mockResolvedValue(true);
  api.GetTrashUsage.mockResolvedValue({
    video: { count: 3, bytes_in_trash: 2 * 1024 * 1024 * 1024, gone_count: 1, legacy_count: 1, legacy_bytes: 1024 * 1024 },
    image: { count: 2, bytes_in_trash: 0, gone_count: 0, legacy_count: 0, legacy_bytes: 0 },
    staged: { count: 1, bytes: 4096 }
  });
  api.ListTrashEntriesPage.mockResolvedValue(page([]));
  api.ListHiddenImages.mockResolvedValue({ items: [], next_cursor: 0, has_more: false });
  api.ListStagedSources.mockResolvedValue([]);
});

describe('LIB-05 回收站中心：列表、分页与用量', () => {
  it('打开即按页签分页读取，并显示废纸篓占用与「在访达中清空才会释放空间」', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(9), entry(8)], { hasMore: true, nextCursor: 8 }));
    const wrapper = await mountDialog();

    expect(api.ListTrashEntriesPage).toHaveBeenCalledWith({ kind: 'video', mode: '', deleted_by: '', query: '', cursor_id: 0, limit: 50 });
    expect(wrapper.text()).toContain('在访达中清空废纸篓才会释放空间');
    const usage = wrapper.get('[data-test="trash-usage"]').text();
    expect(usage).toContain('废纸篓中占用 2.0 GB');
    expect(usage).toContain('旧版回收站目录');
    expect(usage).toContain('1 条的文件已从废纸篓清除');
    expect(wrapper.get('[data-test="trash-tab-video"]').text()).toBe('视频（3）');
    expect(wrapper.get('[data-test="trash-tab-staged"]').text()).toBe('迁移残留（1）');

    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(7)]));
    await wrapper.get('[data-test="trash-load-more"]').trigger('click');
    await flushPromises();
    expect(api.ListTrashEntriesPage).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'video', cursor_id: 8 }));
    expect(wrapper.findAll('.trash-center-entry')).toHaveLength(3);
    expect(wrapper.find('[data-test="trash-load-more"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('按删除方式、删除人筛选与搜索都会从第一页重读', async () => {
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="trash-filter-mode"]').setValue('record_only');
    await flushPromises();
    expect(api.ListTrashEntriesPage).toHaveBeenLastCalledWith(expect.objectContaining({ mode: 'record_only', cursor_id: 0 }));

    await wrapper.get('[data-test="trash-filter-deleted-by"]').setValue('scanner');
    await flushPromises();
    expect(api.ListTrashEntriesPage).toHaveBeenLastCalledWith(expect.objectContaining({ mode: 'record_only', deleted_by: 'scanner' }));

    await wrapper.get('[data-test="trash-search"]').setValue('  假期 ');
    await wrapper.get('[data-test="trash-search"]').trigger('keydown', { key: 'Enter' });
    await flushPromises();
    expect(api.ListTrashEntriesPage).toHaveBeenLastCalledWith(expect.objectContaining({ query: '假期', cursor_id: 0 }));
    expect(wrapper.text()).toContain('没有符合条件的条目');
    wrapper.unmount();
  });

  it('条目显示文件大小与删除人，0 字节的旧记录不显示大小', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([
      entry(1, { deleted_by: 'scanner', mode: 'missing', file_moved: false, file_size: 1.5 * 1024 * 1024 * 1024, actions: ['restore'] }),
      entry(2, { deleted_by: '', file_size: 0 })
    ]));
    const wrapper = await mountDialog();
    const rows = wrapper.findAll('.trash-center-entry');
    expect(rows[0].text()).toContain('1.5 GB');
    expect(rows[0].text()).toContain('删除人：程序（扫描）');
    expect(rows[1].text()).toContain('删除人：历史未知');
    expect(rows[1].text()).not.toContain(' B ·');
    wrapper.unmount();
  });

  it('IMG-02 图片页签与视频对称：同一套接口，kind=image', async () => {
    api.ListTrashEntriesPage.mockResolvedValue(page([entry(5, { kind: 'image', name: 'a.jpg', mode: 'record_only', file_moved: false, actions: ['restore'] })]));
    api.RestoreTrashEntries.mockResolvedValueOnce(batch([{ id: 5, code: 'ok' }]));
    const wrapper = await mountDialog({ initialTab: 'image' });

    expect(api.ListTrashEntriesPage).toHaveBeenCalledWith(expect.objectContaining({ kind: 'image' }));
    // 只删记录的条目：恢复就是「允许重新收录」。
    const restore = wrapper.get('[data-test="trash-entry-restore"]');
    expect(restore.text()).toBe('允许重新收录');
    expect(wrapper.text()).toContain('扫描到同一个文件不会再收录');
    await restore.trigger('click');
    await flushPromises();

    expect(api.RestoreTrashEntries).toHaveBeenCalledWith('image', [5]);
    expect(wrapper.emitted('restored')[0]).toEqual([{ kind: 'image', entityIDs: [105] }]);
    wrapper.unmount();
  });
});

describe('LIB-12 多选与批量操作', () => {
  it('「恢复所选」一次恢复所选条目，只把成功的移出列表并报告失败原因', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(1), entry(2), entry(3, { state: 'file_gone', actions: ['remove_record'] })]));
    api.RestoreTrashEntries.mockResolvedValueOnce(batch([
      { id: 1, code: 'ok' },
      { id: 2, code: 'path_occupied', message: '原位置已收录了新文件' }
    ]));
    const wrapper = await mountDialog();

    await wrapper.get('[data-test="trash-select-all"]').setValue(true);
    expect(wrapper.get('[data-test="trash-bulk-restore"]').text()).toBe('恢复所选（2）');
    expect(wrapper.get('[data-test="trash-bulk-remove"]').text()).toBe('移除已清除记录（1）');
    await wrapper.get('[data-test="trash-bulk-restore"]').trigger('click');
    await flushPromises();

    expect(api.RestoreTrashEntries).toHaveBeenCalledWith('video', [1, 2]);
    expect(wrapper.findAll('.trash-center-entry').map(row => row.attributes('data-test'))).toEqual(['trash-entry-2', 'trash-entry-3']);
    expect(wrapper.get('[data-test="trash-entry-2"]').text()).toContain('恢复失败：原位置已收录了新文件');
    expect(wrapper.get('[data-test="trash-notice"]').text()).toContain('成功 1 项，失败 1 项');
    expect(wrapper.emitted('restored')[0]).toEqual([{ kind: 'video', entityIDs: [101] }]);
    wrapper.unmount();
  });

  it('LIB-05 永久删除所选要二次确认；取消时不调用 PurgeTrashEntries', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(1), entry(2, { mode: 'record_only', file_moved: false, actions: ['restore'] })]));
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="trash-select-all"]').setValue(true);
    expect(wrapper.get('[data-test="trash-bulk-purge"]').text()).toBe('永久删除所选（1）');

    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="trash-bulk-purge"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ danger: true, confirmText: '永久删除' }));
    expect(api.PurgeTrashEntries).not.toHaveBeenCalled();

    api.PurgeTrashEntries.mockResolvedValueOnce(batch([{ id: 1, code: 'ok' }]));
    await wrapper.get('[data-test="trash-bulk-purge"]').trigger('click');
    await flushPromises();
    // 只删记录的条目没有 purge 动作，不会被一起清除。
    expect(api.PurgeTrashEntries).toHaveBeenCalledWith('video', [1]);
    expect(wrapper.find('[data-test="trash-entry-1"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('LIB-05 单条永久删除同样先确认；取消不调用', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(4)]));
    const wrapper = await mountDialog();
    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="trash-entry-purge"]').trigger('click');
    await flushPromises();
    expect(api.PurgeTrashEntries).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('移除已清除记录要确认；取消时不调用 RemoveGoneTrashEntries', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(3, { state: 'file_gone', actions: ['remove_record'] })]));
    const wrapper = await mountDialog();
    expect(wrapper.text()).toContain('文件已从废纸篓清除');

    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="trash-entry-remove"]').trigger('click');
    await flushPromises();
    expect(api.RemoveGoneTrashEntries).not.toHaveBeenCalled();

    api.RemoveGoneTrashEntries.mockResolvedValueOnce(batch([{ id: 3, code: 'ok' }]));
    await wrapper.get('[data-test="trash-entry-remove"]').trigger('click');
    await flushPromises();
    expect(api.RemoveGoneTrashEntries).toHaveBeenCalledWith('video', [3]);
    expect(wrapper.find('[data-test="trash-entry-3"]').exists()).toBe(false);
    wrapper.unmount();
  });
});

describe('LIB-05 特殊条目的出口与说明', () => {
  it('put_back 的行提示「已放回原处」，只提供恢复', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(1, { put_back: true, actions: ['restore'] })]));
    const wrapper = await mountDialog();
    const row = wrapper.get('[data-test="trash-entry-1"]');
    expect(row.text()).toContain('已放回原处');
    expect(row.find('[data-test="trash-entry-restore"]').exists()).toBe(true);
    expect(row.find('[data-test="trash-entry-purge"]').exists()).toBe(false);
    expect(row.find('[data-test="trash-entry-remove"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('put_back 的行清除时若返回 not_purgeable，照实显示「请改用恢复」且不给「仍然移除记录」', async () => {
    // 列表判定之后文件才被放回：清除按钮还在，后端拒绝。
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(1, { mode: 'legacy_trash' })]));
    api.PurgeTrashEntries.mockResolvedValueOnce(batch([{ id: 1, code: 'not_purgeable', message: '文件已被放回原处，请改用恢复' }]));
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="trash-entry-purge"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="trash-entry-1"]').text()).toContain('永久删除失败：文件已被放回原处，请改用恢复');
    expect(wrapper.find('[data-test="trash-entry-force"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('claimed_by_active 的行只提供「移除记录」并说明不动文件', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(2, { claimed_by_active: true, actions: ['remove_record'] })]));
    const wrapper = await mountDialog();
    const row = wrapper.get('[data-test="trash-entry-2"]');
    expect(row.text()).toContain('原位置已由片库中的另一条记录收录，只能移除这条旧记录（不动文件）');
    expect(row.findAll('.trash-center-entry__actions button').map(button => button.text())).toEqual(['移除记录']);
    wrapper.unmount();
  });

  it('original_symlink=true 的行说明原位置是符号链接，不提供恢复', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([
      entry(3, { original_symlink: true, actions: [] }),
      entry(4, { original_symlink: true, actions: ['purge'] })
    ]));
    const wrapper = await mountDialog();
    const blocked = wrapper.get('[data-test="trash-entry-3"]');
    expect(blocked.text()).toContain('原位置是符号链接');
    expect(blocked.text()).toContain('也不提供永久删除');
    expect(blocked.find('[data-test="trash-entry-restore"]').exists()).toBe(false);
    const purgeable = wrapper.get('[data-test="trash-entry-4"]');
    expect(purgeable.text()).toContain('原位置是符号链接');
    expect(purgeable.text()).not.toContain('也不提供永久删除');
    expect(purgeable.find('[data-test="trash-entry-purge"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('pending_move 等中断状态提供「恢复原状态」并显示上次失败原因', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(5, { state: 'pending_move', last_error: '文件位置未知', actions: ['restore'] })]));
    const wrapper = await mountDialog();
    const row = wrapper.get('[data-test="trash-entry-5"]');
    expect(row.text()).toContain('删除曾中断，可恢复原状态');
    expect(row.text()).toContain('上次处理失败：文件位置未知');
    expect(row.get('[data-test="trash-entry-restore"]').text()).toBe('恢复原状态');
    wrapper.unmount();
  });
});

describe('LIB-05 仍然移除记录（不动文件）', () => {
  async function offlineRow(overrides = {}) {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(6, overrides)]));
    api.PurgeTrashEntries.mockResolvedValueOnce(batch([{ id: 6, code: 'volume_offline', message: '文件所在磁盘当前不可访问，未做任何改动' }]));
    const wrapper = await mountDialog();
    expect(wrapper.find('[data-test="trash-entry-force"]').exists()).toBe(false);
    await wrapper.get('[data-test="trash-entry-purge"]').trigger('click');
    await flushPromises();
    return wrapper;
  }

  it('清除因磁盘离线被拒后才出现；必须输入「移除记录」才调用，取消时不调用', async () => {
    const wrapper = await offlineRow();
    expect(wrapper.get('[data-test="trash-entry-6"]').text()).toContain('所在磁盘未连接或不可访问');
    const force = wrapper.get('[data-test="trash-entry-force"]');
    expect(force.text()).toBe('仍然移除记录（不动文件）');

    await force.trigger('click');
    expect(wrapper.text()).toContain('不做任何文件操作');
    await wrapper.get('[data-test="trash-force-cancel"]').trigger('click');
    expect(wrapper.find('[data-test="trash-force-input"]').exists()).toBe(false);
    expect(api.ForceRemoveTrashRecords).not.toHaveBeenCalled();

    await wrapper.get('[data-test="trash-entry-force"]').trigger('click');
    const confirm = wrapper.get('[data-test="trash-force-confirm"]');
    expect(confirm.attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="trash-force-input"]').setValue('移除');
    expect(wrapper.get('[data-test="trash-force-confirm"]').attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="trash-force-confirm"]').trigger('click');
    // 按钮置灰之外，确认函数本身也核对文案：输错时不调用。
    await wrapper.vm.confirmForceRemove();
    await flushPromises();
    expect(api.ForceRemoveTrashRecords).not.toHaveBeenCalled();

    api.ForceRemoveTrashRecords.mockResolvedValueOnce(batch([{ id: 6, code: 'ok' }]));
    await wrapper.get('[data-test="trash-force-input"]').setValue('移除记录');
    await wrapper.get('[data-test="trash-force-confirm"]').trigger('click');
    await flushPromises();
    expect(api.ForceRemoveTrashRecords).toHaveBeenCalledWith('video', [6], '移除记录');
    expect(wrapper.find('[data-test="trash-entry-6"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('确认框开着时按 Esc 只关确认框，回收站不跟着关', async () => {
    const wrapper = await offlineRow();
    await wrapper.get('[data-test="trash-entry-force"]').trigger('click');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted('close')).toBeUndefined();
    expect(wrapper.find('[data-test="trash-force-input"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('record_only 的条目恢复失败也不提供「仍然移除记录」', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(7, { mode: 'record_only', file_moved: false, actions: ['restore'] })]));
    api.RestoreTrashEntries.mockResolvedValueOnce(batch([{ id: 7, code: 'volume_offline' }]));
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="trash-entry-restore"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="trash-entry-7"]').text()).toContain('恢复失败');
    expect(wrapper.find('[data-test="trash-entry-force"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('扫描时已消失的条目恢复失败后提供出口，否则它永远卡在回收站里', async () => {
    api.ListTrashEntriesPage.mockResolvedValueOnce(page([entry(8, { mode: 'missing', file_moved: false, deleted_by: 'scanner', actions: ['restore'] })]));
    api.RestoreTrashEntries.mockResolvedValueOnce(batch([{ id: 8, code: 'error', message: '原文件不可用，无法恢复记录' }]));
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="trash-entry-restore"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="trash-entry-force"]').exists()).toBe(true);

    // 批量入口只数符合条件的所选项。
    await wrapper.get('[data-test="trash-select-all"]').setValue(true);
    expect(wrapper.get('[data-test="trash-bulk-force"]').text()).toContain('（1）');
    wrapper.unmount();
  });
});

describe('LIB-05 结果码文案', () => {
  it('中文、不含绝对路径，维护模式的英文报错会被翻译', () => {
    expect(trashResultText('volume_offline', '')).toBe('所在磁盘未连接或不可访问，未做任何改动');
    expect(trashResultText('permission_denied', '')).toContain('没有权限');
    expect(trashResultText('path_occupied', '')).toBe('原位置已收录了新文件');
    expect(trashResultText('path_occupied', '原位置是符号链接或不是普通文件，未做任何改动')).toContain('符号链接');
    expect(trashResultText('not_purgeable', '文件已被放回原处，请改用恢复')).toBe('文件已被放回原处，请改用恢复');
    expect(trashResultText('identity_mismatch', '')).toContain('可能已被替换');
    expect(trashResultText('error', 'database is in maintenance mode')).toBe('数据库正在维护（恢复或切换后端），暂时不能操作');
    expect(cleanTrashMessage('检查失败: stat /Volumes/盘/电影/a.mp4: no such file')).not.toContain('/Volumes');
  });

  it('LIB-05 已删除的结果码 not_restorable 不再有专门文案；墓碑按「回收站条目不存在」（error + 服务端文案）显示', () => {
    expect(trashResultText('not_restorable', '')).toBe('操作失败');
    expect(trashResultText('not_restorable', '该条目当前不可恢复')).toBe('该条目当前不可恢复');
    expect(trashResultText('error', '回收站条目不存在: 7')).toBe('回收站条目不存在: 7');
  });
});

describe('IMG-09 扫描隐藏页签', () => {
  it('列出原因，重新检查所选后报告结果并通知宿主刷新', async () => {
    api.ListHiddenImages.mockResolvedValueOnce({
      items: [
        { id: 1, name: 'a.jpg', path: '/p/a.jpg', directory: '/p', reason: 'offline_root' },
        { id: 2, name: 'b.jpg', path: '/p/b.jpg', directory: '/p', reason: 'missing_file' },
        { id: 3, name: 'c.jpg', path: '/old/c.jpg', directory: '/old', reason: 'removed_root' }
      ],
      next_cursor: 3,
      has_more: false
    });
    const wrapper = await mountDialog({ initialTab: 'hidden' });

    expect(api.ListHiddenImages).toHaveBeenCalledWith(0, 50);
    expect(wrapper.get('[data-test="hidden-entry-1"]').text()).toContain('所在磁盘未连接');
    expect(wrapper.get('[data-test="hidden-entry-2"]').text()).toContain('扫描时文件不在原处');
    expect(wrapper.get('[data-test="hidden-entry-3"]').text()).toContain('所在目录已不在扫描范围');

    api.RecheckImages.mockResolvedValueOnce({ added: 0, restored: 2, relocated: 0, removed: 0, skipped: 0, errors: [] });
    await wrapper.get('[data-test="hidden-select-all"]').setValue(true);
    await wrapper.get('[data-test="hidden-recheck"]').trigger('click');
    await flushPromises();

    expect(api.RecheckImages).toHaveBeenCalledWith([1, 2, 3]);
    expect(wrapper.get('[data-test="hidden-notice"]').text()).toContain('恢复 2 张');
    expect(wrapper.emitted('restored')[0]).toEqual([{ kind: 'image', entityIDs: [] }]);
    expect(api.ListHiddenImages).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });
});

describe('LIB-11 迁移残留页签', () => {
  const staged = [
    { id: 1, original_path: '/v/a.mp4', staged_path: '/v/.a.mp4.cineinsight-migrating-ab', size: 1024, state: 'pending', created_at: '2026-09-02T00:00:00Z' },
    { id: 2, original_path: '/v/b.mp4', staged_path: '/v/.b.mp4.cineinsight-migrating-cd', size: 2048, state: 'pending', created_at: '2026-09-02T00:00:00Z' }
  ];

  it('列出原位置、大小与时间；移到废纸篓直接执行并刷新', async () => {
    api.ListStagedSources.mockResolvedValueOnce(staged).mockResolvedValueOnce([staged[1]]);
    api.TrashStagedSources.mockResolvedValueOnce(batch([{ id: 1, code: 'ok' }]));
    const wrapper = await mountDialog({ initialTab: 'staged' });

    expect(wrapper.get('[data-test="staged-entry-1"]').text()).toContain('原位置：/v/a.mp4');
    expect(wrapper.get('[data-test="staged-entry-1"]').text()).toContain('1.0 KB');
    expect(wrapper.get('[data-test="trash-usage"]').text()).toContain('迁移残留 1 项');

    await wrapper.get('[data-test="staged-entry-1"] input').setValue(true);
    await wrapper.get('[data-test="staged-trash"]').trigger('click');
    await flushPromises();
    expect(api.TrashStagedSources).toHaveBeenCalledWith([1]);
    expect(wrapper.get('[data-test="staged-notice"]').text()).toContain('已移到废纸篓 1 项');
    expect(wrapper.find('[data-test="staged-entry-1"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('永久删除需二次确认；取消时不调用 DeleteStagedSources', async () => {
    api.ListStagedSources.mockResolvedValue(staged);
    const wrapper = await mountDialog({ initialTab: 'staged' });
    await wrapper.get('[data-test="staged-select-all"]').setValue(true);

    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="staged-delete"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ danger: true }));
    expect(api.DeleteStagedSources).not.toHaveBeenCalled();

    api.DeleteStagedSources.mockResolvedValueOnce({ cleaned: 1, failed: [{ id: 2, error: '暂存文件的大小与登记时不一致，已拒绝删除' }] });
    await wrapper.get('[data-test="staged-delete"]').trigger('click');
    await flushPromises();
    expect(api.DeleteStagedSources).toHaveBeenCalledWith([1, 2]);
    expect(wrapper.get('[data-test="staged-notice"]').text()).toContain('暂存文件的大小与登记时不一致');
    wrapper.unmount();
  });
});

describe('LIB-05 关闭与过期响应', () => {
  it('关闭后仍在路上的列表响应被丢弃', async () => {
    let resolveList;
    api.ListTrashEntriesPage.mockReturnValueOnce(new Promise(resolve => { resolveList = resolve; }));
    const wrapper = await mountDialog();
    await wrapper.setProps({ visible: false });
    resolveList(page([entry(1)]));
    await flushPromises();
    await wrapper.setProps({ visible: true });
    await flushPromises();
    // 第二次打开读到的是空列表（默认桩），而不是上一次迟到的那一页。
    expect(wrapper.findAll('.trash-center-entry')).toHaveLength(0);
    wrapper.unmount();
  });
});
