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

import EditResults from './EditResults.vue';
import { feedbackState, resetFeedback, resolveConfirm } from '../../utils/feedback.js';

const completed = { id: 4, seq: 1, status: 'completed', phase: 'done', progress: 1, output_name: 'e01.mkv', output_video_id: 90,
  source_video_ids: [21], error_code: '', error_message: '' };

function trimProject(items = [completed]) {
  return { id: 2, kind: 'trim_intro', status: 'completed', recipe: { v: 1, trim_intro: { items: [] } }, items };
}

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  api.GetVideosByIDs.mockResolvedValue([{ id: 90 }]);
});

describe('导出结果（TC-23）', () => {
  it('「清理原片…」只在勾选确认成品无误后出现，点了只把来源交给宿主', async () => {
    const wrapper = mount(EditResults, { props: { project: trimProject() } });
    await flushPromises();
    expect(wrapper.find('[data-test="edit-result-cleanup-4"]').exists()).toBe(false);
    await wrapper.get('[data-test="edit-result-confirm-4"]').setValue(true);
    await wrapper.get('[data-test="edit-result-cleanup-4"]').trigger('click');
    expect(wrapper.emitted('cleanup-sources')).toEqual([[[21]]]);
    await wrapper.get('[data-test="edit-result-confirm-4"]').setValue(false);
    expect(wrapper.find('[data-test="edit-result-cleanup-4"]').exists()).toBe(false);
  });

  it('失败项显示失败原因；完成项的旁挂提示单独显示', () => {
    const failed = { ...completed, id: 5, seq: 2, status: 'failed', error_code: 'disk_full', error_message: '需要约 2 GB', output_video_id: null };
    const wrapper = mount(EditResults, { props: { project: trimProject([{ ...completed, error_message: '旁挂字幕写入失败' }, failed]) } });
    expect(wrapper.get('[data-test="edit-result-error-5"]').text()).toBe('磁盘空间不足：需要约 2 GB');
    expect(wrapper.get('[data-test="edit-result-4"]').text()).toContain('提示：旁挂字幕写入失败');
    expect(wrapper.find('[data-test="edit-result-preview-5"]').exists()).toBe(false);
  });

  it('与原片建立版本组：先确认，再 CreateVersionGroup([原片, 成品])', async () => {
    api.CreateVersionGroup.mockResolvedValue({ group_id: 3, member_count: 2 });
    const wrapper = mount(EditResults, { props: { project: trimProject() } });
    await flushPromises();
    await wrapper.get('[data-test="edit-result-version-4"]').trigger('click');
    expect(feedbackState.confirm.title).toBe('与原片建立版本组');
    resolveConfirm(true);
    await flushPromises();
    expect(api.CreateVersionGroup).toHaveBeenCalledWith([21, 90], '');
  });

  it('原片已在一个版本组里时提议把成品加进该组', async () => {
    api.CreateVersionGroup.mockRejectedValue(new Error('version_member_conflict: 1 个视频已在版本组中 {"video_ids":[1],"group_ids":[8]}'));
    api.GetVersionGroup.mockResolvedValue({ group_id: 8, revision: 6, member_count: 2 });
    const project = { id: 3, kind: 'hd_replace', status: 'completed', recipe: { v: 1, hd_replace: { long_video_id: 1, hd_video_id: 2, segments: [] } },
      items: [{ ...completed, source_video_ids: [1, 2] }] };
    const wrapper = mount(EditResults, { props: { project } });
    await flushPromises();
    await wrapper.get('[data-test="edit-result-version-4"]').trigger('click');
    resolveConfirm(true);
    await flushPromises();
    expect(api.CreateVersionGroup).toHaveBeenCalledWith([1, 90], '');
    expect(feedbackState.confirm.title).toBe('加入已有版本组');
    resolveConfirm(true);
    await flushPromises();
    expect(api.GetVersionGroup).toHaveBeenCalledWith(8);
    expect(api.AddVersionMembers).toHaveBeenCalledWith(8, 6, [90]);
  });

  it('成品已不在片库时不出现确认与清理入口，提示「成品已不在片库」', async () => {
    api.GetVideosByIDs.mockResolvedValue([]);
    const wrapper = mount(EditResults, { props: { project: trimProject() } });
    await flushPromises();
    expect(api.GetVideosByIDs).toHaveBeenCalledWith([90]);
    expect(wrapper.get('[data-test="edit-result-missing-4"]').text()).toBe('成品已不在片库');
    expect(wrapper.find('[data-test="edit-result-confirm-4"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="edit-result-cleanup-4"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="edit-result-version-4"]').exists()).toBe(false);
  });

  it('合并的成品不提供版本组入口', async () => {
    const wrapper = mount(EditResults, { props: { project: { ...trimProject(), kind: 'merge' } } });
    await flushPromises();
    expect(wrapper.find('[data-test="edit-result-confirm-4"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="edit-result-version-4"]').exists()).toBe(false);
  });
});
