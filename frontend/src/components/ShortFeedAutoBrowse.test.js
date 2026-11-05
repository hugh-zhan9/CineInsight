import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ShortFeedApp from '../short-feed/ShortFeedApp.vue';
import FeedStage from '../short-feed/components/FeedStage.vue';
enableAutoUnmount(afterEach);
const item = (kind, id) => ({ media_kind: kind, id, name: `${kind}-${id}`, media_url: `/short-media/${kind}/${id}`, tags: [], duration: 20, width: 800, height: 600 });
let queue;
beforeEach(() => {
  queue = [item('image', 1), item('video', 2), item('image', 3)];
  vi.stubGlobal('fetch', vi.fn(async url => {
    let value = {};
    if (String(url).includes('/feed/next')) { value = queue.shift(); if (!value) return { ok: false, status: 404, json: async () => ({ code: 'no_eligible_videos' }) }; }
    return { ok: true, status: 200, json: async () => value, headers: { get: () => 'application/json' } };
  }));
  vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue();
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {});
  vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {} });
});
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
async function app() { const w = mount(ShortFeedApp); await flushPromises(); vi.useFakeTimers(); return w; }
async function ready(w, kind, id) { await w.get(`[src="/short-media/${kind}/${id}"]`).trigger(kind === 'image' ? 'load' : 'canplay'); await flushPromises(); }
async function tick(ms) { await vi.advanceTimersByTimeAsync(ms); await flushPromises(); }
describe('automatic mobile browsing', () => {
  it('counts five seconds only after the current image is decoded and reuses the next player', async () => {
    const w = await app(); await tick(6000); expect(w.vm.currentVideo.id).toBe(1);
    await w.get('[data-test=feed-auto-toggle]').trigger('click');
    await ready(w, 'video', 2); await tick(6000); expect(w.vm.currentVideo.id).toBe(1);
    await ready(w, 'image', 1); await tick(4900); expect(w.vm.currentVideo.id).toBe(1);
    const prepared = w.get('video').element;
    await tick(100); expect(w.vm.currentVideo.id).toBe(2); expect(w.vm.player()).toBe(prepared); expect(prepared.loop).toBe(false);
    expect(w.findAll('video, img.feed-photo').length).toBeLessThanOrEqual(2);
  });
  it('retains the current image until next media is ready and advances on video ended', async () => {
    const w = await app(); await ready(w, 'image', 1); await w.get('[data-test=feed-auto-toggle]').trigger('click'); await tick(5100);
    expect(w.vm.currentVideo.id).toBe(1); expect(w.text()).toContain('正在准备下一项');
    await ready(w, 'video', 2); await tick(100); expect(w.vm.currentVideo.id).toBe(2);
    await w.get('video').trigger('canplaythrough'); await ready(w, 'image', 3); await w.get('video').trigger('ended'); await flushPromises(); expect(w.vm.currentVideo.id).toBe(3);
  });
  it('pauses for a sheet and explicit pause', async () => {
    const w = await app(); await ready(w, 'image', 1); await ready(w, 'video', 2);
    await w.get('[data-test=feed-auto-toggle]').trigger('click'); await tick(2000);
    w.vm.sheet = 'more'; await tick(6000); expect(w.vm.currentVideo.id).toBe(1);
    w.vm.sheet = null; await flushPromises(); await w.get('[data-test=feed-auto-pause]').trigger('click');
    await tick(6000); expect(w.vm.currentVideo.id).toBe(1);
    await w.get('[data-test=feed-auto-pause]').trigger('click'); await tick(3000); expect(w.vm.currentVideo.id).toBe(2);
  });
  it('stops on preload failure and retries without replacing the current picture', async () => {
    const w = await app(); await ready(w, 'image', 1); await w.get('[data-test=feed-auto-toggle]').trigger('click');
    await w.get('video').trigger('error'); await tick(5000);
    expect(w.vm.currentVideo.id).toBe(1); expect(w.vm.autoPaused).toBe(true); expect(w.text()).toContain('加载失败');
    await w.get('[data-test=feed-auto-retry]').trigger('click'); await ready(w, 'video', 2); await tick(100); expect(w.vm.currentVideo.id).toBe(2);
  });
  it('does not let an old prefetch enter a newly selected scope', async () => {
    const originalFetch = globalThis.fetch;
    let complete, oldSignal, number = 0;
    globalThis.fetch = vi.fn((url, options) => {
      if (String(url).includes('/feed/next') && ++number === 2) { oldSignal = options.signal; return new Promise(resolve => { complete = resolve; }); }
      return originalFetch(url, options);
    });
    const w = await app();
    await w.vm.applyScope('favorites'); await flushPromises();
    const current = w.vm.currentVideo.id;
    expect(oldSignal.aborted).toBe(true);
    complete({ ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => item('image', 99) }); await flushPromises();
    expect(w.vm.currentVideo.id).toBe(current); expect(w.vm.stagePrefetched?.id).not.toBe(99);
  });
  it('waits for image decode, ignores a released decode, and never prepares more than the next item', async () => {
    const a = item('image', 1), b = item('image', 2);
    const w = mount(FeedStage, { props: { item: a, prefetched: b, prefetchPreload: 'auto' } });
    let complete;
    w.get(`[src="${b.media_url}"]`).element.decode = () => new Promise(resolve => { complete = resolve; });
    await w.get(`[src="${b.media_url}"]`).trigger('load');
    expect(w.vm.state(b)).toBe('loading');
    await w.setProps({ prefetched: null }); complete(); await flushPromises();
    expect(w.emitted('prepared')).toBeUndefined(); expect(w.findAll('.feed-media-slot')).toHaveLength(1);
  });
  it('requires resume after backgrounding', async () => {
    const w = await app(); await ready(w, 'image', 1); await ready(w, 'video', 2); await w.get('[data-test=feed-auto-toggle]').trigger('click');
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
    document.dispatchEvent(new Event('visibilitychange')); await tick(6000);
    hidden.mockReturnValue(false); document.dispatchEvent(new Event('visibilitychange')); await tick(6000);
    expect(w.vm.currentVideo.id).toBe(1); expect(w.vm.autoPaused).toBe(true);
  });
});
describe('bounded media reuse', () => {
  it('reports a failed prefetched image when manually promoted', async () => {
    const a = item('image', 1), b = item('image', 2);
    const w = mount(FeedStage, { props: { item: a, prefetched: b, prefetchPreload: 'auto' } });
    await w.get(`[src="${b.media_url}"]`).trigger('error');
    expect(w.emitted('media-error')).toBeUndefined();
    await w.setProps({ item: b, prefetched: null }); await flushPromises();
    expect(w.emitted('media-error')).toHaveLength(1);
  });
  it.each([['image','image'],['image','video'],['video','image'],['video','video']])('reuses the prefetched element for %s to %s', async (from, to) => {
    const a = item(from, 1), b = item(to, 2), c = item('image', 3);
    const w = mount(FeedStage, { props: { item: a, prefetched: b, prefetchPreload: 'auto' } }); const next = w.get(`[src="${b.media_url}"]`).element;
    await w.setProps({ item: b, prefetched: c }); expect(w.get(`[src="${b.media_url}"]`).element).toBe(next);
    expect(w.find(`[src="${a.media_url}"]`).exists()).toBe(false); expect(w.findAll('.feed-media-slot')).toHaveLength(2);
  });
});
