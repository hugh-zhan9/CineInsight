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

  it('puts the leading slot (dot / avatar / icon) before the label in both variants', async () => {
    const button = mount(TagPill, { props: { label: '周迅', clickable: true }, slots: { leading: '<img class="avatar" src="a.jpg" alt="">' } });
    const parts = button.element.children;
    expect(parts[0].classList.contains('ui-tag-pill__leading')).toBe(true);
    expect(parts[0].querySelector('img.avatar')).not.toBeNull();
    expect(parts[1].classList.contains('ui-tag-pill__label')).toBe(true);
    expect(button.text()).toBe('周迅');
    await button.trigger('click');
    expect(button.emitted('click')).toHaveLength(1);

    const text = mount(TagPill, { props: { label: '科幻' }, slots: { leading: '<i class="dot"></i>' } });
    expect(text.element.tagName).toBe('SPAN');
    expect(text.get('.ui-tag-pill__leading .dot').exists()).toBe(true);
    expect(mount(TagPill, { props: { label: '科幻' } }).find('.ui-tag-pill__leading').exists()).toBe(false);
  });
});
