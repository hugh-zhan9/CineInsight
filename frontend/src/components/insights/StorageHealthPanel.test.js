import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import StorageHealthPanel from './StorageHealthPanel.vue';
import { feedbackState, resetFeedback } from '../../utils/feedback.js';

const GIB = 1024 ** 3;
const item = (id, size, extra = {}) => ({
  id, title: `片子 ${id}`, path: `/lib/${id}.mkv`, directory: '/lib', size, duration: 7200, width: 1920, height: 1080,
  codec: 'H.264', bit_rate: 20e6, last_watched_at: null, added_at: '2023-01-02T00:00:00Z', is_favorite: false, personal_rating: null, ...extra
});

function report(extra = {}) {
  return {
    generated_at: '2026-10-12T00:00:00Z',
    videos: { count: 10, bytes: 100 * GIB },
    images: { count: 3, bytes: 3 * GIB },
    probe: { probed: 8, total: 10 },
    by_codec: [{ label: 'H.264', count: 8, bytes: 80 * GIB }],
    by_resolution: [{ label: '1080p', count: 10, bytes: 100 * GIB }],
    by_directory: [{ label: '/lib', count: 10, bytes: 100 * GIB }],
    unwatched_criteria: { min_bytes: 2 * GIB, months: 12, cutoff: '2025-10-12T00:00:00Z' },
    unwatched: { count: 3, bytes: 30 * GIB, savings: 0, items: [item(1, 20 * GIB, { is_favorite: true }), item(2, 6 * GIB), item(3, 4 * GIB)], has_more: false },
    transcode: { count: 1, bytes: 20 * GIB, savings: 10 * GIB, items: [item(1, 20 * GIB, { estimated_size: 10 * GIB, estimated_savings: 10 * GIB })], has_more: false },
    ...extra
  };
}

function okResult(ids) {
  return { batch_id: 'batch-1', requested: ids.length, succeeded: ids.length, failed: 0, cancelled: 0, items: ids.map(id => ({ id, code: 'ok' })) };
}

async function mountPanel() {
  const wrapper = mount(StorageHealthPanel, { attachTo: document.body });
  await flushPromises();
  return wrapper;
}

const tileText = (wrapper, key) => wrapper.get(`[data-test="storage-tile-${key}"]`).text();

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  document.body.innerHTML = '';
  api.GetStorageReport.mockResolvedValue(report());
  api.GetCleanupStatus.mockResolvedValue({
    running: false, completed: true, stale: false,
    analysis: { duplicate_groups: [{ original: { id: 50, size: GIB }, candidates: [{ id: 51, size: GIB }] }] }
  });
  api.GetImageCleanupStatus.mockResolvedValue({ running: false, completed: false, analysis: null });
  api.DeleteVideosWithResult.mockImplementation(ids => Promise.resolve(okResult(ids)));
});

describe('存储体检', () => {
  it('按默认门槛取数，统计卡给出总占用、可回收、久未观看与转码可省', async () => {
    const wrapper = await mountPanel();
    expect(api.GetStorageReport).toHaveBeenCalledWith({
      unwatched_min_bytes: 2 * GIB, unwatched_months: 12, unwatched_limit: 50, transcode_limit: 50
    });
    expect(tileText(wrapper, 'total')).toContain('103.0 GB');
    expect(tileText(wrapper, 'total')).toContain('视频 10 部 · 图片 3 张');
    expect(tileText(wrapper, 'reclaim')).toContain('1.0 GB');
    expect(tileText(wrapper, 'reclaim')).toContain('确定 1.0 GB');
    expect(tileText(wrapper, 'unwatched')).toContain('30.0 GB');
    expect(tileText(wrapper, 'transcode')).toContain('≈ 10.0 GB');
    // 图片还没分析过：说明打开清理审阅会自动开始。
    expect(wrapper.get('[data-test="storage-reclaim-image"]').text()).toContain('还没分析过');
    expect(wrapper.get('[data-test="storage-probe-note"]').text()).toContain('还有 2 部视频没有技术信息');
    expect(wrapper.text()).toContain('没看过 · 2023/1/2 入库');
    wrapper.unmount();
  });

  it('清理中心入口按固定命令 ID 交给宿主页', async () => {
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="storage-open-video-cleanup"]').trigger('click');
    await wrapper.get('[data-test="storage-open-image-cleanup"]').trigger('click');
    expect(wrapper.emitted('run-command')).toEqual([
      ['library.openCleanup', '打开视频清理中心'],
      ['photos.openCleanup', '打开图片清理审阅']
    ]);
    wrapper.unmount();
  });

  it('勾选后底部出现选择操作条（总大小、收藏或评过分的提醒）；「删除前确认」开着（默认）先汇总确认，确认才移到废纸篓，并通知片库刷新', async () => {
    const wrapper = await mountPanel();
    // 没勾选时页面上没有批量动作（D-UD-10）。
    expect(wrapper.find('[data-test="storage-selection-bar"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="storage-delete-selected"]').exists()).toBe(false);
    await wrapper.get('[data-test="storage-select-all"]').setValue(true);
    expect(wrapper.get('[data-test="selection-bar-count"]').text()).toBe('已选当前结果全部 3 项');
    expect(wrapper.get('[data-test="storage-selected-summary"]').text()).toBe('共 30.0 GB');
    expect(wrapper.get('[data-test="storage-selected-curated"]').text()).toContain('其中 1 部已收藏或评过分');
    expect(wrapper.get('[data-test="storage-delete-selected"]').text()).toBe('移到废纸篓（3）');
    await wrapper.get('[data-test="storage-delete-selected"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="storage-delete-summary"]').text()).toBe('将把 3 个视频移到废纸篓，共 30.0 GB。');
    expect(wrapper.get('[data-test="storage-delete-curated"]').text()).toContain('其中 1 个已收藏或评过分');

    api.GetStorageReport.mockResolvedValue(report({ unwatched: { count: 0, bytes: 0, savings: 0, items: [], has_more: false } }));
    await wrapper.get('[data-test="storage-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([1, 2, 3], true, expect.any(String));
    expect(wrapper.emitted('videos-changed')).toEqual([[{ removedIDs: [1, 2, 3] }]]);
    expect(api.GetStorageReport).toHaveBeenCalledTimes(2);
    expect(wrapper.vm.selection).toEqual([]);
    wrapper.unmount();
  });

  it('取消确认时什么都不删', async () => {
    const wrapper = await mountPanel();
    await wrapper.findAll('[data-test="storage-unwatched-toggle"]')[1].setValue(true);
    await wrapper.get('[data-test="storage-delete-selected"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="storage-delete-cancel"]').trigger('click');
    await flushPromises();
    expect(api.DeleteVideosWithResult).not.toHaveBeenCalled();
    expect(wrapper.emitted('videos-changed')).toBeUndefined();
    expect(wrapper.vm.selection).toEqual([2]);
    wrapper.unmount();
  });

  it('设置「删除前确认」关了：不弹确认，直接移到废纸篓（可撤销，D-UD-07 第 1 级），只处理此刻清单里勾着的行', async () => {
    api.GetSettings.mockResolvedValueOnce({ confirm_before_delete: false });
    const wrapper = await mountPanel();
    await wrapper.findAll('[data-test="storage-unwatched-toggle"]')[1].setValue(true);
    await wrapper.findAll('[data-test="storage-unwatched-toggle"]')[2].setValue(true);
    // 例如之前发出的取数此时回来，3 号片不在新清单里，它的勾选随之放掉。
    api.GetStorageReport.mockResolvedValue(report({
      unwatched: { count: 2, bytes: 26 * GIB, savings: 0, items: [item(1, 20 * GIB), item(2, 6 * GIB)], has_more: false }
    }));
    await wrapper.vm.load();
    await flushPromises();
    await wrapper.get('[data-test="storage-delete-selected"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="storage-delete-confirm-dialog"]').exists()).toBe(false);
    expect(feedbackState.confirm).toBe(null);
    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([2], true, expect.any(String));
    expect(wrapper.emitted('videos-changed')).toEqual([[{ removedIDs: [2] }]]);
    wrapper.unmount();
  });

  it('全选当前结果（N）的 N 是列出的部数；取消选择（Esc）清掉勾选，什么都不删', async () => {
    const wrapper = await mountPanel();
    await wrapper.findAll('[data-test="storage-unwatched-toggle"]')[1].setValue(true);
    expect(wrapper.get('[data-test="selection-bar-count"]').text()).toBe('已选 1 项');
    expect(wrapper.get('[data-test="selection-bar-select-all-matching"]').text()).toBe('全选当前结果（3）');
    await wrapper.get('[data-test="selection-bar-select-all-matching"]').trigger('click');
    expect(wrapper.vm.selection).toEqual([1, 2, 3]);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(wrapper.vm.selection).toEqual([]);
    expect(wrapper.find('[data-test="storage-selection-bar"]').exists()).toBe(false);
    expect(api.DeleteVideosWithResult).not.toHaveBeenCalled();
    expect(wrapper.emitted('videos-changed')).toBeUndefined();
    wrapper.unmount();
  });

  it('换门槛后重取，看不到的勾选随之放掉，不会悄悄跟着删', async () => {
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="storage-select-all"]').setValue(true);
    api.GetStorageReport.mockResolvedValue(report({
      unwatched: { count: 1, bytes: 20 * GIB, savings: 0, items: [item(1, 20 * GIB)], has_more: false }
    }));
    await wrapper.get('[data-test="storage-unwatched-size"]').setValue('10');
    await flushPromises();
    expect(api.GetStorageReport).toHaveBeenLastCalledWith({
      unwatched_min_bytes: 10 * GIB, unwatched_months: 12, unwatched_limit: 50, transcode_limit: 50
    });
    expect(wrapper.vm.selection).toEqual([1]);
    await wrapper.get('[data-test="storage-unwatched-months"]').setValue('36');
    await flushPromises();
    expect(api.GetStorageReport).toHaveBeenLastCalledWith(expect.objectContaining({ unwatched_months: 36 }));
    wrapper.unmount();
  });

  it('「显示更多」只展开当前清单', async () => {
    api.GetStorageReport.mockResolvedValue(report({ transcode: { ...report().transcode, count: 80, has_more: true } }));
    const wrapper = await mountPanel();
    expect(wrapper.get('[data-test="storage-transcode-more"]').text()).toBe('显示更多（还有 79 部）');
    await wrapper.get('[data-test="storage-transcode-more"]').trigger('click');
    await flushPromises();
    expect(api.GetStorageReport).toHaveBeenLastCalledWith(expect.objectContaining({ unwatched_limit: 50, transcode_limit: 100 }));
    wrapper.unmount();
  });

  it('点片名打开片库详情，「补全技术信息」启动补全', async () => {
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="storage-unwatched-row"] .storage-row__title').trigger('click');
    expect(wrapper.emitted('open-video-id')).toEqual([[1]]);
    await wrapper.get('[data-test="storage-start-probe"]').trigger('click');
    await flushPromises();
    expect(api.StartTechnicalBackfill).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('读取失败时说出来', async () => {
    api.GetStorageReport.mockRejectedValue('数据库繁忙');
    const wrapper = await mountPanel();
    expect(wrapper.get('[data-test="storage-error"]').text()).toBe('读取存储体检失败：数据库繁忙');
    wrapper.unmount();
  });
});

describe('存储体检的删除保护（审查 I-2 / I-3）', () => {
  it('重新取数期间勾选与删除都锁住；取完才能删', async () => {
    const wrapper = await mountPanel();
    let finish;
    api.GetStorageReport.mockImplementationOnce(() => new Promise(resolve => { finish = () => resolve(report()); }));
    const pending = wrapper.vm.load();
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="storage-select-all"]').element.disabled).toBe(true);
    expect(wrapper.findAll('[data-test="storage-unwatched-toggle"]').every(node => node.element.disabled)).toBe(true);
    wrapper.vm.toggleSelection(1);
    wrapper.vm.selectAllListed();
    wrapper.vm.askDelete();
    await wrapper.vm.deleteSelected();
    expect(wrapper.vm.selection).toEqual([]);
    expect(wrapper.find('[data-test="storage-delete-confirm-dialog"]').exists()).toBe(false);
    expect(api.DeleteVideosWithResult).not.toHaveBeenCalled();
    finish();
    await pending;
    await flushPromises();
    expect(wrapper.get('[data-test="storage-select-all"]').element.disabled).toBe(false);
    wrapper.unmount();
  });

  it('确认框打开期间清单变了（勾着的行不在了）：中止，不删任何视频', async () => {
    const wrapper = await mountPanel();
    await wrapper.findAll('[data-test="storage-unwatched-toggle"]')[2].setValue(true);
    await wrapper.get('[data-test="storage-delete-selected"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="storage-delete-confirm-dialog"]').exists()).toBe(true);
    // 例如对话框打开前已经发出的取数此时回来，3 号片不在新清单里。
    api.GetStorageReport.mockResolvedValue(report({
      unwatched: { count: 2, bytes: 26 * GIB, savings: 0, items: [item(1, 20 * GIB), item(2, 6 * GIB)], has_more: false }
    }));
    await wrapper.vm.load();
    await wrapper.get('[data-test="storage-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.DeleteVideosWithResult).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="storage-error"]').text()).toContain('确认期间清单或勾选有变化');
    wrapper.unmount();
  });

  it('删除进行中报告 processing，跳转入口都锁住；删完扣掉「可回收」里刚删的', async () => {
    api.GetStorageReport.mockResolvedValue(report({
      unwatched: { count: 1, bytes: GIB, savings: 0, items: [item(51, GIB)], has_more: false }
    }));
    let finishDelete;
    api.DeleteVideosWithResult.mockImplementation(ids => new Promise(resolve => { finishDelete = () => resolve(okResult(ids)); }));
    const wrapper = await mountPanel();
    const reclaimValue = () => wrapper.get('[data-test="storage-tile-reclaim"] [data-test="stat-tile-value"]').text();
    expect(reclaimValue()).toBe('1.0 GB');
    await wrapper.get('[data-test="storage-select-all"]').setValue(true);
    await wrapper.get('[data-test="storage-delete-selected"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="storage-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(wrapper.emitted('status-change').at(-1)).toEqual([{ loading: false, processing: true }]);
    expect(wrapper.get('[data-test="storage-open-video-cleanup"]').element.disabled).toBe(true);
    expect(wrapper.get('[data-test="storage-unwatched-row"] .storage-row__title').element.disabled).toBe(true);

    finishDelete();
    await flushPromises();
    expect(wrapper.emitted('status-change').at(-1)).toEqual([{ loading: false, processing: false }]);
    // 51 号是清理分析里唯一的可回收项；清理分析还没重跑，这里先扣掉。
    expect(reclaimValue()).toBe('0 B');
    wrapper.unmount();
  });

  it('换门槛只重取体检数据，不重取清理分析结果；「刷新」两样都重取', async () => {
    const wrapper = await mountPanel();
    expect(api.GetCleanupStatus).toHaveBeenCalledTimes(1);
    await wrapper.get('[data-test="storage-unwatched-months"]').setValue('24');
    await flushPromises();
    expect(api.GetStorageReport).toHaveBeenCalledTimes(2);
    expect(api.GetCleanupStatus).toHaveBeenCalledTimes(1);
    await wrapper.vm.refresh();
    expect(api.GetCleanupStatus).toHaveBeenCalledTimes(2);
    expect(api.GetImageCleanupStatus).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it('清单已列满 500 项时不再给「显示更多」，改说只列前 500', async () => {
    const many = Array.from({ length: 500 }, (_, i) => item(i + 1, 3 * GIB));
    api.GetStorageReport.mockResolvedValue(report({ unwatched: { count: 900, bytes: 0, savings: 0, items: many, has_more: true } }));
    const wrapper = await mountPanel();
    expect(wrapper.find('[data-test="storage-unwatched-more"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="storage-unwatched-capped"]').text()).toContain('最多列出前 500 部');
    wrapper.unmount();
  });
});

describe('存储体检复审修复（部分失败、撤销中、清理结果乱序）', () => {
  it('部分没移到废纸篓：失败原因在刷新之后仍然留着', async () => {
    api.DeleteVideosWithResult.mockResolvedValue({
      batch_id: 'batch-1', requested: 2, succeeded: 1, failed: 1, cancelled: 0,
      items: [{ id: 1, code: 'ok' }, { id: 2, code: 'error', message: '文件被占用' }]
    });
    const wrapper = await mountPanel();
    await wrapper.findAll('[data-test="storage-unwatched-toggle"]')[0].setValue(true);
    await wrapper.findAll('[data-test="storage-unwatched-toggle"]')[1].setValue(true);
    await wrapper.get('[data-test="storage-delete-selected"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="storage-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.GetStorageReport).toHaveBeenCalledTimes(2);
    expect(wrapper.get('[data-test="storage-error"]').text()).toContain('有 1 个视频没有移到废纸篓');
    expect(wrapper.emitted('videos-changed')).toEqual([[{ removedIDs: [1] }]]);
    wrapper.unmount();
  });

  it('撤销条正在撤销时也报 processing，宿主据此不让切走', async () => {
    const wrapper = await mountPanel();
    await wrapper.findAll('[data-test="storage-unwatched-toggle"]')[0].setValue(true);
    wrapper.vm.$refs.trashUndo.undoing = true;
    await flushPromises();
    expect(wrapper.emitted('status-change').at(-1)).toEqual([{ loading: false, processing: true }]);
    expect(wrapper.get('[data-test="storage-delete-selected"]').element.disabled).toBe(true);
    wrapper.vm.$refs.trashUndo.undoing = false;
    await flushPromises();
    expect(wrapper.emitted('status-change').at(-1)).toEqual([{ loading: false, processing: false }]);
    wrapper.unmount();
  });

  it('两次刷新的清理结果乱序返回时，留下的是后一次的', async () => {
    const wrapper = await mountPanel();
    const older = { running: false, completed: true, analysis: { duplicate_groups: [{ original: { id: 60, size: GIB }, candidates: [{ id: 61, size: 5 * GIB }] }] } };
    const newer = { running: false, completed: false, analysis: null };
    let finishOlder;
    api.GetCleanupStatus
      .mockImplementationOnce(() => new Promise(resolve => { finishOlder = () => resolve(older); }))
      .mockResolvedValueOnce(newer);
    const first = wrapper.vm.loadCleanup();
    await wrapper.vm.loadCleanup();
    finishOlder();
    await first;
    await flushPromises();
    expect(wrapper.get('[data-test="storage-reclaim-video"]').text()).toContain('还没分析过');
    wrapper.unmount();
  });
});
