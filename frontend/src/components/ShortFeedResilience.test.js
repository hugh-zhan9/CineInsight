import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ShortFeedApp from '../short-feed/ShortFeedApp.vue';

// 手机端的 PIN 页、失败提示与有效观看阈值（P-037：D-PC43 / D-PC45 / D-PC46）。
// 走真实的 api.js，只在 fetch 这一层模拟服务端，这样 401 pin_required 的全局接管也一并覆盖。

const item = (id, extra = {}) => ({ media_kind: 'video', id, name: `clip-${id}.mp4`, media_url: `/short-media/video/${id}`, width: 1080, height: 1920, duration: 20, tags: [], ...extra });

function jsonResponse(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: '',
    headers: { get: () => 'application/json' },
    json: async () => body
  };
}

// route(url, options) 返回 jsonResponse(...)，或抛出错误模拟网络断开。
let route;
let calls;

beforeEach(() => {
  calls = [];
  route = () => jsonResponse(404, { code: 'not_found', message: 'not found' });
  vi.stubGlobal('fetch', vi.fn(async (url, options = {}) => {
    const call = { url: String(url), method: options.method || 'GET', body: options.body ? JSON.parse(options.body) : null };
    calls.push(call);
    return route(call);
  }));
  HTMLMediaElement.prototype.play = vi.fn(() => Promise.resolve());
  HTMLMediaElement.prototype.pause = vi.fn();
  Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: false });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = '';
});

async function mountApp() {
  const host = document.createElement('div');
  document.body.append(host);
  const wrapper = mount(ShortFeedApp, { attachTo: host });
  await flushPromises();
  return wrapper;
}

function feedRoute(sequence) {
  let next = 0;
  return (call) => {
    if (call.url.startsWith('/short-api/feed/next')) {
      const entry = sequence[Math.min(next, sequence.length - 1)];
      next += 1;
      if (entry instanceof Error) throw entry;
      return typeof entry === 'function' ? entry(call) : jsonResponse(200, entry);
    }
    return null;
  };
}

describe('PLAY-01 手机端 PIN 页', () => {
  it('PLAY-01 任何数据请求回 401 pin_required 就显示 PIN 页；6 位以下不发请求；登录成功回到 Feed', async () => {
    let authed = false;
    let authReplies = [];
    route = (call) => {
      if (call.url === '/short-api/auth') return authReplies.shift();
      if (!authed) return jsonResponse(401, { code: 'pin_required', error: 'pin_required', message: '需要输入 PIN' });
      if (call.url.startsWith('/short-api/feed/next')) return jsonResponse(200, item(calls.filter(c => c.url.startsWith('/short-api/feed/next')).length));
      return jsonResponse(200, {});
    };
    const wrapper = await mountApp();
    expect(wrapper.find('[data-test="short-feed-pin-gate"]').exists()).toBe(true);
    expect(wrapper.find('.feed-stage').exists()).toBe(false);
    expect(wrapper.find('[data-test="short-feed-retry-bar"]').exists()).toBe(false);

    const input = wrapper.get('[data-test="short-feed-pin-input"]');
    expect(input.attributes('maxlength')).toBeUndefined();
    await input.setValue('12345');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('[data-test="short-feed-pin-error"]').text()).toBe('PIN 至少 6 位');
    expect(calls.some(call => call.url === '/short-api/auth')).toBe(false);

    authReplies = [jsonResponse(401, { code: 'pin_invalid', message: 'PIN 不正确' })];
    await input.setValue('123456');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(calls.find(call => call.url === '/short-api/auth')).toEqual(expect.objectContaining({ method: 'POST', body: { pin: '123456' } }));
    expect(wrapper.get('[data-test="short-feed-pin-error"]').text()).toBe('PIN 不正确');

    authReplies = [jsonResponse(429, { code: 'pin_locked', retry_after: 60, message: '尝试次数过多，请稍后再试' })];
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('[data-test="short-feed-pin-error"]').text()).toBe('尝试次数过多，请 60 秒后再试');

    authReplies = [jsonResponse(429, { code: 'pin_locked', daily_locked: true, retry_after: 80000, locked_until: '2026-10-01T00:00:00Z' })];
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.get('[data-test="short-feed-pin-error"]').text()).toBe('请在电脑上解除锁定，或 24 小时后再试');

    authReplies = [jsonResponse(200, { authenticated: true })];
    authed = true;
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[data-test="short-feed-pin-gate"]').exists()).toBe(false);
    expect(wrapper.vm.items.length).toBeGreaterThan(0);
    expect(wrapper.get('video').attributes('src')).toMatch(/^\/short-media\/video\//);
    wrapper.unmount();
  });

  it('PLAY-01 已在看的中途会话过期：PIN 页接管，登录后回到原来那一条，历史不丢', async () => {
    let expired = false;
    let served = 0;
    route = (call) => {
      if (call.url === '/short-api/auth') return jsonResponse(200, { authenticated: true });
      if (expired) return jsonResponse(401, { code: 'pin_required', message: '需要输入 PIN' });
      if (call.url.startsWith('/short-api/feed/next')) return jsonResponse(200, item(++served));
      return jsonResponse(200, {});
    };
    const wrapper = await mountApp();
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    const before = wrapper.vm.items.map(entry => entry.id);
    expired = true;
    wrapper.vm.prefetchedVideo = null;
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    expect(wrapper.find('[data-test="short-feed-pin-gate"]').exists()).toBe(true);
    expect(wrapper.vm.loadError).toBe('');
    expect(wrapper.vm.items.map(entry => entry.id)).toEqual(before);

    expired = false;
    await wrapper.get('[data-test="short-feed-pin-input"]').setValue('secret-pin');
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.find('[data-test="short-feed-pin-gate"]').exists()).toBe(false);
    expect(wrapper.vm.items.map(entry => entry.id).slice(0, before.length)).toEqual(before);
    wrapper.unmount();
  });
});

describe('PLAY-13 手机端失败提示', () => {
  it('PLAY-13 网络异常不清空浏览历史，显示「网络异常 · 重试」，重试成功后接着往下', async () => {
    const offline = new TypeError('Failed to fetch');
    const sequence = [item(1), item(2), offline, offline, item(3), item(4)];
    const feed = feedRoute(sequence);
    route = call => feed(call) || jsonResponse(200, {});
    const wrapper = await mountApp();
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    expect(wrapper.vm.items.map(entry => entry.id)).toEqual([1, 2]);
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    expect(wrapper.vm.items.map(entry => entry.id)).toEqual([1, 2]);
    expect(wrapper.vm.index).toBe(1);
    const bar = wrapper.get('[data-test="short-feed-retry-bar"]');
    expect(bar.text()).toContain('网络异常');
    expect(bar.text()).toContain('重试');
    expect(wrapper.get('video').attributes('src')).toBe('/short-media/video/2');

    // 往回划照样能看；回到第一条时后台预取恢复成功，拿到了第 3 条。
    await wrapper.vm.nextVideo(-1);
    await flushPromises();
    expect(wrapper.vm.index).toBe(0);

    // 「重试」不必先划回末尾：直接取新的一条接到历史后面并切过去。
    await wrapper.get('[data-test="short-feed-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.items.map(entry => entry.id)).toEqual([1, 2, 3]);
    expect(wrapper.vm.index).toBe(2);
    expect(wrapper.find('[data-test="short-feed-retry-bar"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('PLAY-13 一开始就取不到：空态说明原因，同样给重试', async () => {
    route = () => { throw new TypeError('Failed to fetch'); };
    const wrapper = await mountApp();
    expect(wrapper.get('.feed-empty').text()).toBe('网络异常');
    expect(wrapper.find('[data-test="short-feed-retry"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('PLAY-13 点赞、收藏写库失败时回滚并提示', async () => {
    const feed = feedRoute([item(1), item(2)]);
    route = (call) => {
      if (call.url.endsWith('/like') || call.url.endsWith('/favorite')) return jsonResponse(500, { code: 'mutation_failed', message: '操作失败' });
      return feed(call) || jsonResponse(200, {});
    };
    const wrapper = await mountApp();
    await wrapper.vm.toggleLike();
    await flushPromises();
    expect(wrapper.vm.currentVideo.liked).toBeFalsy();
    expect(wrapper.get('.feed-toast').text()).toContain('点赞失败：操作失败');

    await wrapper.vm.toggleFavorite();
    await flushPromises();
    expect(wrapper.vm.currentVideo.favorited).toBeFalsy();
    expect(wrapper.get('.feed-toast').text()).toContain('收藏失败：操作失败');
    wrapper.unmount();
  });

  it.each([
    ['volume_offline', '删除失败：文件所在磁盘当前未连接，未做任何改动'],
    ['permission_denied', '删除失败：没有权限把文件移到废纸篓，请在桌面端处理']
  ])('PLAY-13 删除遇到 409 %s 时提示原因，条目留在原处', async (code, text) => {
    const feed = feedRoute([item(1), item(2)]);
    route = (call) => {
      if (call.url.endsWith('/delete')) return jsonResponse(409, { code, error: code, message: 'server text' });
      return feed(call) || jsonResponse(200, {});
    };
    const wrapper = await mountApp();
    await wrapper.vm.confirmDelete();
    await flushPromises();
    expect(wrapper.get('.feed-toast').text()).toBe(text);
    expect(wrapper.vm.items.map(entry => entry.id)).toEqual([1]);
    expect(wrapper.vm.pendingUndo).toBeNull();
    wrapper.unmount();
  });

  it('PLAY-13 收藏页加载失败显示错误态与重试，不再显示成「暂无收藏」', async () => {
    const feed = feedRoute([item(1), item(2)]);
    let favoritesOK = false;
    route = (call) => {
      if (call.url === '/short-api/favorites') {
        return favoritesOK ? jsonResponse(200, { items: [item(5, { name: 'fav.mp4' })] }) : jsonResponse(500, { code: 'favorites_failed', message: '获取收藏失败' });
      }
      return feed(call) || jsonResponse(200, {});
    };
    const wrapper = await mountApp();
    await wrapper.vm.openFavorites();
    await flushPromises();
    expect(wrapper.get('[data-test="short-feed-favorites-error"]').text()).toContain('收藏加载失败：获取收藏失败');
    expect(wrapper.text()).not.toContain('暂无收藏');

    favoritesOK = true;
    await wrapper.get('[data-test="short-feed-favorites-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="short-feed-favorites-error"]').exists()).toBe(false);
    expect(wrapper.get('.favorite-title').text()).toBe('fav.mp4');
    wrapper.unmount();
  });

  it('PLAY-13 播放范围面板说明「N 条因格式暂不可播」', async () => {
    const feed = feedRoute([item(1), item(2)]);
    route = (call) => {
      if (call.url.startsWith('/short-api/feed/scopes')) {
        return jsonResponse(200, { scopes: [
          { scope: 'all', name: '全部短视频', count: 12, unplayable_count: 3 },
          { scope: 'unwatched', name: '未看', count: 4, unplayable_count: 0 }
        ] });
      }
      return feed(call) || jsonResponse(200, {});
    };
    const wrapper = await mountApp();
    await wrapper.vm.openScopeSheet();
    await flushPromises();
    expect(document.body.querySelector('[data-test="short-feed-unplayable-all"]')?.textContent).toBe('3 条因格式暂不可播，可在电脑上生成播放代理');
    expect(document.body.querySelector('[data-test="short-feed-unplayable-unwatched"]')).toBeNull();
    wrapper.unmount();
  });

  it('PLAY-13 视频解码失败自动跳到下一条时留下提示', async () => {
    vi.useFakeTimers();
    const feed = feedRoute([item(1), item(2), item(3)]);
    route = call => feed(call) || jsonResponse(200, {});
    const wrapper = await mountApp();
    await wrapper.get('video').trigger('error');
    expect(wrapper.get('.feed-toast').text()).toBe('「clip-1.mp4」无法在浏览器中播放，已跳过');
    await vi.advanceTimersByTimeAsync(400);
    await flushPromises();
    expect(wrapper.vm.currentVideo.id).toBe(2);
    wrapper.unmount();
    vi.useRealTimers();
  });
});

describe('PLAY-07 手机端有效观看阈值', () => {
  function fakePlayer(element, duration) {
    const state = { currentTime: 0, duration, paused: false, playbackRate: 1 };
    for (const key of Object.keys(state)) {
      Object.defineProperty(element, key, { configurable: true, get: () => state[key], set: value => { state[key] = value; } });
    }
    return state;
  }

  it('PLAY-07 playing 那一刻不再上报；累计播放越过 min(60, 时长一半) 才记一次 mobile_feed，拖动不算', async () => {
    const clock = { now: 0 };
    vi.spyOn(Date, 'now').mockImplementation(() => clock.now);
    const feed = feedRoute([item(1), item(2)]);
    route = call => feed(call) || jsonResponse(200, {});
    const wrapper = await mountApp();
    const video = wrapper.get('video');
    const player = fakePlayer(video.element, 20);
    const plays = () => calls.filter(call => call.url.endsWith('/play'));
    await video.trigger('playing');
    await video.trigger('play');
    await flushPromises();
    expect(plays()).toHaveLength(0);

    const tick = async (to) => { player.currentTime = to; clock.now += 250; await video.trigger('timeupdate'); };
    for (let t = 0.25; t <= 5; t += 0.25) await tick(t);
    // 往前拖到 15 秒：断开一段，不算播放。
    wrapper.vm.videoDuration = 20;
    wrapper.vm.commitSeek(15);
    await tick(15);
    for (let t = 15.25; t <= 19.5; t += 0.25) await tick(t);
    await flushPromises();
    expect(plays()).toHaveLength(0);

    // 循环回片头接着播（回到片头那一下不算），累计到 10 秒（20 秒片的一半）才记。
    for (let t = 0.25; t <= 0.75; t += 0.25) await tick(t);
    await flushPromises();
    expect(plays()).toHaveLength(0);
    await tick(1);
    await flushPromises();
    expect(plays()).toHaveLength(1);
    expect(plays()[0]).toEqual(expect.objectContaining({ method: 'POST', url: '/short-api/items/video/1/play', body: { source: 'short_feed', view_session_id: expect.any(String) } }));
    for (let t = 1; t <= 4; t += 0.25) await tick(t);
    await flushPromises();
    expect(plays()).toHaveLength(1);
    wrapper.unmount();
  });
});

describe('手机观看会话', () => {
  it('includes one stable identity per visit; automatic loops do not split it and reentry does', async () => {
    const feed = feedRoute([item(1), item(2)]); route = call => feed(call) || jsonResponse(200, {});
    const wrapper = await mountApp();
    await wrapper.vm.recordCurrentItemView(); await flushPromises();
    const first = calls.filter(call => call.url.endsWith('/play'))[0]; expect(first.body.source).toBe('short_feed'); expect(first.body.view_session_id).toBeTruthy();
    await wrapper.get('video').trigger('ended'); await wrapper.vm.recordCurrentItemView(); await flushPromises();
    expect(calls.filter(call => call.url.endsWith('/play'))).toHaveLength(1);
    await wrapper.vm.nextVideo(1); await wrapper.vm.nextVideo(-1); await flushPromises(); await wrapper.vm.recordCurrentItemView(); await flushPromises();
    const second = calls.filter(call => call.url.endsWith('/play')).at(-1);
    expect(second.url).toBe(first.url); expect(second.body.view_session_id).not.toBe(first.body.view_session_id); wrapper.unmount();
  });
});
