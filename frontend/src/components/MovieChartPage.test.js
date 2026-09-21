import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ListMovieChart', 'ListMovieChartYears', 'OpenMovieChartYear', 'RefreshMovieChart',
  'CancelMovieChartRefresh', 'MarkMovieChartEntry', 'ClearMovieChartMark'
].map(name => [name, vi.fn()])));
const feedback = vi.hoisted(() => ({ notifySuccess: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', () => feedback);

import MovieChartPage from './MovieChartPage.vue';

const CURRENT_YEAR = new Date().getFullYear();

// 六类分类码一个不少。credential_* 在豆瓣榜单源上永不产生，但它们仍是后端契约的
// 一部分，界面必须各有各的说法——这条列表就是「不合并」的清单。
const FAILURE_CODES = [
  'credential_missing', 'credential_invalid', 'proxy_unreachable',
  'network_unreachable', 'not_found', 'source_error'
];

const item = (doubanID, extra = {}) => ({
  douban_id: doubanID,
  title: `片名 ${doubanID}`,
  original_title: '',
  card_subtitle: '',
  release_date: '2026-03-20',
  release_scope: 'theatrical',
  rating: 7.5,
  rating_count: 120,
  has_poster: false,
  mark: '',
  detail_status: 'succeeded',
  detail_error: '',
  ...extra
});

const chartPage = (extra = {}) => ({
  year: CURRENT_YEAR,
  sort: 'release',
  page: 1,
  page_size: 20,
  total: 0,
  items: [],
  cache: { last_refreshed_at: null, last_failure: '', refreshing: false },
  backfill: { pending: 0, total: 0 },
  ...extra
});

const wrappers = [];
const deferred = () => {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
};

beforeEach(() => {
  vi.resetAllMocks();
  api.ListMovieChartYears.mockResolvedValue([CURRENT_YEAR, CURRENT_YEAR - 1]);
  api.OpenMovieChartYear.mockResolvedValue(false);
  api.ListMovieChart.mockResolvedValue(chartPage());
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  vi.useRealTimers();
});

async function page() {
  const wrapper = mount(MovieChartPage);
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}
const find = (wrapper, name) => wrapper.get(`[data-test="movie-chart-${name}"]`);
const findAll = (wrapper, name) => wrapper.findAll(`[data-test="movie-chart-${name}"]`);

describe('OpenMovieChartYear 的调用点', () => {
  // 这条守的是「取消之后被读接口原样重启」那个两轮评审才找出来的缺陷：
  // 触发点只能是挂载与切年份，翻页 / 切排序 / 切不过滤 / 刷新 / 取消 / 轮询都不算。
  it('挂载时报一次，切年份再报一次，其余交互一次都不报', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ total: 60, items: [item('1')] }));
    const wrapper = await page();

    expect(api.OpenMovieChartYear.mock.calls).toEqual([[CURRENT_YEAR]]);

    await find(wrapper, 'year').setValue(String(CURRENT_YEAR - 1));
    await flushPromises();
    expect(api.OpenMovieChartYear.mock.calls).toEqual([[CURRENT_YEAR], [CURRENT_YEAR - 1]]);

    api.ListMovieChart.mockResolvedValue(chartPage({ year: CURRENT_YEAR - 1, sort: 'rating', total: 60, items: [item('1')] }));
    await find(wrapper, 'sort-rating').trigger('click');
    await flushPromises();
    api.ListMovieChart.mockResolvedValue(chartPage({ year: CURRENT_YEAR - 1, sort: 'rating', page: 2, total: 60, items: [item('2')] }));
    await find(wrapper, 'next').trigger('click');
    await flushPromises();
    await find(wrapper, 'show-marked').setValue(true);
    await flushPromises();
    api.RefreshMovieChart.mockResolvedValue(undefined);
    await find(wrapper, 'refresh').trigger('click');
    await flushPromises();

    expect(api.ListMovieChart.mock.calls.length).toBeGreaterThan(4);
    expect(api.OpenMovieChartYear.mock.calls).toEqual([[CURRENT_YEAR], [CURRENT_YEAR - 1]]);
  });

  it('取消刷新之后不会有任何一次 OpenMovieChartYear 把这一轮重启', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({
      items: [item('1')], total: 1,
      cache: { last_refreshed_at: null, last_failure: '', refreshing: true }
    }));
    const wrapper = await page();
    api.OpenMovieChartYear.mockClear();
    api.CancelMovieChartRefresh.mockResolvedValue(undefined);

    await find(wrapper, 'cancel').trigger('click');
    await flushPromises();

    expect(api.CancelMovieChartRefresh).toHaveBeenCalledTimes(1);
    expect(api.OpenMovieChartYear).not.toHaveBeenCalled();
  });

  it('OpenMovieChartYear 失败只出提示，榜单照常读出来', async () => {
    api.OpenMovieChartYear.mockRejectedValue(new Error('年度榜单服务不可用'));
    api.ListMovieChart.mockResolvedValue(chartPage({ items: [item('1')], total: 1 }));
    const wrapper = await page();

    expect(find(wrapper, 'action-error').text()).toContain('年度榜单服务不可用');
    expect(findAll(wrapper, 'item')).toHaveLength(1);
  });
});

describe('三种缓存状态条', () => {
  it('刷新成功：只说更新于什么时候', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({
      items: [item('1')], total: 1,
      cache: { last_refreshed_at: '2026-09-20T10:00:00+08:00', last_failure: '', refreshing: false }
    }));
    const wrapper = await page();

    const status = find(wrapper, 'status').text();
    expect(status).toContain('数据更新于');
    expect(status).not.toContain('失败');
    expect(findAll(wrapper, 'item')).toHaveLength(1);
  });

  it('有缓存但上次失败：说明来自缓存、带上失败原因，**并且照常列出缓存条目**', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({
      items: [item('1'), item('2')], total: 2,
      cache: { last_refreshed_at: '2026-09-20T10:00:00+08:00', last_failure: 'network_unreachable', refreshing: false }
    }));
    const wrapper = await page();

    const status = find(wrapper, 'status').text();
    expect(status).toContain('数据来自缓存');
    expect(status).toContain('最近一次更新失败');
    expect(status).toContain('无法连接豆瓣');
    // 回落缓存这条要求的界面侧：不可达 ≠ 空榜单。
    expect(findAll(wrapper, 'item')).toHaveLength(2);
    expect(wrapper.find('[data-test="movie-chart-empty"]').exists()).toBe(false);
  });

  it('从未抓到且上次失败：空态 + 失败原因 + 刷新按钮', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({
      cache: { last_refreshed_at: null, last_failure: 'proxy_unreachable', refreshing: false }
    }));
    const wrapper = await page();

    expect(find(wrapper, 'status').text()).toContain('暂无该年数据，更新失败：');
    expect(find(wrapper, 'empty').text()).toContain('资料源出网代理不可用');
    expect(find(wrapper, 'empty-refresh').exists()).toBe(true);
  });

  it('从未抓过也没失败：不说失败，这两件事不合并', async () => {
    const wrapper = await page();

    const status = find(wrapper, 'status').text();
    expect(status).toContain('该年还没有抓取过');
    expect(status).not.toContain('失败');
  });
});

describe('六类失败分类互不合并', () => {
  async function statusFor(code) {
    const wrapper = mount(MovieChartPage);
    wrappers.push(wrapper);
    api.ListMovieChart.mockResolvedValue(chartPage({
      items: [item('1')], total: 1,
      cache: { last_refreshed_at: '2026-09-20T10:00:00+08:00', last_failure: code, refreshing: false }
    }));
    await flushPromises();
    return find(wrapper, 'status').text();
  }

  it('六类各有各的一句话，两两不同', async () => {
    const texts = [];
    for (const code of FAILURE_CODES) texts.push(await statusFor(code));
    for (const [index, code] of FAILURE_CODES.entries()) {
      expect(texts[index], `${code} 应该有自己的文案`).toContain('最近一次更新失败：');
      expect(texts[index].split('最近一次更新失败：')[1].trim()).not.toBe('');
    }
    const reasons = texts.map(text => text.split('最近一次更新失败：')[1].trim());
    expect(new Set(reasons).size).toBe(FAILURE_CODES.length);
  });

  it('凭证 / 代理 / 网络 / 源异常都不得渲染成「查无数据」式的没收录', async () => {
    for (const code of ['credential_missing', 'credential_invalid', 'proxy_unreachable', 'network_unreachable', 'source_error']) {
      const text = await statusFor(code);
      expect(text, `${code} 不能说成没收录`).not.toContain('没有收录');
      expect(text, `${code} 不能说成查无数据`).not.toContain('查无数据');
    }
    // 只有 not_found 才是「源里没有这条」。
    expect(await statusFor('not_found')).toContain('没有收录');
  });

  it('认不出来的分类码原样带出去，不让一次失败在界面上消失', async () => {
    expect(await statusFor('teapot')).toContain('teapot');
  });
});

describe('条目呈现', () => {
  it('详情未补全的条目照常显示并标「上映信息待确认」，不带错误措辞', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({
      total: 3,
      items: [
        item('1', { release_scope: 'theatrical', release_date: '2026-03-20' }),
        item('2', { release_scope: 'undetermined', release_date: '' }),
        item('3', { release_scope: '', release_date: '', detail_status: 'pending', detail_error: '' })
      ]
    }));
    const wrapper = await page();

    const releases = findAll(wrapper, 'release').map(node => node.text());
    expect(releases).toEqual(['2026-03-20 上映', '档期未定', '上映信息待确认']);
    expect(findAll(wrapper, 'item')).toHaveLength(3);
    // 待确认只是还没判定，不是失败：这一行上不该出现补全失败提示。
    expect(findAll(wrapper, 'item')[2].find('[data-test="movie-chart-detail-error"]').exists()).toBe(false);
  });

  it('内地公映但没有具体日期时按年份说明，不说成档期未定', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({
      year: CURRENT_YEAR, total: 1,
      items: [item('1', { release_scope: 'theatrical', release_date: '' })]
    }));
    const wrapper = await page();

    expect(find(wrapper, 'release').text()).toBe(`${CURRENT_YEAR}年内地上映·未定档`);
  });

  it('补全失败的条目仍标待确认，失败原因单独一行且按分类码分开', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({
      total: 1,
      items: [item('1', { release_scope: '', release_date: '', detail_status: 'failed', detail_error: 'not_found' })]
    }));
    const wrapper = await page();

    expect(find(wrapper, 'release').text()).toBe('上映信息待确认');
    expect(find(wrapper, 'detail-error').text()).toContain('豆瓣没有收录');
  });

  it('补全中 N/M 用 pending 与 total，两者归零时整条不显示', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({
      items: [item('1')], total: 1, backfill: { pending: 7, total: 150 }
    }));
    const wrapper = await page();
    expect(find(wrapper, 'backfill').text()).toContain('补全中 7/150');

    api.ListMovieChart.mockResolvedValue(chartPage({
      sort: 'rating', items: [item('1')], total: 1, backfill: { pending: 0, total: 150 }
    }));
    await find(wrapper, 'sort-rating').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="movie-chart-backfill"]').exists()).toBe(false);
  });

  it('只有 has_poster 为真才发海报请求，地址是本地代理路由而不是豆瓣图床', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({
      total: 2,
      items: [item('101', { has_poster: true }), item('202', { has_poster: false })]
    }));
    const wrapper = await page();

    const posters = findAll(wrapper, 'poster');
    expect(posters).toHaveLength(1);
    expect(posters[0].attributes('src')).toBe('/preview/douban-chart-poster/101');
    expect(wrapper.html()).not.toContain('doubanio');

    // 取图失败就地收起，不留一个破图框。
    await posters[0].trigger('error');
    expect(findAll(wrapper, 'poster')).toHaveLength(0);
  });
});

describe('「不过滤」开关', () => {
  it('只解除标记造成的隐藏：带 showMarked 重读第一页，并且界面写明不放宽公映口径', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ items: [item('1')], total: 1 }));
    const wrapper = await page();
    expect(api.ListMovieChart).toHaveBeenLastCalledWith(CURRENT_YEAR, 'release', 1, false);

    api.ListMovieChart.mockResolvedValue(chartPage({
      total: 2, items: [item('1'), item('2', { mark: 'skip' })]
    }));
    await find(wrapper, 'show-marked').setValue(true);
    await flushPromises();

    expect(api.ListMovieChart).toHaveBeenLastCalledWith(CURRENT_YEAR, 'release', 1, true);
    expect(findAll(wrapper, 'mark-tag').map(node => node.text())).toEqual(['不想看']);
    const note = find(wrapper, 'scope-note').text();
    expect(note).toContain('不会放宽');
    expect(note).toContain('非全量');
  });

  it('翻到第 3 页后打开「不过滤」会退回第 1 页：过滤面变了，旧页码已经没有意义', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ items: [item('1')], total: 60 }));
    const wrapper = await page();
    api.ListMovieChart.mockResolvedValue(chartPage({ page: 3, items: [item('9')], total: 60 }));
    await find(wrapper, 'next').trigger('click');
    await flushPromises();
    await find(wrapper, 'next').trigger('click');
    await flushPromises();
    expect(api.ListMovieChart).toHaveBeenLastCalledWith(CURRENT_YEAR, 'release', 3, false);

    await find(wrapper, 'show-marked').setValue(true);
    await flushPromises();
    expect(api.ListMovieChart).toHaveBeenLastCalledWith(CURRENT_YEAR, 'release', 1, true);
  });

  it('被隐藏的条目露出来之后可以原地撤销标记', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({
      total: 1, items: [item('1', { mark: 'watched' })]
    }));
    const wrapper = await page();
    await find(wrapper, 'show-marked').setValue(true);
    await flushPromises();
    api.ClearMovieChartMark.mockResolvedValue(undefined);

    const row = findAll(wrapper, 'item')[0];
    expect(row.get('[data-test="movie-chart-mark-watched"]').attributes('aria-pressed')).toBe('true');
    await row.get('[data-test="movie-chart-mark-watched"]').trigger('click');
    await flushPromises();

    expect(api.ClearMovieChartMark).toHaveBeenCalledWith('1');
    expect(api.MarkMovieChartEntry).not.toHaveBeenCalled();
    expect(feedback.notifySuccess).toHaveBeenCalledWith('已撤销标记');
  });
});

describe('三种标记', () => {
  it('点未生效的按钮就是打标记，值分别是 want / skip / watched', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ total: 1, items: [item('1')] }));
    const wrapper = await page();
    api.MarkMovieChartEntry.mockResolvedValue({ mark: 'want', watchlist_created: true, watchlist_conflict: false });

    for (const [hook, value] of [['want', 'want'], ['skip', 'skip'], ['watched', 'watched']]) {
      await findAll(wrapper, 'item')[0].get(`[data-test="movie-chart-mark-${hook}"]`).trigger('click');
      await flushPromises();
      expect(api.MarkMovieChartEntry).toHaveBeenLastCalledWith('1', value);
    }
  });

  it('撞名不是失败：出「该片名已在想看片单中」，且不走报错通道', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ total: 1, items: [item('1')] }));
    const wrapper = await page();
    api.MarkMovieChartEntry.mockResolvedValue({ mark: 'want', watchlist_created: false, watchlist_conflict: true });

    await findAll(wrapper, 'item')[0].get('[data-test="movie-chart-mark-want"]').trigger('click');
    await flushPromises();

    const notice = find(wrapper, 'notice').text();
    expect(notice).toContain('该片名已在想看片单中');
    expect(notice).toContain('已标记想看');
    expect(wrapper.find('[data-test="movie-chart-action-error"]').exists()).toBe(false);
  });

  it('没撞名时不出那条提示', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ total: 1, items: [item('1')] }));
    const wrapper = await page();
    api.MarkMovieChartEntry.mockResolvedValue({ mark: 'want', watchlist_created: true, watchlist_conflict: false });

    await findAll(wrapper, 'item')[0].get('[data-test="movie-chart-mark-want"]').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="movie-chart-notice"]').exists()).toBe(false);
    expect(feedback.notifySuccess).toHaveBeenCalledWith('已标记想看');
  });

  it('标记失败出错误，不谎报成功也不重读列表', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ total: 1, items: [item('1')] }));
    const wrapper = await page();
    const readsBefore = api.ListMovieChart.mock.calls.length;
    api.MarkMovieChartEntry.mockRejectedValue(new Error('该影片不在本地榜单缓存中'));

    await findAll(wrapper, 'item')[0].get('[data-test="movie-chart-mark-skip"]').trigger('click');
    await flushPromises();

    expect(find(wrapper, 'action-error').text()).toContain('该影片不在本地榜单缓存中');
    expect(feedback.notifySuccess).not.toHaveBeenCalled();
    expect(api.ListMovieChart.mock.calls.length).toBe(readsBefore);
  });

  it('标记把当页清空时退回上一页，不把用户留在空白页', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ items: [item('1')], total: 21 }));
    const wrapper = await page();
    api.ListMovieChart.mockResolvedValue(chartPage({ page: 2, items: [item('21')], total: 21 }));
    await find(wrapper, 'next').trigger('click');
    await flushPromises();

    api.MarkMovieChartEntry.mockResolvedValue({ mark: 'skip', watchlist_created: false, watchlist_conflict: false });
    api.ListMovieChart.mockResolvedValueOnce(chartPage({ page: 2, items: [], total: 20 }))
      .mockResolvedValue(chartPage({ page: 1, items: [item('1')], total: 20 }));
    await findAll(wrapper, 'item')[0].get('[data-test="movie-chart-mark-skip"]').trigger('click');
    await flushPromises();

    expect(api.ListMovieChart).toHaveBeenLastCalledWith(CURRENT_YEAR, 'release', 1, false);
    expect(findAll(wrapper, 'item')).toHaveLength(1);
  });
});

describe('提交期间禁用动作与丢弃过时响应', () => {
  it('标记在飞的时候三个按钮和翻页都禁用', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ items: [item('1')], total: 60 }));
    const wrapper = await page();
    const pending = deferred();
    api.MarkMovieChartEntry.mockReturnValue(pending.promise);

    await findAll(wrapper, 'item')[0].get('[data-test="movie-chart-mark-want"]').trigger('click');
    await flushPromises();

    const row = findAll(wrapper, 'item')[0];
    for (const hook of ['want', 'skip', 'watched']) {
      expect(row.get(`[data-test="movie-chart-mark-${hook}"]`).attributes('disabled')).toBeDefined();
    }
    expect(find(wrapper, 'next').attributes('disabled')).toBeDefined();
    expect(find(wrapper, 'refresh').attributes('disabled')).toBeDefined();

    pending.resolve({ mark: 'want', watchlist_created: true, watchlist_conflict: false });
    await flushPromises();
    expect(findAll(wrapper, 'item')[0].get('[data-test="movie-chart-mark-want"]').attributes('disabled')).toBeUndefined();
  });

  // 用户看得见的读（翻页 / 切排序）期间控件全禁用，所以两次**可见**的读撞不到一起；
  // 真正会并发的是静默轮询——它不亮加载态，用户随时能在它飞行途中切排序。
  // 这一条走的就是那条路径，且让迟到响应的年份 / 排序回显与它自己的请求一致，
  // 使序号守卫成为唯一能拦住它的东西。
  it('静默轮询的迟到响应按序号丢弃，界面停在用户后发那次的结果', async () => {
    vi.useFakeTimers();
    api.ListMovieChart.mockResolvedValue(chartPage({
      items: [item('first')], total: 1,
      cache: { last_refreshed_at: null, last_failure: '', refreshing: true }
    }));
    const wrapper = mount(MovieChartPage);
    wrappers.push(wrapper);
    await flushPromises();

    const slowPoll = deferred();
    api.ListMovieChart.mockReturnValueOnce(slowPoll.promise);
    await vi.advanceTimersByTimeAsync(5000);
    expect(wrapper.vm.loading).toBe(false);

    api.ListMovieChart.mockResolvedValue(chartPage({
      sort: 'rating', items: [item('latest')], total: 1,
      cache: { last_refreshed_at: null, last_failure: '', refreshing: true }
    }));
    await find(wrapper, 'sort-rating').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'title').text()).toContain('latest');

    slowPoll.resolve(chartPage({
      sort: 'release', items: [item('stale')], total: 1,
      cache: { last_refreshed_at: null, last_failure: '', refreshing: true }
    }));
    await flushPromises();
    expect(find(wrapper, 'title').text()).toContain('latest');
  });

  it('回显的年份对不上的响应直接丢弃', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ items: [item('good')], total: 1 }));
    const wrapper = await page();

    // 年份回显是另一年（切年份途中的旧响应恰好按序返回）：不能贴到当前视图上。
    // 序号在这里是对得上的，拦住它的只有回显校验。
    api.ListMovieChart.mockResolvedValue(chartPage({ year: CURRENT_YEAR - 3, sort: 'rating', items: [item('wrong-year')], total: 1 }));
    await find(wrapper, 'sort-rating').trigger('click');
    await flushPromises();

    expect(find(wrapper, 'title').text()).toContain('good');
  });

  it('回显的排序对不上的响应直接丢弃', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ items: [item('good')], total: 1 }));
    const wrapper = await page();

    api.ListMovieChart.mockResolvedValue(chartPage({ year: CURRENT_YEAR, sort: 'release', items: [item('wrong-sort')], total: 1 }));
    await find(wrapper, 'sort-rating').trigger('click');
    await flushPromises();

    expect(find(wrapper, 'title').text()).toContain('good');
  });

  it('读失败出错误，不把界面清成空榜单', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ items: [item('1')], total: 1 }));
    const wrapper = await page();
    api.ListMovieChart.mockRejectedValue(new Error('读取 2026 年榜单失败'));

    await find(wrapper, 'sort-rating').trigger('click');
    await flushPromises();

    expect(find(wrapper, 'error').text()).toContain('读取 2026 年榜单失败');
    expect(findAll(wrapper, 'item')).toHaveLength(1);
  });
});

describe('分页与年份下拉', () => {
  it('每页 20 条，页码跟着后端的 total 走，边界按钮禁用', async () => {
    api.ListMovieChart.mockResolvedValue(chartPage({ items: [item('1')], total: 45, page_size: 20 }));
    const wrapper = await page();

    expect(find(wrapper, 'page-info').text()).toContain('第 1 / 3 页');
    expect(find(wrapper, 'prev').attributes('disabled')).toBeDefined();

    api.ListMovieChart.mockResolvedValue(chartPage({ page: 3, items: [item('41')], total: 45, page_size: 20 }));
    await find(wrapper, 'next').trigger('click');
    await flushPromises();
    await find(wrapper, 'next').trigger('click');
    await flushPromises();

    expect(api.ListMovieChart).toHaveBeenLastCalledWith(CURRENT_YEAR, 'release', 3, false);
    expect(find(wrapper, 'next').attributes('disabled')).toBeDefined();
  });

  it('年份下拉来自 ListMovieChartYears，默认落在当前年', async () => {
    api.ListMovieChartYears.mockResolvedValue([CURRENT_YEAR, CURRENT_YEAR - 1, CURRENT_YEAR - 2]);
    const wrapper = await page();

    const select = find(wrapper, 'year');
    expect(select.findAll('option').map(node => node.text())).toEqual([
      `${CURRENT_YEAR} 年`, `${CURRENT_YEAR - 1} 年`, `${CURRENT_YEAR - 2} 年`
    ]);
    expect(select.element.value).toBe(String(CURRENT_YEAR));
    expect(api.ListMovieChart).toHaveBeenCalledWith(CURRENT_YEAR, 'release', 1, false);
  });

  it('年份下拉取不到时只出提示，仍按当前年读一页', async () => {
    api.ListMovieChartYears.mockRejectedValue(new Error('读取榜单年份失败'));
    const wrapper = await page();

    expect(find(wrapper, 'action-error').text()).toContain('读取榜单年份失败');
    expect(api.ListMovieChart).toHaveBeenCalledWith(CURRENT_YEAR, 'release', 1, false);
  });
});

describe('刷新状态轮询', () => {
  it('抓取或补全没结束时按间隔重读，只调纯读接口', async () => {
    vi.useFakeTimers();
    api.ListMovieChart.mockResolvedValue(chartPage({
      items: [item('1')], total: 1,
      cache: { last_refreshed_at: null, last_failure: '', refreshing: true },
      backfill: { pending: 3, total: 10 }
    }));
    const wrapper = mount(MovieChartPage);
    wrappers.push(wrapper);
    await flushPromises();
    const readsAfterMount = api.ListMovieChart.mock.calls.length;
    api.OpenMovieChartYear.mockClear();

    api.ListMovieChart.mockResolvedValue(chartPage({
      items: [item('1')], total: 1,
      cache: { last_refreshed_at: '2026-09-20T10:00:00+08:00', last_failure: '', refreshing: false },
      backfill: { pending: 0, total: 10 }
    }));
    await vi.advanceTimersByTimeAsync(5000);
    await flushPromises();

    expect(api.ListMovieChart.mock.calls.length).toBe(readsAfterMount + 1);
    expect(api.OpenMovieChartYear).not.toHaveBeenCalled();
    expect(find(wrapper, 'status').text()).toContain('数据更新于');
    expect(wrapper.find('[data-test="movie-chart-cancel"]').exists()).toBe(false);

    // 状态落定之后不再空转。
    await vi.advanceTimersByTimeAsync(20000);
    await flushPromises();
    expect(api.ListMovieChart.mock.calls.length).toBe(readsAfterMount + 1);
  });

  it('卸载后不再轮询', async () => {
    vi.useFakeTimers();
    api.ListMovieChart.mockResolvedValue(chartPage({
      items: [item('1')], total: 1,
      cache: { last_refreshed_at: null, last_failure: '', refreshing: true }
    }));
    const wrapper = mount(MovieChartPage);
    await flushPromises();
    const reads = api.ListMovieChart.mock.calls.length;

    wrapper.unmount();
    await vi.advanceTimersByTimeAsync(20000);
    await flushPromises();

    expect(api.ListMovieChart.mock.calls.length).toBe(reads);
  });
});
