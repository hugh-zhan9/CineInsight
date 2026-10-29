import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetSettings: vi.fn(), RememberDeleteFileChoice: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import { readDeletePreferences, shouldConfirmTrash, rememberDeleteFileChoice } from './trashConfirm.js';

beforeEach(() => vi.resetAllMocks());

describe('shouldConfirmTrash（删除前确认设置）', () => {
  it('follows confirm_before_delete; unset, empty or unreadable settings mean the default (on)', async () => {
    api.GetSettings.mockResolvedValueOnce({ confirm_before_delete: false });
    expect(await shouldConfirmTrash()).toBe(false);
    api.GetSettings.mockResolvedValueOnce({ confirm_before_delete: true });
    expect(await shouldConfirmTrash()).toBe(true);
    api.GetSettings.mockResolvedValueOnce({});
    expect(await shouldConfirmTrash()).toBe(true);
    api.GetSettings.mockResolvedValueOnce(null);
    expect(await shouldConfirmTrash()).toBe(true);
    api.GetSettings.mockRejectedValueOnce(new Error('数据库繁忙'));
    expect(await shouldConfirmTrash()).toBe(true);
  });
});

describe('readDeletePreferences（删除前确认 + 删除原文件，一次读取）', () => {
  it('reads both settings; unset or unreadable settings fall back to confirm first and keep the original file', async () => {
    api.GetSettings.mockResolvedValueOnce({ confirm_before_delete: false, delete_original_file: true });
    expect(await readDeletePreferences()).toEqual({ confirmFirst: false, deleteOriginal: true });
    api.GetSettings.mockResolvedValueOnce({});
    expect(await readDeletePreferences()).toEqual({ confirmFirst: true, deleteOriginal: false });
    api.GetSettings.mockRejectedValueOnce(new Error('数据库繁忙'));
    expect(await readDeletePreferences()).toEqual({ confirmFirst: true, deleteOriginal: false });
    expect(api.GetSettings).toHaveBeenCalledTimes(3);
  });
});

it('remembers both choices using the dedicated single-field call and reports persistence failure', async () => {
 api.RememberDeleteFileChoice.mockResolvedValueOnce();
 expect(await rememberDeleteFileChoice(true)).toBe(true);
 expect(api.RememberDeleteFileChoice).toHaveBeenLastCalledWith(true);
 api.RememberDeleteFileChoice.mockRejectedValueOnce(new Error('write failed'));
 expect(await rememberDeleteFileChoice(false)).toBe(false);
 expect(api.RememberDeleteFileChoice).toHaveBeenLastCalledWith(false);
});
