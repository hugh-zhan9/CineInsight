import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  CreateCollection: vi.fn(),
  CreatePerson: vi.fn(),
  GetCollectionDetail: vi.fn(),
  GetPersonDetail: vi.fn(),
  ListCollections: vi.fn(),
  ListPeople: vi.fn(),
  OpenDirectory: vi.fn(),
  PlayVideo: vi.fn(),
  PreviewExternally: vi.fn(),
  UpdateVideoWatchProgress: vi.fn()
}));

vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./PreviewDrawer.vue', () => ({
  default: {
    name: 'PreviewDrawer',
    props: ['initialEntity'],
    template: '<aside class="preview-drawer-stub">{{ initialEntity.type }}:{{ initialEntity.id }}</aside>'
  }
}));

import EntityLibraryPage from './EntityLibraryPage.vue';

beforeEach(() => {
  vi.clearAllMocks();
  api.ListPeople.mockResolvedValue([]);
  api.ListCollections.mockResolvedValue([]);
  api.GetPersonDetail.mockResolvedValue({
    person: { person: { id: 7, display_name: 'Actor Seven', original_name: 'Seven' }, avatar_url: '', active_video_count: 0 },
    videos: [],
    next_video_id: 0
  });
  api.GetCollectionDetail.mockResolvedValue({
    collection: { collection: { id: 5, name: 'Saga', description: 'Local set' }, cover_url: '', active_video_count: 0 },
    videos: []
  });
  api.PlayVideo.mockResolvedValue({ dispatch_succeeded: true });
});

describe('EntityLibraryPage', () => {
  it('loads people and opens the selected entity drawer', async () => {
    api.ListPeople.mockResolvedValueOnce([{
      person: { id: 7, display_name: 'Actor Seven', original_name: 'Seven' },
      avatar_url: '',
      active_video_count: 2,
      cursor_name: 'actor seven'
    }]);
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();

    expect(api.ListPeople).toHaveBeenCalledWith('', '', 0, 50);
    expect(wrapper.text()).toContain('Actor Seven');
    await wrapper.get('.entity-card').trigger('click');

    expect(wrapper.vm.selectedEntity).toEqual({ type: 'person', id: 7 });
    expect(wrapper.get('.preview-drawer-stub').text()).toBe('person:7');
    expect(api.GetPersonDetail).toHaveBeenCalledWith(7, 0, 30);
  });

  it('creates a collection, reloads the list, and opens its drawer', async () => {
    api.CreateCollection.mockResolvedValueOnce({ id: 5, name: 'Saga' });
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'collection' } });
    await flushPromises();
    wrapper.vm.createForm = { name: 'Saga', secondary: 'Local set' };

    await wrapper.vm.createEntity();
    await flushPromises();

    expect(api.CreateCollection).toHaveBeenCalledWith('Saga', 'Local set');
    expect(api.ListCollections).toHaveBeenCalledTimes(2);
    expect(wrapper.vm.selectedEntity).toEqual({ type: 'collection', id: 5 });
    expect(wrapper.get('.preview-drawer-stub').text()).toBe('collection:5');
  });

  it('loads additional related person videos and exposes playback actions', async () => {
    api.ListPeople.mockResolvedValueOnce([{
      person: { id: 7, display_name: 'Actor Seven', original_name: '' },
      avatar_url: '', active_video_count: 2, cursor_name: 'actor seven'
    }]);
    api.GetPersonDetail
      .mockResolvedValueOnce({
        person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 2 },
        videos: [{ id: 2, name: 'two.mp4', size: 1024, duration: 60 }], next_video_id: 2
      })
      .mockResolvedValueOnce({
        person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 2 },
        videos: [{ id: 1, name: 'one.mp4', size: 2048, duration: 120 }], next_video_id: 0
      });
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();
    await wrapper.get('.entity-card').trigger('click');
    await flushPromises();

    expect(wrapper.findAll('.entity-video-card')).toHaveLength(1);
    await wrapper.vm.loadEntityVideos(false);
    await wrapper.vm.$nextTick();
    expect(wrapper.findAll('.entity-video-card')).toHaveLength(2);

    await wrapper.find('.entity-video-card__actions .btn-primary').trigger('click');
    expect(api.PlayVideo).toHaveBeenCalledWith(2);
  });
});
