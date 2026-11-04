import { describe, expect, it, vi } from 'vitest';
import { registerImageEditor, requestImageEditorsLeave } from './imageEditingGuard.js';
describe('image editing navigation guard', () => {
  it('leaves ordinary navigation synchronous and respects keeping an active draft', async () => {
    expect(requestImageEditorsLeave()).toBe(true);
    const close = vi.fn().mockResolvedValue(false), unregister = registerImageEditor(close);
    try { expect(await requestImageEditorsLeave()).toBe(false); expect(close).toHaveBeenCalledTimes(1); }
    finally { unregister(); }
    expect(requestImageEditorsLeave()).toBe(true);
  });
  it('removes an accepted editor so continuing navigation does not ask again', async () => {
    let unregister; unregister = registerImageEditor(async () => { unregister(); return true; });
    expect(await requestImageEditorsLeave()).toBe(true); expect(requestImageEditorsLeave()).toBe(true);
  });
});
