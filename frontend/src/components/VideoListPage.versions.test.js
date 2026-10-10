// 片库页的多版本聚合接线（D-MW-VERSIONS / TC-16）：开关默认开且本机记住，只有走 SearchLibraryVideoPage 的
// 列表和结果条计数带 collapse_versions；按页合并组汇总；展开状态进 itemVersion；播放 / 预览所选具体文件；
// 组写操作后原地刷新；批量合并与「加入该组」；行菜单与管理菜单入口。
import { flushPromises, shallowMount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), ...feedback }));

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./VirtualVideoList.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./PreviewDrawer.vue', () => ({ default: { template: '<div />' } }));
vi.mock('./VideoListRow.vue', async (importOriginal) => ({ ...(await importOriginal()), default: { template: '<div />' } }));

import VideoListPage from './VideoListPage.vue';

function member(id, overrides = {}) {
  return { video_id: id, label: '', position: 1, display_title: '', name: `v${id}.mp4`, resolution: '', size: 1, duration: 60, is_watched: false, watch_position_seconds: 0, personal_rating: null, is_stale: false, ...overrides };
}
const summary = { group_id: 5, title: '', revision: 2, member_count: 3, watched_count: 1, rated_count: 0, min_rating: null, max_rating: null, members: [member(1), member(2), member(3)] };
const rows = [{ id: 1, name: 'v1.mp4', tags: [] }, { id: 4, name: 'v4.mp4', tags: [] }];

let store;
async function mountPage() {
  const wrapper = shallowMount(VideoListPage, { props: { tags: [], settings: {}, directories: [] } });
  await flushPromises();
  return wrapper;
}
function lastPageFilter() {
  return api.SearchLibraryVideoPage.mock.calls.at(-1)[0].filter;
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  store = {};
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: { getItem: vi.fn(key => store[key] ?? null), setItem: vi.fn((key, value) => { store[key] = value; }), removeItem: vi.fn(), clear: vi.fn() }
  });
  feedback.confirmAction.mockResolvedValue(true);
  api.SearchLibraryVideoPage.mockResolvedValue({ videos: rows, version_groups: { 1: summary }, automatic_override_kinds: {} });
  api.CountLibraryVideos.mockResolvedValue(2);
  api.GetSemanticIndexStatus.mockResolvedValue({ available: true, unavailable: '' });
  api.ListSavedLibraryViews.mockResolvedValue([]);
  api.GetSubtitleQueueState.mockResolvedValue({ active_task: null, queued_tasks: [], total: 0 });
  api.GetAITaggingStatusSummary.mockResolvedValue({ same_source_unread: 0 });
  api.GetTechnicalBackfillStatus.mockResolvedValue({ running: false, failures: [] });
  api.GetPerceptualHashBackfillStatus.mockResolvedValue({ running: false, failures: [] });
  api.GetLocalMetadataBackfillStatus.mockResolvedValue({ running: false, failures: [] });
  api.GetLocalMetadataExportStatus.mockResolvedValue({ running: false, failures: [] });
  api.GetSettings.mockResolvedValue({ scan_exclude_paths: '' });
  api.GetEnhancementCapability.mockResolvedValue({ available: true });
  api.ListStaleReasonCounts.mockResolvedValue({});
  api.GetAutomaticOverrideKinds.mockResolvedValue({});
  api.GetSubtitleIndexSyncStatus.mockResolvedValue({ running: false, checked: 0 });
  api.ListRecentlyPlayedWithFilter.mockResolvedValue([]);
});

describe('合并版本开关与查询', () => {
  it('默认开：分页与两个计数都带 collapse_versions，按页合并组汇总', async () => {
    const wrapper = await mountPage();
    expect(lastPageFilter().collapse_versions).toBe(true);
    const countFilters = api.CountLibraryVideos.mock.calls.map(call => call[0].collapse_versions);
    expect(countFilters).toEqual([true, true]);
    expect(wrapper.vm.versionGroupFor(rows[0])).toEqual(summary);
    expect(wrapper.vm.versionGroupFor(rows[1])).toBeNull();
    // 共享筛选 DTO 本身不带开关：随机、今晚看什么、保存视图等文件级消费方拿不到它。
    expect('collapse_versions' in wrapper.vm.currentLibraryFilter()).toBe(false);
    wrapper.unmount();
  });

  it('关闭后本机记住，重新按文件查询并重取总数，不再显示组', async () => {
    const wrapper = await mountPage();
    api.CountLibraryVideos.mockClear();
    wrapper.vm.setCollapseVersions(false);
    await flushPromises();
    expect(store['library-collapse-versions']).toBe('0');
    expect(lastPageFilter().collapse_versions).toBe(false);
    expect(api.CountLibraryVideos.mock.calls.map(call => call[0].collapse_versions)).toEqual([false, false]);
    expect(wrapper.vm.versionGroupFor(rows[0])).toBeNull();
    wrapper.unmount();
    // 下次打开沿用本机记忆。
    const reopened = await mountPage();
    expect(lastPageFilter().collapse_versions).toBe(false);
    reopened.unmount();
  });

  it('最近播放（均衡排序）是文件级入口：不聚合，计数也按文件', async () => {
    const wrapper = await mountPage();
    wrapper.vm.smartView = 'recently_played';
    expect(wrapper.vm.versionCollapseActive()).toBe(false);
    expect(wrapper.vm.libraryPageFilter().collapse_versions).toBe(false);
    expect(wrapper.vm.versionGroupFor(rows[0])).toBeNull();
    wrapper.vm.smartView = '';
    wrapper.vm.randomPick = { active: true, ids: [1], reason: '', loading: false };
    expect(wrapper.vm.versionCollapseActive()).toBe(false);
    wrapper.vm.randomPick = { active: false, ids: [], reason: '', loading: false };
    expect(wrapper.vm.versionCollapseActive()).toBe(true);
    wrapper.vm.searchMode = 'semantic';
    expect(wrapper.vm.versionCollapseActive()).toBe(false);
    wrapper.unmount();
  });

  it('展开状态进 itemVersion，估高随之变大；组解散后本页汇总被移除', async () => {
    const wrapper = await mountPage();
    const before = wrapper.vm.videoVisualVersion(rows[0]);
    const height = wrapper.vm.estimateVideoHeight(rows[0], 1, false, {});
    wrapper.vm.toggleVersionGroup(rows[0]);
    expect(wrapper.vm.isVersionGroupExpanded(rows[0])).toBe(true);
    expect(wrapper.vm.videoVisualVersion(rows[0])).not.toBe(before);
    expect(wrapper.vm.estimateVideoHeight(rows[0], 1, false, {})).toBeGreaterThan(height);
    wrapper.vm.mergeVersionGroups({}, [rows[0]]);
    expect(wrapper.vm.versionGroupFor(rows[0])).toBeNull();
    wrapper.unmount();
  });
});

describe('播放、预览与组写操作', () => {
  it('播放所选版本走 PlayVideo(该文件 ID)；预览非代表版本先按 ID 取完整视频', async () => {
    api.PlayVideo.mockResolvedValue({ video: { id: 3 }, dispatch_succeeded: true });
    api.GetVideosByIDs.mockResolvedValue([{ id: 3, name: 'v3.mp4', tags: [] }]);
    api.GetPreviewSession.mockResolvedValue({ video_id: 3, mode: 'direct' });
    const wrapper = await mountPage();
    await wrapper.vm.playVersionMember(3);
    expect(api.PlayVideo).toHaveBeenCalledWith(3);
    await wrapper.vm.previewVersionMember(rows[0], 3);
    await flushPromises();
    expect(api.GetVideosByIDs).toHaveBeenCalledWith([3]);
    expect(wrapper.vm.selectedPreviewVideoId).toBe(3);
    await wrapper.vm.previewVersionMember(rows[0], 1);
    expect(api.GetVideosByIDs).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.selectedPreviewVideoId).toBe(1);
    wrapper.unmount();
  });

  it('设为主版本提交新排列并原地刷新；移出只解除关系', async () => {
    api.ReorderVersionMembers.mockResolvedValue({ ...summary, revision: 3 });
    api.RemoveVersionMember.mockResolvedValue({ ...summary, revision: 3 });
    const wrapper = await mountPage();
    const before = api.SearchLibraryVideoPage.mock.calls.length;
    await wrapper.vm.setPrimaryVersion(rows[0], 3);
    await flushPromises();
    expect(api.ReorderVersionMembers).toHaveBeenCalledWith(5, 2, [3, 1, 2]);
    expect(api.SearchLibraryVideoPage.mock.calls.length).toBeGreaterThan(before);
    expect(wrapper.vm.videos.map(video => video.id)).toEqual([1, 4]);
    await wrapper.vm.removeVersionFromGroup(rows[0], 2);
    expect(api.RemoveVersionMember).toHaveBeenCalledWith(5, 2, 2);
    expect(feedback.notify).toHaveBeenCalledWith('已移出版本组，文件未删除');
    wrapper.unmount();
  });

  it('revision 冲突提示并刷新', async () => {
    api.ReorderVersionMembers.mockRejectedValue('version_group_conflict: 版本组已被修改或解散，请刷新后重试');
    const wrapper = await mountPage();
    const before = api.SearchLibraryVideoPage.mock.calls.length;
    expect(await wrapper.vm.setPrimaryVersion(rows[0], 2)).toBe(false);
    await flushPromises();
    expect(feedback.notifyError).toHaveBeenCalledWith('版本组已被其他操作修改，列表已刷新，请再试一次');
    expect(api.SearchLibraryVideoPage.mock.calls.length).toBeGreaterThan(before);
    wrapper.unmount();
  });
});

describe('批量合并与入口', () => {
  it('按选择顺序建组，成功后清空选择', async () => {
    api.CreateVersionGroup.mockResolvedValue({ group_id: 8, member_count: 2 });
    const wrapper = await mountPage();
    wrapper.vm.selectedVideoIds = [4, 1];
    expect(await wrapper.vm.mergeSelectedAsVersionGroup()).toBe(true);
    expect(api.CreateVersionGroup).toHaveBeenCalledWith([4, 1], '');
    expect(wrapper.vm.selectedVideoIds).toEqual([]);
    wrapper.vm.selectedVideoIds = [4];
    expect(await wrapper.vm.mergeSelectedAsVersionGroup()).toBe(false);
    expect(api.CreateVersionGroup).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('冲突项都属于同一组时提供「加入该组」，追加其余视频', async () => {
    api.CreateVersionGroup.mockRejectedValue('version_member_conflict: 1 个视频已在版本组中 {"video_ids":[1],"group_ids":[5]}');
    api.GetVersionGroup.mockResolvedValue({ ...summary });
    api.AddVersionMembers.mockResolvedValue({ ...summary, member_count: 4 });
    const wrapper = await mountPage();
    wrapper.vm.selectedVideoIds = [1, 4];
    expect(await wrapper.vm.mergeSelectedAsVersionGroup()).toBe(true);
    expect(feedback.confirmAction.mock.calls[0][0].confirmText).toBe('加入该组');
    expect(api.AddVersionMembers).toHaveBeenCalledWith(5, 2, [4]);
    wrapper.unmount();
  });

  it('冲突跨多个组时只报错，不追加', async () => {
    api.CreateVersionGroup.mockRejectedValue('version_member_conflict: 2 个视频已在版本组中 {"video_ids":[1,4],"group_ids":[5,6]}');
    const wrapper = await mountPage();
    wrapper.vm.selectedVideoIds = [1, 4, 7];
    expect(await wrapper.vm.mergeSelectedAsVersionGroup()).toBe(false);
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.AddVersionMembers).not.toHaveBeenCalled();
    expect(feedback.notifyError).toHaveBeenCalled();
    wrapper.unmount();
  });

  it('行菜单「管理版本组…」带上组 ID；管理菜单「版本组」打开面板', async () => {
    const wrapper = await mountPage();
    wrapper.vm.openRowMenu(rows[0], null);
    expect(wrapper.vm.rowMenuItems.some(item => item.id === 'version-group')).toBe(true);
    wrapper.vm.onRowMenuSelect({ id: 'version-group' });
    expect(wrapper.vm.versionGroupDialog).toMatchObject({ show: true, groupId: 5, videoId: 1 });
    wrapper.vm.closeVersionGroupDialog();
    wrapper.vm.openRowMenu(rows[1], null);
    wrapper.vm.onRowMenuSelect({ id: 'version-group' });
    expect(wrapper.vm.versionGroupDialog).toMatchObject({ show: true, groupId: 0, videoId: 4 });
    wrapper.vm.onManageSelect({ id: 'version-groups' });
    expect(wrapper.vm.versionPanelOpen).toBe(true);
    wrapper.unmount();
  });
});

describe('复审修复', () => {
  it('#2 组写操作后片库总数（按卡片）也重取', async () => {
    api.ReorderVersionMembers.mockResolvedValue({ ...summary, revision: 3 });
    const wrapper = await mountPage();
    api.CountLibraryVideos.mockClear();
    await wrapper.vm.setPrimaryVersion(rows[0], 3);
    await flushPromises();
    // 结果条命中数 + 片库总数各一次；没有失效总数时只会重取命中数。
    expect(api.CountLibraryVideos).toHaveBeenCalledTimes(2);
    expect(api.CountLibraryVideos.mock.calls.every(call => call[0].collapse_versions === true)).toBe(true);
    wrapper.unmount();
  });

  it('#5 移出前确认；只剩 2 个版本时明确说会解散，取消则不移出', async () => {
    api.RemoveVersionMember.mockResolvedValue(null);
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: rows, version_groups: { 1: { ...summary, member_count: 2, members: [member(1), member(2)] } } });
    const wrapper = await mountPage();
    feedback.confirmAction.mockResolvedValueOnce(false);
    expect(await wrapper.vm.removeVersionFromGroup(rows[0], 2)).toBe(false);
    expect(api.RemoveVersionMember).not.toHaveBeenCalled();
    expect(feedback.confirmAction.mock.calls[0][0].message).toContain('版本组将解散');
    expect(await wrapper.vm.removeVersionFromGroup(rows[0], 2)).toBe(true);
    expect(api.RemoveVersionMember).toHaveBeenCalledWith(5, 2, 2);
    wrapper.unmount();
  });

  it('#5 三个版本时的确认不提解散', async () => {
    api.RemoveVersionMember.mockResolvedValue({ ...summary, member_count: 2 });
    const wrapper = await mountPage();
    await wrapper.vm.removeVersionFromGroup(rows[0], 3);
    expect(feedback.confirmAction.mock.calls[0][0].message).not.toContain('解散');
    expect(feedback.confirmAction.mock.calls[0][0].message).toContain('v3.mp4');
    wrapper.unmount();
  });

  it('组被清理（not_found）时提示已解散并刷新', async () => {
    api.ReorderVersionMembers.mockRejectedValue('version_group_not_found: 版本组不存在或已解散');
    const wrapper = await mountPage();
    const before = api.SearchLibraryVideoPage.mock.calls.length;
    expect(await wrapper.vm.setPrimaryVersion(rows[0], 2)).toBe(false);
    await flushPromises();
    expect(feedback.notifyError).toHaveBeenCalledWith('版本组已解散，列表已刷新');
    expect(api.SearchLibraryVideoPage.mock.calls.length).toBeGreaterThan(before);
    wrapper.unmount();
  });
});

describe('#3 就地修改后刷新组汇总', () => {
  const refreshed = (overrides = {}) => ({ ...summary, revision: 2, watched_count: 2, rated_count: 1, min_rating: 8, max_rating: 8, deleted_member_count: 0, ...overrides });

  it('代表版本标已看后重读该组汇总', async () => {
    api.SetVideoWatched.mockResolvedValue({ ...rows[0], is_watched: true });
    api.GetVersionGroup.mockResolvedValue(refreshed());
    const wrapper = await mountPage();
    await wrapper.vm.toggleVideoWatched(rows[0]);
    await flushPromises();
    expect(api.GetVersionGroup).toHaveBeenCalledWith(5);
    expect(wrapper.vm.versionGroupFor(rows[0]).watched_count).toBe(2);
    wrapper.unmount();
  });

  it('播放非代表版本、改评分都会刷新；不在组里的视频不发请求', async () => {
    api.GetVersionGroup.mockResolvedValue(refreshed());
    const wrapper = await mountPage();
    await wrapper.vm.applyPlaybackAttemptResult({ dispatch_succeeded: true, video: { id: 3 }, reconcile_result: { video_id: 3, updated_video: { id: 3 } } });
    await flushPromises();
    expect(api.GetVersionGroup).toHaveBeenCalledTimes(1);
    await wrapper.vm.handleVideoDetailsUpdated({ video: { id: 2, personal_rating: 8 } });
    await flushPromises();
    expect(api.GetVersionGroup).toHaveBeenCalledTimes(2);
    expect(wrapper.vm.versionGroupFor(rows[0]).max_rating).toBe(8);
    await wrapper.vm.handleVideoDetailsUpdated({ video: { id: 4 } });
    await flushPromises();
    expect(api.GetVersionGroup).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it('迟到的旧响应不覆盖新数据；组已解散时去掉徽标', async () => {
    let resolveFirst;
    api.GetVersionGroup
      .mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve; }))
      .mockResolvedValueOnce(refreshed({ watched_count: 3 }));
    const wrapper = await mountPage();
    const first = wrapper.vm.refreshVersionGroupForVideo(2);
    await wrapper.vm.refreshVersionGroupForVideo(3);
    resolveFirst(refreshed({ watched_count: 0 }));
    expect(await first).toBe(false);
    expect(wrapper.vm.versionGroupFor(rows[0]).watched_count).toBe(3);

    // 整页数据在单组请求途中到达：单组响应作废。
    let resolveLate;
    api.GetVersionGroup.mockImplementationOnce(() => new Promise(resolve => { resolveLate = resolve; }));
    const late = wrapper.vm.refreshVersionGroupForVideo(1);
    wrapper.vm.mergeVersionGroups({ 1: { ...summary, watched_count: 1 } }, [rows[0]]);
    resolveLate(refreshed({ watched_count: 0 }));
    expect(await late).toBe(false);
    expect(wrapper.vm.versionGroupFor(rows[0]).watched_count).toBe(1);

    api.GetVersionGroup.mockRejectedValueOnce('version_group_not_found: 版本组不存在或已解散');
    expect(await wrapper.vm.refreshVersionGroupForVideo(1)).toBe(true);
    expect(wrapper.vm.versionGroupFor(rows[0])).toBeNull();
    wrapper.unmount();
  });
});
