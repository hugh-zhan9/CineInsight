import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import HeroHeader from './HeroHeader.vue';

describe('HeroHeader', () => {
  it('renders eyebrow, an h1 title, meta, the extra slot and actions in that order', () => {
    const wrapper = mount(HeroHeader, {
      props: { title: '影片1', eyebrow: '继续观看', meta: '看到 01:15 / 10:00' },
      attrs: { 'data-test': 'hero' },
      slots: { default: '<div class="chips">科幻</div>', actions: '<button type="button" class="btn btn--primary">打开详情</button>' }
    });
    expect(wrapper.attributes('data-test')).toBe('hero');
    const parts = [...wrapper.element.children].map(node => node.tagName + (node.className ? `.${node.className}` : ''));
    expect(parts).toEqual(['P.ui-hero-header__eyebrow', 'H1.ui-hero-header__title', 'P.ui-hero-header__meta', 'DIV.chips', 'DIV.ui-hero-header__actions']);
    expect(wrapper.get('h1').text()).toBe('影片1');
    expect(wrapper.get('.ui-hero-header__title').attributes('data-test')).toBeUndefined();
    expect(wrapper.get('.ui-hero-header__actions button').text()).toBe('打开详情');
  });

  it('headingLevel picks the heading element; title / meta hooks land on those lines', () => {
    const wrapper = mount(HeroHeader, {
      props: { title: '影片5', meta: 'Original Title', headingLevel: 2, titleDataTest: 'detail-title', metaDataTest: 'detail-original-title' }
    });
    expect(wrapper.find('h1').exists()).toBe(false);
    expect(wrapper.get('h2').attributes('data-test')).toBe('detail-title');
    expect(wrapper.get('[data-test="detail-title"]').text()).toBe('影片5');
    expect(wrapper.get('[data-test="detail-original-title"]').text()).toBe('Original Title');
    expect(mount(HeroHeader, { props: { title: 'a', headingLevel: 3 } }).get('h3').text()).toBe('a');
    expect(HeroHeader.props.headingLevel.validator(0)).toBe(false);
    expect(HeroHeader.props.headingLevel.validator(7)).toBe(false);
    expect(HeroHeader.props.headingLevel.validator(2.5)).toBe(false);
    expect(HeroHeader.props.headingLevel.validator(6)).toBe(true);
  });

  it('a title slot replaces the plain title; eyebrow slot and quiet still work; empty parts are left out', () => {
    const wrapper = mount(HeroHeader, {
      props: { quiet: true },
      slots: { title: '<span class="mark">《影片1》</span>', eyebrow: '<svg class="icon"></svg> 本月' }
    });
    expect(wrapper.classes()).toContain('ui-hero-header--quiet');
    expect(wrapper.get('h1 .mark').text()).toBe('《影片1》');
    expect(wrapper.get('.ui-hero-header__eyebrow .icon').exists()).toBe(true);
    expect(wrapper.find('.ui-hero-header__meta').exists()).toBe(false);
    expect(wrapper.find('.ui-hero-header__actions').exists()).toBe(false);
  });
});
