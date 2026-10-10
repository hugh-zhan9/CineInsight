// 片库里的视频工作台入口（视频编辑合同「入口」/ TC-23）：批量栏「合并为新视频」「批量去片头」、
// 行菜单「高清替换…」建项目后交给宿主打开工作台；工作台「清理原片…」打开既有的删除确认框。
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

const rows = [{ id: 1, name: 'v1.mp4', tags: [] }, { id: 4, name: 'v4.mp4', tags: [] }, { id: 7, name: 'v7.mp4', tags: [] }];

async function mountPage(settings = {}) {
  const wrapper = shallowMount(VideoListPage, { props: { tags: [], settings, directories: [] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  api.SearchLibraryVideoPage.mockResolvedValue({ videos: rows, version_groups: {}, automatic_override_kinds: {} });
  api.CountLibraryVideos.mockResolvedValue(3);
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
  api.CreateEditProject.mockResolvedValue({ id: 42, kind: 'merge' });
});

describe('视频工作台入口', () => {
  it('「合并为新视频」按选中顺序建合并项目，交给宿主打开并清掉选择', async () => {
    const wrapper = await mountPage();
    await wrapper.setData({ selectedVideoIds: [7, 1, 4] });
    wrapper.findComponent({ name: 'LibraryToolbar' }).vm.$emit('batch-edit-merge');
    await flushPromises();
    expect(api.CreateEditProject).toHaveBeenCalledWith({ kind: 'merge', title: '', video_ids: [7, 1, 4] });
    expect(wrapper.emitted('open-video-edit')).toEqual([[42]]);
    expect(wrapper.vm.selectedVideoIds).toEqual([]);
  });

  it('合并少于 2 个不建项目；批量去片头 1 个即可', async () => {
    const wrapper = await mountPage();
    await wrapper.setData({ selectedVideoIds: [4] });
    wrapper.findComponent({ name: 'LibraryToolbar' }).vm.$emit('batch-edit-merge');
    await flushPromises();
    expect(api.CreateEditProject).not.toHaveBeenCalled();
    expect(feedback.notifyError).toHaveBeenCalledWith('合并为新视频需要选择 2–50 个视频');
    wrapper.findComponent({ name: 'LibraryToolbar' }).vm.$emit('batch-edit-trim');
    await flushPromises();
    expect(api.CreateEditProject).toHaveBeenCalledWith({ kind: 'trim_intro', title: '', video_ids: [4] });
  });

  it('建项目失败时提示后端说明，不跳转', async () => {
    api.CreateEditProject.mockRejectedValue(new Error('source_missing: 视频 4 不存在或已删除'));
    const wrapper = await mountPage();
    await wrapper.setData({ selectedVideoIds: [4] });
    wrapper.findComponent({ name: 'LibraryToolbar' }).vm.$emit('batch-edit-trim');
    await flushPromises();
    expect(feedback.notifyError).toHaveBeenCalledWith('创建编辑项目失败：视频 4 不存在或已删除');
    expect(wrapper.emitted('open-video-edit')).toBeUndefined();
    expect(wrapper.vm.selectedVideoIds).toEqual([4]);
  });

  it('行菜单「高清替换…」：该行是长版，在选择框里挑高清版后建 hd_replace 项目', async () => {
    const wrapper = await mountPage();
    wrapper.vm.rowMenu = { video: rows[0], anchor: null, position: { x: 0, y: 0 } };
    expect(wrapper.vm.rowMenuItems.map(item => item.id)).toContain('hd-replace');
    wrapper.vm.onRowMenuSelect({ id: 'hd-replace' });
    await flushPromises();
    const picker = wrapper.findComponent({ name: 'HDReplacePickerDialog' });
    expect(picker.props('longVideo')).toEqual(rows[0]);
    picker.vm.$emit('pick', rows[2]);
    await flushPromises();
    expect(api.CreateEditProject).toHaveBeenCalledWith({ kind: 'hd_replace', title: '', video_ids: [1, 7] });
    expect(wrapper.emitted('open-video-edit')).toEqual([[42]]);
    expect(wrapper.findComponent({ name: 'HDReplacePickerDialog' }).exists()).toBe(false);
  });
});

describe('工作台「清理原片…」走既有删除确认（TC-23）', () => {
  it('即使关了删除前确认也打开确认框；确认后按既有路径删除并带上原片名字', async () => {
    const wrapper = await mountPage({ confirm_before_delete: false, delete_original_file: true });
    const deleteVideos = vi.fn();
    wrapper.vm.deleteVideos = deleteVideos;
    wrapper.vm.requestDeleteVideos([{ id: 11, name: '原片A.mkv' }, { id: 12, name: '原片B.mkv' }]);
    await flushPromises();
    const dialog = wrapper.findComponent({ name: 'DeleteConfirmDialog' });
    expect(dialog.props('visible')).toBe(true);
    expect(dialog.props('videoCount')).toBe(2);
    expect(deleteVideos).not.toHaveBeenCalled();
    dialog.vm.$emit('confirm-delete', { video: null, deleteFile: true, dontAskAgain: false });
    await flushPromises();
    expect(deleteVideos).toHaveBeenCalledWith([11, 12], true, { 11: '原片A.mkv', 12: '原片B.mkv' });
  });

  it('只有一个原片时确认框显示它的名字', async () => {
    const wrapper = await mountPage({ confirm_before_delete: true });
    wrapper.vm.requestDeleteVideos([{ id: 11, name: '原片A.mkv', size: 0 }]);
    await flushPromises();
    const dialog = wrapper.findComponent({ name: 'DeleteConfirmDialog' });
    expect(dialog.props('video')).toEqual({ id: 11, name: '原片A.mkv', size: 0 });
    expect(dialog.props('videoCount')).toBe(0);
  });
});
