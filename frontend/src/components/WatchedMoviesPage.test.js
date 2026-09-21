import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// api 里额外放了一个 ListMovieChart：本页**不该**碰榜单缓存，它在这里的唯一作用是
// 「一旦被调用就炸」——已看页在缓存清空后仍要完整可用（D-MC05 快照列存在的理由）。
const api = vi.hoisted(() => Object.fromEntries([
  'ListWatchedMovies', 'ListMovieChart'
].map(name => [name, vi.fn()])));
vi.mock('../../wailsjs/go/main/App', () => api);

import WatchedMoviesPage from './WatchedMoviesPage.vue';

// vitest 的根就是 frontend/，jsdom 环境下 import.meta.url 不是 file: 协议。
const SOURCE = readFileSync(resolve(process.cwd(), 'src/components/WatchedMoviesPage.vue'), 'utf8');

const movie = (doubanID, extra = {}) => ({
  douban_id: doubanID,
  title: `片名 ${doubanID}`,
  has_poster: false,
  marked_at: '2026-09-20T10:00:00Z',
  ...extra
});
const group = (year, items) => ({ year, items });

const wrappers = [];
const deferred = () => {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
};

beforeEach(() => {
  vi.resetAllMocks();
  api.ListWatchedMovies.mockResolvedValue([]);
  api.ListMovieChart.mockRejectedValue(new Error('已看页不该读榜单缓存'));
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
});

async function page() {
  const wrapper = mount(WatchedMoviesPage);
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}
const find = (wrapper, name) => wrapper.get(`[data-test="watched-movies-${name}"]`);
const findAll = (wrapper, name) => wrapper.findAll(`[data-test="watched-movies-${name}"]`);
const texts = (wrapper, name) => findAll(wrapper, name).map(node => node.text());

describe('只显示榜单标记，不掺主片库的已看', () => {
  // 这条守的是 D-MC11 的「不合并 videos.is_watched」。后端根本没给出能把两者混起来
  // 的绑定，所以真正的风险是后来的人以为它们本来就是混的，顺手加一个。
  it('渲染的条目与 ListWatchedMovies 的返回值逐条相同，一条不多', async () => {
    api.ListWatchedMovies.mockResolvedValue([
      group(2026, [movie('1'), movie('2')]),
      group(2019, [movie('3')])
    ]);
    const wrapper = await page();

    expect(texts(wrapper, 'title')).toEqual(['片名 1', '片名 2', '片名 3']);
    expect(findAll(wrapper, 'item')).toHaveLength(3);
    expect(find(wrapper, 'summary').text()).toBe('共 3 部');
  });

  it('只从绑定层导入 ListWatchedMovies，且不接受任何 props', () => {
    const imported = [...SOURCE.matchAll(/import\s*\{([^}]*)\}\s*from\s*'[^']*wailsjs\/go\/main\/App'/g)]
      .flatMap(match => match[1].split(',').map(name => name.trim()).filter(Boolean));

    expect(imported).toEqual(['ListWatchedMovies']);
    // 没有 props ⇒ 父组件也喂不进来片库侧的数据，两条路径一起堵上。
    expect(WatchedMoviesPage.props).toBeUndefined();
  });
});

describe('榜单缓存被清空后仍然完整', () => {
  // 片名与海报都来自标记行的快照列，页面一次都不回头查缓存表：缓存整年重建、
  // 条目从豆瓣下架之后，这一页照样有内容。
  it('载荷里的条目在缓存中没有对应行，照样显示片名与海报，且不读榜单缓存', async () => {
    api.ListWatchedMovies.mockResolvedValue([
      group(2011, [movie('26387939', { title: '一部已经下架的片', has_poster: true })])
    ]);
    const wrapper = await page();

    expect(find(wrapper, 'title').text()).toBe('一部已经下架的片');
    expect(find(wrapper, 'poster').attributes('src')).toBe('/preview/douban-chart-poster/26387939');
    expect(api.ListMovieChart).not.toHaveBeenCalled();
    expect(api.ListWatchedMovies).toHaveBeenCalledTimes(1);
    expect(findAll(wrapper, 'error')).toHaveLength(0);
  });
});

describe('分组与组内顺序原样渲染', () => {
  // 后端已经排好序并分好组（services/movie_chart_query.go 的 ListWatched），
  // 前端**不得**重排。载荷故意给一个「没排好」的年份顺序：一旦有人在前端补一次
  // 排序，这条就会挂。
  it('组的先后完全跟随载荷，不在前端重排', async () => {
    api.ListWatchedMovies.mockResolvedValue([
      group(1999, [movie('a')]),
      group(2026, [movie('b')]),
      group(0, [movie('c')])
    ]);
    const wrapper = await page();

    expect(texts(wrapper, 'group-title')).toEqual(['1999 年', '2026 年', '年份未知']);
    expect(texts(wrapper, 'title')).toEqual(['片名 a', '片名 b', '片名 c']);
  });

  it('组内顺序也完全跟随载荷（后端按标记时间倒序）', async () => {
    api.ListWatchedMovies.mockResolvedValue([
      group(2026, [
        movie('old', { marked_at: '2026-01-01T08:00:00Z' }),
        movie('new', { marked_at: '2026-09-01T08:00:00Z' })
      ])
    ]);
    const wrapper = await page();

    // 按标记时间倒序重排会把 new 提到前面，这里必须仍是载荷顺序。
    expect(texts(wrapper, 'title')).toEqual(['片名 old', '片名 new']);
  });

  it('release_year 为 0 单列「年份未知」一组，既不显示成「0 年」也不丢条目', async () => {
    api.ListWatchedMovies.mockResolvedValue([
      group(2026, [movie('a')]),
      group(0, [movie('b'), movie('c')])
    ]);
    const wrapper = await page();

    const titles = texts(wrapper, 'group-title');
    expect(titles).toEqual(['2026 年', '年份未知']);
    expect(titles.some(text => text.includes('0 年'))).toBe(false);
    expect(findAll(wrapper, 'group')[1].findAll('[data-test="watched-movies-item"]')).toHaveLength(2);
  });

  it('标记时间不可解析时不显示「标记于」，片名照常在', async () => {
    api.ListWatchedMovies.mockResolvedValue([group(2026, [movie('a', { marked_at: '不是时间' })])]);
    const wrapper = await page();

    expect(find(wrapper, 'title').text()).toBe('片名 a');
    expect(findAll(wrapper, 'marked-at')).toHaveLength(0);
  });
});

describe('空态与失败是两回事', () => {
  it('一条都没标过是普通空态，不报错', async () => {
    api.ListWatchedMovies.mockResolvedValue([]);
    const wrapper = await page();

    expect(find(wrapper, 'empty').text()).toBe('还没有标记过已看');
    expect(findAll(wrapper, 'error')).toHaveLength(0);
    expect(wrapper.findAll('[role="alert"]')).toHaveLength(0);
    expect(find(wrapper, 'summary').text()).toBe('共 0 部');
  });

  it('后端返回 null 也按空态处理', async () => {
    api.ListWatchedMovies.mockResolvedValue(null);
    const wrapper = await page();

    expect(find(wrapper, 'empty').exists()).toBe(true);
    expect(findAll(wrapper, 'error')).toHaveLength(0);
  });

  it('读失败显示原因，且不伪装成「还没有标记过已看」', async () => {
    api.ListWatchedMovies.mockRejectedValue(new Error('数据库连接失败'));
    const wrapper = await page();

    expect(find(wrapper, 'error').text()).toContain('数据库连接失败');
    expect(findAll(wrapper, 'empty')).toHaveLength(0);
  });

  it('重新读取成功后清掉上一次的错误', async () => {
    api.ListWatchedMovies.mockRejectedValueOnce(new Error('数据库连接失败'));
    const wrapper = await page();
    expect(findAll(wrapper, 'error')).toHaveLength(1);

    api.ListWatchedMovies.mockResolvedValue([group(2026, [movie('a')])]);
    await find(wrapper, 'reload').trigger('click');
    await flushPromises();

    expect(findAll(wrapper, 'error')).toHaveLength(0);
    expect(texts(wrapper, 'title')).toEqual(['片名 a']);
  });
});

describe('海报', () => {
  it('只有 has_poster 为真的条目才发代理请求，地址永远是本地代理路由', async () => {
    api.ListWatchedMovies.mockResolvedValue([
      group(2026, [movie('11', { has_poster: true }), movie('22', { has_poster: false })])
    ]);
    const wrapper = await page();

    const posters = findAll(wrapper, 'poster');
    expect(posters).toHaveLength(1);
    const src = posters[0].attributes('src');
    expect(src).toBe('/preview/douban-chart-poster/11');
    expect(src).not.toContain('doubanio');
    expect(src).not.toContain('http');
  });

  it('海报加载失败就地隐藏，不留破图', async () => {
    api.ListWatchedMovies.mockResolvedValue([group(2026, [movie('11', { has_poster: true })])]);
    const wrapper = await page();
    expect(findAll(wrapper, 'poster')).toHaveLength(1);

    await find(wrapper, 'poster').trigger('error');

    expect(findAll(wrapper, 'poster')).toHaveLength(0);
    expect(find(wrapper, 'title').exists()).toBe(true);
  });
});

describe('重新读取', () => {
  it('读取期间按钮禁用，读完恢复', async () => {
    const pending = deferred();
    api.ListWatchedMovies.mockReturnValue(pending.promise);
    const wrapper = mount(WatchedMoviesPage);
    wrappers.push(wrapper);
    await flushPromises();

    expect(find(wrapper, 'reload').attributes('disabled')).toBeDefined();

    pending.resolve([group(2026, [movie('a')])]);
    await flushPromises();
    expect(find(wrapper, 'reload').attributes('disabled')).toBeUndefined();

    api.ListWatchedMovies.mockResolvedValue([group(2026, [movie('a'), movie('b')])]);
    await find(wrapper, 'reload').trigger('click');
    await flushPromises();
    expect(api.ListWatchedMovies).toHaveBeenCalledTimes(2);
    expect(findAll(wrapper, 'item')).toHaveLength(2);
  });

  // 读取期间按钮是禁用的，所以今天这条路径只能由代码触发（未来多一个触发点就会走到）。
  // 这里直接调 load()，钉住「谁后发起、谁说了算」这条，而不是靠界面制造并发。
  it('后发先至的响应被丢弃，界面留在最后一次请求的结果上', async () => {
    const first = deferred();
    api.ListWatchedMovies.mockReturnValueOnce(first.promise);
    const wrapper = mount(WatchedMoviesPage);
    wrappers.push(wrapper);
    await flushPromises();

    api.ListWatchedMovies.mockResolvedValue([group(2026, [movie('new')])]);
    await wrapper.vm.load();
    await flushPromises();
    expect(texts(wrapper, 'title')).toEqual(['片名 new']);

    // 第一次请求这时才回来，且带着一份陈旧数据。
    first.resolve([group(1999, [movie('old')])]);
    await flushPromises();

    expect(texts(wrapper, 'title')).toEqual(['片名 new']);
  });

  it('卸载之后到达的响应不再写回组件', async () => {
    const pending = deferred();
    api.ListWatchedMovies.mockReturnValue(pending.promise);
    const wrapper = mount(WatchedMoviesPage);
    await flushPromises();

    wrapper.unmount();
    pending.resolve([group(2026, [movie('a')])]);
    await flushPromises();

    expect(wrapper.vm.groups).toEqual([]);
  });
});
