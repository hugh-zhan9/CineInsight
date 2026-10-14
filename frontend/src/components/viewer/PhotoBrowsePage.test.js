import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  GetImageDetail: vi.fn(),
  GetWallpaperPreflight: vi.fn(),
  ListImageFolderGroups: vi.fn(),
  ListImageTimelineBuckets: vi.fn(),
  ListPeople: vi.fn(),
  LogFrontend: vi.fn(),
  RecordImageView: vi.fn(),
  SearchImagePage: vi.fn(),
  SetImageFavorite: vi.fn(),
  SetImageRating: vi.fn(),
  SetWallpaper: vi.fn()
}));
vi.mock('../../../wailsjs/go/main/App', async importOriginal => ({ ...(await importOriginal()), ...api }));
vi.mock('../../utils/feedback.js', async importOriginal => ({ ...(await importOriginal()), notify: vi.fn(), notifyError: vi.fn() }));

import PhotoBrowsePage, {
  PHOTO_BROWSE_FOLDER_LIMIT, PHOTO_BROWSE_MISSING_MESSAGE, PHOTO_BROWSE_PAGE_SIZE, photoBrowseFilter, photoBucketCounts
} from './PhotoBrowsePage.vue';
import PhotoLightbox from './PhotoLightbox.vue';
import * as feedback from '../../utils/feedback.js';

enableAutoUnmount(afterEach);

// 假图片库：拍摄时间按本机时区的字符串写（不带 Z），年月分组与时区无关。每张比上一张早 12 小时，约 60 张一个月。
const pad = value => String(value).padStart(2, '0');
function localStamp(date) {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:00:00`;
}
const NEWEST = new Date(2026, 9, 31, 20);
function photo(id, extra = {}) {
  const taken = new Date(NEWEST.getTime() - (id - 1) * 12 * 3600 * 1000);
  return {
    id,
    name: `IMG_${String(id).padStart(4, '0')}.jpg`,
    directory: `/Volumes/Photos/${taken.getFullYear()}-${pad(taken.getMonth() + 1)}`,
    format: 'jpeg',
    is_favorite: false,
    personal_rating: null,
    taken_at: localStamp(taken),
    created_at: localStamp(taken),
    person_ids: [],
    ...extra
  };
}
const photos = (count, extra = () => ({})) => Array.from({ length: count }, (_, index) => photo(index + 1, extra(index + 1)));
const monthOf = image => image.taken_at.slice(0, 7);

let library = [];
// 按 ImageFilter 过滤、按拍摄时间倒序（同时间 id 倒序）；游标只用 id 续上。
function matching(filter) {
  const keyword = String(filter.keyword || '').toLowerCase();
  return library
    .filter(image => (!keyword || image.name.toLowerCase().includes(keyword))
      && (!filter.favorite_only || image.is_favorite)
      && (filter.person_ids || []).every(id => image.person_ids.includes(id))
      && (!filter.directory || image.directory === filter.directory))
    .sort((a, b) => (a.taken_at === b.taken_at ? b.id - a.id : (a.taken_at < b.taken_at ? 1 : -1)));
}
function installLibrary(images) {
  library = images;
  api.SearchImagePage.mockImplementation(async ({ filter, cursor, limit }) => {
    const all = matching(filter);
    const start = cursor ? all.findIndex(image => image.id === cursor.id) + 1 : 0;
    const size = limit > 0 && limit <= 200 ? limit : 60;
    const slice = all.slice(start, start + size);
    const more = start + size < all.length;
    return {
      images: slice.map(image => ({ ...image })),
      next_cursor: more ? { sort_mode: 'taken', taken_at: slice.at(-1).taken_at, id: slice.at(-1).id } : null
    };
  });
  api.ListImageTimelineBuckets.mockImplementation(async filter => {
    const counts = new Map();
    for (const image of matching(filter)) counts.set(monthOf(image), (counts.get(monthOf(image)) || 0) + 1);
    return [...counts].map(([key, count]) => ({ year: Number(key.slice(0, 4)), month: Number(key.slice(5, 7)), count }));
  });
  api.ListImageFolderGroups.mockImplementation(async filter => {
    const groups = new Map();
    for (const image of matching(filter)) groups.set(image.directory, (groups.get(image.directory) || 0) + 1);
    return [...groups].map(([directory, count]) => ({ directory, name: directory.split('/').pop(), count, total_size: 0, covers: [] }));
  });
}

const DEFAULT_FILTER = {
  keyword: '', directory: '', tag_ids: [], person_ids: [], favorite_only: false, min_rating: null, max_rating: null,
  min_size: 0, max_size: 0, taken_after: null, taken_before: null, sort_mode: 'taken', ai_tag_state: '', missing: ''
};
const searchCalls = () => api.SearchImagePage.mock.calls.map(([request]) => request);
const lastSearch = () => searchCalls().at(-1);

beforeEach(() => {
  vi.clearAllMocks();
  installLibrary(photos(150));
  api.ListPeople.mockReset().mockResolvedValue([
    { person: { id: 5, display_name: '甲' }, active_image_count: 12 },
    { person: { id: 6, display_name: '乙' }, active_image_count: 3 }
  ]);
  api.GetImageDetail.mockReset().mockImplementation(id => Promise.resolve({ image: { id }, people: [] }));
  api.RecordImageView.mockReset().mockResolvedValue({ recorded: false });
  api.SetImageFavorite.mockReset().mockImplementation((id, value) => Promise.resolve({ id, is_favorite: value }));
  api.SetImageRating.mockReset();
  api.GetWallpaperPreflight.mockReset().mockResolvedValue({ eligible: false, message: '' });
  api.LogFrontend.mockReset().mockResolvedValue(undefined);
});

async function mountPage(props = {}, options = {}) {
  const wrapper = mount(PhotoBrowsePage, { props, global: { stubs: { teleport: true } }, ...options });
  await flushPromises();
  return wrapper;
}

// 挂进一个假的 .main-view 滚动宿主，让窗口化真正生效（jsdom 不做布局，几何全靠桩）：
// 网格宽度在挂载前就位；内容高度 = 网格直接子节点（占位块 + 行）的内联高度之和；scrollTop 按 scrollHeight 钳制。
async function mountInScrollOwner(props = {}, { viewportHeight = 800, gridWidth = 1200 } = {}) {
  const host = document.createElement('div');
  host.className = 'main-view';
  document.body.appendChild(host);
  let scrollTop = 0;
  const rect = (top, width, height) => ({ top, left: 0, width, height, bottom: top + height, right: width, x: 0, y: top, toJSON() {} });
  const contentHeight = () => {
    const grid = host.querySelector('.photo-browse__grid');
    return grid ? Array.from(grid.children).reduce((sum, child) => sum + (parseFloat(child.style.height) || 0), 0) : 0;
  };
  Object.defineProperty(host, 'clientHeight', { configurable: true, get: () => viewportHeight });
  Object.defineProperty(host, 'scrollHeight', { configurable: true, get: () => Math.max(viewportHeight, contentHeight()) });
  Object.defineProperty(host, 'scrollTop', {
    configurable: true,
    get: () => scrollTop,
    set: value => { scrollTop = Math.max(0, Math.min(Number(value) || 0, Math.max(0, contentHeight() - viewportHeight))); }
  });
  const originalGetRect = Element.prototype.getBoundingClientRect;
  Element.prototype.getBoundingClientRect = function patchedGetRect() {
    if (this === host) return rect(0, gridWidth, viewportHeight);
    if (this.classList?.contains('photo-browse__grid')) return rect(-scrollTop, gridWidth, contentHeight());
    return originalGetRect.call(this);
  };
  const wrapper = mount(PhotoBrowsePage, { props, global: { stubs: { teleport: true } }, attachTo: host });
  await flushPromises();
  await wrapper.vm.$nextTick();
  const scrollTo = async position => {
    host.scrollTop = position;
    host.dispatchEvent(new Event('scroll'));
    await flushPromises();
  };
  const cleanup = () => {
    Element.prototype.getBoundingClientRect = originalGetRect;
    wrapper.unmount();
    host.remove();
  };
  return { wrapper, host, scrollTo, cleanup, contentHeight };
}

const tileNames = wrapper => wrapper.findAll('[data-test="photo-browse-tile"]').map(tile => tile.attributes('title'));
const monthHeaders = wrapper => wrapper.findAll('[data-test="photo-browse-month"]').map(header => header.text());

describe('PhotoBrowsePage 纯函数', () => {
  it('筛选只用关键字、收藏、人物、文件夹，排序固定按拍摄时间，其余保持空', () => {
    expect(photoBrowseFilter()).toEqual(DEFAULT_FILTER);
    expect(photoBrowseFilter({ keyword: ' 海边 ', favoriteOnly: true, people: [{ id: 5, name: '甲' }, { id: '6' }], folder: { directory: '/a/b', name: 'b' } }))
      .toEqual({ ...DEFAULT_FILTER, keyword: '海边', favorite_only: true, person_ids: [5, 6], directory: '/a/b' });
  });

  it('组头计数按年月键汇总', () => {
    expect(photoBucketCounts([{ year: 2026, month: 10, count: 61 }, { year: 2026, month: 9, count: 60 }])).toEqual({ '2026-10': 61, '2026-09': 60 });
    expect(photoBucketCounts(null)).toEqual({});
  });
});

describe('PhotoBrowsePage 照片流', () => {
  it('首批按拍摄时间倒序取 60 张；按年 / 月分组，组头写这一组的总张数（不是已加载的张数），页头写总数', async () => {
    const wrapper = await mountPage();
    expect(searchCalls()).toEqual([{ filter: DEFAULT_FILTER, limit: PHOTO_BROWSE_PAGE_SIZE }]);
    expect(api.ListImageTimelineBuckets).toHaveBeenCalledWith(DEFAULT_FILTER);
    const counts = {};
    for (const image of library) counts[monthOf(image)] = (counts[monthOf(image)] || 0) + 1;
    const months = Object.keys(counts).sort().reverse();
    const loadedMonths = [...new Set(library.slice(0, 60).map(monthOf))];
    expect(monthHeaders(wrapper)).toEqual(loadedMonths.map(key => `${key.slice(0, 4)} 年 ${Number(key.slice(5))} 月 · ${counts[key]} 张`));
    expect(months[0]).toBe('2026-10');
    expect(tileNames(wrapper)).toEqual(library.slice(0, 60).map(image => image.name));
    expect(wrapper.text()).toContain('共 150 张');
  });

  it('组头计数读失败：组头只写年月，页头不写总数，照片照常显示', async () => {
    api.ListImageTimelineBuckets.mockRejectedValueOnce(new Error('超时'));
    const wrapper = await mountPage();
    expect(monthHeaders(wrapper)[0]).toBe('2026 年 10 月');
    expect(wrapper.text()).not.toContain('共 ');
    expect(tileNames(wrapper)).toHaveLength(60);
  });

  it('收藏的照片带一个小标记；缩略图打不开时换成格式占位', async () => {
    installLibrary(photos(3, id => ({ is_favorite: id === 2 })));
    const wrapper = await mountPage();
    const tiles = wrapper.findAll('[data-test="photo-browse-tile"]');
    expect(tiles[1].find('[data-test="photo-browse-favorite-mark"]').exists()).toBe(true);
    expect(tiles[1].attributes('aria-label')).toBe('IMG_0002.jpg，已收藏');
    expect(tiles[0].find('[data-test="photo-browse-favorite-mark"]').exists()).toBe(false);
    expect(tiles[0].get('img').attributes('src')).toBe('/preview/image-thumbnail/1');
    await tiles[0].get('img').trigger('error');
    expect(wrapper.findAll('[data-test="photo-browse-tile"]')[0].text()).toContain('JPEG');
  });

  it('没有删除、打标签、AI、批量、目录、扫描等管理操作，也没有勾选框', async () => {
    const wrapper = await mountPage();
    const text = wrapper.text();
    for (const word of ['删除', '废纸篓', '从图片库移除', '标签', 'AI', '批量', '在访达中显示', '扫描', '回收站', '清理', '全选']) {
      expect(text, word).not.toContain(word);
    }
    expect(wrapper.findAll('input[type="checkbox"]')).toHaveLength(0);
    await wrapper.findAll('[data-test="photo-browse-tile"]')[0].trigger('click');
    await flushPromises();
    const lightbox = wrapper.findComponent(PhotoLightbox);
    for (const word of ['删除', '废纸篓', '标签', 'AI', '批量', '在访达中显示']) expect(lightbox.text(), word).not.toContain(word);
  });
});

describe('PhotoBrowsePage 四个筛选', () => {
  it('文件名搜索去抖 300ms，输入法组词期间不发，回车立即发；页头改写「筛选出」', async () => {
    vi.useFakeTimers();
    try {
      const wrapper = await mountPage();
      const input = wrapper.get('[data-test="photo-browse-search"]');
      await input.setValue('IMG_001');
      input.element.dispatchEvent(new InputEvent('input', { isComposing: true }));
      vi.advanceTimersByTime(400);
      expect(api.SearchImagePage).toHaveBeenCalledTimes(2);
      expect(lastSearch().filter.keyword).toBe('IMG_001');
      await flushPromises();
      expect(wrapper.text()).toContain('筛选出 10 张');

      await input.setValue('IMG_0002');
      await input.trigger('keyup.enter');
      expect(api.SearchImagePage).toHaveBeenCalledTimes(3);
      vi.advanceTimersByTime(400);
      expect(api.SearchImagePage).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it('「全部 / 收藏」：收藏只取收藏的照片', async () => {
    installLibrary(photos(20, id => ({ is_favorite: id % 5 === 0 })));
    const wrapper = await mountPage();
    await wrapper.get('[data-test="photo-browse-scope-favorites"]').trigger('click');
    await flushPromises();
    expect(lastSearch().filter).toEqual({ ...DEFAULT_FILTER, favorite_only: true });
    expect(api.ListImageTimelineBuckets).toHaveBeenLastCalledWith({ ...DEFAULT_FILTER, favorite_only: true });
    expect(tileNames(wrapper)).toEqual(['IMG_0005.jpg', 'IMG_0010.jpg', 'IMG_0015.jpg', 'IMG_0020.jpg']);
    await wrapper.get('[data-test="photo-browse-scope-all"]').trigger('click');
    await flushPromises();
    expect(lastSearch().filter.favorite_only).toBe(false);
  });

  it('人物：浮层用与图片管理同一个人物搜索，选中的进筛选（可多选），芯片上可去掉', async () => {
    installLibrary(photos(10, id => ({ person_ids: id <= 3 ? [5] : [] })));
    const wrapper = await mountPage();
    await wrapper.get('[data-test="photo-browse-person-trigger"]').trigger('click');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledWith('', '', 0, 20);
    expect(wrapper.get('[data-test="photo-browse-person-option-5"]').text()).toContain('12 张');
    await wrapper.get('[data-test="photo-browse-person-option-5"]').trigger('click');
    await flushPromises();
    expect(lastSearch().filter.person_ids).toEqual([5]);
    expect(tileNames(wrapper)).toEqual(['IMG_0001.jpg', 'IMG_0002.jpg', 'IMG_0003.jpg']);
    expect(wrapper.get('[data-test="photo-browse-person-chip-5"]').text()).toContain('甲');

    await wrapper.get('[data-test="photo-browse-person-search"]').setValue('乙');
    await new Promise(resolve => setTimeout(resolve, 220));
    await flushPromises();
    expect(api.ListPeople).toHaveBeenLastCalledWith('乙', '', 0, 20);

    await wrapper.get('[data-test="photo-browse-person-chip-5"] .ui-filter-chip__remove').trigger('click');
    await flushPromises();
    expect(lastSearch().filter.person_ids).toEqual([]);
    expect(wrapper.find('[data-test="photo-browse-active-filters"]').exists()).toBe(false);
  });

  it('文件夹：选项来自其余筛选下的文件夹分组（名称 + 张数），单选，再点一次取消；可按名称找', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="photo-browse-scope-favorites"]').trigger('click');
    await wrapper.get('[data-test="photo-browse-scope-all"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="photo-browse-folder-trigger"]').trigger('click');
    await flushPromises();
    expect(api.ListImageFolderGroups).toHaveBeenLastCalledWith(DEFAULT_FILTER);
    const options = wrapper.findAll('[data-test="photo-browse-folder-option"]');
    const folderCounts = new Map();
    for (const image of library) folderCounts.set(image.directory, (folderCounts.get(image.directory) || 0) + 1);
    expect(options.map(option => option.text())).toEqual([...folderCounts].map(([directory, count]) => `${directory.split('/').pop()}${directory}${count} 张`));
    expect(options).toHaveLength(3);
    await options[1].trigger('click');
    await flushPromises();
    expect(lastSearch().filter).toEqual({ ...DEFAULT_FILTER, directory: '/Volumes/Photos/2026-09' });
    expect(wrapper.find('[data-test="photo-browse-folder-popover"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="photo-browse-folder-chip"]').text()).toContain('2026-09');
    expect(tileNames(wrapper)).toHaveLength(60);

    // 选了文件夹之后再打开：选项仍按其余筛选列出全部文件夹，当前那个打勾；按名称找；再点一次已选的取消。
    await wrapper.get('[data-test="photo-browse-folder-trigger"]').trigger('click');
    await flushPromises();
    expect(api.ListImageFolderGroups).toHaveBeenLastCalledWith(DEFAULT_FILTER);
    await wrapper.get('[data-test="photo-browse-folder-search"]').setValue('08');
    expect(wrapper.findAll('[data-test="photo-browse-folder-option"]')).toHaveLength(1);
    await wrapper.get('[data-test="photo-browse-folder-search"]').setValue('');
    const selected = wrapper.findAll('[data-test="photo-browse-folder-option"]')[1];
    expect(selected.attributes('aria-pressed')).toBe('true');
    await selected.trigger('click');
    await flushPromises();
    expect(lastSearch().filter.directory).toBe('');
  });

  it('文件夹很多时浮层只列前 200 个并提示按名称缩小范围；读失败给重试', async () => {
    api.ListImageFolderGroups.mockResolvedValueOnce(Array.from({ length: PHOTO_BROWSE_FOLDER_LIMIT + 5 }, (_, index) => ({ directory: `/d/${index}`, name: `d${index}`, count: 1 })));
    const wrapper = await mountPage();
    await wrapper.get('[data-test="photo-browse-folder-trigger"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="photo-browse-folder-option"]')).toHaveLength(PHOTO_BROWSE_FOLDER_LIMIT);
    expect(wrapper.get('[data-test="photo-browse-folder-more"]').text()).toContain('还有 5 个文件夹');
    await wrapper.get('[data-test="photo-browse-folder-trigger"]').trigger('click');

    api.ListImageFolderGroups.mockRejectedValueOnce(new Error('读不到'));
    await wrapper.get('[data-test="photo-browse-folder-trigger"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="photo-browse-folder-error"]').text()).toContain('读不到');
    await wrapper.get('[data-test="photo-browse-folder-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="photo-browse-folder-option"]')).toHaveLength(3);
  });

  it('「清空筛选」四样一起回到默认', async () => {
    const wrapper = await mountPage({ navigationRequest: { personId: 5, personName: '甲', token: 1 } });
    await wrapper.get('[data-test="photo-browse-scope-favorites"]').trigger('click');
    await wrapper.get('[data-test="photo-browse-search"]').setValue('IMG');
    await wrapper.get('[data-test="photo-browse-clear-chips"]').trigger('click');
    await flushPromises();
    expect(lastSearch().filter).toEqual(DEFAULT_FILTER);
    expect(wrapper.get('[data-test="photo-browse-search"]').element.value).toBe('');
  });
});

describe('PhotoBrowsePage 状态', () => {
  it('首批在读时是骨架；读失败是横幅 + 重试', async () => {
    let fail;
    api.SearchImagePage.mockImplementationOnce(() => new Promise((_, reject) => { fail = reject; }));
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="photo-browse-loading"]').exists()).toBe(true);
    fail(new Error('database is in maintenance mode'));
    await flushPromises();
    expect(wrapper.find('[data-test="photo-browse-loading"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="photo-browse-error"]').text()).toContain('数据库正在恢复备份或切换后端');
    await wrapper.get('[data-test="photo-browse-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="photo-browse-error"]').exists()).toBe(false);
    expect(tileNames(wrapper)).toHaveLength(60);
  });

  it('图片库为空：下一步去图片管理添加目录；有筛选但没有结果：清空筛选', async () => {
    installLibrary([]);
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="photo-browse-empty"]').text()).toContain('图片库里还没有图片');
    await wrapper.get('[data-test="photo-browse-empty-manage"]').trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['photos-manage']]);

    installLibrary(photos(5));
    await wrapper.get('[data-test="photo-browse-scope-favorites"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="photo-browse-empty"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="photo-browse-no-results"]').text()).toContain('没有符合条件的图片');
    await wrapper.get('[data-test="photo-browse-empty-clear"]').trigger('click');
    await flushPromises();
    expect(tileNames(wrapper)).toHaveLength(5);
  });

  it('「加载更多」带上游标续读；翻页失败给重试，不清掉已加载的', async () => {
    const wrapper = await mountPage();
    api.SearchImagePage.mockRejectedValueOnce(new Error('断开'));
    await wrapper.get('[data-test="photo-browse-load-more"]').trigger('click');
    await flushPromises();
    expect(lastSearch().cursor).toMatchObject({ sort_mode: 'taken', id: 60 });
    expect(wrapper.get('[data-test="photo-browse-more-error"]').text()).toContain('断开');
    expect(tileNames(wrapper)).toHaveLength(60);
    await wrapper.get('[data-test="photo-browse-more-retry"]').trigger('click');
    await flushPromises();
    expect(tileNames(wrapper)).toHaveLength(120);
    await wrapper.get('[data-test="photo-browse-load-more"]').trigger('click');
    await flushPromises();
    expect(tileNames(wrapper)).toHaveLength(150);
    expect(wrapper.find('[data-test="photo-browse-load-more"]').exists()).toBe(false);
  });
});

describe('PhotoBrowsePage 窗口化', () => {
  it('五千张的照片流只渲染视口附近的行：顶部、滚到中间、滚到底都只有几十张', async () => {
    const all = photos(5000);
    api.SearchImagePage.mockResolvedValueOnce({ images: all, next_cursor: null });
    library = all;
    const { wrapper, scrollTo, cleanup, contentHeight } = await mountInScrollOwner();
    try {
      // 1200px 宽、最小列宽 200、间隙 6 => 5 列；视口 800 + 上下各 3 行预留。
      expect(wrapper.vm.columns).toBe(5);
      const atTop = wrapper.findAll('[data-test="photo-browse-tile"]');
      expect(atTop.length).toBeGreaterThan(0);
      expect(atTop.length).toBeLessThanOrEqual(60);
      expect(tileNames(wrapper)[0]).toBe('IMG_0001.jpg');
      expect(wrapper.vm.layout.rows.length).toBeGreaterThan(1000);
      // 占位块撑出整条照片流的高度。
      expect(contentHeight()).toBe(wrapper.vm.layout.totalHeight);

      await scrollTo(Math.floor(wrapper.vm.layout.totalHeight / 2));
      const middle = tileNames(wrapper);
      expect(middle.length).toBeLessThanOrEqual(60);
      expect(middle[0]).not.toBe('IMG_0001.jpg');
      expect(wrapper.vm.windowState.topSpacer).toBeGreaterThan(0);
      expect(wrapper.vm.windowState.bottomSpacer).toBeGreaterThan(0);
      expect(monthHeaders(wrapper).length).toBeLessThanOrEqual(2);

      await scrollTo(wrapper.vm.layout.totalHeight);
      expect(tileNames(wrapper).length).toBeLessThanOrEqual(60);
      expect(tileNames(wrapper).at(-1)).toBe('IMG_5000.jpg');
      expect(wrapper.vm.windowState.endRow).toBe(wrapper.vm.layout.rows.length);
      expect(wrapper.vm.windowState.bottomSpacer).toBe(0);
    } finally {
      cleanup();
    }
  });

  it('滚到离底部不远时自动取下一批；切走再回来放回滚动位置', async () => {
    installLibrary(photos(300));
    const { wrapper, host, scrollTo, cleanup } = await mountInScrollOwner();
    try {
      expect(searchCalls()).toHaveLength(1);
      await scrollTo(wrapper.vm.layout.totalHeight);
      expect(searchCalls()).toHaveLength(2);
      expect(lastSearch().cursor).toMatchObject({ id: 60 });
      expect(wrapper.vm.list.items).toHaveLength(120);

      const target = 900;
      await scrollTo(target);
      await wrapper.setProps({ pageActive: false });
      host.scrollTop = 0;
      host.dispatchEvent(new Event('scroll'));
      await flushPromises();
      expect(searchCalls()).toHaveLength(2);
      await wrapper.setProps({ pageActive: true });
      await flushPromises();
      expect(host.scrollTop).toBe(target);
    } finally {
      cleanup();
    }
  });

  it('换条件回到顶部', async () => {
    installLibrary(photos(300, id => ({ is_favorite: id % 2 === 0 })));
    const { wrapper, host, scrollTo, cleanup } = await mountInScrollOwner();
    try {
      await scrollTo(1500);
      expect(host.scrollTop).toBeGreaterThan(0);
      await wrapper.get('[data-test="photo-browse-scope-favorites"]').trigger('click');
      await flushPromises();
      expect(host.scrollTop).toBe(0);
      expect(tileNames(wrapper)[0]).toBe('IMG_0002.jpg');
    } finally {
      cleanup();
    }
  });
});

describe('PhotoBrowsePage 看图', () => {
  it('点一张打开它；← → 切换，第一张不能往前；到已加载末尾时先取下一批再前进；全部到底不能往后', async () => {
    installLibrary(photos(70));
    const wrapper = await mountPage();
    await wrapper.findAll('[data-test="photo-browse-tile"]')[1].trigger('click');
    await flushPromises();
    let lightbox = wrapper.findComponent(PhotoLightbox);
    expect(lightbox.props('image').id).toBe(2);
    lightbox.vm.$emit('prev');
    await flushPromises();
    expect(wrapper.findComponent(PhotoLightbox).props('image').id).toBe(1);
    expect(wrapper.findComponent(PhotoLightbox).props('hasPrev')).toBe(false);

    await wrapper.findAll('[data-test="photo-browse-tile"]')[59].trigger('click');
    await flushPromises();
    lightbox = wrapper.findComponent(PhotoLightbox);
    expect(lightbox.props()).toMatchObject({ hasNext: true });
    expect(searchCalls()).toHaveLength(1);
    lightbox.vm.$emit('next');
    await flushPromises();
    expect(searchCalls()).toHaveLength(2);
    expect(lastSearch().cursor).toMatchObject({ id: 60 });
    expect(wrapper.findComponent(PhotoLightbox).props('image').id).toBe(61);

    wrapper.vm.lightboxIndex = 69;
    await flushPromises();
    expect(wrapper.findComponent(PhotoLightbox).props('hasNext')).toBe(false);
    wrapper.findComponent(PhotoLightbox).vm.$emit('next');
    await flushPromises();
    expect(searchCalls()).toHaveLength(2);
    expect(wrapper.findComponent(PhotoLightbox).props('image').id).toBe(70);
  });

  it('续载下一批失败：停在这一张，看图里给出原因与重试', async () => {
    installLibrary(photos(70));
    const wrapper = await mountPage();
    await wrapper.findAll('[data-test="photo-browse-tile"]')[59].trigger('click');
    api.SearchImagePage.mockRejectedValueOnce(new Error('网络断开'));
    wrapper.findComponent(PhotoLightbox).vm.$emit('next');
    await flushPromises();
    const lightbox = wrapper.findComponent(PhotoLightbox);
    expect(lightbox.props('image').id).toBe(60);
    expect(lightbox.props('nextError')).toBe('网络断开');
    lightbox.vm.$emit('next');
    await flushPromises();
    expect(wrapper.findComponent(PhotoLightbox).props('image').id).toBe(61);
  });

  it('看图里改了收藏：本页那一张就地更新并上报；存失败时改回原值', async () => {
    const wrapper = await mountPage();
    await wrapper.findAll('[data-test="photo-browse-tile"]')[0].trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="photo-lightbox-favorite"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.list.items[0].is_favorite).toBe(true);
    expect(wrapper.emitted('image-changed')).toEqual([[{ id: 1, is_favorite: true }]]);

    api.SetImageFavorite.mockRejectedValueOnce(new Error('坏了'));
    await wrapper.get('[data-test="photo-lightbox-favorite"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.list.items[0].is_favorite).toBe(true);
    expect(wrapper.get('[data-test="photo-lightbox-error"]').text()).toContain('坏了');
    expect(wrapper.emitted('image-changed')).toHaveLength(1);
    await wrapper.get('[data-test="photo-lightbox-close"]').trigger('click');
    expect(wrapper.findComponent(PhotoLightbox).exists()).toBe(false);
    expect(wrapper.findAll('[data-test="photo-browse-tile"]')[0].find('[data-test="photo-browse-favorite-mark"]').exists()).toBe(true);
  });

  it('人物芯片与「在图片管理中打开」：先关看图再交给 App', async () => {
    api.GetImageDetail.mockResolvedValue({ image: { id: 1 }, people: [{ person: { id: 5, display_name: '甲' } }] });
    const wrapper = await mountPage();
    await wrapper.findAll('[data-test="photo-browse-tile"]')[0].trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="photo-lightbox-person-5"]').trigger('click');
    expect(wrapper.emitted('open-person')).toEqual([[{ id: 5, name: '甲' }]]);
    expect(wrapper.findComponent(PhotoLightbox).exists()).toBe(false);

    await wrapper.findAll('[data-test="photo-browse-tile"]')[2].trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="photo-lightbox-open-manage"]').trigger('click');
    expect(wrapper.emitted('open-in-manage')).toEqual([[{ id: 3, name: 'IMG_0003.jpg' }]]);
    expect(wrapper.findComponent(PhotoLightbox).exists()).toBe(false);
  });

  it('页面隐藏时看图一并收起（它挂在 body 上，不跟着页面隐藏）', async () => {
    const wrapper = await mountPage();
    await wrapper.findAll('[data-test="photo-browse-tile"]')[0].trigger('click');
    await wrapper.setProps({ pageActive: false });
    expect(wrapper.findComponent(PhotoLightbox).exists()).toBe(false);
  });
});

describe('PhotoBrowsePage 别处点图片进来（open-image）', () => {
  it('已加载的直接打开，不重新查', async () => {
    const wrapper = await mountPage();
    expect(await wrapper.vm.locateImage({ id: 30, name: 'IMG_0030.jpg' })).toBe(true);
    await flushPromises();
    expect(wrapper.findComponent(PhotoLightbox).props('image').id).toBe(30);
    expect(searchCalls()).toHaveLength(1);
  });

  it('没加载到：清空筛选、按文件名找到后打开', async () => {
    const wrapper = await mountPage({ navigationRequest: { personId: 5, token: 1 } });
    await wrapper.get('[data-test="photo-browse-scope-favorites"]').trigger('click');
    await flushPromises();
    expect(await wrapper.vm.locateImage({ id: 140, name: 'IMG_0140.jpg' })).toBe(true);
    await flushPromises();
    expect(lastSearch().filter).toEqual({ ...DEFAULT_FILTER, keyword: 'IMG_0140.jpg' });
    expect(wrapper.get('[data-test="photo-browse-search"]').element.value).toBe('IMG_0140.jpg');
    expect(wrapper.find('[data-test="photo-browse-active-filters"]').exists()).toBe(false);
    expect(wrapper.findComponent(PhotoLightbox).props('image').id).toBe(140);
    expect(feedback.notify).not.toHaveBeenCalled();
  });

  it('按文件名也找不到：提示「这张图片已不在图片库中」，不开看图', async () => {
    const wrapper = await mountPage();
    expect(await wrapper.vm.locateImage({ id: 999, name: 'gone.jpg' })).toBe(false);
    await flushPromises();
    expect(feedback.notify).toHaveBeenCalledWith(PHOTO_BROWSE_MISSING_MESSAGE);
    expect(PHOTO_BROWSE_MISSING_MESSAGE).toContain('这张图片已不在图片库中');
    expect(wrapper.findComponent(PhotoLightbox).exists()).toBe(false);
    expect(wrapper.get('[data-test="photo-browse-no-results"]').exists()).toBe(true);
  });
});

describe('PhotoBrowsePage 人物「去图片」（navigationRequest）', () => {
  it('带着人物进来：筛选回到默认、只按这个人；同一个人再交一次也重新浏览', async () => {
    installLibrary(photos(10, id => ({ person_ids: id % 2 ? [7] : [] })));
    const wrapper = await mountPage({ navigationRequest: { personId: 7, personName: '丙', token: 1 } });
    expect(searchCalls()).toEqual([{ filter: { ...DEFAULT_FILTER, person_ids: [7] }, limit: PHOTO_BROWSE_PAGE_SIZE }]);
    expect(wrapper.get('[data-test="photo-browse-person-chip-7"]').text()).toContain('丙');
    expect(tileNames(wrapper)).toEqual(['IMG_0001.jpg', 'IMG_0003.jpg', 'IMG_0005.jpg', 'IMG_0007.jpg', 'IMG_0009.jpg']);

    await wrapper.get('[data-test="photo-browse-scope-favorites"]').trigger('click');
    await wrapper.get('[data-test="photo-browse-person-chip-7"] .ui-filter-chip__remove').trigger('click');
    await flushPromises();
    await wrapper.setProps({ navigationRequest: { personId: 7, token: 2 } });
    await flushPromises();
    expect(lastSearch().filter).toEqual({ ...DEFAULT_FILTER, person_ids: [7] });
    // 没给名字时芯片写人物编号。
    expect(wrapper.get('[data-test="photo-browse-person-chip-7"]').text()).toContain('人物 #7');
  });
});

describe('PhotoBrowsePage 别处改了图片后原地刷新', () => {
  it('隐藏时只记下；下次显示时按同一条件从头重取已加载的张数（每批最多 200），整体替换、不出骨架', async () => {
    installLibrary(photos(500));
    const wrapper = await mountPage();
    await wrapper.vm.loadMore();
    await wrapper.vm.loadMore();
    await wrapper.vm.loadMore();
    await wrapper.vm.loadMore();
    await flushPromises();
    expect(wrapper.vm.list.items).toHaveLength(300);
    await wrapper.setProps({ pageActive: false });

    // 管理侧删了两张、收藏了一张。
    library = library.filter(image => image.id !== 3 && image.id !== 250);
    library.find(image => image.id === 10).is_favorite = true;
    api.SearchImagePage.mockClear();
    await wrapper.setProps({ imagesVersion: 1 });
    await flushPromises();
    expect(api.SearchImagePage).not.toHaveBeenCalled();

    let release;
    const gate = new Promise(resolve => { release = resolve; });
    const real = api.SearchImagePage.getMockImplementation();
    api.SearchImagePage.mockImplementationOnce(async request => { await gate; return real(request); });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    // 取的途中照片流照旧显示，不出骨架。
    expect(wrapper.find('[data-test="photo-browse-loading"]').exists()).toBe(false);
    expect(tileNames(wrapper)).toHaveLength(300);
    release();
    await flushPromises();

    expect(searchCalls().map(request => [request.limit, request.cursor?.id ?? null])).toEqual([[200, null], [100, 201]]);
    expect(searchCalls().every(request => JSON.stringify(request.filter) === JSON.stringify(DEFAULT_FILTER))).toBe(true);
    expect(wrapper.vm.list.items).toHaveLength(300);
    expect(wrapper.vm.list.items.some(image => image.id === 3)).toBe(false);
    expect(wrapper.vm.list.items.find(image => image.id === 10).is_favorite).toBe(true);
    expect(wrapper.vm.list.items.at(-1).id).toBe(302);
    expect(wrapper.vm.list.hasMore).toBe(true);
    expect(api.ListImageTimelineBuckets).toHaveBeenLastCalledWith(DEFAULT_FILTER);

    // 刷新过了，再切走切回不再重取。
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(searchCalls()).toHaveLength(2);
  });

  it('显示中收到改动也原地刷新，视口顶部那张留在原处（它前面删了几张也一样）', async () => {
    installLibrary(photos(400));
    api.SearchImagePage.mockImplementationOnce(async () => ({ images: library.map(image => ({ ...image })), next_cursor: null }));
    const { wrapper, host, scrollTo, cleanup } = await mountInScrollOwner();
    try {
      await scrollTo(4000);
      const before = wrapper.vm.captureAnchor();
      expect(before.id).toBeGreaterThan(20);
      // 视口顶部那张之前的十张被删掉：整条照片流往上收了两行，那张应仍在视口里同一个位置。
      library = library.filter(image => image.id > 10);
      await wrapper.setProps({ imagesVersion: 1 });
      await flushPromises();
      await flushPromises();
      const after = wrapper.vm.captureAnchor();
      expect(after.id).toBe(before.id);
      expect(after.offset).toBe(before.offset);
      expect(host.scrollTop).toBeLessThan(4000);
      expect(wrapper.vm.list.items).toHaveLength(390);
    } finally {
      cleanup();
    }
  });

  it('没经过 App 的改动（后台扫描新增）：切回时核对各月张数，变了才原地重取；没变、读失败都不重取', async () => {
    installLibrary(photos(120));
    const wrapper = await mountPage();
    expect(wrapper.vm.list.items).toHaveLength(60);

    // 张数没变：切走切回只核对一次张数，不重取照片。
    api.SearchImagePage.mockClear();
    api.ListImageTimelineBuckets.mockClear();
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(api.ListImageTimelineBuckets).toHaveBeenCalledTimes(1);
    expect(api.SearchImagePage).not.toHaveBeenCalled();

    // 核对读失败：不重取，照片照旧。
    api.ListImageTimelineBuckets.mockRejectedValueOnce(new Error('数据库繁忙'));
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(api.SearchImagePage).not.toHaveBeenCalled();
    expect(wrapper.vm.list.items).toHaveLength(60);

    // 后台扫描进来两张更新的照片（不经过 imagesVersion）：切回时张数变了，按同一条件原地重取已加载的张数。
    library = [photo(0, { id: 1001, name: 'IMG_NEW1.jpg', taken_at: '2026-11-02T10:00:00', created_at: '2026-11-02T10:00:00' }),
      photo(0, { id: 1002, name: 'IMG_NEW2.jpg', taken_at: '2026-11-01T10:00:00', created_at: '2026-11-01T10:00:00' }), ...library];
    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(searchCalls().map(request => [request.limit, request.cursor?.id ?? null])).toEqual([[60, null]]);
    expect(wrapper.vm.list.items.slice(0, 2).map(image => image.id)).toEqual([1001, 1002]);
    expect(wrapper.vm.list.items).toHaveLength(60);
    expect(monthHeaders(wrapper)[0]).toContain('2026 年 11 月');
  });

  it('只有还没加载过（或什么也没加载到）时才从头取；取的途中换了条件就作废', async () => {
    installLibrary(photos(100));
    const wrapper = await mountPage();
    let release;
    const gate = new Promise(resolve => { release = resolve; });
    const real = api.SearchImagePage.getMockImplementation();
    api.SearchImagePage.mockImplementationOnce(async request => { await gate; return real(request); });
    await wrapper.setProps({ imagesVersion: 1 });
    await flushPromises();
    await wrapper.get('[data-test="photo-browse-scope-favorites"]').trigger('click');
    await flushPromises();
    release();
    await flushPromises();
    expect(wrapper.vm.list.items).toHaveLength(0);
    expect(wrapper.get('[data-test="photo-browse-no-results"]').exists()).toBe(true);
  });
});
