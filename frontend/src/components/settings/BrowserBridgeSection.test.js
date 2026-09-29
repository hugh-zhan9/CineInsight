import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'GetBrowserBridgeStatus', 'RegenerateBrowserBridgeToken', 'SelectBrowserDownloadDirectory', 'ValidateScanDirectory'
].map(name => [name, vi.fn()])));
vi.mock('../../../wailsjs/go/main/App', () => api);

import BrowserBridgeSection, { downloadDirectoryCheckText } from './BrowserBridgeSection.vue';

beforeEach(() => {
  vi.resetAllMocks();
  api.GetBrowserBridgeStatus.mockResolvedValue({ running: false, enabled: false });
});

async function mountSection(form = {}) {
  const wrapper = mount(BrowserBridgeSection, { props: { form: { browser_download_directory: '', ...form } } });
  await flushPromises();
  return wrapper;
}

describe('下载目录是否在扫描范围内（D-PC25）', () => {
  it('MEDIA-09 选完目录就用 ValidateScanDirectory 检查，不在扫描范围时明说不会自动入库', async () => {
    const wrapper = await mountSection();
    api.SelectBrowserDownloadDirectory.mockResolvedValue('/Volumes/下载');
    api.ValidateScanDirectory.mockResolvedValue({ exists: true, duplicate_of: '', nested_in: '', contains: [] });
    await wrapper.vm.chooseDirectory();
    await flushPromises();
    expect(api.ValidateScanDirectory).toHaveBeenCalledWith('/Volumes/下载');
    const check = wrapper.get('[data-test="bridge-directory-check"]');
    expect(check.text()).toContain('不在片库扫描范围内');
    expect(check.classes()).toContain('bridge-directory-check--warn');
  });

  it('MEDIA-09 已有下载目录在挂载时就检查；在扫描范围内说会自动入库', async () => {
    api.ValidateScanDirectory.mockResolvedValue({ exists: true, duplicate_of: '', nested_in: '/Volumes/片库', contains: [] });
    const wrapper = await mountSection({ browser_download_directory: '/Volumes/片库/下载' });
    expect(api.ValidateScanDirectory).toHaveBeenCalledWith('/Volumes/片库/下载');
    expect(wrapper.get('[data-test="bridge-directory-check"]').text()).toContain('会自动入库');
  });

  it('MEDIA-09 三种检查结果各有说法；没有目录时不检查', async () => {
    expect(downloadDirectoryCheckText({ exists: false }).text).toContain('不存在');
    expect(downloadDirectoryCheckText({ exists: true, duplicate_of: '/a' }).level).toBe('ok');
    expect(downloadDirectoryCheckText({ exists: true, duplicate_of: '', nested_in: '' }).level).toBe('warn');
    expect(downloadDirectoryCheckText(null)).toBeNull();

    const wrapper = await mountSection();
    expect(api.ValidateScanDirectory).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="bridge-directory-check"]').exists()).toBe(false);
  });
});
