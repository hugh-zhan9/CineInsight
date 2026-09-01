import { shallowMount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';

vi.mock('../../wailsjs/go/main/App', () => ({
  GetLibraryInsights: vi.fn(() => Promise.resolve(null)),
  GetImageInsights: vi.fn(() => Promise.resolve(null))
}));

import InsightsPage from './InsightsPage.vue';

function mountWith(stats, directories = []) {
  const wrapper = shallowMount(InsightsPage, { props: { directories } });
  wrapper.vm.stats = stats;
  return wrapper;
}

const baseStats = {
  generated_at: '2026-09-01T12:00:00Z',
  summary: { video_count: 4, total_duration: 4 * 3600, total_size: 10, watched_count: 1, watched_percent: 25, recent_added_count: 2 },
  watch_heatmap: [],
  rating_distribution: [{ rating: 7, count: 1 }, { rating: 8.5, count: 3 }]
};

describe('InsightsPage 摘要副行', () => {
  it('平均单片时长由总时长除以条数推出，不另开后端查询', () => {
    const wrapper = mountWith(baseStats);
    expect(wrapper.vm.averageDurationText).toBe('1.0 小时');
    wrapper.vm.stats = { ...baseStats, summary: { ...baseStats.summary, video_count: 0 } };
    expect(wrapper.vm.averageDurationText).toBe('—');
    wrapper.unmount();
  });

  it('评分补齐 21 档空档，中位数与已评分数由分布推出', () => {
    const wrapper = mountWith(baseStats);
    expect(wrapper.vm.ratingBuckets).toHaveLength(21);
    expect(wrapper.vm.ratingBuckets[0]).toEqual({ rating: 0, count: 0 });
    expect(wrapper.vm.ratedCount).toBe(4);
    // 1 个 7.0 + 3 个 8.5，中位数落在 8.5。
    expect(wrapper.vm.ratingMedian).toBe(8.5);
    wrapper.unmount();
  });

  it('没有评分时中位数为 null 而不是 0', () => {
    const wrapper = mountWith({ ...baseStats, rating_distribution: [] });
    expect(wrapper.vm.ratedCount).toBe(0);
    expect(wrapper.vm.ratingMedian).toBeNull();
    wrapper.unmount();
  });

  it('卷可用性从扫描目录与监听状态推出，没有目录时不显示', () => {
    const wrapper = mountWith(baseStats, [
      { path: '/Volumes/Media/影片', watch_state: 'watching' },
      { path: '/Volumes/Media/剧集', watch_state: 'watching' },
      { path: '/Volumes/Archive2/旧片', watch_state: 'unavailable' }
    ]);
    expect(wrapper.vm.volumeSummaryText).toBe('2 个卷 · 1 个当前不可用');

    wrapper.vm.$.props.directories = [];
    const empty = mountWith(baseStats, []);
    expect(empty.vm.volumeSummaryText).toBe('');
    empty.unmount();
    wrapper.unmount();
  });

  it('最长连续观看天数由热力图推出', () => {
    const days = [];
    const cursor = new Date('2026-08-20T00:00:00');
    for (let index = 0; index < 3; index += 1) {
      const date = new Date(cursor);
      date.setDate(date.getDate() + index);
      days.push({ date: date.toISOString().slice(0, 10), count: 2 });
    }
    const wrapper = mountWith({ ...baseStats, generated_at: '2026-09-01T12:00:00', watch_heatmap: days });
    expect(wrapper.vm.heatmapTotal).toBe(6);
    expect(wrapper.vm.longestWatchStreak).toBe(3);
    wrapper.unmount();
  });
});
