import { describe, it, expect, vi } from 'vitest';
const calls = vi.hoisted(() => ({ ListBTTasks: vi.fn(), ConfigureBTConnection: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => calls);
import { btAPI, btConnectionError } from './btAPI.js';
describe('BT Wails adapter', () => {
  it('preserves cursor and bounded page size without inventing an extra cursor', async () => {
    calls.ListBTTasks.mockResolvedValue([{ id: 'a' }, { id: 'b' }]);
    expect(await btAPI.ListBTTasks('before', 2)).toEqual({ tasks: [{ id: 'a' }, { id: 'b' }], next_cursor: 'b' });
    expect(calls.ListBTTasks).toHaveBeenCalledWith('before', 2);
    calls.ListBTTasks.mockResolvedValue([{ id: 'last' }]); expect((await btAPI.ListBTTasks('b', 2)).next_cursor).toBe('');
    calls.ListBTTasks.mockRejectedValue(new Error('failed')); await expect(btAPI.ListBTTasks('', 2)).rejects.toThrow('failed');
  });
  it('passes explicit credentials only to Configure, and sanitizes failures', async () => {
    const request = { prowlarr_key: { operation: 'replace', value: 'secret' } };
    await btAPI.ConfigureBTConnection(request); expect(calls.ConfigureBTConnection).toHaveBeenCalledWith(request);
    expect(btConnectionError(new Error('auth_failed: private/url'))).toContain('认证失败');
    expect(btConnectionError('private/url')).not.toContain('private');
  });
});

it('maps the actual client literals and hides unrecognized upstream errors', () => {
 for (const [code, text] of [['bt_auth_failed', '认证失败'], ['bt_service_unavailable', '服务暂时不可用'], ['bt_connection_unavailable', '配置并启用'], ['bt_credentials_busy', '凭据正在'], ['bt_credentials_unavailable', '系统钥匙串不可用']]) {
  expect(btConnectionError(`${code}: secret`)).toContain(text); expect(btConnectionError(`${code}: secret`)).not.toContain('secret');
 }
 expect(btConnectionError('unknown: secret')).not.toContain('secret');
});

it('explains the restore gate without suggesting credential changes or exposing raw errors', () => {
 const expected = '下载任务的恢复核实尚未完成，请稍后重试；如持续未完成，请重新打开应用。';
 for (const error of ['bt_restore_pending: private-url', new Error('bt_restore_pending: private-path'), { code: 'bt_restore_pending', message: 'private-key' }]) {
  expect(btConnectionError(error)).toBe(expected);
 }
});
