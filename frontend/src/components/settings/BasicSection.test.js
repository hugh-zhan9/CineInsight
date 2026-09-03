import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import BasicSection from './BasicSection.vue';

function mountSection(form) {
  return mount(BasicSection, { props: { form } });
}

describe('基本设置分区的桌面通知开关', () => {
  it('按表单值渲染开关状态', () => {
    const wrapper = mountSection({ desktop_notifications_enabled: false, theme: 'system' });

    const toggle = wrapper.get('[data-test="desktop-notifications-toggle"]');
    expect(toggle.element.checked).toBe(false);
  });

  it('拨动开关写回表单', async () => {
    const form = { desktop_notifications_enabled: true, theme: 'system' };
    const wrapper = mountSection(form);

    const toggle = wrapper.get('[data-test="desktop-notifications-toggle"]');
    expect(toggle.element.checked).toBe(true);

    await toggle.setValue(false);
    expect(form.desktop_notifications_enabled).toBe(false);

    await toggle.setValue(true);
    expect(form.desktop_notifications_enabled).toBe(true);
  });

  it('标注仅 macOS 生效', () => {
    const wrapper = mountSection({ desktop_notifications_enabled: true, theme: 'system' });

    expect(wrapper.text()).toContain('仅 macOS');
  });
});
