import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, expect, it, vi } from 'vitest';
import JellyfinSection from './JellyfinSection.vue';

const api = vi.hoisted(() => ({ GetJellyfinStatus: vi.fn(), ConfigureJellyfin: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
const disabled = { enabled: false, running: false, username: '', port: 8096, password_set: false, lan_urls: [] };
beforeEach(() => { vi.resetAllMocks(); api.GetJellyfinStatus.mockResolvedValue(disabled); });

it('默认关，配置独立保存，成功后清除明文密码', async () => {
  const wrapper = mount(JellyfinSection);
  await flushPromises();
  expect(wrapper.get('[data-test="jellyfin-enabled"]').element.checked).toBe(false);
  await wrapper.get('[data-test="jellyfin-enabled"]').setValue(true);
  await wrapper.get('#jellyfin-username').setValue('viewer');
  await wrapper.get('#jellyfin-password').setValue('test-password');
  api.ConfigureJellyfin.mockResolvedValue({ ...disabled, enabled: true, running: true, username: 'viewer', password_set: true, lan_urls: ['http://192.168.1.4:8096/'] });
  await wrapper.get('[data-test="jellyfin-apply"]').trigger('click');
  await flushPromises();
  expect(api.ConfigureJellyfin).toHaveBeenCalledWith({ enabled: true, username: 'viewer', password: 'test-password', port: 8096 });
  expect(wrapper.get('#jellyfin-password').element.value).toBe('');
  expect(wrapper.get('[data-test="jellyfin-address"]').text()).toContain('192.168.1.4');
  expect(wrapper.get('[data-test="jellyfin-status"]').text()).toContain('运行中');
  wrapper.unmount();
});

it('读取失败不能提交默认值；端口失败如实显示未运行', async () => {
  api.GetJellyfinStatus.mockRejectedValueOnce(new Error('offline'));
  const wrapper = mount(JellyfinSection);
  await flushPromises();
  expect(wrapper.get('[data-test="jellyfin-apply"]').element.disabled).toBe(true);
  expect(wrapper.get('[role="alert"]').text()).toContain('offline');
  api.GetJellyfinStatus.mockResolvedValue({ ...disabled, enabled: true, startup_error: '端口无法监听' });
  await wrapper.vm.load();
  expect(wrapper.get('[data-test="jellyfin-status"]').text()).toBe('已配置，未运行');
  expect(wrapper.get('[role="alert"]').text()).toContain('端口无法监听');
  wrapper.unmount();
});
