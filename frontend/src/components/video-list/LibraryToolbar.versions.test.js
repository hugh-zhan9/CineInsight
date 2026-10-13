// 工具栏上的多版本聚合入口（D-MW-VERSIONS）：结果条「合并版本」开关。批量条的「合并为版本组」「对比播放」在 BatchActionBar.test.js。
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

});
