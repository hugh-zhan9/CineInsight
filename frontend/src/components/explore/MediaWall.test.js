import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, expect, it, vi } from 'vitest';
import MediaWall from './MediaWall.vue';
import RandomGallery from './RandomGallery.vue';
import MediaTile from './MediaTile.vue';

const mounted = [];
afterEach(() => { for (const wrapper of mounted.splice(0)) wrapper.unmount(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
function offscreenGeometry() {
  vi.stubGlobal('ResizeObserver', class { constructor(fn) { this.fn = fn; } observe() { this.fn(); } disconnect() {} });
  vi.stubGlobal('IntersectionObserver', class { constructor(fn) { this.fn = fn; } observe(target) { this.fn([{ target, intersectionRatio: 1 }]); } disconnect() {} });
  vi.stubGlobal('requestAnimationFrame', fn => { fn(); return 0; }); vi.stubGlobal('cancelAnimationFrame', vi.fn());
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function () {
    const top = this.matches?.('.media-wall__viewport, .random-gallery, [data-entry-id]') ? 1200 : 0;
    return { x: 0, y: top, left: 0, top, right: 160, bottom: top + 160, width: 160, height: 160 };
  });
}
const entry = { entry_id: 1, media_id: 1, kind: 'image', slot: 0, presented: false };

it('does not count a wall clipped outside the actual application viewport', async () => {
  offscreenGeometry();
  const wrapper = mount(MediaWall, { props: { entries: [entry], loader: {}, view: { scale: 1, x: 0, y: 0 } }, global: { stubs: { MediaTile: true } } }); mounted.push(wrapper);
  wrapper.vm.setLoaded(1, true); wrapper.vm.reportExposure();
  expect(wrapper.emitted('exposure').at(-1)[0][0].fraction).toBe(0);
});

it('requests on-screen cells before overscan without changing their slots', () => {
  offscreenGeometry();
  const entries = Array.from({ length: 1024 }, (_, slot) => ({ ...entry, entry_id: slot + 1, slot }));
  const wrapper = mount(MediaWall, { props: { entries, loader: {}, view: { scale: 1, x: -324, y: -324 } }, global: { stubs: { MediaTile: true } } }); mounted.push(wrapper);
  const ordered = wrapper.vm.visibleEntries;
  expect(ordered[0].slot).toBe(66);
  const firstOverscan = ordered.findIndex(item => !wrapper.vm.visibleIDs.has(item.entry_id));
  expect(firstOverscan).toBeGreaterThan(0);
  expect(ordered.slice(firstOverscan).every(item => !wrapper.vm.visibleIDs.has(item.entry_id))).toBe(true);
  expect(entries[0].slot).toBe(0);
});

it('updates an existing tile request priority without fetching the image again', async () => {
  const setPriority = vi.fn();
  const load = vi.fn(() => ({ promise: new Promise(() => {}), cancel: vi.fn(), setPriority }));
  const wrapper = mount(MediaTile, { props: { entry, card: { thumbnail_url: '/fixture?x=1' }, loader: { load }, priority: 0 } }); mounted.push(wrapper);
  expect(load).toHaveBeenCalledWith('/fixture?x=1&edge=96', 0);
  await wrapper.setProps({ priority: 1 });
  expect(setPriority).toHaveBeenCalledWith(1);
  expect(load).toHaveBeenCalledTimes(1);
});

it('does not count an offscreen gallery even when its own observer root is full', async () => {
  vi.useFakeTimers(); offscreenGeometry();
  const wrapper = mount(RandomGallery, { props: { entries: [entry], loader: {} }, global: { stubs: { MediaTile: { props: ['entry'], template: '<button :data-entry-id="entry.entry_id" />' } } } }); mounted.push(wrapper);
  await wrapper.vm.$nextTick(); wrapper.vm.setLoaded(1, true); await vi.advanceTimersByTimeAsync(100);
  expect(wrapper.emitted('exposure').at(-1)[0][0].fraction).toBe(0);
});

it('removes a loaded image when its card becomes unavailable', async () => {
  const revoke = vi.fn(); vi.stubGlobal('URL', { createObjectURL: vi.fn(() => 'blob:fixture'), revokeObjectURL: revoke });
  const loader = { load: () => ({ promise: Promise.resolve(new Blob(['fixture'])), cancel: vi.fn() }) };
  const wrapper = mount(MediaTile, { props: { entry, card: { title: 'photo', thumbnail_url: '/fixture?x=1' }, loader } }); mounted.push(wrapper); await flushPromises();
  expect(wrapper.find('img').exists()).toBe(true);
  await wrapper.setProps({ card: { title: '', thumbnail_url: '', unavailable_reason: '已移出可见范围' } }); await flushPromises();
  expect(wrapper.find('img').exists()).toBe(false); expect(revoke).toHaveBeenCalledWith('blob:fixture');
});

it('retries a failed tile only after an explicit retry signal', async () => {
  vi.stubGlobal('URL', { createObjectURL: vi.fn(() => 'blob:retried'), revokeObjectURL: vi.fn() });
  const load = vi.fn().mockImplementationOnce(() => ({ promise: Promise.reject(new Error('disk unavailable')), cancel: vi.fn() })).mockImplementation(() => ({ promise: Promise.resolve(new Blob(['fixture'])), cancel: vi.fn() }));
  const wrapper = mount(MediaTile, { props: { entry, card: { title: 'photo', thumbnail_url: '/fixture?x=1' }, loader: { load } } }); mounted.push(wrapper); await flushPromises();
  expect(load).toHaveBeenCalledTimes(1); expect(wrapper.emitted('failed').at(-1)).toEqual([1, true]);
  await wrapper.setProps({ retrySignal: 1 }); await flushPromises();
  expect(load).toHaveBeenCalledTimes(2); expect(wrapper.find('img').exists()).toBe(true);
});
