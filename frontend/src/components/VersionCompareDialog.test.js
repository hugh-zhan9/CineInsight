// 版本对比播放器（2026-10-12）：两侧经 GetPreviewSession 取源、按 GetVideoDetails 标注；并排 / 滑动对比切换不换元素；
// 一侧出声；共用播放与进度；右侧跟随纠偏、偏移、缓冲时停另一侧；Esc 与关闭释放两个媒体元素；单侧失败就地报错。
// jsdom 不会真的播放：play / pause / load 打桩，每个 <video> 的 currentTime、duration、paused 等用本地状态模拟。
import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  GetPreviewSession: vi.fn(), GetVideoDetails: vi.fn(), RequestPreviewProxy: vi.fn(), CancelPreviewProxy: vi.fn(), CreatePlaybackProxy: vi.fn()
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import VersionCompareDialog from './VersionCompareDialog.vue';
import { COMPARE_SYNC_INTERVAL_MS } from '../utils/versionCompare.js';

const inlineSession = (id, extra = {}) => ({ video_id: id, mode: 'inline', display_name: `v${id}.mp4`, inline_source: { locator_strategy: 'asset_route', locator_value: `/preview/media/${id}`, mime: 'video/mp4' }, ...extra });
const details = (id, extra = {}) => ({
  effective_title: `影片 ${id}`,
  video: { id, name: `v${id}.mp4`, size: 2 * 1024 * 1024 * 1024, width: 0, height: 0 },
  streams: [{ stream_type: 'video', codec_name: id === 1 ? 'h264' : 'hevc', width: id === 1 ? 1280 : 1920, height: id === 1 ? 720 : 1080 }],
  ...extra
});

// 每个媒体元素一份可读写的播放状态；play / pause 改 paused（浏览器里 play() 同步把 paused 置为 false）。
function mediaState(el) {
  if (!el.__media) {
    const state = { currentTime: 0, duration: 100, paused: true, ended: false, error: null };
    for (const key of Object.keys(state)) {
      Object.defineProperty(el, key, { configurable: true, get: () => state[key], set: value => { state[key] = value; } });
    }
    el.__media = state;
  }
  return el.__media;
}

let wrapper;
let documentEscape;
beforeEach(() => {
  vi.clearAllMocks();
  api.GetPreviewSession.mockImplementation(id => Promise.resolve(inlineSession(id)));
  api.GetVideoDetails.mockImplementation(id => Promise.resolve(details(id)));
  vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(function play() { mediaState(this).paused = false; return Promise.resolve(); });
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(function pause() { mediaState(this).paused = true; });
  vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {});
  // 代表下层弹窗（BaseModal）挂在 document 上的 Esc 监听：对比播放器开着时不该收到。
  documentEscape = vi.fn();
  document.addEventListener('keydown', documentEscape);
});
afterEach(() => {
  document.removeEventListener('keydown', documentEscape);
  wrapper?.unmount();
  wrapper = null;
  vi.useRealTimers();
  vi.restoreAllMocks();
});

// jsdom 没有 PointerEvent，test-utils 的 trigger 也写不进 clientX：用 MouseEvent 带上坐标直接派发。
async function pointer(wrapperEl, type, clientX) {
  wrapperEl.element.dispatchEvent(new MouseEvent(type, { clientX, button: 0, bubbles: true }));
  await wrapper.vm.$nextTick();
}

// 从页面里的元素按下 Esc：先经过 window 的捕获阶段，再到 document。
function pressEscape() {
  document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
}

async function openDialog(props = {}) {
  wrapper = mount(VersionCompareDialog, { props: { leftVideoId: 1, rightVideoId: 2, ...props } });
  await flushPromises();
  const left = wrapper.get('[data-test="version-compare-video-left"]').element;
  const right = wrapper.get('[data-test="version-compare-video-right"]').element;
  mediaState(left); mediaState(right);
  return { left, right };
}

describe('读取与标注', () => {
  it('两侧都走 GetPreviewSession 取预览地址，标注标题、分辨率、大小与编码', async () => {
    const { left, right } = await openDialog();
    expect(api.GetPreviewSession.mock.calls).toEqual([[1], [2]]);
    expect(api.GetVideoDetails.mock.calls).toEqual([[1], [2]]);
    expect(left.getAttribute('src')).toBe('/preview/media/1');
    expect(right.getAttribute('src')).toBe('/preview/media/2');
    expect(wrapper.get('[data-test="version-compare-label-left"]').text()).toContain('影片 1');
    expect(wrapper.get('[data-test="version-compare-facts-left"]').text()).toBe('1280×720 · 2.0 GB · h264');
    expect(wrapper.get('[data-test="version-compare-facts-right"]').text()).toBe('1920×1080 · 2.0 GB · hevc');
    expect(wrapper.get('[data-test="version-compare-play"]').attributes('disabled')).toBeUndefined();
  });

  it('经播放代理时在标注里说明', async () => {
    api.GetPreviewSession.mockImplementation(id => Promise.resolve(inlineSession(id, id === 2 ? { proxy: { strategy: 'transcode', size: 10 } } : {})));
    await openDialog();
    expect(wrapper.get('[data-test="version-compare-label-right"]').text()).toContain('不代表原片画质');
    expect(wrapper.get('[data-test="version-compare-label-left"]').text()).not.toContain('播放代理');
  });
});

describe('单侧失败', () => {
  it('读取播放源失败：该侧就地报错，另一侧照常播放', async () => {
    api.GetPreviewSession.mockImplementation(id => (id === 2 ? Promise.reject(new Error('boom')) : Promise.resolve(inlineSession(id))));
    const { left, right } = await openDialog();
    expect(wrapper.get('[data-test="version-compare-error-right"]').text()).toContain('读取播放源失败');
    expect(wrapper.find('[data-test="version-compare-error-left"]').exists()).toBe(false);
    expect(right.hasAttribute('src')).toBe(false);
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    expect(left.paused).toBe(false);
    expect(HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(1);
  });

  it('格式不能内嵌：说明原因并指向播放代理；详情读不到时标注用会话文件名', async () => {
    api.GetPreviewSession.mockImplementation(id => Promise.resolve(id === 1 ? { video_id: 1, mode: 'external-preview', display_name: 'v1.mkv', reason_code: 'inline_not_supported', reason_message: '当前文件格式不适合在应用内稳定预览。' } : inlineSession(id)));
    api.GetVideoDetails.mockImplementation(id => (id === 1 ? Promise.reject(new Error('x')) : Promise.resolve(details(id))));
    await openDialog();
    const error = wrapper.get('[data-test="version-compare-error-left"]').text();
    expect(error).toContain('当前文件格式不适合在应用内稳定预览。');
    expect(error).toContain('生成播放代理');
    expect(wrapper.get('[data-test="version-compare-label-left"]').text()).toContain('v1.mkv');
    // 左侧放不了，右侧单独主导：播放仍可用，声音也改由右侧出。
    expect(wrapper.vm.leaderSide).toBe('right');
    expect(wrapper.vm.followerSide).toBe('');
    expect(wrapper.get('[data-test="version-compare-video-right"]').element.muted).toBe(false);
  });

  it('内嵌解码报错：该侧显示错误，释放阶段的报错不算', async () => {
    const { right } = await openDialog();
    Object.defineProperty(right, 'error', { configurable: true, get: () => ({ code: 4 }) });
    await wrapper.get('[data-test="version-compare-video-right"]').trigger('error');
    expect(wrapper.get('[data-test="version-compare-error-right"]').text()).toContain('无法在应用内播放此文件');
    expect(wrapper.vm.followerSide).toBe('');
  });

  it('两侧都还没读完时不能播放', async () => {
    let resolve;
    api.GetPreviewSession.mockImplementation(id => (id === 2 ? new Promise(r => { resolve = r; }) : Promise.resolve(inlineSession(id))));
    wrapper = mount(VersionCompareDialog, { props: { leftVideoId: 1, rightVideoId: 2 } });
    await flushPromises();
    expect(wrapper.get('[data-test="version-compare-play"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="version-compare-label-right"]').text()).toContain('正在读取');
    resolve(inlineSession(2));
    await flushPromises();
    expect(wrapper.get('[data-test="version-compare-play"]').attributes('disabled')).toBeUndefined();
  });
});

describe('对比方式与声音', () => {
  it('并排与滑动对比切换只换布局，不换 <video> 元素；滑动时左侧画面按分界线裁切', async () => {
    const { left, right } = await openDialog();
    const stage = wrapper.get('[data-test="version-compare-stage"]');
    expect(stage.classes()).toContain('version-compare__stage--side');
    expect(wrapper.find('[data-test="version-compare-divider"]').exists()).toBe(false);
    await wrapper.get('[data-test="version-compare-mode-slider"]').trigger('click');
    expect(stage.classes()).toContain('version-compare__stage--slider');
    expect(wrapper.get('[data-test="version-compare-mode-slider"]').attributes('aria-pressed')).toBe('true');
    expect(wrapper.get('[data-test="version-compare-video-left"]').element).toBe(left);
    expect(wrapper.get('[data-test="version-compare-video-right"]').element).toBe(right);
    expect(wrapper.vm.clipPath).toBe('inset(0 50% 0 0)');

    // 拖动分界线：按舞台宽度换算百分比。
    stage.element.getBoundingClientRect = () => ({ left: 0, width: 1000, top: 0, height: 500 });
    await pointer(stage, 'pointerdown', 250);
    await pointer(stage, 'pointermove', 300);
    await pointer(stage, 'pointerup', 300);
    await pointer(stage, 'pointermove', 900);
    expect(wrapper.vm.sliderPercent).toBe(30);
    expect(wrapper.get('[data-test="version-compare-divider"]').attributes('style')).toContain('left: 30%');
    expect(wrapper.vm.clipPath).toBe('inset(0 70% 0 0)');
    expect(left.style.clipPath).toBe('inset(0 70% 0 0)');
    expect(right.style.clipPath).toBe('');
    await wrapper.get('[data-test="version-compare-divider"]').trigger('keydown', { key: 'ArrowRight' });
    expect(wrapper.vm.sliderPercent).toBe(32);

    await wrapper.get('[data-test="version-compare-mode-side"]').trigger('click');
    expect(stage.classes()).toContain('version-compare__stage--side');
    expect(wrapper.find('[data-test="version-compare-divider"]').exists()).toBe(false);
    expect(left.style.clipPath).toBe('');
    // 并排时按下舞台不动分界线。
    await pointer(stage, 'pointerdown', 900);
    expect(wrapper.vm.sliderPercent).toBe(32);
    expect(wrapper.get('[data-test="version-compare-video-left"]').element).toBe(left);
  });

  it('同一时刻只有一侧出声，切换后另一侧静音', async () => {
    const { left, right } = await openDialog();
    expect(left.muted).toBe(false);
    expect(right.muted).toBe(true);
    expect(wrapper.get('[data-test="version-compare-audio-left"]').attributes('aria-pressed')).toBe('true');
    await wrapper.get('[data-test="version-compare-audio-right"]').trigger('click');
    expect(left.muted).toBe(true);
    expect(right.muted).toBe(false);
    expect(wrapper.get('[data-test="version-compare-audio-right"]').attributes('aria-pressed')).toBe('true');
    await wrapper.get('[data-test="version-compare-audio-left"]').trigger('click');
    expect(left.muted).toBe(false);
    expect(right.muted).toBe(true);
  });
});

describe('同步播放', () => {
  it('一个播放键同时起播与暂停两侧；进度条拖动两侧，右侧按偏移落位', async () => {
    const { left, right } = await openDialog();
    await wrapper.get('[data-test="version-compare-video-left"]').trigger('loadedmetadata');
    expect(wrapper.get('[data-test="version-compare-time"]').text()).toBe('00:00 / 01:40');
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    expect(left.paused).toBe(false);
    expect(right.paused).toBe(false);
    expect(wrapper.get('[data-test="version-compare-play"]').text()).toBe('暂停');
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    expect(left.paused).toBe(true);
    expect(right.paused).toBe(true);

    await wrapper.get('[data-test="version-compare-offset-plus-1"]').trigger('click');
    await wrapper.get('[data-test="version-compare-seek"]').setValue('40');
    expect(left.currentTime).toBe(40);
    expect(right.currentTime).toBe(41);
    expect(wrapper.get('[data-test="version-compare-time"]').text()).toBe('00:40 / 01:40');
  });

  it('偏差超过阈值才把右侧拖回去；任一侧在拖动或缓冲时不纠偏', async () => {
    const { left, right } = await openDialog();
    left.currentTime = 10; right.currentTime = 10.1;
    wrapper.vm.syncTick();
    expect(right.currentTime).toBe(10.1);
    right.currentTime = 10.4;
    await wrapper.get('[data-test="version-compare-video-left"]').trigger('seeking');
    wrapper.vm.syncTick();
    expect(right.currentTime).toBe(10.4);
    await wrapper.get('[data-test="version-compare-video-left"]').trigger('seeked');
    await wrapper.get('[data-test="version-compare-video-right"]').trigger('waiting');
    wrapper.vm.syncTick();
    expect(right.currentTime).toBe(10.4);
    await wrapper.get('[data-test="version-compare-video-right"]').trigger('canplay');
    wrapper.vm.syncTick();
    expect(right.currentTime).toBe(10);
  });

  it('定时器按间隔纠偏，左侧在放而右侧停着时把右侧拉起', async () => {
    vi.useFakeTimers();
    wrapper = mount(VersionCompareDialog, { props: { leftVideoId: 1, rightVideoId: 2 } });
    await flushPromises();
    const left = wrapper.get('[data-test="version-compare-video-left"]').element;
    const right = wrapper.get('[data-test="version-compare-video-right"]').element;
    mediaState(left); mediaState(right);
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    right.pause();
    left.currentTime = 20; right.currentTime = 5;
    vi.advanceTimersByTime(COMPARE_SYNC_INTERVAL_MS - 1);
    expect(right.currentTime).toBe(5);
    vi.advanceTimersByTime(1);
    expect(right.currentTime).toBe(20);
    expect(right.paused).toBe(false);
  });

  it('一侧缓冲时暂停另一侧，缓冲结束后两侧接着放', async () => {
    const { left, right } = await openDialog();
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    await wrapper.get('[data-test="version-compare-video-right"]').trigger('waiting');
    expect(left.paused).toBe(true);
    // 缓冲期间左侧收到的 pause 不算用户暂停。
    await wrapper.get('[data-test="version-compare-video-left"]').trigger('pause');
    expect(wrapper.vm.playing).toBe(true);
    await wrapper.get('[data-test="version-compare-video-right"]').trigger('playing');
    expect(left.paused).toBe(false);
    expect(right.paused).toBe(false);

    await wrapper.get('[data-test="version-compare-video-left"]').trigger('waiting');
    expect(right.paused).toBe(true);
    await wrapper.get('[data-test="version-compare-video-left"]').trigger('playing');
    expect(right.paused).toBe(false);
  });

  it('左侧被别处暂停或播完：播放意图跟着停，右侧也停', async () => {
    const { left, right } = await openDialog();
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    left.pause();
    await wrapper.get('[data-test="version-compare-video-left"]').trigger('pause');
    expect(wrapper.vm.playing).toBe(false);
    expect(right.paused).toBe(true);

    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    left.ended = true;
    await wrapper.get('[data-test="version-compare-video-left"]').trigger('ended');
    expect(wrapper.vm.playing).toBe(false);
    expect(right.paused).toBe(true);
  });

  it('负偏移：左侧还在右侧片头之前时右侧停在 0 等', async () => {
    const { left, right } = await openDialog();
    await wrapper.get('[data-test="version-compare-offset-minus-1"]').trigger('click');
    await wrapper.get('[data-test="version-compare-offset-minus-1"]').trigger('click');
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    expect(left.paused).toBe(false);
    expect(right.paused).toBe(true);
    expect(right.currentTime).toBe(0);
    left.currentTime = 2.5;
    wrapper.vm.syncTick();
    expect(right.currentTime).toBe(0.5);
    expect(right.paused).toBe(false);
  });

  it('偏移微调立即生效（小于纠偏阈值也拖），归零回到同位置；只在本次打开内有效', async () => {
    const { left, right } = await openDialog();
    left.currentTime = 30; right.currentTime = 30;
    await wrapper.get('[data-test="version-compare-offset-plus-01"]').trigger('click');
    expect(right.currentTime).toBe(30.1);
    expect(wrapper.get('[data-test="version-compare-offset"]').text()).toBe('+0.1 秒');
    await wrapper.get('[data-test="version-compare-offset-plus-1"]').trigger('click');
    expect(right.currentTime).toBe(31.1);
    await wrapper.get('[data-test="version-compare-offset-minus-01"]').trigger('click');
    expect(wrapper.get('[data-test="version-compare-offset"]').text()).toBe('+1.0 秒');
    await wrapper.get('[data-test="version-compare-offset-reset"]').trigger('click');
    expect(right.currentTime).toBe(30);
    expect(wrapper.get('[data-test="version-compare-offset"]').text()).toBe('0.0 秒');
    expect(wrapper.get('[data-test="version-compare-offset-reset"]').attributes('disabled')).toBeDefined();

    await wrapper.get('[data-test="version-compare-offset-plus-1"]').trigger('click');
    wrapper.unmount();
    wrapper = null;
    await openDialog();
    expect(wrapper.vm.offset).toBe(0);
  });

  it('起播被拒（非打断）时停下并说明', async () => {
    await openDialog();
    HTMLMediaElement.prototype.play.mockImplementationOnce(() => Promise.reject(new DOMException('not allowed', 'NotAllowedError')));
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.playing).toBe(false);
    expect(wrapper.get('[data-test="version-compare-transport-error"]').text()).toContain('无法开始播放');
  });
});

describe('关闭与释放', () => {
  it('Esc 只关对比播放器：两侧暂停并清掉 src，下层弹窗收不到这次 Esc', async () => {
    const { left, right } = await openDialog();
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    const loadCalls = HTMLMediaElement.prototype.load.mock.calls.length;
    pressEscape();
    expect(wrapper.emitted('close')).toHaveLength(1);
    expect(documentEscape).not.toHaveBeenCalled();
    expect(left.paused).toBe(true);
    expect(right.paused).toBe(true);
    expect(left.hasAttribute('src')).toBe(false);
    expect(right.hasAttribute('src')).toBe(false);
    expect(HTMLMediaElement.prototype.load.mock.calls.length).toBe(loadCalls + 2);
    // 释放后重渲染不会把 src 写回来，定时纠偏也停了。
    await wrapper.vm.$nextTick();
    expect(left.hasAttribute('src')).toBe(false);
    right.currentTime = 50;
    wrapper.vm.syncTick();
    expect(right.currentTime).toBe(50);
  });

  it('关闭按钮同样释放；宿主直接卸载时也释放；释放时的 error 事件不算报错', async () => {
    const first = await openDialog();
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    Object.defineProperty(first.right, 'error', { configurable: true, get: () => ({ code: 4 }) });
    await wrapper.get('[data-test="version-compare-close"]').trigger('click');
    await wrapper.get('[data-test="version-compare-video-right"]').trigger('error');
    expect(wrapper.find('[data-test="version-compare-error-right"]').exists()).toBe(false);
    expect(wrapper.emitted('close')).toHaveLength(1);
    expect(first.left.hasAttribute('src')).toBe(false);
    wrapper.unmount();
    wrapper = null;

    const second = await openDialog();
    await wrapper.get('[data-test="version-compare-play"]').trigger('click');
    wrapper.unmount();
    wrapper = null;
    expect(second.left.paused).toBe(true);
    expect(second.right.paused).toBe(true);
    expect(second.left.hasAttribute('src')).toBe(false);
    expect(second.right.hasAttribute('src')).toBe(false);
    // 卸载后 Esc 不再由它处理。
    pressEscape();
    expect(documentEscape).toHaveBeenCalledTimes(1);
  });

  it('关闭后才回来的读取结果不再写回', async () => {
    let resolve;
    api.GetPreviewSession.mockImplementation(id => (id === 2 ? new Promise(r => { resolve = r; }) : Promise.resolve(inlineSession(id))));
    wrapper = mount(VersionCompareDialog, { props: { leftVideoId: 1, rightVideoId: 2 } });
    await flushPromises();
    await wrapper.get('[data-test="version-compare-close"]').trigger('click');
    resolve(inlineSession(2));
    await flushPromises();
    expect(wrapper.get('[data-test="version-compare-video-right"]').element.hasAttribute('src')).toBe(false);
  });
});

describe('版本对比：不能直放的一侧自动请求播放代理（2026-10-13）', () => {
  const queued = id => ({ running: true, queued: 1, queued_video_ids: [id], current_video_id: 99, results: [] });
  let handlers;
  beforeEach(() => {
    handlers = {};
    window.runtime = { EventsOn: vi.fn((name, fn) => { (handlers[name] ||= []).push(fn); return () => { handlers[name] = handlers[name].filter(item => item !== fn); }; }) };
    api.RequestPreviewProxy.mockImplementation(id => Promise.resolve(queued(id)));
    api.CancelPreviewProxy.mockResolvedValue(true);
  });
  afterEach(() => { delete window.runtime; });
  const emit = status => (handlers['playback-proxy-state'] || []).forEach(fn => fn(status));

  it('proxy-preparing 的一侧按自己的 owner 请求，显示进度，就绪后换成内嵌；关闭时撤销另一侧还在等的', async () => {
    const preparing = id => ({ video_id: id, mode: 'proxy-preparing', reason_message: '需要先生成播放代理。' });
    api.GetPreviewSession.mockImplementation(id => Promise.resolve(preparing(id)));
    await openDialog();
    const owners = Object.fromEntries(api.RequestPreviewProxy.mock.calls.map(([id, owner]) => [id, owner]));
    expect(owners[1]).toMatch(/^compare-left-/);
    expect(owners[2]).toMatch(/^compare-right-/);
    expect(wrapper.get('[data-test="version-compare-proxy-status-left"]').text()).toBe('排队中（第 1 个）');
    expect(wrapper.find('[data-test="version-compare-error-left"]').exists()).toBe(false);
    api.GetPreviewSession.mockImplementation(id => Promise.resolve(id === 1 ? { ...inlineSession(1), proxy: { strategy: 'remux', size: 9 } } : preparing(id)));
    emit({ running: true, queued: 1, queued_video_ids: [2], current_video_id: 0, results: [{ video_id: 1, code: 'created' }] });
    await flushPromises();
    expect(wrapper.get('[data-test="version-compare-video-left"]').attributes('src')).toBe('/preview/media/1');
    await wrapper.get('[data-test="version-compare-close"]').trigger('click');
    await flushPromises();
    expect(api.CancelPreviewProxy.mock.calls).toEqual([[2, owners[2]]]);
  });

  it('proxy-manual 的一侧：报原因、不自动请求，手动「生成播放代理」走 CreatePlaybackProxy', async () => {
    api.GetPreviewSession.mockImplementation(id => Promise.resolve(id === 2
      ? { video_id: 2, mode: 'proxy-manual', reason_message: '文件较大（约 12.0 GB），生成播放代理会占用相同大小的空间' }
      : inlineSession(id)));
    api.CreatePlaybackProxy.mockResolvedValueOnce(queued(2));
    await openDialog();
    expect(api.RequestPreviewProxy).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="version-compare-error-right"]').text()).toContain('文件较大（约 12.0 GB）');
    await wrapper.get('[data-test="version-compare-proxy-generate-right"]').trigger('click');
    await flushPromises();
    expect(api.CreatePlaybackProxy).toHaveBeenCalledWith(2);
    expect(wrapper.get('[data-test="version-compare-proxy-status-right"]').text()).toBe('排队中（第 1 个）');
    expect(wrapper.find('[data-test="version-compare-proxy-generate-right"]').exists()).toBe(false);
  });
});
