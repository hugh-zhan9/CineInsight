import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ShortFeedApp from '../short-feed/ShortFeedApp.vue';

// 手机网页与设计稿的两处偏差（2026-10-14 遗留批次 G4）：标签显示用户给的颜色（与桌面同一份换算
// utils/tagColor.js），图片信息行带日期。与 ShortFeedRedesign 一样走真实的 api.js，只在 fetch 这一层模拟服务端。

const tag = (id, name, extra = {}) => ({ id, name, color: '#3b82f6', automatic: false, ...extra });
const video = (id, extra = {}) => ({ media_kind: 'video', id, name: `clip-${id}.mp4`, media_url: `/short-media/video/${id}`, width: 1080, height: 1920, duration: 20, tags: [], liked: false, favorited: false, personal_rating: null, watched: false, ...extra });
const image = (id, extra = {}) => ({ media_kind: 'image', id, name: `IMG_${id}.jpg`, media_url: `/short-media/image/${id}`, width: 4032, height: 3024, duration: 0, tags: [], liked: false, favorited: false, personal_rating: null, watched: false, ...extra });

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, statusText: '', headers: { get: () => 'application/json' }, json: async () => body };
}

let route;
beforeEach(() => {
  route = () => jsonResponse(404, { code: 'not_found', message: 'not found' });
  vi.stubGlobal('fetch', vi.fn(async (url, options = {}) => route({ url: String(url), method: options.method || 'GET' })));
  HTMLMediaElement.prototype.play = vi.fn(() => Promise.resolve());
  HTMLMediaElement.prototype.pause = vi.fn();
  Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, value: false });
  const store = new Map();
  vi.stubGlobal('localStorage', { getItem: key => store.get(key) ?? null, setItem: (key, value) => store.set(key, String(value)), clear: () => store.clear() });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = '';
});

function server({ next, tags = [] }) {
  const queue = [...next];
  route = call => {
    if (call.url.startsWith('/short-api/feed/next')) {
      const entry = queue.shift();
      return entry ? jsonResponse(200, entry) : jsonResponse(404, { code: 'no_eligible_videos', message: '没有可播放的内容' });
    }
    if (call.url.startsWith('/short-api/feed/scopes')) return jsonResponse(200, { scopes: [] });
    if (call.url.startsWith('/short-api/tags')) return jsonResponse(200, { tags });
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

const background = element => element.style.backgroundColor;

describe('标签颜色是用户数据：舞台标签行与标签面板（已添加）照桌面的换算上色', () => {
  it('舞台标签行每个标签用自己的颜色，没设颜色的保持中性，「＋ 标签」不上色', async () => {
    server({ next: [video(1, { tags: [tag(1, '旅行'), tag(2, '海边', { color: '#ef4444' }), tag(3, '延时', { color: '' })] })] });
    const wrapper = await mountApp();
    const chips = wrapper.findAll('.tag-row span.feed-tag');
    expect(chips.map(chip => chip.text())).toEqual(['旅行', '海边', '延时']);
    expect(chips.map(chip => background(chip.element))).toEqual(['rgba(59, 130, 246, 0.35)', 'rgba(239, 68, 68, 0.35)', '']);
    expect(background(wrapper.get('.feed-tag--add').element)).toBe('');
    wrapper.unmount();
  });

  it('标签面板：已添加的带自己的颜色，没添加的保持中性，自动标签不上色、仍是只读虚线', async () => {
    const travel = tag(1, '旅行');
    const action = tag(2, '动作', { color: '#ef4444' });
    const short = tag(3, '短视频', { color: '#22c55e', automatic: true });
    const plain = tag(4, '海边', { color: '' });
    server({ next: [video(1, { tags: [travel, short, plain] })], tags: [travel, action, short, plain] });
    const wrapper = await mountApp();
    await wrapper.vm.openTagSheet();
    await flushPromises();

    const groups = document.body.querySelectorAll('.tag-group');
    const attached = [...groups[0].querySelectorAll('.tag-option')];
    const others = [...groups[1].querySelectorAll('.tag-option')];
    const byName = list => Object.fromEntries(list.map(option => [option.textContent.replace('自动', '').trim(), option]));
    const attachedByName = byName(attached);
    expect(Object.keys(attachedByName)).toEqual(['旅行', '短视频', '海边']);
    expect(background(attachedByName['旅行'])).toBe('rgba(59, 130, 246, 0.35)');
    expect(background(attachedByName['海边'])).toBe('');
    expect(background(attachedByName['短视频'])).toBe('');
    expect(attachedByName['短视频'].classList.contains('tag-option--auto')).toBe(true);
    expect(attachedByName['短视频'].disabled).toBe(true);
    expect(attached.every(option => option.classList.contains('active'))).toBe(true);

    expect(others.map(option => option.textContent.trim())).toEqual(['动作']);
    expect(background(others[0])).toBe('');
    expect(others[0].classList.contains('active')).toBe(false);
    wrapper.unmount();
  });
});

describe('图片信息行带日期（设计稿「图片 · 4032 × 3024 · 2026-09-18」）', () => {
  it('图片写本地时区的 YYYY-MM-DD，排在尺寸之后、评分之前；视频照旧不写日期', async () => {
    // 本地 0:30：东八区的 UTC 时间还在前一天，直接截 ISO 串的前十位会写成 09-17。
    const takenAt = new Date(2026, 8, 18, 0, 30).toISOString();
    server({ next: [image(7, { taken_at: takenAt }), image(8, { taken_at: takenAt, personal_rating: 9 }), video(9)] });
    const wrapper = await mountApp();
    expect(wrapper.get('.feed-meta__line').text()).toBe('图片 · 4032 × 3024 · 2026-09-18');
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    expect(wrapper.get('.feed-meta__line').text()).toBe('图片 · 4032 × 3024 · 2026-09-18 · 评分 9.0');
    await wrapper.vm.nextVideo(1);
    await flushPromises();
    expect(wrapper.get('.feed-meta__line').text()).toBe('视频 · 00:20');
    wrapper.unmount();
  });
});
