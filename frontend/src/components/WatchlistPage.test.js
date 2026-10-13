import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'CreateWatchlistEntry', 'UpdateWatchlistEntry', 'DeleteWatchlistEntry', 'ListWatchlist',
  'RetryWatchlistEnrichment', 'ListWatchlistCandidates', 'ApplyWatchlistCandidate',
  'SuggestLibraryMatchesBatch', 'ListWatchlistLibraryState', 'LinkWatchlistEntryVideo', 'UnlinkWatchlistEntryVideo'
].map(name => [name, vi.fn()])));
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn(), notifySuccess: vi.fn(), notifyUndo: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('../utils/feedback.js', () => feedback);

import WatchlistPage from './WatchlistPage.vue';
import watchlistPageSource from './WatchlistPage.vue?raw';

const entry = (id, title, extra = {}) => ({
  id,
  title,
  kind: 'movie',
  enrichment_status: 'pending',
  enrichment_error: '',
  source_name: '',
  source_item_id: '',
  source_title: '',
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
  api.SuggestLibraryMatchesBatch.mockResolvedValue({});
  // 片库关联状态（D-P2-03）：缺省每个条目都没有关联。
  api.ListWatchlistLibraryState.mockImplementation(ids => Promise.resolve(Object.fromEntries(ids.map(id => [id, { linked: [] }]))));
  feedback.confirmAction.mockResolvedValue(true);
});
afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
});

async function page(props = {}) {
  const wrapper = mount(WatchlistPage, { props });
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
      '无法连接 豆瓣 / TMDB',
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

describe('海报样式', () => {
  it('海报不跟着简介一起被拉高', () => {
    // 父级 .watchlist-entry-main 是 flex 且默认 align-items: stretch，海报一旦被拉到
    // 跟长简介等高就会变形。挡住它需要两样：align-self 不吃 stretch，以及一个
    // 确定的高度。
    //
    // 原先第二样是 aspect-ratio: 2 / 3，已换成固定 height——因为 2:3 只适合
    // TMDB 的竖版电影海报，AV 封面是 800×538 的横版整幅封套，按 2:3 裁只剩
    // 中间一条窄竖条。现在高度定死、宽度随图片自身比例走。
    const rule = watchlistPageSource.match(/\.watchlist-poster \{[^}]*\}/)?.[0] || '';
    expect(rule).toContain('align-self: flex-start');
    expect(rule).toMatch(/height:\s*\d+px/);
  });

  it('封面不裁切，宽度随图片自身比例走', () => {
    // AV 封面（800×538 横版）与电影海报（2:3 竖版）比例相差很远，写死任何一个
    // 比例都会把另一种裁坏。contain + auto 宽度让两种都完整显示。
    const rule = watchlistPageSource.match(/\.watchlist-poster \{[^}]*\}/)?.[0] || '';
    expect(rule).toContain('object-fit: contain');
    expect(rule).toContain('width: auto');
    expect(rule).not.toContain('aspect-ratio');
  });
});

// 源站片名是只读补充：用户手输的番号永远是主标题（AC-09 / D-AVM11）。
describe('想看片单的源站片名', () => {
  const mountList = async () => {
    const wrapper = mount(WatchlistPage);
    wrappers.push(wrapper);
    await flushPromises();
    return wrapper;
  };

  it('番号与中文片名并列展示，用户手输的番号仍是主标题', async () => {
    api.ListWatchlist.mockResolvedValue({
      entries: [entry(1, 'ABC-123', {
        kind: 'av', enrichment_status: 'succeeded',
        source_name: 'aggregate', source_title: '中文片名'
      })],
      next_cursor_id: 0
    });
    const wrapper = await mountList();

    expect(wrapper.text()).toContain('ABC-123');
    expect(find(wrapper, 'source-title-1').text()).toBe('中文片名');
  });

  it('源站片名取不到时不渲染，不留空位', async () => {
    api.ListWatchlist.mockResolvedValue({
      entries: [entry(1, 'ABC-123', { kind: 'av', enrichment_status: 'succeeded', source_title: '' })],
      next_cursor_id: 0
    });
    const wrapper = await mountList();

    expect(wrapper.find('[data-test="watchlist-source-title-1"]').exists()).toBe(false);
  });

  it('源站片名与用户标题相同时不重复显示一遍', async () => {
    api.ListWatchlist.mockResolvedValue({
      entries: [entry(1, '沙丘', { enrichment_status: 'succeeded', source_title: '沙丘' })],
      next_cursor_id: 0
    });
    const wrapper = await mountList();

    expect(wrapper.find('[data-test="watchlist-source-title-1"]').exists()).toBe(false);
  });

  it('多源合并的条目显示成「多源合并」而不是裸的 aggregate', async () => {
    api.ListWatchlist.mockResolvedValue({
      entries: [entry(1, 'ABC-123', {
        kind: 'av', enrichment_status: 'failed', enrichment_error: 'not_found', source_name: 'aggregate'
      })],
      next_cursor_id: 0
    });
    const wrapper = await mountList();

    expect(wrapper.text()).toContain('多源合并');
    expect(wrapper.text()).not.toContain('aggregate');
  });

  // FANZA 已退场，但存量条目的 source_name 仍是它——展示名必须保留，
  // 否则那些历史条目会显示成裸字符串。
  it('存量 FANZA 条目仍显示为 FANZA', async () => {
    api.ListWatchlist.mockResolvedValue({
      entries: [entry(1, 'ABC-123', {
        kind: 'av', enrichment_status: 'failed', enrichment_error: 'not_found', source_name: 'fanza'
      })],
      next_cursor_id: 0
    });
    const wrapper = await mountList();

    expect(wrapper.text()).toContain('FANZA');
  });
});

// FC2 番号是另一套形态，下拉框要认得出来（AC：输 FC2-PPV 不该停在「电影」）。
describe('想看片单的 FC2 番号识别', () => {
  const typeTitle = async (wrapper, value) => {
    await find(wrapper, 'title').setValue(value);
    await flushPromises();
  };

  it('FC2 的几种写法都自动切到 AV', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [], next_cursor_id: 0 });
    for (const code of ['FC2-PPV-4976527', 'FC2PPV-4976527', 'FC2-4976527', 'fc2-ppv-4976527']) {
      const wrapper = mount(WatchlistPage);
      wrappers.push(wrapper);
      await flushPromises();
      await typeTitle(wrapper, code);
      expect(find(wrapper, 'kind').element.value, code).toBe('av');
    }
  });

  it('光秃秃的商品号不自动切——一串数字同样可能是片名', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [], next_cursor_id: 0 });
    const wrapper = mount(WatchlistPage);
    wrappers.push(wrapper);
    await flushPromises();
    await typeTitle(wrapper, '4976527');
    expect(find(wrapper, 'kind').element.value).toBe('movie');
  });

  it('FC2 源名显示为 FC2，不是裸字符串', async () => {
    api.ListWatchlist.mockResolvedValue({
      entries: [entry(1, 'FC2-PPV-4976527', {
        kind: 'av', enrichment_status: 'failed', enrichment_error: 'not_found', source_name: 'fc2'
      })],
      next_cursor_id: 0
    });
    const wrapper = mount(WatchlistPage);
    wrappers.push(wrapper);
    await flushPromises();
    expect(wrapper.text()).toContain('FC2');
  });
});

describe('想看片单与片库、榜单的闭环（D-PC52）', () => {
  const douban = (id, title, extra = {}) => entry(id, title, {
    enrichment_status: 'succeeded', source_name: 'douban', source_item_id: `3${id}`, year: 2021, ...extra
  });

  it('APP-07 显示条目来源：榜单建立或认领的标「来自榜单」，其余标「手动添加」', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [douban(1, '沙丘'), entry(2, '手输片名')], next_id: 0, origins: { 1: 'chart', 2: 'manual' } });
    const wrapper = await page();
    expect(find(wrapper, 'origin-1').text()).toBe('来自榜单');
    expect(find(wrapper, 'origin-2').text()).toBe('手动添加');
  });

  it('APP-07 移除关联到豆瓣条目的片单项时说明会同时撤销榜单上的「想看」', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [douban(1, '沙丘'), entry(2, '手输片名')], next_id: 0, origins: { 1: 'chart' } });
    const wrapper = await page();
    await find(wrapper, 'remove-1').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction.mock.calls.at(-1)[0].message).toContain('「想看」标记也会一并撤销');
    await find(wrapper, 'remove-2').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction.mock.calls.at(-1)[0].message).not.toContain('榜单');
  });

  it('APP-06 一页条目批量要「片库中可能已有」，任何来源都能确认关联（D-P2-03），之后提示从片单移除', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [douban(1, '沙丘'), entry(2, '手输片名')], next_id: 0, origins: {} });
    api.SuggestLibraryMatchesBatch.mockResolvedValue({
      '沙丘|2021': [{ video_id: 9, name: 'Dune.2021.mkv', display_title: '沙丘', score: 110 }],
      '手输片名|0': [{ video_id: 8, name: '手输片名.mp4', display_title: '', score: 100 }]
    });
    const wrapper = await page();
    expect(api.SuggestLibraryMatchesBatch).toHaveBeenCalledTimes(1);
    expect(api.SuggestLibraryMatchesBatch).toHaveBeenCalledWith([{ title: '沙丘', year: 2021 }, { title: '手输片名', year: 0 }]);
    expect(find(wrapper, 'suggestions-1').text()).toContain('片库中可能已有');
    // 手动条目（没有豆瓣 ID）同样能确认，不再要求先补全到豆瓣。
    expect(find(wrapper, 'link-2-8').text()).toBe('确认');
    expect(find(wrapper, 'suggestions-2').text()).not.toContain('补全到豆瓣条目后');

    // 豆瓣条目也走新接口：后端按来源选表（豆瓣写 movie_video_links，榜单的已看同步照旧）。
    api.LinkWatchlistEntryVideo.mockResolvedValue(undefined);
    api.ListWatchlistLibraryState.mockImplementation(ids => Promise.resolve(Object.fromEntries(ids.map(id => [id, {
      linked: id === 1 ? [{ video_id: 9, title: '沙丘', origin: 'manual', is_watched: false }] : []
    }]))));
    await find(wrapper, 'link-1-9').trigger('click');
    await flushPromises();
    expect(api.LinkWatchlistEntryVideo).toHaveBeenCalledWith(1, 9);
    expect(api.ListWatchlistLibraryState).toHaveBeenLastCalledWith([1]);
    expect(find(wrapper, 'linked-1').text()).toContain('已在库');
    expect(find(wrapper, 'linked-1').text()).toContain('沙丘');
    expect(wrapper.find('[data-test="watchlist-suggestions-1"]').exists()).toBe(false);

    // 片单不会因入库自动删条目，只提示并提供一键移除。
    await find(wrapper, 'remove-after-link-1').trigger('click');
    await flushPromises();
    expect(api.DeleteWatchlistEntry).toHaveBeenCalledWith(1);
  });

  it('APP-06 建议查询失败不影响片单本身，横幅说明并可重试', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [douban(1, '沙丘')], next_id: 0 });
    api.SuggestLibraryMatchesBatch.mockRejectedValueOnce(new Error('年度榜单服务不可用'));
    const wrapper = await page();
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(1);
    expect(wrapper.find('[data-test="watchlist-suggestions-1"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="watchlist-error"]').exists()).toBe(false);
    // 没读全就不说「不在库」。
    expect(wrapper.find('[data-test="watchlist-not-in-library-1"]').exists()).toBe(false);
    expect(find(wrapper, 'library-error').text()).toContain('年度榜单服务不可用');

    api.SuggestLibraryMatchesBatch.mockResolvedValueOnce({ '沙丘|2021': [{ video_id: 9, name: 'Dune.mkv', display_title: '沙丘' }] });
    await find(wrapper, 'library-retry').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="watchlist-library-error"]').exists()).toBe(false);
    expect(find(wrapper, 'suggestions-1').text()).toContain('沙丘');
  });
});

describe('想看已在库（D-P2-03）', () => {
  const linkedVideo = (videoID, title, origin = 'auto', extra = {}) => ({ video_id: videoID, title, origin, is_watched: false, ...extra });
  const stateOf = (map, dismissedMap = {}) => ids => Promise.resolve(Object.fromEntries(ids.map(id => [id, { linked: map[id] || [], dismissed: dismissedMap[id] || [] }])));

  it('一页条目批量读关联状态；已在库显示「播放」「解除」，自动连上的带「自动匹配」', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(3, '沙丘', { year: 2021 }), entry(2, '星际穿越', { year: 2014 }), entry(1, '没入库的片')], next_id: 0 });
    api.ListWatchlistLibraryState.mockImplementation(stateOf({
      3: [linkedVideo(30, '沙丘 (2021)', 'auto', { is_watched: true })],
      2: [linkedVideo(20, '星际穿越', 'manual')]
    }));
    const wrapper = await page();
    expect(api.ListWatchlistLibraryState).toHaveBeenCalledTimes(1);
    expect(api.ListWatchlistLibraryState).toHaveBeenCalledWith([3, 2, 1]);

    expect(find(wrapper, 'linked-3').text()).toContain('已在库');
    expect(find(wrapper, 'linked-3').text()).toContain('沙丘 (2021)');
    expect(find(wrapper, 'linked-3').text()).toContain('看过');
    expect(find(wrapper, 'auto-3-30').text()).toBe('自动匹配');
    expect(wrapper.find('[data-test="watchlist-auto-2-20"]').exists()).toBe(false);
    expect(find(wrapper, 'linked-2').text()).not.toContain('看过');
    // 已在库与不在库互斥；两样都读到了、什么也没有的才是「不在库」。
    expect(wrapper.find('[data-test="watchlist-not-in-library-3"]').exists()).toBe(false);
    expect(find(wrapper, 'not-in-library-1').text()).toBe('不在库');

    await find(wrapper, 'play-3-30').trigger('click');
    expect(wrapper.emitted('open-video-id')).toEqual([[30]]);
  });

  it('「解除」不弹确认，提示里的「撤销」把它确认回来；解除过的这部不再列在「可能已有」里', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(3, '沙丘', { year: 2021 })], next_id: 0 });
    api.SuggestLibraryMatchesBatch.mockResolvedValue({ '沙丘|2021': [{ video_id: 30, name: 'Dune.mkv', display_title: '沙丘 (2021)' }] });
    const linked = { 3: [linkedVideo(30, '沙丘 (2021)')] };
    const dismissed = {};
    api.ListWatchlistLibraryState.mockImplementation(ids => stateOf(linked, dismissed)(ids));
    const wrapper = await page();
    // 已关联的那部不再列在「可能已有」里。
    expect(wrapper.find('[data-test="watchlist-suggestions-3"]').exists()).toBe(false);

    // 后端记下这部作品解除过（dismissed 含它的各个版本）。
    let resolveReload;
    api.UnlinkWatchlistEntryVideo.mockImplementation(async () => { linked[3] = []; dismissed[3] = [30, 31]; });
    api.ListWatchlistLibraryState.mockImplementationOnce(ids => new Promise(done => { resolveReload = () => done(stateOf(linked, dismissed)(ids)); }));
    await find(wrapper, 'unlink-3-30').trigger('click');
    await flushPromises();
    // 重读还没回来时也不闪出「可能已有」。
    expect(wrapper.find('[data-test="watchlist-suggestions-3"]').exists()).toBe(false);
    resolveReload();
    await flushPromises();
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.UnlinkWatchlistEntryVideo).toHaveBeenCalledWith(3, 30);
    expect(wrapper.find('[data-test="watchlist-linked-3"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="watchlist-suggestions-3"]').exists()).toBe(false);
    expect(find(wrapper, 'not-in-library-3').text()).toBe('不在库');
    expect(feedback.notifyUndo).toHaveBeenCalledTimes(1);
    const [message, undo] = feedback.notifyUndo.mock.calls[0];
    expect(message).toContain('已解除「沙丘」与「沙丘 (2021)」的关联');

    // 撤销 = 重新确认这一对；后端同时清掉这部作品的解除记录。
    api.LinkWatchlistEntryVideo.mockImplementation(async () => { linked[3] = [linkedVideo(30, '沙丘 (2021)', 'manual')]; dismissed[3] = []; });
    await undo();
    await flushPromises();
    expect(api.LinkWatchlistEntryVideo).toHaveBeenCalledWith(3, 30);
    expect(find(wrapper, 'linked-3').text()).toContain('沙丘 (2021)');
    expect(wrapper.find('[data-test="watchlist-auto-3-30"]').exists()).toBe(false);
  });

  it('「片库中可能已有」滤掉这个条目解除过的视频（其余建议照常列出）', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(3, '沙丘', { year: 2021 }), entry(4, '沙丘', { year: 2021 })], next_id: 0 });
    api.SuggestLibraryMatchesBatch.mockResolvedValue({
      '沙丘|2021': [{ video_id: 30, name: 'Dune.mkv', display_title: '沙丘 (2021)' }, { video_id: 33, name: 'Dune.Remux.mkv', display_title: '沙丘 Remux' }]
    });
    api.ListWatchlistLibraryState.mockImplementation(stateOf({}, { 3: [30] }));
    const wrapper = await page();
    expect(wrapper.find('[data-test="watchlist-suggestion-3-30"]').exists()).toBe(false);
    expect(find(wrapper, 'suggestion-3-33').text()).toContain('沙丘 Remux');
    // 解除记录按条目：另一个条目照常建议这部。
    expect(find(wrapper, 'suggestion-4-30').exists()).toBe(true);

    // 全被解除过时不再有「可能已有」，读全了就是「不在库」。
    api.ListWatchlistLibraryState.mockImplementation(stateOf({}, { 3: [30, 33] }));
    await wrapper.vm.loadLibrary([wrapper.vm.entries[0]]);
    await flushPromises();
    expect(wrapper.find('[data-test="watchlist-suggestions-3"]').exists()).toBe(false);
    expect(find(wrapper, 'not-in-library-3').text()).toBe('不在库');
  });

  it('解除或确认失败：报错并保持原状，不出撤销提示', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(3, '沙丘', { year: 2021 }), entry(2, '星际穿越')], next_id: 0 });
    api.SuggestLibraryMatchesBatch.mockResolvedValue({ '星际穿越|0': [{ video_id: 20, name: 'Interstellar.mkv', display_title: '' }] });
    api.ListWatchlistLibraryState.mockImplementation(stateOf({ 3: [linkedVideo(30, '沙丘')] }));
    const wrapper = await page();
    api.UnlinkWatchlistEntryVideo.mockRejectedValueOnce('数据库忙');
    await find(wrapper, 'unlink-3-30').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'error').text()).toContain('解除关联失败：数据库忙');
    expect(find(wrapper, 'linked-3').exists()).toBe(true);
    expect(feedback.notifyUndo).not.toHaveBeenCalled();

    api.LinkWatchlistEntryVideo.mockRejectedValueOnce('视频已不在片库');
    await find(wrapper, 'link-2-20').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'error').text()).toContain('关联片库视频失败：视频已不在片库');
    expect(wrapper.find('[data-test="watchlist-linked-2"]').exists()).toBe(false);
    expect(find(wrapper, 'suggestions-2').exists()).toBe(true);
  });

  it('关联状态读失败：横幅 + 重试，片单照常，不误报「不在库」', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(3, '沙丘')], next_id: 0 });
    api.ListWatchlistLibraryState.mockRejectedValueOnce(new Error('数据库忙'));
    const wrapper = await page();
    expect(wrapper.findAll('[data-test="watchlist-entry"]')).toHaveLength(1);
    expect(find(wrapper, 'library-error').text()).toContain('数据库忙');
    expect(wrapper.find('[data-test="watchlist-not-in-library-3"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="watchlist-library-loading-3"]').exists()).toBe(false);

    await find(wrapper, 'library-retry').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="watchlist-library-error"]').exists()).toBe(false);
    expect(find(wrapper, 'not-in-library-3').exists()).toBe(true);
  });

  it('确认成功但随后重读失败：保留刚确认的「已在库」，横幅说明，不退回建议', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(2, '星际穿越')], next_id: 0 });
    api.SuggestLibraryMatchesBatch.mockResolvedValue({ '星际穿越|0': [{ video_id: 20, name: 'Interstellar.mkv', display_title: '' }] });
    const wrapper = await page();
    api.LinkWatchlistEntryVideo.mockResolvedValueOnce(undefined);
    api.ListWatchlistLibraryState.mockRejectedValueOnce('数据库忙');
    await find(wrapper, 'link-2-20').trigger('click');
    await flushPromises();
    expect(find(wrapper, 'linked-2').text()).toContain('Interstellar.mkv');
    expect(wrapper.find('[data-test="watchlist-suggestions-2"]').exists()).toBe(false);
    expect(find(wrapper, 'library-error').text()).toContain('数据库忙');
  });

  it('读关联状态期间这一行显示骨架，读完换成结果', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(3, '沙丘')], next_id: 0 });
    const pending = deferred();
    api.ListWatchlistLibraryState.mockReturnValueOnce(pending.promise);
    const wrapper = await page();
    expect(find(wrapper, 'library-loading-3').exists()).toBe(true);
    expect(wrapper.find('[data-test="watchlist-not-in-library-3"]').exists()).toBe(false);
    pending.resolve({ 3: { linked: [linkedVideo(30, '沙丘')] } });
    await flushPromises();
    expect(wrapper.find('[data-test="watchlist-library-loading-3"]').exists()).toBe(false);
    expect(find(wrapper, 'linked-3').exists()).toBe(true);
  });

  it('翻页只查新的一页，先发的第一页晚到也不丢；同一条目后发的结果不被先发的覆盖', async () => {
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(9, '第一页')], next_id: 9 });
    const firstPage = deferred();
    api.ListWatchlistLibraryState.mockReturnValueOnce(firstPage.promise);
    const wrapper = await page();
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(4, '第二页')], next_id: 0 });
    api.ListWatchlistLibraryState.mockImplementationOnce(stateOf({ 4: [linkedVideo(40, '第二页的片')] }));
    await find(wrapper, 'more').trigger('click');
    await flushPromises();
    expect(api.ListWatchlistLibraryState).toHaveBeenLastCalledWith([4]);
    firstPage.resolve({ 9: { linked: [linkedVideo(90, '第一页的片')] } });
    await flushPromises();
    expect(find(wrapper, 'linked-9').text()).toContain('第一页的片');
    expect(find(wrapper, 'linked-4').text()).toContain('第二页的片');

    // 同一条目：先发的慢请求晚到，不盖掉后发的结果。
    const slow = deferred();
    api.ListWatchlistLibraryState.mockReturnValueOnce(slow.promise).mockImplementationOnce(stateOf({}));
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(9, '第一页')], next_id: 0 });
    progressHandler({ entry_id: 9, status: 'succeeded' });
    await flushPromises();
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(9, '第一页')], next_id: 0 });
    progressHandler({ entry_id: 9, status: 'succeeded' });
    await flushPromises();
    expect(wrapper.find('[data-test="watchlist-linked-9"]').exists()).toBe(false);
    slow.resolve({ 9: { linked: [linkedVideo(90, '旧结果')] } });
    await flushPromises();
    expect(wrapper.find('[data-test="watchlist-linked-9"]').exists()).toBe(false);
  });

  it('补全成功后重读这一行的关联：后端同步跑过的自动匹配立刻看得到', async () => {
    api.ListWatchlist.mockResolvedValue({ entries: [entry(7, '沙丘', { enrichment_status: 'running' }), entry(6, '别的片')], next_id: 0 });
    const wrapper = await page();
    expect(find(wrapper, 'not-in-library-7').exists()).toBe(true);
    api.ListWatchlist.mockResolvedValueOnce({ entries: [entry(7, '沙丘', { enrichment_status: 'succeeded', year: 2021 })], next_id: 0 });
    api.ListWatchlistLibraryState.mockImplementationOnce(stateOf({ 7: [linkedVideo(70, '沙丘')] }));
    progressHandler({ entry_id: 7, status: 'succeeded', source_name: 'douban' });
    await flushPromises();
    expect(api.ListWatchlistLibraryState).toHaveBeenLastCalledWith([7]);
    expect(api.SuggestLibraryMatchesBatch).toHaveBeenLastCalledWith([{ title: '沙丘', year: 2021 }]);
    expect(find(wrapper, 'auto-7-70').text()).toBe('自动匹配');
    expect(find(wrapper, 'not-in-library-6').exists()).toBe(true);
  });
});

describe('嵌在片单页里（D-P2-04）', () => {
  it('embedded 时不显示自己的页头，其余照常；单独使用时页头还在', async () => {
    const standalone = await page();
    expect(standalone.find('h1').text()).toBe('想看');
    expect(standalone.classes()).toContain('page-content');
    const embedded = await page({ embedded: true });
    expect(embedded.find('h1').exists()).toBe(false);
    expect(embedded.classes()).not.toContain('page-content');
    expect(embedded.find('[data-test="watchlist-form"]').exists()).toBe(true);
  });
});
