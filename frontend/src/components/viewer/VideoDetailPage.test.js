import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  GetCollectionDetail: vi.fn(), GetPersonDetail: vi.fn(), GetPreviewSession: vi.fn(), GetSubtitleSegments: vi.fn(),
  GetVideoDetails: vi.fn(), PlayVideo: vi.fn(), PreviewExternally: vi.fn(), SearchLibraryVideoPage: vi.fn(),
  SetVideoFavorite: vi.fn(), SetVideoWatched: vi.fn(), UpdateVideoRating: vi.fn(), UpdateVideoWatchProgress: vi.fn(),
  // 子组件（书签、待播、真实播放器）用到的绑定。
  ListVideoBookmarks: vi.fn(), SaveVideoBookmark: vi.fn(), DeleteVideoBookmark: vi.fn(), ResolveVideoBookmark: vi.fn(),
  GetPlaybackQueue: vi.fn(), EditPlaybackQueue: vi.fn(), PlayPlaybackQueue: vi.fn(),
  GetPlaybackProxy: vi.fn(), GetPlaybackProxyStatus: vi.fn(), CreatePlaybackProxy: vi.fn(), DeletePlaybackProxy: vi.fn(), RecordViewEvent: vi.fn()
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import VideoDetailPage from './VideoDetailPage.vue';
import BookmarkPanel from '../BookmarkPanel.vue';
import RecapBackdrop from '../recap/RecapBackdrop.vue';
import { closeVersionCompare, versionCompareState } from '../../utils/versionCompare.js';

enableAutoUnmount(afterEach);

// 播放器替身：记下宿主给的参数，暴露宿主会读的状态与方法（真实播放器另有一条集成用例）。
const PlayerStub = {
  name: 'InlineVideoPlayer',
  props: ['videoId', 'session', 'startTimeMs', 'resumePositionSeconds', 'bookmarkRange', 'autoplay', 'durationSeconds'],
  emits: ['watch-progress', 'preview-session-stale', 'preview-externally', 'bookmark-range-end'],
  data: () => ({ proxyBusy: false, proxyPending: false, proxyError: '', proxyNotice: '', proxyCreateLabel: '生成播放代理', position: 0, proxyCalls: 0, playCalls: 0 }),
  methods: {
    currentTime() { return this.position; },
    play() { this.playCalls += 1; return Promise.resolve(); },
    createPlaybackProxy() { this.proxyCalls += 1; }
  },
  template: '<div data-test="player-stub"></div>'
};
const QuoteStub = { name: 'QuoteCardDialog', props: ['videoID', 'atMS', 'quote', 'title'], template: '<div data-test="quote-stub"></div>' };

const baseVideo = (id, extra = {}) => ({
  id, name: `v${id}.mp4`, display_title: `影片${id}`, original_title: 'Original Title', year: 2001, duration: 600, width: 1920, height: 1080,
  path: `/lib/v${id}.mp4`, personal_rating: 7.5, description: '一段简介', tags: [{ id: 3, name: '科幻', color: '#336699' }],
  is_favorite: false, is_watched: false, play_count: 2, last_played_at: '2026-10-01T20:00:00+08:00', watch_position_seconds: 0, ...extra
});
const detailsFor = (id = 5, { video = {}, ...extra } = {}) => ({
  video: baseVideo(id, video), effective_title: `影片${id}`,
  people: [{ person: { id: 31, display_name: '甲' }, avatar_url: '', active_video_count: 3 }],
  collections: [{ collection: { id: 41, name: '三部曲' }, active_video_count: 3 }],
  artwork: { has_poster: true, has_fanart: false },
  ...extra
});
const versionGroup = id => ({
  group_id: 9, member_count: 2,
  members: [{ video_id: id, label: '4K', display_title: `影片${id}`, resolution: '3840x2160' }, { video_id: 6, label: '', name: 'v6.mp4', resolution: '1920x1080' }]
});
const inlineSession = id => ({ video_id: id, mode: 'inline', source_version: 'sv1', inline_source: { locator_value: `/preview/video/${id}`, mime: 'video/mp4' } });

beforeEach(() => {
  vi.resetAllMocks();
  closeVersionCompare();
  api.GetVideoDetails.mockImplementation(async id => detailsFor(id));
  api.GetPersonDetail.mockResolvedValue({ videos: [baseVideo(5), baseVideo(7), baseVideo(8)] });
  api.SearchLibraryVideoPage.mockImplementation(async () => ({ videos: [baseVideo(5)], version_groups: { 5: versionGroup(5) } }));
  api.GetCollectionDetail.mockResolvedValue({ videos: [{ position: 1, video: baseVideo(5) }, { position: 2, video: baseVideo(12) }, { position: 3, video: baseVideo(13) }] });
  api.GetPreviewSession.mockImplementation(async id => inlineSession(id));
  api.ListVideoBookmarks.mockResolvedValue({ items: [], has_more: false });
  api.GetPlaybackProxy.mockResolvedValue(null);
  api.GetPlaybackProxyStatus.mockResolvedValue({ running: false, queued: 0, queued_video_ids: [], current_video_id: 0, results: [] });
  api.RecordViewEvent.mockResolvedValue(true);
});

async function mountPage(props = {}, { realPlayer = false } = {}) {
  const stubs = { QuoteCardDialog: QuoteStub, ...(realPlayer ? {} : { InlineVideoPlayer: PlayerStub }) };
  const wrapper = mount(VideoDetailPage, { props: { videoId: 5, ...props }, global: { stubs } });
  await flushPromises();
  return wrapper;
}
const player = wrapper => wrapper.findComponent({ name: 'InlineVideoPlayer' });
const backdrop = wrapper => wrapper.findAllComponents(RecapBackdrop).find(item => item.attributes('data-test') === 'detail-backdrop');

describe('VideoDetailPage data', () => {
  it('reads the details once and each block from its existing API', async () => {
    const wrapper = await mountPage();
    expect(api.GetVideoDetails).toHaveBeenCalledTimes(1);
    expect(api.GetVideoDetails).toHaveBeenCalledWith(5);
    expect(api.GetPersonDetail).toHaveBeenCalledWith(31, 0, 12);
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith({ filter: { path_prefix: '/lib/v5.mp4', collapse_versions: true }, limit: 1 });
    expect(api.GetCollectionDetail).toHaveBeenCalledWith(41);
    expect(api.ListVideoBookmarks).toHaveBeenCalledWith(expect.objectContaining({ video_id: 5 }));
    expect(api.GetPreviewSession).not.toHaveBeenCalled();

    expect(wrapper.get('[data-test="detail-title"]').text()).toBe('影片5');
    expect(wrapper.get('[data-test="detail-original-title"]').text()).toBe('Original Title');
    expect(['year', 'duration', 'resolution', 'versions', 'rating'].map(key => wrapper.get(`[data-test="detail-fact-${key}"]`).text()))
      .toEqual(['2001 年', '10:00', '1920×1080', '2 个版本', '评分 7.5']);
    expect(wrapper.get('[data-test="detail-tag-3"]').text()).toBe('科幻');
    expect(wrapper.get('[data-test="detail-description"]').text()).toContain('一段简介');
    expect(wrapper.get('[data-test="detail-person-31"]').text()).toContain('甲');
    expect(wrapper.findAll('[data-test="detail-related-item"]').map(item => item.text())).toEqual([
      expect.stringContaining('影片7'), expect.stringContaining('影片8')
    ]);
    expect(wrapper.get('[data-test="detail-related"]').text()).toContain('同为 甲 的作品');
    expect(wrapper.get('[data-test="detail-version-5"]').text()).toContain('当前');
    expect(wrapper.get('[data-test="detail-version-5"]').text()).toContain('4K · 影片5');
    expect(wrapper.find('[data-test="detail-version-compare-6"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="detail-collection-position-41"]').text()).toBe('第 1 部 · 共 3 部');
    expect(wrapper.get('[data-test="detail-collection-next-41"]').text()).toContain('影片12');
    expect(wrapper.get('[data-test="detail-history-plays"]').text()).toContain('2次');
    expect(wrapper.get('[data-test="detail-history-last-played"]').text()).toContain('2026年10月1日');
  });

  it('shows a loading state while the details are pending', async () => {
    api.GetVideoDetails.mockReturnValue(new Promise(() => {}));
    const wrapper = await mountPage();
    expect(wrapper.find('[data-test="detail-loading"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="detail-backdrop"]').exists()).toBe(false);
  });

  it('fails without raw error text, retries, and offers the library', async () => {
    api.GetVideoDetails.mockRejectedValueOnce(new Error('database is in maintenance mode'));
    const wrapper = await mountPage();
    const error = wrapper.get('[data-test="detail-error"]');
    expect(error.text()).toContain('读取视频详情失败：数据库正在恢复备份或切换后端');
    expect(error.text()).not.toContain('maintenance');
    await wrapper.get('[data-test="detail-go-library"]').trigger('click');
    expect(wrapper.emitted('navigate')).toEqual([['videos']]);
    await wrapper.get('[data-test="detail-retry"]').trigger('click');
    await flushPromises();
    expect(api.GetVideoDetails).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[data-test="detail-error"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="detail-title"]').text()).toBe('影片5');
  });

  it('fails each secondary block on its own', async () => {
    api.GetPersonDetail.mockRejectedValue('人物读取失败');
    api.SearchLibraryVideoPage.mockRejectedValue('版本读取失败');
    api.GetCollectionDetail.mockRejectedValue('作品集读取失败');
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="detail-related-error"]').text()).toBe('读取相关推荐失败：人物读取失败');
    expect(wrapper.get('[data-test="detail-versions-error"]').text()).toBe('读取版本失败：版本读取失败');
    expect(wrapper.get('[data-test="detail-collection-41"]').text()).toContain('读取作品集失败：作品集读取失败');
    expect(wrapper.get('[data-test="detail-title"]').text()).toBe('影片5');
    expect(wrapper.find('[data-test="detail-fact-versions"]').exists()).toBe(false);
  });

  it('leaves out sections that have nothing to show', async () => {
    api.GetVideoDetails.mockResolvedValue(detailsFor(5, {
      video: { description: '   ', original_title: '', tags: [], play_count: 0, last_played_at: null },
      people: [], collections: []
    }));
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [baseVideo(5)], version_groups: {} });
    const wrapper = await mountPage();
    for (const name of ['description', 'people', 'related', 'versions', 'collections', 'history', 'original-title']) {
      expect(wrapper.find(`[data-test="detail-${name}"]`).exists(), name).toBe(false);
    }
    expect(api.GetPersonDetail).not.toHaveBeenCalled();
    expect(api.GetCollectionDetail).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="detail-bookmarks"]').exists()).toBe(true);
  });
});

describe('VideoDetailPage artwork', () => {
  it('always asks for the poster (the server falls back to the thumbnail)', async () => {
    api.GetVideoDetails.mockResolvedValue(detailsFor(5, { artwork: { has_poster: false, has_fanart: false } }));
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="detail-poster"]').attributes('src')).toBe('/preview/video-artwork/5?kind=poster');
  });

  it('uses the fanart as the backdrop when it exists, otherwise the frame carousel', async () => {
    api.GetVideoDetails.mockResolvedValue(detailsFor(5, { artwork: { has_poster: true, has_fanart: true } }));
    let wrapper = await mountPage();
    expect(backdrop(wrapper).props('frames')).toEqual(['/preview/video-artwork/5?kind=fanart']);
    expect(backdrop(wrapper).props('fallback')).toBe('/preview/thumbnail/5');
    wrapper.unmount();

    api.GetVideoDetails.mockResolvedValue(detailsFor(5));
    wrapper = await mountPage();
    const frames = backdrop(wrapper).props('frames');
    expect(frames).toHaveLength(5);
    expect(frames.every(src => src.startsWith('/preview/frame/5?ms='))).toBe(true);
  });

  describe('with images that always load', () => {
    let originalImage;
    let originalMatchMedia;
    beforeEach(() => {
      originalImage = globalThis.Image;
      originalMatchMedia = window.matchMedia;
      globalThis.Image = class {
        set src(value) { this._src = value; queueMicrotask(() => this.onload?.()); }
        get src() { return this._src; }
      };
    });
    afterEach(() => {
      globalThis.Image = originalImage;
      window.matchMedia = originalMatchMedia;
    });
    const reduceMotion = reduce => { window.matchMedia = vi.fn(() => ({ matches: reduce })); };

    it('rotates through every frame normally', async () => {
      reduceMotion(false);
      const wrapper = await mountPage();
      expect(wrapper.findAll('[data-test="detail-backdrop"] .recap-backdrop__frame')).toHaveLength(5);
    });

    it('shows only the first frame when the system asks for reduced motion', async () => {
      reduceMotion(true);
      const wrapper = await mountPage();
      const shown = wrapper.findAll('[data-test="detail-backdrop"] .recap-backdrop__frame');
      expect(shown).toHaveLength(1);
      expect(shown[0].attributes('style')).toContain('/preview/frame/5?ms=60000');
    });
  });
});

describe('VideoDetailPage actions', () => {
  it('plays through PlayVideo like the drawer and writes the played video back', async () => {
    api.PlayVideo.mockResolvedValueOnce({ dispatch_succeeded: true, video: baseVideo(5, { play_count: 3, tags: null }) });
    const wrapper = await mountPage();
    await wrapper.get('[data-test="detail-play"]').trigger('click');
    await flushPromises();
    expect(api.PlayVideo).toHaveBeenCalledWith(5);
    expect(wrapper.get('[data-test="detail-history-plays"]').text()).toContain('3次');
    expect(wrapper.get('[data-test="detail-tag-3"]').exists()).toBe(true);

    api.PlayVideo.mockResolvedValueOnce({ dispatch_succeeded: false, user_message: '文件已不在原位置' });
    await wrapper.get('[data-test="detail-play"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="detail-action-error"]').text()).toBe('文件已不在原位置');
  });

  it('toggles favorite and watched with the single-field APIs', async () => {
    api.SetVideoFavorite.mockResolvedValue(baseVideo(5, { is_favorite: true }));
    api.SetVideoWatched.mockResolvedValue(baseVideo(5, { is_favorite: true, is_watched: true }));
    const wrapper = await mountPage();
    await wrapper.get('[data-test="detail-favorite"]').trigger('click');
    await flushPromises();
    expect(api.SetVideoFavorite).toHaveBeenCalledWith(5, true);
    expect(wrapper.get('[data-test="detail-favorite"]').text()).toBe('已收藏');
    expect(wrapper.get('[data-test="detail-favorite"]').attributes('aria-pressed')).toBe('true');
    await wrapper.get('[data-test="detail-watched"]').trigger('click');
    await flushPromises();
    expect(api.SetVideoWatched).toHaveBeenCalledWith(5, true);
    expect(wrapper.get('[data-test="detail-watched"]').text()).toBe('已看过');

    api.SetVideoWatched.mockRejectedValueOnce('database is in maintenance mode');
    await wrapper.get('[data-test="detail-watched"]').trigger('click');
    await flushPromises();
    expect(api.SetVideoWatched).toHaveBeenLastCalledWith(5, false);
    expect(wrapper.get('[data-test="detail-action-error"]').text()).toContain('更新观看状态失败：数据库正在恢复备份或切换后端');
  });

  it('saves the rating on click and clears it', async () => {
    api.UpdateVideoRating.mockImplementation(async (_id, value) => ({ personal_rating: value }));
    const wrapper = await mountPage();
    expect(wrapper.get('[data-test="detail-stars-value"]').text()).toBe('7.5 / 10');
    await wrapper.get('[data-test="detail-star-8"]').trigger('click');
    await flushPromises();
    expect(api.UpdateVideoRating).toHaveBeenCalledWith(5, 8);
    expect(wrapper.get('[data-test="detail-stars-value"]').text()).toBe('8.0 / 10');
    expect(wrapper.get('[data-test="detail-fact-rating"]').text()).toBe('评分 8.0');
    await wrapper.get('[data-test="detail-stars-clear"]').trigger('click');
    await flushPromises();
    expect(api.UpdateVideoRating).toHaveBeenLastCalledWith(5, null);
    expect(wrapper.get('[data-test="detail-stars-value"]').text()).toBe('未评分');
    await wrapper.get('[data-test="detail-star-0.5"]').trigger('click');
    expect(api.UpdateVideoRating).toHaveBeenLastCalledWith(5, 0.5);
  });

  it('adds the video to the playback queue', async () => {
    api.GetPlaybackQueue.mockResolvedValue({ total: 0, state: { revision: 4 }, items: [] });
    api.EditPlaybackQueue.mockResolvedValue(undefined);
    const wrapper = await mountPage();
    await wrapper.get('[data-test="detail-actions"] [data-test="queue-add"]').trigger('click');
    await flushPromises();
    expect(api.EditPlaybackQueue).toHaveBeenCalledWith(expect.objectContaining({ action: 'append_videos', video_ids: [5], expected_revision: 4 }));
  });

  it('opens the quote card at the resume position with the subtitle of that moment', async () => {
    api.GetVideoDetails.mockResolvedValue(detailsFor(5, { video: { watch_position_seconds: 120 } }));
    api.GetSubtitleSegments.mockResolvedValue([{ start_time_ms: 119000, end_time_ms: 121000, text: '你好' }]);
    const wrapper = await mountPage();
    await wrapper.get('[data-test="detail-quote-card"]').trigger('click');
    await flushPromises();
    expect(api.GetSubtitleSegments).toHaveBeenCalledWith(5);
    expect(wrapper.findComponent(QuoteStub).props()).toEqual({ videoID: 5, atMS: 120000, quote: '你好', title: '影片5' });
  });

  it('emits find-similar, edit-in-manage and back with the documented payloads', async () => {
    api.GetVideoDetails.mockResolvedValue(detailsFor(5, { video: { watch_position_seconds: 30 } }));
    const wrapper = await mountPage();
    await wrapper.get('[data-test="detail-find-similar"]').trigger('click');
    await wrapper.get('[data-test="detail-edit"]').trigger('click');
    await wrapper.get('[data-test="detail-back"]').trigger('click');
    // 播放器开着时读它的当前位置。
    await wrapper.get('[data-test="detail-inline-play"]').trigger('click');
    player(wrapper).vm.position = 61.4;
    await wrapper.get('[data-test="detail-find-similar"]').trigger('click');
    expect(wrapper.emitted('find-similar')).toEqual([
      [{ videoID: 5, atMS: 30000, title: '影片5' }],
      [{ videoID: 5, atMS: 61400, title: '影片5' }]
    ]);
    expect(wrapper.emitted('edit-in-manage')).toEqual([[5]]);
    expect(wrapper.emitted('back')).toHaveLength(1);
  });

  it('compares the selected version against this one', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="detail-version-compare-6"]').trigger('click');
    expect(versionCompareState).toMatchObject({ open: true, leftVideoID: 5, rightVideoID: 6 });
    await wrapper.get('[data-test="detail-version-open-6"]').trigger('click');
    expect(wrapper.emitted('open-video')).toEqual([[6]]);
  });
});

describe('VideoDetailPage in-app playback', () => {
  it('expands the player in the backdrop on demand, resuming from the breakpoint, and collapses it again', async () => {
    api.GetVideoDetails.mockResolvedValue(detailsFor(5, { video: { watch_position_seconds: 95 } }));
    const wrapper = await mountPage();
    expect(player(wrapper).exists()).toBe(false);
    expect(wrapper.find('[data-test="detail-poster"]').exists()).toBe(true);
    await wrapper.get('[data-test="detail-inline-play"]').trigger('click');
    await flushPromises();
    expect(api.GetPreviewSession).toHaveBeenCalledWith(5);
    expect(wrapper.find('[data-test="detail-backdrop"] [data-test="detail-player"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="detail-poster"]').exists()).toBe(false);
    expect(player(wrapper).props()).toMatchObject({ videoId: 5, session: inlineSession(5), startTimeMs: null, resumePositionSeconds: 95, autoplay: true, durationSeconds: 600 });
    expect(wrapper.get('[data-test="detail-inline-play"]').text()).toBe('收起播放器');
    await wrapper.get('[data-test="detail-inline-play"]').trigger('click');
    expect(player(wrapper).exists()).toBe(false);
  });

  it('starts in-app playback at startAtMs right away, and again when a new time arrives for the same video', async () => {
    const wrapper = await mountPage({ startAtMs: 90000 });
    expect(player(wrapper).exists()).toBe(true);
    expect(player(wrapper).props()).toMatchObject({ videoId: 5, startTimeMs: 90000, autoplay: true, session: inlineSession(5) });
    await wrapper.setProps({ startAtMs: 30000 });
    expect(player(wrapper).props('startTimeMs')).toBe(30000);
    expect(api.GetPreviewSession).toHaveBeenCalledTimes(1);
  });

  it('does not start playing by itself without startAtMs', async () => {
    const wrapper = await mountPage({ startAtMs: null });
    expect(player(wrapper).exists()).toBe(false);
    expect(api.GetPreviewSession).not.toHaveBeenCalled();
  });

  it('starts the real inline player at startAtMs', async () => {
    const wrapper = await mountPage({ startAtMs: 90000 }, { realPlayer: true });
    const video = wrapper.find('[data-test="detail-player"] video');
    expect(video.exists()).toBe(true);
    expect(video.find('source').attributes('src')).toBe('/preview/video/5');
    expect(player(wrapper).props()).toMatchObject({ startTimeMs: 90000, autoplay: true });
  });

  it('shows proxy messages from the player and offers a proxy in external-preview mode', async () => {
    api.GetPreviewSession.mockResolvedValue({ video_id: 5, mode: 'external-preview', reason_message: '这个容器不能内嵌', external_action: { button_label: '用系统播放器预览' } });
    const wrapper = await mountPage({ startAtMs: 1000 });
    const stub = player(wrapper).vm;
    stub.proxyError = '生成播放代理失败：磁盘空间不足';
    stub.proxyNotice = '已取消生成播放代理';
    await flushPromises();
    expect(wrapper.get('[data-test="detail-proxy-error"]').text()).toBe('生成播放代理失败：磁盘空间不足');
    expect(wrapper.get('[data-test="detail-proxy-notice"]').text()).toBe('已取消生成播放代理');
    await wrapper.get('[data-test="detail-proxy-create"]').trigger('click');
    expect(stub.proxyCalls).toBe(1);
    stub.proxyPending = true;
    stub.proxyCreateLabel = '排队中...';
    await flushPromises();
    expect(wrapper.get('[data-test="detail-proxy-create"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="detail-proxy-create"]').text()).toBe('排队中...');
  });

  it('has no proxy button while the session plays inline', async () => {
    const wrapper = await mountPage({ startAtMs: 1000 });
    expect(wrapper.find('[data-test="detail-proxy-create"]').exists()).toBe(false);
  });

  it('refetches the session when the player reports it stale, keeping the old one if that fails', async () => {
    const wrapper = await mountPage({ startAtMs: 1000 });
    api.GetPreviewSession.mockResolvedValueOnce({ ...inlineSession(5), source_version: 'sv2' });
    player(wrapper).vm.$emit('preview-session-stale', 5);
    await flushPromises();
    expect(api.GetPreviewSession).toHaveBeenCalledTimes(2);
    expect(player(wrapper).props('session').source_version).toBe('sv2');
    api.GetPreviewSession.mockRejectedValueOnce(new Error('/Users/secret/a.mkv: boom'));
    player(wrapper).vm.$emit('preview-session-stale', 5);
    await flushPromises();
    expect(player(wrapper).props('session').source_version).toBe('sv2');
    expect(wrapper.get('[data-test="detail-action-error"]').text()).toBe('刷新预览会话失败，请稍后重试。');
  });

  it('gives the player a failure session when the first session request fails', async () => {
    api.GetPreviewSession.mockRejectedValue(new Error('/Users/secret/a.mkv: boom'));
    const wrapper = await mountPage({ startAtMs: 1000 });
    expect(player(wrapper).props('session')).toEqual({ video_id: 5, mode: 'unsupported', reason_code: 'preview_failed', reason_message: '准备预览失败，请稍后重试。' });
  });

  it('saves watch progress with the five-argument contract and previews externally', async () => {
    api.UpdateVideoWatchProgress.mockResolvedValue(baseVideo(5, { watch_position_seconds: 30, tags: null }));
    const wrapper = await mountPage({ startAtMs: 1000 });
    player(wrapper).vm.$emit('watch-progress', { videoID: 5, positionSeconds: 30, completed: false, origin: 'jump', durationSeconds: 0 });
    await flushPromises();
    expect(api.UpdateVideoWatchProgress).toHaveBeenCalledWith(5, 30, 0, false, 'jump');
    expect(wrapper.get('[data-test="detail-history-progress"]').text()).toContain('00:30 / 10:00');
    player(wrapper).vm.$emit('preview-externally', 5);
    await flushPromises();
    expect(api.PreviewExternally).toHaveBeenCalledWith(5);
  });

  it('plays a bookmark range in the inline player and offers to continue at its end', async () => {
    const wrapper = await mountPage();
    wrapper.findComponent(BookmarkPanel).vm.$emit('open-bookmark', {
      status: 'ready', video: { id: 5 }, bookmark: { start_ms: 5000, end_ms: 9000 }, source_token: 'sv1'
    });
    await flushPromises();
    expect(player(wrapper).props('startTimeMs')).toBe(5000);
    expect(player(wrapper).props('bookmarkRange')).toMatchObject({ videoID: 5, startMS: 5000, endMS: 9000, sourceToken: 'sv1' });
    expect(wrapper.findComponent(BookmarkPanel).props('session')).toEqual(inlineSession(5));
    player(wrapper).vm.$emit('bookmark-range-end', player(wrapper).props('bookmarkRange'));
    await flushPromises();
    await wrapper.get('[data-test="detail-bookmark-range-end"] button').trigger('click');
    expect(player(wrapper).vm.playCalls).toBe(1);
    expect(wrapper.find('[data-test="detail-bookmark-range-end"]').exists()).toBe(false);
  });
});

describe('VideoDetailPage navigation', () => {
  it('emits open-tag, open-video and open-entity for tags, related items, next items, people and collections', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-test="detail-tag-3"]').trigger('click');
    await wrapper.findAll('[data-test="detail-related-item"]')[1].trigger('click');
    await wrapper.get('[data-test="detail-collection-next-41"]').trigger('click');
    await wrapper.get('[data-test="detail-person-31"]').trigger('click');
    await wrapper.get('[data-test="detail-collection-open-41"]').trigger('click');
    expect(wrapper.emitted('open-tag')).toEqual([[3]]);
    expect(wrapper.emitted('open-video')).toEqual([[8], [12]]);
    expect(wrapper.emitted('open-entity')).toEqual([
      [{ type: 'person', id: 31, name: '甲' }],
      [{ type: 'collection', id: 41, name: '三部曲' }]
    ]);
  });

  it('hands a bookmark for another video to the host', async () => {
    const wrapper = await mountPage();
    wrapper.findComponent(BookmarkPanel).vm.$emit('open-video', { id: 77 });
    expect(wrapper.emitted('open-video')).toEqual([[77]]);
  });

  it('reloads for a new videoId, resets the player and scrolls the page container back to the top', async () => {
    const host = document.createElement('div');
    host.className = 'main-view';
    document.body.appendChild(host);
    let scrollTop = 640;
    Object.defineProperty(host, 'scrollTop', { configurable: true, get: () => scrollTop, set: value => { scrollTop = value; } });
    const wrapper = mount(VideoDetailPage, { props: { videoId: 5, startAtMs: 1000 }, attachTo: host, global: { stubs: { InlineVideoPlayer: PlayerStub, QuoteCardDialog: QuoteStub } } });
    await flushPromises();
    expect(scrollTop).toBe(0);
    expect(player(wrapper).exists()).toBe(true);
    scrollTop = 900;
    await wrapper.setProps({ videoId: 7, startAtMs: null });
    expect(scrollTop).toBe(0);
    await flushPromises();
    expect(api.GetVideoDetails).toHaveBeenLastCalledWith(7);
    expect(wrapper.get('[data-test="detail-title"]').text()).toBe('影片7');
    expect(player(wrapper).exists()).toBe(false);
    wrapper.unmount();
    host.remove();
  });

  it('drops a late response for the previous video', async () => {
    let resolveFirst;
    api.GetVideoDetails.mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve; }));
    const wrapper = await mountPage();
    await wrapper.setProps({ videoId: 7 });
    await flushPromises();
    resolveFirst(detailsFor(5));
    await flushPromises();
    expect(wrapper.get('[data-test="detail-title"]').text()).toBe('影片7');
  });
});
