import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import BasicSection from './BasicSection.vue';

function mountSection(form) {
  return mount(BasicSection, { props: { form } });
}

describe('独立日志开关', () => {
  it('给定互相独立的值时，两个开关分别反映各自值', async () => {
    const form = { theme: 'system', log_enabled: false, perf_log_enabled: true };
    const wrapper = mountSection(form);
    const system = wrapper.get('[data-test="system-log-toggle"]');
    const performance = wrapper.get('[data-test="performance-log-toggle"]');
    expect(system.element.checked).toBe(false);
    expect(performance.element.checked).toBe(true);

    await performance.setValue(false);
    expect(form.perf_log_enabled).toBe(false);
    expect(system.element.checked).toBe(false);

    await system.setValue(true);
    await performance.setValue(true);
    expect(form.log_enabled).toBe(true);
    expect(form.perf_log_enabled).toBe(true);
    wrapper.unmount();
  });
});

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
