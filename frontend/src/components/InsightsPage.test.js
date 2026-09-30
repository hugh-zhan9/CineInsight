import { flushPromises, shallowMount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';

vi.mock('../../wailsjs/go/main/App', () => ({
  GetLibraryInsights: vi.fn(() => Promise.resolve(null)),
  GetImageInsights: vi.fn(() => Promise.resolve(null))
}));

import InsightsPage from './InsightsPage.vue';
import { GetLibraryInsights } from '../../wailsjs/go/main/App';

function mountWith(stats, directories = []) {
  const wrapper = shallowMount(InsightsPage, { props: { directories } });
  wrapper.vm.stats = stats;
  return wrapper;
}

const baseStats = {
  generated_at: '2026-09-01T12:00:00Z',
  summary: { video_count: 4, viewed_count: 3, viewed_percent: 75, total_duration: 4 * 3600, total_size: 10, watched_count: 1, watched_percent: 25, recent_added_count: 2 },
  watch_heatmap: [],
  rating_distribution: [{ rating: 7, count: 1 }, { rating: 8.5, count: 3 }],
  total_play_events: 0,
  plays_by_source: {}
};

describe('InsightsPage 摘要副行', () => {
  it('shows distinct viewed coverage separately from completed/manual marks and play events', async () => {
    const wrapper = mountWith({ ...baseStats, total_play_events: 1217,
      summary: { ...baseStats.summary, video_count: 13237, viewed_count: 1510, viewed_percent: 1510 / 13237 * 100, watched_count: 1, watched_percent: 1 / 13237 * 100 }
    });
    await wrapper.vm.$nextTick();
    const coverage = wrapper.get('[data-test="insights-viewed-coverage"]');
    expect(coverage.text()).toContain('11.4%'); expect(coverage.text()).toContain('1,510 / 13,237 部');
    expect(wrapper.get('[data-test="insights-watched-marks"]').text()).toBe('标记已看 1 部（<0.1%）');
    expect(wrapper.get('.insights-panel--wide').text()).toContain('播放事件 1,217');
    expect(wrapper.get('.insights-panel--wide').text()).toContain('重复播放及已删除影片');
    expect(wrapper.get('.insights-summary').text()).not.toContain('播放事件');
    wrapper.unmount();
  });
  it('distinguishes zero coverage from a small positive percentage', () => {
    const wrapper = mountWith(baseStats);
    expect(wrapper.vm.formatPercent(0)).toBe('0.0%');
    expect(wrapper.vm.formatPercent(0.0076)).toBe('<0.1%');
    expect(wrapper.vm.formatPercent(100)).toBe('100.0%');
    wrapper.unmount();
  });

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

  // P-037（D-PC43、PLAY-07）改了来源叫法：桌面两种来源标「启动播放」，其余是越过阈值的有效观看。
  it('播放事件总数与来源拆分独立于覆盖率', () => {
    const wrapper = mountWith({
      ...baseStats,
      total_play_events: 1234,
      plays_by_source: { desktop_play: 800, desktop_random: 300, mobile_feed: 100, legacy: 34 }
    });
    expect(wrapper.vm.playEventsSummaryText)
      .toBe('播放事件 1,234 · 启动播放 800 · 随机启动播放 300 · 手机观看 100 · 历史补记 34');
    wrapper.unmount();
  });

  it('来源为空时只显示总数，计数为 0 的来源不占位', () => {
    const wrapper = mountWith({ ...baseStats, total_play_events: 0, plays_by_source: {} });
    expect(wrapper.vm.playEventsSummaryText).toBe('播放事件 0');
    wrapper.vm.stats = { ...baseStats, total_play_events: 5, plays_by_source: { desktop_play: 5, mobile_feed: 0 } };
    expect(wrapper.vm.playEventsSummaryText).toBe('播放事件 5 · 启动播放 5');
    wrapper.unmount();
  });

  it('后端缺字段时不报错，认不出的来源按原样列出', () => {
    const wrapper = mountWith({ ...baseStats, total_play_events: undefined, plays_by_source: undefined });
    expect(wrapper.vm.playEventsSummaryText).toBe('播放事件 0');
    wrapper.vm.stats = { ...baseStats, total_play_events: 2, plays_by_source: { future_source: 2 } };
    expect(wrapper.vm.playEventsSummaryText).toBe('播放事件 2 · future_source 2');
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

describe('InsightsPage 观看记录（P-037，D-PC43）', () => {
  function heatmapStats(days) {
    return { ...baseStats, generated_at: '2026-09-01T12:00:00', watch_heatmap: days };
  }
  // 走一遍真实的加载：挂载时的 GetLibraryInsights 就返回这份统计，不会在断言途中被 null 覆盖。
  async function mountLoaded(stats) {
    GetLibraryInsights.mockResolvedValueOnce(stats);
    const wrapper = shallowMount(InsightsPage, { props: { directories: [] } });
    await flushPromises();
    return wrapper;
  }
  const days = [
    { date: '2026-08-30', count: 3, by_source: { desktop_play: 2, inline_view: 1 } },
    { date: '2026-08-31', count: 2, by_source: { mobile_feed: 1, jellyfin_view: 1 } },
    { date: '2026-09-01', count: 1, by_source: { desktop_random: 1 } }
  ];

  it('PLAY-07 热力图标题为「观看记录」，并写明启动播放与有效观看的口径', async () => {
    const wrapper = await mountLoaded(heatmapStats(days));
    const panel = wrapper.get('[data-test="insights-watch-records"]');
    expect(panel.get('h3').text()).toBe('观看记录');
    expect(panel.text()).not.toContain('观看热力');
    const note = wrapper.get('[data-test="insights-watch-records-note"]').text();
    expect(note).toContain('「启动播放」在应用启动外部播放器时即记一次');
    expect(note).toContain('累计超过 60 秒');
    wrapper.unmount();
  });

  it('PLAY-07 按来源分列：桌面来源标「启动播放」，内嵌观看、Jellyfin 观看都有标签，计数取近一年 by_source 之和', async () => {
    const wrapper = await mountLoaded(heatmapStats(days));
    expect(wrapper.vm.heatmapSourceOptions).toEqual([
      { source: 'all', label: '全部', count: 6 },
      { source: 'desktop_play', label: '启动播放', count: 2 },
      { source: 'desktop_random', label: '随机启动播放', count: 1 },
      { source: 'inline_view', label: '内嵌观看', count: 1 },
      { source: 'mobile_feed', label: '手机观看', count: 1 },
      { source: 'jellyfin_view', label: 'Jellyfin 观看', count: 1 }
    ]);
    const buttons = wrapper.findAll('[data-test="insights-heatmap-sources"] button');
    expect(buttons.map(button => button.text())).toEqual(['全部6', '启动播放2', '随机启动播放1', '内嵌观看1', '手机观看1', 'Jellyfin 观看1']);
    expect(wrapper.get('[data-test="insights-heatmap-total"]').text()).toContain('近一年 6 条');
    wrapper.unmount();
  });

  it('PLAY-07 选中某一来源后热力图只按该来源着色，悬停提示仍列出当天全部来源', async () => {
    const wrapper = await mountLoaded(heatmapStats(days));
    await wrapper.get('[data-test="insights-heatmap-source-inline_view"]').trigger('click');
    expect(wrapper.vm.heatmapTotal).toBe(1);
    expect(wrapper.get('[data-test="insights-heatmap-total"]').text()).toContain('近一年内嵌观看 1 条');
    const day = wrapper.vm.heatmapDays.find(item => item.date === '2026-08-30');
    expect(day.count).toBe(1);
    expect(day.title).toBe('2026-08-30 · 共 3 条（启动播放 2 · 内嵌观看 1）');
    expect(wrapper.vm.heatmapDays.find(item => item.date === '2026-08-31').count).toBe(0);
    expect(wrapper.get('[data-test="insights-heatmap-source-inline_view"]').attributes('aria-pressed')).toBe('true');
    wrapper.unmount();
  });

  it('PLAY-07 选中的来源在刷新后没有了就回到「全部」；没有 by_source 的日子照常按总数着色', async () => {
    const wrapper = await mountLoaded(heatmapStats(days));
    wrapper.vm.heatmapSource = 'jellyfin_view';
    wrapper.vm.stats = heatmapStats([{ date: '2026-09-01', count: 4 }]);
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.activeHeatmapSource).toBe('all');
    expect(wrapper.get('[data-test="insights-heatmap-source-all"]').attributes('aria-pressed')).toBe('true');
    expect(wrapper.vm.heatmapTotal).toBe(4);
    expect(wrapper.vm.heatmapSourceOptions).toEqual([{ source: 'all', label: '全部', count: 4 }]);
    expect(wrapper.vm.heatmapDays.find(item => item.date === '2026-09-01').title).toBe('2026-09-01 · 4 条');
    wrapper.unmount();
  });

  it('PLAY-07 账本摘要补上 inline_view 与 jellyfin_view 的叫法', () => {
    const wrapper = mountWith({ ...baseStats, total_play_events: 3, plays_by_source: { inline_view: 2, jellyfin_view: 1 } });
    expect(wrapper.vm.playEventsSummaryText).toBe('播放事件 3 · 内嵌观看 2 · Jellyfin 观看 1');
    wrapper.unmount();
  });
});
