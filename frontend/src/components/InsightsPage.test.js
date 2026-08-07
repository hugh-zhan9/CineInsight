import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetLibraryInsights: vi.fn(), GetImageInsights: vi.fn() }));
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
    expect(wrapper.text()).toContain('图片库还没有图片');
  });

  it('renders the image section with summary cards and bucket charts', async () => {
    api.GetLibraryInsights.mockResolvedValue({ summary: { video_count: 0 } });
    api.GetImageInsights.mockResolvedValue({
      generated_at: '2026-08-07T12:00:00Z',
      summary: { image_count: 4, total_size: 2147483648, favorite_count: 2 },
      storage_by_directory: [{ label: '/pics/b', total_size: 1610612736 }, { label: '/pics/a', total_size: 536870912 }],
      storage_by_format: [{ label: 'heic', total_size: 1610612736 }, { label: 'jpg', total_size: 536870912 }]
    });
    const wrapper = mount(InsightsPage);
    await flushPromises();

    expect(wrapper.text()).toContain('图片总数');
    expect(wrapper.text()).toContain('图片目录存储');
    expect(wrapper.text()).toContain('图片格式存储');
    expect(wrapper.text()).toContain('/pics/b');
    expect(wrapper.text()).toContain('heic');
    expect(wrapper.text()).toContain('1.5 GB');
  });

  it('keeps the video section rendered when the image section fails to load', async () => {
    api.GetLibraryInsights.mockResolvedValue({
      generated_at: '2026-08-04T12:00:00Z',
      summary: { video_count: 3, total_duration: 7200, total_size: 1073741824, watched_count: 1, watched_percent: 33.333 },
      storage_by_directory: [{ label: '/library', count: 3, bytes: 1073741824 }],
      storage_by_tag: [], storage_by_resolution: [], watch_heatmap: [], rating_distribution: [], top_ai_tags: []
    });
    api.GetImageInsights.mockRejectedValue(new Error('image stats unavailable'));
    const wrapper = mount(InsightsPage);
    await flushPromises();

    expect(wrapper.text()).toContain('33.3%');
    expect(wrapper.text()).toContain('/library');
    expect(wrapper.text()).toContain('读取图片洞察失败');
    expect(wrapper.text()).not.toContain('读取片库洞察失败');
  });

  it('keeps the image section rendered when the video section fails to load', async () => {
    api.GetLibraryInsights.mockRejectedValue(new Error('video stats unavailable'));
    api.GetImageInsights.mockResolvedValue({
      generated_at: '2026-08-07T12:00:00Z',
      summary: { image_count: 1, total_size: 1024, favorite_count: 0 },
      storage_by_directory: [{ label: '/pics', total_size: 1024 }],
      storage_by_format: [{ label: 'png', total_size: 1024 }]
    });
    const wrapper = mount(InsightsPage);
    await flushPromises();

    expect(wrapper.text()).toContain('读取片库洞察失败');
    expect(wrapper.text()).toContain('图片总数');
    expect(wrapper.text()).toContain('/pics');
  });
});
