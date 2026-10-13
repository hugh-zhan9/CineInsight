import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import HeroHeader from './HeroHeader.vue';
import PageHeader from './PageHeader.vue';

describe('PageHeader', () => {
  it('renders the page title as h1 with subtitle, actions and the below slot', () => {
    const wrapper = mount(PageHeader, {
      props: { title: '数据维护', subtitle: '失联、完整度、规则与体检' },
      attrs: { 'data-test': 'page-header' },
      slots: {
        actions: '<button type="button" class="btn btn--secondary">刷新</button>',
        below: '<div class="tabs-here">页签</div>'
      }
    });
    expect(wrapper.element.tagName).toBe('HEADER');
    expect(wrapper.attributes('data-test')).toBe('page-header');
    expect(wrapper.get('h1').text()).toBe('数据维护');
    expect(wrapper.get('.ui-page-header__subtitle').text()).toBe('失联、完整度、规则与体检');
    expect(wrapper.get('.ui-page-header__actions button').text()).toBe('刷新');
    expect(wrapper.get('.ui-page-header__below .tabs-here').exists()).toBe(true);
  });

  it('omits empty parts; level="section" renders h2', () => {
    const wrapper = mount(PageHeader, { props: { title: '洞察' } });
    expect(wrapper.find('.ui-page-header__subtitle').exists()).toBe(false);
    expect(wrapper.find('.ui-page-header__actions').exists()).toBe(false);
    expect(wrapper.find('.ui-page-header__below').exists()).toBe(false);
    const section = mount(PageHeader, { props: { title: '分区', level: 'section' } });
    expect(section.find('h1').exists()).toBe(false);
    expect(section.get('h2').text()).toBe('分区');
    expect(PageHeader.props.level.validator('hero')).toBe(false);
  });
});

describe('HeroHeader', () => {
  it('renders eyebrow, h1 title, meta, extra content and actions', () => {
    const wrapper = mount(HeroHeader, {
      props: { title: '星际穿越', eyebrow: '继续观看', meta: '2014 · 169 分钟' },
      attrs: { 'data-test': 'hero' },
      slots: { default: '<p class="extra">评分 9.0</p>', actions: '<button type="button" class="btn btn--primary">播放</button>' }
    });
    expect(wrapper.attributes('data-test')).toBe('hero');
    expect(wrapper.get('.ui-hero-header__eyebrow').text()).toBe('继续观看');
    expect(wrapper.get('h1').text()).toBe('星际穿越');
    expect(wrapper.get('.ui-hero-header__meta').text()).toBe('2014 · 169 分钟');
    expect(wrapper.get('.extra').text()).toBe('评分 9.0');
    expect(wrapper.get('.ui-hero-header__actions button').text()).toBe('播放');
  });

  it('accepts an eyebrow slot (with icon) and a quiet variant', () => {
    const wrapper = mount(HeroHeader, {
      props: { title: '还没有可以接着看的视频', quiet: true },
      slots: { eyebrow: '<span class="icon-here"></span>今晚看什么' }
    });
    expect(wrapper.classes()).toContain('ui-hero-header--quiet');
    expect(wrapper.get('.ui-hero-header__eyebrow .icon-here').exists()).toBe(true);
    expect(wrapper.find('.ui-hero-header__meta').exists()).toBe(false);
    expect(wrapper.find('.ui-hero-header__actions').exists()).toBe(false);
  });
});
