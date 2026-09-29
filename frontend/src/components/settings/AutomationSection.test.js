import { mount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';
import { reactive } from 'vue';

vi.mock('../../../wailsjs/go/main/App', () => ({ SelectDirectory: vi.fn() }));

import AutomationSection from './AutomationSection.vue';

function mountSection(overrides = {}) {
  const form = reactive({
    cleanup_short_seconds: 5,
    cleanup_low_width: 480,
    cleanup_low_height: 320,
    playback_resume_mode: 'resume',
    ...overrides
  });
  return { form, wrapper: mount(AutomationSection, { props: { form } }) };
}

describe('AutomationSection（P-032）', () => {
  it('META-10 清理中心的三个阈值可以在「自动化与扫描」里修改，写回表单', async () => {
    const { form, wrapper } = mountSection();
    expect(wrapper.get('[data-test="cleanup-short-seconds"]').element.value).toBe('5');
    expect(wrapper.get('[data-test="cleanup-low-width"]').element.value).toBe('480');
    expect(wrapper.get('[data-test="cleanup-low-height"]').element.value).toBe('320');

    await wrapper.get('[data-test="cleanup-short-seconds"]').setValue('12');
    await wrapper.get('[data-test="cleanup-low-width"]').setValue('640');
    await wrapper.get('[data-test="cleanup-low-height"]').setValue('360');

    expect(form.cleanup_short_seconds).toBe(12);
    expect(form.cleanup_low_width).toBe(640);
    expect(form.cleanup_low_height).toBe(360);
    const help = wrapper.get('[data-test="cleanup-thresholds"]').text();
    expect(help).toContain('极短片段');
    expect(help).toContain('极低分辨率');
    expect(help).toContain('下一次清理分析生效');
    wrapper.unmount();
  });

  it('PLAY-06 续播说明写明片库断点跨端互通、以最近一次写入为准，不再说断点是 IINA 自己记的', () => {
    const { wrapper } = mountSection();
    const help = wrapper.get('[data-test="playback-resume-help"]').text();
    expect(help).toContain('以最近一次写入为准');
    expect(help).toContain('从这个位置启动 IINA');
    expect(help).toContain('Jellyfin');
    expect(wrapper.text()).not.toContain('断点是 IINA 自己记的');
    expect(wrapper.text()).not.toContain('交给播放器决定');
    wrapper.unmount();
  });

  it('APP-14 兼容代理的说明指向改名后的「播放兼容缓存」分区', () => {
    const { wrapper } = mountSection();
    expect(wrapper.text()).toContain('「播放兼容缓存」分区');
    expect(wrapper.text()).not.toContain('「播放代理」分区');
    wrapper.unmount();
  });
});
