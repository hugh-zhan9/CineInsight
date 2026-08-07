import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'SearchImagePage', 'GetImageDetail', 'SetImageFavorite', 'SetImageRating',
  'AddTagToImage', 'RemoveTagFromImage', 'GetAllImageDirectories', 'SyncImageDirectories',
  'DeleteImage', 'ListImageTrashEntries', 'RestoreImageTrashEntry',
  'StartImageCleanupAnalysis', 'GetImageCleanupStatus', 'DismissImageNearDuplicateGroup', 'BatchDeleteImages',
  'GetImageSemanticIndexStatus', 'SearchImagesSemantic', 'RegenerateImageAIDescription'
].map(name => [name, vi.fn()])));

vi.mock('../../wailsjs/go/main/App', () => api);

import PhotoLibraryPage from './PhotoLibraryPage.vue';

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

function completedCleanupStatus(analysis) {
  return { running: false, completed: true, error: '', progress: { stage: 'done' }, analysis };
}

async function mountPage({ settings = baseSettings(), tags = [] } = {}) {
  const wrapper = mount(PhotoLibraryPage, { props: { settings, tags } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  api.GetAllImageDirectories.mockResolvedValue([{ id: 1, alias: '相册', path: '/photos' }]);
  api.SearchImagePage.mockResolvedValue(makePage([]));
  api.SyncImageDirectories.mockResolvedValue({ added: 0, relocated: 0, removed: 0, skipped: 0, errors: [] });
  api.GetImageDetail.mockImplementation(id => Promise.resolve({ image: makeImage(Number(id)), ai_description: '' }));
  api.SetImageFavorite.mockImplementation((id, favorite) => Promise.resolve({ id, is_favorite: favorite }));
  api.SetImageRating.mockImplementation((id, rating) => Promise.resolve({ id, personal_rating: rating }));
  api.DeleteImage.mockResolvedValue();
  api.ListImageTrashEntries.mockResolvedValue([]);
  api.GetImageCleanupStatus.mockResolvedValue(idleCleanupStatus());
  api.StartImageCleanupAnalysis.mockResolvedValue(idleCleanupStatus());
  api.DismissImageNearDuplicateGroup.mockResolvedValue();
  api.BatchDeleteImages.mockResolvedValue({ requested: 0, succeeded: 0, failed: 0, errors: [] });
  api.GetImageSemanticIndexStatus.mockResolvedValue({ available: true, running: false, completed: true, unavailable: '' });
  api.SearchImagesSemantic.mockResolvedValue({ hits: [], coverage: { indexed: 0, total: 0 }, has_more: false });
  api.RegenerateImageAIDescription.mockResolvedValue({ image_id: 1, description: '', generated_at: null });
});

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

    await wrapper.get('[data-test="photo-favorite-only"]').setValue(true);
    await flushPromises();

    expect(api.SearchImagePage).toHaveBeenCalledTimes(2);
    const secondRequest = api.SearchImagePage.mock.calls[1][0];
    expect(secondRequest.cursor).toBeUndefined();
    expect(secondRequest.filter.favorite_only).toBe(true);
    expect(wrapper.findAll('.photo-card')).toHaveLength(1);
    expect(wrapper.text()).toContain('photo-9.jpg');
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
    expect(wrapper.get('[data-test="photo-ai-description-empty"]').text()).toBe('尚未生成');

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

  it('shows the AI description from GetImageDetail when present', async () => {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(3)]));
    api.GetImageDetail.mockResolvedValue({ image: makeImage(3), ai_description: '一片海边的落日。' });
    const wrapper = await mountPage();

    await wrapper.get('.photo-card__media').trigger('click');
    await flushPromises();

    expect(wrapper.get('[data-test="photo-ai-description"]').text()).toBe('一片海边的落日。');
  });
});

describe('PhotoLibraryPage AI description regenerate', () => {
  async function openViewer(detail = { image: makeImage(3), ai_description: '' }) {
    api.SearchImagePage.mockResolvedValueOnce(makePage([makeImage(3)]));
    api.GetImageDetail.mockResolvedValue(detail);
    const wrapper = await mountPage();
    await wrapper.get('.photo-card__media').trigger('click');
    await flushPromises();
    return wrapper;
  }

  it('regenerates the description in place and shows the generated time', async () => {
    const wrapper = await openViewer();
    expect(wrapper.find('[data-test="photo-ai-description-empty"]').exists()).toBe(true);

    let resolveCall;
    api.RegenerateImageAIDescription.mockReturnValueOnce(new Promise(resolve => { resolveCall = resolve; }));
    await wrapper.get('[data-test="photo-ai-regenerate"]').trigger('click');
    await wrapper.vm.$nextTick();

    const button = wrapper.get('[data-test="photo-ai-regenerate"]');
    expect(button.attributes('disabled')).toBeDefined();
    expect(button.text()).toBe('生成中...');

    resolveCall({ image_id: 3, description: '沙滩上的两个人在看日落。', generated_at: '2026-08-07T12:00:00Z' });
    await flushPromises();

    expect(api.RegenerateImageAIDescription).toHaveBeenCalledWith(3);
    expect(wrapper.get('[data-test="photo-ai-description"]').text()).toBe('沙滩上的两个人在看日落。');
    expect(wrapper.get('[data-test="photo-ai-generated-at"]').text()).toContain('生成时间');
    expect(wrapper.find('[data-test="photo-ai-description-empty"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="photo-ai-regenerate"]').attributes('disabled')).toBeUndefined();
  });

  it('keeps the placeholder and reports an unavailable AI configuration', async () => {
    const wrapper = await openViewer();
    api.RegenerateImageAIDescription.mockRejectedValueOnce('AI 配置不可用: BaseURL 或 Model 为空');

    await wrapper.get('[data-test="photo-ai-regenerate"]').trigger('click');
    await flushPromises();

    const error = wrapper.get('[data-test="photo-ai-regenerate-error"]');
    expect(error.text()).toContain('AI 配置不可用');
    expect(error.text()).toContain('请先在设置页配置 AI 接口');
    expect(wrapper.find('[data-test="photo-ai-description-empty"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="photo-ai-regenerate"]').attributes('disabled')).toBeUndefined();
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
    const wrapper = await mountPage({ tags: [{ id: 5, name: '风景', color: '#123456' }] });

    await wrapper.get('[data-test="photo-favorite-only"]').setValue(true);
    await flushPromises();
    await wrapper.findAll('.tag-chip')[0].trigger('click');
    await flushPromises();
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
    await wrapper.get('[data-test="photo-cleanup-open"]').trigger('click');
    await flushPromises();
    return wrapper;
  }

  it('opens the panel and polls for status without auto-starting an analysis', async () => {
    api.SearchImagePage.mockResolvedValue(makePage([]));
    const wrapper = await mountPage();

    expect(wrapper.find('[data-test="photo-cleanup-panel"]').exists()).toBe(false);

    await wrapper.get('[data-test="photo-cleanup-open"]').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="photo-cleanup-panel"]').exists()).toBe(true);
    expect(api.GetImageCleanupStatus).toHaveBeenCalledTimes(1);
    expect(api.StartImageCleanupAnalysis).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="cleanup-idle"]').exists()).toBe(true);
  });

  it('renders both group sections with candidates unchecked and shows the stale hash hint', async () => {
    const wrapper = await openCleanup(
      { duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] },
      { staleHashCount: 7 }
    );

    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="cleanup-exact-section"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="cleanup-near-section"]').exists()).toBe(true);

    const toggles = wrapper.findAll('[data-test="cleanup-candidate-toggle"]');
    expect(toggles).toHaveLength(2);
    expect(toggles.every(toggle => toggle.element.checked === false)).toBe(true);

    const thumbs = wrapper.findAll('.photo-cleanup-thumb');
    expect(thumbs[0].attributes('src')).toBe('/preview/image-thumbnail/1');
    expect(wrapper.text()).toContain('4000×3000');

    expect(wrapper.get('[data-test="cleanup-stale-hint"]').text()).toContain('7');
    expect(wrapper.get('[data-test="cleanup-stale-hint"]').text()).toContain('浏览图片可自动刷新缩略图与指纹');
    expect(wrapper.get('[data-test="cleanup-delete-selected"]').attributes('disabled')).toBeDefined();
  });

  it('deletes only the selected candidates into the trash and refreshes the analysis', async () => {
    const wrapper = await openCleanup({ duplicate_groups: [exactGroup()], near_duplicate_groups: [nearGroup()] });

    await wrapper.get('[data-test="cleanup-start"]').trigger('click');
    await flushPromises();

    await wrapper.findAll('[data-test="cleanup-candidate-toggle"]')[1].setValue(true);
    await flushPromises();

    expect(wrapper.get('[data-test="cleanup-delete-selected"]').text()).toContain('(1)');

    api.SearchImagePage.mockClear();
    await wrapper.get('[data-test="cleanup-delete-selected"]').trigger('click');
    await flushPromises();

    expect(api.BatchDeleteImages).toHaveBeenCalledWith([4], true);
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
