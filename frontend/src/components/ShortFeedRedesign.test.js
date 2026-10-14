import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ShortFeedApp from '../short-feed/ShortFeedApp.vue';

// 手机网页重做（D-MW-01..07，docs/loopx/design/2026-10-14-mobile-web-redesign/）。
// 与 ShortFeedResilience 一样走真实的 api.js，只在 fetch 这一层模拟服务端。

const tag = (id, name, extra = {}) => ({ id, name, color: '#3b82f6', automatic: false, ...extra });
const video = (id, extra = {}) => ({ media_kind: 'video', id, name: `clip-${id}.mp4`, media_url: `/short-media/video/${id}`, width: 1080, height: 1920, duration: 20, tags: [], liked: false, favorited: false, personal_rating: null, watched: false, ...extra });
const image = (id, extra = {}) => ({ media_kind: 'image', id, name: `IMG_${id}.jpg`, media_url: `/short-media/image/${id}`, width: 4032, height: 3024, duration: 0, tags: [], liked: false, favorited: false, personal_rating: null, watched: false, ...extra });
const SCOPES = [
  { scope: 'all', name: '全部', count: 1284, unplayable_count: 23 },
  { scope: 'unwatched', name: '没看过', count: 312, unplayable_count: 0 },
  { scope: 'favorites', name: '只在收藏里随机', count: 48, unplayable_count: 0 },
  { scope: 'recent', name: '最近 30 天添加', count: 96, unplayable_count: 0 },
  { scope: 'untagged', name: '还没打标签', count: 205, unplayable_count: 7 }
];

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, statusText: '', headers: { get: () => 'application/json' }, json: async () => body };
}

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
  // 资源类型记在 localStorage（测试环境里没有，换成内存版）。
  const store = new Map();
  vi.stubGlobal('localStorage', { getItem: key => store.get(key) ?? null, setItem: (key, value) => store.set(key, String(value)), clear: () => store.clear() });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
  document.body.innerHTML = '';
});

// 一个小服务端：next 依次出队（最后一条之后回 404 no_eligible_videos），其余路由按 handlers 覆盖。
function server({ next = [video(1), video(2), video(3), video(4)], favorites = [], handlers = {} } = {}) {
  const queue = [...next];
  route = (call) => {
    for (const [prefix, handler] of Object.entries(handlers)) {
      if (call.url.startsWith(prefix)) {
        const reply = handler(call);
        if (reply) return reply;
      }
    }
    if (call.url.startsWith('/short-api/feed/next')) {
      const entry = queue.shift();
      if (entry instanceof Error) throw entry;
      return entry ? jsonResponse(200, entry) : jsonResponse(404, { code: 'no_eligible_videos', message: '没有可播放的内容' });
    }
    if (call.url.startsWith('/short-api/feed/scopes')) return jsonResponse(200, { scopes: SCOPES });
    if (call.url === '/short-api/favorites') return jsonResponse(200, { items: favorites });
    if (call.url.startsWith('/short-api/tags')) return jsonResponse(200, { tags: [] });
    return jsonResponse(200, {});
  };
}

async function mountApp() {
  const host = document.createElement('div');
  document.body.append(host);
  const wrapper = mount(ShortFeedApp, { attachTo: host });
  await flushPromises();
  return wrapper;
}

const body = () => document.body;
const toast = () => body().querySelector('.feed-toast');
const callsTo = suffix => calls.filter(call => call.url.endsWith(suffix) || call.url.startsWith(suffix));
async function openMore(wrapper) {
  await wrapper.get('[data-test="short-feed-more"]').trigger('click');
  await flushPromises();
  return body().querySelector('[role="dialog"][aria-label="更多操作"]');
}
async function click(element) {
  element.click();
  await flushPromises();
}

describe('D-MW-01 顶部「推荐 · 收藏」', () => {
  it('顶栏只有两个标签、范围与静音；去掉标题、计数、圆点、收藏夹与全屏按钮', async () => {
    server();
    const wrapper = await mountApp();
    const bar = wrapper.get('.top-bar');
    const tabs = bar.findAll('.feed-tabs__tab');
    expect(tabs.map(tab => tab.text())).toEqual(['推荐', '收藏']);
    expect(tabs[0].attributes('aria-current')).toBe('page');
    expect(tabs[1].attributes('aria-current')).toBeUndefined();
    expect(bar.findAll('button')).toHaveLength(4);
    expect(bar.get('[data-test="short-feed-scope"]').text()).toBe('全部');
    expect(bar.get('[data-test="short-feed-scope"]').attributes('aria-label')).toBe('播放范围：全部，视频和图片');
    expect(bar.find('[aria-label="打开声音"]').exists()).toBe(true);
    expect(wrapper.text()).not.toContain('短视频');
    expect(wrapper.find('.feed-dots').exists()).toBe(false);
    expect(wrapper.find('[data-test="short-feed-fullscreen"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('切到收藏再回来，推荐流的历史与位置不变', async () => {
    server({ favorites: [video(50, { favorited: true })] });
    const wrapper = await mountApp();
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    await wrapper.vm.nextVideo(-1);
    await flushPromises();
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1, 2, 3]);
    expect(wrapper.vm.index).toBe(1);

    await wrapper.get('[data-test="short-feed-tab-favorites"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('.feed-stage').exists()).toBe(false);
    expect(wrapper.find('.favorites-view').exists()).toBe(true);
    expect(wrapper.get('[data-test="short-feed-tab-favorites"]').attributes('aria-current')).toBe('page');
    expect(callsTo('/short-api/favorites')).toHaveLength(1);
    const nextCalls = callsTo('/short-api/feed/next').length;
    // 墙上方向键不翻推荐流。
    await wrapper.get('main').trigger('keydown', { key: 'ArrowDown' });
    await flushPromises();
    expect(callsTo('/short-api/feed/next')).toHaveLength(nextCalls);

    await wrapper.get('[data-test="short-feed-tab-feed"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1, 2, 3]);
    expect(wrapper.vm.index).toBe(1);
    expect(wrapper.get('video.feed-video').attributes('src')).toBe('/short-media/video/2');
    wrapper.unmount();
  });

  it('范围按钮写当前范围名，类型收窄时加「· 视频」', async () => {
    window.localStorage.setItem('short-feed-media-kind', 'video');
    server();
    const wrapper = await mountApp();
    expect(callsTo('/short-api/feed/next')[0].url).toContain('media=video');
    expect(wrapper.get('[data-test="short-feed-scope"]').text()).toBe('全部 · 视频');
    await wrapper.get('[data-test="short-feed-scope"]').trigger('click');
    await flushPromises();
    await click(body().querySelector('[data-test="short-feed-scope-untagged"]'));
    expect(wrapper.get('[data-test="short-feed-scope"]').text()).toBe('还没打标签 · 视频');
    expect(wrapper.get('[data-test="short-feed-scope"]').attributes('aria-label')).toBe('播放范围：还没打标签，只看视频');
    wrapper.unmount();
  });
});

describe('D-MW-02 右侧三个按钮与「更多」', () => {
  it('右侧只有收藏、点赞、更多，状态写在按钮上', async () => {
    server({ next: [video(1, { favorited: true }), video(2)] });
    const wrapper = await mountApp();
    const buttons = wrapper.findAll('.action-rail button');
    expect(buttons.map(button => button.text())).toEqual(['已收藏', '点赞', '更多']);
    expect(buttons[0].attributes('aria-pressed')).toBe('true');
    expect(buttons[1].attributes('aria-pressed')).toBe('false');
    await buttons[1].trigger('click');
    await flushPromises();
    expect(callsTo('/like').at(-1)).toEqual(expect.objectContaining({ url: '/short-api/items/video/1/like', body: { liked: true } }));
    expect(wrapper.findAll('.action-rail button')[1].text()).toBe('已点赞');
    wrapper.unmount();
  });

  it('「更多」按媒体类型与能力列出：评分、标签、已看过（只对视频）、全屏（能全屏时）、移到废纸篓、取消', async () => {
    server({ next: [video(1, { personal_rating: 8.5, tags: [tag(1, '旅行'), tag(2, '海边')] }), image(7)] });
    const wrapper = await mountApp();
    let sheet = await openMore(wrapper);
    expect(sheet.querySelector('h2').textContent).toBe('clip-1.mp4');
    expect(sheet.querySelector('.sheet__subtitle').textContent).toBe('视频 · 00:20');
    expect([...sheet.querySelectorAll('.more-row__label')].map(node => node.textContent.replace(/\s+/g, ''))).toEqual(['个人评分', '标签', '已看过', '移到废纸篓点了直接移走，可在电脑端回收站恢复']);
    expect(sheet.querySelector('[data-test="short-feed-more-rating"]').textContent).toContain('8.5');
    expect(sheet.querySelector('[data-test="short-feed-more-tags"]').textContent).toContain('2 个');
    expect(sheet.querySelector('[data-test="short-feed-watched"]').getAttribute('role')).toBe('switch');
    expect(sheet.querySelector('[data-test="short-feed-fullscreen"]')).toBeNull();
    expect(sheet.querySelector('.sheet__footer').textContent.trim()).toBe('取消');
    await click(sheet.querySelector('[data-test="short-feed-more-cancel"]'));
    expect(body().querySelector('.sheet-layer')).toBeNull();

    await wrapper.vm.nextVideo(1);
    await flushPromises();
    sheet = await openMore(wrapper);
    expect(sheet.querySelector('.sheet__subtitle').textContent).toBe('图片 · 4032 × 3024');
    expect(sheet.querySelector('[data-test="short-feed-watched"]')).toBeNull();
    expect(sheet.querySelector('[data-test="short-feed-more-rating"]').textContent).toContain('未评分');
    wrapper.unmount();
  });

  it('已看过是开关：写入成功跟着变，失败回到原状并提示', async () => {
    let fail = false;
    server({ handlers: { '/short-api/items/video/1/watched': call => (fail ? jsonResponse(500, { code: 'x', message: '写入失败' }) : jsonResponse(200, video(1, { watched: call.body.watched }))) } });
    const wrapper = await mountApp();
    const sheet = await openMore(wrapper);
    const row = () => body().querySelector('[data-test="short-feed-watched"]');
    expect(row().getAttribute('aria-checked')).toBe('false');
    await click(row());
    expect(callsTo('/watched').at(-1).body).toEqual({ watched: true });
    expect(row().getAttribute('aria-checked')).toBe('true');
    expect(sheet.isConnected).toBe(true);
    fail = true;
    await click(row());
    expect(row().getAttribute('aria-checked')).toBe('true');
    expect(toast().textContent).toContain('标记失败：写入失败');
    // 提示挂在 body 上，叠在面板（--z-modal）之上。
    expect(toast().parentElement).toBe(body());
    wrapper.unmount();
  });

  it('长按画面照旧同时点赞 + 收藏', async () => {
    vi.useFakeTimers();
    server();
    const wrapper = await mountApp();
    wrapper.get('video').element.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 100, clientY: 300 }));
    await vi.advanceTimersByTimeAsync(700);
    await flushPromises();
    expect(callsTo('/like').map(call => call.body)).toEqual([{ liked: true }]);
    expect(callsTo('/favorite').map(call => call.body)).toEqual([{ favorited: true }]);
    expect(wrapper.findAll('.action-rail button').map(button => button.text()).slice(0, 2)).toEqual(['已收藏', '已点赞']);
    wrapper.unmount();
  });
});

describe('D-MW-03 移到废纸篓：不确认、不撤销', () => {
  it('点一下就移走：只调一次 delete，前进到下一条，提示去电脑端回收站找回，没有按钮也不调 restore', async () => {
    server();
    const wrapper = await mountApp();
    await wrapper.vm.nextVideo(1);
    await wrapper.vm.nextVideo(-1);
    await flushPromises();
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1, 2]);
    expect(wrapper.vm.index).toBe(0);
    const sheet = await openMore(wrapper);
    await click(sheet.querySelector('[data-test="short-feed-delete"]'));
    expect(callsTo('/delete')).toEqual([expect.objectContaining({ url: '/short-api/items/video/1/delete', method: 'POST', body: { confirm_move_to_trash: true } })]);
    expect(body().querySelector('.sheet-layer')).toBeNull();
    expect(body().querySelector('[role="dialog"]')).toBeNull();
    expect(wrapper.vm.items.map(item => item.id)).toEqual([2]);
    expect(wrapper.vm.currentVideo.id).toBe(2);
    expect(toast().textContent.replace(/\s+/g, '')).toBe('「clip-1.mp4」已移到废纸篓要找回，在电脑端「回收站」里恢复');
    expect(toast().querySelector('button')).toBeNull();
    expect(calls.some(call => call.url.endsWith('/restore'))).toBe(false);
    wrapper.unmount();
  });

  it('删的是历史里最后一条：取新的一条接上', async () => {
    server();
    const wrapper = await mountApp();
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1]);
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    expect(wrapper.vm.items.map(item => item.id)).toEqual([2]);
    expect(wrapper.get('video.feed-video').attributes('src')).toBe('/short-media/video/2');
    wrapper.unmount();
  });

  it('请求在路上时「更多」里的删除项禁用', async () => {
    let finish;
    server({ handlers: { '/short-api/items/video/1/delete': () => new Promise(resolve => { finish = () => resolve(jsonResponse(200, {})); }) } });
    const wrapper = await mountApp();
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    const again = await openMore(wrapper);
    expect(again.querySelector('[data-test="short-feed-delete"]').disabled).toBe(true);
    await click(again.querySelector('[data-test="short-feed-delete"]'));
    expect(callsTo('/delete')).toHaveLength(1);
    finish();
    await flushPromises();
    expect(wrapper.vm.deleting).toBe(false);
    wrapper.unmount();
  });

  it('不支持废纸篓：留在原条目，说清原因', async () => {
    server({ handlers: { '/short-api/items/video/1/delete': () => jsonResponse(409, { code: 'trash_unsupported', message: 'x' }) } });
    const wrapper = await mountApp();
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    expect(wrapper.vm.currentVideo.id).toBe(1);
    expect(toast().textContent.trim()).toBe('删除失败：该磁盘不支持废纸篓，请在桌面端处理');
    wrapper.unmount();
  });

  // 没有确认与撤销兜底，删的必须是打开「更多」时看着的那一条：面板开着时新取到的一条只接到历史末尾、不切过去。
  it('「更多」开着时下一条才取回来：不切过去，删的仍是正看着的那一条', async () => {
    let releaseNext;
    let nextCount = 0;
    server({ handlers: { '/short-api/feed/next': () => {
      nextCount += 1;
      if (nextCount === 1) return jsonResponse(200, video(1));
      // 后台预取失败，划到末尾时只能现取，取的这段时间里用户点开了「更多」。
      if (nextCount === 2) return jsonResponse(404, { code: 'no_eligible_videos', message: '没有' });
      return new Promise(resolve => { releaseNext = () => resolve(jsonResponse(200, video(2))); });
    } } });
    const wrapper = await mountApp();
    const swipe = wrapper.vm.nextVideo(1);
    await flushPromises();
    expect(wrapper.vm.loading).toBe(true);
    const sheet = await openMore(wrapper);
    expect(sheet.querySelector('h2').textContent).toBe('clip-1.mp4');
    releaseNext();
    await swipe;
    await flushPromises();
    expect(wrapper.vm.currentVideo.id).toBe(1);
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1, 2]);
    expect(body().querySelector('[role="dialog"][aria-label="更多操作"] h2').textContent).toBe('clip-1.mp4');
    await click(body().querySelector('[data-test="short-feed-delete"]'));
    expect(callsTo('/delete').map(call => call.url)).toEqual(['/short-api/items/video/1/delete']);
    // 删完前进到刚才接上的那一条。
    expect(wrapper.vm.currentVideo.id).toBe(2);
    wrapper.unmount();
  });

  it('删除很慢、删完前又打开了「更多」：删完换到下一条时面板收起，删除不会落到下一条上', async () => {
    let finishDelete;
    server({ handlers: { '/short-api/items/video/1/delete': () => new Promise(resolve => { finishDelete = () => resolve(jsonResponse(200, {})); }) } });
    const wrapper = await mountApp();
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    const again = await openMore(wrapper);
    expect(again.querySelector('h2').textContent).toBe('clip-1.mp4');
    expect(again.querySelector('[data-test="short-feed-delete"]').disabled).toBe(true);
    finishDelete();
    await flushPromises();
    expect(wrapper.vm.currentVideo.id).toBe(2);
    expect(body().querySelector('[role="dialog"][aria-label="更多操作"]')).toBeNull();
    expect(callsTo('/delete').map(call => call.url)).toEqual(['/short-api/items/video/1/delete']);
    wrapper.unmount();
  });

  it('删的是最后一条、下一条还要现取：删完立刻收起面板，取的这段时间里不会对前一条误删', async () => {
    let finishDelete;
    let releaseNext;
    let nextCount = 0;
    server({ handlers: {
      '/short-api/feed/next': () => {
        nextCount += 1;
        if (nextCount <= 2) return jsonResponse(200, video(nextCount));
        // 2 之后的预取没取到，删完只能现取，而且取得慢。
        if (nextCount === 3) return jsonResponse(404, { code: 'no_eligible_videos', message: '没有' });
        return new Promise(resolve => { releaseNext = () => resolve(jsonResponse(200, video(3))); });
      },
      '/short-api/items/video/2/delete': () => new Promise(resolve => { finishDelete = () => resolve(jsonResponse(200, {})); })
    } });
    const wrapper = await mountApp();
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1, 2]);
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    await openMore(wrapper);
    finishDelete();
    await flushPromises();
    // 新的一条还在取：面板已经收起，画面停在前一条上。
    expect(releaseNext).toBeTypeOf('function');
    expect(body().querySelector('[role="dialog"][aria-label="更多操作"]')).toBeNull();
    expect(wrapper.vm.sheet).toBe(null);
    releaseNext();
    await flushPromises();
    expect(wrapper.vm.currentVideo.id).toBe(3);
    expect(callsTo('/delete').map(call => call.url)).toEqual(['/short-api/items/video/2/delete']);
    wrapper.unmount();
  });

  it('评分写入在路上时这一条被删了：晚到的回包只换它自己，不顶掉换上来的那一条', async () => {
    let finishDelete;
    let releaseRating;
    server({ handlers: {
      '/short-api/items/video/1/delete': () => new Promise(resolve => { finishDelete = () => resolve(jsonResponse(200, {})); }),
      '/short-api/items/video/1/rating': call => new Promise(resolve => { releaseRating = () => resolve(jsonResponse(200, video(1, { personal_rating: call.body.rating }))); })
    } });
    const wrapper = await mountApp();
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-more-rating"]'));
    wrapper.vm.applyRating(7);
    await flushPromises();
    finishDelete();
    await flushPromises();
    expect(wrapper.vm.currentVideo.id).toBe(2);
    releaseRating();
    await flushPromises();
    expect(wrapper.vm.items.map(item => item.id)).toEqual([2]);
    expect(wrapper.vm.currentVideo.id).toBe(2);
    expect(wrapper.vm.currentVideo.personal_rating).toBe(null);
    wrapper.unmount();
  });

  it('删除很慢时在标签面板里新建标签：删完换到下一条后，标签照样建好但不挂到下一条上', async () => {
    let finishDelete;
    let finishCreate;
    server({ handlers: {
      '/short-api/items/video/1/delete': () => new Promise(resolve => { finishDelete = () => resolve(jsonResponse(200, {})); }),
      '/short-api/tags': call => (call.method === 'POST' ? new Promise(resolve => { finishCreate = () => resolve(jsonResponse(200, { id: 77, name: '新标签' })); }) : null)
    } });
    const wrapper = await mountApp();
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-more-tags"]'));
    wrapper.vm.tagKeyword = '新标签';
    await flushPromises();
    const creating = wrapper.vm.createTagFromKeyword();
    await flushPromises();
    finishDelete();
    await flushPromises();
    expect(wrapper.vm.currentVideo.id).toBe(2);
    finishCreate();
    await creating;
    await flushPromises();
    expect(callsTo('/tag')).toEqual([]);
    expect(wrapper.vm.feedTags.map(item => item.id)).toContain(77);
    wrapper.unmount();
  });

  it('长按到点之前换到了下一条：这次长按作废，不给换上来的那一条点赞 + 收藏', async () => {
    let releaseNext;
    let nextCount = 0;
    server({ handlers: { '/short-api/feed/next': () => {
      nextCount += 1;
      if (nextCount === 1) return jsonResponse(200, video(1));
      if (nextCount === 2) return jsonResponse(404, { code: 'no_eligible_videos', message: '没有' });
      return new Promise(resolve => { releaseNext = () => resolve(jsonResponse(200, video(2))); });
    } } });
    const wrapper = await mountApp();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const swipe = wrapper.vm.nextVideo(1);
    await flushPromises();
    // 手指按在还显示着的第 1 条上；650ms 到点之前第 2 条取回来了。
    wrapper.vm.startLongPressAt(100, 100);
    vi.advanceTimersByTime(300);
    releaseNext();
    await swipe;
    await flushPromises();
    expect(wrapper.vm.currentVideo.id).toBe(2);
    vi.advanceTimersByTime(400);
    await flushPromises();
    expect(callsTo('/like')).toEqual([]);
    expect(callsTo('/favorite')).toEqual([]);
    wrapper.unmount();
  });

  it('同一条在历史里出现两次（范围很小时会重复）：删掉后两处都摘掉，停在后面那一条或取新的', async () => {
    server({ next: [video(1), video(2), video(1), video(3)] });
    const wrapper = await mountApp();
    // 每划一次等后台预取落定，服务端给的顺序就是历史的顺序。
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1, 2, 1]);
    expect(wrapper.vm.index).toBe(2);
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    expect(callsTo('/delete')).toHaveLength(1);
    expect(wrapper.vm.items.map(item => item.id)).toEqual([2, 3]);
    expect(wrapper.vm.currentVideo.id).toBe(3);
    // 往回划只剩 2，不会再翻到已删的 1。
    await wrapper.vm.nextVideo(-1);
    expect(wrapper.vm.currentVideo.id).toBe(2);
    await wrapper.vm.nextVideo(-1);
    expect(wrapper.vm.currentVideo.id).toBe(2);
    wrapper.unmount();
  });
});

describe('D-MW-04 评分：10 颗星 + 半分滑杆', () => {
  it('点第 n 颗写 n 分；拖动只改显示，松手写半分；清空写 null', async () => {
    server({ handlers: { '/short-api/items/video/1/rating': call => jsonResponse(200, video(1, { personal_rating: call.body.rating })) } });
    const wrapper = await mountApp();
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-more-rating"]'));
    const value = () => body().querySelector('[data-test="short-feed-rating-value"]').textContent;
    expect(value()).toBe('未评分');
    expect(body().querySelectorAll('.rating-star')).toHaveLength(10);
    await click(body().querySelector('[data-test="short-feed-rating-star-7"]'));
    expect(callsTo('/rating').map(call => call.body)).toEqual([{ rating: 7 }]);
    expect(value()).toBe('7.0');
    expect(body().querySelectorAll('.rating-star--full')).toHaveLength(7);

    const slider = body().querySelector('[data-test="short-feed-rating-slider"]');
    slider.value = '8.5';
    slider.dispatchEvent(new Event('input'));
    await flushPromises();
    expect(value()).toBe('8.5');
    expect(callsTo('/rating')).toHaveLength(1);
    slider.dispatchEvent(new Event('change'));
    await flushPromises();
    expect(callsTo('/rating').at(-1).body).toEqual({ rating: 8.5 });
    expect(body().querySelectorAll('.rating-star--half')).toHaveLength(1);

    await click(body().querySelector('[data-test="short-feed-rating-clear"]'));
    expect(callsTo('/rating').at(-1).body).toEqual({ rating: null });
    expect(value()).toBe('未评分');
    await click(body().querySelector('[data-test="short-feed-rating-done"]'));
    expect(body().querySelector('.sheet-layer')).toBeNull();
    wrapper.unmount();
  });

  it('写入失败：显示回到写入前的分并说原因', async () => {
    server({ next: [video(1, { personal_rating: 6 })], handlers: { '/short-api/items/video/1/rating': () => jsonResponse(500, { code: 'x', message: '写入失败' }) } });
    const wrapper = await mountApp();
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-more-rating"]'));
    await click(body().querySelector('[data-test="short-feed-rating-star-9"]'));
    expect(body().querySelector('[data-test="short-feed-rating-value"]').textContent).toBe('6.0');
    expect(body().querySelector('[data-test="short-feed-rating-slider"]').value).toBe('6');
    expect(toast().textContent).toContain('评分失败：写入失败');
    wrapper.unmount();
  });
});

describe('D-MW-05 收藏墙与「在收藏里看」', () => {
  const favorites = () => [
    video(11, { favorited: true, duration: 161 }),
    image(12, { favorited: true }),
    video(13, { favorited: true, duration: 48 }),
    image(14, { favorited: true }),
    video(15, { favorited: true, duration: 5 })
  ];

  async function openWall(options = {}) {
    server({ favorites: favorites(), ...options });
    const wrapper = await mountApp();
    await wrapper.get('[data-test="short-feed-tab-favorites"]').trigger('click');
    await flushPromises();
    return wrapper;
  }

  it('三列缩略图墙：缩略图走 /short-thumb/，视频角标时长，类型筛选与计数', async () => {
    const wrapper = await openWall();
    const tiles = () => wrapper.findAll('.favorite-tile');
    expect(tiles()).toHaveLength(5);
    expect(wrapper.get('[data-test="short-feed-favorites-count"]').text()).toBe('5 个 · 最近收藏在前');
    expect(tiles().map(tile => tile.get('img').attributes('src'))).toEqual(['/short-thumb/video/11', '/short-thumb/image/12', '/short-thumb/video/13', '/short-thumb/image/14', '/short-thumb/video/15']);
    expect(tiles()[0].get('img').attributes('loading')).toBe('lazy');
    expect(tiles()[0].get('.favorite-tile__badge').text()).toBe('02:41');
    expect(tiles()[1].find('.favorite-tile__badge').exists()).toBe(false);
    await wrapper.findAll('[data-test="short-feed-favorites-kind"] [role="radio"]')[2].trigger('click');
    expect(tiles().map(tile => tile.attributes('aria-label'))).toEqual(['IMG_12.jpg，图片', 'IMG_14.jpg，图片']);
    expect(wrapper.get('[data-test="short-feed-favorites-count"]').text()).toBe('2 个 · 最近收藏在前');
    wrapper.unmount();
  });

  it('点一张从这一张开始，只在墙上筛出的列表里走，两端停住，顶栏「‹ 收藏」与 n / N', async () => {
    const wrapper = await openWall();
    await wrapper.findAll('[data-test="short-feed-favorites-kind"] [role="radio"]')[1].trigger('click');
    await wrapper.findAll('.favorite-tile')[1].trigger('click');
    await flushPromises();
    const nextCalls = callsTo('/short-api/feed/next').length;
    expect(wrapper.vm.currentVideo.id).toBe(13);
    expect(wrapper.get('[data-test="short-feed-favorites-position"]').text()).toBe('2 / 3');
    expect(wrapper.get('[data-test="short-feed-favorites-back"]').text()).toBe('收藏');
    expect(wrapper.find('.feed-tabs').exists()).toBe(false);
    await wrapper.vm.nextVideo(1);
    expect(wrapper.vm.currentVideo.id).toBe(15);
    await wrapper.vm.nextVideo(1);
    expect(wrapper.vm.currentVideo.id).toBe(15);
    await wrapper.vm.nextVideo(-1);
    await wrapper.vm.nextVideo(-1);
    await wrapper.vm.nextVideo(-1);
    await flushPromises();
    expect(wrapper.vm.currentVideo.id).toBe(11);
    expect(wrapper.get('[data-test="short-feed-favorites-position"]').text()).toBe('1 / 3');
    expect(callsTo('/short-api/feed/next')).toHaveLength(nextCalls);
    // 收藏流不改推荐流的排除集。
    expect(wrapper.vm.recentKeys).toEqual(['video:1']);
    wrapper.unmount();
  });

  it('在里面取消收藏：这一条留到划走为止，划走后从流里拿掉；返回墙时重新取', async () => {
    const wrapper = await openWall();
    await wrapper.findAll('.favorite-tile')[0].trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="short-feed-favorite"]').trigger('click');
    await flushPromises();
    expect(callsTo('/favorite').at(-1).body).toEqual({ favorited: false });
    expect(wrapper.vm.currentVideo.id).toBe(11);
    expect(wrapper.get('[data-test="short-feed-favorites-position"]').text()).toBe('1 / 5');
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    expect(wrapper.vm.currentVideo.id).toBe(12);
    expect(wrapper.get('[data-test="short-feed-favorites-position"]').text()).toBe('1 / 4');
    await wrapper.vm.nextVideo(-1);
    expect(wrapper.vm.currentVideo.id).toBe(12);

    const before = callsTo('/short-api/favorites').length;
    await wrapper.get('[data-test="short-feed-favorites-back"]').trigger('click');
    await flushPromises();
    expect(callsTo('/short-api/favorites')).toHaveLength(before + 1);
    expect(wrapper.find('.favorites-view').exists()).toBe(true);
    wrapper.unmount();
  });

  it('在里面移到废纸篓：从列表拿掉并前进；删光了回到墙上', async () => {
    server({ favorites: [video(11, { favorited: true }), video(13, { favorited: true })] });
    const wrapper = await mountApp();
    await wrapper.get('[data-test="short-feed-tab-favorites"]').trigger('click');
    await flushPromises();
    await wrapper.findAll('.favorite-tile')[0].trigger('click');
    await flushPromises();
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    expect(wrapper.vm.currentVideo.id).toBe(13);
    expect(wrapper.get('[data-test="short-feed-favorites-position"]').text()).toBe('1 / 1');
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    expect(wrapper.find('.favorites-view').exists()).toBe(true);
    // 推荐流没受影响。
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1]);
    wrapper.unmount();
  });

  it('在收藏里删掉的正是推荐流停着的最后一条：切回推荐停在它前一条，不是空舞台', async () => {
    server({ next: [video(1), video(2)], favorites: [video(2, { favorited: true }), video(13, { favorited: true })] });
    const wrapper = await mountApp();
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1, 2]);
    await wrapper.get('[data-test="short-feed-tab-favorites"]').trigger('click');
    await flushPromises();
    await wrapper.findAll('.favorite-tile')[0].trigger('click');
    await flushPromises();
    expect(wrapper.vm.currentVideo.id).toBe(2);
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    await wrapper.get('[data-test="short-feed-favorites-back"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="short-feed-tab-feed"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1]);
    expect(wrapper.vm.currentVideo.id).toBe(1);
    expect(wrapper.get('video.feed-video').attributes('src')).toBe('/short-media/video/1');
    expect(wrapper.find('[data-test="short-feed-more"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('删除请求还在路上时回到墙上：删除提交前发出的收藏请求晚回来、还带着它，也不让它回到墙上；之后的请求以服务端为准', async () => {
    let finishDelete;
    let releaseFavorites = null;
    let favoritesNow = [video(11, { favorited: true }), video(13, { favorited: true })];
    server({ handlers: {
      '/short-api/items/video/11/delete': () => new Promise(resolve => { finishDelete = () => resolve(jsonResponse(200, {})); }),
      '/short-api/favorites': () => {
        const snapshot = favoritesNow;
        if (!releaseFavorites) return jsonResponse(200, { items: snapshot });
        return new Promise(resolve => { const release = releaseFavorites; releaseFavorites = () => { resolve(jsonResponse(200, { items: snapshot })); release(); }; });
      }
    } });
    const wrapper = await mountApp();
    const tileIDs = () => wrapper.findAll('.favorite-tile').map(tile => tile.get('img').attributes('src'));
    await wrapper.get('[data-test="short-feed-tab-favorites"]').trigger('click');
    await flushPromises();
    await wrapper.findAll('.favorite-tile')[0].trigger('click');
    await flushPromises();
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-delete"]'));
    // 删除还没提交就点「‹ 收藏」：这次取到的是删除前的列表（还有 11），而且它比删除晚到。
    releaseFavorites = () => {};
    await wrapper.get('[data-test="short-feed-favorites-back"]').trigger('click');
    await flushPromises();
    favoritesNow = [video(13, { favorited: true })];
    finishDelete();
    await flushPromises();
    releaseFavorites();
    releaseFavorites = null;
    await flushPromises();
    expect(tileIDs()).toEqual(['/short-thumb/video/13']);

    // 之后在电脑端恢复并重新收藏了 11：删除之后发出的请求照常显示它。
    favoritesNow = [video(11, { favorited: true }), video(13, { favorited: true })];
    await wrapper.get('[data-test="short-feed-favorites-refresh"]').trigger('click');
    await flushPromises();
    expect(tileIDs()).toEqual(['/short-thumb/video/11', '/short-thumb/video/13']);
    wrapper.unmount();
  });

  it('在收藏里评分：服务端回的整条换掉当前这一条，墙上同一条也跟着换', async () => {
    const wrapper = await openWall({ handlers: { '/short-api/items/video/13/rating': call => jsonResponse(200, video(13, { favorited: true, duration: 48, personal_rating: call.body.rating })) } });
    await wrapper.findAll('.favorite-tile')[2].trigger('click');
    await flushPromises();
    await click((await openMore(wrapper)).querySelector('[data-test="short-feed-more-rating"]'));
    await click(body().querySelector('[data-test="short-feed-rating-star-8"]'));
    await click(body().querySelector('[data-test="short-feed-rating-done"]'));
    expect(wrapper.get('.feed-meta__line').text()).toBe('视频 · 00:48 · 评分 8.0');
    expect(wrapper.vm.favorites.find(item => item.id === 13).personal_rating).toBe(8);
    wrapper.unmount();
  });

  it('没有收藏：说清怎么收藏', async () => {
    server({ favorites: [] });
    const wrapper = await mountApp();
    await wrapper.get('[data-test="short-feed-tab-favorites"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="short-feed-favorites-empty"]').text()).toBe('还没有收藏刷的时候点右侧书签就能收藏');
    wrapper.unmount();
  });
});

describe('D-MW-07 范围面板与空范围', () => {
  it('「刷什么」：类型三段、五个范围带条数与勾、当前范围的格式说明；选了就关面板按新范围取', async () => {
    server();
    const wrapper = await mountApp();
    await wrapper.get('[data-test="short-feed-scope"]').trigger('click');
    await flushPromises();
    const sheet = body().querySelector('[role="dialog"][aria-label="播放范围"]');
    expect(sheet.querySelector('h2').textContent).toBe('刷什么');
    expect([...sheet.querySelectorAll('[data-test="short-feed-media-kind"] [role="radio"]')].map(node => node.textContent.trim())).toEqual(['视频和图片', '只看视频', '只看图片']);
    const rows = [...sheet.querySelectorAll('.scope-option')];
    expect(rows.map(row => row.querySelector('.scope-option__name').textContent)).toEqual(['全部', '没看过', '只在收藏里随机', '最近 30 天添加', '还没打标签']);
    expect(rows[0].querySelector('.scope-option__count').textContent).toBe('1,284');
    expect(rows.map(row => row.getAttribute('aria-checked'))).toEqual(['true', 'false', 'false', 'false', 'false']);
    expect(sheet.querySelector('[data-test="short-feed-unplayable-all"]').textContent).toContain('另有 23 个视频手机放不了');
    await click(rows[1]);
    expect(body().querySelector('.sheet-layer')).toBeNull();
    expect(callsTo('/short-api/feed/next').at(-1).url).toContain('scope=unwatched');
    wrapper.unmount();
  });

  it('范围里没有（404）：整屏空态，不给重试条；「换个范围」打开面板', async () => {
    server({ next: [] });
    const wrapper = await mountApp();
    const empty = wrapper.get('[data-test="short-feed-empty-scope"]');
    expect(empty.text()).toContain('这个范围里没有能播的');
    expect(empty.text()).toContain('「全部」里的 23 个视频手机都放不了。在电脑端为它们生成兼容副本，或者换个范围。');
    expect(wrapper.find('[data-test="short-feed-retry-bar"]').exists()).toBe(false);
    expect(wrapper.vm.statusText).toBe('');
    await wrapper.get('[data-test="short-feed-change-scope"]').trigger('click');
    await flushPromises();
    expect(body().querySelector('[role="dialog"][aria-label="播放范围"]')).not.toBeNull();
    wrapper.unmount();
  });

  it('范围里只有放不了的视频（服务端回一条说明）：同样是空态，不插进历史', async () => {
    server({ next: [video(9, { media_url: '', reason_code: 'inline_not_supported', reason_message: '当前文件格式不适合浏览器内播放。' })] });
    const wrapper = await mountApp();
    expect(wrapper.find('[data-test="short-feed-empty-scope"]').exists()).toBe(true);
    expect(wrapper.vm.items).toEqual([]);
    wrapper.unmount();
  });

  it('取下一条失败仍是底部横幅「没取到下一条」+ 原因 + 重试，历史保留', async () => {
    server({ next: [video(1), new TypeError('Failed to fetch'), new TypeError('Failed to fetch')] });
    const wrapper = await mountApp();
    wrapper.vm.prefetchedVideo = null;
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    const bar = wrapper.get('[data-test="short-feed-retry-bar"]');
    expect(bar.attributes('role')).toBe('alert');
    expect(bar.text()).toContain('没取到下一条');
    expect(bar.text()).toContain('网络异常');
    expect(wrapper.vm.items.map(item => item.id)).toEqual([1]);
    expect(wrapper.find('[data-test="short-feed-empty-scope"]').exists()).toBe(false);
    wrapper.unmount();
  });
});

describe('PIN 页（设计 §6）', () => {
  it('默认遮住，点眼睛显示，再点遮回', async () => {
    route = () => jsonResponse(401, { code: 'pin_required', message: '需要输入 PIN' });
    const wrapper = await mountApp();
    const input = wrapper.get('[data-test="short-feed-pin-input"]');
    const reveal = wrapper.get('[data-test="short-feed-pin-reveal"]');
    expect(input.attributes('type')).toBe('password');
    expect(reveal.attributes('aria-label')).toBe('显示 PIN');
    await reveal.trigger('click');
    expect(input.attributes('type')).toBe('text');
    expect(reveal.attributes('aria-label')).toBe('隐藏 PIN');
    await reveal.trigger('click');
    expect(input.attributes('type')).toBe('password');
    wrapper.unmount();
  });
});
