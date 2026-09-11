import { mount, flushPromises } from '@vue/test-utils';
import { describe, it, expect, vi } from 'vitest';
const api = vi.hoisted(() => ({ RevealImage: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import ImageSourceDialog from './ImageSourceDialog.vue';
import { confirmAction, feedbackState } from '../utils/feedback.js';
describe('ImageSourceDialog', () => {
 it('lets a pending confirmation own Escape without closing the image', async () => {
  const w = mount(ImageSourceDialog, { props: { image: { id: 1 } } });
  const answer = confirmAction({ message: '解除最后一条关联？' });
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
  expect(await answer).toBe(false); expect(feedbackState.confirm).toBeNull();
  expect(w.emitted('close')).toBeUndefined(); w.unmount();
 });

 it('shows the original, reveals its file and offers unlink without deleting', async () => {
  const image = { id: 5, name: 'a.jpg', path: '/a.jpg' };
  const w = mount(ImageSourceDialog, { props: { image, allowUnlink: true }, global: { stubs: { teleport: true } } });
  expect(w.get('img').attributes('src')).toBe('/preview/image/5');
  api.RevealImage.mockRejectedValueOnce('disk offline');
  await w.get('[data-test="image-source-directory"]').trigger('click'); await flushPromises();
  expect(api.RevealImage).toHaveBeenCalledWith(5); expect(w.text()).toContain('disk offline');
  await w.get('[data-test="image-source-unlink"]').trigger('click');
  expect(w.emitted('unlink')[0]).toEqual([image]);
  await w.get('img').trigger('error'); expect(w.text()).toContain('无法加载原图');
  await w.setProps({ image: { id: 6, name: 'b.jpg' } }); expect(w.get('img').attributes('src')).toBe('/preview/image/6'); w.unmount();
 });
});
