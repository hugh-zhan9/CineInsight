import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import QueueActionButton from './QueueActionButton.vue';

// 按钮尺寸跟着所在那一行：抽屉与列表里是紧凑的 sm（默认），整页详情的动作行是常规尺寸（md）。
describe('QueueActionButton size', () => {
  it('is compact by default and regular-sized with size="md"', () => {
    expect(mount(QueueActionButton).get('[data-test="queue-add"]').classes()).toEqual(['btn', 'btn--secondary', 'btn--sm']);
    expect(mount(QueueActionButton, { props: { size: 'md' } }).get('[data-test="queue-add"]').classes()).toEqual(['btn', 'btn--secondary']);
  });
});
