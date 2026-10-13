import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  CancelPreviewProxy: vi.fn(),
  CreatePlaybackProxy: vi.fn(),
  DeletePlaybackProxy: vi.fn(),
  GetPlaybackProxy: vi.fn(),
  GetPlaybackProxyStatus: vi.fn(),
  GetPreviewSession: vi.fn(),
  RecordViewEvent: vi.fn(),
  RequestPreviewProxy: vi.fn()
}));
vi.mock('../../../wailsjs/go/main/App', () => api);
vi.mock('../../utils/feedback.js', () => ({ confirmAction: vi.fn(() => Promise.resolve(true)) }));

import InlineVideoPlayer from './InlineVideoPlayer.vue';
import { currentActivePlayer, releaseActivePlayer } from '../../utils/activePlayer.js';

const inlineSession = (id, extra = {}) => ({ video_id: id, mode: 'inline', inline_source: { locator_value: `/preview/video/${id}`, mime: 'video/mp4' }, ...extra });
const preparingSession = id => ({ video_id: id, mode: 'proxy-preparing', reason_code: 'proxy_required', reason_message: '需要先生成播放代理。', external_action: { button_label: '使用系统播放器预览' } });
const idleStatus = (extra = {}) => ({ running: false, queued: 0, queued_video_ids: [], current_video_id: 0, results: [], ...extra });

const mounted = [];
async function mountPlayer(props = {}) {
  const wrapper = mount(InlineVideoPlayer, { props: { videoId: 1, session: inlineSession(1), ...props } });
  mounted.push(wrapper);
  await flushPromises();
  return wrapper;
}

// jsdom 的 <video> 不会真的播放：把播放器状态接到一个可改的对象上。
function fakePlayer(element, { duration = Number.NaN } = {}) {
  const state = { currentTime: 0, duration, paused: true, seeking: false, playbackRate: 1, readyState: 4 };
  for (const key of Object.keys(state)) {
    Object.defineProperty(element, key, { configurable: true, get: () => state[key], set: value => { state[key] = value; } });
  }
  element.play = vi.fn(() => { state.paused = false; return Promise.resolve(); });
  element.pause = vi.fn(() => { state.paused = true; });
  return state;
}

// 以 250ms 一次的 timeupdate 往前播 seconds 秒。clock 是 Date.now 的假时钟。
async function playThrough(video, player, clock, seconds) {
  const from = player.currentTime;
  const steps = Math.round(seconds / 0.25);
  for (let step = 1; step <= steps; step += 1) {
    player.currentTime = from + step * 0.25;
    clock.now += 250;
    await video.trigger('timeupdate');
  }
}

function installRuntime() {
  const handlers = {};
  window.runtime = { EventsOn: vi.fn((name, fn) => { handlers[name] = fn; return () => { delete handlers[name]; }; }) };
  return handlers;
}

beforeEach(() => {
  vi.clearAllMocks();
  api.GetPlaybackProxy.mockResolvedValue(null);
  api.GetPlaybackProxyStatus.mockResolvedValue({ running: false, queued: 0, queued_video_ids: [], current_video_id: 0, results: [] });
  api.RecordViewEvent.mockResolvedValue(true);
  api.RequestPreviewProxy.mockResolvedValue({ running: false, queued: 0, queued_video_ids: [], current_video_id: 0, results: [] });
  api.CancelPreviewProxy.mockResolvedValue(true);
});

afterEach(() => {
  while (mounted.length) mounted.pop().unmount();
  releaseActivePlayer(currentActivePlayer());
  delete window.runtime;
  vi.restoreAllMocks();
});

describe('InlineVideoPlayer 会话（D-UI-11）', () => {
  it('受控：用宿主给的会话，不自己取；null 时显示准备中', async () => {
    const waiting = await mountPlayer({ session: null });
    expect(waiting.text()).toContain('正在准备预览...');
    const wrapper = await mountPlayer();
    expect(wrapper.get('.preview-drawer__video source').attributes('src')).toBe('/preview/video/1');
    expect(api.GetPreviewSession).not.toHaveBeenCalled();
  });

  it('不传 session：按 videoId 自己取会话，换片时重取', async () => {
    api.GetPreviewSession.mockImplementation(id => Promise.resolve(inlineSession(id)));
    const wrapper = await mountPlayer({ session: undefined });
    expect(api.GetPreviewSession).toHaveBeenCalledWith(1);
    expect(wrapper.get('source').attributes('src')).toBe('/preview/video/1');
    await wrapper.setProps({ videoId: 2 });
    await flushPromises();
    expect(api.GetPreviewSession).toHaveBeenLastCalledWith(2);
    expect(wrapper.get('source').attributes('src')).toBe('/preview/video/2');
  });

  it('自己取会话失败：显示不带原始错误的说明', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    api.GetPreviewSession.mockRejectedValue(new Error('open /Volumes/Movies/a.mkv: permission denied'));
    const wrapper = await mountPlayer({ session: undefined });
    expect(wrapper.text()).toContain('准备预览失败');
    expect(wrapper.text()).not.toContain('/Volumes');
  });

  it('外部预览会话：给出系统播放器入口，点了发 preview-externally(videoID)', async () => {
    const wrapper = await mountPlayer({ videoId: 3, session: { video_id: 3, mode: 'external-preview', reason_message: '当前格式不支持内嵌预览', external_action: { button_label: '用系统播放器预览' } } });
    expect(wrapper.find('video').exists()).toBe(false);
    await wrapper.get('button').trigger('click');
    expect(wrapper.emitted('preview-externally')).toEqual([[3]]);
  });
});

describe('InlineVideoPlayer 起播位置与 watch-progress', () => {
  it('没有指定时刻时从续播断点起播，按 resume 上报', async () => {
    const wrapper = await mountPlayer({ resumePositionSeconds: 30 });
    const video = wrapper.get('video');
    const player = fakePlayer(video.element, { duration: 600 });
    await video.trigger('loadedmetadata');
    expect(player.currentTime).toBe(30);
    await video.trigger('play');
    player.currentTime = 31;
    await video.trigger('pause');
    expect(wrapper.emitted('watch-progress').at(-1)[0]).toEqual({ videoID: 1, positionSeconds: 31, completed: false, origin: 'resume', durationSeconds: 600 });
  });

  it('指定时刻优先于断点，按 jump 上报；没有任何起点时不动并按 start 上报', async () => {
    const jump = await mountPlayer({ startTimeMs: 4200, resumePositionSeconds: 30 });
    let video = jump.get('video');
    let player = fakePlayer(video.element);
    await video.trigger('loadedmetadata');
    expect(player.currentTime).toBe(4.2);
    await video.trigger('play');
    await video.trigger('pause');
    expect(jump.emitted('watch-progress').at(-1)[0].origin).toBe('jump');

    const fresh = await mountPlayer();
    video = fresh.get('video');
    player = fakePlayer(video.element);
    player.currentTime = 2;
    await video.trigger('loadedmetadata');
    expect(player.currentTime).toBe(2);
    await video.trigger('play');
    await video.trigger('pause');
    expect(fresh.emitted('watch-progress').at(-1)[0].origin).toBe('start');
  });

  it('播放中每 10 秒最多上报一次，暂停时立即补报', async () => {
    const clock = { now: 1_000_000 };
    vi.spyOn(Date, 'now').mockImplementation(() => clock.now);
    const wrapper = await mountPlayer();
    const video = wrapper.get('video');
    const player = fakePlayer(video.element, { duration: 600 });
    player.paused = false;
    await video.trigger('play');
    await playThrough(video, player, clock, 25);
    const progress = wrapper.emitted('watch-progress').map(([payload]) => payload.positionSeconds);
    expect(progress).toEqual([0.25, 10.25, 20.25]);
    player.currentTime = 25.5;
    await video.trigger('pause');
    expect(wrapper.emitted('watch-progress').map(([payload]) => payload.positionSeconds)).toEqual([0.25, 10.25, 20.25, 25.5]);
  });

  it('还没开始播放时不上报；卸载（关闭）时补报最后位置', async () => {
    const wrapper = mount(InlineVideoPlayer, { props: { videoId: 1, session: inlineSession(1) } });
    await flushPromises();
    const video = wrapper.get('video');
    const player = fakePlayer(video.element);
    await video.trigger('timeupdate');
    expect(wrapper.emitted('watch-progress')).toBeUndefined();
    await video.trigger('play');
    player.currentTime = 12;
    wrapper.unmount();
    expect(wrapper.emitted('watch-progress').at(-1)[0]).toEqual(expect.objectContaining({ videoID: 1, positionSeconds: 12, completed: false }));
  });

  it('越过有效观看阈值记一次 inline_view，播完再记不重复', async () => {
    const clock = { now: 1_000_000 };
    vi.spyOn(Date, 'now').mockImplementation(() => clock.now);
    const wrapper = await mountPlayer({ durationSeconds: 60 });
    const video = wrapper.get('video');
    const player = fakePlayer(video.element);
    player.paused = false;
    await video.trigger('play');
    await playThrough(video, player, clock, 29.75);
    expect(api.RecordViewEvent).not.toHaveBeenCalled();
    await playThrough(video, player, clock, 0.5);
    await flushPromises();
    expect(api.RecordViewEvent).toHaveBeenCalledWith(1, 'inline_view', wrapper.vm.viewSession.id);
    await video.trigger('ended');
    await flushPromises();
    expect(api.RecordViewEvent).toHaveBeenCalledTimes(1);
  });
});

describe('InlineVideoPlayer 书签区间', () => {
  const range = (extra = {}) => ({ startMS: 1000, endMS: 3000, requestID: 1, ...extra });

  it('定位到起点，播到终点暂停一次并发 bookmark-range-end，之后接着播不再暂停', async () => {
    const wrapper = await mountPlayer({ bookmarkRange: range() });
    const video = wrapper.get('video');
    const player = fakePlayer(video.element, { duration: 600 });
    await video.trigger('loadedmetadata');
    expect(player.currentTime).toBe(1);
    player.paused = false;
    await video.trigger('play');
    player.currentTime = 2.9; await video.trigger('timeupdate');
    expect(video.element.pause).not.toHaveBeenCalled();
    player.currentTime = 3; await video.trigger('timeupdate');
    expect(video.element.pause).toHaveBeenCalledTimes(1);
    expect(wrapper.emitted('bookmark-range-end')).toEqual([[range()]]);
    expect(wrapper.vm.bookmarkRangeActive).toBe(false);
    await wrapper.vm.play();
    player.currentTime = 5; await video.trigger('timeupdate');
    expect(video.element.pause).toHaveBeenCalledTimes(1);
    expect(wrapper.emitted('bookmark-range-end')).toHaveLength(1);
  });

  it('源版本与会话不一致时不套用任何起播位置（连断点也不用）', async () => {
    const wrapper = await mountPlayer({ session: inlineSession(1, { source_version: 'new' }), resumePositionSeconds: 30, bookmarkRange: range({ sourceToken: 'old' }) });
    const video = wrapper.get('video');
    const player = fakePlayer(video.element, { duration: 600 });
    player.currentTime = 7;
    await video.trigger('loadedmetadata');
    expect(player.currentTime).toBe(7);
    player.currentTime = 3; await video.trigger('timeupdate');
    expect(wrapper.emitted('bookmark-range-end')).toBeUndefined();
  });

  it('播放中换了书签：这一场改按 jump 上报', async () => {
    const wrapper = await mountPlayer({ resumePositionSeconds: 30 });
    const video = wrapper.get('video');
    const player = fakePlayer(video.element);
    await video.trigger('play');
    await wrapper.setProps({ bookmarkRange: range({ requestID: 2 }) });
    player.currentTime = 4;
    await video.trigger('pause');
    expect(wrapper.emitted('watch-progress').at(-1)[0].origin).toBe('jump');
  });
});

describe('InlineVideoPlayer 无法内嵌与播放代理', () => {
  async function failPlayback(wrapper) {
    const video = wrapper.get('video');
    Object.defineProperty(video.element, 'error', { configurable: true, get: () => ({ code: 3 }) });
    await video.trigger('error');
  }

  it('解码失败：自动排代理（两个按钮照留）；受控时代理就绪后请宿主重取会话', async () => {
    const handlers = installRuntime();
    api.RequestPreviewProxy.mockResolvedValueOnce({ running: true, queued: 1, queued_video_ids: [1], current_video_id: 7, results: [] });
    api.GetPlaybackProxy.mockResolvedValueOnce(null).mockResolvedValue({ video_id: 1, strategy: 'remux', status: 'ready', output_size: 10, last_error: '' });
    const wrapper = await mountPlayer();
    await failPlayback(wrapper);
    await flushPromises();
    expect(wrapper.find('video').exists()).toBe(false);
    expect(api.RequestPreviewProxy).toHaveBeenCalledTimes(1);
    expect(api.RequestPreviewProxy).toHaveBeenCalledWith(1);
    expect(api.CreatePlaybackProxy).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="preview-inline-error-progress"]').text()).toBe('排队中（第 1 个）');
    expect(wrapper.get('[data-test="preview-inline-error-proxy"]').attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="preview-inline-error-external"]').trigger('click');
    expect(wrapper.emitted('preview-externally')).toEqual([[1]]);
    handlers['playback-proxy-state']({ running: false, completed: true, queued: 0, queued_video_ids: [], current_video_id: 0, results: [{ video_id: 1, code: 'created', strategy: 'remux' }] });
    await flushPromises();
    expect(wrapper.emitted('preview-session-stale')).toEqual([[1]]);
    expect(api.GetPreviewSession).not.toHaveBeenCalled();
    await wrapper.setProps({ session: inlineSession(1, { proxy: { strategy: 'remux', size: 10 } }) });
    expect(wrapper.find('video').exists()).toBe(true);
  });

  it('不受控：解码失败自动排代理，就绪后自己重取会话并切回内嵌，不发 preview-session-stale', async () => {
    installRuntime();
    api.GetPreviewSession.mockResolvedValueOnce(inlineSession(1)).mockResolvedValueOnce(inlineSession(1, { proxy: { strategy: 'remux', size: 10 } }));
    api.RequestPreviewProxy.mockResolvedValueOnce({ running: false, completed: true, queued: 0, queued_video_ids: [], current_video_id: 0, results: [{ video_id: 1, code: 'created', strategy: 'remux' }] });
    api.GetPlaybackProxy.mockResolvedValue({ video_id: 1, strategy: 'remux', status: 'ready', output_size: 10, last_error: '' });
    const wrapper = await mountPlayer({ session: undefined });
    await failPlayback(wrapper);
    await flushPromises();
    expect(api.GetPreviewSession).toHaveBeenCalledTimes(2);
    expect(wrapper.find('video').exists()).toBe(true);
    expect(wrapper.vm.playingThroughProxy).toBe(true);
    expect(wrapper.emitted('preview-session-stale')).toBeUndefined();
  });

  it('生成失败的结果码翻成人话，留给宿主显示', async () => {
    api.CreatePlaybackProxy.mockResolvedValueOnce({ results: [{ video_id: 1, code: 'disk_full' }] });
    const wrapper = await mountPlayer();
    await failPlayback(wrapper);
    await wrapper.get('[data-test="preview-inline-error-proxy"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.proxyError).toBe('生成播放代理失败：磁盘空间不足');
    expect(wrapper.vm.proxyPending).toBe(false);
  });
});

describe('InlineVideoPlayer 同一时间只有一个在播与自动播放', () => {
  it('另一个播放器开始播放时暂停这一个；暂停的播放器不再被登记', async () => {
    const first = await mountPlayer();
    const second = await mountPlayer({ videoId: 2, session: inlineSession(2) });
    const a = first.get('video'); fakePlayer(a.element);
    const b = second.get('video'); fakePlayer(b.element);
    await a.trigger('play');
    await b.trigger('play');
    expect(a.element.pause).toHaveBeenCalledTimes(1);
    expect(b.element.pause).not.toHaveBeenCalled();
    await b.trigger('pause');
    await a.trigger('play');
    expect(b.element.pause).not.toHaveBeenCalled();
  });

  it('autoplay：读到元数据、定好位置后每个会话自动开始一次', async () => {
    const wrapper = await mountPlayer({ autoplay: true, resumePositionSeconds: 30 });
    let video = wrapper.get('video');
    const player = fakePlayer(video.element, { duration: 600 });
    await video.trigger('loadedmetadata');
    expect(player.currentTime).toBe(30);
    expect(video.element.play).toHaveBeenCalledTimes(1);
    await video.trigger('loadedmetadata');
    expect(video.element.play).toHaveBeenCalledTimes(1);
    await wrapper.setProps({ session: inlineSession(1, { source_version: 'b' }) });
    video = wrapper.get('video');
    fakePlayer(video.element);
    await video.trigger('loadedmetadata');
    expect(video.element.play).toHaveBeenCalledTimes(1);
  });

  it('默认不自动播放', async () => {
    const wrapper = await mountPlayer();
    const video = wrapper.get('video');
    fakePlayer(video.element);
    await video.trigger('loadedmetadata');
    expect(video.element.play).not.toHaveBeenCalled();
  });
});

describe('InlineVideoPlayer 自动代理（2026-10-13「直接播 + 自动代理」）', () => {
  it('proxy-preparing：自动排一项代理，显示排位与生成中，就绪后请宿主重取会话并切到内嵌', async () => {
    const handlers = installRuntime();
    api.RequestPreviewProxy.mockResolvedValueOnce(idleStatus({ running: true, queued: 2, queued_video_ids: [9, 4], current_video_id: 5 }));
    api.GetPlaybackProxy.mockResolvedValueOnce(null).mockResolvedValue({ video_id: 4, strategy: 'transcode', status: 'ready', output_size: 10, last_error: '' });
    const wrapper = await mountPlayer({ videoId: 4, session: preparingSession(4) });
    expect(api.RequestPreviewProxy).toHaveBeenCalledTimes(1);
    expect(api.RequestPreviewProxy).toHaveBeenCalledWith(4);
    expect(api.CreatePlaybackProxy).not.toHaveBeenCalled();
    expect(wrapper.find('video').exists()).toBe(false);
    expect(wrapper.get('[data-test="preview-proxy-preparing"]').text()).toContain('需要先生成播放代理。');
    expect(wrapper.get('[data-test="preview-proxy-preparing-state"]').text()).toBe('排队中（第 2 个）');
    expect(wrapper.get('[data-test="preview-proxy-preparing-proxy"]').attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="preview-proxy-preparing-external"]').trigger('click');
    expect(wrapper.emitted('preview-externally')).toEqual([[4]]);

    handlers['playback-proxy-state'](idleStatus({ running: true, current_video_id: 4 }));
    await flushPromises();
    expect(wrapper.get('[data-test="preview-proxy-preparing-state"]').text()).toBe('生成中');
    handlers['playback-proxy-state'](idleStatus({ completed: true, results: [{ video_id: 4, code: 'created', strategy: 'transcode' }] }));
    await flushPromises();
    expect(wrapper.emitted('preview-session-stale')).toEqual([[4]]);
    await wrapper.setProps({ session: inlineSession(4, { proxy: { strategy: 'transcode', size: 10 } }) });
    expect(wrapper.find('video').exists()).toBe(true);
    expect(api.RequestPreviewProxy).toHaveBeenCalledTimes(1);
  });

  it('不受控：自己取到 proxy-preparing 也自动排代理，就绪后自己重取并切到内嵌', async () => {
    installRuntime();
    api.GetPreviewSession.mockResolvedValueOnce(preparingSession(4)).mockResolvedValueOnce(inlineSession(4, { proxy: { strategy: 'remux', size: 10 } }));
    api.RequestPreviewProxy.mockResolvedValueOnce(idleStatus({ completed: true, results: [{ video_id: 4, code: 'created', strategy: 'remux' }] }));
    api.GetPlaybackProxy.mockResolvedValue({ video_id: 4, strategy: 'remux', status: 'ready', output_size: 10, last_error: '' });
    const wrapper = await mountPlayer({ videoId: 4, session: undefined });
    await flushPromises();
    expect(api.RequestPreviewProxy).toHaveBeenCalledWith(4);
    expect(api.GetPreviewSession).toHaveBeenCalledTimes(2);
    expect(wrapper.find('video').exists()).toBe(true);
    expect(wrapper.vm.playingThroughProxy).toBe(true);
  });

  it('生成失败：框里显示失败原因，留着系统播放器入口，可手动重试', async () => {
    const handlers = installRuntime();
    api.RequestPreviewProxy.mockResolvedValueOnce(idleStatus({ running: true, current_video_id: 4 }));
    const wrapper = await mountPlayer({ videoId: 4, session: preparingSession(4) });
    handlers['playback-proxy-state'](idleStatus({ completed: true, results: [{ video_id: 4, code: 'encode_failed', message: 'x' }] }));
    await flushPromises();
    const state = wrapper.get('[data-test="preview-proxy-preparing-state"]');
    expect(state.text()).toBe('生成播放代理失败：转封装失败');
    expect(state.attributes('role')).toBe('alert');
    expect(wrapper.find('[data-test="preview-proxy-preparing-external"]').exists()).toBe(true);
    expect(wrapper.vm.showsProxyState).toBe(true);
    api.CreatePlaybackProxy.mockResolvedValueOnce(idleStatus({ running: true, queued: 1, queued_video_ids: [4], current_video_id: 2 }));
    await wrapper.get('[data-test="preview-proxy-preparing-proxy"]').trigger('click');
    await flushPromises();
    expect(api.CreatePlaybackProxy).toHaveBeenCalledWith(4);
    expect(api.RequestPreviewProxy).toHaveBeenCalledTimes(1);
  });
});

describe('InlineVideoPlayer 自动代理跟着预览走', () => {
  it('换片时取消上一部的自动代理，关闭（卸载）时取消当前这一部', async () => {
    installRuntime();
    // 入队回执与后端一样带着刚排进去的这一项。
    api.RequestPreviewProxy.mockImplementation(id => Promise.resolve(idleStatus({ running: true, current_video_id: 99, queued: 1, queued_video_ids: [id] })));
    const wrapper = await mountPlayer({ videoId: 4, session: preparingSession(4) });
    expect(api.RequestPreviewProxy).toHaveBeenLastCalledWith(4);
    await wrapper.setProps({ videoId: 6, session: preparingSession(6) });
    await flushPromises();
    expect(api.CancelPreviewProxy).toHaveBeenCalledTimes(1);
    expect(api.CancelPreviewProxy).toHaveBeenCalledWith(4);
    expect(api.RequestPreviewProxy).toHaveBeenLastCalledWith(6);
    mounted.pop().unmount();
    await flushPromises();
    expect(api.CancelPreviewProxy).toHaveBeenCalledTimes(2);
    expect(api.CancelPreviewProxy).toHaveBeenLastCalledWith(6);
  });

  it('换片到能直放的视频同样取消上一部；直放会话本身不排代理', async () => {
    installRuntime();
    api.RequestPreviewProxy.mockResolvedValue(idleStatus({ running: true, current_video_id: 4 }));
    const wrapper = await mountPlayer({ videoId: 4, session: preparingSession(4) });
    await wrapper.setProps({ videoId: 7, session: inlineSession(7) });
    await flushPromises();
    expect(api.CancelPreviewProxy).toHaveBeenCalledWith(4);
    expect(api.RequestPreviewProxy).toHaveBeenCalledTimes(1);
    mounted.pop().unmount();
    await flushPromises();
    expect(api.CancelPreviewProxy).toHaveBeenCalledTimes(1);
  });

  it('自动代理做完之后关闭不再取消；手动生成的从不取消', async () => {
    const handlers = installRuntime();
    api.RequestPreviewProxy.mockResolvedValueOnce(idleStatus({ running: true, current_video_id: 4 }));
    const auto = await mountPlayer({ videoId: 4, session: preparingSession(4) });
    handlers['playback-proxy-state'](idleStatus({ completed: true, results: [{ video_id: 4, code: 'created', strategy: 'remux' }] }));
    await flushPromises();
    mounted.splice(mounted.indexOf(auto), 1); auto.unmount();
    api.CreatePlaybackProxy.mockResolvedValueOnce(idleStatus({ running: true, queued: 1, queued_video_ids: [3], current_video_id: 2 }));
    const manual = await mountPlayer({ videoId: 3, session: { video_id: 3, mode: 'external-preview', reason_message: '旧行为', external_action: { button_label: '用系统播放器预览' } } });
    await manual.vm.createPlaybackProxy();
    await flushPromises();
    expect(api.CreatePlaybackProxy).toHaveBeenCalledWith(3);
    mounted.pop().unmount();
    await flushPromises();
    expect(api.RequestPreviewProxy).toHaveBeenCalledTimes(1);
    expect(api.CancelPreviewProxy).not.toHaveBeenCalled();
  });

  it('已经在经代理播放时解码失败不再自动排代理', async () => {
    const wrapper = await mountPlayer({ session: inlineSession(1, { proxy: { strategy: 'remux', size: 10 } }) });
    const video = wrapper.get('video');
    Object.defineProperty(video.element, 'error', { configurable: true, get: () => ({ code: 4 }) });
    await video.trigger('error');
    await flushPromises();
    expect(wrapper.find('[data-test="preview-inline-error"]').exists()).toBe(true);
    expect(api.RequestPreviewProxy).not.toHaveBeenCalled();
  });

  it('用户在这次预览里删掉代理：会话回到 proxy-preparing 也不当场重建', async () => {
    installRuntime();
    api.GetPlaybackProxy.mockResolvedValue({ video_id: 4, strategy: 'remux', status: 'ready', output_size: 10, last_error: '' });
    api.DeletePlaybackProxy.mockResolvedValue(undefined);
    const wrapper = await mountPlayer({ videoId: 4, session: inlineSession(4, { proxy: { strategy: 'remux', size: 10 } }) });
    await wrapper.vm.deletePlaybackProxy();
    await flushPromises();
    expect(api.DeletePlaybackProxy).toHaveBeenCalledWith(4);
    expect(wrapper.emitted('preview-session-stale')).toEqual([[4]]);
    await wrapper.setProps({ session: preparingSession(4) });
    await flushPromises();
    expect(api.RequestPreviewProxy).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="preview-proxy-preparing-proxy"]').attributes('disabled')).toBeUndefined();
  });
});
