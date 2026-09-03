import { flushPromises, mount } from '@vue/test-utils';

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

const api = vi.hoisted(() => Object.fromEntries(
  ['ListGlossaryEntries', 'UpsertGlossaryEntry', 'DeleteGlossaryEntry'].map(name => [name, vi.fn()])
));
vi.mock('../../wailsjs/go/main/App', () => api);

import GlossaryEditor from './GlossaryEditor.vue';

const entry = (overrides = {}) => ({
  id: 1, collection_id: null, scope_key: 0,
  source_term: 'Neo', source_term_lower: 'neo', target_term: '尼奥', note: '主角',
  ...overrides
});

async function mountEditor(props = {}) {
  const wrapper = mount(GlossaryEditor, { props });
  await flushPromises();
  return wrapper;
}

describe('GlossaryEditor', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    feedback.confirmAction.mockResolvedValue(true);
    api.ListGlossaryEntries.mockResolvedValue([entry()]);
    api.UpsertGlossaryEntry.mockResolvedValue(entry());
    api.DeleteGlossaryEntry.mockResolvedValue(undefined);
  });

  it('按作用域加载条目：全局传 0，作品集传作品集 ID', async () => {
    await mountEditor();
    expect(api.ListGlossaryEntries).toHaveBeenCalledWith(0);

    api.ListGlossaryEntries.mockClear();
    const wrapper = await mountEditor({ collectionId: 7 });
    expect(api.ListGlossaryEntries).toHaveBeenCalledWith(7);
    expect(wrapper.findAll('[data-test="glossary-entry"]')).toHaveLength(1);
  });

  it('空表显示空态', async () => {
    api.ListGlossaryEntries.mockResolvedValue([]);
    const wrapper = await mountEditor();
    expect(wrapper.find('[data-test="glossary-empty"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="glossary-entry"]').exists()).toBe(false);
  });

  it('新增术语：全局作用域写 collection_id=null，写完重新加载', async () => {
    const wrapper = await mountEditor();
    await wrapper.find('[data-test="glossary-source-input"]').setValue('Trinity');
    await wrapper.find('[data-test="glossary-target-input"]').setValue('崔妮蒂');
    await wrapper.find('[data-test="glossary-note-input"]').setValue('女主角');
    api.ListGlossaryEntries.mockClear();
    await wrapper.find('[data-test="glossary-save"]').trigger('click');
    await flushPromises();

    expect(api.UpsertGlossaryEntry).toHaveBeenCalledWith({
      id: 0, collection_id: null, source_term: 'Trinity', target_term: '崔妮蒂', note: '女主角'
    });
    expect(api.ListGlossaryEntries).toHaveBeenCalledWith(0);
    expect(wrapper.find('[data-test="glossary-source-input"]').element.value).toBe('');
  });

  it('作品集作用域新增术语写自己的 collection_id', async () => {
    const wrapper = await mountEditor({ collectionId: 7 });
    await wrapper.find('[data-test="glossary-source-input"]').setValue('Morpheus');
    await wrapper.find('[data-test="glossary-target-input"]').setValue('墨菲斯');
    await wrapper.find('[data-test="glossary-save"]').trigger('click');
    await flushPromises();

    expect(api.UpsertGlossaryEntry).toHaveBeenCalledWith({
      id: 0, collection_id: 7, source_term: 'Morpheus', target_term: '墨菲斯', note: ''
    });
  });

  it('三个输入框都有长度上限：源词/目标词 200，备注 500', async () => {
    const wrapper = await mountEditor();
    expect(wrapper.find('[data-test="glossary-source-input"]').attributes('maxlength')).toBe('200');
    expect(wrapper.find('[data-test="glossary-target-input"]').attributes('maxlength')).toBe('200');
    expect(wrapper.find('[data-test="glossary-note-input"]').attributes('maxlength')).toBe('500');
  });

  it('源词或目标词为空时不能保存', async () => {
    const wrapper = await mountEditor();
    expect(wrapper.find('[data-test="glossary-save"]').attributes('disabled')).toBeDefined();
    await wrapper.find('[data-test="glossary-source-input"]').setValue('Trinity');
    expect(wrapper.find('[data-test="glossary-save"]').attributes('disabled')).toBeDefined();
    await wrapper.find('[data-test="glossary-target-input"]').setValue('崔妮蒂');
    expect(wrapper.find('[data-test="glossary-save"]').attributes('disabled')).toBeUndefined();
  });

  it('编辑既有术语带上 ID，取消后回到新增态', async () => {
    const wrapper = await mountEditor();
    await wrapper.find('[data-test="glossary-edit-1"]').trigger('click');
    expect(wrapper.find('[data-test="glossary-source-input"]').element.value).toBe('Neo');
    expect(wrapper.find('[data-test="glossary-note-input"]').element.value).toBe('主角');

    await wrapper.find('[data-test="glossary-target-input"]').setValue('尼欧');
    await wrapper.find('[data-test="glossary-save"]').trigger('click');
    await flushPromises();
    expect(api.UpsertGlossaryEntry).toHaveBeenCalledWith({
      id: 1, collection_id: null, source_term: 'Neo', target_term: '尼欧', note: '主角'
    });

    await wrapper.find('[data-test="glossary-edit-1"]').trigger('click');
    expect(wrapper.find('[data-test="glossary-cancel"]').exists()).toBe(true);
    await wrapper.find('[data-test="glossary-cancel"]').trigger('click');
    expect(wrapper.find('[data-test="glossary-cancel"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="glossary-source-input"]').element.value).toBe('');
  });

  it('删除术语先确认，取消确认则不删', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const wrapper = await mountEditor();
    await wrapper.find('[data-test="glossary-delete-1"]').trigger('click');
    await flushPromises();
    expect(api.DeleteGlossaryEntry).not.toHaveBeenCalled();

    feedback.confirmAction.mockResolvedValue(true);
    api.ListGlossaryEntries.mockClear();
    await wrapper.find('[data-test="glossary-delete-1"]').trigger('click');
    await flushPromises();
    expect(api.DeleteGlossaryEntry).toHaveBeenCalledWith(1);
    expect(api.ListGlossaryEntries).toHaveBeenCalledWith(0);
  });

  it('后端报错时展示错误而不是静默失败', async () => {
    api.UpsertGlossaryEntry.mockRejectedValue(new Error('translation_glossary_term_conflict'));
    const wrapper = await mountEditor();
    await wrapper.find('[data-test="glossary-source-input"]').setValue('Trinity');
    await wrapper.find('[data-test="glossary-target-input"]').setValue('崔妮蒂');
    await wrapper.find('[data-test="glossary-save"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="glossary-error"]').text()).toContain('translation_glossary_term_conflict');
  });
});
