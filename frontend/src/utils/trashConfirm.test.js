import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetSettings: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import { shouldConfirmTrash } from './trashConfirm.js';

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
