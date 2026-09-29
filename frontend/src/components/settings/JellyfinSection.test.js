import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, expect, it, vi } from 'vitest';
import JellyfinSection from './JellyfinSection.vue';

const api = vi.hoisted(() => ({ GetJellyfinStatus: vi.fn(), ConfigureJellyfin: vi.fn(), GetJellyfinDiagnostics: vi.fn() }));
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
vi.mock('../../utils/feedback.js', () => feedback);
const disabled = { enabled: false, running: false, username: '', port: 8096, password_set: false, lan_urls: [] };
beforeEach(() => {
  vi.resetAllMocks();
  api.GetJellyfinStatus.mockResolvedValue(disabled);
  api.GetJellyfinDiagnostics.mockResolvedValue({ enabled: false, listening: false, last_client: '', last_failure: null });
  feedback.confirmAction.mockResolvedValue(true);
});

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

it('PLAY-14 诊断显示最近连接与最近失败；没有失败（last_failure 为 null）时明说', async () => {
  api.GetJellyfinDiagnostics.mockResolvedValue({
    enabled: true, listening: true, last_request_at: '2026-09-29T12:00:00Z', last_client: 'Fileball', last_failure: null
  });
  const wrapper = mount(JellyfinSection);
  await flushPromises();
  expect(wrapper.get('[data-test="jellyfin-last-request"]').text()).toContain('Fileball');
  expect(wrapper.get('[data-test="jellyfin-last-failure"]').text()).toBe('最近没有失败的请求。');

  api.GetJellyfinDiagnostics.mockResolvedValue({
    enabled: true, listening: true, last_request_at: '2026-09-29T12:00:00Z', last_client: 'Fileball',
    last_failure: { at: '2026-09-29T12:01:00Z', route_shape: '/Users/{id}/Items', status: 401 }
  });
  await wrapper.vm.load();
  const failure = wrapper.get('[data-test="jellyfin-last-failure"]').text();
  expect(failure).toContain('/Users/{id}/Items');
  expect(failure).toContain('HTTP 401');
  wrapper.unmount();
});

it('PLAY-14 服务开着时重新应用配置前提示所有已登录的客户端需要重新登录；取消则不提交', async () => {
  api.GetJellyfinStatus.mockResolvedValue({ ...disabled, enabled: true, running: true, username: 'viewer', password_set: true });
  const wrapper = mount(JellyfinSection);
  await flushPromises();

  feedback.confirmAction.mockResolvedValueOnce(false);
  await wrapper.get('[data-test="jellyfin-apply"]').trigger('click');
  await flushPromises();
  expect(feedback.confirmAction.mock.calls[0][0].message).toContain('所有已登录的客户端需要重新登录');
  expect(api.ConfigureJellyfin).not.toHaveBeenCalled();

  api.ConfigureJellyfin.mockResolvedValue({ ...disabled, enabled: true, running: true, username: 'viewer', password_set: true });
  await wrapper.get('[data-test="jellyfin-apply"]').trigger('click');
  await flushPromises();
  expect(api.ConfigureJellyfin).toHaveBeenCalledTimes(1);
  // 会话已持久化：说明文字不再说「应用重启后需要重新登录」。
  expect(wrapper.text()).toContain('应用重启后客户端不用重新登录');
  wrapper.unmount();
});

it('APP-14 Jellyfin 是有自己锚点与标题的独立分区', async () => {
  const wrapper = mount(JellyfinSection);
  await flushPromises();
  expect(wrapper.get('#settings-jellyfin h3').text()).toBe('Jellyfin 客户端连接');
  wrapper.unmount();
});
