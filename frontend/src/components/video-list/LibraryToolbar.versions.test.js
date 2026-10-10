// 工具栏上的多版本聚合入口（D-MW-VERSIONS）：结果条「合并版本」开关、批量栏「合并为版本组」。
import { mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import LibraryToolbar from './LibraryToolbar.vue';

function props(extra = {}) {
  return {
    tags: [], videos: [], selectedVideoIds: [], sizeOptions: [], resOptions: [],
    incrementalScan: { running: false, state: 'idle', message: '' }, directories: [], settings: {},
    technicalBackfill: { running: false }, perceptualHash: { running: false },
    localMetadataBackfill: { running: false }, localMetadataExport: { running: false },
    tagBgColor: hex => hex, libraryFilterFrom: draft => ({ draft }),
    ...extra
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('LibraryToolbar 版本聚合入口', () => {
  it('结果条的「合并版本」反映开关并 emit update:collapseVersions', async () => {
    const wrapper = mount(LibraryToolbar, { props: props({ collapseVersions: true }) });
    const input = wrapper.get('[data-test="collapse-versions-toggle"] input');
    expect(input.element.checked).toBe(true);
    await input.setValue(false);
    expect(wrapper.emitted('update:collapseVersions')).toEqual([[false]]);
    wrapper.unmount();
  });

  it('批量栏「合并为版本组」至少选 2 个才可用，点击向上转发', async () => {
    const videos = [{ id: 1, name: 'a', size: 1, tags: [] }, { id: 2, name: 'b', size: 1, tags: [] }];
    const single = mount(LibraryToolbar, { props: props({ videos, selectedVideoIds: [1] }) });
    expect(single.get('[data-test="batch-version-group"]').attributes('disabled')).toBeDefined();
    single.unmount();
    const wrapper = mount(LibraryToolbar, { props: props({ videos, selectedVideoIds: [1, 2] }) });
    await wrapper.get('[data-test="batch-version-group"]').trigger('click');
    expect(wrapper.emitted('batch-version-group')).toHaveLength(1);
    wrapper.unmount();
  });
});
