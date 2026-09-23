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
  CreateTagWithCategory: vi.fn(),
  MergeTags: vi.fn(),
  UpdateTagWithCategory: vi.fn(),
  GetAITagLibrary: vi.fn(),
  SaveAITagLibrary: vi.fn(),
  ClearAITagLibrary: vi.fn(),
  TriggerAITagging: vi.fn(),
  CreateTagCategory: vi.fn(),
  RenameTagCategory: vi.fn(),
  DeleteTagCategory: vi.fn()
}));

vi.mock('../../wailsjs/go/main/App', () => api);

import TagManagerDialog from './TagManagerDialog.vue';

const tags = [
  { id: 1, name: '旅行', color: '#111111', is_system: false, automatic_kind: '' },
  { id: 2, name: '旅游', color: '#222222', is_system: false, automatic_kind: '' },
  { id: 3, name: '动作', color: '#333333', is_system: true, automatic_kind: '' },
  { id: 4, name: '激烈动作', color: '#444444', is_system: true, automatic_kind: '' },
  { id: 5, name: '短视频', color: '#555555', is_system: false, automatic_kind: 'short_video' }
];

beforeEach(() => {
  vi.clearAllMocks();
  api.MergeTags.mockResolvedValue({ target_tag_id: 1, merged_tag_count: 1 });
  api.GetAITagLibrary.mockResolvedValue([]);
  api.SaveAITagLibrary.mockResolvedValue([]);
  api.ClearAITagLibrary.mockResolvedValue([]);
  api.TriggerAITagging.mockResolvedValue(false);
  feedback.confirmAction.mockResolvedValue(true);
});

describe('TagManagerDialog merge picker', () => {
  it('filters ordinary source labels and merges checkbox-selected sources', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    const target = wrapper.get('.merge-target-select');
    expect(target.findAll('option').map(option => option.text())).toEqual([
      '选择要保留的标签',
      '旅行', '旅游', '动作', '激烈动作'
    ]);
    await target.setValue('1');
    const filter = wrapper.get('[aria-label="筛选待合并标签"]');
    await filter.setValue('旅游');

    const sources = wrapper.findAll('.merge-source-option');
    expect(sources).toHaveLength(1);
    expect(sources[0].text()).toContain('旅游');
    await sources[0].get('input[type="checkbox"]').setValue(true);
    expect(wrapper.text()).toContain('已选 1 个');

    await wrapper.get('.merge-actions .btn-primary').trigger('click');

    expect(feedback.confirmAction).toHaveBeenCalledOnce();
    expect(api.MergeTags).toHaveBeenCalledWith([2], 1);
  });

  it('keeps all targets available while filtering sources', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });

    const target = wrapper.get('.merge-target-select');
    expect(target.findAll('option').map(option => option.text())).toEqual([
      '选择要保留的标签',
      '旅行', '旅游', '动作', '激烈动作'
    ]);
    await wrapper.get('.merge-target-select').setValue('3');
    await wrapper.get('[aria-label="筛选待合并标签"]').setValue('旅行');

    expect(wrapper.findAll('.merge-source-option').map(option => option.text())).toEqual(['旅行']);
    expect(target.findAll('option').map(option => option.text())).toContain('动作');
    expect(target.findAll('option').map(option => option.text())).toContain('旅行');
    expect(wrapper.findAll('.merge-source-option').map(option => option.text())).not.toContain('短视频自动标签');

    await wrapper.get('.merge-source-option input[type="checkbox"]').setValue(true);
    await wrapper.get('.merge-actions .btn-primary').trigger('click');

    expect(api.MergeTags).toHaveBeenCalledWith([1], 3);
  });

  it('allows an AI tag source when the retained target is ordinary', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    await wrapper.get('.merge-target-select').setValue('1');
    await wrapper.get('[aria-label="筛选待合并标签"]').setValue('动作');

    expect(wrapper.findAll('.merge-source-option').map(option => option.text())).toEqual(['动作', '激烈动作']);
    await wrapper.findAll('.merge-source-option input[type="checkbox"]')[0].setValue(true);
    await wrapper.get('.merge-actions .btn-primary').trigger('click');

    expect(api.MergeTags).toHaveBeenCalledWith([3], 1);
  });

});

describe('TagManagerDialog tag list search', () => {
  const listNames = wrapper =>
    wrapper.findAll('.tag-edit-row').map(row => row.get('input[type="text"]').element.value);

  it('filters the tag list by name and reports the visible count', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    expect(listNames(wrapper)).toHaveLength(tags.length);
    expect(wrapper.get('.tag-list-count').text()).toContain(String(tags.length));

    await wrapper.get('.tag-filter-input').setValue('旅');

    expect(listNames(wrapper)).toEqual(['旅行', '旅游']);
    expect(wrapper.get('.tag-list-count').text()).toBe('显示 2 / 5');
  });

  it('matches AI and automatic tags too, case-insensitively', async () => {
    const wrapper = mount(TagManagerDialog, {
      props: {
        visible: true,
        tags: [...tags, { id: 6, name: 'Anime', color: '#666666', is_system: true, automatic_kind: '' }]
      }
    });

    await wrapper.get('.tag-filter-input').setValue('动作');
    expect(listNames(wrapper)).toEqual(['动作', '激烈动作']);

    await wrapper.get('.tag-filter-input').setValue('anime');
    expect(listNames(wrapper)).toEqual(['Anime']);

    await wrapper.get('.tag-filter-input').setValue('短视频');
    expect(listNames(wrapper)).toEqual(['短视频']);
  });

  it('shows a no-match hint without hiding the empty-library hint', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    await wrapper.get('.tag-filter-input').setValue('不存在的标签');

    expect(listNames(wrapper)).toEqual([]);
    expect(wrapper.text()).toContain('没有匹配');

    const empty = mount(TagManagerDialog, { props: { visible: true, tags: [] } });
    expect(empty.text()).toContain('暂无标签');
  });

  it('keeps a row visible while its name is edited away from the keyword', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    await wrapper.get('.tag-filter-input').setValue('旅行');
    expect(listNames(wrapper)).toEqual(['旅行']);

    await wrapper.get('.tag-edit-row input[type="text"]').setValue('假期');

    // 行内改名不应让该行中途消失，否则用户点不到“保存”。
    expect(listNames(wrapper)).toEqual(['假期']);
    expect(wrapper.vm.localTags.find(t => t.id === 1).name).toBe('假期');
  });

  it('allows all non-automatic tags to be edited', () => {
    // 回归：automatic_kind 为空字符串时，:disabled="a || b" 会得到 ''，
    // 而 Vue 对布尔属性把空字符串视为 true，导致普通标签无法改名/改色。
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    const state = wrapper.findAll('.tag-edit-row').map(row => ({
      name: row.get('input[type="text"]').element.value,
      nameDisabled: row.get('input[type="text"]').element.disabled,
      colorDisabled: row.get('input[type="color"]').element.disabled
    }));

    expect(state.find(s => s.name === '旅行')).toMatchObject({ nameDisabled: false, colorDisabled: false });
    expect(state.find(s => s.name === '动作')).toMatchObject({ nameDisabled: false, colorDisabled: false });
    expect(state.find(s => s.name === '短视频')).toMatchObject({ nameDisabled: true, colorDisabled: true });
  });

  it('resets the keyword when the dialog is reopened', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: false, tags } });
    await wrapper.setProps({ visible: true });
    await wrapper.get('.tag-filter-input').setValue('旅');
    expect(wrapper.vm.tagKeyword).toBe('旅');

    await wrapper.setProps({ visible: false });
    await wrapper.setProps({ visible: true });

    expect(wrapper.vm.tagKeyword).toBe('');
    expect(listNames(wrapper)).toHaveLength(tags.length);
  });
});

describe('TagManagerDialog categories', () => {
  it('creates a tag with a category and clears both inputs after success', async () => {
    api.CreateTagWithCategory.mockResolvedValue({ id: 6, name: '夜景', namespace: '场景' });
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags: tags.map(t => t.id === 1 ? { ...t, namespace: '场景' } : t) } });
    await wrapper.get('input[placeholder="输入标签名称..."]').setValue('夜景');
    await wrapper.get('[aria-label="新标签的主题分类"]').setValue('场景');
    await wrapper.get('.setting-item .btn-primary').trigger('click');
    expect(api.CreateTagWithCategory).toHaveBeenCalledWith('夜景', '', '场景');
    expect(wrapper.get('input[placeholder="输入标签名称..."]').element.value).toBe('');
    expect(wrapper.get('[aria-label="新标签的主题分类"]').element.value).toBe('');
    expect(wrapper.emitted('tags-changed')).toHaveLength(1);
  });

  it('saves category changes for either legacy type and locks automatic categories', async () => {
    api.UpdateTagWithCategory.mockResolvedValue();
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags: tags.map(t => t.id === 2 ? { ...t, namespace: '旅行主题' } : t) } });
    const rows = wrapper.findAll('.tag-edit-row');
    await rows[0].get('.tag-category-edit').setValue('旅行主题');
    await rows[0].get('button').trigger('click');
    expect(api.UpdateTagWithCategory).toHaveBeenCalledWith(1, '旅行', '#111111', '旅行主题');
    expect(rows[2].get('.tag-category-edit').element.disabled).toBe(false);
    await rows[2].get('input[type="text"]').setValue('新动作');
    await rows[2].get('button').trigger('click');
    expect(api.UpdateTagWithCategory).toHaveBeenCalledWith(3, '新动作', '#333333', '');
    expect(rows[4].get('.tag-category-edit').element.disabled).toBe(true);
  });

  it('creates, renames and deletes a shared category for ordinary and AI tags', async () => {
    api.CreateTagCategory.mockResolvedValue();
    api.RenameTagCategory.mockResolvedValue();
    api.DeleteTagCategory.mockResolvedValue();
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    await flushPromises();
    await wrapper.get('[aria-label="新分类名称"]').setValue('题材');
    const choices = wrapper.findAll('.category-tag-picker input[type="checkbox"]');
    await choices[0].setValue(true);
    await choices[2].setValue(true);
    await wrapper.get('.category-create button').trigger('click');
    await flushPromises();
    expect(api.CreateTagCategory).toHaveBeenCalledWith('题材', [1, 3]);
    expect(wrapper.text()).toContain('题材 · 2 个标签');
    await wrapper.get('[aria-label="重命名分类 题材"]').setValue('内容');
    await wrapper.get('.category-row .btn-secondary').trigger('click');
    await flushPromises();
    expect(api.RenameTagCategory).toHaveBeenCalledWith('题材', '内容');
    await wrapper.get('.category-delete').trigger('click');
    await flushPromises();
    expect(api.DeleteTagCategory).toHaveBeenCalledWith('内容');
    expect(wrapper.vm.localTags.filter(tag => [1, 3].includes(tag.id)).every(tag => tag.namespace === '')).toBe(true);
  });

  it('shows the automatic category as read-only and excludes it from new tag choices', async () => {
    const withAutomaticCategory = tags.map(tag => tag.id === 5 ? { ...tag, namespace: '自动' } : tag);
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags: withAutomaticCategory } });
    expect(wrapper.get('[aria-label="新标签的主题分类"]').findAll('option').map(option => option.text())).not.toContain('自动');
    expect(wrapper.text()).toContain('系统分类，不可改名或删除');
    expect(wrapper.find('.category-row input').exists()).toBe(false);
    expect(wrapper.find('.category-delete').exists()).toBe(false);
  });
});


describe('unified tag management', () => {
  it('manages tags and categories on one page without type switches or library APIs', () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    expect(wrapper.find('[role="tab"]').exists()).toBe(false);
    expect(wrapper.find('[aria-label="选择目标标签类型"]').exists()).toBe(false);
    expect(wrapper.text()).not.toContain('AI 标签库');
    expect(wrapper.text()).not.toContain('普通标签');
    expect(wrapper.find('[aria-label="新分类名称"]').exists()).toBe(true);
    expect(api.GetAITagLibrary).not.toHaveBeenCalled();
  });

  it('preserves edits and reports a failed row save', async () => {
    api.UpdateTagWithCategory.mockRejectedValueOnce('database unavailable');
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    const row = wrapper.findAll('.tag-edit-row')[2];
    await row.get('input[type="text"]').setValue('新名字');
    await row.get('button').trigger('click');
    await flushPromises();
    expect(row.get('input[type="text"]').element.value).toBe('新名字');
    expect(wrapper.emitted('tags-changed')).toBeUndefined();
    expect(feedback.notifyError).toHaveBeenCalledWith(expect.stringContaining('database unavailable'));
  });

  it('offers the existing delete confirmation for a legacy AI tag', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    await wrapper.findAll('.tag-edit-row')[2].findAll('button')[1].trigger('click');
    expect(wrapper.emitted('request-delete-tag')[0][0].id).toBe(3);
  });
});
