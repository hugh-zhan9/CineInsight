import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'CreateWatchlistEntry', 'UpdateWatchlistEntry', 'DeleteWatchlistEntry', 'ListWatchlist',
  'RetryWatchlistEnrichment', 'ListWatchlistCandidates', 'ApplyWatchlistCandidate'
].map(name => [name, vi.fn()])));
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn(), notifySuccess: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', () => feedback);

import WatchlistPage from './WatchlistPage.vue';

const entry = (id, title, extra = {}) => ({
  id,
  title,
  kind: 'movie',
  enrichment_status: 'pending',
  enrichment_error: '',
  source_name: '',
  source_item_id: '',
  year: 0,
  overview: '',
  genres: '',
  credits: '',
  rating: 0,
  created_at: '2026-09-10T10:00:00+08:00',
  ...extra
});
const wrappers = [];
const deferred = () => {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
};

// 补全进度是后端推过来的，测试里握住那个回调就能模拟「补全完成」。
let progressHandler = null;
let unsubscribed = 0;

beforeEach(() => {
  vi.resetAllMocks();
  progressHandler = null;
  unsubscribed = 0;
  window.runtime = {
    EventsOn: (name, handler) => {
      if (name !== 'watchlist-enrich-progress') return () => {};
      progressHandler = handler;
      return () => { unsubscribed += 1; progressHandler = null; };
    }
  };
  api.ListWatchlist.mockResolvedValue({ entries: [], next_id: 0 });
  api.CreateWatchlistEntry.mockResolvedValue(entry(1, '沙丘'));
  api.ListWatchlistCandidates.mockResolvedValue([]);
  feedback.confirmAction.mockResolvedValue(true);
});
afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
});

async function page() {
  const wrapper = mount(WatchlistPage);
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}
const find = (wrapper, name) => wrapper.get(`[data-test="watchlist-${name}"]`);

describe('想看片单', () => {
  it('空片单提示添加，空白不提交，填写片名即可按默认类型添加并重读', async () => {
    const wrapper = await page();
    expect(find(wrapper, 'empty').text()).toContain('还没有');
    await find(wrapper, 'title').setValue('   ');
    await find(wrapper, 'form').trigger('submit');
    expect(api.CreateWatchlistEntry).not.toHaveBeenCalled();
    api.ListWatchlist.mockResolvedValue({ entries: [entry(1, '沙丘')], next_id: 0 });
    await find(wrapper, 'title').setValue(' 沙丘 ');
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(api.CreateWatchlistEntry).toHaveBeenCalledWith('沙丘', 'movie');
    expect(find(wrapper, 'title').element.value).toBe('');
    expect(find(wrapper, 'entry').text()).toContain('沙丘');
  });

  it('保存期间不重复提交，失败保留草稿与已有记录', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(1, '原片名')], next_id: 0 });
    const wrapper = await page();
    const pending = deferred();
    api.CreateWatchlistEntry.mockReturnValue(pending.promise);
    await find(wrapper, 'title').setValue('新片名');
    await find(wrapper, 'form').trigger('submit');
    await find(wrapper, 'form').trigger('submit');
    expect(api.CreateWatchlistEntry).toHaveBeenCalledTimes(1);
    pending.resolve(entry(2, '新片名'));
    await flushPromises();
    api.CreateWatchlistEntry.mockRejectedValue(new Error('数据库断开'));
    await find(wrapper, 'title').setValue('保留草稿');
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(find(wrapper, 'title').element.value).toBe('保留草稿');
    expect(find(wrapper, 'entry').text()).toContain('原片名');
    expect(find(wrapper, 'error').text()).toContain('数据库断开');
  });

  it('按 ID 改名，取消编辑不写入，修改失败保留输入', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(3, '原片名')], next_id: 0 });
    const wrapper = await page();
    await find(wrapper, 'edit-3').trigger('click');
    await find(wrapper, 'cancel').trigger('click');
    expect(api.UpdateWatchlistEntry).not.toHaveBeenCalled();
    await find(wrapper, 'edit-3').trigger('click');
    await find(wrapper, 'title').setValue('改后的片名');
    api.UpdateWatchlistEntry.mockRejectedValueOnce(new Error('写入失败'));
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(find(wrapper, 'title').element.value).toBe('改后的片名');
    api.ListWatchlist.mockResolvedValue({ entries: [entry(3, '改后的片名')], next_id: 0 });
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(api.UpdateWatchlistEntry).toHaveBeenLastCalledWith(3, '改后的片名');
    expect(api.CreateWatchlistEntry).not.toHaveBeenCalled();
    expect(find(wrapper, 'entry').text()).toContain('改后的片名');
  });

  it('移除需确认，失败保留条目，成功只移除指定 ID', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(2, '沙丘'), entry(1, '沙丘')], next_id: 0 });
    const wrapper = await page();
    feedback.confirmAction.mockResolvedValueOnce(false);
    await find(wrapper, 'remove-2').trigger('click');
    await flushPromises();
    expect(api.DeleteWatchlistEntry).not.toHaveBeenCalled();
    api.DeleteWatchlistEntry.mockRejectedValueOnce(new Error('移除失败'));
    await find(wrapper, 'remove-2').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(2);
    await find(wrapper, 'remove-2').trigger('click');
    await flushPromises();
    expect(api.DeleteWatchlistEntry).toHaveBeenLastCalledWith(2);
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(1);
    expect(find(wrapper, 'remove-1').exists()).toBe(true);
  });

  it('搜索丢弃旧响应，翻页按当前词追加，失败保留已加载页', async () => {
    const wrapper = await page();
    const stale = deferred();
    api.ListWatchlist.mockReturnValueOnce(stale.promise);
    await find(wrapper, 'search').setValue('旧词');
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(9, '沙丘')], next_id: 9 });
    await find(wrapper, 'search').setValue('沙丘');
    await flushPromises();
    stale.resolve({ entries: [entry(10, '旧词结果')], next_id: 10 });
    await flushPromises();
    expect(find(wrapper, 'entry').text()).toContain('沙丘');
    api.ListWatchlist.mockRejectedValueOnce(new Error('翻页失败'));
    await find(wrapper, 'more').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'entry').text()).toContain('沙丘');
    expect(find(wrapper, 'error').text()).toContain('翻页失败');
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(3, '沙丘（1984）')], next_id: 0 });
    await find(wrapper, 'more').trigger('click');
    await flushPromises();
    expect(api.ListWatchlist).toHaveBeenLastCalledWith('沙丘', 9, 50);
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(2);
    expect(wrapper.find('[data-test="watchlist-more"]').exists()).toBe(false);
  });

  it('移除当前页最后一条后加载剩余页，不误报片单为空', async () => {
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(5, '第一页')], next_id: 5 });
    const wrapper = await page();
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(2, '下一页')], next_id: 0 });
    await find(wrapper, 'remove-5').trigger('click');
    await flushPromises();
    expect(api.ListWatchlist).toHaveBeenLastCalledWith('', 5, 50);
    expect(find(wrapper, 'entry').text()).toContain('下一页');
    expect(wrapper.find('[data-test="watchlist-empty"]').exists()).toBe(false);
  });

  it('读取失败不显示空片单，200 字符边界按 Unicode 计数', async () => {
    api.ListWatchlist.mockRejectedValueOnce(new Error('读取失败'));
    const wrapper = await page();
    expect(wrapper.find('[data-test="watchlist-empty"]').exists()).toBe(false);
    expect(find(wrapper, 'error').text()).toContain('读取失败');
    await find(wrapper, 'title').setValue('影'.repeat(201));
    await find(wrapper, 'form').trigger('submit');
    expect(api.CreateWatchlistEntry).not.toHaveBeenCalled();
    await find(wrapper, 'title').setValue('🎬'.repeat(200));
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(api.CreateWatchlistEntry).toHaveBeenCalledWith('🎬'.repeat(200), 'movie');
  });
});

describe('想看片单的类型选择', () => {
  it('五个类型都能按所选值提交', async () => {
    const wrapper = await page();
    expect(find(wrapper, 'kind').findAll('option').map(option => option.element.value))
      .toEqual(['movie', 'tv', 'anime', 'show', 'av']);
    for (const kind of ['movie', 'tv', 'anime', 'show', 'av']) {
      await find(wrapper, 'kind').setValue(kind);
      await find(wrapper, 'title').setValue(`片名-${kind}`);
      await find(wrapper, 'form').trigger('submit');
      await flushPromises();
      expect(api.CreateWatchlistEntry).toHaveBeenLastCalledWith(`片名-${kind}`, kind);
    }
  });

  it('番号输入自动切到 AV，改回去后提交的是改回后的类型', async () => {
    const wrapper = await page();
    expect(find(wrapper, 'kind').element.value).toBe('movie');
    await find(wrapper, 'title').setValue('SSIS-001');
    expect(find(wrapper, 'kind').element.value).toBe('av');
    // 还没碰过下拉框时，输入改成普通片名就跟着退回默认类型。
    await find(wrapper, 'title').setValue('沙丘');
    expect(find(wrapper, 'kind').element.value).toBe('movie');
    // 用户改回去之后，再怎么敲番号也不再被自动覆盖。
    await find(wrapper, 'title').setValue('259LUXU-1234');
    expect(find(wrapper, 'kind').element.value).toBe('av');
    await find(wrapper, 'kind').setValue('anime');
    await find(wrapper, 'title').setValue('ABP-456');
    expect(find(wrapper, 'kind').element.value).toBe('anime');
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(api.CreateWatchlistEntry).toHaveBeenCalledWith('ABP-456', 'anime');
  });

  it('两位数字的片名不算番号，不误切类型', async () => {
    const wrapper = await page();
    await find(wrapper, 'title').setValue('Catch-22');
    expect(find(wrapper, 'kind').element.value).toBe('movie');
    await find(wrapper, 'form').trigger('submit');
    await flushPromises();
    expect(api.CreateWatchlistEntry).toHaveBeenCalledWith('Catch-22', 'movie');
  });
});

describe('想看片单的补全状态展示', () => {
  it('六类失败分类码各显示各的文案，并按类型说出是哪个源', async () => {
    const failed = (id, code, extra = {}) => entry(id, `失败-${id}`, {
      enrichment_status: 'failed', enrichment_error: code, ...extra
    });
    api.ListWatchlist.mockResolvedValue({
      entries: [
        failed(1, 'credential_missing'),
        failed(2, 'credential_invalid'),
        failed(3, 'proxy_unreachable'),
        failed(4, 'network_unreachable'),
        failed(5, 'not_found', { kind: 'anime' }),
        failed(6, 'source_error', { kind: 'av' }),
        failed(7, '来路不明的码')
      ],
      next_id: 0
    });
    const wrapper = await page();
    const texts = [1, 2, 3, 4, 5, 6, 7].map(id => find(wrapper, `failure-${id}`).text());
    expect(texts).toEqual([
      '未配置 TMDB 的凭证',
      'TMDB 凭证无效或已过期',
      '资料源出网代理不可用',
      '无法连接 TMDB',
      'Bangumi 没有收录',
      'AV 资料源 返回异常',
      '补全失败（来路不明的码）'
    ]);
    expect(new Set(texts).size).toBe(7);
  });

  it('五种补全状态各有文案，只有失败条目给重试按钮', async () => {
    api.ListWatchlist.mockResolvedValue({
      entries: ['pending', 'running', 'succeeded', 'failed', 'manual'].map((status, index) =>
        entry(index + 1, `状态-${status}`, { enrichment_status: status })),
      next_id: 0
    });
    const wrapper = await page();
    expect([1, 2, 3, 4, 5].map(id => find(wrapper, `status-${id}`).text()))
      .toEqual(['待补全', '补全中…', '已补全', '补全失败', '手动维护']);
    expect(wrapper.findAll('[data-test^="watchlist-retry-"]')).toHaveLength(1);
    expect(wrapper.find('[data-test="watchlist-retry-4"]').exists()).toBe(true);
  });

  it('Genres 与 Credits 是 JSON 字符串，解析失败不带崩整页', async () => {
    api.ListWatchlist.mockResolvedValue({
      entries: [
        entry(1, '正常', {
          enrichment_status: 'succeeded', year: 2021, rating: 8.15,
          genres: '["科幻","冒险"]',
          credits: '{"directors":["维伦纽瓦"],"cast":["提莫西","赞达亚"]}'
        }),
        entry(2, '坏数据', { enrichment_status: 'succeeded', genres: '不是 JSON', credits: '{坏' })
      ],
      next_id: 0
    });
    const wrapper = await page();
    const rows = wrapper.findAll('[data-test="watchlist-entry"]');
    expect(rows[0].text()).toContain('科幻 · 冒险');
    expect(rows[0].text()).toContain('导演：维伦纽瓦');
    expect(rows[0].text()).toContain('主演：提莫西、赞达亚');
    expect(rows[0].text()).toContain('2021');
    expect(rows[0].text()).toContain('8.2 分');
    expect(rows[1].text()).toContain('坏数据');
    expect(rows[1].text()).not.toContain('导演');
  });
});

describe('想看片单的补全刷新与重试', () => {
  it('补全完成后该行自动刷新，无需手动操作', async () => {
    api.ListWatchlist.mockResolvedValue({
      entries: [entry(7, '沙丘', { enrichment_status: 'running' }), entry(6, '别的片')],
      next_id: 0
    });
    const wrapper = await page();
    expect(find(wrapper, 'status-7').text()).toBe('补全中…');
    api.ListWatchlist.mockResolvedValueOnce({
      entries: [entry(7, '沙丘', { enrichment_status: 'succeeded', source_name: 'tmdb', year: 2021 })],
      next_id: 0
    });
    progressHandler({ entry_id: 7, status: 'succeeded', source_name: 'tmdb' });
    await flushPromises();
    // 只补读这一条（游标是 id < cursorID），不重拉整页。
    expect(api.ListWatchlist).toHaveBeenLastCalledWith('', 8, 1);
    expect(find(wrapper, 'status-7').text()).toBe('已补全');
    expect(wrapper.findAll('[data-test="watchlist-entry"]')[0].text()).toContain('2021');
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(2);
  });

  it('失败进度就地改状态与文案，不额外发请求；不在列表里的条目被忽略', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(7, '沙丘', { enrichment_status: 'running' })], next_id: 0 });
    const wrapper = await page();
    api.ListWatchlist.mockClear();
    progressHandler({ entry_id: 7, status: 'failed', failure: 'credential_missing' });
    await flushPromises();
    expect(api.ListWatchlist).not.toHaveBeenCalled();
    expect(find(wrapper, 'status-7').text()).toBe('补全失败');
    expect(find(wrapper, 'failure-7').text()).toBe('未配置 TMDB 的凭证');
    progressHandler({ entry_id: 999, status: 'succeeded' });
    await flushPromises();
    expect(api.ListWatchlist).not.toHaveBeenCalled();
  });

  it('卸载时取消订阅', async () => {
    const wrapper = await page();
    expect(progressHandler).toBeTypeOf('function');
    wrappers.pop();
    wrapper.unmount();
    expect(unsubscribed).toBe(1);
  });

  it('重试把失败条目交回补全流程，失败时保留列表并报错', async () => {
    api.ListWatchlist.mockResolvedValue({
      entries: [entry(4, '取不到的片', { enrichment_status: 'failed', enrichment_error: 'not_found' })],
      next_id: 0
    });
    const wrapper = await page();
    api.RetryWatchlistEnrichment.mockRejectedValueOnce(new Error('条目已不存在'));
    await find(wrapper, 'retry-4').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'error').text()).toContain('条目已不存在');
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(1);
    api.RetryWatchlistEnrichment.mockResolvedValueOnce(undefined);
    api.ListWatchlist.mockResolvedValueOnce({
      entries: [entry(4, '取不到的片', { enrichment_status: 'pending' })],
      next_id: 0
    });
    await find(wrapper, 'retry-4').trigger('click');
    await flushPromises();
    expect(api.RetryWatchlistEnrichment).toHaveBeenLastCalledWith(4);
    expect(find(wrapper, 'status-4').text()).toBe('待补全');
    expect(wrapper.find('[data-test="watchlist-retry-4"]').exists()).toBe(false);
  });
});

describe('想看片单的候选重选', () => {
  const candidate = (id, title, extra = {}) => ({
    source_name: 'tmdb', source_item_id: id, title, original_title: title,
    year: 2021, overview: '简介', rating: 8, ...extra
  });

  it('列出候选并应用用户选定的那条，之后刷新该行', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(1, '沙丘')], next_id: 0 });
    const wrapper = await page();
    api.ListWatchlistCandidates.mockResolvedValueOnce([candidate('438631', '沙丘'), candidate('841', '沙丘 1984')]);
    await find(wrapper, 'candidates-1').trigger('click');
    await flushPromises();
    expect(api.ListWatchlistCandidates).toHaveBeenCalledWith(1);
    expect(wrapper.findAll('[data-test="watchlist-candidate"]')).toHaveLength(2);
    api.ListWatchlist.mockResolvedValueOnce({
      entries: [entry(1, '沙丘', { enrichment_status: 'manual', source_name: 'tmdb', year: 1984 })],
      next_id: 0
    });
    await find(wrapper, 'candidate-apply-1').trigger('click');
    await flushPromises();
    expect(api.ApplyWatchlistCandidate).toHaveBeenCalledWith(1, '841');
    expect(wrapper.find('[data-test="watchlist-candidate-panel"]').exists()).toBe(false);
    expect(find(wrapper, 'status-1').text()).toBe('手动维护');
    expect(find(wrapper, 'entry').text()).toContain('1984');
  });

  it('候选查询失败与应用失败都保留面板和已加载列表', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(1, '沙丘')], next_id: 0 });
    const wrapper = await page();
    api.ListWatchlistCandidates.mockRejectedValueOnce(new Error('未配置 TMDB 的凭证'));
    await find(wrapper, 'candidates-1').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'candidate-error').text()).toContain('未配置 TMDB 的凭证');
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(1);
    api.ListWatchlistCandidates.mockResolvedValueOnce([candidate('438631', '沙丘')]);
    await find(wrapper, 'candidates-1').trigger('click');
    await flushPromises();
    api.ApplyWatchlistCandidate.mockRejectedValueOnce(new Error('应用候选失败'));
    await find(wrapper, 'candidate-apply-0').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'candidate-error').text()).toContain('应用候选失败');
    expect(wrapper.find('[data-test="watchlist-candidate-panel"]').exists()).toBe(true);
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(1);
  });

  it('资料源没有候选时明说，不显示空面板', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(1, '冷门片')], next_id: 0 });
    const wrapper = await page();
    await find(wrapper, 'candidates-1').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'candidate-empty').text()).toContain('没有给出候选');
    await find(wrapper, 'candidate-close').trigger('click');
    expect(wrapper.find('[data-test="watchlist-candidate-panel"]').exists()).toBe(false);
  });
});
