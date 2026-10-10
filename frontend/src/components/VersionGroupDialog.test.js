// 版本组管理弹窗（D-MW-VERSIONS / TC-16）：每次写操作都带当前 revision，冲突时重读；
// 移出到不足两个版本时组解散；视频不在组里时可加入已有组，已在组里时直接打开那个组。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), ...feedback }));

const api = vi.hoisted(() => Object.fromEntries([
  'GetVersionGroup', 'ListVersionGroups', 'AddVersionMembers', 'RemoveVersionMember', 'ReorderVersionMembers',
  'UpdateVersionGroup', 'DissolveVersionGroup'
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);

import VersionGroupDialog from './VersionGroupDialog.vue';

function member(id, overrides = {}) {
  return { video_id: id, label: '', position: id, display_title: '', name: `v${id}.mp4`, resolution: '1280x720', size: 100, duration: 60, is_watched: false, watch_position_seconds: 0, personal_rating: null, is_stale: false, ...overrides };
}

function group(overrides = {}) {
  return { group_id: 4, title: '', revision: 3, member_count: 3, watched_count: 0, rated_count: 0, min_rating: null, max_rating: null, deleted_member_count: 0, members: [member(1, { label: '原版' }), member(2), member(3)], ...overrides };
}

async function mountDialog(props) {
  const wrapper = mount(VersionGroupDialog, { props, global: { stubs: { teleport: true } } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  feedback.confirmAction.mockResolvedValue(true);
  api.GetVersionGroup.mockResolvedValue(group());
});

describe('VersionGroupDialog 管理一个组', () => {
  it('保存标题与标签时带当前 revision，并发 changed', async () => {
    api.UpdateVersionGroup.mockResolvedValue(group({ revision: 4, title: '正片', members: [member(1, { label: '原版' }), member(2, { label: '高清版' }), member(3)] }));
    const wrapper = await mountDialog({ groupId: 4 });
    expect(api.GetVersionGroup).toHaveBeenCalledWith(4);
    expect(wrapper.get('[data-test="version-dialog-save"]').attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="version-dialog-title"]').setValue(' 正片 ');
    await wrapper.get('[data-test="version-dialog-label-2"]').setValue('高清版');
    await wrapper.get('[data-test="version-dialog-save"]').trigger('click');
    await flushPromises();
    expect(api.UpdateVersionGroup).toHaveBeenCalledWith(4, 3, '正片', { 1: '原版', 2: '高清版', 3: '' });
    expect(wrapper.emitted('changed')).toHaveLength(1);
    expect(wrapper.vm.group.revision).toBe(4);
  });

  it('上移提交完整的新排列，未保存的标签输入保留', async () => {
    api.ReorderVersionMembers.mockResolvedValue(group({ revision: 4, members: [member(2), member(1, { label: '原版' }), member(3)] }));
    const wrapper = await mountDialog({ groupId: 4 });
    await wrapper.get('[data-test="version-dialog-label-3"]').setValue('剪辑版');
    await wrapper.get('[data-test="version-dialog-up-2"]').trigger('click');
    await flushPromises();
    expect(api.ReorderVersionMembers).toHaveBeenCalledWith(4, 3, [2, 1, 3]);
    expect(wrapper.vm.draftLabels[3]).toBe('剪辑版');
    expect(wrapper.vm.group.members[0].video_id).toBe(2);
  });

  it('revision 冲突时重读该组并提示', async () => {
    api.ReorderVersionMembers.mockRejectedValue('version_group_conflict: 版本组已被修改或解散，请刷新后重试');
    api.GetVersionGroup.mockResolvedValueOnce(group()).mockResolvedValueOnce(group({ revision: 9 }));
    const wrapper = await mountDialog({ groupId: 4 });
    await wrapper.get('[data-test="version-dialog-up-3"]').trigger('click');
    await flushPromises();
    expect(api.GetVersionGroup).toHaveBeenCalledTimes(2);
    expect(wrapper.vm.group.revision).toBe(9);
    expect(wrapper.get('[data-test="version-dialog-error"]').text()).toContain('已重新读取');
    expect(wrapper.emitted('changed')).toBeUndefined();
  });

  it('移出到不足两个版本时组解散，弹窗关闭', async () => {
    api.GetVersionGroup.mockResolvedValue(group({ member_count: 2, members: [member(1), member(2)] }));
    api.RemoveVersionMember.mockResolvedValue(null);
    const wrapper = await mountDialog({ groupId: 4 });
    await wrapper.get('[data-test="version-dialog-remove-2"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction.mock.calls[0][0].message).toContain('版本组会解散');
    expect(api.RemoveVersionMember).toHaveBeenCalledWith(4, 3, 2);
    expect(wrapper.emitted('changed')).toHaveLength(1);
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('取消确认不移出', async () => {
    feedback.confirmAction.mockResolvedValueOnce(false);
    const wrapper = await mountDialog({ groupId: 4 });
    await wrapper.get('[data-test="version-dialog-remove-2"]').trigger('click');
    await flushPromises();
    expect(api.RemoveVersionMember).not.toHaveBeenCalled();
  });

  it('解散需要确认，成功后关闭', async () => {
    api.DissolveVersionGroup.mockResolvedValue(null);
    const wrapper = await mountDialog({ groupId: 4 });
    await wrapper.get('[data-test="version-dialog-dissolve"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction.mock.calls[0][0].danger).toBe(true);
    expect(api.DissolveVersionGroup).toHaveBeenCalledWith(4, 3);
    expect(wrapper.emitted('close')).toHaveLength(1);
  });
});

describe('VersionGroupDialog 为单个视频选组', () => {
  it('列出已有的组并把视频加入所选组', async () => {
    api.ListVersionGroups.mockResolvedValue({ items: [group()], has_more: false, next_cursor_id: 0 });
    api.AddVersionMembers.mockResolvedValue(group({ revision: 4, member_count: 4, members: [member(1), member(2), member(3), member(8)] }));
    const wrapper = await mountDialog({ videoId: 8, videoTitle: '新文件' });
    expect(api.ListVersionGroups).toHaveBeenCalledWith(0, 50);
    expect(wrapper.text()).toContain('新文件');
    await wrapper.get('[data-test="version-dialog-join-4"]').trigger('click');
    await flushPromises();
    expect(api.AddVersionMembers).toHaveBeenCalledWith(4, 3, [8]);
    expect(wrapper.vm.group.member_count).toBe(4);
    expect(wrapper.emitted('changed')).toHaveLength(1);
  });

  it('视频其实已在某个组里：打开那个组', async () => {
    api.ListVersionGroups.mockResolvedValue({ items: [group({ group_id: 6 })], has_more: false });
    api.AddVersionMembers.mockRejectedValue('version_member_conflict: 1 个视频已在版本组中 {"video_ids":[8],"group_ids":[4]}');
    const wrapper = await mountDialog({ videoId: 8 });
    await wrapper.get('[data-test="version-dialog-join-6"]').trigger('click');
    await flushPromises();
    expect(api.GetVersionGroup).toHaveBeenCalledWith(4);
    expect(wrapper.vm.group.group_id).toBe(4);
    expect(feedback.notify).toHaveBeenCalledWith('这个视频已在一个版本组里，已打开该组');
  });

  it('没有组时给出说明', async () => {
    api.ListVersionGroups.mockResolvedValue({ items: [], has_more: false });
    const wrapper = await mountDialog({ videoId: 8 });
    expect(wrapper.find('[data-test="version-dialog-empty"]').exists()).toBe(true);
  });
});

describe('VersionGroupDialog 组已被清理', () => {
  it('写操作报 version_group_not_found 时按已解散关闭并通知片库刷新，不再反复重读', async () => {
    api.UpdateVersionGroup.mockRejectedValue('version_group_not_found: 版本组不存在或已解散');
    const wrapper = await mountDialog({ groupId: 4 });
    await wrapper.get('[data-test="version-dialog-title"]').setValue('新名');
    await wrapper.get('[data-test="version-dialog-save"]').trigger('click');
    await flushPromises();
    expect(api.GetVersionGroup).toHaveBeenCalledTimes(1);
    expect(feedback.notify).toHaveBeenCalledWith('版本组已解散');
    expect(wrapper.emitted('changed')).toHaveLength(1);
    expect(wrapper.emitted('close')).toHaveLength(1);
  });
});
