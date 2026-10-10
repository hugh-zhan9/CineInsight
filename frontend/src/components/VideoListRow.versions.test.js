// 行上的版本徽标（D-MW-VERSIONS）：只在有组汇总时出现，点击发 toggle-versions，不影响原有动作。
import { mount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import VideoListRow from './VideoListRow.vue';

const video = { id: 7, name: 'movie.mkv', path: '/m/movie.mkv', directory: '/m', tags: [], duration: 60, watch_position_seconds: 0, is_watched: false };
const summary = { group_id: 2, revision: 1, member_count: 2, watched_count: 0, rated_count: 0, min_rating: null, max_rating: null, members: [] };

describe('VideoListRow 版本徽标', () => {
  it('不属于组时不显示徽标', () => {
    const wrapper = mount(VideoListRow, { props: { video } });
    expect(wrapper.find('[data-test="version-badge"]').exists()).toBe(false);
  });

  it('属于组时显示「N 个版本」，点击发 toggle-versions（不触发预览或播放）', async () => {
    const wrapper = mount(VideoListRow, { props: { video, versionSummary: summary, versionsExpanded: true } });
    const badge = wrapper.get('[data-test="version-badge"]');
    expect(badge.text()).toContain('2 个版本');
    expect(badge.attributes('aria-expanded')).toBe('true');
    await badge.trigger('click');
    expect(wrapper.emitted('toggle-versions')).toEqual([[video]]);
    expect(wrapper.emitted('preview')).toBeUndefined();
    expect(wrapper.emitted('play')).toBeUndefined();
  });
});
