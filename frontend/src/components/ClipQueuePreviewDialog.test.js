import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, expect, it, vi } from 'vitest';
import Dialog from './ClipQueuePreviewDialog.vue';
let wrapper; afterEach(() => { wrapper?.unmount(); wrapper = null; });
const preview = () => ({ preview_token: 'frozen', expires_at: '2099-01-01T00:00:00Z', topic_title: '专题', items: [{ bookmark_id: 1, video_id: 2, title: '片段', start_ms: 1000, end_ms: 2000 }], conflicts: 0, requires_inline: true });
it('never switches the player without explicit consent and preserves the token on submission', async () => {
  const api = { append: vi.fn().mockResolvedValue({ entry_ids: [5] }), discard: vi.fn().mockResolvedValue({}) };
  wrapper = mount(Dialog, { props: { preview: preview(), api }, global: { stubs: { teleport: true } } });
  expect(wrapper.get('[data-test="clip-preview-confirm"]').element.disabled).toBe(true); await wrapper.vm.submit(); expect(api.append).not.toHaveBeenCalled();
  await wrapper.get('[data-test="clip-switch-inline"]').setValue(true); await wrapper.get('[data-test="clip-preview-confirm"]').trigger('click'); await flushPromises();
  expect(api.append).toHaveBeenCalledWith({ preview_token: 'frozen', switch_to_inline: true }); expect(wrapper.emitted('queued')).toHaveLength(1);
});
it('refuses a conflicting batch and discards only unsubmitted authority when closed', async () => {
  const api = { append: vi.fn(), discard: vi.fn().mockResolvedValue({}) };
  wrapper = mount(Dialog, { props: { preview: { ...preview(), requires_inline: false, conflicts: 1, preview_token: '' }, api }, global: { stubs: { teleport: true } } });
  await wrapper.vm.submit(); expect(api.append).not.toHaveBeenCalled(); wrapper.unmount(); expect(api.discard).not.toHaveBeenCalled();
  wrapper = mount(Dialog, { props: { preview: preview(), api }, global: { stubs: { teleport: true } } }); wrapper.unmount(); expect(api.discard).toHaveBeenCalledWith('frozen');
});
