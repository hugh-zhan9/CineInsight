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

// 精确重复与疑似重复分在两个区域，点统计卡切换。
async function showKind(wrapper, kind) {
  await wrapper.get(`[data-test="cleanup-kind-${kind}"]`).trigger('click');
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
  it.each(['many-groups', 'one-group'])('十万张图片从扫描切到结果，%s 只渲染一页', async shape => {
    const wrapper = await mountWith(statusWith({}, {
      running: true, completed: false, analysis: null,
      progress: { stage: 'near', current: 99800, total: 100000 }
    }));
    expect(wrapper.find('[data-test="cleanup-cancel-analysis"]').exists()).toBe(true);
    const groups = shape === 'many-groups'
      ? Array.from({ length: 50000 }, (_, i) => ({
        original: makeMember(i * 2 + 1, `keep-${i}.jpg`),
        candidates: [makeMember(i * 2 + 2, `copy-${i}.jpg`)]
      }))
      : [{ original: makeMember(1, 'keep.jpg'), candidates: Array.from({ length: 99999 }, (_, i) => makeMember(i + 2, `copy-${i}.jpg`)) }];
    photoCleanupStore.status = statusWith({ duplicate_groups: groups });
    await wrapper.vm.$nextTick();
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(shape === 'many-groups' ? 20 : 1);
    expect(wrapper.findAll('[data-test="cleanup-member"]')).toHaveLength(shape === 'many-groups' ? 40 : 20);
    expect(wrapper.vm.selection).toHaveLength(shape === 'many-groups' ? 50000 : 99999);
    wrapper.unmount();
  });

  it('数千组结果只挂载一页，翻页保留全量勾选与保留项', async () => {
    const groups = Array.from({ length: 1000 }, (_, i) => ({
      original: makeMember(i * 2 + 1, `keep-${i}.jpg`),
      candidates: [makeMember(i * 2 + 2, `copy-${i}.jpg`)]
    }));
    const wrapper = await mountWith(statusWith({ duplicate_groups: groups }));
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(20);
    expect(wrapper.findAll('[data-test="cleanup-member"]')).toHaveLength(40);
    expect(wrapper.vm.selection).toHaveLength(1000);
    expect(wrapper.get('.cleanup-dir__count').text()).toContain('1000 组');
    await wrapper.get('[data-test="cleanup-page-next"]').trigger('click');
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(20);
    expect(wrapper.text()).toContain('keep-20.jpg');
    expect(wrapper.vm.selection).toHaveLength(1000);
    await wrapper.findAll('[data-test="cleanup-keep-toggle"]')[1].setValue(true);
    expect(wrapper.vm.review.keepOverrides['exact-41']).toBe(42);
    expect(wrapper.vm.selection).not.toContain(42);
    await wrapper.get('[data-test="cleanup-page-prev"]').trigger('click');
    expect(wrapper.text()).toContain('keep-0.jpg');
    await wrapper.get('[data-test="cleanup-page-next"]').trigger('click');
    expect(wrapper.findAll('[data-test="cleanup-keep-toggle"]')[1].element.checked).toBe(true);
    wrapper.unmount();
  });

  it('大组换保留项并启用同目录策略时，不逐成员扫描整组', async () => {
    const group = { original: makeMember(1, 'keep.jpg'), candidates: Array.from({ length: 19999 }, (_, i) => makeMember(i + 2, `copy-${i}.jpg`)) };
    const wrapper = await mountWith(statusWith({ duplicate_groups: [group] }));
    wrapper.vm.review.sameDirOnly = true;
    wrapper.vm.review.keepOverrides = { 'exact-1': 20000 };
    const members = wrapper.vm.cleanupGroups[0].memberIds;
    const includes = vi.spyOn(members, 'includes');
    expect(wrapper.vm.suggestedIDsFor(wrapper.vm.entries[0])).toHaveLength(19999);
    // 查保留项至多按组执行；旧逻辑会为 19999 个候选分别 includes(20000)。
    expect(includes.mock.calls.length).toBeLessThan(10);
    includes.mockRestore();
    wrapper.unmount();
  });

  it('单一大组也分页，指纹距离不声称内容相似度百分比', async () => {
    const group = { original: makeMember(1, 'keep.jpg'), candidates: Array.from({ length: 999 }, (_, i) => makeMember(i + 2, `copy-${i}.jpg`)), max_hamming_distance: 0 };
    const wrapper = await mountWith(statusWith({ near_duplicate_groups: [group] }));
    expect(wrapper.findAll('[data-test="cleanup-member"]')).toHaveLength(20);
    expect(wrapper.get('[data-test="cleanup-similarity"]').text()).not.toContain('%');
    await wrapper.get('[data-test="cleanup-members-next"]').trigger('click');
    expect(wrapper.findAll('[data-test="cleanup-member"]')).toHaveLength(20);
    expect(wrapper.text()).toContain('copy-19.jpg');
    expect(wrapper.vm.selection).toEqual([]);
    wrapper.unmount();
  });

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
    const outcomes = () => wrapper.findAll('[data-test="cleanup-group-outcome"]').map(node => node.text());
    expect(outcomes()).toEqual(['本组将删除 1 张']);
    await showKind(wrapper, 'near');
    expect(outcomes()).toEqual(['本组暂不删除任何图片']);
    wrapper.unmount();
  });

  it('IMG-05「按建议勾选 / 取消」只翻转本组', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] }));
    await showKind(wrapper, 'near');

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
    // 收藏标记用图标集里的星形（不再用字符 ★）加文字。
    expect(members[0].get('[data-test="cleanup-member-favorite"]').text()).toBe('已收藏');
    expect(members[0].find('[data-test="cleanup-member-favorite"] [data-icon="star"]').exists()).toBe(true);
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
    await showKind(wrapper, 'near');
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
      '[data-test="cleanup-suggest-page"]', '[data-test="cleanup-samedir-switch"] input', '[data-test="cleanup-back"]'
    ].join(', '));
    // 两个区域分开渲染，切换区域不改勾选，删除进行中也能切过去看。
    expect(controls().length).toBeGreaterThanOrEqual(9);
    expect(controls().every(node => node.element.disabled)).toBe(true);
    await showKind(wrapper, 'near');
    expect(controls().length).toBeGreaterThanOrEqual(15);
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
    // 空态换成统一的 EmptyState：说明文字在它的 message 里。
    expect(partial.get('[data-test="cleanup-empty"] .ui-empty__message').text()).toContain('指纹只算了 10 / 40 张');
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
  const CHANGED = '确认期间清理结果、勾选或保留项有变化，请重新确认';
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
      '[data-test="cleanup-suggest-page"]', '[data-test="cleanup-samedir-switch"] input', '[data-test="cleanup-start"]'
    ].join(', '));
    expect(controls().length).toBeGreaterThanOrEqual(9);
    expect(controls().every(node => node.element.disabled)).toBe(true);
    await showKind(wrapper, 'near');
    expect(controls().length).toBeGreaterThanOrEqual(18);
    expect(controls().every(node => node.element.disabled)).toBe(true);
    wrapper.vm.togglePageSuggestion();

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

  // P-032 复审（修复 R）Minor：「不是重复」与「移出本组」同构，确认返回后再查一次锁。
  it('D-PC49 等「不是重复」确认的时候打开了删除确认框：之后再确认也不发请求', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()], near_duplicate_groups: [threeWayNear()] }));
    let answerDismiss;
    feedback.confirmAction.mockImplementationOnce(() => new Promise(resolve => { answerDismiss = resolve; }));
    const nearEntry = wrapper.vm.entries.find(entry => entry.kind === 'near');
    const pending = wrapper.vm.dismissGroup(nearEntry);
    await clickDelete(wrapper);
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);

    answerDismiss(true);
    await pending;
    await flushPromises();
    expect(api.DismissImageNearDuplicateGroup).not.toHaveBeenCalled();
    expect(wrapper.vm.dismissing).toBe(false);
    expect(wrapper.vm.review.dismissedKeys || []).toEqual([]);
    await wrapper.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    wrapper.unmount();
  });
});

// ===== P-032 复审（修复 R）：「移出本组」后接替的保留项不能是已经删掉的那一张（I-a） =====
describe('D-PC49「移出本组」后的接替保留项（P-032 复审 I-a）', () => {
  const nearOf = wrapper => wrapper.vm.entries.find(entry => entry.kind === 'near');

  // 勾上这几张、删掉；删除后的「重新分析」由调用方的 confirmAction 替身选取消，结果留着继续审阅。
  async function deleteSelection(wrapper, ids) {
    photoCleanupStore.review.selection = ids;
    await clickDelete(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
  }

  it('IMG-05 近似组 {1,2,3,4} 先删 2 再移出 1：接替者是 3 而不是 2，合并调用的保留项是 3', async () => {
    feedback.confirmAction.mockResolvedValue(false).mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    const wrapper = await mountWith(statusWith({ near_duplicate_groups: [{
      original: makeMember(1, 'a.jpg'), candidates: [makeMember(2, 'b.jpg'), makeMember(3, 'c.jpg'), makeMember(4, 'd.jpg')], reason: '接近'
    }] }));
    await deleteSelection(wrapper, [2]);
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['image', 1, [2], FULL_MERGE]]);
    expect(wrapper.vm.deletedIDs).toEqual([2]);

    await wrapper.vm.removeMember(nearOf(wrapper), nearOf(wrapper).members[0]);
    await flushPromises();
    expect(api.DismissImageNearDuplicateMember).toHaveBeenCalledWith([1, 2, 3, 4], 1);
    const entry = nearOf(wrapper);
    expect(entry.members.map(member => member.id)).toEqual([2, 3, 4]);
    expect(wrapper.vm.keepFor(entry)).toBe(3);
    expect(wrapper.vm.protectedIDs.has(3)).toBe(true);
    expect(wrapper.vm.protectedIDs.has(2)).toBe(false);
    expect(wrapper.find('[data-test="cleanup-group-exhausted"]').exists()).toBe(false);

    api.MergeMediaMetadata.mockClear();
    api.DeleteImagesWithResult.mockClear();
    await deleteSelection(wrapper, [4]);
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['image', 3, [4], FULL_MERGE]]);
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([4], true, expect.any(String));
    wrapper.unmount();
  });

  it('IMG-05 移出后本组剩下的都已删：组上说明原因，成员不可勾，不进入合并与删除', async () => {
    feedback.confirmAction.mockResolvedValue(false).mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    const wrapper = await mountWith(statusWith({
      duplicate_groups: [exactGroup()],
      near_duplicate_groups: [{ original: makeMember(1, 'a.jpg'), candidates: [makeMember(2, 'b.jpg'), makeMember(3, 'c.jpg')], reason: '接近' }]
    }));
    await deleteSelection(wrapper, [2, 3]);
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['image', 1, [2, 3], FULL_MERGE]]);

    await wrapper.vm.removeMember(nearOf(wrapper), nearOf(wrapper).members[0]);
    await flushPromises();
    const entry = nearOf(wrapper);
    expect(entry.members.map(member => member.id)).toEqual([2, 3]);
    expect(wrapper.vm.isExhausted(entry)).toBe(true);
    expect(wrapper.vm.keepFor(entry)).toBeNull();
    await showKind(wrapper, 'near');
    const card = wrapper.findAll('[data-test="cleanup-group-card"]').find(node => node.attributes('data-kind') === 'near');
    expect(card.get('[data-test="cleanup-group-exhausted"]').text()).toContain('本组剩下的都已移到废纸篓');
    expect(card.get('[data-test="cleanup-group-outcome"]').text()).toBe('本组不参与删除');
    expect(card.findAll('[data-test="cleanup-candidate-toggle"]').every(node => node.element.disabled)).toBe(true);
    expect(card.findAll('.cleanup-member--keep')).toHaveLength(0);
    expect(card.find('[data-test="cleanup-member-locked"]').exists()).toBe(false);
    expect(wrapper.vm.protectedIDs.has(2) && wrapper.vm.protectedIDs.has(3)).toBe(true);

    // 强行把这组成员放回勾选：删除前被裁掉，只删别的组，合并也只按别的组。
    api.MergeMediaMetadata.mockClear();
    api.DeleteImagesWithResult.mockClear();
    await deleteSelection(wrapper, [2, 3, 12]);
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['image', 11, [12], FULL_MERGE]]);
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([12], true, expect.any(String));
    wrapper.unmount();
  });
});

// ===== 2026-10-12：精确重复 / 疑似重复分区、每页 20 组、「本页按建议勾选」 =====
describe('精确重复与疑似重复分区', () => {
  // 第 i 组：精确组 id 1000+2i / 1000+2i+1，近似组 id 2i+1 / 2i+2；目录按组号分开，便于测折叠。
  const exactGroups = n => Array.from({ length: n }, (_, i) => ({
    original: makeMember(1000 + i * 2, `keep-${i}.jpg`), candidates: [makeMember(1001 + i * 2, `copy-${i}.jpg`)], reason: '一致'
  }));
  const nearGroups = (n, dirOf = () => '/p') => Array.from({ length: n }, (_, i) => ({
    original: makeMember(i * 2 + 1, `near-a-${i}.jpg`, { directory: dirOf(i) }),
    candidates: [makeMember(i * 2 + 2, `near-b-${i}.jpg`, { directory: dirOf(i) })],
    reason: '接近'
  }));
  const kinds = wrapper => wrapper.findAll('[data-test="cleanup-group-card"]').map(card => card.attributes('data-kind'));
  const pageButton = wrapper => wrapper.get('[data-test="cleanup-suggest-page"]');

  it('统计卡给出两类的组数、张数与已勾数，默认先看精确重复，切换不改勾选', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: exactGroups(3), near_duplicate_groups: nearGroups(2) }));
    const exactTile = wrapper.get('[data-test="cleanup-kind-exact"]');
    expect(exactTile.attributes('aria-selected')).toBe('true');
    expect(exactTile.text()).toContain('3组');
    expect(exactTile.text()).toContain('6 张 · 已勾 3 张');
    expect(wrapper.get('[data-test="cleanup-kind-near"]').text()).toContain('4 张 · 已勾 0 张');
    expect(wrapper.get('[data-test="cleanup-tile-pending"]').text()).toContain('3张');
    expect(new Set(kinds(wrapper))).toEqual(new Set(['exact']));

    await showKind(wrapper, 'near');
    expect(wrapper.get('[data-test="cleanup-kind-near"]').attributes('aria-selected')).toBe('true');
    expect(new Set(kinds(wrapper))).toEqual(new Set(['near']));
    expect(wrapper.get('[data-test="cleanup-area-bar"]').text()).toContain('疑似重复');
    expect(wrapper.vm.selection).toHaveLength(3);
    wrapper.unmount();
  });

  it('只有疑似重复时默认打开疑似重复；另一类为空时说明空态', async () => {
    const wrapper = await mountWith(statusWith({ near_duplicate_groups: nearGroups(1) }));
    expect(wrapper.vm.activeKind).toBe('near');
    expect(kinds(wrapper)).toEqual(['near']);
    await showKind(wrapper, 'exact');
    expect(wrapper.get('[data-test="cleanup-area-empty"]').text()).toContain('没有发现精确重复。');
    expect(wrapper.find('[data-test="cleanup-group-card"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('精确重复有结果、指纹一张都没算时，疑似重复区说「尚未计算」并给出补全入口', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: exactGroups(1), coverage: { perceptual_hash: { done: 0, total: 40 } } }));
    await showKind(wrapper, 'near');
    expect(wrapper.get('[data-test="cleanup-area-empty"]').text()).toContain('疑似重复尚未计算。');
    await wrapper.get('[data-test="cleanup-area-start-phash"]').trigger('click');
    await flushPromises();
    expect(api.StartImagePerceptualHashBackfill).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('两个区域各自翻页，每页 20 组，底部也能翻页', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: exactGroups(45), near_duplicate_groups: nearGroups(25) }));
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(20);
    await wrapper.get('[data-test="cleanup-page-next-bottom"]').trigger('click');
    await wrapper.get('[data-test="cleanup-page-next-bottom"]').trigger('click');
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(5);
    expect(wrapper.get('[data-test="cleanup-area-bar"]').text()).toContain('第 3 / 3 页');

    await showKind(wrapper, 'near');
    expect(wrapper.get('[data-test="cleanup-area-bar"]').text()).toContain('第 1 / 2 页');
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(20);
    await showKind(wrapper, 'exact');
    expect(wrapper.get('[data-test="cleanup-area-bar"]').text()).toContain('第 3 / 3 页');
    wrapper.unmount();
  });

  it('「本页按建议勾选」只勾本页展开着的组，再点取消这些组，不动别的页与另一区', async () => {
    // 25 组疑似重复：第 0 组单独一个目录（/a），其余在 /b；按目录排序后 /a 在第一页最前。
    const wrapper = await mountWith(statusWith({
      duplicate_groups: exactGroups(1),
      near_duplicate_groups: nearGroups(25, i => (i === 0 ? '/a' : '/b'))
    }));
    await showKind(wrapper, 'near');
    expect(wrapper.vm.selection).toEqual([1001]);
    expect(pageButton(wrapper).text()).toBe('本页按建议勾选');

    // 折叠 /a：它那一组看不到，不替用户勾。
    await wrapper.findAll('[data-test="cleanup-dir-toggle"]')[0].trigger('click');
    await pageButton(wrapper).trigger('click');
    // 本页 = /a 的 1 组 + /b 的前 19 组（第 1..19 组）；勾的是各组的非保留项（偶数 id）。
    const page1 = Array.from({ length: 19 }, (_, k) => (k + 1) * 2 + 2);
    expect(wrapper.vm.selection.slice().sort((a, b) => a - b)).toEqual([...page1, 1001].sort((a, b) => a - b));
    expect(wrapper.vm.selection).not.toContain(2);
    expect(pageButton(wrapper).text()).toBe('取消本页勾选');

    // 翻到第二页：那一页的组没被勾。
    await wrapper.get('[data-test="cleanup-page-next"]').trigger('click');
    expect(pageButton(wrapper).text()).toBe('本页按建议勾选');
    await wrapper.get('[data-test="cleanup-page-prev"]').trigger('click');

    await pageButton(wrapper).trigger('click');
    expect(wrapper.vm.selection).toEqual([1001]);
    wrapper.unmount();
  });

  it('「本页按建议勾选」跳过「本组不删」的组；精确重复默认已是建议状态，按钮直接是「取消本页勾选」', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: exactGroups(2), near_duplicate_groups: nearGroups(2) }));
    expect(pageButton(wrapper).text()).toBe('取消本页勾选');
    await pageButton(wrapper).trigger('click');
    expect(wrapper.vm.selection).toEqual([]);
    await pageButton(wrapper).trigger('click');
    expect(wrapper.vm.selection.slice().sort((a, b) => a - b)).toEqual([1001, 1003]);

    await showKind(wrapper, 'near');
    const cards = wrapper.findAll('[data-test="cleanup-group-card"]');
    await cards[0].get('[data-test="cleanup-skip-group"]').trigger('click');
    await pageButton(wrapper).trigger('click');
    expect(wrapper.vm.selection.slice().sort((a, b) => a - b)).toEqual([4, 1001, 1003]);
    wrapper.unmount();
  });

  it('同一个目录在两个区域里各自折叠', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: exactGroups(1), near_duplicate_groups: nearGroups(1) }));
    await wrapper.get('[data-test="cleanup-dir-toggle"]').trigger('click');
    expect(wrapper.find('[data-test="cleanup-group-card"]').exists()).toBe(false);
    await showKind(wrapper, 'near');
    expect(kinds(wrapper)).toEqual(['near']);
    await showKind(wrapper, 'exact');
    expect(wrapper.find('[data-test="cleanup-group-card"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('正在看的区域跨分析批次保留', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: exactGroups(1), near_duplicate_groups: nearGroups(1) }));
    await showKind(wrapper, 'near');
    photoCleanupStore.status = statusWith({ duplicate_groups: exactGroups(1), near_duplicate_groups: nearGroups(1) }, { started_at: '2026-10-12T00:00:00Z' });
    await flushPromises();
    expect(wrapper.vm.review.key).toBe('2026-10-12T00:00:00Z');
    expect(wrapper.vm.activeKind).toBe('near');
    wrapper.unmount();
  });
});

// 统一设计第 0 步（B2）：选择操作条、用词与空态。
describe('统一设计：选择操作条与用词（D-UD-10 / D-UD-14）', () => {
  it('有勾选才浮出选择操作条与「移到废纸篓」；「取消选择」清空两区勾选后，页面上没有删除动作', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] }));
    const bar = () => wrapper.find('[data-test="cleanup-selection-bar"]');
    expect(bar().exists()).toBe(true);
    expect(bar().get('[data-test="selection-bar-count"]').text()).toBe('已选 1 项');
    // 疑似重复要逐组看过才能勾（D-PC49）：条上不给「全选当前结果」。
    expect(bar().find('[data-test="selection-bar-select-all-matching"]').exists()).toBe(false);
    expect(bar().get('[data-test="cleanup-delete-selected"]').text()).toBe('移到废纸篓 (1)');

    await bar().get('[data-test="selection-bar-clear"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.review.selection).toEqual([]);
    expect(bar().exists()).toBe(false);
    expect(wrapper.find('[data-test="cleanup-delete-selected"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('Esc 取消选择；确认框开着时 Esc 只关确认框，勾选不动', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    await clickDelete(wrapper);
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(false);
    expect(wrapper.vm.review.selection).toEqual([12]);
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();
    expect(wrapper.vm.review.selection).toEqual([]);
    wrapper.unmount();
  });

  it('删除确认框打开或删除进行中时「取消选择」不改勾选', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    wrapper.vm.processing = true;
    wrapper.vm.clearSelection();
    expect(wrapper.vm.review.selection).toEqual([12]);
    wrapper.vm.processing = false;
    wrapper.vm.confirming = true;
    wrapper.vm.clearSelection();
    expect(wrapper.vm.review.selection).toEqual([12]);
    wrapper.vm.confirming = false;
    wrapper.vm.clearSelection();
    expect(wrapper.vm.review.selection).toEqual([]);
    wrapper.unmount();
  });

  it('用词：「设为保留」「在访达中显示」，页头是统一的 PageHeader', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    expect(wrapper.get('h1').text()).toBe('清理审阅');
    expect(wrapper.text()).toContain('设为保留');
    expect(wrapper.text()).not.toContain('保留这份');
    expect(wrapper.get('[data-test="cleanup-open-dir"]').text()).toBe('在访达中显示');
    expect(wrapper.findAll('[data-test="cleanup-reveal"]').every(button => button.text() === '在访达中显示')).toBe(true);
    api.OpenImageDirectory.mockRejectedValueOnce('offline');
    await wrapper.get('[data-test="cleanup-open-dir"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-error"]').text()).toBe('在访达中显示失败：offline');
    wrapper.unmount();
  });

  it('还没分析时的空态给出「开始分析」', async () => {
    const wrapper = await mountWith({ running: false, completed: false, cancelled: true, progress: { stage: 'done' } });
    api.StartImageCleanupAnalysis.mockResolvedValueOnce({ running: true, completed: false, analysis: null });
    await wrapper.get('[data-test="cleanup-idle-start"]').trigger('click');
    await flushPromises();
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    wrapper.unmount();
    stopPhotoCleanupPolling();
  });

  it('分析中用统一的进度条报进度', async () => {
    const wrapper = await mountWith({ running: true, completed: false, progress: { stage: 'near', current: 3, total: 12 } });
    const bar = wrapper.get('[data-test="cleanup-progress"] [role="progressbar"]');
    expect(bar.attributes('aria-valuenow')).toBe('25');
    expect(wrapper.get('[data-test="cleanup-progress"]').text()).toContain('已处理 3 / 12');
    wrapper.unmount();
    stopPhotoCleanupPolling();
  });

  it('查看器关闭按钮在右上角、带「关闭」读屏名，← → 切换，Esc 退出', async () => {
    const wrapper = await mountWith(statusWith({ duplicate_groups: [exactGroup()] }));
    await wrapper.findAll('[data-test="cleanup-member-open"]')[0].trigger('click');
    const viewer = () => wrapper.find('[data-test="cleanup-viewer"]');
    expect(viewer().get('.cleanup-viewer__close').attributes('aria-label')).toBe('关闭');
    expect(viewer().get('.cleanup-viewer__img').attributes('src')).toBe('/preview/image/11');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    await flushPromises();
    expect(viewer().get('.cleanup-viewer__img').attributes('src')).toBe('/preview/image/12');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(viewer().exists()).toBe(false);
    wrapper.unmount();
  });
});
