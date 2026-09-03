import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

// 这个用例要的是真行 + 真菜单的整链路，所以不 stub 子组件；
// 后端调用一律给一个自动生成的 vi.fn()，页面挂载时的探活请求不至于炸。
const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));

vi.mock('../../wailsjs/go/main/App', () => api);

import VideoListPage from './VideoListPage.vue';

async function mountPageWithOneRow() {
  api.SearchLibraryVideoPage.mockResolvedValue({ videos: [] });
  api.CountLibraryVideos.mockResolvedValue(0);
  api.GetSemanticIndexStatus.mockResolvedValue({ available: true, unavailable: '' });
  api.ListSavedLibraryViews.mockResolvedValue([]);
  api.GetSubtitleQueueState.mockResolvedValue({ active_task: null, queued_tasks: [], total: 0 });
  api.GetAITaggingStatusSummary.mockResolvedValue({ same_source_unread: 0 });
  for (const name of ['GetTechnicalBackfillStatus', 'GetPerceptualHashBackfillStatus', 'GetLocalMetadataBackfillStatus', 'GetLocalMetadataExportStatus']) {
    api[name].mockResolvedValue({ running: false, completed: false, cancelled: false, failed: 0, failures: [] });
  }
  api.GetSettings.mockResolvedValue({ scan_exclude_paths: '' });

  const wrapper = mount(VideoListPage, {
    attachTo: document.body,
    props: { tags: [], settings: {}, directories: [] }
  });
  await flushPromises();
  wrapper.vm.videos = [{ id: 1, name: 'a.mp4', path: '/a.mp4', size: 1, tags: [] }];
  wrapper.vm.loading = false;
  wrapper.vm.homeListVirtualizationEnabled = false;
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.innerHTML = '';
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: { getItem: vi.fn(() => null), setItem: vi.fn(), removeItem: vi.fn(), clear: vi.fn() }
  });
});

describe('行内 ⋯ 菜单', () => {
  it('点 ⋯ 后菜单留在屏幕上，不被同一次 click 冒泡关掉', async () => {
    const wrapper = await mountPageWithOneRow();

    await wrapper.find('[aria-label="更多操作"]').trigger('click');
    await flushPromises();

    expect(wrapper.vm.rowMenu.video?.id).toBe(1);
    expect(document.querySelectorAll('.base-menu__item').length).toBeGreaterThan(0);

    wrapper.unmount();
  });

  it('点菜单外部才关闭', async () => {
    const wrapper = await mountPageWithOneRow();
    await wrapper.find('[aria-label="更多操作"]').trigger('click');
    await flushPromises();

    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    await flushPromises();

    expect(wrapper.vm.rowMenu.video).toBe(null);
    expect(document.querySelectorAll('.base-menu__item').length).toBe(0);

    wrapper.unmount();
  });
});
