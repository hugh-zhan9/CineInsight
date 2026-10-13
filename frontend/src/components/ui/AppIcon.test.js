import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import AppIcon from './AppIcon.vue';
import RecapIcon from '../recap/RecapIcon.vue';
import { ICON_PATHS, iconPaths } from './icons.js';

// 统一设计 §2：迁入 RecapIcon / StatTile 的路径，并补齐界面通用的这些图标。
const REQUIRED = ['close', 'chevron-down', 'chevron-up', 'chevron-left', 'chevron-right', 'star', 'warning', 'trash', 'undo',
  'search', 'folder', 'play', 'more', 'check', 'refresh', 'filter', 'info'];
const RECAP_KEYS = ['night_owl', 'weekend_fan', 'rewatcher', 'explorer', 'streak', 'tag_loyal', 'image_lover', 'recorder', 'play',
  'plays', 'videos', 'images', 'days', 'clock', 'spark', 'heart', 'diary', 'live', 'person', 'collection', 'arrow'];
const STAT_TILE_ICONS = ['copy', 'image', 'trash', 'database', 'clock', 'transcode', 'inbox', 'activity', 'disk'];

describe('AppIcon', () => {
  it('draws the named icon on a 24 viewBox with a 1.75 stroke, decorative by default', () => {
    const wrapper = mount(AppIcon, { props: { name: 'trash' }, attrs: { 'data-test': 'icon-hook' } });
    const svg = wrapper.get('svg');
    expect(svg.attributes('viewBox')).toBe('0 0 24 24');
    expect(svg.attributes('stroke-width')).toBe('1.75');
    expect(svg.attributes('aria-hidden')).toBe('true');
    expect(svg.attributes('role')).toBeUndefined();
    expect(svg.attributes('width')).toBe('16');
    expect(svg.attributes('data-test')).toBe('icon-hook');
    expect(wrapper.findAll('path').map(path => path.attributes('d'))).toEqual(ICON_PATHS.trash);
  });

  it('becomes an image with an accessible name when a label is given', () => {
    const svg = mount(AppIcon, { props: { name: 'warning', label: '有问题', size: 20 } }).get('svg');
    expect(svg.attributes('role')).toBe('img');
    expect(svg.attributes('aria-label')).toBe('有问题');
    expect(svg.attributes('aria-hidden')).toBeUndefined();
    expect(svg.attributes('width')).toBe('20');
  });

  it('only allows 16 / 20 / 24 and known names', () => {
    expect(AppIcon.props.size.validator(24)).toBe(true);
    expect(AppIcon.props.size.validator(18)).toBe(false);
    expect(AppIcon.props.name.validator('close')).toBe(true);
    expect(AppIcon.props.name.validator('nope')).toBe(false);
    expect(iconPaths('toString')).toBe(null);
  });
});

describe('icon set', () => {
  it('contains the required UI icons plus the migrated recap and stat-tile paths', () => {
    for (const name of [...REQUIRED, ...RECAP_KEYS, ...STAT_TILE_ICONS]) {
      expect(iconPaths(name), name).not.toBe(null);
    }
  });

  it('every icon is a non-empty list of SVG path commands', () => {
    for (const [name, paths] of Object.entries(ICON_PATHS)) {
      expect(paths.length, name).toBeGreaterThan(0);
      for (const d of paths) expect(d, name).toMatch(/^M[\d.\s,a-zA-Z-]+$/);
    }
  });

  it('RecapIcon reads the shared set and still falls back to spark for unknown titles', () => {
    const night = mount(RecapIcon, { props: { name: 'night_owl' } });
    expect(night.findAll('path').map(path => path.attributes('d'))).toEqual(ICON_PATHS.night_owl);
    const unknown = mount(RecapIcon, { props: { name: 'no_such_title' } });
    expect(unknown.findAll('path').map(path => path.attributes('d'))).toEqual(ICON_PATHS.spark);
  });
});
