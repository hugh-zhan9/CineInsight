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
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  AddCollectionVideo: vi.fn(),
  AddCollectionVideos: vi.fn(),
  AddPersonVideo: vi.fn(),
  AddPersonVideos: vi.fn(),
  CreatePerson: vi.fn(),
  DeleteCollection: vi.fn(),
  DeleteVideo: vi.fn(),
  DeleteImage: vi.fn(),
  // P-034：抽屉里删除人物媒体改走带结果码的批量删除（PersonMediaDeleteDialog + TrashUndoBanner）。
  DeleteVideosWithResult: vi.fn(),
  DeleteImagesWithResult: vi.fn(),
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
  SearchImagePage: vi.fn(),
  SelectCollectionCover: vi.fn(),
  SelectDirectory: vi.fn(),
  SelectPersonAvatar: vi.fn(),
  SetCollectionCover: vi.fn(),
  SetPersonAvatar: vi.fn(),
  UpdateCollection: vi.fn(),
  UpdatePerson: vi.fn(),
  UpdateVideoDetails: vi.fn(),
  UpdateVideoRating: vi.fn(),
  CreatePlaybackProxy: vi.fn(),
  DeletePlaybackProxy: vi.fn(),
  GetPlaybackProxy: vi.fn(),
  // P-037：代理排位快照、动作条与有效观看。
  GetPlaybackProxyStatus: vi.fn(),
  PlayVideo: vi.fn(),
  SetVideoFavorite: vi.fn(),
  SetVideoLiked: vi.fn(),
  SetVideoWatched: vi.fn(),
  RecordViewEvent: vi.fn()
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
  feedback.confirmAction.mockResolvedValue(true);
  api.GetAllDirectories.mockResolvedValue([]);
  api.SelectDirectory.mockResolvedValue('');
  api.GetPlaybackProxy.mockResolvedValue(null);
  api.CreatePlaybackProxy.mockResolvedValue({ results: [{ video_id: 1, code: 'created', strategy: 'remux' }] });
  api.DeletePlaybackProxy.mockResolvedValue(null);
  api.GetPlaybackProxyStatus.mockResolvedValue({ running: false, queued: 0, queued_video_ids: [], current_video_id: 0, results: [] });
  api.RecordViewEvent.mockResolvedValue(true);
});

describe('PreviewDrawer', () => {
  it('deletes person media with confirmation and keeps the person and remaining sources', async () => {
    api.GetPersonDetail.mockResolvedValueOnce({
      person: { person: { id: 7, display_name: '人物' }, active_video_count: 1, active_image_count: 2 },
      videos: [{ id: 1, name: 'clip.mp4' }], images: [{ id: 11, name: 'a.jpg' }, { id: 12, name: 'b.jpg' }]
    });
    const okResult = ids => Promise.resolve({ batch_id: 'b1', items: ids.map(id => ({ id, code: 'ok' })) });
    api.DeleteVideosWithResult.mockImplementation(okResult); api.DeleteImagesWithResult.mockImplementation(okResult);
    const w = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } }, global: { stubs: { teleport: true } } }); await flushPromises();
    await w.get('[data-test="drawer-person-video-delete-1"]').trigger('click');
    await w.get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([1], false, expect.any(String));
    expect(api.DeleteVideo).not.toHaveBeenCalled();
    expect(w.vm.personDetail.videos).toHaveLength(0); expect(w.vm.personDetail.person.active_video_count).toBe(0);
    await w.get('[data-test="drawer-person-image-delete-11"]').trigger('click');
    await w.get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([11], false, expect.any(String));
    expect(api.DeleteImage).not.toHaveBeenCalled();
    expect(w.vm.personImages.map(image => image.id)).toEqual([12]); expect(w.vm.personDetail.person.active_image_count).toBe(1);
    expect(w.emitted('media-deleted')).toHaveLength(2); expect(w.emitted('person-deleted')).toBeUndefined();
    expect(api.GetPersonDetail).toHaveBeenCalledTimes(1); w.unmount();
  });
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
    // D-PC47 之后评分改完即保存：0 分同样立即写入，之后的「保存作品信息」也带着 0。
    api.UpdateVideoRating.mockResolvedValueOnce({ id: 1, personal_rating: 0 });
    await ratingInput.setValue('0');
    await flushPromises();
    expect(api.UpdateVideoRating).toHaveBeenCalledWith(1, 0);

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

  // META-05（D-PC32）：「新建并加入」只暂存，离开这个视频（确认放弃）就丢掉，绝不留下空人物。
  // 此前这里断言的是点一下就 CreatePerson 的旧行为。
  it('META-05 drops a staged person when navigating away without creating it', async () => {
    const wrapper = await mountDrawer();
    wrapper.vm.newPerson = { displayName: 'Actor A', originalName: '' };
    wrapper.vm.createAndSelectPerson();
    expect(api.CreatePerson).not.toHaveBeenCalled();
    expect(wrapper.vm.pendingPeople.map(item => item.displayName)).toEqual(['Actor A']);

    await wrapper.vm.openVideo(2);
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ title: '放弃未保存的修改' }));
    expect(wrapper.vm.currentEntry).toEqual({ type: 'video', id: 2 });
    expect(wrapper.vm.pendingPeople).toEqual([]);
    expect(api.CreatePerson).not.toHaveBeenCalled();
  });

  it('META-05 creates staged people only once on save and associates them', async () => {
    const wrapper = await mountDrawer();
    const pendingCreate = deferred();
    api.CreatePerson.mockReturnValueOnce(pendingCreate.promise);
    api.UpdateVideoDetails.mockResolvedValueOnce(videoDetails(1, { people: [{ person: { id: 10, display_name: 'Only Once' }, active_video_count: 1, active_image_count: 0 }] }));
    wrapper.vm.newPerson = { displayName: 'Only Once', originalName: '' };
    wrapper.vm.createAndSelectPerson();
    await flushPromises();
    expect(wrapper.find('[data-test="drawer-pending-person-1"]').text()).toContain('保存后新建');

    const firstSave = wrapper.vm.saveVideoDetails();
    const secondSave = wrapper.vm.saveVideoDetails();
    expect(wrapper.vm.creatingPerson).toBe(true);
    expect(api.CreatePerson).toHaveBeenCalledOnce();
    expect(api.CreatePerson).toHaveBeenCalledWith('Only Once', '');
    pendingCreate.resolve({ id: 10, display_name: 'Only Once', original_name: '' });
    await Promise.all([firstSave, secondSave]);
    await flushPromises();

    expect(api.UpdateVideoDetails).toHaveBeenCalledOnce();
    expect(api.UpdateVideoDetails).toHaveBeenCalledWith(expect.objectContaining({ video_id: 1, person_ids: [10] }));
    expect(wrapper.vm.creatingPerson).toBe(false);
    expect(wrapper.vm.pendingPeople).toEqual([]);
    expect(wrapper.vm.draft.personIDs).toEqual([10]);
  });

  it('META-05 keeps already-created people when a later step fails, so a retry does not duplicate them', async () => {
    const wrapper = await mountDrawer();
    api.CreatePerson.mockResolvedValueOnce({ id: 11, display_name: 'A' }).mockRejectedValueOnce(new Error('person_name_invalid'));
    wrapper.vm.newPerson = { displayName: 'A', originalName: '' };
    wrapper.vm.createAndSelectPerson();
    wrapper.vm.newPerson = { displayName: 'B', originalName: '' };
    wrapper.vm.createAndSelectPerson();

    await wrapper.vm.saveVideoDetails();
    await flushPromises();
    expect(api.UpdateVideoDetails).not.toHaveBeenCalled();
    expect(wrapper.vm.draft.personIDs).toEqual([11]);
    expect(wrapper.vm.pendingPeople.map(item => item.displayName)).toEqual(['B']);
  });

  it('META-05 asks before closing with unsaved changes and closes directly when clean', async () => {
    const wrapper = await mountDrawer();
    await wrapper.get('[data-test="preview-drawer-close"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(wrapper.emitted('close')).toHaveLength(1);

    wrapper.vm.draft.displayTitle = '改过的标题';
    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="preview-drawer-close"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledTimes(1);
    expect(wrapper.emitted('close')).toHaveLength(1);

    await wrapper.get('[data-test="preview-drawer-close"]').trigger('click');
    await flushPromises();
    expect(wrapper.emitted('close')).toHaveLength(2);
  });

  it('META-05 confirms before removing the last relation of a person from the video', async () => {
    const details = videoDetails(1, { people: [
      { person: { id: 5, display_name: '只此一部' }, active_video_count: 1, active_image_count: 0 },
      { person: { id: 6, display_name: '还有别的' }, active_video_count: 3, active_image_count: 1 },
    ] });
    const wrapper = await mountDrawer(details);

    await wrapper.get('[data-test="drawer-person-remove-6"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(wrapper.vm.draft.personIDs).toEqual([5]);

    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="drawer-person-remove-5"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction.mock.calls[0][0].message).toContain('最后一个活跃关联媒体');
    expect(wrapper.vm.draft.personIDs).toEqual([5]);

    await wrapper.get('[data-test="drawer-person-remove-5"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.draft.personIDs).toEqual([]);
  });

  // D-PC47：评分改完即保存，不经「保存作品信息」。
  it('saves the rating immediately through UpdateVideoRating and reports invalid values', async () => {
    const wrapper = await mountDrawer();
    api.UpdateVideoRating.mockResolvedValueOnce({ id: 1, personal_rating: 7.5 });
    const input = wrapper.get('[data-test="detail-rating-input"]');
    await input.setValue('7.5');
    await input.trigger('change');
    await flushPromises();
    expect(api.UpdateVideoRating).toHaveBeenCalledWith(1, 7.5);
    expect(api.UpdateVideoDetails).not.toHaveBeenCalled();
    expect(wrapper.vm.details.video.personal_rating).toBe(7.5);
    expect(wrapper.emitted('details-updated').at(-1)[0].video.personal_rating).toBe(7.5);
    // 保存过的评分不算未保存的修改。
    expect(wrapper.vm.hasUnsavedVideoDraft).toBe(false);

    await input.setValue('7.3');
    await input.trigger('change');
    await flushPromises();
    expect(api.UpdateVideoRating).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="detail-rating-error"]').text()).toContain('0.5 倍数');

    api.UpdateVideoRating.mockResolvedValueOnce({ id: 1, personal_rating: null });
    await input.setValue('');
    await input.trigger('change');
    await flushPromises();
    expect(api.UpdateVideoRating).toHaveBeenLastCalledWith(1, null);
    expect(wrapper.vm.details.video.personal_rating).toBe(null);
  });

  it('shows the people section with the same name as the tag category', async () => {
    const wrapper = await mountDrawer();
    expect(wrapper.findAll('.detail-section__heading h4').map(heading => heading.text())).toContain('人物');
    expect(wrapper.findAll('.detail-section__heading h4').map(heading => heading.text())).not.toContain('演员');
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

// ===== P-037：抽屉动作条、代理排位与自动切换、内嵌报错、观看会话 =====

// 捕获抽屉订阅的 playback-proxy-state 回调，模拟后端推送。
function installRuntime() {
  const handlers = {};
  window.runtime = { EventsOn: vi.fn((name, fn) => { handlers[name] = fn; return () => { delete handlers[name]; }; }) };
  return handlers;
}

const inlineSession = (id, extra = {}) => ({ video_id: id, mode: 'inline', inline_source: { locator_value: `/preview/video/${id}`, mime: 'video/mp4' }, ...extra });

async function mountInline(props = {}, details = videoDetails(1)) {
  api.GetVideoDetails.mockImplementation(id => Promise.resolve(id === details.video.id ? details : videoDetails(id)));
  api.ListCollections.mockResolvedValue([]);
  const wrapper = mount(PreviewDrawer, { props: { video: details.video, session: inlineSession(details.video.id), ...props } });
  await flushPromises();
  return wrapper;
}

// jsdom 的 <video> 不会真的播放：把播放器状态接到一个可改的对象上。
function fakePlayer(element, { duration = Number.NaN } = {}) {
  const state = { currentTime: 0, duration, paused: true, seeking: false, playbackRate: 1, readyState: 4 };
  for (const key of Object.keys(state)) {
    Object.defineProperty(element, key, { configurable: true, get: () => state[key], set: value => { state[key] = value; } });
  }
  return state;
}

// 以 250ms 一次的 timeupdate 往前播 seconds 秒。clock 是 Date.now 的假时钟。
async function playThrough(video, player, clock, seconds, from = player.currentTime) {
  const steps = Math.round(seconds / 0.25);
  for (let step = 1; step <= steps; step += 1) {
    player.currentTime = from + step * 0.25;
    clock.now += 250;
    await video.trigger('timeupdate');
  }
}

afterEach(() => {
  delete window.runtime;
  vi.restoreAllMocks();
});

describe('P-037 抽屉代理排位、进度与自动切换（D-PC26）', () => {
  it('PLAY-04 入队后显示「排队中（第 N 个）」与「生成中」，本项完成后回读代理并请求刷新根条目的预览会话', async () => {
    const handlers = installRuntime();
    api.CreatePlaybackProxy.mockResolvedValueOnce({ running: true, queued: 2, queued_video_ids: [9, 1], current_video_id: 5, results: [] });
    api.GetPlaybackProxy
      .mockResolvedValueOnce(null)
      .mockResolvedValue({ video_id: 1, strategy: 'remux', status: 'ready', output_size: 1024, last_error: '' });
    const wrapper = await mountDrawer();
    await wrapper.get('[data-test="preview-proxy-create"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="preview-proxy-status"]').text()).toContain('排队中（第 2 个）');
    expect(wrapper.get('[data-test="preview-proxy-create"]').text()).toBe('排队中...');
    expect(wrapper.get('[data-test="preview-proxy-create"]').attributes('disabled')).toBeDefined();
    expect(wrapper.emitted('preview-session-stale')).toBeUndefined();
    expect(wrapper.find('[data-test="preview-proxy-error"]').exists()).toBe(false);

    handlers['playback-proxy-state']({ running: true, queued: 0, queued_video_ids: [], current_video_id: 1, results: [{ video_id: 9, code: 'created' }] });
    await flushPromises();
    expect(wrapper.get('[data-test="preview-proxy-status"]').text()).toContain('生成中');
    expect(wrapper.emitted('preview-session-stale')).toBeUndefined();

    handlers['playback-proxy-state']({ running: false, completed: true, queued: 0, queued_video_ids: [], current_video_id: 0, results: [{ video_id: 9, code: 'created' }, { video_id: 1, code: 'created', strategy: 'remux' }] });
    await flushPromises();
    expect(wrapper.emitted('preview-session-stale')).toEqual([[1]]);
    expect(wrapper.vm.proxyTracking).toBeNull();
    expect(wrapper.get('[data-test="preview-proxy-status"]').text()).toContain('已有播放代理');
    wrapper.unmount();
    expect(handlers['playback-proxy-state']).toBeUndefined();
  });

  it('PLAY-04 「进行中」显示为「生成中」，不再当失败文案', async () => {
    installRuntime();
    api.CreatePlaybackProxy.mockResolvedValueOnce({ running: true, queued: 0, queued_video_ids: [], current_video_id: 1, results: [{ video_id: 1, code: 'in_progress' }] });
    const wrapper = await mountDrawer();
    await wrapper.get('[data-test="preview-proxy-create"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="preview-proxy-status"]').text()).toContain('生成中');
    expect(wrapper.find('[data-test="preview-proxy-error"]').exists()).toBe(false);
    expect(wrapper.emitted('preview-session-stale')).toBeUndefined();
  });

  it('PLAY-04 打开时这一项已被别处排进队列：读当前快照补上排位', async () => {
    installRuntime();
    api.GetPlaybackProxyStatus.mockResolvedValueOnce({ running: true, queued: 3, queued_video_ids: [4, 5, 1], current_video_id: 3, results: [] });
    const wrapper = await mountDrawer();
    expect(api.GetPlaybackProxyStatus).toHaveBeenCalled();
    expect(wrapper.get('[data-test="preview-proxy-status"]').text()).toContain('排队中（第 3 个）');
  });

  it('PLAY-04 本轮里别的时候的旧结果不触发切换，也不回读', async () => {
    const handlers = installRuntime();
    const wrapper = await mountDrawer();
    const reads = api.GetPlaybackProxy.mock.calls.length;
    handlers['playback-proxy-state']({ running: false, completed: true, queued: 0, queued_video_ids: [], current_video_id: 0, results: [{ video_id: 1, code: 'created' }] });
    await flushPromises();
    expect(wrapper.emitted('preview-session-stale')).toBeUndefined();
    expect(api.GetPlaybackProxy.mock.calls.length).toBe(reads);
  });

  it('PLAY-04 入队请求还没返回时，事件里没有这一项不算完成', async () => {
    const handlers = installRuntime();
    const pending = deferred();
    api.CreatePlaybackProxy.mockReturnValueOnce(pending.promise);
    const wrapper = await mountDrawer();
    await wrapper.get('[data-test="preview-proxy-create"]').trigger('click');
    handlers['playback-proxy-state']({ running: false, completed: true, queued: 0, queued_video_ids: [], current_video_id: 0, results: [{ video_id: 1, code: 'created' }] });
    await flushPromises();
    expect(wrapper.vm.proxyTracking?.state.phase).toBe('submitting');
    expect(wrapper.get('[data-test="preview-proxy-status"]').text()).toContain('正在加入队列');
    pending.resolve({ running: true, queued: 1, queued_video_ids: [1], current_video_id: 8, results: [] });
    await flushPromises();
    expect(wrapper.get('[data-test="preview-proxy-status"]').text()).toContain('排队中（第 1 个）');
    expect(wrapper.emitted('preview-session-stale')).toBeUndefined();
  });

  it('PLAY-04 入队回执里没有这一项（也不在跑）时当场收尾，不会一直停在「正在加入队列」', async () => {
    installRuntime();
    api.CreatePlaybackProxy.mockResolvedValueOnce(undefined);
    const wrapper = await mountDrawer();
    const reads = api.GetPlaybackProxy.mock.calls.length;
    await wrapper.get('[data-test="preview-proxy-create"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.proxyTracking).toBeNull();
    expect(api.GetPlaybackProxy.mock.calls.length).toBe(reads + 1);
    expect(wrapper.get('[data-test="preview-proxy-create"]').text()).toBe('生成播放代理');
  });

  it('PLAY-04 本项失败时翻成人话并停止等待', async () => {
    const handlers = installRuntime();
    api.CreatePlaybackProxy.mockResolvedValueOnce({ running: true, queued: 1, queued_video_ids: [1], current_video_id: 3, results: [] });
    const wrapper = await mountDrawer();
    await wrapper.get('[data-test="preview-proxy-create"]').trigger('click');
    await flushPromises();
    handlers['playback-proxy-state']({ running: false, completed: true, queued: 0, queued_video_ids: [], current_video_id: 0, results: [{ video_id: 1, code: 'disk_full' }] });
    await flushPromises();
    expect(wrapper.get('[data-test="preview-proxy-error"]').text()).toBe('生成播放代理失败：磁盘空间不足');
    expect(wrapper.vm.proxyTracking).toBeNull();
    expect(wrapper.emitted('preview-session-stale')).toBeUndefined();
    expect(wrapper.get('[data-test="preview-proxy-create"]').attributes('disabled')).toBeUndefined();
  });

  it('PLAY-04 嵌套条目完成后抽屉自己重取预览会话，切到内嵌播放', async () => {
    installRuntime();
    api.GetVideoDetails.mockImplementation(id => Promise.resolve(videoDetails(id)));
    api.ListCollections.mockResolvedValue([]);
    api.GetPreviewSession
      .mockResolvedValueOnce({ video_id: 2, mode: 'external-preview', reason_message: '当前格式不支持内嵌预览', external_action: { button_label: '用系统播放器预览' } })
      .mockResolvedValueOnce(inlineSession(2, { proxy: { strategy: 'remux', size: 2048 } }));
    api.CreatePlaybackProxy.mockResolvedValueOnce({ running: false, completed: true, queued: 0, queued_video_ids: [], current_video_id: 0, results: [{ video_id: 2, code: 'created', strategy: 'remux' }] });
    api.GetPlaybackProxy.mockResolvedValue({ video_id: 2, strategy: 'remux', status: 'ready', output_size: 2048, last_error: '' });
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'video', id: 2 } } });
    await flushPromises();
    expect(wrapper.find('video').exists()).toBe(false);
    await wrapper.get('[data-test="preview-proxy-create"]').trigger('click');
    await flushPromises();
    expect(api.GetPreviewSession).toHaveBeenCalledTimes(2);
    expect(wrapper.get('.preview-drawer__video source').attributes('src')).toBe('/preview/video/2');
    expect(wrapper.vm.playingThroughProxy).toBe(true);
  });
});

describe('P-037 内嵌播放器报错的回退（D-PC26）', () => {
  it('PLAY-05 解码失败时显示「无法内嵌播放此文件」，给系统播放器与生成代理两个出口；换会话后恢复播放器', async () => {
    installRuntime();
    const wrapper = await mountInline();
    const video = wrapper.get('video');
    Object.defineProperty(video.element, 'error', { configurable: true, get: () => ({ code: 3 }) });
    await video.trigger('error');
    expect(wrapper.find('video').exists()).toBe(false);
    const fallback = wrapper.get('[data-test="preview-inline-error"]');
    expect(fallback.text()).toContain('无法内嵌播放此文件');
    await wrapper.get('[data-test="preview-inline-error-external"]').trigger('click');
    expect(wrapper.emitted('preview-externally')[0][0]).toEqual(expect.objectContaining({ id: 1 }));

    api.CreatePlaybackProxy.mockResolvedValueOnce({ running: true, queued: 1, queued_video_ids: [1], current_video_id: 7, results: [] });
    await wrapper.get('[data-test="preview-inline-error-proxy"]').trigger('click');
    await flushPromises();
    expect(api.CreatePlaybackProxy).toHaveBeenCalledWith(1);
    expect(wrapper.get('[data-test="preview-inline-error-progress"]').text()).toBe('排队中（第 1 个）');

    await wrapper.setProps({ session: inlineSession(1, { proxy: { strategy: 'remux', size: 10 } }) });
    expect(wrapper.find('video').exists()).toBe(true);
    expect(wrapper.find('[data-test="preview-inline-error"]').exists()).toBe(false);
  });

  it('PLAY-05 <source> 报错只在它还带着地址时算数（换会话清地址再 load 的那一次不算）', async () => {
    const wrapper = await mountInline();
    const source = wrapper.get('video source');
    source.element.removeAttribute('src');
    await source.trigger('error');
    expect(wrapper.find('[data-test="preview-inline-error"]').exists()).toBe(false);
    source.element.setAttribute('src', '/preview/video/1');
    await source.trigger('error');
    expect(wrapper.find('[data-test="preview-inline-error"]').exists()).toBe(true);
  });

  it('PLAY-05 <video> 没有 MediaError 的 error 事件不算失败', async () => {
    const wrapper = await mountInline();
    await wrapper.get('video').trigger('error');
    expect(wrapper.find('video').exists()).toBe(true);
  });

  it('PLAY-05 已经在经代理播放时报错，只给系统播放器', async () => {
    const wrapper = await mountInline({ session: inlineSession(1, { proxy: { strategy: 'transcode', size: 10 } }) });
    const video = wrapper.get('video');
    Object.defineProperty(video.element, 'error', { configurable: true, get: () => ({ code: 4 }) });
    await video.trigger('error');
    expect(wrapper.find('[data-test="preview-inline-error-external"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="preview-inline-error-proxy"]').exists()).toBe(false);
  });
});

describe('P-037 抽屉动作条（D-PC47）', () => {
  it('PLAY-14 收藏、点赞、已看直接写库，拿回的整条视频写回详情并通知宿主', async () => {
    const wrapper = await mountDrawer();
    expect(wrapper.find('[data-test="drawer-action-bar"]').exists()).toBe(true);
    const cases = [
      { test: 'drawer-action-favorite', call: api.SetVideoFavorite, field: 'is_favorite' },
      { test: 'drawer-action-like', call: api.SetVideoLiked, field: 'is_liked' },
      { test: 'drawer-action-watched', call: api.SetVideoWatched, field: 'is_watched' }
    ];
    for (const { test, call, field } of cases) {
      call.mockImplementationOnce((id, value) => Promise.resolve({ ...wrapper.vm.details.video, id, [field]: value }));
      expect(wrapper.get(`[data-test="${test}"]`).attributes('aria-pressed')).toBe('false');
      await wrapper.get(`[data-test="${test}"]`).trigger('click');
      await flushPromises();
      expect(call).toHaveBeenCalledWith(1, true);
      expect(wrapper.get(`[data-test="${test}"]`).attributes('aria-pressed')).toBe('true');
      expect(wrapper.emitted('details-updated').at(-1)[0].video[field]).toBe(true);
    }
  });

  it('PLAY-14 根条目以宿主传入的行为准：快捷键在宿主改过的状态，动作条跟着变', async () => {
    const details = videoDetails(1);
    const wrapper = await mountDrawer(details);
    await wrapper.setProps({ video: { ...details.video, is_favorite: true, is_watched: true } });
    expect(wrapper.get('[data-test="drawer-action-favorite"]').attributes('aria-pressed')).toBe('true');
    expect(wrapper.get('[data-test="drawer-action-watched"]').attributes('aria-pressed')).toBe('true');
    api.SetVideoFavorite.mockResolvedValueOnce({ ...details.video, is_favorite: false });
    await wrapper.get('[data-test="drawer-action-favorite"]').trigger('click');
    await flushPromises();
    expect(api.SetVideoFavorite).toHaveBeenCalledWith(1, false);
  });

  it('PLAY-14 状态写入失败时就地报错', async () => {
    const wrapper = await mountDrawer();
    api.SetVideoWatched.mockRejectedValueOnce(new Error('数据库维护中'));
    await wrapper.get('[data-test="drawer-action-watched"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="drawer-action-error"]').text()).toBe('更新观看状态失败：Error: 数据库维护中');
    expect(wrapper.get('[data-test="drawer-action-watched"]').attributes('aria-pressed')).toBe('false');
  });

  it('PLAY-14 播放走 PlayVideo：结果交给宿主，失败原因就地显示', async () => {
    const wrapper = await mountDrawer();
    api.PlayVideo.mockResolvedValueOnce({ dispatch_succeeded: false, user_message: '所在磁盘未连接', reason_code: 'offline_root' });
    await wrapper.get('[data-test="drawer-action-play"]').trigger('click');
    await flushPromises();
    expect(api.PlayVideo).toHaveBeenCalledWith(1);
    expect(wrapper.emitted('playback-attempted')[0][0]).toEqual(expect.objectContaining({ reason_code: 'offline_root' }));
    expect(wrapper.get('[data-test="drawer-action-error"]').text()).toBe('所在磁盘未连接');

    api.PlayVideo.mockResolvedValueOnce({ dispatch_succeeded: true });
    await wrapper.get('[data-test="drawer-action-play"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="drawer-action-error"]').exists()).toBe(false);
    expect(wrapper.emitted('playback-attempted')).toHaveLength(2);
  });

  it('PLAY-14 星级评分 0.5 步进，点一下即保存，可清除', async () => {
    const wrapper = await mountDrawer();
    expect(wrapper.get('[data-test="drawer-stars-value"]').text()).toBe('未评分');
    expect(wrapper.findAll('.drawer-star__half')).toHaveLength(20);
    api.UpdateVideoRating.mockResolvedValueOnce({ id: 1, personal_rating: 7.5 });
    await wrapper.get('[data-test="drawer-star-7.5"]').trigger('click');
    await flushPromises();
    expect(api.UpdateVideoRating).toHaveBeenCalledWith(1, 7.5);
    expect(api.UpdateVideoDetails).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="drawer-stars-value"]').text()).toBe('7.5 / 10');
    expect(wrapper.findAll('.drawer-star--full')).toHaveLength(7);
    expect(wrapper.findAll('.drawer-star--half')).toHaveLength(1);
    expect(wrapper.get('[data-test="drawer-star-7.5"]').attributes('aria-pressed')).toBe('true');
    expect(wrapper.get('[data-test="detail-rating-input"]').element.value).toBe('7.5');

    api.UpdateVideoRating.mockResolvedValueOnce({ id: 1, personal_rating: null });
    await wrapper.get('[data-test="drawer-stars-clear"]').trigger('click');
    await flushPromises();
    expect(api.UpdateVideoRating).toHaveBeenLastCalledWith(1, null);
    expect(wrapper.get('[data-test="drawer-stars-value"]').text()).toBe('未评分');
    expect(wrapper.find('[data-test="drawer-stars-clear"]').exists()).toBe(false);
  });
});

describe('P-037 内嵌观看会话与 watch-progress 载荷（D-PC42、D-PC43）', () => {
  it('PLAY-07 累计播放首次越过阈值时记一次 inline_view，同一会话不再记', async () => {
    const clock = { now: 0 };
    vi.spyOn(Date, 'now').mockImplementation(() => clock.now);
    const wrapper = await mountInline();
    const video = wrapper.get('video');
    const player = fakePlayer(video.element, { duration: 60 });
    player.paused = false;
    await video.trigger('play');
    await playThrough(video, player, clock, 29.75);
    expect(api.RecordViewEvent).not.toHaveBeenCalled();
    await playThrough(video, player, clock, 0.5);
    await flushPromises();
    expect(api.RecordViewEvent).toHaveBeenCalledTimes(1);
    const [videoID, source, sessionID] = api.RecordViewEvent.mock.calls[0];
    expect(videoID).toBe(1);
    expect(source).toBe('inline_view');
    expect(sessionID).toMatch(/^[0-9a-f]{32}$/);
    await playThrough(video, player, clock, 10);
    await video.trigger('ended');
    await flushPromises();
    expect(api.RecordViewEvent).toHaveBeenCalledTimes(1);
  });

  it('PLAY-07 拖动与跳转不算播放时长', async () => {
    const clock = { now: 0 };
    vi.spyOn(Date, 'now').mockImplementation(() => clock.now);
    const wrapper = await mountInline();
    const video = wrapper.get('video');
    const player = fakePlayer(video.element, { duration: 600 });
    player.paused = false;
    await video.trigger('play');
    await playThrough(video, player, clock, 10);
    await video.trigger('seeking');
    player.currentTime = 400;
    clock.now += 250;
    await video.trigger('timeupdate');
    await playThrough(video, player, clock, 10);
    await flushPromises();
    expect(api.RecordViewEvent).not.toHaveBeenCalled();
  });

  it('PLAY-07 播到结尾即便没够阈值也记一次；换一次会话会生成新的会话标识', async () => {
    const clock = { now: 0 };
    vi.spyOn(Date, 'now').mockImplementation(() => clock.now);
    const wrapper = await mountInline();
    let video = wrapper.get('video');
    let player = fakePlayer(video.element, { duration: 600 });
    player.paused = false;
    await video.trigger('play');
    await playThrough(video, player, clock, 5, 590);
    await video.trigger('ended');
    await flushPromises();
    expect(api.RecordViewEvent).toHaveBeenCalledTimes(1);
    expect(wrapper.emitted('watch-progress').at(-1)[0]).toEqual(expect.objectContaining({ videoID: 1, completed: true }));

    await wrapper.setProps({ session: inlineSession(1, { proxy: { strategy: 'remux', size: 10 } }) });
    await flushPromises();
    video = wrapper.get('video');
    player = fakePlayer(video.element, { duration: 600 });
    player.paused = false;
    await video.trigger('play');
    await video.trigger('ended');
    await flushPromises();
    expect(api.RecordViewEvent).toHaveBeenCalledTimes(2);
    expect(api.RecordViewEvent.mock.calls[1][2]).not.toBe(api.RecordViewEvent.mock.calls[0][2]);
  });

  it('PLAY-07 暂停在片尾区间（判定看完）也记一次', async () => {
    const wrapper = await mountInline();
    const video = wrapper.get('video');
    const player = fakePlayer(video.element, { duration: 7200 });
    player.paused = false;
    await video.trigger('play');
    player.currentTime = 7050;
    await video.trigger('pause');
    await flushPromises();
    expect(api.RecordViewEvent).toHaveBeenCalledTimes(1);
  });

  it('PLAY-10 字幕命中打开为 jump，并带上播放器读到的时长', async () => {
    const wrapper = await mountInline({ startTimeMs: 4200 });
    const video = wrapper.get('video');
    const player = fakePlayer(video.element, { duration: 120 });
    player.paused = false;
    await video.trigger('play');
    player.currentTime = 12;
    await video.trigger('pause');
    expect(wrapper.emitted('watch-progress').at(-1)[0]).toEqual({ videoID: 1, positionSeconds: 12, completed: false, origin: 'jump', durationSeconds: 120 });
  });

  it('PLAY-10 从断点起播为 resume、从头为 start；时长未知传 0', async () => {
    const resumed = await mountInline({ resumePositionSeconds: 30 });
    let video = resumed.get('video');
    let player = fakePlayer(video.element);
    player.paused = false;
    await video.trigger('play');
    player.currentTime = 40;
    await video.trigger('pause');
    expect(resumed.emitted('watch-progress').at(-1)[0]).toEqual(expect.objectContaining({ origin: 'resume', durationSeconds: 0 }));
    resumed.unmount();

    const fresh = await mountInline();
    video = fresh.get('video');
    player = fakePlayer(video.element);
    player.paused = false;
    await video.trigger('play');
    player.currentTime = 3;
    await video.trigger('pause');
    expect(fresh.emitted('watch-progress').at(-1)[0]).toEqual(expect.objectContaining({ origin: 'start', durationSeconds: 0 }));
  });

  it('PLAY-10 抽屉开着时又从字幕命中跳了一次：这一场之后按 jump 上报', async () => {
    const wrapper = await mountInline({ resumePositionSeconds: 30 });
    const video = wrapper.get('video');
    const player = fakePlayer(video.element);
    player.paused = false;
    await video.trigger('play');
    await wrapper.setProps({ startTimeMs: 9000 });
    player.currentTime = 9;
    await video.trigger('pause');
    expect(wrapper.emitted('watch-progress').at(-1)[0].origin).toBe('jump');
  });

  it('PLAY-10 嵌套条目的续播用 watchState：标已看之前的旧断点从头播，重看中的断点续播', async () => {
    const base = '2026-09-30T12:00:00+08:00';
    const watchedBefore = videoDetails(2, { video: { id: 2, duration: 7200, watch_position_seconds: 600, is_watched: true, watched_at: base, watch_progress_updated_at: '2026-09-30T11:00:00+08:00' } });
    api.GetVideoDetails.mockResolvedValue(watchedBefore);
    api.ListCollections.mockResolvedValue([]);
    api.GetPreviewSession.mockResolvedValue({ video_id: 2, mode: 'unavailable', reason_message: '不可预览' });
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'video', id: 2 } } });
    await flushPromises();
    expect(wrapper.vm.playbackStartOptions().nestedResumePositionSeconds).toBe(0);

    wrapper.vm.details = videoDetails(2, { video: { id: 2, duration: 7200, watch_position_seconds: 600, is_watched: true, watched_at: base, watch_progress_updated_at: '2026-09-30T13:00:00+08:00' } });
    expect(wrapper.vm.playbackStartOptions().nestedResumePositionSeconds).toBe(600);
  });
});

describe('P-037 人物媒体删除撤销后的刷新', () => {
  it('LIB-12 撤销或从回收站恢复（restored）后重读人物详情，并通知宿主刷新它的列表', async () => {
    api.GetPersonDetail.mockResolvedValue({
      person: { person: { id: 7, display_name: '人物' }, active_video_count: 1, active_image_count: 0 },
      videos: [{ id: 1, name: 'clip.mp4' }], images: []
    });
    const w = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } }, global: { stubs: { teleport: true } } });
    await flushPromises();
    expect(api.GetPersonDetail).toHaveBeenCalledTimes(1);
    w.vm.personMediaDeleteTarget = { kind: 'video', media: { id: 1, name: 'clip.mp4', size: 1 }, personID: 7 };
    await flushPromises();
    w.findComponent({ name: 'PersonMediaDeleteDialog' }).vm.$emit('restored', { kind: 'video', ids: [1] });
    await flushPromises();
    expect(api.GetPersonDetail).toHaveBeenCalledTimes(2);
    expect(w.emitted('media-restored')).toEqual([[{ kind: 'video', ids: [1] }]]);
  });
});


describe('person avatar from library images', () => {
  const person = { person: { person: { id: 7, display_name: 'Person' }, avatar_url: '' }, videos: [], images: [{ id: 12, name: 'portrait', path: '/portrait.png' }] };
  it('sets a linked image as the avatar and refreshes the detail', async () => {
    api.GetPersonDetail.mockResolvedValue(person); api.SetPersonAvatar.mockResolvedValue({});
    const w = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } } }); await flushPromises();
    await w.find('[data-test="person-image-avatar-12"]').trigger('click'); await flushPromises();
    expect(api.SetPersonAvatar).toHaveBeenCalledWith(7, '/portrait.png'); expect(api.GetPersonDetail).toHaveBeenCalledTimes(2); w.unmount();
  });
  it('keeps picker on failure and does not refresh a different person', async () => {
    api.GetPersonDetail.mockResolvedValue(person); api.SearchImagePage.mockResolvedValue({ images: person.images });
    api.SetPersonAvatar.mockRejectedValueOnce(new Error('unsupported'));
    const w = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } }, global: { stubs: { teleport: true } } }); await flushPromises();
    w.vm.avatarPickerOpen = true; await flushPromises(); await w.vm.setAvatarFromImage(person.images[0]);
    expect(w.vm.avatarPickerOpen).toBe(true); expect(w.vm.avatarError).toContain('unsupported');
    const pending = deferred(); api.SetPersonAvatar.mockReturnValueOnce(pending.promise);
    const saving = w.vm.setAvatarFromImage(person.images[0]); w.vm.currentEntry = { type: 'person', id: 8 }; pending.resolve({}); await saving;
    expect(api.SetPersonAvatar).toHaveBeenLastCalledWith(7, '/portrait.png'); expect(api.GetPersonDetail).toHaveBeenCalledTimes(1); w.unmount();
  });
});
