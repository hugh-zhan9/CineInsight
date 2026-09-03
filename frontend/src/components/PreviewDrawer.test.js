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

const api = vi.hoisted(() => ({
  AddCollectionVideo: vi.fn(),
  AddCollectionVideos: vi.fn(),
  AddPersonVideo: vi.fn(),
  AddPersonVideos: vi.fn(),
  CreatePerson: vi.fn(),
  DeleteCollection: vi.fn(),
  DeleteGlossaryEntry: vi.fn(),
  ListGlossaryEntries: vi.fn(() => Promise.resolve([])),
  UpsertGlossaryEntry: vi.fn(),
  GetCollectionDetail: vi.fn(),
  GetAllDirectories: vi.fn(),
  GetPersonDetail: vi.fn(),
  GetPersonImages: vi.fn(),
  GetPreviewSession: vi.fn(),
  GetVideoDetails: vi.fn(),
  ListCollections: vi.fn(),
  ListPeople: vi.fn(),
  RefreshVideoTechnicalMetadata: vi.fn(),
  RemoveCollectionCover: vi.fn(),
  RemoveCollectionVideo: vi.fn(),
  RemovePersonAvatar: vi.fn(),
  RemovePersonImage: vi.fn(),
  RemovePersonVideo: vi.fn(),
  ReorderCollectionVideos: vi.fn(),
  SearchLibraryVideoPage: vi.fn(),
  SelectCollectionCover: vi.fn(),
  SelectDirectory: vi.fn(),
  SelectPersonAvatar: vi.fn(),
  SetCollectionCover: vi.fn(),
  SetPersonAvatar: vi.fn(),
  UpdateCollection: vi.fn(),
  UpdatePerson: vi.fn(),
  UpdateVideoDetails: vi.fn(),
  CreatePlaybackProxy: vi.fn(),
  DeletePlaybackProxy: vi.fn(),
  GetPlaybackProxy: vi.fn()
}));

vi.mock('../../wailsjs/go/main/App', () => api);

import PreviewDrawer from './PreviewDrawer.vue';

function videoDetails(id, overrides = {}) {
  return {
    video: {
      id,
      name: `video-${id}.mp4`,
      display_title: `Video ${id}`,
      original_title: '',
      personal_rating: null,
      size: 1024,
      duration: 60,
      watch_position_seconds: 0,
      ...overrides.video
    },
    effective_title: `Video ${id}`,
    people: [],
    collections: [],
    streams: [],
    technical_status: { state: 'current' },
    technical_metadata: {
      format_name: 'mp4',
      format_long_name: 'MPEG-4',
      probed_at: '2026-07-30T10:00:00Z',
      ...overrides.technical_metadata
    },
    ...overrides
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

async function mountDrawer(details = videoDetails(1)) {
  api.GetVideoDetails.mockImplementation(id => Promise.resolve(id === details.video.id ? details : videoDetails(id)));
  api.GetPreviewSession.mockImplementation(id => Promise.resolve({ video_id: id, mode: 'unavailable', reason_message: '不可预览' }));
  api.ListCollections.mockResolvedValue([]);
  const wrapper = mount(PreviewDrawer, { props: { video: details.video, session: null } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  api.GetAllDirectories.mockResolvedValue([]);
  api.SelectDirectory.mockResolvedValue('');
  api.GetPlaybackProxy.mockResolvedValue(null);
  api.CreatePlaybackProxy.mockResolvedValue({ results: [{ video_id: 1, code: 'created', strategy: 'remux' }] });
  api.DeletePlaybackProxy.mockResolvedValue(null);
});

describe('PreviewDrawer', () => {
  it('mounts and loads the root video details', async () => {
    const wrapper = await mountDrawer();

    expect(api.GetVideoDetails).toHaveBeenCalledWith(1);
    expect(wrapper.vm.currentEntry).toEqual({ type: 'video', id: 1 });
    expect(wrapper.vm.details.video.id).toBe(1);
    expect(wrapper.text()).toContain('Video 1');
  });

  it('exposes an explicit NFO export action for the current video', async () => {
	  const wrapper = await mountDrawer();
	  const button = wrapper.findAll('button').find(item => item.text() === '写出 NFO');
	  expect(button).toBeTruthy();
	  await button.trigger('click');
	  expect(wrapper.emitted('export-local-metadata')?.[0]?.[0]).toEqual(expect.objectContaining({ id: 1 }));
	});

  it('emits the current video from the find-similar action', async () => {
	const wrapper = await mountDrawer();
	const button = wrapper.findAll('button').find(item => item.text() === '找相似');
	expect(button).toBeTruthy();
	await button.trigger('click');
	expect(wrapper.emitted('find-similar')?.[0]?.[0]).toEqual(expect.objectContaining({ id: 1 }));
  });

  it('renders an inline preview inside the dedicated non-collapsing player section', async () => {
    const details = videoDetails(1);
    api.GetVideoDetails.mockResolvedValue(details);
    api.ListCollections.mockResolvedValue([]);
    const wrapper = mount(PreviewDrawer, {
      props: {
        video: details.video,
        session: {
          video_id: 1,
          mode: 'inline',
          inline_source: { locator_value: '/preview/video/1', mime: 'video/mp4' }
        }
      }
    });
    await flushPromises();

    expect(wrapper.get('.detail-section--player').exists()).toBe(true);
    expect(wrapper.get('.preview-drawer__player-shell').exists()).toBe(true);
    expect(wrapper.get('.preview-drawer__video source').attributes('src')).toBe('/preview/video/1');
    expect(wrapper.find('.preview-drawer__seek-track').exists()).toBe(false);
  });

	it('emits the same review action for drawer keyboard shortcuts and ignores inputs', async () => {
	  const wrapper = await mountDrawer();
	  const preventDefault = vi.fn();
	  wrapper.vm.handleReviewShortcut({ key: 'f', target: document.body, preventDefault });
	  expect(preventDefault).toHaveBeenCalledOnce();
	  expect(wrapper.emitted('shortcut')?.[0]?.[0]).toEqual(expect.objectContaining({ action: 'favorite', video: expect.objectContaining({ id: 1 }) }));

	  wrapper.vm.handleReviewShortcut({ key: 'w', target: document.createElement('input'), preventDefault });
	  expect(wrapper.emitted('shortcut')).toHaveLength(1);
	});

  it('maps seek hover time to the matching sprite frame and degrades on asset failure', async () => {
    const details = videoDetails(1);
    api.GetVideoDetails.mockResolvedValue(details);
    api.ListCollections.mockResolvedValue([]);
    const wrapper = mount(PreviewDrawer, {
      props: {
        video: details.video,
        session: {
          video_id: 1,
          mode: 'inline',
          inline_source: { locator_value: '/preview/video/1', mime: 'video/mp4' },
          seek_sprite: {
            locator_value: '/preview/seek-sprite/1',
            frame_width: 160,
            frame_height: 90,
            columns: 6,
            rows: 1,
            frame_count: 6,
            interval_seconds: 10
          }
        }
      }
    });
    await flushPromises();

    const track = wrapper.get('.preview-drawer__seek-track');
    wrapper.vm.handleSeekPointerMove({
      clientX: 55,
      currentTarget: { getBoundingClientRect: () => ({ left: 5, width: 100 }) }
    });
    await wrapper.vm.$nextTick();

    expect(wrapper.vm.seekPreview.frameIndex).toBe(3);
    expect(wrapper.get('.preview-drawer__seek-preview time').text()).toBe('00:30');
    const sprite = wrapper.get('.preview-drawer__seek-image img');
    expect(sprite.attributes('src')).toBe('/preview/seek-sprite/1');
    expect(sprite.attributes('style')).toContain('left: -300%');

    await sprite.trigger('error');
    expect(wrapper.find('.preview-drawer__seek-track').exists()).toBe(false);
    expect(wrapper.get('.preview-drawer__video source').attributes('src')).toBe('/preview/video/1');
  });

  it('saves a zero rating through the mounted drawer', async () => {
    const wrapper = await mountDrawer();
    const ratingInput = wrapper.get('.detail-rating-input input');
    expect(ratingInput.attributes('type')).toBe('text');
    expect(ratingInput.attributes('inputmode')).toBe('decimal');
    expect(wrapper.find('.detail-rating-input select').exists()).toBe(false);
    const updated = videoDetails(1, { video: { id: 1, personal_rating: 0 } });
    api.UpdateVideoDetails.mockResolvedValueOnce(updated);
    await ratingInput.setValue('0');

    await wrapper.vm.saveVideoDetails();

    expect(api.UpdateVideoDetails).toHaveBeenCalledWith(expect.objectContaining({ video_id: 1, personal_rating: 0 }));
    expect(wrapper.vm.details.video.personal_rating).toBe(0);
    expect(wrapper.vm.draft.personalRating).toBe('0');
  });

  it('navigates to a person detail inside the mounted drawer', async () => {
    const wrapper = await mountDrawer();
    api.GetPersonDetail.mockResolvedValueOnce({
      person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 0 },
      videos: [],
      next_video_id: 0
    });

    await wrapper.vm.openPerson(7);
    await flushPromises();

    expect(wrapper.vm.currentEntry).toEqual({ type: 'person', id: 7 });
    expect(wrapper.vm.personDetail.person.person.display_name).toBe('Actor Seven');
    expect(wrapper.text()).toContain('Actor Seven');
    expect(wrapper.vm.canGoBack).toBe(true);
  });

  it('searches and associates videos from a person detail with thumbnails', async () => {
    const firstVideo = { id: 1, name: 'one.mp4', display_title: 'One' };
    const secondVideo = { id: 2, name: 'two.mp4', display_title: 'Two' };
    api.GetPersonDetail
      .mockResolvedValueOnce({
        person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 1 },
        videos: [firstVideo], next_video_id: 0
      })
      .mockResolvedValueOnce({
        person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 2 },
        videos: [secondVideo, firstVideo], next_video_id: 0
      });
    api.SearchLibraryVideoPage.mockResolvedValueOnce({ videos: [secondVideo] });
    api.AddPersonVideo.mockResolvedValueOnce();
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } } });
    await flushPromises();

    wrapper.vm.relatedVideoKeyword = 'two';
    await wrapper.vm.searchRelatedVideos();

    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith(expect.objectContaining({
      filter: expect.objectContaining({ keyword: 'two', search_mode: 'file' }),
      limit: 30
    }));
    expect(wrapper.find('img[src="/preview/thumbnail/2"]').exists()).toBe(true);

    await wrapper.vm.addRelatedVideo(secondVideo);
    await flushPromises();

    expect(api.AddPersonVideo).toHaveBeenCalledWith(7, 2);
    expect(wrapper.vm.personDetail.person.active_video_count).toBe(2);
    expect(wrapper.vm.relatedVideoIDs).toEqual([2, 1]);
  });

  it('selects a nested folder and searches all videos below that path', async () => {
    api.GetPersonDetail.mockResolvedValueOnce({
      person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 0 },
      videos: [], next_video_id: 0
    });
    api.SelectDirectory.mockResolvedValueOnce('/library/shows/season-1');
    api.SearchLibraryVideoPage.mockResolvedValueOnce({ videos: [] });
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } } });
    await flushPromises();

    await wrapper.get('[data-test="related-video-folder-picker"]').trigger('click');
    await flushPromises();

    expect(wrapper.vm.relatedVideoDirectory).toBe('/library/shows/season-1');
    expect(wrapper.text()).toContain('已选：/library/shows/season-1');
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith(expect.objectContaining({
      filter: expect.objectContaining({ path_prefix: '/library/shows/season-1' })
    }));
  });

  it('warns before removing a person final video and reports person cleanup', async () => {
    const onlyVideo = { id: 1, name: 'only.mp4', display_title: 'Only' };
    api.GetPersonDetail.mockResolvedValueOnce({
      person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 1 },
      videos: [onlyVideo], next_video_id: 0
    });
    api.RemovePersonVideo.mockResolvedValueOnce(true);
    feedback.confirmAction.mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } } });
    await flushPromises();

    await wrapper.vm.removeRelatedVideo(onlyVideo);
    expect(api.RemovePersonVideo).not.toHaveBeenCalled();
    await wrapper.vm.removeRelatedVideo(onlyVideo);

    expect(feedback.confirmAction).toHaveBeenCalledTimes(2);
    expect(api.RemovePersonVideo).toHaveBeenCalledWith(7, 1);
    expect(wrapper.emitted('person-deleted')).toEqual([[7]]);
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  // D-021：人物详情里视频与图片是两个区块、各自分页。
  it('renders the person image section, pages it separately and removes one relation', async () => {
    api.GetPersonDetail.mockResolvedValue({
      person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 1, active_image_count: 3 },
      videos: [{ id: 1, name: 'one.mp4' }], next_video_id: 0,
      images: [{ id: 11, name: 'a.jpg' }, { id: 12, name: 'b.jpg' }], next_image_id: 12
    });
    api.GetPersonImages.mockResolvedValueOnce({ images: [{ id: 13, name: 'c.jpg' }], next_image_id: 0 });
    api.RemovePersonImage.mockResolvedValueOnce(false);
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } } });
    await flushPromises();

    const section = wrapper.get('[data-test="person-image-section"]');
    expect(section.text()).toContain('关联图片（3）');
    expect(section.findAll('.person-image-card')).toHaveLength(2);
    expect(section.find('img[src="/preview/image-thumbnail/11"]').exists()).toBe(true);

    await wrapper.get('[data-test="person-image-more"]').trigger('click');
    await flushPromises();
    expect(api.GetPersonImages).toHaveBeenCalledWith(7, 12, 200);
    expect(wrapper.findAll('.person-image-card')).toHaveLength(3);
    expect(wrapper.find('[data-test="person-image-more"]').exists()).toBe(false);

    // 该人物还有视频关系，解除一条图片关系不需要确认。
    await wrapper.get('[data-test="person-image-remove-11"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.RemovePersonImage).toHaveBeenCalledWith(7, 11);
    expect(wrapper.emitted('relations-updated')).toEqual([[{ type: 'person', id: 7 }]]);
  });

  it('warns before removing a person final image and reports person cleanup', async () => {
    api.GetPersonDetail.mockResolvedValue({
      person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 0, active_image_count: 1 },
      videos: [], next_video_id: 0,
      images: [{ id: 11, name: 'a.jpg' }], next_image_id: 0
    });
    api.RemovePersonImage.mockResolvedValueOnce(true);
    feedback.confirmAction.mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } } });
    await flushPromises();

    await wrapper.get('[data-test="person-image-remove-11"]').trigger('click');
    await flushPromises();
    expect(api.RemovePersonImage).not.toHaveBeenCalled();

    await wrapper.get('[data-test="person-image-remove-11"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledTimes(2);
    expect(api.RemovePersonImage).toHaveBeenCalledWith(7, 11);
    expect(wrapper.emitted('person-deleted')).toEqual([[7]]);
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  // 最后关系判定跨两种媒体：还有图片关系时解除最后一个视频关系不该弹确认。
  it('skips the final-relation warning for a video when image relations remain', async () => {
    const onlyVideo = { id: 1, name: 'only.mp4', display_title: 'Only' };
    api.GetPersonDetail.mockResolvedValue({
      person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 1, active_image_count: 2 },
      videos: [onlyVideo], next_video_id: 0,
      images: [{ id: 11, name: 'a.jpg' }, { id: 12, name: 'b.jpg' }], next_image_id: 0
    });
    api.RemovePersonVideo.mockResolvedValueOnce(false);
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } } });
    await flushPromises();

    await wrapper.vm.removeRelatedVideo(onlyVideo);
    await flushPromises();

    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.RemovePersonVideo).toHaveBeenCalledWith(7, 1);
  });

  it('does not apply a completed save after navigating to another video', async () => {
    const wrapper = await mountDrawer();
    const firstSave = deferred();
    const secondSave = deferred();
    api.UpdateVideoDetails.mockReturnValueOnce(firstSave.promise).mockReturnValueOnce(secondSave.promise);

    const firstSavePromise = wrapper.vm.saveVideoDetails();
    await wrapper.vm.openVideo(2);
    await flushPromises();
    expect(wrapper.vm.details.video.id).toBe(2);
    const secondSavePromise = wrapper.vm.saveVideoDetails();

    firstSave.resolve(videoDetails(1, { video: { id: 1, display_title: 'Saved A' } }));
    await firstSavePromise;
    await flushPromises();

    expect(wrapper.vm.currentEntry).toEqual({ type: 'video', id: 2 });
    expect(wrapper.vm.details.video.id).toBe(2);
    expect(wrapper.vm.details.video.display_title).toBe('Video 2');
    expect(wrapper.vm.saving).toBe(true);
    expect(wrapper.emitted('details-updated')[0][0].video.display_title).toBe('Saved A');

    secondSave.resolve(videoDetails(2, { video: { id: 2, display_title: 'Saved B' } }));
    await secondSavePromise;

    expect(wrapper.vm.details.video.display_title).toBe('Saved B');
    expect(wrapper.vm.saving).toBe(false);
  });

  it('ignores stale person-search results that finish out of order', async () => {
    const wrapper = await mountDrawer();
    const oldSearch = deferred();
    const newSearch = deferred();
    api.ListPeople.mockImplementation(keyword => keyword === 'old' ? oldSearch.promise : newSearch.promise);

    wrapper.vm.personKeyword = 'old';
    const oldPromise = wrapper.vm.searchPeople();
    wrapper.vm.personKeyword = 'new';
    const newPromise = wrapper.vm.searchPeople();

    newSearch.resolve([{ person: { id: 2, display_name: 'New result' }, avatar_url: '', active_video_count: 1 }]);
    await newPromise;
    oldSearch.resolve([{ person: { id: 1, display_name: 'Old result' }, avatar_url: '', active_video_count: 1 }]);
    await oldPromise;

    expect(wrapper.vm.personCandidates.map(item => item.person.display_name)).toEqual(['New result']);
  });

  it('does not attach a person created for a video after navigating away', async () => {
    const wrapper = await mountDrawer();
    const pendingCreate = deferred();
    api.CreatePerson.mockReturnValueOnce(pendingCreate.promise);
    wrapper.vm.newPerson = { displayName: 'Actor A', originalName: '' };

    const createPromise = wrapper.vm.createAndSelectPerson();
    await wrapper.vm.openVideo(2);
    pendingCreate.resolve({ id: 9, display_name: 'Actor A', original_name: '' });
    await createPromise;

    expect(wrapper.vm.currentEntry).toEqual({ type: 'video', id: 2 });
    expect(wrapper.vm.draft.personIDs).toEqual([]);
    expect(wrapper.vm.personCandidates).toEqual([]);
  });

  it('prevents duplicate person creation while the first request is pending', async () => {
    const wrapper = await mountDrawer();
    const pendingCreate = deferred();
    api.CreatePerson.mockReturnValueOnce(pendingCreate.promise);
    wrapper.vm.newPerson = { displayName: 'Only Once', originalName: '' };

    const firstCreate = wrapper.vm.createAndSelectPerson();
    const secondCreate = wrapper.vm.createAndSelectPerson();

    expect(wrapper.vm.creatingPerson).toBe(true);
    expect(api.CreatePerson).toHaveBeenCalledOnce();
    pendingCreate.resolve({ id: 10, display_name: 'Only Once', original_name: '' });
    await Promise.all([firstCreate, secondCreate]);
    expect(wrapper.vm.creatingPerson).toBe(false);
    expect(wrapper.vm.draft.personIDs).toEqual([10]);
  });

  it('keeps the last successful technical snapshot visible when refresh fails', async () => {
    const previous = videoDetails(1, { technical_metadata: { format_name: 'matroska', format_long_name: 'Matroska', probed_at: '2026-07-30T09:00:00Z' } });
    const wrapper = await mountDrawer(previous);
    api.RefreshVideoTechnicalMetadata.mockRejectedValueOnce(new Error('ffprobe failed'));
    api.GetVideoDetails.mockResolvedValueOnce(previous);

    await wrapper.vm.refreshTechnical();
    await flushPromises();

    expect(wrapper.vm.details.technical_metadata.format_long_name).toBe('Matroska');
    expect(wrapper.vm.technicalError).toContain('ffprobe failed');
    expect(wrapper.text()).toContain('Matroska');
    expect(wrapper.text()).toContain('ffprobe failed');
  });

  it('reorders collection members through the mounted drawer', async () => {
    api.GetCollectionDetail.mockResolvedValueOnce({
      collection: { collection: { id: 3, name: 'Collection', description: '' }, cover_url: '', active_video_count: 2 },
      videos: [
        { video: { id: 1, name: 'one.mp4' }, position: 1 },
        { video: { id: 2, name: 'two.mp4' }, position: 2 }
      ]
    });
    api.ReorderCollectionVideos.mockResolvedValueOnce();
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'collection', id: 3 } } });
    await flushPromises();

    wrapper.vm.draggedMemberIndex = 0;
    await wrapper.vm.dropCollectionMember(1);

    expect(api.ReorderCollectionVideos).toHaveBeenCalledWith(3, [2, 1]);
    expect(wrapper.vm.collectionDetail.videos.map(item => item.video.id)).toEqual([2, 1]);
  });

  it('adds and removes collection videos while preserving thumbnail cards', async () => {
    const firstVideo = { id: 1, name: 'one.mp4', display_title: 'One' };
    const secondVideo = { id: 2, name: 'two.mp4', display_title: 'Two' };
    const collection = { collection: { id: 3, name: 'Collection', description: '' }, cover_url: '', active_video_count: 1 };
    api.GetCollectionDetail
      .mockResolvedValueOnce({ collection, videos: [{ video: firstVideo, position: 1 }] })
      .mockResolvedValueOnce({ collection: { ...collection, active_video_count: 2 }, videos: [{ video: firstVideo, position: 1 }, { video: secondVideo, position: 2 }] })
      .mockResolvedValueOnce({ collection, videos: [{ video: secondVideo, position: 1 }] });
    api.SearchLibraryVideoPage.mockResolvedValueOnce({ videos: [secondVideo] });
    api.AddCollectionVideo.mockResolvedValueOnce();
    api.RemoveCollectionVideo.mockResolvedValueOnce();
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'collection', id: 3 } } });
    await flushPromises();

    await wrapper.vm.searchRelatedVideos();
    expect(wrapper.find('img[src="/preview/thumbnail/1"]').exists()).toBe(true);
    expect(wrapper.find('img[src="/preview/thumbnail/2"]').exists()).toBe(true);

    await wrapper.vm.addRelatedVideo(secondVideo);
    expect(api.AddCollectionVideo).toHaveBeenCalledWith(3, 2);
    expect(wrapper.vm.relatedVideoIDs).toEqual([1, 2]);

    await wrapper.vm.removeRelatedVideo(firstVideo);
    expect(api.RemoveCollectionVideo).toHaveBeenCalledWith(3, 1);
    expect(wrapper.vm.relatedVideoIDs).toEqual([2]);
  });
});

describe('PreviewDrawer 作品集术语表（P-010）', () => {
  it('作品集详情按该作品集的作用域加载术语表', async () => {
    api.GetCollectionDetail.mockResolvedValueOnce({
      collection: { collection: { id: 3, name: 'Collection', description: '' }, cover_url: '', active_video_count: 0 },
      videos: []
    });
    api.ListGlossaryEntries.mockResolvedValue([
      { id: 9, collection_id: 3, scope_key: 3, source_term: 'Neo', source_term_lower: 'neo', target_term: '尼奥', note: '' }
    ]);

    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'collection', id: 3 } } });
    await flushPromises();

    const editor = wrapper.findComponent({ name: 'GlossaryEditor' });
    expect(editor.exists()).toBe(true);
    expect(editor.props('collectionId')).toBe(3);
    expect(api.ListGlossaryEntries).toHaveBeenCalledWith(3);
    expect(wrapper.findAll('[data-test="glossary-entry"]')).toHaveLength(1);
  });

  it('视频详情里没有术语表区块', async () => {
    api.GetVideoDetails.mockResolvedValue(videoDetails(1));
    api.GetPreviewSession.mockResolvedValue({ url: 'http://127.0.0.1/preview/1' });
    api.ListCollections.mockResolvedValue([]);
    const wrapper = mount(PreviewDrawer, { props: { video: { id: 1, name: 'one.mp4' } } });
    await flushPromises();

    expect(wrapper.findComponent({ name: 'GlossaryEditor' }).exists()).toBe(false);
  });

  // ===== 播放代理（D-004、D-006）=====

  it('没有代理时技术信息区块给出生成入口，没有删除入口', async () => {
    const wrapper = await mountDrawer();
    expect(api.GetPlaybackProxy).toHaveBeenCalledWith(1);
    expect(wrapper.get('[data-test="preview-proxy-status"]').text()).toContain('尚未生成播放代理');
    expect(wrapper.find('[data-test="preview-proxy-create"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="preview-proxy-delete"]').exists()).toBe(false);
  });

  it('已有代理时显示策略与体积，并给出删除入口', async () => {
    api.GetPlaybackProxy.mockResolvedValue({
      video_id: 1, strategy: 'remux', status: 'ready', output_size: 2048, last_error: ''
    });
    const wrapper = await mountDrawer();
    const text = wrapper.get('[data-test="preview-proxy-status"]').text();
    expect(text).toContain('已有播放代理');
    expect(text).toContain('换容器（未重编码）');
    expect(text).toContain('2.0 KB');
    expect(wrapper.find('[data-test="preview-proxy-delete"]').exists()).toBe(true);
  });

  it('上次生成失败时把原因显示出来', async () => {
    api.GetPlaybackProxy.mockResolvedValue({
      video_id: 1, strategy: 'transcode', status: 'failed', output_size: 0, last_error: '磁盘空间不足'
    });
    const wrapper = await mountDrawer();
    expect(wrapper.get('[data-test="preview-proxy-status"]').text()).toContain('磁盘空间不足');
  });

  it('经代理播放时明确标注出来', async () => {
    const details = videoDetails(1);
    api.GetVideoDetails.mockResolvedValue(details);
    api.ListCollections.mockResolvedValue([]);
    const wrapper = mount(PreviewDrawer, {
      props: {
        video: details.video,
        session: {
          video_id: 1,
          mode: 'inline',
          inline_source: { locator_strategy: 'asset_route', locator_value: '/preview/media/1', mime: 'video/mp4' },
          proxy: { strategy: 'transcode', size: 4096 }
        }
      }
    });
    await flushPromises();
    const text = wrapper.get('[data-test="preview-proxy-status"]').text();
    expect(text).toContain('正经播放代理播放');
    expect(text).toContain('已重编码');
    expect(wrapper.vm.playingThroughProxy).toBe(true);
  });

  it('生成代理之后回读元数据并请求刷新根条目的预览会话', async () => {
    api.GetPlaybackProxy
      .mockResolvedValueOnce(null)
      .mockResolvedValue({ video_id: 1, strategy: 'remux', status: 'ready', output_size: 1024, last_error: '' });
    const wrapper = await mountDrawer();
    await wrapper.get('[data-test="preview-proxy-create"]').trigger('click');
    await flushPromises();

    expect(api.CreatePlaybackProxy).toHaveBeenCalledWith(1);
    expect(wrapper.vm.playbackProxy?.status).toBe('ready');
    expect(wrapper.emitted('preview-session-stale')?.[0]).toEqual([1]);
    expect(wrapper.get('[data-test="preview-proxy-status"]').text()).toContain('已有播放代理');
  });

  it('生成失败时把结果码翻成人话', async () => {
    api.CreatePlaybackProxy.mockResolvedValue({ results: [{ video_id: 1, code: 'disk_full' }] });
    const wrapper = await mountDrawer();
    await wrapper.get('[data-test="preview-proxy-create"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="preview-proxy-error"]').text()).toContain('磁盘空间不足');
  });

  it('删除代理先确认，取消就什么都不做', async () => {
    api.GetPlaybackProxy.mockResolvedValue({ video_id: 1, strategy: 'remux', status: 'ready', output_size: 1024, last_error: '' });
    feedback.confirmAction.mockResolvedValue(false);
    const wrapper = await mountDrawer();
    await wrapper.get('[data-test="preview-proxy-delete"]').trigger('click');
    await flushPromises();
    expect(api.DeletePlaybackProxy).not.toHaveBeenCalled();
  });

  it('确认后删除代理并刷新状态', async () => {
    // 上一条用例把确认框改成了"取消"，clearAllMocks 不会撤销 mockResolvedValue。
    feedback.confirmAction.mockResolvedValue(true);
    api.GetPlaybackProxy
      .mockResolvedValueOnce({ video_id: 1, strategy: 'remux', status: 'ready', output_size: 1024, last_error: '' })
      .mockResolvedValue(null);
    const wrapper = await mountDrawer();
    await wrapper.get('[data-test="preview-proxy-delete"]').trigger('click');
    await flushPromises();
    expect(api.DeletePlaybackProxy).toHaveBeenCalledWith(1);
    expect(wrapper.vm.playbackProxy).toBeNull();
    expect(wrapper.emitted('preview-session-stale')?.[0]).toEqual([1]);
  });
});
