import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetIINASyncStatus: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);

import IINASyncSection from './IINASyncSection.vue';

let syncedHandler = null;
const wrappers = [];

beforeEach(() => {
  vi.resetAllMocks();
  syncedHandler = null;
  window.runtime = {
    EventsOn: (name, handler) => {
      if (name === 'iina-progress-synced') syncedHandler = handler;
      return () => {};
    }
  };
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
});

async function mountSection() {
  const wrapper = mount(IINASyncSection);
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}

it('PLAY-14 设置页能看到 IINA 是否在同步、最近同步时间与最近的失败原因', async () => {
  api.GetIINASyncStatus.mockResolvedValue({
    enabled: true, watching_dir: '/Users/me/Library/Application Support/com.colliderli.iina/watch_later',
    last_sync_at: '2026-09-29T10:00:00Z', last_error: ''
  });
  const wrapper = await mountSection();
  expect(wrapper.get('#settings-iina-sync h3').text()).toBe('IINA 进度同步');
  expect(wrapper.get('[data-test="iina-sync-state"]').text()).toBe('正在同步');
  expect(wrapper.get('[data-test="iina-sync-last"]').text()).toContain('最近一次同步');
  expect(wrapper.find('[data-test="iina-sync-error"]').exists()).toBe(false);

  api.GetIINASyncStatus.mockResolvedValue({ enabled: false, watching_dir: '', last_error: '读取断点文件失败' });
  syncedHandler({ updated: 1 });
  await flushPromises();
  expect(wrapper.get('[data-test="iina-sync-state"]').text()).toBe('未在同步');
  expect(wrapper.get('[data-test="iina-sync-last"]').text()).toBe('还没有成功同步过。');
  expect(wrapper.get('[data-test="iina-sync-error"]').text()).toContain('读取断点文件失败');
});

it('PLAY-14 没装 IINA 时说清原因，读取失败时如实报错', async () => {
  api.GetIINASyncStatus.mockResolvedValue({ enabled: false, watching_dir: '', last_error: '' });
  const wrapper = await mountSection();
  expect(wrapper.get('[data-test="iina-sync-state"]').text()).toContain('没有找到 IINA 的播放断点目录');

  api.GetIINASyncStatus.mockRejectedValue(new Error('服务未就绪'));
  await wrapper.get('[data-test="iina-sync-refresh"]').trigger('click');
  await flushPromises();
  expect(wrapper.get('[data-test="iina-sync-status"]').text()).toContain('读取 IINA 同步状态失败');
});
