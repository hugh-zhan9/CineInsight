import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ PreviewLocalMetadataBatch: vi.fn(), ApplyLocalMetadataBatch: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import LocalMetadataDialog from './LocalMetadataDialog.vue';

const scalar = (field, currentValue, sourceValue, changeType, defaultSelected = false, requiresOverwrite = false) => ({
  field, current_value: currentValue, source_value: sourceValue, change_type: changeType,
  default_selected: defaultSelected, requires_overwrite: requiresOverwrite
});

const fixture = () => ({
  requested: 1,
  failures: [],
  diffs: [{
    video_id: 7, manifest_sha256: 'manifest', current_sha256: 'current', status: 'update_available', warnings: [],
    title: scalar('title', 'Manual', 'Imported', 'overwrite', false, true),
    original_title: scalar('original_title', '', 'Original', 'fill', true, false),
    description: scalar('description', '', '', 'none'),
    people: {
      field: 'people', current: [], change_type: 'fill', default_selected: true, requires_overwrite: false,
      source: [{ source_name: 'Alex', normalized_name: 'alex', matches: [{ id: 2, name: 'Alex A' }, { id: 3, name: 'Alex B' }], default_mode: '', default_entity_id: 0 }]
    },
    collection: { field: 'collection', current: [], source: [], change_type: 'none', default_selected: false, requires_overwrite: false },
    poster: { field: 'poster', has_current: false, source_name: '', change_type: 'none', default_selected: false, requires_overwrite: false },
    fanart: { field: 'fanart', has_current: false, source_name: '', change_type: 'none', default_selected: false, requires_overwrite: false }
  }]
});

async function mountDialog() {
  api.PreviewLocalMetadataBatch.mockResolvedValue(fixture());
  const wrapper = mount(LocalMetadataDialog, { props: { visible: true, videoIds: [7] } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => vi.clearAllMocks());

describe('LocalMetadataDialog', () => {
  it('defaults empty fields on and leaves ambiguous mappings unresolved', async () => {
    const wrapper = await mountDialog();
    const form = wrapper.vm.forms[0];

    expect(form.selected.original_title).toBe(true);
    expect(form.selected.title).toBe(false);
    expect(form.resolutions.people.alex).toBe('');
    expect(wrapper.text()).toContain('Alex A');
    expect(wrapper.text()).toContain('Alex B');
  });

  it('requires explicit overwrite confirmation before calling the backend', async () => {
    const wrapper = await mountDialog();
    const form = wrapper.vm.forms[0];
    form.selected.people = false;
    form.selected.original_title = false;
    form.selected.title = true;
    await wrapper.vm.$nextTick();

    await wrapper.find('[data-test="apply-local-metadata"]').trigger('click');
    expect(api.ApplyLocalMetadataBatch).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain('需要确认覆盖');

    form.overwrite.title = true;
    api.ApplyLocalMetadataBatch.mockResolvedValue({ requested: 1, succeeded: 1, failed: 0, results: [], failures: [] });
    await wrapper.find('[data-test="apply-local-metadata"]').trigger('click');
    await flushPromises();
    expect(api.ApplyLocalMetadataBatch).toHaveBeenCalledWith({ requests: [expect.objectContaining({ selected_fields: ['title'], overwrite_fields: ['title'] })] });
  });

  it('submits an explicit ambiguous mapping and reports partial success', async () => {
    const wrapper = await mountDialog();
    const form = wrapper.vm.forms[0];
    form.selected.original_title = false;
    form.resolutions.people.alex = 'existing:3';
    api.ApplyLocalMetadataBatch.mockResolvedValue({ requested: 2, succeeded: 1, failed: 1, results: [], failures: [{ video_id: 8, message: 'conflict' }] });

    await wrapper.find('[data-test="apply-local-metadata"]').trigger('click');
    await flushPromises();

    expect(api.ApplyLocalMetadataBatch).toHaveBeenCalledWith({ requests: [expect.objectContaining({
      people_resolutions: [{ normalized_name: 'alex', mode: 'existing', entity_id: 3 }]
    })] });
    expect(wrapper.emitted('applied')).toHaveLength(1);
  });
});
