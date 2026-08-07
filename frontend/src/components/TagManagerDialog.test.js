import { mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  CreateTag: vi.fn(),
  MergeTags: vi.fn(),
  UpdateTag: vi.fn()
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
  window.confirm = vi.fn(() => true);
});

describe('TagManagerDialog merge picker', () => {
  it('filters ordinary source labels and merges checkbox-selected sources', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    const target = wrapper.get('.merge-target-select');
    expect(target.findAll('option').map(option => option.text())).toEqual([
      '选择要保留的标签',
      '旅行 · 普通',
      '旅游 · 普通'
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

    expect(window.confirm).toHaveBeenCalledOnce();
    expect(api.MergeTags).toHaveBeenCalledWith([2], 1);
  });

  it('filters AI targets independently while allowing ordinary sources', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    await wrapper.get('[aria-label="选择目标标签类型"] button:nth-child(2)').trigger('click');

    const target = wrapper.get('.merge-target-select');
    expect(target.findAll('option').map(option => option.text())).toEqual([
      '选择要保留的标签',
      '动作 · AI',
      '激烈动作 · AI'
    ]);
    await wrapper.get('.merge-target-select').setValue('3');
    await wrapper.get('[aria-label="筛选待合并标签"]').setValue('旅行');

    expect(wrapper.findAll('.merge-source-option').map(option => option.text())).toEqual(['旅行普通标签']);
    expect(target.findAll('option').map(option => option.text())).toContain('动作 · AI');
    expect(target.findAll('option').map(option => option.text())).not.toContain('旅行 · 普通');
    expect(wrapper.findAll('.merge-source-option').map(option => option.text())).not.toContain('短视频自动标签');

    await wrapper.get('.merge-source-option input[type="checkbox"]').setValue(true);
    await wrapper.get('.merge-actions .btn-primary').trigger('click');

    expect(api.MergeTags).toHaveBeenCalledWith([1], 3);
  });

  it('allows an AI tag source when the retained target is ordinary', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    await wrapper.get('.merge-target-select').setValue('1');
    await wrapper.get('[aria-label="筛选待合并标签"]').setValue('动作');

    expect(wrapper.findAll('.merge-source-option').map(option => option.text())).toEqual(['动作AI 标签', '激烈动作AI 标签']);
    await wrapper.findAll('.merge-source-option input[type="checkbox"]')[0].setValue(true);
    await wrapper.get('.merge-actions .btn-primary').trigger('click');

    expect(api.MergeTags).toHaveBeenCalledWith([3], 1);
  });

  it('clears target and source selections when switching merge type', async () => {
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    await wrapper.get('.merge-target-select').setValue('1');
    await wrapper.get('.merge-source-option input[type="checkbox"]').setValue(true);

    await wrapper.get('[aria-label="选择目标标签类型"] button:nth-child(2)').trigger('click');

    expect(wrapper.vm.mergeTargetId).toBe(0);
    expect(wrapper.vm.mergeSourceIds).toEqual([]);
    expect(wrapper.find('.merge-source-picker').exists()).toBe(false);
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

  it('keeps ordinary tag inputs editable while locking system and automatic ones', () => {
    // 回归：automatic_kind 为空字符串时，:disabled="a || b" 会得到 ''，
    // 而 Vue 对布尔属性把空字符串视为 true，导致普通标签无法改名/改色。
    const wrapper = mount(TagManagerDialog, { props: { visible: true, tags } });
    const state = wrapper.findAll('.tag-edit-row').map(row => ({
      name: row.get('input[type="text"]').element.value,
      nameDisabled: row.get('input[type="text"]').element.disabled,
      colorDisabled: row.get('input[type="color"]').element.disabled
    }));

    expect(state.find(s => s.name === '旅行')).toMatchObject({ nameDisabled: false, colorDisabled: false });
    expect(state.find(s => s.name === '动作')).toMatchObject({ nameDisabled: true, colorDisabled: true });
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
