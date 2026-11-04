import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, expect, it, vi } from 'vitest';
const finish = vi.hoisted(() => vi.fn(async () => {}));
vi.mock('../../wailsjs/go/main/App', () => ({ FinishMediaExplorationClose: finish }));
import ExplorationQuitDialog from './ExplorationQuitDialog.vue';
const wrappers = [];
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); delete window.runtime; vi.clearAllMocks(); });
function dialog(flush = vi.fn(async () => {})) {
  const events = {}, resume = vi.fn();
  window.runtime = { EventsOn: (name, handler) => { events[name] = handler; return () => delete events[name]; } };
  const wrapper = mount(ExplorationQuitDialog, { props: { flush, resume }, global: { renderStubDefaultSlot: true, stubs: { Teleport: true, DialogShell: { template: '<div><slot/><slot name="footer"/></div>' } } } });
  wrappers.push(wrapper); return { wrapper, events, resume };
}
it('waits for flush and coalesces duplicate close requests', async () => {
  let resolve; const flush = vi.fn(() => new Promise(r => { resolve = r; }));
  const { events } = dialog(flush);
  events['exploration-close-flush']({ request_id: 'a' }); events['exploration-close-flush']({ request_id: 'a' });
  expect(flush).toHaveBeenCalledTimes(1); expect(finish).not.toHaveBeenCalled();
  resolve(); await flushPromises(); expect(finish).toHaveBeenCalledWith('a', 'saved');
});
it('does not turn a late flush result into automatic quit after timeout', async () => {
  let resolve; const { wrapper, events, resume } = dialog(() => new Promise(r => { resolve = r; }));
  events['exploration-close-flush']({ request_id: 'a' }); events['exploration-close-timeout']({ request_id: 'a' });
  resolve(); await flushPromises(); expect(finish).not.toHaveBeenCalled(); expect(wrapper.text()).toContain('超时');
  await wrapper.vm.cancel(); expect(finish).toHaveBeenCalledWith('a', 'cancel'); expect(resume).toHaveBeenCalledTimes(1);
});
it('keeps a new retry request when its triggering binding replies later', async () => {
  const { wrapper, events } = dialog(vi.fn().mockRejectedValue(new Error('save failed')));
  events['exploration-close-flush']({ request_id: 'a' }); await flushPromises();
  finish.mockImplementationOnce(async (_id, action) => { if (action === 'retry') events['exploration-close-flush']({ request_id: 'b' }); });
  await wrapper.vm.retry(); await flushPromises(); expect(wrapper.vm.request).toBe('b');
  await wrapper.vm.discard(); expect(finish).toHaveBeenLastCalledWith('b', 'discard');
});
