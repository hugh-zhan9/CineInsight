import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  CreateCollection: vi.fn(),
  CreatePerson: vi.fn(),
  ListCollections: vi.fn(),
  ListPeople: vi.fn(),
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
});
