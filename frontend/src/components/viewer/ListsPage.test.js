import { shallowMount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';

import ListsPage from './ListsPage.vue';

const wrappers = [];
afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
});

// 四个标签页是桩（各自的测试在 components/*Page.test.js）；页头与标签用真的，才点得到标签。
function mountLists(props = {}) {
  const wrapper = shallowMount(ListsPage, { props, global: { stubs: { PageHeader: false, AppTabs: false } } });
  wrappers.push(wrapper);
  return wrapper;
}
const child = (wrapper, name) => wrapper.findComponent({ name });

describe('片单（D-P2-04）', () => {
  it('页头「片单」+ 四个标签，默认「想看」；副标题跟着当前标签换成原来那一页的说明', async () => {
    const wrapper = mountLists();
    expect(wrapper.get('h1').text()).toBe('片单');
    expect(wrapper.findAll('[role="tab"]').map(tab => tab.text())).toEqual(['想看', '榜单', '观影记录', '观看笔记']);
    expect(wrapper.get('[data-test="lists-tab-watchlist"]').attributes('aria-selected')).toBe('true');
    expect(wrapper.get('.ui-page-header__subtitle').text()).toContain('记下想看');

    await wrapper.setProps({ tab: 'movie-chart' });
    expect(wrapper.get('[data-test="lists-tab-movie-chart"]').attributes('aria-selected')).toBe('true');
    expect(wrapper.get('.ui-page-header__subtitle').text()).toContain('按年份浏览');
    await wrapper.setProps({ tab: 'watched-movies' });
    expect(wrapper.get('.ui-page-header__subtitle').text()).toContain('标过「已看」');
    await wrapper.setProps({ tab: 'viewing-notes' });
    expect(wrapper.get('.ui-page-header__subtitle').text()).toContain('观影日记');
  });

  it('标签是页面参数：点标签只发 update:tab，由宿主落下；认不出的标签按「想看」显示', async () => {
    const wrapper = mountLists({ tab: 'watchlist' });
    await wrapper.get('[data-test="lists-tab-watched-movies"]').trigger('click');
    expect(wrapper.emitted('update:tab')).toEqual([['watched-movies']]);
    expect(child(wrapper, 'WatchlistPage').exists()).toBe(true);
    expect(child(wrapper, 'WatchedMoviesPage').exists()).toBe(false);

    await wrapper.setProps({ tab: 'no-such-tab' });
    expect(wrapper.get('[data-test="lists-tab-watchlist"]').attributes('aria-selected')).toBe('true');
    expect(child(wrapper, 'WatchlistPage').exists()).toBe(true);
  });

  it('标签面板与页签互相指向（tabpanel / aria-labelledby），四页都以 embedded 嵌入', async () => {
    const wrapper = mountLists({ tab: 'watchlist' });
    const panel = wrapper.get('#lists-panel-watchlist');
    expect(panel.attributes('role')).toBe('tabpanel');
    expect(panel.attributes('aria-labelledby')).toBe('lists-tab-watchlist');
    expect(wrapper.get('#lists-tab-watchlist').attributes('aria-controls')).toBe('lists-panel-watchlist');
    for (const [tab, name] of [['watchlist', 'WatchlistPage'], ['movie-chart', 'MovieChartPage'], ['watched-movies', 'WatchedMoviesPage'], ['viewing-notes', 'ViewingNotesPage']]) {
      await wrapper.setProps({ tab });
      expect(child(wrapper, name).props('embedded'), name).toBe(true);
    }
  });

  // 原来四页的挂载方式不变：榜单一挂载就报 OpenMovieChartYear，不在这个标签上不能挂；观影记录每次进来重读；
  // 想看每次进来重读；观看笔记首次进入才挂载、之后只隐藏。
  it('想看、榜单、观影记录在自己的标签上且页面可见时才挂载，切走即卸载', async () => {
    const wrapper = mountLists({ tab: 'watchlist' });
    expect(child(wrapper, 'WatchlistPage').exists()).toBe(true);
    expect(child(wrapper, 'MovieChartPage').exists()).toBe(false);
    expect(child(wrapper, 'WatchedMoviesPage').exists()).toBe(false);
    expect(child(wrapper, 'ViewingNotesPage').exists()).toBe(false);

    await wrapper.setProps({ tab: 'movie-chart' });
    expect(child(wrapper, 'MovieChartPage').exists()).toBe(true);
    expect(child(wrapper, 'WatchlistPage').exists()).toBe(false);

    // 离开片单页（例如进了整页详情）：标签留着，但这一页卸载，回来重新挂载。
    await wrapper.setProps({ pageActive: false });
    expect(child(wrapper, 'MovieChartPage').exists()).toBe(false);
    await wrapper.setProps({ pageActive: true });
    expect(child(wrapper, 'MovieChartPage').exists()).toBe(true);

    await wrapper.setProps({ tab: 'watched-movies' });
    expect(child(wrapper, 'WatchedMoviesPage').exists()).toBe(true);
    expect(child(wrapper, 'MovieChartPage').exists()).toBe(false);
  });

  it('观看笔记首次进入才挂载，之后切标签、离开片单都只隐藏，回来是同一个实例', async () => {
    const wrapper = mountLists({ tab: 'viewing-notes', pageActive: false });
    expect(child(wrapper, 'ViewingNotesPage').exists()).toBe(false);
    await wrapper.setProps({ pageActive: true });
    const notes = child(wrapper, 'ViewingNotesPage');
    expect(notes.props('pageActive')).toBe(true);

    await wrapper.setProps({ tab: 'watchlist' });
    expect(child(wrapper, 'ViewingNotesPage').vm).toBe(notes.vm);
    expect(child(wrapper, 'ViewingNotesPage').props('pageActive')).toBe(false);
    expect(child(wrapper, 'ViewingNotesPage').isVisible()).toBe(false);

    await wrapper.setProps({ pageActive: false, tab: 'viewing-notes' });
    expect(child(wrapper, 'ViewingNotesPage').props('pageActive')).toBe(false);
    await wrapper.setProps({ pageActive: true });
    expect(child(wrapper, 'ViewingNotesPage').vm).toBe(notes.vm);
    expect(child(wrapper, 'ViewingNotesPage').props('pageActive')).toBe(true);
  });

  it('子页面的事件原样转给宿主：播放、打开视频、书签、去榜单', async () => {
    const wrapper = mountLists({ tab: 'watchlist' });
    child(wrapper, 'WatchlistPage').vm.$emit('open-video-id', 30);

    await wrapper.setProps({ tab: 'watched-movies' });
    child(wrapper, 'WatchedMoviesPage').vm.$emit('navigate', 'movie-chart');
    child(wrapper, 'WatchedMoviesPage').vm.$emit('open-video', { id: 7 });

    await wrapper.setProps({ tab: 'viewing-notes' });
    const resolution = { status: 'ready', video: { id: 8 } };
    child(wrapper, 'ViewingNotesPage').vm.$emit('open-bookmark', resolution);
    child(wrapper, 'ViewingNotesPage').vm.$emit('open-video', { id: 9 });
    child(wrapper, 'ViewingNotesPage').vm.$emit('open-video-id', 10);

    expect(wrapper.emitted('open-video-id')).toEqual([[30], [10]]);
    expect(wrapper.emitted('navigate')).toEqual([['movie-chart']]);
    expect(wrapper.emitted('open-video')).toEqual([[{ id: 7 }], [{ id: 9 }]]);
    expect(wrapper.emitted('open-bookmark')).toEqual([[resolution]]);
  });
});

it('transfers resource search title without changing the watchlist tab', () => { const w = mountLists(); child(w, 'WatchlistPage').vm.$emit('find-resources', '沙丘'); expect(w.emitted('find-resources')).toEqual([['沙丘']]); expect(w.emitted('update:tab')).toBeUndefined(); });
