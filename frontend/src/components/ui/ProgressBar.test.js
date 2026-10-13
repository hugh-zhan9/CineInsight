import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import ProgressBar from './ProgressBar.vue';

describe('ProgressBar', () => {
  it('reports a determinate value as a clamped percentage', async () => {
    const wrapper = mount(ProgressBar, { props: { value: 0.426, label: '转码' } });
    const bar = wrapper.get('[role="progressbar"]');
    expect(bar.attributes('aria-valuenow')).toBe('43');
    expect(bar.attributes('aria-valuemin')).toBe('0');
    expect(bar.attributes('aria-valuemax')).toBe('100');
    expect(bar.attributes('aria-label')).toBe('转码');
    expect(wrapper.get('.ui-progress__fill').attributes('style')).toContain('width: 43%');
    expect(wrapper.text()).toContain('43%');

    await wrapper.setProps({ value: 1.7 });
    expect(bar.attributes('aria-valuenow')).toBe('100');
    await wrapper.setProps({ value: -1 });
    expect(bar.attributes('aria-valuenow')).toBe('0');
  });

  it('is indeterminate without a value: no valuenow, busy, no percentage text', () => {
    const wrapper = mount(ProgressBar, { props: { label: '准备中' } });
    const bar = wrapper.get('[role="progressbar"]');
    expect(bar.attributes('aria-valuenow')).toBeUndefined();
    expect(bar.attributes('aria-busy')).toBe('true');
    expect(wrapper.classes()).toContain('ui-progress--indeterminate');
    expect(wrapper.text()).not.toContain('%');
    expect(mount(ProgressBar).get('[role="progressbar"]').attributes('aria-label')).toBe('进度');
  });
});
