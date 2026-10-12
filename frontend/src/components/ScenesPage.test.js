import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import ScenesPage from './ScenesPage.vue';
import { feedbackState, resetFeedback, resolveConfirm } from '../utils/feedback.js';

const COVERAGE = { total_videos: 3, subtitle_indexed: 2, subtitle_unindexed: 1, visual_indexed: 1, visual_model_id: 'cn-clip-vit-b16-q8@1', visual_provider: 'local' };

function deferred() {
  let resolve;
  const promise = new Promise(res => { resolve = res; });
  return { promise, resolve };
}

let handlers;
const wrappers = [];
function mountPage(props = {}) {
  const wrapper = mount(ScenesPage, { props });
  wrappers.push(wrapper);
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  handlers = {};
  window.runtime = { EventsOn: (event, handler) => { handlers[event] = handler; return () => { delete handlers[event]; }; } };
  api.GetSceneCoverage.mockResolvedValue(COVERAGE);
  api.GetSceneRuntimeStatus.mockResolvedValue({ state: 'available', model_id: 'cn-clip-vit-b16-q8@1' });
  api.GetSceneIndexStatus.mockResolvedValue({ running: false, failures: [] });
  api.SearchScenes.mockResolvedValue({ hits: [], notices: [], coverage: COVERAGE });
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
});

async function typeAndSearch(wrapper, text) {
  await wrapper.get('[data-test="scenes-query"]').setValue(text);
  await wrapper.get('[data-test="scenes-search-form"]').trigger('submit');
  await flushPromises();
}

describe('场景检索页（D-MW-SCENES）', () => {
  it('覆盖率条说清范围与未索引的部分，检索前显示引导而不是"没有命中"', async () => {
    const wrapper = mountPage();
    await flushPromises();
    expect(api.GetSceneCoverage).toHaveBeenCalledWith({});
    const coverage = wrapper.get('[data-test="scenes-coverage"]').text();
    expect(coverage).toContain('范围内 3 部视频');
    expect(coverage).toContain('另有 1 部只有其他格式或内嵌字幕');
    expect(coverage).toContain('画面已索引 1 部');
    expect(wrapper.find('[data-test="scenes-idle"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="scenes-empty"]').exists()).toBe(false);
  });

  it('按视频分组展示命中、缩略帧与来源；点击从区间起点提前 2 秒打开', async () => {
    api.SearchScenes.mockResolvedValue({
      hits: [
        { video_id: 7, title: '长片', start_ms: 7_080_000, end_ms: 7_083_500, source: 'dialogue', score: 1 / 61, text: '雨夜里的告白', context_before: '前一句', context_after: '后一句' },
        { video_id: 9, title: '海边', start_ms: 1000, end_ms: 15000, source: 'visual', score: 1 / 61, text: '' },
        { video_id: 7, title: '长片', start_ms: 30_000, end_ms: 35_000, source: 'visual', score: 1 / 62, text: '' }
      ],
      notices: [],
      coverage: COVERAGE
    });
    const wrapper = mountPage();
    await flushPromises();
    await typeAndSearch(wrapper, '告白');
    expect(api.SearchScenes).toHaveBeenCalledWith({ query: '告白', mode: 'all', filter: null, limit: 50 });
    const groups = wrapper.findAll('[data-test^="scenes-group-"]');
    expect(groups.map(group => group.attributes('data-test'))).toEqual(['scenes-group-7', 'scenes-group-9']);
    const first = wrapper.get('[data-test="scenes-hit-7-0"]');
    expect(first.text()).toContain('对白');
    expect(first.text()).toContain('1:58:00 – 1:58:03');
    expect(first.text()).toContain('前一句');
    expect(first.get('img').attributes('src')).toBe('/preview/frame/7?ms=7080000&w=320');
    expect(wrapper.get('[data-test="scenes-hit-9-0"]').text()).toContain('画面');
    await first.trigger('click');
    expect(wrapper.emitted('open-video-at')[0][0]).toEqual({ videoID: 7, startMs: 7_078_000 });
    await wrapper.get('[data-test="scenes-hit-9-0"]').trigger('click');
    expect(wrapper.emitted('open-video-at')[1][0]).toEqual({ videoID: 9, startMs: 0 });
  });

  it('切换模式会按新模式重新检索', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await typeAndSearch(wrapper, '海边');
    await wrapper.get('[data-test="scenes-mode-visual"]').trigger('click');
    await flushPromises();
    expect(api.SearchScenes).toHaveBeenLastCalledWith({ query: '海边', mode: 'visual', filter: null, limit: 50 });
    expect(wrapper.get('[data-test="scenes-mode-visual"]').attributes('aria-checked')).toBe('true');
  });

  it('请求代次：晚到的旧结果直接丢弃', async () => {
    const slow = deferred();
    const fast = deferred();
    api.SearchScenes.mockReturnValueOnce(slow.promise).mockReturnValueOnce(fast.promise);
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.get('[data-test="scenes-query"]').setValue('旧的');
    await wrapper.get('[data-test="scenes-search-form"]').trigger('submit');
    await wrapper.get('[data-test="scenes-query"]').setValue('新的');
    await wrapper.get('[data-test="scenes-search-form"]').trigger('submit');
    fast.resolve({ hits: [{ video_id: 2, title: '新结果', start_ms: 0, end_ms: 1000, source: 'dialogue', text: '新的' }], notices: [], coverage: COVERAGE });
    await flushPromises();
    slow.resolve({ hits: [{ video_id: 1, title: '旧结果', start_ms: 0, end_ms: 1000, source: 'dialogue', text: '旧的' }], notices: [], coverage: COVERAGE });
    await flushPromises();
    expect(wrapper.find('[data-test="scenes-group-1"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="scenes-group-2"]').exists()).toBe(true);
  });

  it('M-5 画面部分没有执行时不说"没有找到"，按提示说明是哪部分没执行', async () => {
    for (const notice of ['scene_runtime_unavailable', 'scene_visual_failed', 'scene_external_not_configured']) {
      api.SearchScenes.mockResolvedValue({ hits: [], notices: [notice], coverage: COVERAGE });
      const wrapper = mountPage();
      await flushPromises();
      await wrapper.get('[data-test="scenes-mode-visual"]').trigger('click');
      await typeAndSearch(wrapper, '猫');
      const empty = wrapper.get('[data-test="scenes-empty"]').text();
      expect(empty, notice).not.toContain('没有找到');
      expect(empty, notice).toContain('画面检索没有执行');
      await wrapper.get('[data-test="scenes-mode-all"]').trigger('click');
      await flushPromises();
      const mixed = wrapper.get('[data-test="scenes-empty"]').text();
      expect(mixed, notice).toContain('对白里没有找到');
      expect(mixed, notice).toContain('画面检索没有执行');
      expect(mixed, notice).not.toContain('在已索引的内容里没有找到');
    }
  });

  it('没有命中时按提示说明哪部分没执行，并指出未索引的内容', async () => {
    api.SearchScenes.mockResolvedValue({ hits: [], notices: ['visual_index_empty'], coverage: COVERAGE });
    const wrapper = mountPage();
    await flushPromises();
    await typeAndSearch(wrapper, '猫');
    expect(wrapper.get('[data-test="scenes-notice-visual_index_empty"]').text()).toContain('画面索引');
    const empty = wrapper.get('[data-test="scenes-empty"]').text();
    expect(empty).toContain('在已索引的内容里没有找到');
    expect(empty).toContain('还有 2 部视频没有画面索引');
  });

  it('片库带来的筛选作为检索范围，可改为全部视频', async () => {
    const filter = { smart_view: 'unwatched', tag_ids: [3] };
    const wrapper = mountPage({ scopeRequest: { filter, token: 1 } });
    await flushPromises();
    expect(api.GetSceneCoverage).toHaveBeenLastCalledWith(filter);
    expect(wrapper.get('[data-test="scenes-scope"]').text()).toContain('片库当前筛选');
    await typeAndSearch(wrapper, '雨');
    expect(api.SearchScenes).toHaveBeenLastCalledWith({ query: '雨', mode: 'all', filter, limit: 50 });
    await wrapper.get('[data-test="scenes-clear-scope"]').trigger('click');
    await flushPromises();
    expect(api.GetSceneCoverage).toHaveBeenLastCalledWith({});
    expect(wrapper.find('[data-test="scenes-results"]').exists()).toBe(false);
  });

  it('运行时未就绪时给出「准备模型」，建索引入口置灰；索引进度来自事件', async () => {
    api.GetSceneRuntimeStatus.mockResolvedValue({ state: 'missing_model', reason: '未下载' });
    api.PrepareSceneRuntime.mockResolvedValue({ state: 'missing_model', preparing: true, message: '正在下载', total_bytes: 100, downloaded_bytes: 25 });
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.get('[data-test="scenes-start-index"]').attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="scenes-prepare"]').trigger('click');
    await flushPromises();
    expect(api.PrepareSceneRuntime).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="scenes-runtime-state"]').text()).toContain('25%');
    handlers['scene-index-state']({ running: true, processed: 1, total: 4, current_video_name: 'a.mp4', failures: [] });
    await flushPromises();
    expect(wrapper.get('[data-test="scenes-index-status"]').text()).toContain('1/4');
    expect(wrapper.find('[data-test="scenes-cancel-index"]').exists()).toBe(true);
    api.GetSceneCoverage.mockClear();
    handlers['scene-index-state']({ running: false, completed: true, succeeded: 4, skipped: 0, failed: 0, failures: [] });
    await flushPromises();
    expect(api.GetSceneCoverage).toHaveBeenCalledTimes(1);
  });

  it('建索引与清理旧索引走对应绑定；清理先确认', async () => {
    api.StartSceneIndex.mockResolvedValue({ running: true, preparing: true, failures: [] });
    api.ClearStaleSceneIndex.mockResolvedValue({ deleted_segments: 5, deleted_states: 1 });
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.get('[data-test="scenes-start-index"]').trigger('click');
    await flushPromises();
    expect(api.StartSceneIndex).toHaveBeenCalledWith({ video_ids: [], filter: null, provider: '' });
    await wrapper.get('[data-test="scenes-cancel-index"]').trigger('click');
    await flushPromises();
    expect(api.CancelSceneIndex).toHaveBeenCalledTimes(1);
    api.GetSceneIndexStatus.mockResolvedValue({ running: false, failures: [] });
    wrapper.vm.indexStatus = { running: false };
    await wrapper.vm.$nextTick();
    const clearing = wrapper.get('[data-test="scenes-clear-stale"]').trigger('click');
    await flushPromises();
    expect(feedbackState.confirm?.title).toBe('清理旧索引');
    resolveConfirm(true);
    await clearing;
    await flushPromises();
    expect(api.ClearStaleSceneIndex).toHaveBeenCalledTimes(1);
  });

  it('M-3 取消尚未收尾时启动建索引，给出明确提示', async () => {
    api.StartSceneIndex.mockRejectedValue(new Error('scene_index_cancelling'));
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.get('[data-test="scenes-start-index"]').trigger('click');
    await flushPromises();
    expect(feedbackState.toasts.map(toast => toast.message).join('\n')).toContain('正在取消');
  });

  it('后端错误代码显示成中文', async () => {
    api.SearchScenes.mockRejectedValue(new Error('scene_query_invalid'));
    const wrapper = mountPage();
    await flushPromises();
    await typeAndSearch(wrapper, '猫');
    expect(wrapper.get('[data-test="scenes-error"]').text()).toContain('1–200');
  });
});

// ===== 以图搜片（2026-10-12）=====
describe('以图搜片与相似画面', () => {
  const HIT = { video_id: 7, title: '片子', start_ms: 125000, end_ms: 130000, source: 'visual', score: 0.9, text: '' };
  const pngFile = (name = 'shot.png', type = 'image/png', size = 12) => new File([new Uint8Array(size).fill(1)], name, { type });

  beforeEach(() => {
    api.SearchScenesByImage.mockResolvedValue({ hits: [HIT], notices: [], coverage: COVERAGE });
  });

  it('选一张图片：按图检索全库画面，显示查询图与命中；文字检索会清掉它', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.vm.useImageFile(pngFile());
    await flushPromises();
    const request = api.SearchScenesByImage.mock.calls[0][0];
    expect(request).toEqual({ image_base64: expect.any(String), video_id: 0, at_ms: 0, filter: null, limit: 50 });
    expect(request.image_base64).toBe(btoa(String.fromCharCode(...new Uint8Array(12).fill(1))));
    expect(wrapper.get('[data-test="scenes-image-query"]').text()).toContain('按这张图找画面：shot.png');
    expect(wrapper.find('[data-test="scenes-hit-7-0"]').exists()).toBe(true);

    await typeAndSearch(wrapper, '雨夜');
    expect(api.SearchScenes).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="scenes-image-query"]').exists()).toBe(false);
  });

  it('格式不对或超过 20 MB 的图片在本地就挡掉，说清支持什么', async () => {
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.vm.useImageFile(pngFile('a.heic', 'image/heic'));
    await flushPromises();
    expect(api.SearchScenesByImage).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="scenes-error"]').text()).toContain('支持 JPEG、PNG、WebP、GIF、BMP');
    const big = pngFile();
    Object.defineProperty(big, 'size', { value: 20 * 1024 * 1024 + 1 });
    await wrapper.vm.useImageFile(big);
    await flushPromises();
    expect(api.SearchScenesByImage).not.toHaveBeenCalled();
  });

  it('拖进图片或在本页粘贴图片都会按图检索；页面不可见时不接管粘贴', async () => {
    const wrapper = mountPage();
    await flushPromises();
    // jsdom 的拖放事件带不进 dataTransfer：直接交给页面上绑定的处理函数。
    wrapper.vm.onDragOver({ dataTransfer: { types: ['Files'] } });
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="scenes-page"]').classes()).toContain('scenes-page--dropping');
    await wrapper.vm.onDrop({ dataTransfer: { files: [new File(['x'], 'note.txt', { type: 'text/plain' }), pngFile('drop.png')] } });
    await flushPromises();
    expect(wrapper.get('[data-test="scenes-page"]').classes()).not.toContain('scenes-page--dropping');
    expect(api.SearchScenesByImage).toHaveBeenCalledTimes(1);

    const paste = new Event('paste', { cancelable: true });
    paste.clipboardData = { files: [pngFile('paste.png')] };
    window.dispatchEvent(paste);
    await vi.waitFor(() => expect(api.SearchScenesByImage).toHaveBeenCalledTimes(2));
    expect(paste.defaultPrevented).toBe(true);

    await wrapper.setProps({ pageActive: false });
    const hidden = new Event('paste', { cancelable: true });
    hidden.clipboardData = { files: [pngFile('hidden.png')] };
    window.dispatchEvent(hidden);
    await new Promise(resolve => setTimeout(resolve, 20));
    await flushPromises();
    expect(api.SearchScenesByImage).toHaveBeenCalledTimes(2);
    expect(hidden.defaultPrevented).toBe(false);
    // 粘贴的是文字（没有图片）时不拦。
    await wrapper.setProps({ pageActive: true });
    const text = new Event('paste', { cancelable: true });
    text.clipboardData = { files: [] };
    window.dispatchEvent(text);
    expect(text.defaultPrevented).toBe(false);
  });

  it('命中卡片「相似画面」用那一刻的画面检索，并说明不含这一刻前后 10 秒', async () => {
    api.SearchScenes.mockResolvedValue({ hits: [HIT], notices: [], coverage: COVERAGE });
    const wrapper = mountPage();
    await flushPromises();
    await typeAndSearch(wrapper, '海边');
    await wrapper.get('[data-test="scenes-similar-7-0"]').trigger('click');
    await flushPromises();
    expect(api.SearchScenesByImage).toHaveBeenCalledWith({ image_base64: '', video_id: 7, at_ms: 125000, filter: null, limit: 50 });
    expect(wrapper.get('[data-test="scenes-image-query"]').text()).toContain('与《片子》2:05 相似的画面（不含这一刻前后 10 秒）');
    await wrapper.get('[data-test="scenes-clear-image"]').trigger('click');
    expect(wrapper.find('[data-test="scenes-image-query"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="scenes-idle"]').exists()).toBe(true);
  });

  it('详情抽屉带来的「找相似画面」进来就检索；苹果系统能力缺失时给出准确提示', async () => {
    api.SearchScenesByImage.mockResolvedValue({ hits: [], notices: ['apple_image_unavailable'], coverage: COVERAGE });
    const wrapper = mountPage({ scopeRequest: { filter: null, similar: { videoID: 9, atMS: 61000.4, title: '另一部' }, token: 1 } });
    await flushPromises();
    expect(api.SearchScenesByImage).toHaveBeenCalledWith({ image_base64: '', video_id: 9, at_ms: 61000, filter: null, limit: 50 });
    expect(wrapper.get('[data-test="scenes-notice-apple_image_unavailable"]').text()).toContain('苹果系统图像能力不可用');
    expect(wrapper.get('[data-test="scenes-empty"]').text()).toContain('画面检索没有执行');
  });
});

// ===== 台词卡（2026-10-12）=====
describe('台词卡入口', () => {
  const DIALOGUE = { video_id: 7, title: '长片', start_ms: 7_080_000, end_ms: 7_083_500, source: 'dialogue', score: 1 / 61, text: '雨夜里的告白' };
  // 外部描述的画面命中也带文字（画面描述），但那不是台词。
  const VISUAL = { video_id: 7, title: '长片', start_ms: 30_000, end_ms: 35_000, source: 'visual', score: 1 / 62, text: '海边的日落' };
  const EMPTY_DIALOGUE = { video_id: 9, title: '', start_ms: 1000, end_ms: 2000, source: 'dialogue', score: 1 / 63, text: '' };

  function mountWithStubbedDialog() {
    const stub = { name: 'QuoteCardDialog', props: ['videoID', 'atMS', 'quote', 'title'], emits: ['close'], template: '<div data-test="quote-card-stub"></div>' };
    const wrapper = mount(ScenesPage, { global: { stubs: { QuoteCardDialog: stub } } });
    wrappers.push(wrapper);
    return wrapper;
  }

  it('只有带台词的对白命中有「台词卡」；点开带上这句台词、片名与字幕区间正中那一刻', async () => {
    api.SearchScenes.mockResolvedValue({ hits: [DIALOGUE, VISUAL, EMPTY_DIALOGUE], notices: [], coverage: COVERAGE });
    const wrapper = mountWithStubbedDialog();
    await flushPromises();
    await typeAndSearch(wrapper, '告白');
    expect(wrapper.find('[data-test="scenes-quote-7-0"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="scenes-quote-7-1"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="scenes-quote-9-0"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="scenes-similar-7-1"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="quote-card-stub"]').exists()).toBe(false);

    await wrapper.get('[data-test="scenes-quote-7-0"]').trigger('click');
    const dialog = wrapper.getComponent({ name: 'QuoteCardDialog' });
    expect(dialog.props()).toEqual({ videoID: 7, atMS: 7_081_750, quote: '雨夜里的告白', title: '长片' });
    expect(wrapper.emitted('open-video-at')).toBeUndefined();

    dialog.vm.$emit('close');
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-test="quote-card-stub"]').exists()).toBe(false);
  });

  it('台词卡开着时粘贴图片不触发以图搜片', async () => {
    api.SearchScenes.mockResolvedValue({ hits: [DIALOGUE], notices: [], coverage: COVERAGE });
    const wrapper = mountWithStubbedDialog();
    await flushPromises();
    await typeAndSearch(wrapper, '告白');
    await wrapper.get('[data-test="scenes-quote-7-0"]').trigger('click');
    const paste = new Event('paste', { cancelable: true });
    paste.clipboardData = { files: [new File([new Uint8Array(4)], 'shot.png', { type: 'image/png' })] };
    window.dispatchEvent(paste);
    await flushPromises();
    expect(paste.defaultPrevented).toBe(false);
    expect(api.SearchScenesByImage).not.toHaveBeenCalled();
  });
});

it('以图模式独立建立苹果索引，不要求准备中文模型', async () => {
  api.GetSceneRuntimeStatus.mockResolvedValue({ state: 'missing_model' });
  api.SearchScenesByImage.mockResolvedValue({ hits: [], notices: [], coverage: { ...COVERAGE, image_indexed: 0, image_available: true, visual_provider: 'apple' } });
  const wrapper = mountPage({ scopeRequest: { filter: null, similar: { videoID: 9, atMS: 0 }, token: 1 } });
  await flushPromises();
  expect(wrapper.find('[data-test="scenes-prepare"]').exists()).toBe(false);
  expect(wrapper.get('[data-test="scenes-start-index"]').attributes('disabled')).toBeUndefined();
  await wrapper.get('[data-test="scenes-start-index"]').trigger('click');
  await flushPromises();
  expect(api.StartSceneIndex).toHaveBeenCalledWith({ video_ids: [], filter: null, provider: 'apple' });
});
