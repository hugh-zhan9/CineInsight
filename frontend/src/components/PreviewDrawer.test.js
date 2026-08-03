import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  AddCollectionVideo: vi.fn(),
  AddCollectionVideos: vi.fn(),
  AddPersonVideo: vi.fn(),
  AddPersonVideos: vi.fn(),
  CreatePerson: vi.fn(),
  DeleteCollection: vi.fn(),
  GetCollectionDetail: vi.fn(),
  GetAllDirectories: vi.fn(),
  GetPersonDetail: vi.fn(),
  GetPreviewSession: vi.fn(),
  GetVideoDetails: vi.fn(),
  ListCollections: vi.fn(),
  ListPeople: vi.fn(),
  RefreshVideoTechnicalMetadata: vi.fn(),
  RemoveCollectionCover: vi.fn(),
  RemoveCollectionVideo: vi.fn(),
  RemovePersonAvatar: vi.fn(),
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
  UpdateVideoDetails: vi.fn()
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
});

describe('PreviewDrawer', () => {
  it('mounts and loads the root video details', async () => {
    const wrapper = await mountDrawer();

    expect(api.GetVideoDetails).toHaveBeenCalledWith(1);
    expect(wrapper.vm.currentEntry).toEqual({ type: 'video', id: 1 });
    expect(wrapper.vm.details.video.id).toBe(1);
    expect(wrapper.text()).toContain('Video 1');
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
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    const wrapper = mount(PreviewDrawer, { props: { initialEntity: { type: 'person', id: 7 } } });
    await flushPromises();

    await wrapper.vm.removeRelatedVideo(onlyVideo);
    expect(api.RemovePersonVideo).not.toHaveBeenCalled();
    await wrapper.vm.removeRelatedVideo(onlyVideo);

    expect(confirm).toHaveBeenCalledTimes(2);
    expect(api.RemovePersonVideo).toHaveBeenCalledWith(7, 1);
    expect(wrapper.emitted('person-deleted')).toEqual([[7]]);
    expect(wrapper.emitted('close')).toHaveLength(1);
    confirm.mockRestore();
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
