import { mount, flushPromises } from '@vue/test-utils';
import { describe, it, expect, vi } from 'vitest';
const api = vi.hoisted(() => ({ GetBTConnection: vi.fn(), ConfigureBTConnection: vi.fn(), TestBTConnection: vi.fn() }));
vi.mock('../../utils/btAPI.js', () => ({ btAPI: api, btConnectionError: () => '认证失败' }));
import BTConnectionSection from './BTConnectionSection.vue';
describe('independent BT settings', () => {
 it('keeps credentials out of general settings, tests saved state, and tracks dirty fields', async () => {
  const c = { revision: 1, enabled: true, credential_status: 'busy', download_root: '/films' };
  api.GetBTConnection.mockResolvedValue(c); api.ConfigureBTConnection.mockResolvedValue({ ...c, revision: 2, credential_status: 'configured' }); api.TestBTConnection.mockResolvedValue({ prowlarr_status: 'ok', qb_status: 'auth_failed' });
  const w = mount(BTConnectionSection); await flushPromises(); expect(w.text()).toContain('正在使用');
  await w.get('[data-test="bt-prowlarr_key-op"]').setValue('replace'); await w.get('[data-test="bt-prowlarr_key-value"]').setValue('secret');
  expect(w.emitted('update:dirty').at(-1)).toEqual([true]);
  await w.get('form').trigger('submit'); await flushPromises(); expect(api.ConfigureBTConnection.mock.calls.at(-1)[0].prowlarr_key.value).toBe('secret');
  expect(w.html()).not.toContain('secret'); expect(w.emitted('update:dirty').at(-1)).toEqual([false]);
  await w.get('[data-test="bt-test-connection"]').trigger('click'); await flushPromises(); expect(api.TestBTConnection).toHaveBeenCalledWith(); expect(w.text()).toContain('qBittorrent：认证失败'); w.unmount();
 });
 it('does not erase typed secrets on save failure', async () => {
  api.GetBTConnection.mockResolvedValue({ revision: 1 }); api.ConfigureBTConnection.mockRejectedValue(new Error('private'));
  const w = mount(BTConnectionSection); await flushPromises(); await w.get('[data-test="bt-qb_password-op"]').setValue('replace'); await w.get('[data-test="bt-qb_password-value"]').setValue('keep-me');
  await w.get('form').trigger('submit'); await flushPromises(); expect(w.get('[data-test="bt-qb_password-value"]').element.value).toBe('keep-me'); expect(w.text()).not.toContain('private'); w.unmount();
 });
});

it('shows actual optional versions and never assumes metadata support when absent or failed', async () => {
 api.GetBTConnection.mockResolvedValue({ revision: 1 });
 const w = mount(BTConnectionSection); await flushPromises();
 api.TestBTConnection.mockResolvedValue({ prowlarr_status: 'ok', qb_status: 'ok', prowlarr_version: '2.3.0', qb_version: '5.2.3', qb_api_version: '2.15.1', metadata_preview_status: 'supported' });
 await w.vm.test(); expect(w.text()).toContain('2.3.0'); expect(w.text()).toContain('5.2.3 · API 2.15.1'); expect(w.get('[data-test="bt-metadata-capability"]').text()).toBe('磁力文件清单：可用');
 api.TestBTConnection.mockResolvedValue({ prowlarr_status: 'ok', qb_status: 'ok', metadata_preview_status: 'unsupported' });
 await w.vm.test(); expect(w.get('[data-test="bt-metadata-capability"]').text()).toContain('此版本未验证'); expect(w.text()).not.toContain('2.15.1');
 api.TestBTConnection.mockResolvedValue({ prowlarr_status: 'ok', qb_status: 'ok' });
 await w.vm.test(); expect(w.get('[data-test="bt-metadata-capability"]').text()).toContain('未能完成检查');
 api.TestBTConnection.mockRejectedValue(new Error('private'));
 await w.vm.test(); expect(w.get('[data-test="bt-metadata-capability"]').text()).toContain('未能完成检查'); expect(w.text()).not.toContain('private'); w.unmount();
});
