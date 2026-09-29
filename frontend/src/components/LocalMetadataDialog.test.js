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
    video_id: 7, video_name: 'Movie.2020.mkv', video_path: '/library/Movie.2020.mkv',
    manifest_sha256: 'manifest', current_sha256: 'current', status: 'update_available', warnings: [],
    title: scalar('title', 'Manual', 'Imported', 'overwrite', false, true),
    original_title: scalar('original_title', '', 'Original', 'fill', true, false),
    description: scalar('description', '', '', 'none'),
    people: {
      field: 'people', current: [], change_type: 'fill', default_selected: true, requires_overwrite: false,
      added: ['Alex'], removed: [], kept: [],
      source: [{ source_name: 'Alex', normalized_name: 'alex', matches: [{ id: 2, name: 'Alex A' }, { id: 3, name: 'Alex B' }], default_mode: '', default_entity_id: 0 }]
    },
    collection: { field: 'collection', current: [], source: [], change_type: 'none', default_selected: false, requires_overwrite: false },
    poster: { field: 'poster', has_current: false, source_name: '', change_type: 'none', default_selected: false, requires_overwrite: false },
    fanart: { field: 'fanart', has_current: false, source_name: '', change_type: 'none', default_selected: false, requires_overwrite: false }
  }],
  people_decisions: [{ source_name: 'Alex', normalized_name: 'alex', matches: [{ id: 2, name: 'Alex A' }, { id: 3, name: 'Alex B' }], default_mode: '', default_entity_id: 0, video_ids: [7] }]
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
    // 人物映射改为批次级决策（META-03），逐视频不再各存一份。
    expect(wrapper.vm.batchResolutions.alex).toBe('');
    expect(form.resolutions.people).toBeUndefined();
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
    wrapper.vm.batchResolutions.alex = 'existing:3';
    api.ApplyLocalMetadataBatch.mockResolvedValue({ requested: 2, succeeded: 1, failed: 1, results: [], failures: [{ video_id: 8, message: 'conflict' }] });

    await wrapper.find('[data-test="apply-local-metadata"]').trigger('click');
    await flushPromises();

    expect(api.ApplyLocalMetadataBatch).toHaveBeenCalledWith({
      requests: [expect.objectContaining({ selected_fields: ['people'], people_resolutions: [] })],
      batch_resolutions: { alex: { normalized_name: 'alex', mode: 'existing', entity_id: 3 } }
    });
    expect(wrapper.emitted('applied')).toHaveLength(1);
  });

  // META-09：导入对话框显示片名，人物差异列出具体姓名。
  it('META-09 shows the video name and the people names that change', async () => {
    const wrapper = await mountDialog();
    const title = wrapper.get('[data-test="metadata-video-title-7"]');
    expect(title.text()).toBe('Movie.2020.mkv');
    expect(title.attributes('title')).toBe('/library/Movie.2020.mkv');
    expect(wrapper.text()).not.toContain('视频 #7');
    expect(wrapper.get('[data-test="metadata-relation-diff-7-people"]').text()).toBe('新增：Alex');
  });

  // META-03（D-PC32）：同一来源名在一个批次里只决策一次，结果以 batch_resolutions 交给后端。
  it('META-03 decides a shared source name once for the whole batch', async () => {
    const diff = (id, name) => ({
      ...fixture().diffs[0], video_id: id, video_name: name,
      title: scalar('title', '', '', 'none'), original_title: scalar('original_title', '', '', 'none'),
      people: { field: 'people', current: [], change_type: 'fill', default_selected: true, requires_overwrite: false, added: ['Alex'], removed: [], kept: [],
        source: [{ source_name: 'Alex', normalized_name: 'alex', matches: [], default_mode: 'create_new', default_entity_id: 0 }] },
    });
    api.PreviewLocalMetadataBatch.mockResolvedValue({
      requested: 2, failures: [], diffs: [diff(7, 'E01.mkv'), diff(8, 'E02.mkv')],
      people_decisions: [{ source_name: 'Alex', normalized_name: 'alex', matches: [], default_mode: 'create_new', default_entity_id: 0, video_ids: [7, 8] }],
    });
    api.ApplyLocalMetadataBatch.mockResolvedValue({ requested: 2, succeeded: 2, failed: 0, results: [], failures: [] });
    const wrapper = mount(LocalMetadataDialog, { props: { visible: true, videoIds: [7, 8] } });
    await flushPromises();

    // 只有一个人物选择框，而不是每个视频各一个。
    expect(wrapper.findAll('[data-test="metadata-batch-person-alex"]')).toHaveLength(1);
    expect(wrapper.get('[data-test="metadata-batch-people"]').text()).toContain('出现在 2 个视频');
    expect(wrapper.get('[data-test="metadata-batch-person-alex"]').element.value).toBe('create_new');

    await wrapper.get('[data-test="apply-local-metadata"]').trigger('click');
    await flushPromises();
    const payload = api.ApplyLocalMetadataBatch.mock.calls[0][0];
    expect(payload.requests.map(request => request.video_id)).toEqual([7, 8]);
    expect(payload.requests.every(request => request.people_resolutions.length === 0)).toBe(true);
    expect(payload.batch_resolutions).toEqual({ alex: { normalized_name: 'alex', mode: 'create_new', entity_id: 0 } });
  });

  it('META-03 blocks applying while a shared source name has no mapping', async () => {
    const wrapper = await mountDialog();
    wrapper.vm.forms[0].selected.original_title = false;
    await wrapper.get('[data-test="apply-local-metadata"]').trigger('click');
    await flushPromises();
    expect(api.ApplyLocalMetadataBatch).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain('Alex 尚未选择映射');
  });
});
