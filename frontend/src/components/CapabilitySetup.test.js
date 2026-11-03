import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ GetSystemHealthSection: vi.fn(), CountLibraryVideos: vi.fn(), GetPendingWorkSummary: vi.fn(), GetSubtitleEngineStatuses: vi.fn(), GetSceneRuntimeStatus: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import CapabilitySetup from './CapabilitySetup.vue';
import CapabilitySetupPrompt from './CapabilitySetupPrompt.vue';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';

beforeEach(() => {
  vi.clearAllMocks();
  api.GetSystemHealthSection.mockImplementation(async key => ({ key, checked_at: '2026-11-03T00:00:00Z', items: [] }));
  api.CountLibraryVideos.mockResolvedValue(0); api.GetPendingWorkSummary.mockResolvedValue({ total: 2 });
  const values = new Map(); Object.defineProperty(window, 'localStorage', { configurable: true, value: { getItem: key => values.get(key), setItem: (key, value) => values.set(key, value) } });
});
describe('goal preparation', () => {
  it('only reads cached facts on mount and probes only after an explicit check', async () => {
    const wrapper = mount(CapabilitySetup); await flushPromises();
    expect(api.GetSystemHealthSection).toHaveBeenCalledTimes(3);
    expect(api.GetSubtitleEngineStatuses).not.toHaveBeenCalled(); expect(api.GetSceneRuntimeStatus).not.toHaveBeenCalled();
    const subtitles = wrapper.findAll('button').find(button => button.text() === '制作字幕'); await subtitles.trigger('click');
    expect(api.GetSubtitleEngineStatuses).not.toHaveBeenCalled();
    await wrapper.findAll('button').find(button => button.text() === '检查当前状态').trigger('click'); await flushPromises();
    expect(api.GetSubtitleEngineStatuses).toHaveBeenCalledOnce();
    wrapper.unmount();
  });
  it('isolates a failed section and preserves cancelled navigation', async () => {
    api.GetSystemHealthSection.mockImplementation(async key => { if (key === 'indexes') throw new Error('private path'); return { key, items: [] }; });
    const run = vi.fn(async () => false);
    registerCommands('setup-fixture', [{ id: 'action:settings:scan-dirs', group: 'action', label: '目录', run }]);
    const wrapper = mount(CapabilitySetup); await flushPromises();
    expect(wrapper.get('[role=alert]').text()).toContain('部分状态'); expect(wrapper.text()).not.toContain('private path');
    await wrapper.get('[data-test=setup-step-roots] button').trigger('click'); await flushPromises();
    expect(run).toHaveBeenCalledOnce(); expect(wrapper.emitted('close')).toBeUndefined();
    wrapper.unmount(); unregisterCommands('setup-fixture');
  });
  it('does not treat a late snapshot as a current ready status', async () => {
    let release; api.CountLibraryVideos.mockImplementationOnce(() => new Promise(resolve => { release = resolve; }));
    const wrapper = mount(CapabilitySetup); await flushPromises();
    await wrapper.vm.refresh(); release(200); await flushPromises();
    expect(wrapper.get('[data-test=setup-step-library]').text()).toContain('0 部');
    wrapper.unmount();
  });
});
describe('first use prompt', () => {
  it('appears only for a known empty library and remembers dismissal', async () => {
    let wrapper = mount(CapabilitySetupPrompt); await flushPromises();
    expect(wrapper.find('[data-test=setup-prompt]').exists()).toBe(true);
    await wrapper.findAll('button').find(button => button.text() === '稍后再说').trigger('click'); wrapper.unmount();
    wrapper = mount(CapabilitySetupPrompt); await flushPromises();
    expect(wrapper.find('[data-test=setup-prompt]').exists()).toBe(false); wrapper.unmount();
  });
  it('does not call an existing or unreadable library empty', async () => {
    api.CountLibraryVideos.mockResolvedValueOnce(1).mockRejectedValueOnce(new Error('offline'));
    for (let i = 0; i < 2; i++) { const wrapper = mount(CapabilitySetupPrompt); await flushPromises(); expect(wrapper.find('[data-test=setup-prompt]').exists()).toBe(false); wrapper.unmount(); }
  });
  it('rechecks when returning to a library that now has content', async () => {
    const wrapper = mount(CapabilitySetupPrompt); await flushPromises();
    expect(wrapper.find('[data-test=setup-prompt]').exists()).toBe(true);
    await wrapper.setProps({ active: false }); api.CountLibraryVideos.mockResolvedValue(1);
    await wrapper.setProps({ active: true }); await flushPromises();
    expect(wrapper.find('[data-test=setup-prompt]').exists()).toBe(false); wrapper.unmount();
  });
});
