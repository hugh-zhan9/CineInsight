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

import LostMediaPanel from './LostMediaPanel.vue';
import { feedbackState, resetFeedback, resolveConfirm } from '../../utils/feedback.js';

const item = (id, extra = {}) => ({ id, name: `${id}.mp4`, path: `/old/a/${id}.mp4`, directory: '/old/a', size: 10, source: 'stale', reason: 'missing', purgeable: true, ...extra });

function report(kind = 'video') {
  return {
    kind, total: 3, total_size: 30, purgeable: 2, reasons: { missing: 2, offline: 1 },
    roots: [
      { root: '/old', label: '旧盘', state: 'active', count: 2, size: 20, purgeable: 2, reasons: { missing: 2 },
        directories: [{ directory: '/old/a', count: 2, size: 20, purgeable: 2, reasons: { missing: 2 }, samples: [item(1), item(2)] }] },
      { root: '/Volumes/X', label: '外接盘', state: 'active', count: 1, size: 10, purgeable: 0, reasons: { offline: 1 },
        directories: [{ directory: '/Volumes/X/m', count: 1, size: 10, purgeable: 0, reasons: { offline: 1 }, samples: [] }] }
    ]
  };
}

async function mountPanel() {
  const wrapper = mount(LostMediaPanel, { attachTo: document.body });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  document.body.innerHTML = '';
  api.GetLostMediaReport.mockImplementation(kind => Promise.resolve(report(kind)));
  api.ListLostMediaItems.mockResolvedValue({ items: [item(1), item(2)], has_more: false });
  api.GetOrphanFileReport.mockResolvedValue({ locations: [{ key: 'thumbnails', label: '视频缩略图与拖动预览帧', files: 2, bytes: 2048 }], total_files: 2, total_bytes: 2048 });
  api.SelectDirectory.mockResolvedValue('/new');
});

describe('失联数据面板（D-LD-*）', () => {
  it('按根 → 目录分组，带原因与可清除标记；卷未挂载的目录只展示、不给操作', async () => {
    const wrapper = await mountPanel();
    expect(api.GetLostMediaReport).toHaveBeenCalledWith('video');
    expect(wrapper.get('[data-test="lost-summary"]').text()).toContain('可找回或清除 2');
    const dirs = wrapper.findAll('[data-test="lost-directory"]');
    expect(dirs).toHaveLength(2);
    expect(dirs[0].text()).toContain('可清除 2');
    expect(dirs[1].text()).toContain('卷未挂载 1');
    expect(dirs[1].find('[data-test="lost-dir-wait"]').exists()).toBe(true);
    expect(dirs[1].get('[data-test="lost-dir-purge"]').attributes('disabled')).toBeDefined();
    expect(dirs[1].get('[data-test="lost-dir-move"]').attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="lost-kind-image"]').trigger('click');
    await flushPromises();
    expect(api.GetLostMediaReport).toHaveBeenLastCalledWith('image');
    wrapper.unmount();
  });

  it('目录搬家：选新位置 → 预览默认勾选可靠配对，不可执行的不能勾；执行后通知宿主并重读报告', async () => {
    api.PreviewLostRelinkMove.mockResolvedValue({ kind: 'video', unmatched: 1, items: [
      { id: 1, name: '1.mp4', old_path: '/old/a/1.mp4', new_path: '/new/1.mp4', match: 'exact', selectable: true, selected: true, candidates: [] },
      { id: 2, name: '2.mp4', old_path: '/old/a/2.mp4', new_path: '/new/2.mp4', match: 'size_mismatch', selectable: false, selected: false, candidates: [] },
      { id: 3, name: '3.mp4', old_path: '/old/a/3.mp4', new_path: '/elsewhere/3.mp4', match: 'exact', outside_roots: true, selectable: false, selected: false, candidates: [] }
    ] });
    api.ApplyLostRelink.mockResolvedValue({ done: 1, skipped: 0, failed: 0, cancelled: false, items: [] });
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="lost-dir-move"]').trigger('click');
    await flushPromises();
    expect(api.PreviewLostRelinkMove).toHaveBeenCalledWith({ kind: 'video', old_directory: '/old/a', new_directory: '/new' });
    const toggles = wrapper.findAll('[data-test="lost-relink-toggle"]');
    expect(toggles.map(t => t.element.checked)).toEqual([true, false, false]);
    expect(toggles[1].attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="lost-relink-outside-hint"]').text()).toContain('先把这个文件夹加为扫描目录');
    await wrapper.get('[data-test="lost-relink-apply"]').trigger('click');
    await flushPromises();
    expect(api.ApplyLostRelink).toHaveBeenCalledWith({ kind: 'video', pairs: [{ id: 1, new_path: '/new/1.mp4' }] });
    expect(wrapper.get('[data-test="lost-relink-result"]').text()).toContain('已找回 1 条');
    expect(wrapper.emitted('videos-changed')).toEqual([[{ kind: 'video', removedIDs: [] }]]);
    expect(api.GetLostMediaReport).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it('从文件夹找回：选择操作条只处理勾选的条目，目录行上的按钮处理整个目录；取消选文件夹什么都不做', async () => {
    api.PreviewLostRelinkSearch.mockResolvedValue({ kind: 'video', unmatched: 0, items: [] });
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="lost-dir-expand"]').trigger('click');
    await flushPromises();
    // 没勾选时页面上没有批量动作（D-UD-10）。
    expect(wrapper.find('[data-test="lost-selection-bar"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="lost-items"]').text()).not.toContain('不勾选时');
    await wrapper.findAll('[data-test="lost-item-toggle"]')[1].setValue(true);
    expect(wrapper.get('[data-test="selection-bar-count"]').text()).toBe('已选 1 项');
    await wrapper.get('[data-test="lost-selection-search"]').trigger('click');
    await flushPromises();
    expect(api.PreviewLostRelinkSearch).toHaveBeenCalledWith({ kind: 'video', directory: '/new', item_ids: [2] });
    // 取消勾选后，目录行上的按钮按整个目录处理，不跟着勾选变。
    await wrapper.get('[data-test="selection-bar-clear"]').trigger('click');
    await wrapper.get('[data-test="lost-dir-search"]').trigger('click');
    await flushPromises();
    expect(api.PreviewLostRelinkSearch).toHaveBeenLastCalledWith({ kind: 'video', directory: '/new', item_ids: [1, 2] });
    api.SelectDirectory.mockResolvedValueOnce('');
    await wrapper.get('[data-test="lost-search-all"]').trigger('click');
    await flushPromises();
    expect(api.PreviewLostRelinkSearch).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  // I-3：勾了几条再点目录行上的「清除…」很容易以为只处理勾选的——有勾选时行上处理整个目录的按钮都禁用并说明，
  // 别的目录不受影响；勾选清掉后照旧处理整个目录。
  it('目录里有勾选时，这一行的「目录搬家」「从文件夹找回…」「清除…」禁用并提示用底部操作条；没勾选时照旧', async () => {
    const twoDirs = report();
    twoDirs.roots[0].directories.push({ directory: '/old/b', count: 1, size: 10, purgeable: 1, reasons: { missing: 1 }, samples: [] });
    api.GetLostMediaReport.mockResolvedValueOnce(twoDirs);
    api.PreviewLostPurge.mockResolvedValue({ kind: 'video', ids: [1, 2], count: 2, size: 20, not_purgeable: 0, skipped: {} });
    const wrapper = await mountPanel();
    const row = index => wrapper.findAll('[data-test="lost-directory"]')[index];
    const rowButtons = index => ['lost-dir-move', 'lost-dir-search', 'lost-dir-purge'].map(name => row(index).get(`[data-test="${name}"]`));
    await row(0).get('[data-test="lost-dir-expand"]').trigger('click');
    await flushPromises();
    expect(rowButtons(0).map(button => button.element.disabled)).toEqual([false, false, false]);
    expect(row(0).get('[data-test="lost-dir-purge"]').attributes('title')).toContain('清除这个目录里全部 2 条');

    await wrapper.findAll('[data-test="lost-item-toggle"]')[0].setValue(true);
    expect(rowButtons(0).map(button => button.element.disabled)).toEqual([true, true, true]);
    expect(rowButtons(0).map(button => button.attributes('title'))).toEqual(Array(3).fill('已勾选条目，请用底部操作条'));
    // 别的目录没有勾选，不受影响。
    expect(rowButtons(1).map(button => button.element.disabled)).toEqual([false, false, false]);
    await row(0).get('[data-test="lost-dir-purge"]').trigger('click');
    await flushPromises();
    expect(api.PreviewLostPurge).not.toHaveBeenCalled();

    // 取消勾选后，行上的「清除…」照旧处理整个目录。
    await wrapper.get('[data-test="selection-bar-clear"]').trigger('click');
    expect(rowButtons(0).map(button => button.element.disabled)).toEqual([false, false, false]);
    await row(0).get('[data-test="lost-dir-purge"]').trigger('click');
    await flushPromises();
    expect(api.PreviewLostPurge).toHaveBeenCalledWith({ kind: 'video', ids: [1, 2], confirm: '' });
    wrapper.unmount();
  });

  it('全选当前结果（N）：N 是这个目录里可处理的条数，含还没加载的；Esc 取消选择', async () => {
    api.ListLostMediaItems
      .mockResolvedValueOnce({ items: [item(1), item(2)], has_more: true })
      .mockResolvedValueOnce({ items: [item(1), item(2), item(5)], has_more: false });
    api.PreviewLostPurge.mockResolvedValue({ kind: 'video', ids: [1, 2, 5], count: 3, size: 30, not_purgeable: 0, skipped: {} });
    const report3 = report();
    report3.roots[0].directories[0].purgeable = 3;
    api.GetLostMediaReport.mockResolvedValueOnce(report3);
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="lost-dir-expand"]').trigger('click');
    await flushPromises();
    await wrapper.findAll('[data-test="lost-item-toggle"]')[0].setValue(true);
    expect(wrapper.get('[data-test="selection-bar-select-all-matching"]').text()).toBe('全选当前结果（3）');
    await wrapper.get('[data-test="selection-bar-select-all-matching"]').trigger('click');
    expect(wrapper.get('[data-test="selection-bar-count"]').text()).toBe('已选当前结果全部 3 项');
    expect(wrapper.findAll('[data-test="lost-item-toggle"]').map(toggle => toggle.element.checked)).toEqual([true, true]);
    await wrapper.get('[data-test="lost-selection-purge"]').trigger('click');
    await flushPromises();
    // 「全部」按目录逐页读完，不是只拿已加载的两条。
    expect(api.PreviewLostPurge).toHaveBeenCalledWith({ kind: 'video', ids: [1, 2, 5], confirm: '' });
    await wrapper.get('[data-test="lost-purge-close"]').trigger('click');
    await flushPromises();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(wrapper.find('[data-test="lost-selection-bar"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('清除：列出连带影响，先备份交给宿主，在不可逆确认框里输入「清除记录」才执行；结果按原因列出跳过', async () => {
    api.ListLostMediaItems.mockResolvedValue({ items: [item(1), item(2), item(3)], has_more: false });
    api.PreviewLostPurge.mockResolvedValue({ kind: 'video', ids: [1, 2], count: 2, size: 20, not_purgeable: 1, skipped: { file_present: 1 },
      play_events: 7, tag_links: 3, person_links: 1, collection_links: 0, version_groups_dissolved: 1, version_groups_shrunk: 0,
      bookmarks: 2, diary_entries: 1, derived_bytes: 4096 });
    api.ApplyLostPurge.mockResolvedValue({ done: 1, skipped: 1, failed: 0, cancelled: false, items: [{ id: 2, outcome: 'skipped', reason: 'volume_offline' }] });
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="lost-dir-purge"]').trigger('click');
    await flushPromises();
    expect(api.ListLostMediaItems).toHaveBeenCalledWith({ kind: 'video', directory: '/old/a', after_id: 0, limit: 500 });
    expect(api.PreviewLostPurge).toHaveBeenCalledWith({ kind: 'video', ids: [1, 2, 3], confirm: '' });
    expect(wrapper.get('[data-test="lost-purge-not-purgeable"]').text()).toContain('文件还在原处 1 条');
    expect(wrapper.get('[data-test="lost-purge-impact-plays"]').text()).toContain('7 条');
    expect(wrapper.get('[data-test="lost-purge-impact-versions"]').text()).toContain('解散 1 个');
    expect(wrapper.get('[data-test="lost-purge-impact-notes"]').text()).toContain('3 条');
    await wrapper.get('[data-test="lost-purge-backup"]').trigger('click');
    expect(wrapper.emitted('run-command')).toEqual([['action:backup-now', '立即备份数据库']]);
    const apply = wrapper.get('[data-test="lost-purge-apply"]');
    // 按钮上的字就是要输入的确认词（D-UD-07 第 3 级）。
    expect(apply.text()).toBe('清除记录');
    await apply.trigger('click');
    expect(feedbackState.confirm).toMatchObject({ level: 'irreversible', requiredText: '清除记录', confirmText: '清除记录', danger: true });
    expect(feedbackState.confirm.message).toContain('将清除 2 条视频记录');
    expect(feedbackState.confirm.impact).toContain('不能撤销');
    // 不确认就不执行。
    resolveConfirm(false);
    await flushPromises();
    expect(api.ApplyLostPurge).not.toHaveBeenCalled();
    await apply.trigger('click');
    resolveConfirm(true);
    await flushPromises();
    // 只执行预览时通过复核的条目：3 号预览时文件还在，确认前即使变得可清除也不处理。
    expect(api.ApplyLostPurge).toHaveBeenCalledWith({ kind: 'video', ids: [1, 2], confirm: '清除记录' });
    expect(wrapper.get('[data-test="lost-purge-result"]').text()).toContain('已清除 1 条，跳过 1 条');
    expect(wrapper.get('[data-test="lost-purge-skip-reason"]').text()).toContain('所在的卷没挂载：1 条');
    expect(wrapper.emitted('videos-changed')).toEqual([[{ kind: 'video', removedIDs: [1] }]]);
    wrapper.unmount();
  });

  it('孤儿文件：按位置列出，勾选后回收并重新统计', async () => {
    api.ReclaimOrphanFiles.mockResolvedValue({ files: 2, bytes: 2048, failed: 0, cancelled: false });
    const wrapper = await mountPanel();
    expect(wrapper.get('[data-test="lost-orphan-row"]').text()).toContain('2 个');
    await wrapper.get('[data-test="lost-orphan-toggle-thumbnails"]').setValue(true);
    api.GetOrphanFileReport.mockResolvedValue({ locations: [{ key: 'thumbnails', label: '视频缩略图与拖动预览帧', files: 0, bytes: 0 }], total_files: 0, total_bytes: 0 });
    await wrapper.get('[data-test="lost-orphans-reclaim"]').trigger('click');
    await flushPromises();
    expect(api.ReclaimOrphanFiles).toHaveBeenCalledWith(['thumbnails']);
    expect(wrapper.get('[data-test="lost-orphans-result"]').text()).toContain('已回收 2 个文件');
    expect(wrapper.find('[data-test="lost-orphans-empty"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('后端忙或带路径的错误：只显示固定说明，不显示原文', async () => {
    api.GetLostMediaReport.mockRejectedValueOnce(new Error('open /Volumes/A/secret: permission denied'));
    const wrapper = await mountPanel();
    expect(wrapper.get('[data-test="lost-error"]').text()).toBe('读取失联条目失败，请稍后重试。');
    wrapper.unmount();
  });

  it('图片清除后按 kind: image 通知宿主（宿主提示图片库刷新）', async () => {
    api.ListLostMediaItems.mockResolvedValue({ items: [item(1, { name: '1.jpg' })], has_more: false });
    api.PreviewLostPurge.mockResolvedValue({ kind: 'image', ids: [1], count: 1, size: 10, not_purgeable: 0, skipped: {}, image_view_events: 2, derived_bytes: 0 });
    api.ApplyLostPurge.mockResolvedValue({ done: 1, skipped: 0, failed: 0, cancelled: false, items: [] });
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="lost-kind-image"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="lost-dir-purge"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="lost-purge-impact-views"]').text()).toContain('2 条');
    await wrapper.get('[data-test="lost-purge-apply"]').trigger('click');
    expect(feedbackState.confirm.title).toBe('清除失联图片记录');
    resolveConfirm(true);
    await flushPromises();
    expect(api.ApplyLostPurge).toHaveBeenCalledWith({ kind: 'image', ids: [1], confirm: '清除记录' });
    expect(wrapper.emitted('videos-changed')).toEqual([[{ kind: 'image' }]]);
    wrapper.unmount();
  });

  it('取消执行时正在处理的那条按「已取消，未处理」列出，不算失败', async () => {
    api.PreviewLostPurge.mockResolvedValue({ kind: 'video', ids: [1, 2], count: 2, size: 20, not_purgeable: 0, skipped: {} });
    api.ApplyLostPurge.mockResolvedValue({ done: 1, skipped: 0, failed: 0, cancelled: true, items: [{ id: 2, outcome: 'cancelled', reason: 'cancelled' }] });
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="lost-dir-purge"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="lost-purge-apply"]').trigger('click');
    resolveConfirm(true);
    await flushPromises();
    const text = wrapper.get('[data-test="lost-purge-result"]').text();
    expect(text).toContain('已取消，其余未处理');
    expect(text).toContain('已取消，未处理：1 条');
    expect(text).not.toContain('失败');
    expect(wrapper.emitted('videos-changed')).toEqual([[{ kind: 'video', removedIDs: [] }]]);
    wrapper.unmount();
  });
});
