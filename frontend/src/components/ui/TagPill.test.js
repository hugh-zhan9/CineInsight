import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import TagPill from './TagPill.vue';

describe('TagPill', () => {
  it('is read-only text by default and emits nothing', async () => {
    const wrapper = mount(TagPill, { props: { label: '科幻' }, attrs: { 'data-test': 'tag' } });
    expect(wrapper.element.tagName).toBe('SPAN');
    expect(wrapper.text()).toBe('科幻');
    expect(wrapper.attributes('data-test')).toBe('tag');
    await wrapper.trigger('click');
    expect(wrapper.emitted('click')).toBeUndefined();
  });

  it('becomes a keyboard-reachable button that emits click when clickable', async () => {
    const wrapper = mount(TagPill, { props: { label: '纪录片', clickable: true }, attrs: { 'data-test': 'tag' } });
    expect(wrapper.element.tagName).toBe('BUTTON');
    expect(wrapper.attributes('type')).toBe('button');
    expect(wrapper.attributes('data-test')).toBe('tag');
    await wrapper.trigger('click');
    expect(wrapper.emitted('click')).toHaveLength(1);
  });
});
