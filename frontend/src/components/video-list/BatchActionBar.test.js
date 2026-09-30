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
});
