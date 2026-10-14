import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  ListPeople: vi.fn(),
  ListPeopleByCount: vi.fn(),
  ListCollections: vi.fn(),
  ListCollectionProgress: vi.fn(),
  GetPersonDetail: vi.fn(),
  GetPersonImages: vi.fn(),
  SearchLibraryVideoPage: vi.fn(),
  GetCollectionDetail: vi.fn(),
  GetPlaybackQueue: vi.fn(),
  EditPlaybackQueue: vi.fn(),
  PlayPlaybackQueue: vi.fn()
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import EntityBrowsePage, {
  collectionProgressText, collectionWorkRows, ENTITY_PAGE_SIZE, nextWorkKey, playNextRequest, SEARCH_DEBOUNCE_MS, watchStateOf
} from './EntityBrowsePage.vue';

enableAutoUnmount(afterEach);

const person = (id, extra = {}) => ({
  person: { id, display_name: `人物${id}`, original_name: '' }, avatar_url: '', active_video_count: 3, active_image_count: 2, cursor_name: `人物${id}`, ...extra
});
const collection = (id, extra = {}) => ({
  collection: { id, name: `作品集${id}`, description: '' }, cover_url: '', active_video_count: 3, cursor_name: `作品集${id}`, ...extra
});
const video = (id, extra = {}) => ({
  id, name: `v${id}.mp4`, display_title: `第${id}部`, path: `/m/v${id}.mp4`, duration: 3600, year: 2000 + id,
  is_watched: false, watch_position_seconds: 0, is_stale: false, ...extra
});
const member = (id, position, extra = {}) => ({ video: video(id, extra), position });
const work = (index, ids, state = 'unwatched', extra = {}) => ({
  work_index: index, video_ids: ids, representative_video_id: ids[0], state, resume_video_id: 0, ...extra
});
// 默认的作品集 5：成员 1（看过）、2、4（离线）、3，逐部明细与 GetCollectionDetail 的默认成员一致。
const progressOf = (id, extra = {}) => ({
  collection_id: id, name: `作品集${id}`, has_cover: false, total_works: 3, watched_works: 1, in_progress_works: 0, complete: false,
  next: { video_id: 2, title: '第2部', work_index: 2, resume: false, watch_position_seconds: 0, duration: 3600 },
  works: [work(1, [1], 'watched'), work(2, [2]), work(3, [3])],
  offline_video_ids: [4],
  ...extra
});

beforeEach(() => {
  vi.useRealTimers();
  for (const fn of Object.values(api)) fn.mockReset();
  api.ListPeople.mockResolvedValue([person(1), person(2, { avatar_url: '/preview/person-avatar/2' })]);
  api.ListPeopleByCount.mockResolvedValue([person(2, { avatar_url: '/preview/person-avatar/2', active_video_count: 9 }), person(1)]);
  api.ListCollections.mockResolvedValue([collection(5), collection(6)]);
  api.ListCollectionProgress.mockImplementation(({ collection_ids: ids }) => Promise.resolve(ids.map(id => progressOf(id))));
  api.GetPersonDetail.mockResolvedValue({ person: person(8, { active_video_count: 12, active_image_count: 4 }), videos: [], images: [] });
  api.SearchLibraryVideoPage.mockResolvedValue({ videos: [video(21), video(22, { is_watched: true })], version_groups: {} });
  api.GetPersonImages.mockResolvedValue({ images: [{ id: 41, name: 'a.jpg', width: 800, height: 600 }], next_image_id: 0 });
  api.GetCollectionDetail.mockResolvedValue({
    collection: collection(5, { collection: { id: 5, name: '三部曲', description: '一套片' } }),
    videos: [member(1, 1, { is_watched: true }), member(2, 2), member(4, 3, { is_stale: true }), member(3, 4)]
  });
  api.GetPlaybackQueue.mockResolvedValue({ total: 0, state: { revision: 3 }, items: [] });
  api.EditPlaybackQueue.mockResolvedValue(undefined);
});

async function mountPage(props) {
  const wrapper = mount(EntityBrowsePage, { props });
  await flushPromises();
  return wrapper;
}

const text = wrapper => wrapper.text();

describe('pure helpers', () => {
  it('watchStateOf：任一版本看过即看过；都没看过、有续播位置即在看', () => {
    expect(watchStateOf([video(1), video(2, { is_watched: true })])).toBe('watched');
    expect(watchStateOf([video(1), video(2, { watch_position_seconds: 5 })])).toBe('in-progress');
    expect(watchStateOf(video(1))).toBe('unwatched');
  });

  it('collectionProgressText：看完、没开始、接着看第几部、没有在线作品', () => {
    expect(collectionProgressText(null)).toBe('');
    expect(collectionProgressText(progressOf(5, { total_works: 0, watched_works: 0, next: undefined }))).toBe('还没有在线的作品');
    expect(collectionProgressText(progressOf(5, { complete: true, watched_works: 3, next: undefined }))).toBe('已看完 · 共 3 部');
    expect(collectionProgressText(progressOf(5, { watched_works: 0 }))).toBe('共 3 部 · 还没开始看');
    expect(collectionProgressText(progressOf(5))).toBe('第 2 / 3 部');
    expect(collectionProgressText(progressOf(5, { watched_works: 0, in_progress_works: 1, next: { video_id: 1, work_index: 1, resume: true } }))).toBe('第 1 / 3 部');
  });

  it('playNextRequest：在看的那一部从续播位置起播，其余从头；没有下一部为 null', () => {
    expect(playNextRequest(progressOf(5))).toEqual({ id: 2, startAtMs: 0 });
    expect(playNextRequest(progressOf(5, { next: { video_id: 7, work_index: 2, resume: true, watch_position_seconds: 754.25 } }))).toEqual({ id: 7, startAtMs: 754250 });
    expect(playNextRequest(progressOf(5, { complete: true, next: undefined }))).toBeNull();
    expect(playNextRequest(null)).toBeNull();
  });

  it('collectionWorkRows：结构来自逐部明细；离线成员按作品集位置插回原处、不编号；版本合成一部；在看那一部打开有断点的成员', () => {
    const members = [
      member(10, 1, { is_watched: true }),
      member(12, 2),
      member(30, 3, { is_stale: true }),
      member(20, 4),
      member(11, 5, { watch_position_seconds: 90 })
    ];
    // 12 与 11 是同一部的两个版本（11 是主版本，组的位置取最先出现的 12 那里）；11 有断点。
    const progress = {
      works: [
        work(1, [10], 'watched'),
        work(2, [11, 12], 'in_progress', { representative_video_id: 11, resume_video_id: 11 }),
        work(3, [20])
      ],
      offline_video_ids: [30],
      next: { video_id: 11, work_index: 2, resume: true }
    };
    const rows = collectionWorkRows(progress, members);
    expect(rows.map(row => [row.index, row.offline, row.videos.map(item => item.id), row.state])).toEqual([
      [1, false, [10], 'watched'],
      [2, false, [11, 12], 'in-progress'],
      [0, true, [30], 'offline'],
      [3, false, [20], 'unwatched']
    ]);
    expect(rows[1].representative.display_title).toBe('第11部');
    expect(rows[1].openID).toBe(11);
    expect(rows[1].resumeVideo.id).toBe(11);
    expect(rows[3].openID).toBe(20);
    expect(nextWorkKey(rows, progress)).toBe(rows[1].key);
    expect(nextWorkKey(rows, { next: { video_id: 30 } })).toBe('');
    expect(nextWorkKey(rows, null)).toBe('');
  });

  it('collectionWorkRows：在看那一部的断点在非主版本上时打开那个版本；作品集详情里没有的成员排在最后、保留接口顺序', () => {
    const members = [member(1, 1), member(2, 2, { watch_position_seconds: 20 })];
    const rows = collectionWorkRows({
      works: [work(1, [1, 2], 'in_progress', { resume_video_id: 2 }), work(2, [7])],
      offline_video_ids: [8]
    }, members);
    expect(rows.map(row => [row.key, row.openID])).toEqual([['work-1', 2], ['work-2', 7], ['offline-8', 8]]);
    expect(rows[1].representative).toEqual({ id: 7 });
    expect(collectionWorkRows(null, members)).toEqual([]);
  });
});

describe('人物观看页', () => {
  it('头像墙默认按出现次数（ListPeopleByCount），卡片写作品数；骨架只在首次加载时出现', async () => {
    let resolve;
    api.ListPeopleByCount.mockReturnValueOnce(new Promise(done => { resolve = done; }));
    const wrapper = mount(EntityBrowsePage, { props: { kind: 'person' } });
    await flushPromises();
    expect(wrapper.find('[data-test="entity-browse-list-loading"]').exists()).toBe(true);
    resolve([person(2, { active_video_count: 9 }), person(1)]);
    await flushPromises();
    expect(api.ListPeopleByCount).toHaveBeenCalledWith('', 0, '', 0, ENTITY_PAGE_SIZE);
    expect(api.ListPeople).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="entity-browse-list-loading"]').exists()).toBe(false);
    expect(wrapper.findAll('.entity-browse__person strong').map(item => item.text())).toEqual(['人物2', '人物1']);
    expect(wrapper.get('[data-test="entity-browse-person-1"]').text()).toContain('3 部视频 · 2 张图片');
    expect(wrapper.get('[data-test="entity-browse-sort-count"]').attributes('aria-checked')).toBe('true');
    expect(wrapper.find('h1').text()).toBe('人物');
  });

  it('按出现次数满一批才有下一批：游标是最后一项的（次数, 名称, ID）', async () => {
    const page = Array.from({ length: ENTITY_PAGE_SIZE }, (_, index) => person(index + 1, { active_video_count: 100 - index }));
    api.ListPeopleByCount.mockResolvedValueOnce(page).mockResolvedValueOnce([person(99, { active_video_count: 1 })]);
    const wrapper = await mountPage({ kind: 'person' });
    await wrapper.get('[data-test="entity-browse-list-more"]').trigger('click');
    await flushPromises();
    expect(api.ListPeopleByCount).toHaveBeenLastCalledWith('', 100 - ENTITY_PAGE_SIZE + 1, `人物${ENTITY_PAGE_SIZE}`, ENTITY_PAGE_SIZE, ENTITY_PAGE_SIZE);
    expect(wrapper.find('[data-test="entity-browse-person-99"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="entity-browse-list-more"]').exists()).toBe(false);
  });

  it('切到「名称」改用 ListPeople（名称游标续读），切回「出现次数」从头按次数重取', async () => {
    const wrapper = await mountPage({ kind: 'person' });
    const page = Array.from({ length: ENTITY_PAGE_SIZE }, (_, index) => person(index + 1));
    api.ListPeople.mockResolvedValueOnce(page).mockResolvedValueOnce([person(99)]);
    await wrapper.get('[data-test="entity-browse-sort-name"]').trigger('click');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledWith('', '', 0, ENTITY_PAGE_SIZE);
    expect(wrapper.findAll('.entity-browse__person')).toHaveLength(ENTITY_PAGE_SIZE);
    expect(wrapper.get('[data-test="entity-browse-list"]').text()).toContain('按名称排列');
    await wrapper.get('[data-test="entity-browse-list-more"]').trigger('click');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenLastCalledWith('', `人物${ENTITY_PAGE_SIZE}`, ENTITY_PAGE_SIZE, ENTITY_PAGE_SIZE);

    api.ListPeopleByCount.mockClear();
    await wrapper.get('[data-test="entity-browse-sort-count"]').trigger('click');
    await flushPromises();
    expect(api.ListPeopleByCount).toHaveBeenCalledWith('', 0, '', 0, ENTITY_PAGE_SIZE);
    expect(wrapper.findAll('.entity-browse__person strong').map(item => item.text())).toEqual(['人物2', '人物1']);
  });

  it('换排序时上一种排序晚到的那一批丢掉', async () => {
    let resolveSlow;
    api.ListPeopleByCount.mockReturnValueOnce(new Promise(done => { resolveSlow = done; }));
    const wrapper = mount(EntityBrowsePage, { props: { kind: 'person' } });
    await flushPromises();
    api.ListPeople.mockResolvedValueOnce([person(5)]);
    await wrapper.get('[data-test="entity-browse-sort-name"]').trigger('click');
    await flushPromises();
    resolveSlow([person(77)]);
    await flushPromises();
    expect(wrapper.find('[data-test="entity-browse-person-77"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="entity-browse-person-5"]').exists()).toBe(true);
  });

  it('搜索去抖后按关键词重取（沿用当前排序）；没有结果给「清空搜索」', async () => {
    vi.useFakeTimers();
    const wrapper = mount(EntityBrowsePage, { props: { kind: 'person' } });
    await flushPromises();
    api.ListPeopleByCount.mockResolvedValue([]);
    await wrapper.get('[data-test="entity-browse-search"]').setValue('甲');
    await vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS - 10);
    expect(api.ListPeopleByCount).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(20);
    await flushPromises();
    expect(api.ListPeopleByCount).toHaveBeenLastCalledWith('甲', 0, '', 0, ENTITY_PAGE_SIZE);
    expect(api.ListPeopleByCount).toHaveBeenCalledTimes(2);
    const empty = wrapper.get('[data-test="entity-browse-list-empty"]');
    expect(empty.text()).toContain('没有找到「甲」');
    api.ListPeopleByCount.mockResolvedValue([person(1)]);
    await wrapper.get('[data-test="entity-browse-clear-search"]').trigger('click');
    await vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS + 10);
    await flushPromises();
    expect(api.ListPeopleByCount).toHaveBeenLastCalledWith('', 0, '', 0, ENTITY_PAGE_SIZE);
    // 清空后去抖到点不再重复取一次。
    expect(api.ListPeopleByCount).toHaveBeenCalledTimes(3);
    expect(wrapper.find('[data-test="entity-browse-person-1"]').exists()).toBe(true);
    vi.useRealTimers();
  });

  it('关键词交接（命令面板「查看全部」）：挂载时直接按它搜；停在人物页时回到列表再搜；同一个词再交一次也重搜', async () => {
    vi.useFakeTimers();
    const wrapper = mount(EntityBrowsePage, { props: { kind: 'person', keywordRequest: { keyword: '梁', token: 1 } } });
    await flushPromises();
    expect(api.ListPeopleByCount).toHaveBeenCalledTimes(1);
    expect(api.ListPeopleByCount).toHaveBeenCalledWith('梁', 0, '', 0, ENTITY_PAGE_SIZE);
    expect(wrapper.get('[data-test="entity-browse-search"]').element.value).toBe('梁');

    await wrapper.get('[data-test="entity-browse-person-2"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="entity-person-hero"]').exists()).toBe(true);

    await wrapper.setProps({ keywordRequest: { keyword: '张', token: 2 } });
    await flushPromises();
    expect(wrapper.find('[data-test="entity-person-hero"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="entity-browse-list"]').isVisible()).toBe(true);
    expect(api.ListPeopleByCount).toHaveBeenLastCalledWith('张', 0, '', 0, ENTITY_PAGE_SIZE);
    expect(wrapper.get('[data-test="entity-browse-search"]').element.value).toBe('张');

    await wrapper.setProps({ keywordRequest: null });
    await wrapper.setProps({ keywordRequest: { keyword: '张', token: 3 } });
    await vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS + 10);
    await flushPromises();
    // 每次交接各搜一次（共 3 次），去抖不再多发。
    expect(api.ListPeopleByCount).toHaveBeenCalledTimes(3);
    vi.useRealTimers();
  });

  it('一个人物都没有：空状态的下一步去管理侧', async () => {
    api.ListPeopleByCount.mockResolvedValue([]);
    const wrapper = await mountPage({ kind: 'person' });
    expect(wrapper.get('[data-test="entity-browse-list-empty"]').text()).toContain('还没有人物');
    await wrapper.get('[data-test="entity-browse-go-manage"]').trigger('click');
    expect(wrapper.emitted('edit-in-manage')).toEqual([[{ kind: 'person' }]]);
  });

  it('列表读取失败：横幅 + 重试', async () => {
    api.ListPeopleByCount.mockRejectedValueOnce(new Error('后端不可用'));
    const wrapper = await mountPage({ kind: 'person' });
    expect(wrapper.get('[data-test="entity-browse-list-error"]').text()).toContain('读取人物失败：后端不可用');
    expect(wrapper.find('[data-test="entity-browse-list-empty"]').exists()).toBe(false);
    await wrapper.get('[data-test="entity-browse-list-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="entity-browse-list-error"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="entity-browse-person-2"]').exists()).toBe(true);
  });

  it('人物页：大图头部 + 作品海报墙（合并版本、年份从新到旧）+ 相关图片；三块各自取数', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({
      videos: [video(21), video(22, { is_watched: true }), video(23, { is_stale: true })],
      version_groups: { 21: { group_id: 4, member_count: 2, members: [{ video_id: 21, is_watched: false, watch_position_seconds: 0 }, { video_id: 25, is_watched: true }] } },
      next_cursor: null
    });
    const wrapper = await mountPage({ kind: 'person' });
    await wrapper.get('[data-test="entity-browse-person-2"]').trigger('click');
    await flushPromises();

    expect(api.GetPersonDetail).toHaveBeenCalledWith(2, 0, 1);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith({ filter: { person_ids: [2], collapse_versions: true, sort_mode: 'year_desc' }, limit: 60 });
    expect(api.GetPersonImages).toHaveBeenCalledWith(2, 0, 30);
    expect(wrapper.get('[data-test="entity-browse-list"]').isVisible()).toBe(false);
    expect(wrapper.get('[data-test="entity-person-name"]').text()).toBe('人物8');
    expect(wrapper.get('[data-test="entity-person-counts"]').text()).toBe('12 部视频 · 4 张图片');

    const card = wrapper.get('[data-test="entity-person-work-21"]');
    expect(card.text()).toContain('2021 · 2 个版本');
    // 版本组按整组算：另一个版本看过，整张卡就是看过。
    expect(card.text()).toContain('看过');
    expect(wrapper.get('[data-test="entity-person-work-23"]').attributes('disabled')).toBeDefined();
    await card.trigger('click');
    expect(wrapper.emitted('open-video')).toEqual([[21]]);

    await wrapper.get('[data-test="entity-person-image-41"]').trigger('click');
    expect(wrapper.emitted('open-image')).toEqual([[{ id: 41, name: 'a.jpg', width: 800, height: 600 }]]);
  });

  it('人物页没有编辑、删除、合并、人脸审阅入口，只有「在管理中编辑」', async () => {
    const wrapper = await mountPage({ kind: 'person', focusEntity: { id: 8, name: '某人' } });
    const buttons = wrapper.findAll('button').filter(button => button.isVisible()).map(button => button.text());
    for (const word of ['删除', '合并', '人脸', '新建', '移除', '解绑', '编辑与批量关联']) {
      expect(buttons.join('|'), word).not.toContain(word);
    }
    expect(buttons.filter(label => label.includes('编辑'))).toEqual(['在管理中编辑']);
    await wrapper.get('[data-test="entity-edit-in-manage"]').trigger('click');
    expect(wrapper.emitted('edit-in-manage')).toEqual([[{ kind: 'person', id: 8, name: '人物8' }]]);
  });

  it('外部指定对象（命令面板、首页）直接打开它；返回回到列表', async () => {
    const wrapper = await mountPage({ kind: 'person', focusEntity: { id: 8, name: '某人' } });
    expect(api.GetPersonDetail).toHaveBeenCalledWith(8, 0, 1);
    await wrapper.setProps({ focusEntity: null });
    expect(wrapper.find('[data-test="entity-person-hero"]').exists()).toBe(true);
    await wrapper.get('[data-test="entity-browse-back"]').trigger('click');
    expect(wrapper.find('[data-test="entity-person-hero"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="entity-browse-list"]').isVisible()).toBe(true);

    await wrapper.setProps({ focusEntity: { id: 9, name: '另一人' } });
    await flushPromises();
    expect(api.GetPersonDetail).toHaveBeenLastCalledWith(9, 0, 1);
  });

  it('作品读取失败只影响作品这一块，图片照常显示；重试后补上', async () => {
    api.SearchLibraryVideoPage.mockRejectedValueOnce(new Error('查询超时'));
    const wrapper = await mountPage({ kind: 'person', focusEntity: { id: 8, name: '某人' } });
    expect(wrapper.get('[data-test="entity-person-works-error"]').text()).toContain('读取作品失败：查询超时');
    expect(wrapper.find('[data-test="entity-person-works-empty"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="entity-person-image-41"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="entity-person-name"]').text()).toBe('人物8');
    await wrapper.get('[data-test="entity-person-works-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="entity-person-works-error"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="entity-person-work-21"]').exists()).toBe(true);
  });

  it('作品与图片都为空：各自的空状态给下一步', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [], version_groups: {} });
    api.GetPersonImages.mockResolvedValue({ images: [], next_image_id: 0 });
    const wrapper = await mountPage({ kind: 'person', focusEntity: { id: 8, name: '某人' } });
    expect(wrapper.get('[data-test="entity-person-works-empty"]').text()).toContain('还没有关联的视频');
    await wrapper.get('[data-test="entity-person-images-go-photos"]').trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['photos']]);
  });

  // D-P2-08：相关图片的「去图片」去观看侧照片流并带上这个人（App 的 navigateTo('photos', { personId })）。
  it('相关图片「去图片」带上人物去照片流；没有图片时不出这个入口', async () => {
    const wrapper = await mountPage({ kind: 'person', focusEntity: { id: 8, name: '某人' } });
    await wrapper.get('[data-test="entity-person-images-open-photos"]').trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['photos', { personId: 8, personName: '人物8' }]]);

    api.GetPersonImages.mockResolvedValue({ images: [], next_image_id: 0 });
    const empty = await mountPage({ kind: 'person', focusEntity: { id: 9, name: '另一人' } });
    expect(empty.find('[data-test="entity-person-images-open-photos"]').exists()).toBe(false);
  });

  it('作品翻页带上游标；图片「更多」按 next_image_id 续读', async () => {
    const cursor = { sort_mode: 'year_desc', year: 2021, year_is_null: false, id: 22, score: 0, size: 0, rating_is_null: false };
    api.SearchLibraryVideoPage.mockResolvedValueOnce({ videos: [video(21), video(22)], version_groups: {}, next_cursor: cursor })
      .mockResolvedValueOnce({ videos: [video(23)], version_groups: {} });
    api.GetPersonImages.mockResolvedValueOnce({ images: [{ id: 41, name: 'a.jpg' }], next_image_id: 41 })
      .mockResolvedValueOnce({ images: [{ id: 40, name: 'b.jpg' }], next_image_id: 0 });
    const wrapper = await mountPage({ kind: 'person', focusEntity: { id: 8, name: '某人' } });
    await wrapper.get('[data-test="entity-person-works-more"]').trigger('click');
    await flushPromises();
    expect(api.SearchLibraryVideoPage).toHaveBeenLastCalledWith({ filter: { person_ids: [8], collapse_versions: true, sort_mode: 'year_desc' }, limit: 60, cursor });
    expect(wrapper.findAll('[data-test^="entity-person-work-"]')).toHaveLength(3);
    expect(wrapper.find('[data-test="entity-person-works-more"]').exists()).toBe(false);

    await wrapper.get('[data-test="entity-person-images-more"]').trigger('click');
    await flushPromises();
    expect(api.GetPersonImages).toHaveBeenLastCalledWith(8, 41, 30);
    expect(wrapper.find('[data-test="entity-person-image-40"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="entity-person-images-more"]').exists()).toBe(false);
  });

  it('换了对象之后，上一个对象晚到的结果丢掉', async () => {
    let resolveFirst;
    api.SearchLibraryVideoPage.mockReturnValueOnce(new Promise(done => { resolveFirst = done; }));
    const wrapper = await mountPage({ kind: 'person', focusEntity: { id: 8, name: '某人' } });
    await wrapper.setProps({ focusEntity: { id: 9, name: '另一人' } });
    await flushPromises();
    resolveFirst({ videos: [video(77)], version_groups: {} });
    await flushPromises();
    expect(wrapper.find('[data-test="entity-person-work-77"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="entity-person-work-21"]').exists()).toBe(true);
  });
});

describe('作品集观看页', () => {
  it('封面墙：每批卡片一次取进度，卡片写「第 N / M 部」与进度条', async () => {
    api.ListCollectionProgress.mockResolvedValue([
      progressOf(5),
      progressOf(6, { watched_works: 0, in_progress_works: 0, next: { video_id: 9, work_index: 1 } })
    ]);
    const wrapper = await mountPage({ kind: 'collection' });
    expect(api.ListCollections).toHaveBeenCalledWith('', '', 0, ENTITY_PAGE_SIZE);
    expect(api.ListCollectionProgress).toHaveBeenCalledTimes(1);
    expect(api.ListCollectionProgress).toHaveBeenCalledWith({ collection_ids: [5, 6] });
    const first = wrapper.get('[data-test="entity-browse-collection-progress-5"]');
    expect(first.text()).toContain('第 2 / 3 部');
    expect(first.find('[role="progressbar"]').attributes('aria-valuenow')).toBe('33');
    const second = wrapper.get('[data-test="entity-browse-collection-progress-6"]');
    expect(second.text()).toContain('共 3 部 · 还没开始看');
    expect(second.find('[role="progressbar"]').exists()).toBe(false);
  });

  it('进度那一批失败：卡片写「进度没读到」，列表上方重试只补失败的', async () => {
    api.ListCollectionProgress.mockRejectedValueOnce(new Error('忙'));
    const wrapper = await mountPage({ kind: 'collection' });
    expect(wrapper.get('[data-test="entity-browse-collection-progress-5"]').text()).toContain('进度没读到');
    expect(wrapper.get('[data-test="entity-browse-progress-error"]').text()).toContain('有 2 个作品集的观看进度没读到');
    await wrapper.get('[data-test="entity-browse-progress-retry"]').trigger('click');
    await flushPromises();
    expect(api.ListCollectionProgress).toHaveBeenLastCalledWith({ collection_ids: [5, 6] });
    expect(wrapper.find('[data-test="entity-browse-progress-error"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="entity-browse-collection-progress-5"]').text()).toContain('第 2 / 3 部');
  });

  it('作品集页：按顺序列出各部，离线的标「离线」不编号，突出下一部；头部用进度接口的部数', async () => {
    const wrapper = await mountPage({ kind: 'collection' });
    await wrapper.get('[data-test="entity-browse-collection-5"]').trigger('click');
    await flushPromises();

    expect(api.GetCollectionDetail).toHaveBeenCalledWith(5);
    expect(api.ListCollectionProgress).toHaveBeenLastCalledWith({ collection_ids: [5], include_works: true });
    // 各部的结构一次取到，不再逐个成员查版本组。
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="entity-collection-name"]').text()).toBe('三部曲');
    expect(wrapper.get('[data-test="entity-collection-progress-text"]').text()).toBe('第 2 / 3 部 · 下一部《第2部》');

    const rows = wrapper.findAll('.entity-browse__work');
    expect(rows.map(row => [row.get('.entity-browse__work-index').text(), row.get('.entity-browse__work-copy strong').text()])).toEqual([
      ['1', '第1部'], ['2', '第2部'], ['—', '第4部'], ['3', '第3部']
    ]);
    expect(wrapper.get('[data-test="entity-collection-state-1"]').text()).toBe('看过');
    expect(wrapper.get('[data-test="entity-collection-state-4"]').text()).toBe('离线');
    expect(wrapper.get('[data-test="entity-collection-work-4"]').attributes('disabled')).toBeDefined();
    const next = wrapper.get('[data-test="entity-collection-work-2"]');
    expect(next.classes()).toContain('entity-browse__work--next');
    expect(next.attributes('aria-current')).toBe('step');
    expect(next.find('[data-test="entity-collection-next-badge"]').exists()).toBe(true);
    expect(wrapper.findAll('[data-test="entity-collection-next-badge"]')).toHaveLength(1);

    await wrapper.get('[data-test="entity-collection-work-3"]').trigger('click');
    expect(wrapper.emitted('open-video')).toEqual([[3]]);
  });

  it('「从下一部播放」：在看的那一部带续播位置；「全部加入待播」走 append_collection', async () => {
    api.ListCollectionProgress.mockResolvedValue([progressOf(5, {
      watched_works: 1, in_progress_works: 1,
      next: { video_id: 2, title: '第2部', work_index: 2, resume: true, watch_position_seconds: 754, duration: 3600 },
      works: [work(1, [1], 'watched'), work(2, [2], 'in_progress', { resume_video_id: 2 }), work(3, [3])],
      offline_video_ids: []
    })]);
    api.GetCollectionDetail.mockResolvedValue({ collection: collection(5), videos: [member(1, 1, { is_watched: true }), member(2, 2, { watch_position_seconds: 754 }), member(3, 3)] });
    const wrapper = await mountPage({ kind: 'collection', focusEntity: { id: 5, name: '三部曲' } });
    expect(wrapper.get('[data-test="entity-collection-progress-text"]').text()).toBe('第 2 / 3 部 · 下一部《第2部》 · 看到 12:34');
    expect(wrapper.get('[data-test="entity-collection-state-2"]').text()).toBe('在看');
    await wrapper.get('[data-test="entity-collection-play-next"]').trigger('click');
    expect(wrapper.emitted('play-video')).toEqual([[{ id: 2, startAtMs: 754000 }]]);

    const queue = wrapper.findAll('[data-test="queue-add"]').find(button => button.text() === '全部加入待播');
    await queue.trigger('click');
    await flushPromises();
    expect(api.EditPlaybackQueue).toHaveBeenCalledWith(expect.objectContaining({ action: 'append_collection', collection_id: 5, expected_revision: 3 }));
  });

  it('全部看完：没有「从下一部播放」，头部写已看完', async () => {
    api.ListCollectionProgress.mockResolvedValue([progressOf(5, { complete: true, watched_works: 3, next: undefined })]);
    const wrapper = await mountPage({ kind: 'collection', focusEntity: { id: 5, name: '三部曲' } });
    expect(wrapper.find('[data-test="entity-collection-play-next"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="entity-collection-progress-text"]').text()).toBe('已看完 · 共 3 部');
    expect(wrapper.find('[data-test="entity-collection-next-badge"]').exists()).toBe(false);
  });

  it('同一部的多个版本合成一行（逐部明细），在看那一部点开的是有断点的那个版本', async () => {
    api.GetCollectionDetail.mockResolvedValue({
      collection: collection(5),
      videos: [member(1, 1, { is_watched: true }), member(2, 2, { watch_position_seconds: 300 }), member(3, 3), member(9, 4)]
    });
    // 2 与 3 是同一部的两个版本，3 是主版本，断点在 2 上。
    api.ListCollectionProgress.mockResolvedValue([progressOf(5, {
      total_works: 3, in_progress_works: 1,
      next: { video_id: 2, title: '第2部', work_index: 2, resume: true, watch_position_seconds: 300 },
      works: [work(1, [1], 'watched'), work(2, [3, 2], 'in_progress', { resume_video_id: 2 }), work(3, [9])],
      offline_video_ids: []
    })]);
    const wrapper = await mountPage({ kind: 'collection', focusEntity: { id: 5, name: '三部曲' } });
    expect(api.SearchLibraryVideoPage).not.toHaveBeenCalled();
    const rows = wrapper.findAll('.entity-browse__work');
    expect(rows.map(row => row.get('.entity-browse__work-index').text())).toEqual(['1', '2', '3']);
    const merged = wrapper.get('[data-test="entity-collection-work-3"]');
    expect(merged.get('.entity-browse__work-copy strong').text()).toBe('第3部');
    expect(merged.text()).toContain('2 个版本');
    expect(merged.text()).toContain('看到 05:00');
    expect(merged.classes()).toContain('entity-browse__work--next');
    expect(wrapper.get('[data-test="entity-collection-state-3"]').text()).toBe('在看');
    await merged.trigger('click');
    expect(wrapper.emitted('open-video')).toEqual([[2]]);
  });

  it('进度（逐部明细）读取失败：头部写原因，各部列表出横幅；重试补上', async () => {
    api.ListCollectionProgress.mockRejectedValueOnce(new Error('忙'));
    const wrapper = await mountPage({ kind: 'collection', focusEntity: { id: 5, name: '三部曲' } });
    expect(wrapper.get('[data-test="entity-collection-progress"]').text()).toContain('读取观看进度失败：忙');
    expect(wrapper.get('[data-test="entity-collection-works-error"]').text()).toContain('读取观看进度失败：忙');
    expect(wrapper.findAll('.entity-browse__work')).toHaveLength(0);
    expect(wrapper.find('[data-test="entity-collection-works-empty"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="entity-collection-play-next"]').exists()).toBe(false);
    await wrapper.get('[data-test="entity-collection-progress-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="entity-collection-progress-text"]').text()).toContain('第 2 / 3 部');
    expect(wrapper.find('[data-test="entity-collection-works-error"]').exists()).toBe(false);
    expect(wrapper.findAll('.entity-browse__work')).toHaveLength(4);
    expect(wrapper.find('[data-test="entity-collection-next-badge"]').exists()).toBe(true);
  });

  it('作品集读取失败：列表区横幅 + 重试；没有成员给空状态', async () => {
    api.GetCollectionDetail.mockRejectedValueOnce(new Error('record not found'));
    const wrapper = await mountPage({ kind: 'collection', focusEntity: { id: 5, name: '三部曲' } });
    expect(wrapper.get('[data-test="entity-collection-works-error"]').text()).toContain('读取作品集失败');
    api.GetCollectionDetail.mockResolvedValue({ collection: collection(5), videos: [] });
    api.ListCollectionProgress.mockResolvedValue([progressOf(5, { total_works: 0, watched_works: 0, next: undefined, works: [], offline_video_ids: [] })]);
    await wrapper.get('[data-test="entity-collection-works-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="entity-collection-works-empty"]').text()).toContain('这个作品集还没有作品');
    expect(wrapper.get('[data-test="entity-collection-progress-text"]').text()).toBe('还没有在线的作品');
  });

  it('回到本页（pageActive 变 true）：对象页重取作品集与进度，不闪骨架', async () => {
    const wrapper = await mountPage({ kind: 'collection', focusEntity: { id: 5, name: '三部曲' }, pageActive: true });
    await wrapper.setProps({ pageActive: false });
    api.ListCollectionProgress.mockResolvedValue([progressOf(5, { watched_works: 2, next: { video_id: 3, title: '第3部', work_index: 3 } })]);
    await wrapper.setProps({ pageActive: true });
    expect(wrapper.find('[data-test="entity-collection-works-loading"]').exists()).toBe(false);
    expect(wrapper.findAll('.entity-browse__work')).toHaveLength(4);
    await flushPromises();
    expect(api.GetCollectionDetail).toHaveBeenCalledTimes(2);
    expect(wrapper.get('[data-test="entity-collection-progress-text"]').text()).toContain('第 3 / 3 部');

    // 列表模式下回来只重取已加载卡片的进度。
    await wrapper.get('[data-test="entity-browse-back"]').trigger('click');
    const calls = api.ListCollectionProgress.mock.calls.length;
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(api.ListCollectionProgress.mock.calls.length).toBe(calls + 1);
    expect(api.ListCollectionProgress).toHaveBeenLastCalledWith({ collection_ids: [5, 6] });
  });

  it('作品集页没有编辑入口，只有「在管理中编辑」', async () => {
    const wrapper = await mountPage({ kind: 'collection', focusEntity: { id: 5, name: '三部曲' } });
    const labels = wrapper.findAll('button').filter(button => button.isVisible()).map(button => button.text()).join('|');
    for (const word of ['删除', '新建', '移除', '排序', '编辑与批量关联']) expect(labels, word).not.toContain(word);
    await wrapper.get('[data-test="entity-edit-in-manage"]').trigger('click');
    expect(wrapper.emitted('edit-in-manage')).toEqual([[{ kind: 'collection', id: 5, name: '三部曲' }]]);
    expect(text(wrapper)).toContain('全部加入待播');
  });
});
