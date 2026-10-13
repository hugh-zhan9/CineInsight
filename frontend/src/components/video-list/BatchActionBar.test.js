import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import BatchActionBar, { selectedSizeText } from './BatchActionBar.vue';

describe('批量操作栏', () => {
  it('MEDIA-14 选中视频后有「批量生成字幕」，点了交给片库页', async () => {
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 3 } });
    const button = wrapper.find('[data-test="batch-subtitle"]');
    expect(button.text()).toBe('批量生成字幕');
    await button.trigger('click');
    expect(wrapper.emitted('batch-subtitle')).toHaveLength(1);
    wrapper.unmount();
  });

  it('D-MC-04 有「批量编辑…」，点了交给片库页打开批量编辑', async () => {
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 1 } });
    const button = wrapper.get('[data-test="batch-metadata-edit"]');
    expect(button.text()).toBe('批量编辑…');
    await button.trigger('click');
    expect(wrapper.emitted('batch-metadata-edit')).toHaveLength(1);
    wrapper.unmount();
  });

  it('正好选中两个时才有「对比播放」，点了交给片库页', async () => {
    for (const count of [1, 3]) {
      const other = mount(BatchActionBar, { props: { selectedCount: count } });
      expect(other.find('[data-test="batch-compare-play"]').exists()).toBe(false);
      other.unmount();
    }
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 2 } });
    const button = wrapper.get('[data-test="batch-compare-play"]');
    expect(button.text()).toBe('对比播放');
    await button.trigger('click');
    expect(wrapper.emitted('batch-compare')).toHaveLength(1);
    wrapper.unmount();
  });

  // 原在 LibraryToolbar.test.js「多选批量栏」：批量条移到片库页底部之后，转发与合计体积的用例搬到这里。
  it('批量栏的动作都发回片库页；「取消选择」发 clear-selection', async () => {
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 2 } });
    const byText = text => wrapper.findAll('button').find(button => button.text().includes(text));
    await byText('批量标签编辑').trigger('click');
    await byText('批量迁移').trigger('click');
    await byText('导入本地资料').trigger('click');
    await byText('移到废纸篓').trigger('click');
    await byText('取消选择').trigger('click');
    await byText('批量生成字幕').trigger('click');
    await wrapper.get('[data-test="batch-playback-proxy"]').trigger('click');
    for (const name of ['batch-add-tag', 'batch-move', 'batch-local-metadata', 'batch-delete', 'clear-selection', 'batch-subtitle', 'batch-playback-proxy']) {
      expect(wrapper.emitted(name)).toHaveLength(1);
    }
    wrapper.unmount();
  });

  it('D-MW-VERSIONS「合并为版本组」至少选 2 个才可用', async () => {
    const single = mount(BatchActionBar, { props: { selectedCount: 1 } });
    expect(single.get('[data-test="batch-version-group"]').attributes('disabled')).toBeDefined();
    single.unmount();
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 2 } });
    await wrapper.get('[data-test="batch-version-group"]').trigger('click');
    expect(wrapper.emitted('batch-version-group')).toHaveLength(1);
    wrapper.unmount();
  });

  it('删除按钮用片库页给的词（D-UD-08），不再是笼统的「批量删除」', () => {
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 1, deleteLabel: '从片库移除…' } });
    expect(wrapper.get('[data-test="batch-delete"]').text()).toBe('从片库移除…');
    expect(wrapper.text()).not.toContain('批量删除');
    wrapper.unmount();
  });

  it('合计体积只算已加载的行；有选中的条目不在已加载的行里时不猜', () => {
    const rows = [
      { id: 1, name: 'a.mp4', size: 2 * 1024 ** 3 },
      { id: 2, name: 'b.mp4', size: 1024 ** 3 }
    ];
    expect(selectedSizeText(rows, [1, 2])).toBe('3.0 GB');
    expect(selectedSizeText([rows[0]], [1, 2])).toBe('');
    expect(selectedSizeText(rows, [])).toBe('');
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 2, selectedTotalSizeText: '3.0 GB' } });
    expect(wrapper.text()).toContain('共 3.0 GB');
    wrapper.unmount();
  });
});

// D-UD-10：全站同一个选择操作条——没选中时没有批量动作；「全选当前结果（N）」写出真实总数；「取消选择」与 Esc 清空。
describe('批量操作条（SelectionBar）', () => {
  it('没选中时整条不出现，也就没有任何批量动作', () => {
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 0, totalMatching: 12 } });
    expect(wrapper.find('.selection-toolbar').exists()).toBe(false);
    expect(wrapper.findAll('button')).toHaveLength(0);
    wrapper.unmount();
  });

  it('「全选当前结果（N）」写出当前结果的真实总数，点了发 select-all-matching；全选之后不再出现', async () => {
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 3, totalMatching: 128 } });
    expect(wrapper.get('[data-test="selection-bar-count"]').text()).toBe('已选 3 项');
    const selectAll = wrapper.get('[data-test="selection-bar-select-all-matching"]');
    expect(selectAll.text()).toBe('全选当前结果（128）');
    await selectAll.trigger('click');
    expect(wrapper.emitted('select-all-matching')).toHaveLength(1);

    await wrapper.setProps({ selectedCount: 128, allMatchingSelected: true });
    expect(wrapper.find('[data-test="selection-bar-select-all-matching"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="selection-bar-count"]').text()).toBe('已选当前结果全部 128 项');
    wrapper.unmount();
  });

  it('不知道总数时不提供「全选当前结果」，免得把已加载的说成全部', () => {
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 3, totalMatching: null } });
    expect(wrapper.find('[data-test="selection-bar-select-all-matching"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('Esc 发 clear-selection', async () => {
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 2 }, attachTo: document.body });
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(wrapper.emitted('clear-selection')).toHaveLength(1);
    wrapper.unmount();
  });
});
