import { mount, flushPromises } from '@vue/test-utils';
import { describe, it, expect, vi, beforeEach } from 'vitest';
const api = vi.hoisted(() => ({ GetFaceClusterObservations: vi.fn(), PreviewExternally: vi.fn(), RevealImage: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./PreviewDrawer.vue', () => ({ default: { name: 'PreviewDrawer', props: ['initialEntity', 'startTimeMs'], template: '<aside />' } }));
import FaceClusterDetailDialog from './FaceClusterDetailDialog.vue';
import PreviewDrawer from './PreviewDrawer.vue';
import ImageSourceDialog from './ImageSourceDialog.vue';
const source = (id, kind = 'image', more = {}) => ({ observation_id: id, media_id: 10, media_kind: kind, name: `${id}.jpg`, path: '/photos/source.jpg', frame_ms: kind === 'video' ? 0 : -1, size: 100, ...more });
const cluster = { id: 1, status: 'unnamed' };
beforeEach(() => vi.resetAllMocks());
describe('FaceClusterDetailDialog', () => {
 it('paginates sources and opens the original image or video at frame zero', async () => {
  api.GetFaceClusterObservations.mockResolvedValueOnce({ observations: [source(1)], next_id: 1 }).mockResolvedValueOnce({ observations: [source(2, 'video')], next_id: 0 });
  const w = mount(FaceClusterDetailDialog, { props: { cluster }, attachTo: document.body });
  await flushPromises();
  await w.getComponent({ name: 'BaseModal' }).get('[data-test="face-source-more"]').trigger('click'); await flushPromises();
  expect(api.GetFaceClusterObservations).toHaveBeenLastCalledWith(1, 1, 30);
  expect(w.getComponent({ name: 'BaseModal' }).findAll('.face-source-dialog__row')).toHaveLength(2);
  await w.getComponent({ name: 'BaseModal' }).get('[data-test="face-source-open-1"]').trigger('click');
  expect(w.getComponent(ImageSourceDialog).props('image').id).toBe(10);
  w.getComponent(ImageSourceDialog).vm.$emit('close'); await flushPromises();
  await w.getComponent({ name: 'BaseModal' }).get('[data-test="face-source-open-2"]').trigger('click');
  expect(w.vm.videoSource).toMatchObject({ media_kind: 'video' });
  await flushPromises();
  expect(w.getComponent(PreviewDrawer).props()).toMatchObject({ initialEntity: { type: 'video', id: 10 }, startTimeMs: 0 });
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises();
  expect(w.findComponent(PreviewDrawer).exists()).toBe(false);
  expect(w.getComponent({ name: 'BaseModal' }).findAll('.face-source-dialog__row')).toHaveLength(2);
  expect(w.emitted('close')).toBeUndefined(); w.unmount();
 });
 it('shows unavailable sources without preview and forwards naming', async () => {
  api.GetFaceClusterObservations.mockResolvedValue({ observations: [source(1, 'image', { unavailable: '已删除' })] });
  const w = mount(FaceClusterDetailDialog, { props: { cluster }, attachTo: document.body }); await flushPromises();
  expect(w.getComponent({ name: 'BaseModal' }).text()).toContain('已删除'); expect(w.getComponent({ name: 'BaseModal' }).find('[data-test="face-source-open-1"]').exists()).toBe(false);
  await w.getComponent({ name: 'BaseModal' }).findAll('button').find(b => b.text() === '命名为新人物').trigger('click');
  expect(w.emitted('name')[0]).toEqual([cluster]); w.unmount();
 });
 it('discards a late response after switching clusters', async () => {
  let finish; api.GetFaceClusterObservations.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; })).mockResolvedValueOnce({ observations: [source(2)] });
  const w = mount(FaceClusterDetailDialog, { props: { cluster }, attachTo: document.body });
  await w.setProps({ cluster: { id: 2, status: 'unnamed' } }); await flushPromises();
  finish({ observations: [source(1)] }); await flushPromises();
  expect(w.vm.sources.map(s => s.observation_id)).toEqual([2]); w.unmount();
 });
 it('retries a failed page without discarding already loaded sources', async () => {
  api.GetFaceClusterObservations.mockResolvedValueOnce({ observations: [source(1)], next_id: 1 }).mockRejectedValueOnce('offline').mockResolvedValueOnce({ observations: [source(2)], next_id: 0 });
  const w = mount(FaceClusterDetailDialog, { props: { cluster }, attachTo: document.body }); await flushPromises();
  await w.getComponent({ name: 'BaseModal' }).get('[data-test="face-source-more"]').trigger('click'); await flushPromises();
  expect(w.vm.sources).toHaveLength(1); expect(w.getComponent({ name: 'BaseModal' }).text()).toContain('offline');
  await w.getComponent({ name: 'BaseModal' }).get('[data-test="face-source-more"]').trigger('click'); await flushPromises();
  expect(w.vm.sources).toHaveLength(2); w.unmount();
 });
});
