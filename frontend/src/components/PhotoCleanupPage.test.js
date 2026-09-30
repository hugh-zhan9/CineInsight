import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

// 应用内确认框：默认答"确定"，需要"取消"的用例单独覆盖。
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

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import PhotoCleanupPage from './PhotoCleanupPage.vue';
import { photoCleanupStore, resetPhotoCleanupReview, stopPhotoCleanupPolling } from '../utils/photoCleanupStore.js';

function makeMember(id, name, extra = {}) {
  return { id, name, path: `/p/${name}`, directory: '/p', width: 1000, height: 1000, file_size: 500000, ...extra };
}

function nearGroup() {
  return {
    original: makeMember(1, '1 (102).jpg'),
    candidates: [makeMember(2, 'guochan2048.com-1 (123).jpg')],
    reason: 'dHash 汉明距离 3'
  };
}

function exactGroup() {
  return {
    original: makeMember(11, 'keep.jpg'),
    candidates: [makeMember(12, 'copy.jpg')],
    reason: '文件大小和采样哈希一致'
  };
}

function statusWith(analysis, extra = {}) {
  return {
    running: false,
    completed: true,
    stale: false,
    started_at: '2026-09-02T00:00:00Z',
    analysis: { duplicate_groups: [], near_duplicate_groups: [], ...analysis },
    ...extra
  };
}

const analysisStatus = () => statusWith({ near_duplicate_groups: [nearGroup()] });

// MergeMediaMetadata 的第 4 个参数（§9.1）：图片只有精确 / 近似两类，两项都不跳过（后端对图片也忽略它们）。
const FULL_MERGE = { skip_playback_state: false, skip_subtitle: false };

function okResult(ids, batchID = 'batch-1') {
  return { batch_id: batchID, requested: ids.length, succeeded: ids.length, failed: 0, cancelled: 0, items: ids.map(id => ({ id, code: 'ok' })) };
}

async function mountWith(status) {
  photoCleanupStore.status = status;
  api.GetImageCleanupStatus.mockResolvedValue(status);
  const wrapper = mount(PhotoCleanupPage, { attachTo: document.body });
  await flushPromises();
  return wrapper;
}

async function clickDelete(wrapper) {
  await wrapper.vm.$nextTick();
  await wrapper.get('[data-test="cleanup-delete-selected"]').trigger('click');
  await flushPromises();
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  document.body.innerHTML = '';
  stopPhotoCleanupPolling();
  resetPhotoCleanupReview('');
  photoCleanupStore.status = analysisStatus();
  api.GetImageCleanupStatus.mockResolvedValue(analysisStatus());
  api.DeleteImagesWithResult.mockImplementation(ids => Promise.resolve(okResult(ids)));
  api.MergeMediaMetadata.mockResolvedValue({ kind: 'image', warnings: [] });
  feedback.confirmAction.mockResolvedValue(true);
});

describe('图片清理审阅', () => {
  // 2026-09-30 P-032：旧用例（第 42 行）钉住的是「近似重复也默认预勾」，按 D-PC49 改为近似不勾。
  it('IMG-04 近似重复默认不勾选', async () => {
    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    expect(wrapper.vm.selection).toEqual([]);
    expect(wrapper.find('[data-test="cleanup-group-outcome"]').text()).toBe('本组暂不删除任何图片');
    wrapper.unmount();
  });

  it('IMG-04 精确重复默认勾选保留项以外的副本', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] }));
    expect(wrapper.vm.selection).toEqual([12]);
    const outcomes = wrapper.findAll('[data-test="cleanup-group-outcome"]').map(node => node.text());
    expect(outcomes).toEqual(['本组将删除 1 张', '本组暂不删除任何图片']);
    wrapper.unmount();
  });

  it('IMG-05「按建议勾选 / 取消」只翻转本组', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] }));

    const nearCard = () => wrapper.findAll('[data-test="cleanup-group-card"]').find(card => card.attributes('data-kind') === 'near');
    const toggle = () => nearCard().get('[data-test="cleanup-suggest-group"]');
    expect(toggle().text()).toBe('按建议勾选（保留推荐项）');

    await toggle().trigger('click');
    await flushPromises();
    expect(wrapper.vm.selection).toEqual([12, 2]);
    expect(nearCard().get('[data-test="cleanup-group-outcome"]').text()).toBe('本组将删除 1 张');
    expect(toggle().text()).toBe('取消本组勾选');

    await toggle().trigger('click');
    await flushPromises();
    // 精确重复那一组的勾选不受影响。
    expect(wrapper.vm.selection).toEqual([12]);
    wrapper.unmount();
  });

  // §9.2 主代理裁决（P-032 评审 Minor 2）：原保留项只解除锁定，不自动勾上。
  it('D-PC49 改了保留项之后，原保留项可勾但不自动勾上', async () => {
    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    const entry = wrapper.vm.entries[0];
    wrapper.vm.toggleGroupSuggestion(entry);
    expect(wrapper.vm.selection).toEqual([2]);
    wrapper.vm.setKeep(entry, entry.members[1]);
    await flushPromises();

    expect(wrapper.vm.selection).toEqual([]);
    const toggles = wrapper.findAll('[data-test="cleanup-candidate-toggle"]');
    expect(toggles[0].element.disabled).toBe(false);
    expect(toggles[0].element.checked).toBe(false);
    expect(toggles[1].element.disabled).toBe(true);
    wrapper.unmount();
  });

  it('IMG-05 另一组要保留的图在本组锁定，勾选框禁用并说明原因', async () => {
    const wrapper = await mountWith(statusWith({
      duplicate_groups: [{ original: makeMember(1, 'a.jpg'), candidates: [makeMember(2, 'b.jpg')], reason: '一致' }],
      near_duplicate_groups: [{ original: makeMember(2, 'b.jpg'), candidates: [makeMember(3, 'c.jpg')], reason: '接近' }]
    }));
    // 2 是近似重复那一组的保留项，所以精确重复那一组也不能默认勾它。
    expect(wrapper.vm.selection).toEqual([]);
    const exactCard = wrapper.findAll('[data-test="cleanup-group-card"]')[0];
    expect(exactCard.get('[data-test="cleanup-member-locked"]').text()).toContain('另一组要保留它');
    const toggles = exactCard.findAll('[data-test="cleanup-candidate-toggle"]');
    expect(toggles.every(node => node.element.disabled)).toBe(true);
    wrapper.unmount();
  });

  it('IMG-05 近似重复的忽略按钮统一叫「不是重复」', async () => {
    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-dismiss-group"]').text()).toBe('不是重复');
    wrapper.unmount();
  });

  it('IMG-03 卡片标出人物等整理成果', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [{
      original: makeMember(11, 'keep.jpg', { curation: { people: true, favorite: true }, is_favorite: true }),
      candidates: [makeMember(12, 'copy.jpg', { curation: {} })],
      reason: '一致'
    }] }));
    const members = wrapper.findAll('[data-test="cleanup-member"]');
    expect(members[0].find('[data-test="cleanup-member-people"]').exists()).toBe(true);
    expect(members[0].text()).toContain('★ 已收藏');
    expect(members[1].text()).toContain('无收藏 / 评分 / 人物 / 标签');
    wrapper.unmount();
  });
});

describe('删除前汇总确认、合并元数据与废纸篓（D-PC48 / D-PC49 / D-PC02）', () => {
  it('IMG-04 删除前先汇总确认；取消时不调用任何写入绑定', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    await clickDelete(wrapper);

    const dialog = wrapper.get('[data-test="cleanup-delete-confirm-dialog"]');
    expect(dialog.get('[data-test="cleanup-delete-summary"]').text()).toContain('将把 1 张图片移到废纸篓');
    expect(dialog.get('[data-test="cleanup-delete-kinds"]').text()).toBe('其中精确重复 1 张。');
    expect(dialog.get('[data-test="cleanup-merge-toggle"]').element.checked).toBe(true);
    await dialog.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(false);
    expect(api.MergeMediaMetadata).not.toHaveBeenCalled();
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    expect(api.BatchDeleteImages).not.toHaveBeenCalled();
    expect(api.PermanentlyDeleteImages).not.toHaveBeenCalled();
    expect(wrapper.vm.selection).toEqual([12]);
    expect(wrapper.emitted('deleted')).toBeUndefined();
    wrapper.unmount();
  });

  it('IMG-04 汇总写出近似重复几张，并提醒按哈希判断', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] }));
    wrapper.vm.toggleSelection(2);
    await clickDelete(wrapper);
    const dialog = wrapper.get('[data-test="cleanup-delete-confirm-dialog"]');
    expect(dialog.get('[data-test="cleanup-delete-summary"]').text()).toContain('将把 2 张图片移到废纸篓，共 976.6 KB');
    expect(dialog.get('[data-test="cleanup-delete-kinds"]').text()).toBe('其中精确重复 1 张、近似重复 1 张。');
    expect(dialog.get('[data-test="cleanup-delete-similarity"]').text()).toContain('1 张是按感知哈希判断的近似重复');
    await dialog.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    wrapper.unmount();
  });

  it('IMG-03 默认先合并元数据到保留项，再经删除宿主移到废纸篓（*WithResult + 撤销条）', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const calls = [];
    api.MergeMediaMetadata.mockImplementation(async (...args) => { calls.push(['merge', ...args]); return { warnings: [] }; });
    api.DeleteImagesWithResult.mockImplementation(async (ids, deleteFile, requestID) => { calls.push(['delete', ids, deleteFile, requestID]); return okResult(ids, 'b-7'); });
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));

    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(calls[0]).toEqual(['merge', 'image', 11, [12], FULL_MERGE]);
    expect(calls[1].slice(0, 3)).toEqual(['delete', [12], true]);
    expect(calls[1][3]).toMatch(/^[0-9a-f]{32}$/);
    expect(api.BatchDeleteImages).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="delete-undo-banner"]').text()).toContain('已移到废纸篓 1 张图片');
    expect(wrapper.findAll('[data-test="cleanup-member-deleted"]')).toHaveLength(1);
    expect(wrapper.vm.selection).toEqual([]);
    expect(wrapper.emitted('deleted')).toHaveLength(1);
    wrapper.unmount();
  });

  it('IMG-03 合并失败时不删除，并说明一张都没删', async () => {
    api.MergeMediaMetadata.mockRejectedValue(new Error('数据库繁忙'));
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(api.MergeMediaMetadata).toHaveBeenCalledTimes(1);
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    expect(api.BatchDeleteImages).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="cleanup-error"]').text()).toContain('合并元数据失败，没有删除任何图片');
    expect(wrapper.vm.selection).toEqual([12]);
    expect(wrapper.vm.processing).toBe(false);
    wrapper.unmount();
  });

  // P-032 评审 I-1：删除前按锁定规则裁剪勾选。去掉这一步时合并计划会拒绝生成，删除不会发生。
  it('IMG-03 保留项被强行放进勾选时不会送进删除：先裁剪，再合并与删除其余项', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    photoCleanupStore.review.selection = [11, 12];
    await clickDelete(wrapper);

    const dialog = wrapper.get('[data-test="cleanup-delete-confirm-dialog"]');
    expect(dialog.get('[data-test="cleanup-delete-summary"]').text()).toContain('将把 1 张图片移到废纸篓');
    // 合并说明（P-032 评审 Minor 3）：只合并手动标签、撤销删除不撤回合并。
    expect(dialog.get('[data-test="cleanup-merge-note"]').text()).toContain('标签只合并手动标签');
    expect(dialog.get('[data-test="cleanup-merge-note"]').text()).toContain('撤销删除不会撤回合并');
    await dialog.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(api.MergeMediaMetadata).toHaveBeenCalledWith('image', 11, [12], FULL_MERGE);
    expect(api.DeleteImagesWithResult).toHaveBeenCalledTimes(1);
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([12], true, expect.any(String));
    wrapper.unmount();
  });

  // P-032 评审 Minor 4：按组逐个合并，第二组失败时前一组已经合并（不回滚），一张都不删。
  it('IMG-03 多组合并中途失败：说明前面几组已合并，没有删除任何图片', async () => {
    api.MergeMediaMetadata
      .mockResolvedValueOnce({ warnings: [] })
      .mockRejectedValueOnce(new Error('数据库繁忙'));
    const second = { original: makeMember(21, 'keep-2.jpg'), candidates: [makeMember(22, 'copy-2.jpg')], reason: '一致' };
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup(), second] }));
    expect(wrapper.vm.selection).toEqual([12, 22]);
    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(api.MergeMediaMetadata.mock.calls).toEqual([['image', 11, [12], FULL_MERGE], ['image', 21, [22], FULL_MERGE]]);
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    const error = wrapper.get('[data-test="cleanup-error"]').text();
    expect(error).toContain('没有删除任何图片');
    expect(error).toContain('共 2 组，前 1 组已经合并到各自的保留项');
    expect(wrapper.vm.selection).toEqual([12, 22]);
    expect(wrapper.vm.processing).toBe(false);
    wrapper.unmount();
  });

  it('IMG-03 取消勾选「合并元数据」时只删除', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-merge-toggle"]').setValue(false);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.MergeMediaMetadata).not.toHaveBeenCalled();
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([12], true, expect.any(String));
    wrapper.unmount();
  });

  it('LIB-04 磁盘不支持废纸篓时进入二选一；选「只删记录」按记录删除', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    api.DeleteImagesWithResult
      .mockResolvedValueOnce({ batch_id: 'b-1', requested: 1, succeeded: 0, failed: 1, cancelled: 0, items: [{ id: 12, code: 'trash_unsupported' }] })
      .mockResolvedValueOnce(okResult([12], 'b-2'));
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    // 二选一弹窗列出文件名；这时还没有任何改动。
    expect(wrapper.get('[data-test="trash-unsupported-names"]').text()).toContain('copy.jpg');
    expect(wrapper.vm.processing).toBe(true);
    await wrapper.get('[data-test="trash-unsupported-record-only"]').trigger('click');
    await flushPromises();

    expect(api.DeleteImagesWithResult).toHaveBeenLastCalledWith([12], false, '');
    expect(wrapper.vm.deletedIDs).toEqual([12]);
    expect(wrapper.get('[data-test="delete-undo-banner"]').text()).toContain('已从图片库移除 1 张图片（文件保留）');
    wrapper.unmount();
  });

  it('LIB-04 二选一时选「暂不处理」：图片原样留着、仍然勾选，不算删除', async () => {
    api.DeleteImagesWithResult.mockResolvedValueOnce({ batch_id: 'b-1', requested: 1, succeeded: 0, failed: 1, cancelled: 0, items: [{ id: 12, code: 'trash_unsupported' }] });
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="trash-unsupported-cancel"]').trigger('click');
    await flushPromises();

    expect(api.DeleteImagesWithResult).toHaveBeenCalledTimes(1);
    expect(api.PermanentlyDeleteImages).not.toHaveBeenCalled();
    expect(wrapper.vm.deletedIDs).toEqual([]);
    expect(wrapper.vm.selection).toEqual([12]);
    expect(wrapper.emitted('deleted')).toBeUndefined();
    wrapper.unmount();
  });

  it('LIB-12 在清理页的撤销条上整批撤销后，放掉已删除标记并通知图片库重载', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    api.RestoreTrashBatch.mockResolvedValue(okResult([12], 'batch-1'));
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.deletedIDs).toEqual([12]);

    await wrapper.get('[data-test="delete-undo"]').trigger('click');
    await flushPromises();
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('image', 'batch-1');
    expect(wrapper.vm.deletedIDs).toEqual([]);
    expect(wrapper.emitted('deleted')).toHaveLength(2);
    wrapper.unmount();
  });
});

describe('D-PC49 锁定与删除进行中（P-032 评审 I-1、Minor 5 / 7 / 8）', () => {
  // A（精确，保留 1）与 B（近似，保留 3）都含图 2；B 另有图 4。
  const overlapping = () => statusWith({
    duplicate_groups: [{ original: makeMember(1, 'a.jpg'), candidates: [makeMember(2, 'b.jpg')], reason: '一致' }],
    near_duplicate_groups: [{ original: makeMember(3, 'c.jpg'), candidates: [makeMember(2, 'b.jpg'), makeMember(4, 'd.jpg')], reason: '接近' }]
  });
  const card = (wrapper, kind) => wrapper.findAll('[data-test="cleanup-group-card"]').find(node => node.attributes('data-kind') === kind);
  const toggleOf = (node, name) => node.findAll('[data-test="cleanup-candidate-toggle"]').find(item => item.attributes('aria-label') === `删除 ${name}`);

  it('D-PC49「本组不删」的成员在别的组里也锁定，合并计划也不合并到被跳过的组', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const wrapper = await mountWith(overlapping());
    expect(wrapper.vm.selection).toEqual([2]);

    await card(wrapper, 'exact').get('[data-test="cleanup-skip-group"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.selection).toEqual([]);
    const nearCard = card(wrapper, 'near');
    expect(toggleOf(nearCard, 'b.jpg').element.disabled).toBe(true);
    expect(nearCard.get('[data-test="cleanup-member-locked"]').text()).toBe('（另一组设了「本组不删」）');
    wrapper.vm.toggleSelection(2);
    expect(wrapper.vm.selection).toEqual([]);

    // 强行放进勾选也会在删除前被裁掉；被跳过的组不进合并计划。
    photoCleanupStore.review.selection = [2, 4];
    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['image', 3, [4], FULL_MERGE]]);
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([4], true, expect.any(String));
    wrapper.unmount();
  });

  it('D-PC49 删除进行中禁用勾选、「保留这份」「本组不删」「移出本组」「不是重复」与返回，结束后恢复', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    let finishDelete;
    api.DeleteImagesWithResult.mockImplementation(ids => new Promise(resolve => { finishDelete = () => resolve(okResult(ids)); }));
    const wrapper = await mountWith(overlapping());
    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([2], true, expect.any(String));
    expect(wrapper.vm.processing).toBe(true);

    const controls = () => wrapper.findAll([
      '[data-test="cleanup-candidate-toggle"]', '[data-test="cleanup-keep-toggle"]', '[data-test="cleanup-skip-group"]',
      '[data-test="cleanup-suggest-group"]', '[data-test="cleanup-remove-member"]', '[data-test="cleanup-dismiss-group"]',
      '[data-test="cleanup-samedir-switch"] input', '[data-test="cleanup-back"]'
    ].join(', '));
    expect(controls().length).toBeGreaterThanOrEqual(14);
    expect(controls().every(node => node.element.disabled)).toBe(true);

    // 界面之外直接调用也不生效。
    const nearEntry = wrapper.vm.entries.find(entry => entry.kind === 'near');
    wrapper.vm.setKeep(nearEntry, nearEntry.members[2]);
    wrapper.vm.toggleSelection(4);
    wrapper.vm.toggleSkipGroup(nearEntry);
    await wrapper.vm.removeMember(nearEntry, nearEntry.members[2]);
    expect(wrapper.vm.keepOverrides).toEqual({});
    expect(wrapper.vm.selection).toEqual([2]);
    expect(wrapper.vm.skippedGroups).toEqual({});
    expect(api.DismissImageNearDuplicateMember).not.toHaveBeenCalled();

    finishDelete();
    await flushPromises();
    expect(wrapper.vm.processing).toBe(false);
    expect(wrapper.get('[data-test="cleanup-back"]').element.disabled).toBe(false);
    wrapper.unmount();
  });

  it('D-PC49「已忽略」下不能移到废纸篓', async () => {
    api.ListCleanupDismissals.mockResolvedValue({ items: [], next_cursor: 0, has_more: false });
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    expect(wrapper.vm.selection).toEqual([12]);
    await wrapper.get('[data-test="cleanup-dismissed-toggle"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').element.disabled).toBe(true);
    await wrapper.vm.deleteSelected();
    await flushPromises();
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(false);
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('IMG-05「移出本组」移走推荐保留项时，接替的保留项移出勾选', async () => {
    const wrapper = await mountWith(statusWith({ near_duplicate_groups: [{
      original: makeMember(1, 'a.jpg'), candidates: [makeMember(2, 'b.jpg'), makeMember(3, 'c.jpg')], reason: '接近'
    }] }));
    wrapper.vm.toggleSelection(2);
    wrapper.vm.toggleSelection(3);
    expect(wrapper.vm.selection).toEqual([2, 3]);
    const entry = wrapper.vm.entries[0];
    await wrapper.vm.removeMember(entry, entry.members[0]);
    await flushPromises();
    expect(api.DismissImageNearDuplicateMember).toHaveBeenCalledWith([1, 2, 3], 1);
    expect(wrapper.vm.keepFor(wrapper.vm.entries[0])).toBe(2);
    expect(wrapper.vm.selection).toEqual([3]);
    wrapper.unmount();
  });
});

describe('IMG-07 忽略可控：确认、移出单个成员、「已忽略」与撤销', () => {
  const threeWay = () => statusWith({ near_duplicate_groups: [{
    original: makeMember(1, 'a.jpg'),
    candidates: [makeMember(2, 'b.jpg'), makeMember(3, 'c.jpg')],
    reason: '接近'
  }] });

  it('IMG-07「不是重复」先确认，取消时不写忽略', async () => {
    feedback.confirmAction.mockResolvedValueOnce(false);
    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();
    await wrapper.get('[data-test="cleanup-dismiss-group"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ title: '不是重复' }));
    expect(api.DismissImageNearDuplicateGroup).not.toHaveBeenCalled();
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(1);
    wrapper.unmount();
  });

  it('IMG-07「移出本组」只否决这一张，其余成员留在组里', async () => {
    const wrapper = await mountWith(threeWay());
    const removeButtons = wrapper.findAll('[data-test="cleanup-remove-member"]');
    expect(removeButtons).toHaveLength(3);
    await removeButtons[2].trigger('click');
    await flushPromises();

    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ title: '移出本组' }));
    expect(api.DismissImageNearDuplicateMember).toHaveBeenCalledWith([1, 2, 3], 3);
    expect(wrapper.findAll('[data-test="cleanup-member"]').map(node => node.find('.cleanup-member__name').text())).toEqual(['a.jpg', 'b.jpg']);
    // 剩两张时不再给「移出本组」（等同于「不是重复」）。
    expect(wrapper.find('[data-test="cleanup-remove-member"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('IMG-07 推荐保留项被移出本组时由剩下排第一的接替', async () => {
    const wrapper = await mountWith(threeWay());
    await wrapper.findAll('[data-test="cleanup-remove-member"]')[0].trigger('click');
    await flushPromises();
    expect(api.DismissImageNearDuplicateMember).toHaveBeenCalledWith([1, 2, 3], 1);
    const entry = wrapper.vm.entries[0];
    expect(wrapper.vm.keepFor(entry)).toBe(2);
    wrapper.unmount();
  });

  it('IMG-07「移出本组」取消确认时什么都不写', async () => {
    feedback.confirmAction.mockResolvedValueOnce(false);
    const wrapper = await mountWith(threeWay());
    await wrapper.findAll('[data-test="cleanup-remove-member"]')[1].trigger('click');
    await flushPromises();
    expect(api.DismissImageNearDuplicateMember).not.toHaveBeenCalled();
    expect(wrapper.findAll('[data-test="cleanup-member"]')).toHaveLength(3);
    wrapper.unmount();
  });

  it('IMG-07「已忽略」列出图片近似重复的忽略记录，可以撤销、加载更多', async () => {
    api.ListCleanupDismissals.mockImplementation(async (kind, cursor) => (cursor === 0
      ? { items: [{ id: 9, kind, media: [{ id: 1, name: 'a.jpg' }, { id: 2, name: 'b.jpg', missing: true }], created_at: '2026-09-29T10:00:00Z' }], next_cursor: 9, has_more: true }
      : { items: [{ id: 4, kind, media: [{ id: 5, name: 'e.jpg' }, { id: 6, name: 'f.jpg' }] }], next_cursor: 4, has_more: false }));
    api.UndoCleanupDismissals.mockResolvedValue({ kind: 'image_near_duplicate', removed: 1 });
    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    await wrapper.get('[data-test="cleanup-dismissed-toggle"]').trigger('click');
    await flushPromises();
    expect(api.ListCleanupDismissals).toHaveBeenCalledWith('image_near_duplicate', 0, 50);
    const items = () => wrapper.findAll('[data-test="cleanup-dismissal-item"]');
    expect(items()[0].text()).toContain('「a.jpg」与「b.jpg（已删除）」');

    await wrapper.get('[data-test="cleanup-dismissed-more"]').trigger('click');
    await flushPromises();
    expect(api.ListCleanupDismissals).toHaveBeenLastCalledWith('image_near_duplicate', 9, 50);
    expect(items()).toHaveLength(2);

    await items()[0].get('[data-test="cleanup-dismissal-undo"]').trigger('click');
    await flushPromises();
    expect(api.UndoCleanupDismissals).toHaveBeenCalledWith('image_near_duplicate', [9]);
    expect(items()).toHaveLength(1);

    api.UndoCleanupDismissals.mockRejectedValueOnce(new Error('数据库繁忙'));
    await items()[0].get('[data-test="cleanup-dismissal-undo"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-dismissed-error"]').text()).toContain('撤销忽略失败');
    expect(items()).toHaveLength(1);

    await wrapper.get('[data-test="cleanup-dismissed-toggle"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(1);
    wrapper.unmount();
  });
});

describe('IMG-11 覆盖率与空态', () => {
  it('IMG-11 指纹一张都没算时说「近似重复尚未计算」并给出补全入口', async () => {
    const wrapper = await mountWith(statusWith({ coverage: { perceptual_hash: { done: 0, total: 40 } } }));
    expect(wrapper.get('[data-test="cleanup-empty"]').text()).toContain('没有发现精确重复；近似重复尚未计算。');
    expect(wrapper.get('[data-test="cleanup-coverage"]').text()).toContain('近似重复尚未计算');
    await wrapper.get('[data-test="cleanup-empty-start-phash"]').trigger('click');
    await flushPromises();
    expect(api.StartImagePerceptualHashBackfill).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('IMG-11 只算了一部分时标出已算比例；全部算完才说「没有发现」', async () => {
    const partial = await mountWith(statusWith({ coverage: { perceptual_hash: { done: 10, total: 40 } } }));
    expect(partial.get('[data-test="cleanup-empty-detail"]').text()).toContain('指纹只算了 10 / 40 张');
    expect(partial.get('[data-test="cleanup-coverage"]').text()).toBe('近似重复指纹已算 10 / 40');
    partial.unmount();

    const full = await mountWith(statusWith({ coverage: { perceptual_hash: { done: 40, total: 40 } } }));
    expect(full.get('[data-test="cleanup-empty"]').text()).toBe('没有发现重复或近似重复的图片。');
    expect(full.find('[data-test="cleanup-coverage"]').exists()).toBe(false);
    expect(full.find('[data-test="cleanup-empty-start-phash"]').exists()).toBe(false);
    full.unmount();
  });
});

describe('IMG-12 图片清理分析可以取消', () => {
  it('IMG-12 分析中点「取消分析」调用 CancelImageCleanupAnalysis', async () => {
    const wrapper = await mountWith({ running: true, completed: false, progress: { stage: 'near', current: 3, total: 9 } });
    await wrapper.get('[data-test="cleanup-cancel-analysis"]').trigger('click');
    await flushPromises();
    expect(api.CancelImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="cleanup-cancel-analysis"]').text()).toBe('正在取消…');
    wrapper.unmount();
    stopPhotoCleanupPolling();
  });

  it('IMG-12 上一轮被取消时显示「已取消」，进来也不自动重跑', async () => {
    const wrapper = await mountWith({ running: false, completed: false, cancelled: true, progress: { stage: 'done' } });
    expect(api.StartImageCleanupAnalysis).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="cleanup-idle"]').text()).toContain('上一轮分析已取消');
    wrapper.unmount();
  });

  it('IMG-12 取消失败时说出来', async () => {
    api.CancelImageCleanupAnalysis.mockRejectedValueOnce(new Error('图片清理分析未在运行'));
    const wrapper = await mountWith({ running: true, completed: false, progress: { stage: 'near' } });
    await wrapper.get('[data-test="cleanup-cancel-analysis"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-error"]').text()).toContain('取消分析失败');
    expect(wrapper.get('[data-test="cleanup-cancel-analysis"]').element.disabled).toBe(false);
    wrapper.unmount();
    stopPhotoCleanupPolling();
  });
});

describe('进入清理页的自动分析', () => {
  it('没有可用结果时自动发起一次分析，不用再点"开始分析"', async () => {
    api.GetImageCleanupStatus.mockResolvedValue({ running: false, completed: false, analysis: null });
    api.StartImageCleanupAnalysis.mockResolvedValue({ running: true, completed: false, analysis: null });
    photoCleanupStore.status = { running: false, completed: false, analysis: null };

    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    wrapper.unmount();
    stopPhotoCleanupPolling();
  });

  it('后端已有结果就直接用，不覆盖正在审阅的那份', async () => {
    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    expect(api.StartImageCleanupAnalysis).not.toHaveBeenCalled();
    expect(wrapper.findAll('[data-test="cleanup-group-card"]').length).toBeGreaterThan(0);
    wrapper.unmount();
  });

  it('分析正在后台跑时不再重复发起', async () => {
    api.GetImageCleanupStatus.mockResolvedValue({ running: true, completed: false, analysis: null });
    photoCleanupStore.status = { running: true, completed: false, analysis: null };

    const wrapper = mount(PhotoCleanupPage);
    await flushPromises();

    expect(api.StartImageCleanupAnalysis).not.toHaveBeenCalled();
    wrapper.unmount();
    stopPhotoCleanupPolling();
  });
});

describe('D-PC01 文案', () => {
  it('LIB-05 按钮与可释放空间说「移到废纸篓」，不再说移入回收站', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toBe('移到废纸篓 (1)');
    expect(wrapper.get('[data-test="cleanup-summary"]').text()).toContain('移到废纸篓后，在访达清空废纸篓即可释放');
    expect(wrapper.text()).not.toContain('移入回收站');
    wrapper.unmount();
  });
});

// ===== P-032 复审（修复 Q）：确认框打开期间与改分组请求进行中（I-1）、合并失败提示（m3） =====
describe('D-PC49 确认框打开期间与改分组请求进行中（P-032 复审 I-1）', () => {
  const CHANGED = '确认期间勾选或保留项有变化，请重新确认';
  // A（精确，保留 1）与 B（近似，保留 3）都含图 2；B 另有图 4。
  const overlapping = () => statusWith({
    duplicate_groups: [{ original: makeMember(1, 'a.jpg'), candidates: [makeMember(2, 'b.jpg')], reason: '一致' }],
    near_duplicate_groups: [{ original: makeMember(3, 'c.jpg'), candidates: [makeMember(2, 'b.jpg'), makeMember(4, 'd.jpg')], reason: '接近' }]
  });
  const threeWayNear = () => ({
    original: makeMember(1, 'a.jpg'), candidates: [makeMember(2, 'b.jpg'), makeMember(3, 'c.jpg')], reason: '接近'
  });
  const secondExact = () => ({ original: makeMember(21, 'keep-2.jpg'), candidates: [makeMember(22, 'copy-2.jpg')], reason: '一致' });
  const errorText = wrapper => (wrapper.find('[data-test="cleanup-error"]').exists() ? wrapper.get('[data-test="cleanup-error"]').text() : '');

  it('D-PC49 确认框打开期间勾选、「保留这份」「本组不删」「移出本组」「不是重复」都禁用并拦截；状态没变时确认后照常按原名单删除', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const wrapper = await mountWith(statusWith({
      duplicate_groups: [{ original: makeMember(1, 'a.jpg'), candidates: [makeMember(2, 'b.jpg')], reason: '一致' }],
      near_duplicate_groups: [{ original: makeMember(3, 'c.jpg'), candidates: [makeMember(2, 'b.jpg'), makeMember(4, 'd.jpg'), makeMember(5, 'e.jpg')], reason: '接近' }]
    }));
    expect(wrapper.vm.selection).toEqual([2]);
    await clickDelete(wrapper);
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);
    expect(wrapper.vm.processing).toBe(false);

    const controls = () => wrapper.findAll([
      '[data-test="cleanup-candidate-toggle"]', '[data-test="cleanup-keep-toggle"]', '[data-test="cleanup-skip-group"]',
      '[data-test="cleanup-suggest-group"]', '[data-test="cleanup-remove-member"]', '[data-test="cleanup-dismiss-group"]',
      '[data-test="cleanup-samedir-switch"] input', '[data-test="cleanup-start"]'
    ].join(', '));
    expect(controls().length).toBeGreaterThanOrEqual(16);
    expect(controls().every(node => node.element.disabled)).toBe(true);

    // 界面之外直接调用同样被拦下。
    const nearEntry = wrapper.vm.entries.find(entry => entry.kind === 'near');
    wrapper.vm.setKeep(nearEntry, nearEntry.members[2]);
    wrapper.vm.toggleSelection(4);
    wrapper.vm.toggleSkipGroup(nearEntry);
    wrapper.vm.toggleGroupSuggestion(nearEntry);
    wrapper.vm.toggleSameDirOnly();
    await wrapper.vm.removeMember(nearEntry, nearEntry.members[3]);
    await wrapper.vm.dismissGroup(nearEntry);
    await wrapper.vm.startAnalysis();
    expect(wrapper.vm.keepOverrides).toEqual({});
    expect(wrapper.vm.selection).toEqual([2]);
    expect(wrapper.vm.skippedGroups).toEqual({});
    expect(wrapper.vm.sameDirOnly).toBe(false);
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.DismissImageNearDuplicateMember).not.toHaveBeenCalled();
    expect(api.DismissImageNearDuplicateGroup).not.toHaveBeenCalled();
    expect(api.StartImageCleanupAnalysis).not.toHaveBeenCalled();

    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['image', 1, [2], FULL_MERGE], ['image', 3, [2], FULL_MERGE]]);
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([2], true, expect.any(String));
    expect(errorText(wrapper)).not.toContain(CHANGED);
    expect(wrapper.vm.processing).toBe(false);
    expect(wrapper.vm.confirming).toBe(false);
    wrapper.unmount();
  });

  // 场景 A：「本组不删」在确认期间生效（绕过界面锁直接写审阅状态，效果与点「本组不删」相同）。
  it('D-PC49 确认框打开期间「本组不删」生效：确认后不合并、不删除，提示重新确认，勾选保持新的', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup(), secondExact()] }));
    expect(wrapper.vm.selection).toEqual([12, 22]);
    await clickDelete(wrapper);

    photoCleanupStore.review.skippedGroups = { 'exact-21': true };
    photoCleanupStore.review.selection = [12];
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.MergeMediaMetadata).not.toHaveBeenCalled();
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    expect(errorText(wrapper)).toContain(CHANGED);
    expect(wrapper.vm.selection).toEqual([12]);
    expect(wrapper.vm.processing).toBe(false);
    expect(wrapper.emitted('deleted')).toBeUndefined();
    wrapper.unmount();
  });

  // 场景 A：「保留这份」在确认期间生效。待删名单没变，但保留项变了，同样中止。
  it('D-PC49 确认框打开期间「保留这份」生效（待删名单不变、保留项换了）：确认后不合并、不删除', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [{
      original: makeMember(11, 'keep.jpg'), candidates: [makeMember(12, 'copy.jpg'), makeMember(13, 'copy-2.jpg')], reason: '一致'
    }] }));
    wrapper.vm.toggleSelection(13);
    expect(wrapper.vm.selection).toEqual([12]);
    await clickDelete(wrapper);

    photoCleanupStore.review.keepOverrides = { 'exact-11': 13 };
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.MergeMediaMetadata).not.toHaveBeenCalled();
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    expect(errorText(wrapper)).toContain(CHANGED);
    expect(wrapper.vm.selection).toEqual([12]);
    wrapper.unmount();
  });

  // 场景 A：确认期间轮询换来同一轮分析的新版本，推荐保留项变了。
  it('D-PC49 确认框打开期间轮询替换了分析结果：确认后不合并、不删除', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    expect(wrapper.vm.selection).toEqual([12]);
    await clickDelete(wrapper);

    const replaced = statusWith({ duplicate_groups: [{
      original: makeMember(12, 'copy.jpg'), candidates: [makeMember(11, 'keep.jpg')], reason: '一致'
    }] });
    api.GetImageCleanupStatus.mockResolvedValue(replaced);
    photoCleanupStore.status = replaced;
    await flushPromises();
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.MergeMediaMetadata).not.toHaveBeenCalled();
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    expect(errorText(wrapper)).toContain(CHANGED);
    wrapper.unmount();
  });

  // 场景 B：「移出本组」的请求还没返回。
  it('D-PC49「移出本组」请求没返回时「移到废纸篓」禁用，直接调用删除入口也被拦截；返回后恢复', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()], near_duplicate_groups: [threeWayNear()] }));
    expect(wrapper.vm.selection).toEqual([12]);
    let finishDismiss;
    api.DismissImageNearDuplicateMember.mockImplementationOnce(() => new Promise(resolve => { finishDismiss = resolve; }));

    const nearEntry = wrapper.vm.entries.find(entry => entry.kind === 'near');
    const pending = wrapper.vm.removeMember(nearEntry, nearEntry.members[2]);
    await flushPromises();
    expect(api.DismissImageNearDuplicateMember).toHaveBeenCalledWith([1, 2, 3], 3);
    const button = () => wrapper.get('[data-test="cleanup-delete-selected"]');
    expect(button().element.disabled).toBe(true);
    expect(button().attributes('title')).toBe('正在更新分组，完成后再删除');

    await wrapper.vm.deleteSelected();
    await flushPromises();
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(false);
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();

    finishDismiss();
    await pending;
    await flushPromises();
    expect(button().element.disabled).toBe(false);
    await clickDelete(wrapper);
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);
    await wrapper.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    wrapper.unmount();
  });

  it('D-PC49 等「移出本组」确认的时候打开了删除确认框：之后再确认移出也不发请求', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()], near_duplicate_groups: [threeWayNear()] }));
    let answerRemove;
    feedback.confirmAction.mockImplementationOnce(() => new Promise(resolve => { answerRemove = resolve; }));
    const nearEntry = wrapper.vm.entries.find(entry => entry.kind === 'near');
    const pending = wrapper.vm.removeMember(nearEntry, nearEntry.members[2]);
    await clickDelete(wrapper);
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);

    answerRemove(true);
    await pending;
    await flushPromises();
    expect(api.DismissImageNearDuplicateMember).not.toHaveBeenCalled();
    expect(wrapper.vm.dismissing).toBe(false);
    await wrapper.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    wrapper.unmount();
  });

  // P-032 复审 m3：多组合并中途失败时，前面几组返回的提示随失败提示一起给出。
  it('IMG-03 多组合并中途失败：前面几组返回的提示一并列出，一张都不删', async () => {
    api.MergeMediaMetadata
      .mockResolvedValueOnce({ kind: 'image', warnings: ['整理图标刷新失败'] })
      .mockRejectedValueOnce('数据库繁忙');
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup(), secondExact()] }));
    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    const error = errorText(wrapper);
    expect(error).toContain('合并元数据失败，没有删除任何图片：数据库繁忙。');
    expect(error).toContain('共 2 组，前 1 组已经合并到各自的保留项');
    expect(error).toContain('已合并的组另有 1 条提示：整理图标刷新失败');
    wrapper.unmount();
  });
});
