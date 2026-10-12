import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import BatchActionBar from './BatchActionBar.vue';

describe('批量操作栏', () => {
  it('MEDIA-14 选中视频后有「批量生成字幕」，点了交给片库页', async () => {
    const wrapper = mount(BatchActionBar, { props: { selectedCount: 3 } });
    const button = wrapper.find('[data-test="batch-subtitle"]');
    expect(button.text()).toBe('批量生成字幕');
    await button.trigger('click');
    expect(wrapper.emitted('batch-subtitle')).toHaveLength(1);
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
});
