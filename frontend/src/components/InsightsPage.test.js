import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetLibraryInsights: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import InsightsPage from './InsightsPage.vue';

beforeEach(() => { vi.clearAllMocks(); });

describe('InsightsPage', () => {
  it('renders summary, distributions, and a full-year heatmap', async () => {
    api.GetLibraryInsights.mockResolvedValue({
      generated_at: '2026-08-04T12:00:00Z',
      summary: { video_count: 3, total_duration: 7200, total_size: 1073741824, watched_count: 1, watched_percent: 33.333 },
      storage_by_directory: [{ label: '/library', count: 3, bytes: 1073741824 }],
      storage_by_tag: [{ label: '剧情', count: 2, bytes: 800 }],
      storage_by_resolution: [{ label: '1080p', count: 3, bytes: 900 }],
      watch_heatmap: [{ date: '2026-08-03', count: 2 }],
      rating_distribution: [{ rating: 8.5, count: 2 }],
      top_ai_tags: [{ label: '室内', count: 2, bytes: 800 }]
    });
    const wrapper = mount(InsightsPage);
    await flushPromises();

    expect(wrapper.text()).toContain('片库洞察');
    expect(wrapper.text()).toContain('33.3%');
    expect(wrapper.text()).toContain('剧情');
    expect(wrapper.findAll('.watch-heatmap span')).toHaveLength(365);
    expect(wrapper.findAll('.watch-heatmap .heat-2')).toHaveLength(1);
  });

  it('shows a useful empty state', async () => {
    api.GetLibraryInsights.mockResolvedValue({ summary: { video_count: 0 } });
    const wrapper = mount(InsightsPage);
    await flushPromises();
    expect(wrapper.text()).toContain('片库还没有视频');
  });
});
