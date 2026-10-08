import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import VirtualVideoList from './VirtualVideoList.vue';

const ROW_HEIGHT = 50;
const rows = ids => ids.map(id => ({ id }));
const range = (from, to) => Array.from({ length: to - from + 1 }, (_, index) => from + index);

let cleanup = null;

afterEach(() => {
  cleanup?.();
  cleanup = null;
});

// 挂进假的 .main-view 滚动宿主。jsdom 不做布局，几何全靠桩：宿主上沿在 0，列表从内容顶端开始，
// 第 i 行（data-virtual-index）的上沿在 i × 行高 − scrollTop；scrollTop 按内容高度钳制，与浏览器一致。
async function mountInScrollOwner({ items, virtualizationEnabled, viewportHeight = 200 }) {
  const host = document.createElement('div');
  host.className = 'main-view';
  document.body.appendChild(host);

  let scrollTop = 0;
  let itemCount = items.length;
  const rect = (top, height) => ({ top, bottom: top + height, left: 0, right: 800, width: 800, height, x: 0, y: top, toJSON() {} });
  Object.defineProperty(host, 'clientHeight', { configurable: true, get: () => viewportHeight });
  Object.defineProperty(host, 'scrollTop', {
    configurable: true,
    get: () => scrollTop,
    set: value => { scrollTop = Math.max(0, Math.min(Number(value) || 0, itemCount * ROW_HEIGHT - viewportHeight)); }
  });

  const originalGetRect = Element.prototype.getBoundingClientRect;
  Element.prototype.getBoundingClientRect = function patchedGetRect() {
    if (this === host) return rect(0, viewportHeight);
    if (this.classList?.contains('virtual-video-list')) return rect(-scrollTop, itemCount * ROW_HEIGHT);
    if (this.hasAttribute?.('data-virtual-row-id')) {
      return rect(Number(this.getAttribute('data-virtual-index')) * ROW_HEIGHT - scrollTop, ROW_HEIGHT);
    }
    return originalGetRect.call(this);
  };

  const wrapper = mount(VirtualVideoList, {
    props: { items, virtualizationEnabled, estimateHeight: () => ROW_HEIGHT, itemVersion: () => '' },
    slots: { default: '<div class="row-body"></div>' },
    attachTo: host
  });
  await flushPromises();

  cleanup = () => {
    wrapper.unmount();
    host.remove();
    Element.prototype.getBoundingClientRect = originalGetRect;
  };
  return {
    wrapper,
    host,
    async setItems(next) {
      itemCount = next.length;
      await wrapper.setProps({ items: next });
    }
  };
}

describe.each([
  ['未虚拟化（macOS WebKit 下的片库）', false],
  ['虚拟化', true]
])('原地替换数据后锚点行留在原位：%s', (_label, virtualizationEnabled) => {
  it('前面插进新行：视口顶端那一行仍停在原来的距离', async () => {
    const list = await mountInScrollOwner({ items: rows(range(1, 10)), virtualizationEnabled });
    list.host.scrollTop = 220;

    // 第 5 行（index 4）上沿在 200 − 220 = −20，是第一条下沿越过视口顶端的行
    const anchor = list.wrapper.vm.captureScrollAnchor();
    expect(anchor).toEqual({ id: 5, offset: -20 });

    await list.setItems(rows([101, 102, ...range(1, 10)]));
    list.wrapper.vm.restoreScrollAnchor(anchor);

    // 第 5 行挪到 index 6：上沿 300 − scrollTop 要回到 −20
    expect(list.host.scrollTop).toBe(320);
  });

  it('锚点行已经不在列表里：滚动位置不动', async () => {
    const list = await mountInScrollOwner({ items: rows(range(1, 10)), virtualizationEnabled });
    list.host.scrollTop = 220;
    const anchor = list.wrapper.vm.captureScrollAnchor();

    await list.setItems(rows([1, 2, 3, 4, ...range(6, 10)]));
    list.wrapper.vm.restoreScrollAnchor(anchor);

    expect(list.host.scrollTop).toBe(220);
  });
});
