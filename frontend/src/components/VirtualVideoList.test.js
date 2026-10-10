import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import VirtualVideoList from './VirtualVideoList.vue';
import { reactive } from 'vue';

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
  let width = 800;
  let rowHeight = ROW_HEIGHT;
  let wrapper;
  let itemCount = items.length;
  const rect = (top, height) => ({ top, bottom: top + height, left: 0, right: width, width, height, x: 0, y: top, toJSON() {} });
  Object.defineProperty(host, 'clientHeight', { configurable: true, get: () => viewportHeight });
  Object.defineProperty(host, 'scrollTop', {
    configurable: true,
    get: () => scrollTop,
    set: value => { scrollTop = Math.max(0, Math.min(Number(value) || 0, itemCount * rowHeight - viewportHeight)); }
  });

  const originalGetRect = Element.prototype.getBoundingClientRect;
  Element.prototype.getBoundingClientRect = function patchedGetRect() {
    if (this === host) return rect(0, viewportHeight);
    if (this.classList?.contains('virtual-video-list')) return rect(-scrollTop, itemCount * rowHeight);
    if (this.hasAttribute?.('data-virtual-row-id')) {
      const i = Number(this.getAttribute('data-virtual-index'));
      const top = virtualizationEnabled ? (wrapper?.vm.topSpacer || 0) + (i - (wrapper?.vm.startIndex || 0)) * rowHeight : i * rowHeight;
      return rect(top - scrollTop, rowHeight);
    }
    return originalGetRect.call(this);
  };

  wrapper = mount(VirtualVideoList, {
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
    scroll(top) { host.scrollTop = top; host.dispatchEvent(new Event('scroll')); },
    async resize(nextWidth, nextHeight = rowHeight) { width = nextWidth; rowHeight = nextHeight; wrapper.vm.handleWidthChange(); await flushPromises(); },
    visible() { const nodes = wrapper.findAll('[data-virtual-row-id]'); const row = nodes.find(node => node.element.getBoundingClientRect().bottom > 0); return row ? { id: Number(row.attributes('data-virtual-row-id')), offset: row.element.getBoundingClientRect().top } : null; },
    async setItems(next) {
      itemCount = next.length;
      await wrapper.setProps({ items: next });
    }
  };
}

describe.each([
  ['未虚拟化兼容入口', false],
  ['虚拟化', true]
])('原地替换数据后锚点行留在原位：%s', (_label, virtualizationEnabled) => {
  it('automatically preserves old DOM identity when new rows arrive before the anchor', async () => {
    const list = await mountInScrollOwner({ items: rows(range(1, 20)), virtualizationEnabled });
    list.scroll(220);
    await flushPromises();
    await list.setItems(rows([101, 102, ...range(1, 20)]));
    await flushPromises();
    expect(list.host.scrollTop).toBe(320);
    expect(list.visible()).toEqual({ id: 5, offset: -20 });
  });
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


describe('window lifecycle', () => {
  it('reindexes in-place page appends, replacements and deletions used by the library caller', async () => {
    const items = reactive(rows(range(1, 10)));
    const list = await mountInScrollOwner({ items, virtualizationEnabled: true });
    items.push(...rows(range(11, 20)));
    await flushPromises();
    expect(list.wrapper.vm.layoutIndex.count).toBe(20);
    expect(list.wrapper.vm.layoutIndex.indexOf(20)).toBe(19);
    items.splice(0, 2, { id: 100 });
    await flushPromises();
    expect(list.wrapper.vm.layoutIndex.indexOf(1)).toBe(-1);
    expect(list.wrapper.vm.layoutIndex.indexOf(100)).toBe(0);
    expect(list.wrapper.vm.layoutIndex.indexOf(20)).toBe(18);
  });
  it('preserves the visible row through width and measured-height changes, not the overscan row', async () => {
    const list = await mountInScrollOwner({ items: rows(range(1, 1000)), virtualizationEnabled: true });
    list.scroll(20020);
    await flushPromises();
    const anchor = list.visible();
    await list.resize(500, 100);
    expect(list.visible()).toEqual(anchor);
    await list.resize(800, 50);
    expect(list.visible()).toEqual(anchor);
  });

  it('owns page activation including position zero and rejects late activation writes', async () => {
    const list = await mountInScrollOwner({ items: rows(range(1, 100)), virtualizationEnabled: true });
    list.scroll(0);
    await list.wrapper.setProps({ active: false });
    list.scroll(900);
    await list.wrapper.setProps({ active: true, itemVersion: () => '' });
    await flushPromises();
    expect(list.host.scrollTop).toBe(0);
    list.scroll(320);
    await list.wrapper.setProps({ active: false });
    list.scroll(900);
    const activation = list.wrapper.setProps({ active: true });
    await list.wrapper.setProps({ active: false });
    await activation;
    await flushPromises();
    expect(list.host.scrollTop).toBe(900);
  });

  it('does not measure or navigate the shared owner while hidden; rebuilds on return', async () => {
    const list = await mountInScrollOwner({ items: rows(range(1, 1000)), virtualizationEnabled: true });
    list.scroll(2020);
    await flushPromises();
    const anchor = list.visible();
    await list.wrapper.setProps({ active: false });
    list.scroll(700);
    await list.resize(500, 100);
    await list.setItems(rows([1001, 1002, ...range(1, 1000)]));
    list.wrapper.vm.scrollToItem(80);
    await list.wrapper.vm.measureVisibleRows();
    expect(list.host.scrollTop).toBe(700);
    await list.wrapper.setProps({ active: true, itemVersion: () => 'updated while hidden' });
    await flushPromises();
    expect(list.visible()).toEqual(anchor);
  });

  it('old query anchors cannot restore into a replacement query with the same IDs', async () => {
    const list = await mountInScrollOwner({ items: rows(range(1, 100)), virtualizationEnabled: true });
    list.scroll(2020);
    await flushPromises();
    const old = list.wrapper.vm.captureScrollAnchor();
    list.scroll(0);
    await list.wrapper.setProps({ queryKey: 'new', items: rows(range(1, 100)) });
    await flushPromises();
    list.wrapper.vm.restoreScrollAnchor(old);
    expect(list.host.scrollTop).toBe(0);
  });
});
