import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import AppSpinner from './AppSpinner.vue';

describe('AppSpinner', () => {
  it('is decorative next to text and announces itself only with a label', () => {
    const plain = mount(AppSpinner, { attrs: { 'data-test': 'spin' } });
    expect(plain.attributes('aria-hidden')).toBe('true');
    expect(plain.attributes('role')).toBeUndefined();
    expect(plain.attributes('data-test')).toBe('spin');
    expect(plain.get('svg').attributes('width')).toBe('16');

    const labelled = mount(AppSpinner, { props: { size: 20, label: '正在读取' } });
    expect(labelled.attributes('role')).toBe('status');
    expect(labelled.attributes('aria-label')).toBe('正在读取');
    expect(labelled.get('svg').attributes('width')).toBe('20');
    expect(AppSpinner.props.size.validator(24)).toBe(false);
  });
});
