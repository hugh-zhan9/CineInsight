// 卡片上的版本徽标与展开列表（D-MW-VERSIONS / TC-16）：只呈现与发事件，写操作由片库页执行。
import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import VersionGroupBadge from './VersionGroupBadge.vue';
import VersionGroupMembers from './VersionGroupMembers.vue';

function summary(overrides = {}) {
  return {
    group_id: 5, title: '', revision: 2, member_count: 3, watched_count: 1, rated_count: 2, min_rating: 7, max_rating: 8.5,
    members: [
      { video_id: 11, label: '原版', position: 1, display_title: '', name: 'movie.mkv', resolution: '1280x720', size: 1024, duration: 5400, is_watched: true, watch_position_seconds: 0, personal_rating: 7, is_stale: false },
      { video_id: 12, label: '', position: 2, display_title: '电影 高清', name: 'movie.4k.mkv', resolution: '3840x2160', size: 4096, duration: 5400, is_watched: false, watch_position_seconds: 600, personal_rating: 8.5, is_stale: false },
      { video_id: 13, label: '编辑版', position: 3, display_title: '', name: 'movie.cut.mkv', resolution: '', size: 0, duration: 0, is_watched: false, watch_position_seconds: 0, personal_rating: null, is_stale: true }
    ],
    ...overrides
  };
}

describe('VersionGroupBadge', () => {
  it('显示版本数与汇总，点击只发 toggle', async () => {
    const wrapper = mount(VersionGroupBadge, { props: { summary: summary() } });
    expect(wrapper.text()).toContain('3 个版本');
    expect(wrapper.get('[data-test="version-badge-summary"]').text()).toBe('已看 1/3 · 评分 7–8.5');
    expect(wrapper.attributes('aria-expanded')).toBe('false');
    await wrapper.trigger('click');
    expect(wrapper.emitted('toggle')).toHaveLength(1);
    await wrapper.setProps({ expanded: true });
    expect(wrapper.attributes('aria-expanded')).toBe('true');
  });
});

describe('VersionGroupMembers', () => {
  it('按顺序列出每个版本，标出主版本与失效版本', () => {
    const wrapper = mount(VersionGroupMembers, { props: { summary: summary(), currentVideoId: 12 } });
    const rows = wrapper.findAll('.version-member');
    expect(rows).toHaveLength(3);
    expect(rows[0].text()).toContain('原版');
    expect(rows[0].text()).toContain('主版本');
    expect(rows[0].text()).toContain('已看');
    expect(rows[1].text()).toContain('版本 2');
    expect(rows[1].text()).toContain('看到 10:00');
    expect(rows[1].classes()).toContain('version-member--current');
    expect(rows[2].text()).toContain('路径失效');
    expect(wrapper.text()).toContain('movie.mkv');
    // 主版本那一行没有「设为主版本」。
    expect(rows[0].find('[data-test="version-member-primary"]').exists()).toBe(false);
    expect(rows[1].find('[data-test="version-member-primary"]').exists()).toBe(true);
  });

  it('播放、预览、设为主版本、移出、管理都带所选版本的视频 ID', async () => {
    const wrapper = mount(VersionGroupMembers, { props: { summary: summary(), currentVideoId: 11 } });
    const row = wrapper.get('[data-test="version-member-12"]');
    await row.get('[data-test="version-member-play"]').trigger('click');
    await row.get('[data-test="version-member-preview"]').trigger('click');
    await row.get('[data-test="version-member-primary"]').trigger('click');
    await row.get('[data-test="version-member-remove"]').trigger('click');
    await wrapper.get('[data-test="version-members-manage"]').trigger('click');
    expect(wrapper.emitted('play')).toEqual([[12]]);
    expect(wrapper.emitted('preview')).toEqual([[12]]);
    expect(wrapper.emitted('set-primary')).toEqual([[12]]);
    expect(wrapper.emitted('remove')).toEqual([[12]]);
    expect(wrapper.emitted('manage')).toHaveLength(1);
  });

  it('写操作进行中禁用按钮', () => {
    const wrapper = mount(VersionGroupMembers, { props: { summary: summary(), busy: true } });
    expect(wrapper.get('[data-test="version-member-11"] [data-test="version-member-play"]').attributes('disabled')).toBeDefined();
  });
});
