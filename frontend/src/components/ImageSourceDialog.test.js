import { mount, flushPromises } from '@vue/test-utils';
import { describe, it, expect, vi } from 'vitest';
const api = vi.hoisted(() => ({ RevealImage: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import ImageSourceDialog from './ImageSourceDialog.vue';
import AppFeedback from './AppFeedback.vue';
import { confirmAction, feedbackState } from '../utils/feedback.js';
describe('ImageSourceDialog', () => {
 // Esc 只交给最上面一层（DialogShell / BaseModal 的栈）：叠在原图上的确认框先收到 Esc，原图不关；
 // 确认框答完以后，再按 Esc 才关原图。
 it('lets a pending confirmation own Escape without closing the image', async () => {
  const w = mount(ImageSourceDialog, { props: { image: { id: 1 } }, attachTo: document.body });
  const host = mount(AppFeedback, { attachTo: document.body });
  const answer = confirmAction({ message: '解除最后一条关联？' });
  await flushPromises();
  expect(document.querySelector('[data-test="app-confirm"]')).not.toBeNull();
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
  expect(await answer).toBe(false); expect(feedbackState.confirm).toBeNull();
  expect(w.emitted('close')).toBeUndefined();
  await flushPromises();
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
  expect(w.emitted('close')).toHaveLength(1);
  host.unmount(); w.unmount();
 });

 it('shows the original, reveals its file and offers unlink without deleting', async () => {
  const image = { id: 5, name: 'a.jpg', path: '/a.jpg' };
  const w = mount(ImageSourceDialog, { props: { image, allowUnlink: true }, global: { stubs: { teleport: true } } });
  expect(w.get('h2').text()).toBe('a.jpg');
  expect(w.get('[data-test="image-source-directory"]').text()).toBe('在访达中显示');
  expect(w.get('img').attributes('src')).toBe('/preview/image/5');
  api.RevealImage.mockRejectedValueOnce('disk offline');
  await w.get('[data-test="image-source-directory"]').trigger('click'); await flushPromises();
  expect(api.RevealImage).toHaveBeenCalledWith(5); expect(w.text()).toContain('disk offline');
  await w.get('[data-test="image-source-unlink"]').trigger('click');
  expect(w.emitted('unlink')[0]).toEqual([image]);
  expect(w.find('[data-test="image-source-delete"]').exists()).toBe(false);
  await w.get('img').trigger('error'); expect(w.text()).toContain('无法加载原图');
  await w.setProps({ image: { id: 6, name: 'b.jpg' } }); expect(w.get('img').attributes('src')).toBe('/preview/image/6'); w.unmount();
 });

 it('closes from the top-right close button', async () => {
  const w = mount(ImageSourceDialog, { props: { image: { id: 7, name: 'c.jpg' } }, global: { stubs: { teleport: true } } });
  await w.get('[data-test="dialog-shell-close"]').trigger('click');
  expect(w.emitted('close')).toHaveLength(1); w.unmount();
 });
});
