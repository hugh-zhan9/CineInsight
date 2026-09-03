import { flushPromises, mount } from '@vue/test-utils';

// 应用内确认框取代了失效的 window.confirm：默认答"确定"，需要"取消"的用例单独覆盖。
const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'SearchImagePage', 'GetImageDetail', 'SetImageFavorite', 'SetImageRating',
  'AddTagToImage', 'BatchAddTagToImages', 'RemoveTagFromImage', 'GetAllImageDirectories', 'SyncImageDirectories',
  'DeleteImage', 'ListImageTrashEntries', 'RestoreImageTrashEntry',
  'StartImageCleanupAnalysis', 'GetImageCleanupStatus', 'DismissImageNearDuplicateGroup', 'BatchDeleteImages',
  'GetImageSemanticIndexStatus', 'SearchImagesSemantic',
  'RetagImage', 'ListImageAITagCandidates', 'ApproveImageAITagCandidate', 'RejectImageAITagCandidate',
  'GetImageAITaggingSummary',
  'ListImageFolderGroups', 'ListImageTimelineBuckets', 'GetImageTags', 'OpenImageDirectory', 'RevealImage',
  'BatchDeleteImagesInDirectory',
  'ListPeople', 'AddPersonImages', 'RemovePersonImage'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);

import PhotoLibraryPage from './PhotoLibraryPage.vue';
import { photoCleanupStore, resetPhotoCleanupReview, stopPhotoCleanupPolling } from '../utils/photoCleanupStore.js';

function makeImage(id, overrides = {}) {
  return {
    id,
    name: `photo-${id}.jpg`,
    path: `/photos/photo-${id}.jpg`,
    directory: '/photos',
    size: 1024 * id,
    width: 4000,
    height: 3000,
    format: 'jpg',
    is_favorite: false,
    personal_rating: null,
    tags: [],
    created_at: '2026-08-07T10:00:00Z',
    ...overrides
  };
}

function makePage(images, nextCursor = null) {
  return { images, next_cursor: nextCursor };
}

const baseSettings = () => ({ confirm_before_delete: false, delete_original_file: false });

function idleCleanupStatus() {
  return { running: false, completed: false, error: '', progress: {}, analysis: null };
}

function completedCleanupStatus(analysis, { stale = false } = {}) {
  return { running: false, completed: true, error: '', progress: { stage: 'done' }, stale, analysis };
}

async function mountPage({ settings = baseSettings(), tags = [] } = {}) {
  const wrapper = mount(PhotoLibraryPage, { props: { settings, tags } });
  await flushPromises();
  return wrapper;
}


// 2026-09-01 起「立即扫描 / 清理审阅 / AI 标签审阅 / 回收站」收进了「管理」菜单，
// 低频筛选收进了「筛选」浮层，测试需要先把它们打开。
async function openPhotoManageItem(wrapper, id) {
  await wrapper.get('[data-test="photo-manage-open"]').trigger('click');
  await flushPromises();
  document.querySelector(`.base-menu__item[data-menu-id="${id}"]`).click();
  await flushPromises();
}

async function openPhotoFilter(wrapper) {
  if (!wrapper.find('[data-test="photo-filter-open"]').exists()) return;
  if (wrapper.find('.photo-filter-popover').exists()) return;
  await wrapper.get('[data-test="photo-filter-open"]').trigger('click');
  await flushPromises();
}

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage?.removeItem?.('cineinsight-photo-display-mode');
  api.GetAllImageDirectories.mockResolvedValue([{ id: 1, alias: '相册', path: '/photos' }]);
  api.SearchImagePage.mockResolvedValue(makePage([]));
  api.ListImageFolderGroups.mockResolvedValue([]);
  api.SyncImageDirectories.mockResolvedValue({ added: 0, restored: 0, relocated: 0, removed: 0, skipped: 0, errors: [] });
  api.GetImageDetail.mockImplementation(id => Promise.resolve({ image: makeImage(Number(id)) }));
  api.SetImageFavorite.mockImplementation((id, favorite) => Promise.resolve({ id, is_favorite: favorite }));
  api.SetImageRating.mockImplementation((id, rating) => Promise.resolve({ id, personal_rating: rating }));
  api.DeleteImage.mockResolvedValue();
  api.ListImageTrashEntries.mockResolvedValue([]);
  api.GetImageCleanupStatus.mockResolvedValue(idleCleanupStatus());
  api.StartImageCleanupAnalysis.mockResolvedValue(idleCleanupStatus());
  api.DismissImageNearDuplicateGroup.mockResolvedValue();
  api.BatchDeleteImages.mockResolvedValue({ requested: 0, succeeded: 0, failed: 0, errors: [] });
  api.BatchAddTagToImages.mockResolvedValue({ requested: 0, succeeded: 0, failed: 0, errors: [] });
  api.GetImageSemanticIndexStatus.mockResolvedValue({ available: true, running: false, completed: true, unavailable: '' });
  api.SearchImagesSemantic.mockResolvedValue({ hits: [], coverage: { indexed: 0, total: 0 }, has_more: false });
  api.RetagImage.mockResolvedValue([]);
  api.ListImageAITagCandidates.mockResolvedValue([]);
  api.ApproveImageAITagCandidate.mockResolvedValue({ id: 1, status: 'approved' });
  api.RejectImageAITagCandidate.mockResolvedValue();
  api.GetImageAITaggingSummary.mockResolvedValue({ config_available: true, pending: 0, pending_images: 0 });
  api.ListImageTimelineBuckets.mockResolvedValue([]);
  api.GetImageTags.mockResolvedValue([]);
  api.OpenImageDirectory.mockResolvedValue();
  api.RevealImage.mockResolvedValue();
  api.ListPeople.mockResolvedValue([]);
  api.AddPersonImages.mockResolvedValue();
  api.RemovePersonImage.mockResolvedValue(false);
  // 删除后是否重新分析改为询问用户；默认答"取消"，让结果留在原地继续审阅。
  feedback.confirmAction.mockResolvedValue(false);
  // 清理审阅状态是模块级共享 store，用例之间必须清干净。
  stopPhotoCleanupPolling();
  photoCleanupStore.status = null;
  resetPhotoCleanupReview('');
});

// mountInScrollOwner 把页面挂进一个假的 .main-view 滚动宿主里，让虚拟化真正生效。
// jsdom 不做布局，所以几何全靠桩：
//   - 网格宽度通过 Element.prototype.getBoundingClientRect 在 mount 之前就位，组件第一次
//     测量就能拿到真实宽度，因此这里**不**手动调 syncWindow —— 冷启动首屏走的是组件自己的
//     响应式路径（这正是"翻页/首屏靠 images 侦听刷新窗口"要覆盖的东西）。
//   - scrollTop 的 setter 复刻浏览器的钳制：写入值被 scrollHeight - clientHeight 限制。
//     少了这一条就分不出"锚点在渲染前写"和"渲染后写"，锚点用例会失去意义。
// fallbackContentHeight：网格不在 DOM 里时（清理审阅整页接管）宿主里装的是审阅页，
// 它自己也很高。默认 0 保持既有用例不变；要覆盖"审阅页里滚动"的场景就把它调大。
async function mountInScrollOwner({ viewportHeight = 800, gridWidth = 1200, settings = baseSettings(), fallbackContentHeight = 0 } = {}) {
  const host = document.createElement('div');
  host.className = 'main-view';
  document.body.appendChild(host);

  let scrollTop = 0;
  const rect = (top, width, height) => ({ top, left: 0, width, height, bottom: top + height, right: width, x: 0, y: top, toJSON() {} });
  // 内容高度 = 网格所有直接子节点（占位块 + 行）的内联高度之和，与真实文档流一致。
  const contentHeight = () => {
    const grid = host.querySelector('.photo-grid');
    if (!grid) return fallbackContentHeight;
    return Array.from(grid.children)
      .reduce((sum, child) => sum + (parseFloat(child.style.height) || 0), 0);
  };
  const maxScrollTop = () => Math.max(0, contentHeight() - viewportHeight);

  Object.defineProperty(host, 'clientHeight', { configurable: true, get: () => viewportHeight });
  Object.defineProperty(host, 'scrollHeight', { configurable: true, get: () => Math.max(viewportHeight, contentHeight()) });
  Object.defineProperty(host, 'scrollTop', {
    configurable: true,
    get: () => scrollTop,
    set: value => { scrollTop = Math.max(0, Math.min(Number(value) || 0, maxScrollTop())); }
  });
  host.getBoundingClientRect = () => rect(0, gridWidth, viewportHeight);

  const originalGetRect = Element.prototype.getBoundingClientRect;
  Element.prototype.getBoundingClientRect = function patchedGetRect() {
    if (this === host) return rect(0, gridWidth, viewportHeight);
    if (this.classList?.contains('photo-grid')) return rect(-scrollTop, gridWidth, contentHeight());
    return originalGetRect.call(this);
  };

  const wrapper = mount(PhotoLibraryPage, { props: { settings, tags: [] }, attachTo: host });
  await flushPromises();
  await wrapper.vm.$nextTick();

  const setGridWidth = (width) => { gridWidth = width; };
  const scrollTo = async (position) => {
    host.scrollTop = position;
    host.dispatchEvent(new Event('scroll'));
    await wrapper.vm.$nextTick();
  };
  const cleanup = () => {
    Element.prototype.getBoundingClientRect = originalGetRect;
    wrapper.unmount();
    host.remove();
  };
  return { wrapper, host, scrollTo, setGridWidth, cleanup };
}

// 关键词输入有 300ms 防抖，等待真实定时器触发后再断言，避免测试结束后有游离定时器。
async function typeKeyword(wrapper, value) {
  await wrapper.get('[data-test="photo-keyword"]').setValue(value);
  await new Promise(resolve => setTimeout(resolve, 320));
  await flushPromises();
}

function semanticPage(hits, hasMore = false, coverage = { indexed: hits.length, total: hits.length }) {
  return { hits, coverage, has_more: hasMore };
}

describe('PhotoLibraryPage grid paging', () => {
  it('loads the first page and appends the next page with the returned cursor', async () => {
    const firstPage = Array.from({ length: 60 }, (_, index) => makeImage(index + 1));
    const cursor = { sort_mode: 'recent', created_at: '2026-08-07T09:00:00Z', size: 0, rating_is_null: false, id: 60 };
    api.SearchImagePage
      .mockResolvedValueOnce(makePage(firstPage, cursor))
      .mockResolvedValueOnce(makePage([makeImage(61)], null));

    const wrapper = await mountPage();

    expect(api.SearchImagePage).toHaveBeenCalledTimes(1);
    const firstRequest = api.SearchImagePage.mock.calls[0][0];
    expect(firstRequest.limit).toBe(60);
    expect(firstRequest.cursor).toBeUndefined();
    expect(firstRequest.filter).toMatchObject({ keyword: '', favorite_only: false, sort_mode: 'recent', tag_ids: [] });
    expect(wrapper.findAll('.photo-card')).toHaveLength(60);

    await wrapper.get('[data-test="photo-load-more"]').trigger('click');
    await flushPromises();

    expect(api.SearchImagePage).toHaveBeenCalledTimes(2);
    expect(api.SearchImagePage.mock.calls[1][0].cursor).toEqual(cursor);
    expect(wrapper.findAll('.photo-card')).toHaveLength(61);
    expect(wrapper.find('[data-test="photo-load-more"]').exists()).toBe(false);
  });

  it('resets the cursor and replaces the list when a filter changes', async () => {
    const cursor = { sort_mode: 'recent', created_at: '2026-08-07T09:00:00Z', size: 0, rating_is_null: false, id: 2 };
    api.SearchImagePage
      .mockResolvedValueOnce(makePage([makeImage(1), makeImage(2)], cursor))
      .mockResolvedValueOnce(makePage([makeImage(9, { is_favorite: true })], null));

    const wrapper = await mountPage();
    expect(wrapper.findAll('.photo-card')).toHaveLength(2);

    await openPhotoFilter(wrapper);

    await wrapper.get('[data-test="photo-favorite-only"]').setValue(true);
    await flushPromises();

    expect(api.SearchImagePage).toHaveBeenCalledTimes(2);
    const secondRequest = api.SearchImagePage.mock.calls[1][0];
    expect(secondRequest.cursor).toBeUndefined();
    expect(secondRequest.filter.favorite_only).toBe(true);
    expect(wrapper.findAll('.photo-card')).toHaveLength(1);
    expect(wrapper.text()).toContain('photo-9.jpg');
  });

  it('keeps 最近添加 as the default sort and offers 拍摄时间 as an option', async () => {
    const wrapper = await mountPage();
    const sort = wrapper.get('[data-test="photo-sort"]');

    expect(sort.element.value).toBe('recent');
    expect(api.SearchImagePage.mock.calls[0][0].filter.sort_mode).toBe('recent');
    expect(sort.findAll('option').map(option => option.element.value))
      .toEqual(['recent', 'size', 'rating', 'taken']);

    await sort.setValue('taken');
    await flushPromises();
    expect(api.SearchImagePage.mock.calls[1][0].filter.sort_mode).toBe('taken');
  });

  it('sends the taken date range as a full-day RFC3339 interval', async () => {
    const wrapper = await mountPage();
    expect(api.SearchImagePage.mock.calls[0][0].filter.taken_after).toBeNull();
    expect(api.SearchImagePage.mock.calls[0][0].filter.taken_before).toBeNull();

    await openPhotoFilter(wrapper);

    await wrapper.get('[data-test="photo-taken-after"]').setValue('2024-03-11');
    await flushPromises();
    await openPhotoFilter(wrapper);
    await wrapper.get('[data-test="photo-taken-before"]').setValue('2024-03-12');
    await flushPromises();

    const filter = api.SearchImagePage.mock.calls.at(-1)[0].filter;
    expect(filter.taken_after).toBe(new Date('2024-03-11T00:00:00.000').toISOString());
    expect(filter.taken_before).toBe(new Date('2024-03-12T23:59:59.999').toISOString());
  });
});

describe('PhotoLibraryPage folder display', () => {
  async function openFolderMode() {
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(1), makeImage(2)]));
    api.ListImageFolderGroups.mockResolvedValue([
      { directory: '/photos', name: 'photos', count: 2, covers: [{ id: 1, name: 'one.jpg', format: 'jpg' }] }
    ]);
    const wrapper = await mountPage();
    await wrapper.get('[data-test="photo-display-folders"]').trigger('click');
    await flushPromises();
    return wrapper;
  }

  it('删除文件夹把该目录的图片整体移入回收站，并让分组重新拉取', async () => {
    const wrapper = await openFolderMode();
    feedback.confirmAction.mockResolvedValue(true);
    api.BatchDeleteImagesInDirectory.mockResolvedValue({ requested: 2, succeeded: 2, failed: 0, errors: [] });
    api.ListImageFolderGroups.mockResolvedValue([]);

    await wrapper.get('[data-test="photo-folder-delete"]').trigger('click');
    await flushPromises();
    // eslint-disable-next-line no-console

    expect(api.BatchDeleteImagesInDirectory).toHaveBeenCalledWith('/photos', false);
    // 删空之后不该还剩一个点进去是空的文件夹
    expect(wrapper.findAll('.photo-folder-card')).toHaveLength(0);
  });

  it('确认框答"取消"时什么都不做', async () => {
    const wrapper = await openFolderMode();
    feedback.confirmAction.mockResolvedValueOnce(false);

    await wrapper.get('[data-test="photo-folder-delete"]').trigger('click');
    await flushPromises();

    expect(api.BatchDeleteImagesInDirectory).not.toHaveBeenCalled();
    expect(wrapper.findAll('.photo-folder-card')).toHaveLength(1);
  });

  it('批量删除之后文件夹分组也要作废重拉，不能留着空文件夹', async () => {
    const wrapper = await openFolderMode();
    await wrapper.get('[data-test="photo-display-stream"]').trigger('click');
    await flushPromises();
    wrapper.vm.selectedImageIDs = [1, 2];
    api.BatchDeleteImages.mockResolvedValue({ requested: 2, succeeded: 2, failed: 0, errors: [] });

    await wrapper.vm.performBatchDelete(false);
    await flushPromises();

    expect(wrapper.vm.folderGroups).toEqual([]);
    expect(wrapper.vm.folderLoadedOnce).toBe(false);
  });

  it('loads independent folder albums and enters a folder with an exact directory filter', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(1)]));
    api.ListImageFolderGroups.mockResolvedValue([
      {
        directory: '/photos',
        name: 'photos',
        count: 2,
        covers: [{ id: 1, name: 'one.jpg', format: 'jpg' }, { id: 2, name: 'two.jpg', format: 'jpg' }]
      },
      {
        directory: '/photos/trip',
        name: 'trip',
        count: 1,
        covers: [{ id: 3, name: 'trip.jpg', format: 'jpg' }]
      }
    ]);

    const wrapper = await mountPage();
    await wrapper.get('[data-test="photo-display-folders"]').trigger('click');
    await flushPromises();

    expect(api.ListImageFolderGroups).toHaveBeenCalledWith(expect.objectContaining({ directory: '' }));
    expect(wrapper.findAll('.photo-folder-card')).toHaveLength(2);
    expect(wrapper.find('[data-test="photo-folder-grid"]').text()).toContain('2 张图片');
    expect(wrapper.findAll('.photo-folder-card__covers img')).toHaveLength(3);

    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(7, { directory: '/photos' })]));
    await wrapper.findAll('.photo-folder-card__open')[0].trigger('click');
    await flushPromises();

    expect(api.SearchImagePage.mock.calls.at(-1)[0].filter.directory).toBe('/photos');
    expect(wrapper.get('[data-test="photo-folder-breadcrumb"]').text()).toContain('photos');
    expect(wrapper.findAll('.photo-card')).toHaveLength(1);

    await wrapper.get('[data-test="photo-folder-back"]').trigger('click');
    await flushPromises();
    expect(api.ListImageFolderGroups).toHaveBeenCalledTimes(3);
    expect(wrapper.find('[data-test="photo-folder-grid"]').exists()).toBe(true);
  });
});

describe('PhotoLibraryPage thumbnails and empty state', () => {
  it('falls back to a placeholder with name and format badge when the thumbnail fails', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(5, { name: 'trip.heic', format: 'heic' })]));
    const wrapper = await mountPage();

    expect(wrapper.find('[data-test="photo-thumb-fallback"]').exists()).toBe(false);
    await wrapper.get('.photo-card__media img').trigger('error');

    const fallback = wrapper.get('[data-test="photo-thumb-fallback"]');
    expect(fallback.text()).toContain('trip.heic');
    expect(fallback.text()).toContain('HEIC');
    expect(wrapper.find('.photo-card__media img').exists()).toBe(false);
  });

  it('guides towards settings when no image directory is configured and rescans on demand', async () => {
    api.GetAllImageDirectories.mockResolvedValue([]);
    const wrapper = await mountPage();

    const empty = wrapper.get('[data-test="photo-empty"]');
    expect(empty.text()).toContain('还没有配置图片扫描目录');

    await empty.get('[data-test="photo-empty-settings"]').trigger('click');
    expect(wrapper.emitted('open-settings')).toHaveLength(1);

    api.GetAllImageDirectories.mockResolvedValue([{ id: 1, alias: '相册', path: '/photos' }]);
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(1)]));
    await empty.findAll('button').find(button => button.text() === '立即扫描').trigger('click');
    await flushPromises();

    expect(api.SyncImageDirectories).toHaveBeenCalledTimes(1);
    expect(wrapper.findAll('.photo-card')).toHaveLength(1);
  });
});

describe('PhotoLibraryPage viewer', () => {
  it('opens the lightbox, navigates with arrow keys, favorites with F and closes with Escape', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(1), makeImage(2)]));
    const wrapper = await mountPage();

    await wrapper.findAll('.photo-card__media')[0].trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="photo-viewer"]').exists()).toBe(true);
    expect(api.GetImageDetail).toHaveBeenCalledWith(1);
    expect(wrapper.get('[data-test="photo-ai-candidates-empty"]').text()).toBe('没有待审候选。');

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    await flushPromises();
    expect(api.GetImageDetail).toHaveBeenLastCalledWith(2);
    expect(wrapper.get('.photo-viewer__sidebar h3').text()).toBe('photo-2.jpg');

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft' }));
    await flushPromises();
    expect(wrapper.get('.photo-viewer__sidebar h3').text()).toBe('photo-1.jpg');

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'f' }));
    await flushPromises();
    expect(api.SetImageFavorite).toHaveBeenCalledWith(1, true);

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(wrapper.find('[data-test="photo-viewer"]').exists()).toBe(false);
  });

  it('renders the 拍摄信息 block with camera, exposure and GPS when EXIF is present', async () => {
    const withEXIF = makeImage(4, {
      taken_at: '2024-03-11T08:09:10Z',
      camera_make: 'CineCam',
      camera_model: 'CI-900',
      lens_model: 'CineLens 35mm F2.8',
      iso: 400,
      f_number: 2.8,
      exposure_time: '1/250',
      focal_length: 35,
      gps_latitude: 31.233333,
      gps_longitude: 121.466667
    });
    api.SearchImagePage.mockResolvedValueOnce(makePage([withEXIF]));
    api.GetImageDetail.mockResolvedValue({ image: withEXIF });
    const wrapper = await mountPage();

    await wrapper.get('.photo-card__media').trigger('click');
    await flushPromises();

    const exif = wrapper.get('[data-test="photo-viewer-exif"]').text();
    expect(exif).toContain('CineCam CI-900');
    expect(exif).toContain('CineLens 35mm F2.8');
    expect(exif).toContain('400');
    expect(exif).toContain('f/2.8');
    expect(exif).toContain('1/250 秒');
    expect(exif).toContain('35 mm');
    expect(exif).toContain('31.233333, 121.466667');
  });

  it('omits the 拍摄信息 block entirely when no EXIF field is present', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(5)]));
    const wrapper = await mountPage();

    await wrapper.get('.photo-card__media').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="photo-viewer"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="photo-viewer-exif"]').exists()).toBe(false);
  });

  it('filters tags in the input menu and adds the first match with Enter', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(6)]));
    const wrapper = await mountPage({ tags: [
      { id: 1, name: '旅行' },
      { id: 2, name: '家人' },
      { id: 3, name: '旅行夜景' }
    ] });

    await wrapper.get('.photo-card__media').trigger('click');
    await flushPromises();
    const input = wrapper.get('[data-test="photo-tag-search"]');
    await input.setValue('旅行');

    const options = wrapper.findAll('[data-test="photo-tag-options"] .photo-tag-option').map(option => option.text());
    expect(options).toEqual(['旅行', '旅行夜景']);
    expect(wrapper.find('[data-test="photo-tag-add"]').exists()).toBe(false);

    await input.trigger('keydown', { key: 'Enter' });
    await flushPromises();

    expect(api.AddTagToImage).toHaveBeenCalledWith(6, 1);
    expect(wrapper.find('[data-test="photo-tag-options"]').exists()).toBe(false);
  });

  it('lists this image\'s pending AI tag candidates', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(3)]));
    api.GetImageDetail.mockResolvedValue({ image: makeImage(3) });
    api.ListImageAITagCandidates.mockResolvedValue([
      { id: 11, image_id: 3, suggested_name: '海边', confidence: 'high' }
    ]);
    const wrapper = await mountPage();

    await wrapper.get('.photo-card__media').trigger('click');
    await flushPromises();

    expect(api.ListImageAITagCandidates).toHaveBeenCalledWith(3, '', '');
    expect(wrapper.get('[data-test="photo-ai-candidates"]').text()).toContain('海边');
    expect(wrapper.find('[data-test="photo-ai-candidates-empty"]').exists()).toBe(false);
  });
});

// D-015 / D-021：照片页人物筛选与单图人物维护。
describe('PhotoLibraryPage people', () => {
  function personItem(id, name, { videos = 0, images = 0 } = {}) {
    return {
      person: { id, display_name: name, original_name: '' },
      avatar_url: '',
      active_video_count: videos,
      active_image_count: images
    };
  }

  it('filters by person with AND semantics and drops the filter when removed', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([]));
    api.ListPeople.mockResolvedValue([personItem(7, '周迅'), personItem(8, '张三')]);
    const wrapper = await mountPage();
    await openPhotoFilter(wrapper);

    const input = wrapper.get('[data-test="photo-person-filter-search"]');
    await input.trigger('focus');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledWith('', '', 0, 20);

    await input.setValue('周');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenLastCalledWith('周', '', 0, 20);
    const options = wrapper.findAll('[data-test="photo-person-filter-options"] .photo-tag-option').map(item => item.text());
    expect(options).toEqual(['周迅', '张三']);

    await input.trigger('keydown', { key: 'Enter' });
    await flushPromises();
    expect(api.SearchImagePage.mock.calls.at(-1)[0].filter.person_ids).toEqual([7]);
    expect(wrapper.get('[data-test="photo-person-filter-chips"]').text()).toContain('周迅');

    // 第二个人物是 AND 叠加，不是替换。
    await input.trigger('focus');
    await flushPromises();
    await input.trigger('keydown', { key: 'ArrowDown' });
    await input.trigger('keydown', { key: 'Enter' });
    await flushPromises();
    expect(api.SearchImagePage.mock.calls.at(-1)[0].filter.person_ids).toEqual([7, 8]);

    await wrapper.get('[data-test="photo-person-filter-remove-7"]').trigger('click');
    await flushPromises();
    expect(api.SearchImagePage.mock.calls.at(-1)[0].filter.person_ids).toEqual([8]);
    expect(wrapper.get('[data-test="photo-person-filter-chips"]').text()).not.toContain('周迅');
  });

  it('shows and maintains the people of one image in the viewer', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(9)]));
    api.GetImageDetail.mockResolvedValue({ image: makeImage(9), people: [personItem(3, '既有人物', { videos: 2, images: 1 })] });
    api.ListPeople.mockResolvedValue([personItem(3, '既有人物', { videos: 2, images: 1 }), personItem(4, '新人物')]);
    const wrapper = await mountPage();

    await wrapper.get('.photo-card__media').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="photo-viewer-people"]').text()).toContain('既有人物');

    const input = wrapper.get('[data-test="photo-person-search"]');
    await input.trigger('focus');
    await flushPromises();
    // 已关联的人物不再出现在候选里。
    const options = wrapper.findAll('[data-test="photo-person-options"] .photo-tag-option').map(item => item.text());
    expect(options).toEqual(['新人物']);

    await input.trigger('keydown', { key: 'Enter' });
    await flushPromises();
    expect(api.AddPersonImages).toHaveBeenCalledWith(4, [9]);

    // 该人物还有视频关系，不需要确认。
    await wrapper.get('[data-test="photo-person-remove-3"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.RemovePersonImage).toHaveBeenCalledWith(3, 9);
  });

  // 人物筛选生效时改动该人物的关系会改变网格成员，必须重查。
  it('reloads the grid when the changed person is part of the active filter', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(9)]));
    api.GetImageDetail.mockResolvedValue({ image: makeImage(9), people: [personItem(3, '筛选人物', { videos: 2, images: 1 })] });
    api.ListPeople.mockResolvedValue([personItem(3, '筛选人物', { videos: 2, images: 1 })]);
    const wrapper = await mountPage();
    await openPhotoFilter(wrapper);

    const filterInput = wrapper.get('[data-test="photo-person-filter-search"]');
    await filterInput.trigger('focus');
    await flushPromises();
    await filterInput.trigger('keydown', { key: 'Enter' });
    await flushPromises();
    expect(api.SearchImagePage.mock.calls.at(-1)[0].filter.person_ids).toEqual([3]);
    const queriesAfterFilter = api.SearchImagePage.mock.calls.length;

    await wrapper.findAll('.photo-card__media')[0].trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="photo-person-remove-3"]').trigger('click');
    await flushPromises();

    expect(api.RemovePersonImage).toHaveBeenCalledWith(3, 9);
    // 关系没了，这张图不再命中筛选：网格重查，筛选条件本身保留。
    expect(api.SearchImagePage.mock.calls.length).toBeGreaterThan(queriesAfterFilter);
    expect(api.SearchImagePage.mock.calls.at(-1)[0].filter.person_ids).toEqual([3]);
    expect(wrapper.find('[data-test="photo-viewer"]').exists()).toBe(false);
  });

  it('does not reload the grid when the changed person is not filtered', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(9)]));
    api.GetImageDetail.mockResolvedValue({ image: makeImage(9), people: [personItem(3, '无关人物', { videos: 2, images: 1 })] });
    const wrapper = await mountPage();

    await wrapper.findAll('.photo-card__media')[0].trigger('click');
    await flushPromises();
    const queriesBefore = api.SearchImagePage.mock.calls.length;

    await wrapper.get('[data-test="photo-person-remove-3"]').trigger('click');
    await flushPromises();

    expect(api.RemovePersonImage).toHaveBeenCalledWith(3, 9);
    expect(api.SearchImagePage.mock.calls.length).toBe(queriesBefore);
    expect(wrapper.find('[data-test="photo-viewer"]').exists()).toBe(true);
  });

  it('confirms before removing the person\'s final relationship', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(10)]));
    api.GetImageDetail.mockResolvedValue({ image: makeImage(10), people: [personItem(5, '最后关系', { videos: 0, images: 1 })] });
    const wrapper = await mountPage();

    await wrapper.get('.photo-card__media').trigger('click');
    await flushPromises();

    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="photo-person-remove-5"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledTimes(1);
    expect(api.RemovePersonImage).not.toHaveBeenCalled();

    feedback.confirmAction.mockResolvedValueOnce(true);
    await wrapper.get('[data-test="photo-person-remove-5"]').trigger('click');
    await flushPromises();
    expect(api.RemovePersonImage).toHaveBeenCalledWith(5, 10);
  });
});

describe('PhotoLibraryPage AI retag and candidate review', () => {
  async function openViewer(candidates = []) {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(3)]));
    api.GetImageDetail.mockResolvedValue({ image: makeImage(3) });
    api.ListImageAITagCandidates.mockResolvedValue(candidates);
    const wrapper = await mountPage();
    await wrapper.get('.photo-card__media').trigger('click');
    await flushPromises();
    return wrapper;
  }

  it('retags in place and shows the returned candidates', async () => {
    const wrapper = await openViewer();
    expect(wrapper.find('[data-test="photo-ai-candidates-empty"]').exists()).toBe(true);

    let resolveCall;
    api.RetagImage.mockReturnValueOnce(new Promise(resolve => { resolveCall = resolve; }));
    await wrapper.get('[data-test="photo-ai-retag"]').trigger('click');
    await wrapper.vm.$nextTick();

    const button = wrapper.get('[data-test="photo-ai-retag"]');
    expect(button.attributes('disabled')).toBeDefined();
    expect(button.text()).toBe('打标中...');

    resolveCall([{ id: 21, image_id: 3, suggested_name: '日落', confidence: 'medium' }]);
    await flushPromises();

    expect(api.RetagImage).toHaveBeenCalledWith(3);
    expect(wrapper.get('[data-test="photo-ai-candidates"]').text()).toContain('日落');
    expect(wrapper.find('[data-test="photo-ai-candidates-empty"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="photo-ai-retag"]').attributes('disabled')).toBeUndefined();
  });

  it('keeps the placeholder and reports an unavailable AI configuration', async () => {
    const wrapper = await openViewer();
    api.RetagImage.mockRejectedValueOnce('AI 配置不可用: BaseURL 或 Model 为空');

    await wrapper.get('[data-test="photo-ai-retag"]').trigger('click');
    await flushPromises();

    const error = wrapper.get('[data-test="photo-ai-retag-error"]');
    expect(error.text()).toContain('AI 配置不可用');
    expect(error.text()).toContain('请先在设置页配置 AI 接口');
    expect(wrapper.find('[data-test="photo-ai-candidates-empty"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="photo-ai-retag"]').attributes('disabled')).toBeUndefined();
  });

  it('approves a candidate from the viewer and refreshes the image', async () => {
    const wrapper = await openViewer([{ id: 31, image_id: 3, suggested_name: '海边', confidence: 'high' }]);
    api.ApproveImageAITagCandidate.mockResolvedValueOnce({ id: 31, status: 'approved' });
    api.ListImageAITagCandidates.mockResolvedValue([]);

    await wrapper.get('[data-test="photo-ai-candidate-approve-31"]').trigger('click');
    await flushPromises();

    expect(api.ApproveImageAITagCandidate).toHaveBeenCalledWith(31);
    expect(wrapper.find('[data-test="photo-ai-candidates-empty"]').exists()).toBe(true);
  });

  // 后端在图片已有手工标签时返回 superseded 而不是报错，界面必须解释标签为什么没挂上。
  it('explains why approving wrote nothing when the image has manual tags', async () => {
    const wrapper = await openViewer([{ id: 32, image_id: 3, suggested_name: '海边', confidence: 'high' }]);
    api.ApproveImageAITagCandidate.mockResolvedValueOnce({ id: 32, status: 'superseded' });
    api.ListImageAITagCandidates.mockResolvedValue([]);

    await wrapper.get('[data-test="photo-ai-candidate-approve-32"]').trigger('click');
    await flushPromises();

    expect(wrapper.get('[data-test="photo-ai-retag-error"]').text()).toContain('手工打的标签');
  });

  it('rejects a candidate without touching the image tags', async () => {
    const wrapper = await openViewer([{ id: 33, image_id: 3, suggested_name: '海边', confidence: 'high' }]);
    api.RejectImageAITagCandidate.mockResolvedValueOnce();
    api.ListImageAITagCandidates.mockResolvedValue([]);

    await wrapper.get('[data-test="photo-ai-candidate-reject-33"]').trigger('click');
    await flushPromises();

    expect(api.RejectImageAITagCandidate).toHaveBeenCalledWith(33);
    expect(api.ApproveImageAITagCandidate).not.toHaveBeenCalled();
  });
});

describe('PhotoLibraryPage semantic search', () => {
  it('switches to semantic mode, renders scored hits and appends by offset', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(1)]));
    const wrapper = await mountPage();

    await wrapper.get('[data-test="photo-mode-semantic"]').trigger('click');
    await flushPromises();

    expect(api.SearchImagesSemantic).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="photo-semantic-prompt"]').exists()).toBe(true);

    api.SearchImagesSemantic
      .mockResolvedValueOnce(semanticPage([{ image: makeImage(21), score: 0.9 }], true, { indexed: 4, total: 6 }))
      .mockResolvedValueOnce(semanticPage([{ image: makeImage(22), score: 0.42 }], false, { indexed: 4, total: 6 }));

    await typeKeyword(wrapper, '海边日落');

    expect(api.SearchImagesSemantic).toHaveBeenCalledTimes(1);
    const firstRequest = api.SearchImagesSemantic.mock.calls[0][0];
    expect(firstRequest.query).toBe('海边日落');
    expect(firstRequest.offset).toBe(0);
    expect(firstRequest.limit).toBe(60);
    expect(firstRequest.filter).toEqual({
      tag_ids: [], favorite_only: false, min_rating: null, max_rating: null, min_size: 0, max_size: 0
    });
    expect(firstRequest.filter.sort_mode).toBeUndefined();
    expect(wrapper.findAll('.photo-card')).toHaveLength(1);
    expect(wrapper.text()).toContain('相关度 0.90');

    await wrapper.get('[data-test="photo-load-more"]').trigger('click');
    await flushPromises();

    expect(api.SearchImagesSemantic.mock.calls[1][0].offset).toBe(1);
    expect(wrapper.findAll('.photo-card')).toHaveLength(2);
    expect(wrapper.find('[data-test="photo-load-more"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('carries the shared filters that semantic search supports', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([]));
    api.GetImageTags.mockResolvedValue([{ id: 5, name: '风景', color: '#123456' }]);
    const wrapper = await mountPage({ tags: [{ id: 5, name: '风景', color: '#123456' }] });

    await openPhotoFilter(wrapper);

    await wrapper.get('[data-test="photo-favorite-only"]').setValue(true);
    await flushPromises();
    await wrapper.findAll('.tag-chip')[0].trigger('click');
    await flushPromises();
    await openPhotoFilter(wrapper);
    await wrapper.get('[data-test="photo-min-rating"]').setValue('6');
    await flushPromises();

    await wrapper.get('[data-test="photo-mode-semantic"]').trigger('click');
    await flushPromises();
    api.SearchImagesSemantic.mockResolvedValueOnce(semanticPage([]));
    await typeKeyword(wrapper, '雪山');

    expect(api.SearchImagesSemantic.mock.calls[0][0].filter).toEqual({
      tag_ids: [5], favorite_only: true, min_rating: 6, max_rating: null, min_size: 0, max_size: 0
    });
    wrapper.unmount();
  });

  it('disables the semantic mode with the backend reason when the capability is unavailable', async () => {
    api.GetImageSemanticIndexStatus.mockResolvedValue({ available: false, unavailable: 'pgvector 扩展不可用' });
    const wrapper = await mountPage();

    const modeButton = wrapper.get('[data-test="photo-mode-semantic"]');
    expect(modeButton.attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="photo-semantic-unavailable"]').text()).toContain('pgvector 扩展不可用');

    wrapper.vm.setSearchMode('semantic');
    await flushPromises();

    expect(wrapper.vm.searchMode).toBe('name');
    expect(api.SearchImagesSemantic).not.toHaveBeenCalled();
  });

  it('reports a semantic search failure and drops back to filename mode when the capability is gone', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(1)]));
    const wrapper = await mountPage();

    await wrapper.get('[data-test="photo-mode-semantic"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.searchMode).toBe('semantic');

    api.SearchImagesSemantic.mockRejectedValueOnce('语义查询向量生成失败: embedding 模型未配置');
    api.GetImageSemanticIndexStatus.mockResolvedValue({ available: false, unavailable: 'embedding 模型未配置' });
    await typeKeyword(wrapper, '雨天的街道');

    expect(wrapper.get('[data-test="photo-semantic-unavailable"]').text()).toContain('语义搜索失败');
    expect(wrapper.get('[data-test="photo-semantic-unavailable"]').text()).toContain('embedding 模型未配置');
    expect(wrapper.vm.searchMode).toBe('name');
    expect(wrapper.get('[data-test="photo-mode-semantic"]').attributes('disabled')).toBeDefined();
    wrapper.unmount();
  });

  it('keeps cursor paging and semantic offset paging isolated across mode switches', async () => {
    const cursor = { sort_mode: 'recent', created_at: '2026-08-07T09:00:00Z', size: 0, rating_is_null: false, id: 1 };
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(1)], cursor));
    const wrapper = await mountPage();

    await wrapper.get('[data-test="photo-load-more"]').trigger('click');
    await flushPromises();
    expect(api.SearchImagePage.mock.calls[1][0].cursor).toEqual(cursor);

    api.SearchImagesSemantic.mockResolvedValue(semanticPage([{ image: makeImage(31), score: 0.7 }], true));
    await wrapper.get('[data-test="photo-mode-semantic"]').trigger('click');
    await typeKeyword(wrapper, '猫');

    expect(api.SearchImagesSemantic.mock.calls[0][0].offset).toBe(0);
    expect(api.SearchImagesSemantic.mock.calls[0][0].cursor).toBeUndefined();
    await wrapper.get('[data-test="photo-load-more"]').trigger('click');
    await flushPromises();
    expect(api.SearchImagesSemantic.mock.calls[1][0].offset).toBe(1);

    api.SearchImagePage.mockClear();
    await wrapper.get('[data-test="photo-mode-name"]').trigger('click');
    await flushPromises();

    expect(api.SearchImagePage).toHaveBeenCalledTimes(1);
    const backRequest = api.SearchImagePage.mock.calls[0][0];
    expect(backRequest.cursor).toBeUndefined();
    expect(backRequest.offset).toBeUndefined();
    expect(wrapper.vm.semanticOffset).toBe(0);
    expect(wrapper.findAll('.photo-card')).toHaveLength(1);
    expect(wrapper.text()).not.toContain('相关度');
    wrapper.unmount();
  });
});

describe('PhotoLibraryPage delete', () => {
  it('multi-selects images and applies a tag in one batch', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(1), makeImage(2)]));
    api.BatchAddTagToImages.mockResolvedValue({ requested: 2, succeeded: 2, failed: 0, errors: [] });
    const wrapper = await mountPage({ tags: [{ id: 7, name: '旅行' }] });

    await wrapper.get('[data-test="photo-select-all"]').trigger('click');
    const input = wrapper.get('[data-test="photo-batch-tag-search"]');
    await input.setValue('旅');
    expect(wrapper.get('[data-test="photo-batch-tag-options"]').text()).toContain('旅行');
    expect(wrapper.find('[data-test="photo-batch-tag-add"]').exists()).toBe(false);
    await input.trigger('keydown', { key: 'Enter' });
    await flushPromises();

    expect(api.BatchAddTagToImages).toHaveBeenCalledWith([1, 2], 7);
  });

  it('deletes directly with the settings default and removes the card from the list', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(1), makeImage(2)]));
    const wrapper = await mountPage({ settings: { confirm_before_delete: false, delete_original_file: true } });

    await wrapper.findAll('[data-test="photo-card-delete"]')[0].trigger('click');
    await flushPromises();

    expect(api.DeleteImage).toHaveBeenCalledWith(1, true);
    expect(wrapper.findAll('.photo-card')).toHaveLength(1);
    expect(wrapper.text()).not.toContain('photo-1.jpg');
  });

  it('asks for confirmation first when confirm_before_delete is enabled', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(4)]));
    const wrapper = await mountPage({ settings: { confirm_before_delete: true, delete_original_file: false } });

    await wrapper.get('[data-test="photo-card-delete"]').trigger('click');
    expect(api.DeleteImage).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="photo-delete-file"]').element.checked).toBe(false);

    await wrapper.get('[data-test="photo-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(api.DeleteImage).toHaveBeenCalledWith(4, false);
    expect(wrapper.findAll('.photo-card')).toHaveLength(0);
  });
});

describe('PhotoLibraryPage AI tag filter', () => {
  it('sends the selected AI tag state and reloads', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(1)]));
    const wrapper = await mountPage();
    expect(api.SearchImagePage.mock.calls[0][0].filter.ai_tag_state).toBe('');

    await openPhotoFilter(wrapper);

    await wrapper.get('[data-test="photo-ai-state"]').setValue('pending');
    await flushPromises();

    const last = api.SearchImagePage.mock.calls.at(-1)[0];
    expect(last.filter.ai_tag_state).toBe('pending');
    expect(last.cursor).toBeUndefined();

    await openPhotoFilter(wrapper);

    await wrapper.get('[data-test="photo-ai-state"]').setValue('untagged');
    await flushPromises();
    expect(api.SearchImagePage.mock.calls.at(-1)[0].filter.ai_tag_state).toBe('untagged');
  });

  it('disables the AI filter in semantic search mode', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([]));
    const wrapper = await mountPage();
    await wrapper.get('[data-test="photo-mode-semantic"]').trigger('click');
    await flushPromises();
    await openPhotoFilter(wrapper);
    expect(wrapper.get('[data-test="photo-ai-state"]').attributes('disabled')).toBeDefined();
  });
});

describe('PhotoLibraryPage cleanup review', () => {
  const exactGroup = () => ({
    original: makeImage(1, { name: 'keep.jpg', width: 4000, height: 3000 }),
    candidates: [makeImage(2, { name: 'copy.jpg', width: 800, height: 600 })],
    reason: '文件大小和采样哈希一致'
  });
  const nearGroup = () => ({
    original: makeImage(3, { name: 'near-big.jpg', width: 4000, height: 3000 }),
    candidates: [makeImage(4, { name: 'near-small.jpg', width: 1000, height: 750 })],
    reason: '感知哈希相近，可能是同图不同尺寸或压缩（不会默认选中）'
  });

  async function openCleanup(analysis, { staleHashCount = 0 } = {}) {
    api.SearchImagePage.mockResolvedValue(makePage([]));
    const wrapper = await mountPage();
    api.StartImageCleanupAnalysis.mockResolvedValue(
      completedCleanupStatus({ ...analysis, stale_hash_count: staleHashCount })
    );
    api.GetImageCleanupStatus.mockResolvedValue(
      completedCleanupStatus({ ...analysis, stale_hash_count: staleHashCount })
    );
    await openPhotoManageItem(wrapper, 'cleanup');
    await flushPromises();
    return wrapper;
  }

  it('opens the panel and starts an analysis when the backend has nothing to show', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([]));
    const wrapper = await mountPage();

    expect(wrapper.find('[data-test="photo-cleanup-page"]').exists()).toBe(false);

    await openPhotoManageItem(wrapper, 'cleanup');
    await flushPromises();

    expect(wrapper.find('[data-test="photo-cleanup-page"]').exists()).toBe(true);
    // 与视频侧同构（用户裁决）：既没在跑也没有可用结果时，打开面板就自动分析一次。
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
  });

  it('renders both group sections grouped by directory, auto-checks same-dir candidates, and shows the stale hash hint', async () => {
    const wrapper = await openCleanup(
      { duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] },
      { staleHashCount: 7 }
    );

    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    // 顶层就是目录：两组的推荐保留项都在 /photos，所以只有一个目录区，下挂两个组。
    const sections = wrapper.findAll('[data-test="cleanup-dir-section"]');
    expect(sections).toHaveLength(1);
    const cards = sections[0].findAll('[data-test="cleanup-group-card"]');
    expect(cards.map(card => card.attributes('data-kind'))).toEqual(['exact', 'near']);

    // 每个成员都有勾选框；两类都按"保留推荐那份、其余勾删"预置（用户裁决）。
    const toggles = wrapper.findAll('[data-test="cleanup-candidate-toggle"]');
    expect(toggles).toHaveLength(4);
    const checked = toggles.filter(t => t.element.checked === true);
    expect(checked.map(t => t.attributes('aria-label')).join(' ')).toContain('copy.jpg');
    expect(checked.map(t => t.attributes('aria-label')).join(' ')).toContain('near-small.jpg');
    expect(checked).toHaveLength(2);

    // 目录分组标题可见并显示路径。
    const dirToggles = wrapper.findAll('[data-test="cleanup-dir-toggle"]');
    expect(dirToggles.length).toBeGreaterThan(0);
    expect(dirToggles[0].text()).toContain('/photos');

    const thumbs = wrapper.findAll('.cleanup-member__thumb img');
    expect(thumbs[0].attributes('src')).toBe('/preview/image-thumbnail/1');
    expect(wrapper.text()).toContain('4000×3000');

    expect(wrapper.get('[data-test="cleanup-stale-hint"]').text()).toContain('7');
    expect(wrapper.get('[data-test="cleanup-stale-hint"]').text()).toContain('浏览图片可自动刷新缩略图与指纹');
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(2)');
  });

  it('collapses and expands a directory group', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactGroup()], near_duplicate_groups: [] });

    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    // exactGroup 两个成员（保留项 + 候选）同目录，共 2 行，候选勾选框 1 个。
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]')).toHaveLength(2);
    // 折叠后成员行隐藏。
    await wrapper.get('[data-test="cleanup-dir-toggle"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]')).toHaveLength(0);
    // 展开后恢复。
    await wrapper.get('[data-test="cleanup-dir-toggle"]').trigger('click');
    await flushPromises();
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]')).toHaveLength(2);
  });

  it('lets the user switch which copy to keep and re-marks the rest for deletion', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactGroup()], near_duplicate_groups: [] });

    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    // 初始：建议保留 id=1，候选 id=2 默认勾选待删。
    let keepRadios = wrapper.findAll('[data-test="cleanup-keep-toggle"]');
    expect(keepRadios).toHaveLength(2);
    expect(keepRadios[0].element.checked).toBe(true); // id=1
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');

    // 切换保留 id=2：id=1 变为待删，删除集仍是 1 个但换成了 id=1。
    await keepRadios[1].setValue(true);
    await flushPromises();

    keepRadios = wrapper.findAll('[data-test="cleanup-keep-toggle"]');
    expect(keepRadios[1].element.checked).toBe(true); // id=2
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');

    api.SearchImagePage.mockClear();
    await wrapper.get('[data-test="cleanup-delete-selected"]').trigger('click');
    await flushPromises();
    expect(api.BatchDeleteImages).toHaveBeenCalledWith([1], true);
  });

  it('lets the user skip a whole group so nothing in it is deleted, then restore it', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactGroup()], near_duplicate_groups: [] });

    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    // 初始候选 id=2 默认勾选待删。
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');

    // 跳过本组：勾选清空、删除计数归零、勾选框禁用。
    await wrapper.get('[data-test="cleanup-skip-group"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(0)');
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]').every(t => t.element.disabled)).toBe(true);

    // 恢复本组：按当前保留项重算默认勾选，id=2 重新进入待删。
    await wrapper.get('[data-test="cleanup-skip-group"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');
  });

  it('keeps the reviewed result after deleting and only re-analyses when the user confirms', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] });

    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);

    // 答"取消"：不重跑，结果留在原地，已删的行置灰标注，剩下的组还能继续审阅。
    await wrapper.get('[data-test="cleanup-delete-selected"]').trigger('click');
    await flushPromises();

    expect(feedback.confirmAction).toHaveBeenCalledTimes(1);
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(2);
    expect(wrapper.findAll('[data-test="cleanup-member-deleted"]')).toHaveLength(2);
    expect(wrapper.get('[data-test="cleanup-outdated-hint"]').exists()).toBe(true);
  });

  it('keeps the review progress when the panel is closed and reopened', async () => {
    const status = completedCleanupStatus({
      duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()], stale_hash_count: 0
    });
    status.started_at = '2026-08-08T09:00:00Z';
    api.GetImageCleanupStatus.mockResolvedValue(status);
    api.StartImageCleanupAnalysis.mockResolvedValue(status);
    api.SearchImagePage.mockResolvedValue(makePage([]));
    const wrapper = await mountPage();
    await openPhotoManageItem(wrapper, 'cleanup');
    await flushPromises();

    // 两类默认都勾上；手动取消掉近似重复那张，然后折叠目录，模拟审阅到一半。
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(2)');
    const nearToggle = wrapper.findAll('[data-test="cleanup-candidate-toggle"]')
      .find(toggle => toggle.attributes('aria-label').includes('near-small'));
    await nearToggle.setValue(false);
    await wrapper.get('[data-test="cleanup-dir-toggle"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');

    // 关掉面板再打开：勾选和折叠都还在，不用从头再勾一遍。
    await wrapper.get('[data-test="photo-cleanup-page"] .btn-secondary').trigger('click');
    await flushPromises();
    await openPhotoManageItem(wrapper, 'cleanup');
    await flushPromises();

    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');
    expect(wrapper.findAll('[data-test="cleanup-candidate-toggle"]')).toHaveLength(0);
  });

  it('keeps the results visible when part of the deletion fails and only greys the ones that went through', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] });
    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    // 手动把近似重复那张也勾上，凑成两张；其中 id=4 删除失败。
    const nearToggle = wrapper.findAll('[data-test="cleanup-candidate-toggle"]')
      .find(toggle => toggle.attributes('aria-label').includes('near-small'));
    await nearToggle.setValue(true);
    api.BatchDeleteImages.mockResolvedValue({
      requested: 2, succeeded: 1, failed: 1, errors: [{ image_id: 4, error: 'record not found' }]
    });
    await wrapper.get('[data-test="cleanup-delete-selected"]').trigger('click');
    await flushPromises();

    // 结果没有被错误信息顶掉，两组还在，可以接着审阅。
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(2);
    expect(wrapper.get('[data-test="cleanup-error"]').text()).toContain('1 张图片删除失败');
    // 只有真正删掉的那张置灰；失败的那张仍然勾着可以重试。
    expect(wrapper.findAll('[data-test="cleanup-member-deleted"]')).toHaveLength(1);
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');
  });

  it('shows per-item facts, diffs against the kept copy, and can open the directory or reveal a file', async () => {
    const wrapper = await openCleanup({
      duplicate_groups: [{
        original: {
          ...makeImage(1, { name: 'keep.jpg', width: 4000, height: 3000, directory: '/photos' }),
          file_size: 8_000_000,
          mod_time_ns: Date.parse('2026-03-01T00:00:00Z') * 1e6,
          tags: [{ id: 7, name: '旅行', color: '#0d9488' }],
          is_favorite: true
        },
        candidates: [{
          ...makeImage(2, { name: 'copy.jpg', width: 800, height: 600, directory: '/backup' }),
          file_size: 1_000_000,
          mod_time_ns: Date.parse('2026-03-03T00:00:00Z') * 1e6,
          tags: []
        }],
        reason: '文件大小和采样哈希一致',
        max_hamming_distance: 0
      }],
      near_duplicate_groups: []
    });
    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    const text = wrapper.text();
    // 尺寸、体积、时间、元数据、AI 描述都要在卡片上直接看得到。
    expect(text).toContain('4000×3000');
    expect(text).toContain('7.6 MB');
    expect(text).toContain('★ 已收藏');
    expect(text).toContain('旅行');
    // 清理审阅刻意不展示 AI 内容：近似重复的两张图标签几乎一定相同，在这里帮不上判断。
    expect(wrapper.find('[data-test="cleanup-member-description"]').exists()).toBe(false);
    // 与保留项的差异直接标出来，不用自己换算。
    expect(text).toContain('仅 1/25');
    expect(text).toContain('小 87%');
    expect(text).toContain('晚 2 天');
    // 跨目录的副本要有醒目标记。
    expect(wrapper.get('[data-test="cleanup-member-otherdir"]').text()).toContain('/backup');
    expect(wrapper.get('[data-test="cleanup-cross-dir"]').exists()).toBe(true);

    await wrapper.get('[data-test="cleanup-open-dir"]').trigger('click');
    await flushPromises();
    expect(api.OpenImageDirectory).toHaveBeenCalledWith('/photos');

    await wrapper.findAll('[data-test="cleanup-reveal"]')[1].trigger('click');
    await flushPromises();
    expect(api.RevealImage).toHaveBeenCalledWith(2);
  });

  it('marks every extra copy of both exact and near duplicates by default', async () => {
    const wrapper = await openCleanup({
      duplicate_groups: [{
        original: makeImage(11, { name: 'keep.jpg', directory: '/photos' }),
        candidates: [
          makeImage(12, { name: 'same-dir-copy.jpg', directory: '/photos' }),
          makeImage(13, { name: 'other-dir-copy.jpg', directory: '/backup' })
        ],
        reason: '文件大小和采样哈希一致'
      }],
      near_duplicate_groups: [nearGroup()]
    });
    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    // 两份精确副本都勾上（不管在不在同一目录），近似重复那份也按建议勾上。
    const checkedLabels = wrapper.findAll('[data-test="cleanup-candidate-toggle"]')
      .filter(toggle => toggle.element.checked)
      .map(toggle => toggle.attributes('aria-label'));
    expect(checkedLabels).toHaveLength(3);
    expect(checkedLabels.join(' ')).toContain('same-dir-copy.jpg');
    expect(checkedLabels.join(' ')).toContain('other-dir-copy.jpg');

    // 打开"只勾同目录"后，跨目录那份退出待删集合。
    await wrapper.get('[data-test="cleanup-samedir-switch"] input').setValue(true);
    await flushPromises();
    const narrowed = wrapper.findAll('[data-test="cleanup-candidate-toggle"]')
      .filter(toggle => toggle.element.checked)
      .map(toggle => toggle.attributes('aria-label'));
    expect(narrowed.join(' ')).toContain('same-dir-copy.jpg');
    expect(narrowed.join(' ')).not.toContain('other-dir-copy.jpg');
  });

  it('re-reads the backend status when the panel opens so an outdated result is flagged', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([]));
    const wrapper = await mountPage();

    // 面板打开时后端已经把结果标记为过期（例如刚从网格里删了一张图）。
    api.GetImageCleanupStatus.mockResolvedValue(
      completedCleanupStatus({ duplicate_groups: [exactGroup()], near_duplicate_groups: [], stale_hash_count: 0 }, { stale: true })
    );
    await openPhotoManageItem(wrapper, 'cleanup');
    await flushPromises();

    expect(wrapper.find('[data-test="cleanup-outdated-hint"]').exists()).toBe(true);
    expect(wrapper.findAll('[data-test="cleanup-group-card"]')).toHaveLength(1);
  });

  it('deletes only the selected candidates into the trash and re-analyses when confirmed', async () => {
    feedback.confirmAction.mockResolvedValue(true);
    const wrapper = await openCleanup({ duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] });

    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    // 两个候选（id=2、id=4）均默认勾选，取消勾选 id=4 那一行（保留项是 id=1、id=3）。
    const toggles = wrapper.findAll('[data-test="cleanup-candidate-toggle"]');
    const target = toggles.find(t => Number(t.attributes('aria-label').match(/\d+/)) === 4 || t.attributes('aria-label').includes('near-small'));
    await target.setValue(false);
    await flushPromises();

    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');

    api.SearchImagePage.mockClear();
    await wrapper.get('[data-test="cleanup-delete-selected"]').trigger('click');
    await flushPromises();

    expect(api.BatchDeleteImages).toHaveBeenCalledWith([2], true);
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(2);
    expect(api.SearchImagePage).toHaveBeenCalled();
  });

  it('dismisses a near-duplicate group with every member id and removes it from the list', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [], near_duplicate_groups: [nearGroup()] });

    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    await wrapper.get('[data-test="cleanup-dismiss-group"]').trigger('click');
    await flushPromises();

    expect(api.DismissImageNearDuplicateGroup).toHaveBeenCalledWith([3, 4]);
    expect(wrapper.find('[data-test="cleanup-near-section"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="cleanup-empty"]').exists()).toBe(true);
  });
});

describe('PhotoLibraryPage timeline grouping', () => {
  const timelineImages = () => [
    makeImage(1, { taken_at: '2026-08-21T10:00:00' }),
    makeImage(2, { taken_at: '2026-08-03T10:00:00' }),
    makeImage(3, { taken_at: '2026-07-09T10:00:00' })
  ];

  it('forces the taken sort, pulls the bucket summary and labels each group with the backend total', async () => {
    api.SearchImagePage
      .mockResolvedValueOnce(makePage([makeImage(9)]))
      .mockResolvedValueOnce(makePage(timelineImages()));
    api.ListImageTimelineBuckets.mockResolvedValue([
      { year: 2026, month: 8, count: 128 },
      { year: 2026, month: 7, count: 40 }
    ]);

    const wrapper = await mountPage();
    expect(api.ListImageTimelineBuckets).not.toHaveBeenCalled();
    expect(wrapper.findAll('[data-test="photo-timeline-header"]')).toHaveLength(0);

    await wrapper.get('[data-test="photo-timeline-toggle"]').setValue(true);
    await flushPromises();

    expect(api.SearchImagePage.mock.calls[1][0].filter.sort_mode).toBe('taken');
    expect(api.ListImageTimelineBuckets).toHaveBeenCalledTimes(1);
    expect(api.ListImageTimelineBuckets.mock.calls[0][0]).toMatchObject({ sort_mode: 'taken', keyword: '', tag_ids: [] });

    const headers = wrapper.findAll('[data-test="photo-timeline-header"]');
    expect(headers.map(header => header.text())).toEqual(['2026 年 8 月 · 128 张', '2026 年 7 月 · 40 张']);
    // 分组头是整行穿插的，卡片仍是全部 3 张。
    expect(wrapper.findAll('.photo-card')).toHaveLength(3);
    expect(wrapper.get('[data-test="photo-sort"]').attributes('disabled')).toBeDefined();
    wrapper.unmount();
  });

  it('keeps 最近添加 as the default and restores the chosen sort when timeline mode is switched off', async () => {
    api.SearchImagePage.mockResolvedValue(makePage(timelineImages()));
    const wrapper = await mountPage();
    expect(api.SearchImagePage.mock.calls[0][0].filter.sort_mode).toBe('recent');

    await wrapper.get('[data-test="photo-sort"]').setValue('size');
    await flushPromises();
    expect(api.SearchImagePage.mock.calls.at(-1)[0].filter.sort_mode).toBe('size');

    await wrapper.get('[data-test="photo-timeline-toggle"]').setValue(true);
    await flushPromises();
    expect(api.SearchImagePage.mock.calls.at(-1)[0].filter.sort_mode).toBe('taken');

    await wrapper.get('[data-test="photo-timeline-toggle"]').setValue(false);
    await flushPromises();
    expect(api.SearchImagePage.mock.calls.at(-1)[0].filter.sort_mode).toBe('size');
    expect(wrapper.get('[data-test="photo-sort"]').element.value).toBe('size');
    expect(wrapper.findAll('[data-test="photo-timeline-header"]')).toHaveLength(0);
    wrapper.unmount();
  });

  it('shows the year and month without a count when the bucket summary fails', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(1, { taken_at: '2026-08-21T10:00:00' })]));
    api.ListImageTimelineBuckets.mockRejectedValue(new Error('分组计数失败'));

    const wrapper = await mountPage();
    await wrapper.get('[data-test="photo-timeline-toggle"]').setValue(true);
    await flushPromises();

    expect(wrapper.get('[data-test="photo-timeline-header"]').text()).toBe('2026 年 8 月');
    expect(wrapper.text()).toContain('加载时间线分组失败');
    wrapper.unmount();
  });

  it('disables timeline grouping in semantic mode', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(1)]));
    const wrapper = await mountPage();

    await wrapper.get('[data-test="photo-mode-semantic"]').trigger('click');
    await flushPromises();

    expect(wrapper.get('[data-test="photo-timeline-toggle"]').attributes('disabled')).toBeDefined();
    wrapper.unmount();
  });
});

describe('PhotoLibraryPage grid virtualization', () => {
  it('keeps the rendered card count bounded by the viewport for a ten-thousand image library', async () => {
    const images = Array.from({ length: 10000 }, (_, index) => makeImage(index + 1));
    api.SearchImagePage.mockResolvedValue(makePage(images, null));

    const { wrapper, scrollTo, cleanup } = await mountInScrollOwner();

    // 1200px 宽、180px 最小列宽、12px 间隙 => 6 列；视口 800px + 3 行 overscan。
    expect(wrapper.vm.columns).toBe(6);
    const atTop = wrapper.findAll('.photo-card');
    expect(atTop.length).toBeGreaterThan(0);
    expect(atTop.length).toBeLessThan(100);
    expect(wrapper.vm.layout.rows.length).toBeGreaterThan(1600);

    await scrollTo(Math.floor(wrapper.vm.layout.totalHeight / 2));
    const atMiddle = wrapper.findAll('.photo-card');
    expect(atMiddle.length).toBeLessThan(100);
    // 窗口确实跟着滚动位置走，而不是永远渲染开头那批。
    expect(atMiddle[0].text()).not.toContain('photo-1.jpg');
    expect(wrapper.vm.windowState.startRow).toBeGreaterThan(700);
    expect(wrapper.vm.windowState.topSpacer).toBeGreaterThan(0);
    expect(wrapper.vm.windowState.bottomSpacer).toBeGreaterThan(0);

    await scrollTo(wrapper.vm.layout.totalHeight);
    expect(wrapper.findAll('.photo-card').length).toBeLessThan(100);
    expect(wrapper.vm.windowState.endRow).toBe(wrapper.vm.layout.rows.length);
    expect(wrapper.vm.windowState.bottomSpacer).toBe(0);
    cleanup();
  });

  it('keeps its scroll position when the tab is switched away and back', async () => {
    const images = Array.from({ length: 3000 }, (_, index) => makeImage(index + 1));
    api.SearchImagePage.mockResolvedValue(makePage(images, null));

    const { wrapper, host, scrollTo, cleanup } = await mountInScrollOwner();

    const target = Math.floor(wrapper.vm.layout.totalHeight / 2);
    await scrollTo(target);
    const startRow = wrapper.vm.windowState.startRow;
    expect(startRow).toBeGreaterThan(0);

    // 切到别的标签页：本页只隐藏不卸载，共用的 .main-view 被别的页面归零。
    await wrapper.setProps({ pageActive: false });
    host.scrollTop = 0;
    host.dispatchEvent(new Event('scroll'));
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.windowState.startRow).toBe(startRow);

    // 切回来：位置和渲染窗口都回到原处，不用重新往下滑。
    await wrapper.setProps({ pageActive: true });
    await flushPromises();
    expect(host.scrollTop).toBe(target);
    expect(wrapper.vm.windowState.startRow).toBe(startRow);
    // 切回来只探一次首页判断要不要提示，已加载的列表原封不动（不会被重新拉一遍）。
    expect(wrapper.vm.images).toHaveLength(3000);
    expect(wrapper.find('[data-test="photo-library-refresh"]').exists()).toBe(false);
    cleanup();
  });

  it('offers a refresh bar when the library changed while the tab was away, without moving the scroll', async () => {
    const images = Array.from({ length: 200 }, (_, index) => makeImage(index + 1));
    api.SearchImagePage.mockResolvedValue(makePage(images, null));

    const { wrapper, host, scrollTo, cleanup } = await mountInScrollOwner();
    const target = Math.floor(wrapper.vm.layout.totalHeight / 2);
    await scrollTo(target);

    // 切走期间扫描到一张新图，回来时首页第一条变了。
    await wrapper.setProps({ pageActive: false });
    api.SearchImagePage.mockResolvedValue(makePage([makeImage(999), ...images], null));
    await wrapper.setProps({ pageActive: true });
    await flushPromises();

    // 只提示，不自动刷新：位置还在原处，列表也还是原来那批。
    expect(wrapper.get('[data-test="photo-library-refresh"]').exists()).toBe(true);
    expect(host.scrollTop).toBe(target);
    expect(wrapper.vm.images[0].id).toBe(1);

    // 点刷新才真正重新加载，并回到顶部。
    await wrapper.get('[data-test="photo-library-refresh-apply"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.images[0].id).toBe(999);
    expect(host.scrollTop).toBe(0);
    expect(wrapper.find('[data-test="photo-library-refresh"]').exists()).toBe(false);
    cleanup();
  });

  it('stays quiet when nothing changed while the tab was away', async () => {
    const images = Array.from({ length: 200 }, (_, index) => makeImage(index + 1));
    api.SearchImagePage.mockResolvedValue(makePage(images, null));

    const { wrapper, scrollTo, cleanup } = await mountInScrollOwner();
    await scrollTo(400);

    await wrapper.setProps({ pageActive: false });
    await wrapper.setProps({ pageActive: true });
    await flushPromises();

    expect(wrapper.find('[data-test="photo-library-refresh"]').exists()).toBe(false);
    cleanup();
  });

  // 回归：清理审阅页整页接管主区域，但滚的是同一个 .main-view。网格已不在 DOM 里，
  // 再按那个 scrollTop 算窗口会把 maybeLoadMore 一路触发到翻完整个库。
  it('does not paginate the grid while the cleanup review page is showing', async () => {
    const cursor = { sort_mode: 'recent', created_at: '2026-08-07T09:00:00Z', size: 0, rating_is_null: false, id: 60 };
    api.SearchImagePage.mockResolvedValue(
      makePage(Array.from({ length: 60 }, (_, index) => makeImage(index + 1)), cursor)
    );

    const { wrapper, host, cleanup } = await mountInScrollOwner({ fallbackContentHeight: 200000 });
    expect(api.SearchImagePage).toHaveBeenCalledTimes(1);

    await openPhotoManageItem(wrapper, 'cleanup');
    await flushPromises();
    expect(wrapper.find('[data-test="photo-cleanup-page"]').exists()).toBe(true);

    api.SearchImagePage.mockClear();
    host.scrollTop = 100000;
    host.dispatchEvent(new Event('scroll'));
    await flushPromises();

    expect(api.SearchImagePage).not.toHaveBeenCalled();
    expect(wrapper.vm.images).toHaveLength(60);
    cleanup();
  });

  // 回归：从网格顶部进入审阅页时保存的位置是 0，返回时必须真的回到 0，
  // 而不是停在审阅页滚出去的偏移上。
  it('returns the grid to the top when the review page was opened from the top', async () => {
    api.SearchImagePage.mockResolvedValue(
      makePage(Array.from({ length: 600 }, (_, index) => makeImage(index + 1)), null)
    );
    const { wrapper, host, cleanup } = await mountInScrollOwner({ fallbackContentHeight: 200000 });
    expect(host.scrollTop).toBe(0);

    await openPhotoManageItem(wrapper, 'cleanup');
    await flushPromises();
    // 审阅页里滚了很远。
    host.scrollTop = 100000;
    host.dispatchEvent(new Event('scroll'));
    await flushPromises();

    await wrapper.get('[data-test="cleanup-back"]').trigger('click');
    await flushPromises();

    expect(host.scrollTop).toBe(0);
    cleanup();
  });

  it('asks for the next page from the scroll position instead of a sentinel element', async () => {
    const cursor = { sort_mode: 'recent', created_at: '2026-08-07T09:00:00Z', size: 0, rating_is_null: false, id: 60 };
    const first = Array.from({ length: 60 }, (_, index) => makeImage(index + 1));
    api.SearchImagePage
      .mockResolvedValueOnce(makePage(first, cursor))
      .mockResolvedValueOnce(makePage([makeImage(61)], null));

    const { wrapper, scrollTo, cleanup } = await mountInScrollOwner();
    expect(api.SearchImagePage).toHaveBeenCalledTimes(1);

    await scrollTo(wrapper.vm.layout.totalHeight);
    await flushPromises();

    expect(api.SearchImagePage).toHaveBeenCalledTimes(2);
    expect(api.SearchImagePage.mock.calls[1][0].cursor).toEqual(cursor);
    expect(wrapper.vm.images).toHaveLength(61);
    cleanup();
  });

  // 回归：分页是 this.images.push 原地改数组，不会替换数组引用。侦听 images 本身在 Vue 3 下
  // 不触发，窗口会永远停在第一页的高度，新照片渲染不出来。这里刻意不手动调 syncWindow。
  it('refreshes the window and renders the appended page after an in-place push', async () => {
    const cursor = { sort_mode: 'recent', created_at: '2026-08-07T09:00:00Z', size: 0, rating_is_null: false, id: 300 };
    api.SearchImagePage
      .mockResolvedValueOnce(makePage(Array.from({ length: 300 }, (_, index) => makeImage(index + 1)), cursor))
      .mockResolvedValueOnce(makePage(Array.from({ length: 60 }, (_, index) => makeImage(index + 301)), null));

    const { wrapper, scrollTo, cleanup } = await mountInScrollOwner();
    const heightBefore = wrapper.vm.windowState.totalHeight;
    expect(heightBefore).toBe(wrapper.vm.layout.totalHeight);
    expect(api.SearchImagePage).toHaveBeenCalledTimes(1);

    await wrapper.get('[data-test="photo-load-more"]').trigger('click');
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(wrapper.vm.images).toHaveLength(360);
    expect(wrapper.vm.windowState.totalHeight).toBeGreaterThan(heightBefore);
    expect(wrapper.vm.windowState.totalHeight).toBe(wrapper.vm.layout.totalHeight);

    await scrollTo(wrapper.vm.layout.totalHeight);
    expect(wrapper.text()).toContain('photo-360.jpg');
    expect(wrapper.findAll('.photo-card').length).toBeLessThan(100);
    cleanup();
  });

  it('re-lays out on a width change and keeps the anchored photo in view', async () => {
    const images = Array.from({ length: 600 }, (_, index) => makeImage(index + 1));
    api.SearchImagePage.mockResolvedValue(makePage(images, null));

    const { wrapper, host, scrollTo, setGridWidth, cleanup } = await mountInScrollOwner();
    expect(wrapper.vm.columns).toBe(6);

    // 滚到 80%：收窄到 3 列后行数翻倍，锚点的新位置会超过旧布局的最大 scrollTop，
    // 于是"渲染前写 scrollTop"会被钳制、落不到锚点上——这正是本用例要区分的。
    const before = wrapper.vm.layout.totalHeight;
    await scrollTo(Math.floor(before * 0.8));
    const anchorIndex = wrapper.vm.layout.rows[wrapper.vm.windowState.startRow].startIndex;
    expect(anchorIndex).toBeGreaterThan(0);

    // 收窄到 3 列：ResizeObserver 在 jsdom 里不存在，直接调用它的回调。
    setGridWidth(600);
    wrapper.vm.handleGridResize();
    await wrapper.vm.$nextTick();
    await wrapper.vm.$nextTick();

    expect(wrapper.vm.columns).toBe(3);
    // 锚点照片所在的行必须精确回到视口顶，而不只是"没跳回开头"。宿主的 scrollTop 会按
    // scrollHeight 钳制，所以只有等新布局渲染完再写才落得准。
    const anchorRow = wrapper.vm.layout.rows.find(row => row.startIndex <= anchorIndex && anchorIndex < row.endIndex);
    expect(anchorRow).toBeTruthy();
    expect(anchorRow.top).toBeGreaterThan(before - 800);
    expect(host.scrollTop).toBe(anchorRow.top);
    expect(wrapper.vm.windowState.startRow).toBe(Math.max(0, wrapper.vm.layout.rows.indexOf(anchorRow) - 3));
    expect(wrapper.findAll('.photo-card').length).toBeLessThan(100);
    cleanup();
  });

  // N2 回归：加宽列数会让总高变短，锚点回写前的那次同步若还做预取判断，就会用旧的大 scrollTop
  // 误判成"到底了"而多拉一页。
  it('does not prefetch an extra page when widening the grid while scrolled deep', async () => {
    const cursor = { sort_mode: 'recent', created_at: '2026-08-07T09:00:00Z', size: 0, rating_is_null: false, id: 600 };
    api.SearchImagePage.mockResolvedValue(makePage(Array.from({ length: 600 }, (_, index) => makeImage(index + 1)), cursor));

    const { wrapper, scrollTo, setGridWidth, cleanup } = await mountInScrollOwner();
    expect(api.SearchImagePage).toHaveBeenCalledTimes(1);

    await scrollTo(Math.floor(wrapper.vm.layout.totalHeight / 2));
    expect(api.SearchImagePage).toHaveBeenCalledTimes(1);

    setGridWidth(2400);
    wrapper.vm.handleGridResize();
    await wrapper.vm.$nextTick();
    await wrapper.vm.$nextTick();

    expect(wrapper.vm.columns).toBe(12);
    expect(api.SearchImagePage).toHaveBeenCalledTimes(1);
    cleanup();
  });

  // T1 回归：不手动 syncWindow，冷启动首屏必须自己量出列数与行高并渲染出窗口。
  it('measures and renders the first paint without any manual window sync', async () => {
    api.SearchImagePage.mockResolvedValue(makePage(Array.from({ length: 500 }, (_, index) => makeImage(index + 1)), null));

    const { wrapper, cleanup } = await mountInScrollOwner();

    expect(wrapper.vm.columns).toBe(6);
    expect(wrapper.vm.mediaHeight).toBe(188);
    expect(wrapper.vm.windowState.totalHeight).toBe(wrapper.vm.layout.totalHeight);
    expect(wrapper.vm.windowState.endRow).toBeGreaterThan(0);
    const cards = wrapper.findAll('.photo-card');
    expect(cards.length).toBeGreaterThan(0);
    expect(cards.length).toBeLessThan(100);
    cleanup();
  });
});

describe('PhotoLibraryPage 工具栏收纳', () => {
  it('四个库维护动作收进「管理」菜单，一个都不少', async () => {
    const wrapper = await mountPage();
    const ids = wrapper.vm.photoManageItems.filter(item => item.id).map(item => item.id);
    expect(ids).toEqual(['scan', 'cleanup', 'ai-tags', 'trash']);
    wrapper.unmount();
  });

  it('筛选徽标只数收进浮层的低频条件；排序与时间线留在第一行', async () => {
    const wrapper = await mountPage();
    expect(wrapper.vm.activePhotoFilterCount).toBe(0);
    // 排序和时间线是高频视图切换，留在常驻行，不进徽标。
    expect(wrapper.find('[data-test="photo-sort"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="photo-timeline-toggle"]').exists()).toBe(true);

    wrapper.vm.filters.favoriteOnly = true;
    wrapper.vm.filters.minRating = '6';
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.activePhotoFilterCount).toBe(2);
    wrapper.unmount();
  });

  it('选中态把选择工具条变成批量栏', async () => {
    // 工具条只在有已加载图片时渲染。
    const wrapper = await mountPage();
    wrapper.vm.images = [{ id: 1, name: 'a.jpg', tags: [] }];
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-test="photo-selection-tools"]').exists()).toBe(true);
    expect(wrapper.find('.photo-selection-tools--active').exists()).toBe(false);
    wrapper.vm.selectedImageIDs = [1];
    await wrapper.vm.$nextTick();
    expect(wrapper.find('.photo-selection-tools--active').exists()).toBe(true);
    wrapper.unmount();
  });
});
