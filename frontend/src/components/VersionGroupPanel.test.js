// 管理菜单「版本组」面板（D-MW-VERSIONS / TC-16）：建议只在点「建立版本组」时建组，忽略后从列表移除；
// 已有组逐个打开管理弹窗，Esc 先关嵌套弹窗。
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
  'ListVersionGroupSuggestions', 'ListVersionGroups', 'CreateVersionGroup', 'DismissVersionGroupSuggestion', 'GetVersionGroup'
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);

import VersionGroupPanel from './VersionGroupPanel.vue';

function member(id, overrides = {}) {
  return { video_id: id, label: '', position: 1, display_title: '', name: `v${id}.mp4`, resolution: '1920x1080', size: 100, duration: 60, is_watched: false, watch_position_seconds: 0, personal_rating: null, is_stale: false, ...overrides };
}

const suggestion = { video_ids: [3, 7], members: [member(3), member(7)], latest_relation_at: '2026-10-01T00:00:00Z' };
const existing = { group_id: 9, title: '', revision: 2, member_count: 2, deleted_member_count: 1, members: [member(1, { label: '原版' }), member(2)] };

async function mountPanel() {
  const wrapper = mount(VersionGroupPanel, { global: { stubs: { VersionGroupDialog: { props: ['groupId'], template: '<div data-test="nested-dialog" />' } } } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  api.ListVersionGroupSuggestions.mockResolvedValue([suggestion]);
  api.ListVersionGroups.mockResolvedValue({ items: [existing], has_more: false, next_cursor_id: 0 });
});

describe('VersionGroupPanel', () => {
  it('接受建议按建议顺序建组并发 changed，随后重读建议与组', async () => {
    api.CreateVersionGroup.mockResolvedValue({ group_id: 10, member_count: 2 });
    const wrapper = await mountPanel();
    expect(api.ListVersionGroupSuggestions).toHaveBeenCalledWith(50);
    expect(wrapper.findAll('[data-test="version-panel-suggestion"]')).toHaveLength(1);
    expect(api.CreateVersionGroup).not.toHaveBeenCalled();
    await wrapper.get('[data-test="version-panel-accept"]').trigger('click');
    await flushPromises();
    expect(api.CreateVersionGroup).toHaveBeenCalledWith([3, 7], '');
    expect(wrapper.emitted('changed')).toHaveLength(1);
    expect(api.ListVersionGroupSuggestions).toHaveBeenCalledTimes(2);
  });

  it('忽略建议后从列表移除，不建组', async () => {
    api.DismissVersionGroupSuggestion.mockResolvedValue(null);
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="version-panel-dismiss"]').trigger('click');
    await flushPromises();
    expect(api.DismissVersionGroupSuggestion).toHaveBeenCalledWith([3, 7]);
    expect(wrapper.find('[data-test="version-panel-no-suggestions"]').exists()).toBe(true);
    expect(api.CreateVersionGroup).not.toHaveBeenCalled();
  });

  it('建组冲突时显示错误', async () => {
    api.CreateVersionGroup.mockRejectedValue('version_member_conflict: 1 个视频已在版本组中 {"video_ids":[3],"group_ids":[9]}');
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="version-panel-accept"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="version-panel-error"]').text()).toContain('已在其他版本组中');
    expect(wrapper.emitted('changed')).toBeUndefined();
  });

  it('已有的组：显示软删版本数，管理打开嵌套弹窗，Esc 先关它', async () => {
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="version-panel-tab-groups"]').trigger('click');
    const row = wrapper.get('[data-test="version-panel-group-9"]');
    expect(row.text()).toContain('2 个版本');
    expect(row.text()).toContain('1 个在回收站');
    expect(row.text()).toContain('原版 / v2.mp4');
    await wrapper.get('[data-test="version-panel-manage-9"]').trigger('click');
    expect(wrapper.find('[data-test="nested-dialog"]').exists()).toBe(true);
    wrapper.vm.onEscape();
    await flushPromises();
    expect(wrapper.find('[data-test="nested-dialog"]').exists()).toBe(false);
    expect(wrapper.emitted('close')).toBeUndefined();
    wrapper.vm.onEscape();
    expect(wrapper.emitted('close')).toHaveLength(1);
  });
});
