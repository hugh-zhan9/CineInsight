import { afterEach, describe, expect, it } from 'vitest';
import { enableAutoUnmount, mount } from '@vue/test-utils';
import ImageCompareDialog from './ImageCompareDialog.vue';
import ImageViewport from './ImageViewport.vue';
enableAutoUnmount(afterEach);
const photo = (id, width = 4000, height = 2000) => ({ id, width, height, name: `image ${id}` });
async function wheel(viewport) {
  viewport.element.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: -200, clientX: 300, clientY: 150 }));
  await viewport.vm.$nextTick();
}

function compare() {
  const wrapper = mount(ImageCompareDialog, { props: { left: photo(1), right: photo(2, 2000, 1000) }, global: { stubs: { teleport: true } } });
  for (const viewport of wrapper.findAllComponents(ImageViewport)) {
    viewport.element.getBoundingClientRect = () => ({ width: 400, height: 300, left: 0, top: 0 }); viewport.vm.measure();
  }
  return wrapper;
}

describe('synchronized image comparison', () => {
  it('compares a small enhancement and source at the same display size without stretching the source', async () => {
    const wrapper = compare();
    await wrapper.setProps({ left: photo(3, 100, 50), right: photo(4, 200, 100), sameDisplaySize: true });
    const [left, right] = wrapper.findAllComponents(ImageViewport);
    expect(left.vm.geometry.width).toBe(100); expect(right.vm.geometry.width).toBe(100);
    expect(left.vm.geometry.scale).toBe(1); expect(right.vm.geometry.scale).toBe(0.5);
    right.vm.toggleNatural(); await wrapper.vm.$nextTick();
    expect(right.vm.geometry.scale).toBe(1); expect(left.vm.geometry.scale).toBe(2);
  });

  it('keeps the same focal point in two images while displaying their own pixel scale', async () => {
    const wrapper = compare(); await wrapper.vm.$nextTick();
    const [left, right] = wrapper.findAllComponents(ImageViewport);
    await wheel(left);
    expect(left.vm.geometry.x).toBeCloseTo(right.vm.geometry.x);
    expect(left.vm.geometry.y).toBeCloseTo(right.vm.geometry.y);
    expect(left.vm.geometry.scale * 2).toBeCloseTo(right.vm.geometry.scale);
    expect(left.vm.geometry.zoom).toBeGreaterThan(1);
    const rightWidth = right.get('img').attributes('style');
    await wrapper.get('[data-test="image-compare-link"]').trigger('click');
    await wheel(left);
    expect(right.get('img').attributes('style')).toBe(rightWidth);
    await wrapper.get('[data-test="image-compare-reset"]').trigger('click');
    expect(left.vm.geometry.zoom).toBe(1); expect(right.vm.geometry.zoom).toBe(1);
  });

  it('isolates failed loads and resets when a source changes', async () => {
    const wrapper = compare();
    await wrapper.findAllComponents(ImageViewport)[0].get('img').trigger('error');
    expect(wrapper.findAllComponents(ImageViewport)).toHaveLength(1);
    expect(wrapper.text()).toContain('没有加载出来');
    await wrapper.setProps({ left: photo(3) });
    expect(wrapper.findAllComponents(ImageViewport)).toHaveLength(2);
    expect(wrapper.findAll('img')[0].attributes('src')).toBe('/preview/image/3');
    expect(wrapper.vm.sharedView).toEqual({ zoom: 1, x: 0.5, y: 0.5 });
  });
});
