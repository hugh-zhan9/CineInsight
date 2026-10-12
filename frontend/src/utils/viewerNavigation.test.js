import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../wailsjs/go/main/App', () => ({}));

import {
  DETAIL_RETURN_LIMIT, opensVideoDetail, popDetailReturn, pushDetailReturn, restoreScrollWhenReachable, SCROLL_RESTORE_TIMEOUT_MS
} from './viewerNavigation.js';

const detail = (videoId, scrollTop = 0) => ({ page: 'video-detail', params: { videoId }, scrollTop });
const page = (key, scrollTop = 0) => ({ page: key, params: null, scrollTop });

describe('「打开一部片」按调用方区域分流（D-UI-07）', () => {
  it.each([
    'home', 'recap', 'viewing-notes', 'scenes', 'watchlist', 'movie-chart', 'watched-movies', 'people', 'collections',
    'playback-queue', 'video-detail'
  ])('观看区调用方 %s 进整页详情', from => {
    expect(opensVideoDetail(from, 'settings')).toBe(true);
  });

  it.each([
    'library-manage', 'photos-manage', 'manage-home', 'downloads', 'video-workbench', 'insights', 'data-care', 'settings',
    'task-center'
  ])('管理区调用方 %s 仍切到片库管理开抽屉', from => {
    expect(opensVideoDetail(from, 'home')).toBe(false);
  });

  it('命令面板跟着当前页面所在的区域走', () => {
    expect(opensVideoDetail('command-palette', 'home')).toBe(true);
    expect(opensVideoDetail('command-palette', 'video-detail')).toBe(true);
    expect(opensVideoDetail('command-palette', 'videos')).toBe(true);
    expect(opensVideoDetail('command-palette', 'library-manage')).toBe(false);
    expect(opensVideoDetail('command-palette', 'settings')).toBe(false);
  });

  it('认不出或没写来源时维持现在的抽屉行为', () => {
    expect(opensVideoDetail(undefined, 'home')).toBe(false);
    expect(opensVideoDetail('', 'home')).toBe(false);
    expect(opensVideoDetail('no-such-page', 'home')).toBe(false);
  });
});

describe('整页详情的返回栈（D-UI-10）', () => {
  it('从观看页打开详情时把来源压栈，弹出时拿回来源与滚动位置', () => {
    const stack = pushDetailReturn([], page('recap', 1200), 7);
    expect(stack).toEqual([page('recap', 1200)]);

    const popped = popDetailReturn(stack);
    expect(popped.entry).toEqual(page('recap', 1200));
    expect(popped.stack).toEqual([]);
  });

  it('栈空时弹不出东西', () => {
    expect(popDetailReturn([])).toEqual({ entry: null, stack: [] });
  });

  it('相关推荐在栈上再压一层，返回逐层回去', () => {
    let stack = pushDetailReturn([], page('home', 300), 1);
    stack = pushDetailReturn(stack, detail(1, 800), 2);
    stack = pushDetailReturn(stack, detail(2, 50), 3);
    expect(stack.map(entry => entry.params?.videoId || entry.page)).toEqual(['home', 1, 2]);

    let popped = popDetailReturn(stack);
    expect(popped.entry).toEqual(detail(2, 50));
    popped = popDetailReturn(popped.stack);
    expect(popped.entry).toEqual(detail(1, 800));
    popped = popDetailReturn(popped.stack);
    expect(popped.entry).toEqual(page('home', 300));
    expect(popped.stack).toEqual([]);
  });

  it('同一部片不重复压栈：点自己不动，回到栈里已有的片就退回到它那一层', () => {
    const base = pushDetailReturn(pushDetailReturn([], page('home'), 1), detail(1, 400), 2);
    // 在 2 的详情里点 2：栈不变。
    expect(pushDetailReturn(base, detail(2), 2)).toBe(base);
    // 1 → 2 → 3 之后再点 1：退回到打开 1 之前，返回就回首页。
    const deeper = pushDetailReturn(base, detail(2), 3);
    const back = pushDetailReturn(deeper, detail(3), 1);
    expect(back).toEqual([page('home')]);
    expect(back.filter(entry => entry.params?.videoId === 1)).toEqual([]);
  });

  it(`最多 ${DETAIL_RETURN_LIMIT} 层，超出丢最早的`, () => {
    let stack = pushDetailReturn([], page('home'), 1);
    for (let id = 1; id < DETAIL_RETURN_LIMIT; id++) stack = pushDetailReturn(stack, detail(id), id + 1);
    expect(stack).toHaveLength(DETAIL_RETURN_LIMIT);
    expect(stack[0]).toEqual(page('home'));

    stack = pushDetailReturn(stack, detail(DETAIL_RETURN_LIMIT), DETAIL_RETURN_LIMIT + 1);
    expect(stack).toHaveLength(DETAIL_RETURN_LIMIT);
    expect(stack[0]).toEqual(detail(1));
    expect(stack[stack.length - 1]).toEqual(detail(DETAIL_RETURN_LIMIT));
  });

  it('管理区的来源不压栈（没有可回的观看页）', () => {
    expect(pushDetailReturn([], page('settings'), 1)).toEqual([]);
    expect(pushDetailReturn([], page('library-manage'), 1)).toEqual([]);
  });
});

describe('restoreScrollWhenReachable（I-2）', () => {
  beforeEach(() => vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame', 'Date'] }));
  afterEach(() => vi.useRealTimers());

  const container = height => ({ scrollHeight: height, clientHeight: 600, scrollTop: 0 });

  it('内容已经够高时立刻放回', () => {
    const el = container(3000);
    restoreScrollWhenReachable(el, 2400);
    expect(el.scrollTop).toBe(2400);
  });

  it('目标为 0 时直接放回 0', () => {
    const el = container(0);
    el.scrollTop = 50;
    restoreScrollWhenReachable(el, 0);
    expect(el.scrollTop).toBe(0);
  });

  it('等内容长到 scrollHeight - clientHeight ≥ 目标才放回，正好够也算', () => {
    const el = container(600);
    restoreScrollWhenReachable(el, 2400);
    vi.advanceTimersByTime(100);
    el.scrollHeight = 2999;
    vi.advanceTimersByTime(32);
    expect(el.scrollTop).toBe(0);
    el.scrollHeight = 3000;
    vi.advanceTimersByTime(32);
    expect(el.scrollTop).toBe(2400);
  });

  it(`超过 ${SCROLL_RESTORE_TIMEOUT_MS} 毫秒就放弃`, () => {
    const el = container(600);
    restoreScrollWhenReachable(el, 2400);
    vi.advanceTimersByTime(SCROLL_RESTORE_TIMEOUT_MS + 100);
    el.scrollHeight = 9000;
    vi.advanceTimersByTime(200);
    expect(el.scrollTop).toBe(0);
  });

  it('取消后不再放回', () => {
    const el = container(600);
    const cancel = restoreScrollWhenReachable(el, 2400);
    vi.advanceTimersByTime(50);
    cancel();
    el.scrollHeight = 9000;
    vi.advanceTimersByTime(200);
    expect(el.scrollTop).toBe(0);
  });
});
