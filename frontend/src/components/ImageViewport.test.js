import { enableAutoUnmount, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import ImageViewport from './ImageViewport.vue';

enableAutoUnmount(afterEach);

function viewport(options = {}) {
  const w = mount(ImageViewport, { props: { src: '/test.jpg', imageWidth: 400, imageHeight: 400 }, ...options });
  w.element.getBoundingClientRect = () => ({ left: 10, top: 20, width: 800, height: 600 });
  w.element.setPointerCapture = vi.fn();
  w.vm.measure();
  return w;
}
const pointer = async (w, type, x, y, extra = {}) => {
  w.element.dispatchEvent(new PointerEvent(type, { bubbles: true, pointerId: 1, button: 0, clientX: x + 10, clientY: y + 20, ...extra }));
  await nextTick();
};

describe('ImageViewport background activation', () => {
  it.each([[20, 300], [780, 300], [400, 20], [400, 580]])('signals a stationary click outside the image at %i,%i', async (x, y) => {
    const w = viewport();
    await pointer(w, 'pointerdown', x, y);
    await pointer(w, 'pointerup', x, y);
    expect(w.emitted('background-click')).toHaveLength(1);
  });

  it('keeps clicks on the image, including its boundary, inside the viewer', async () => {
    const w = viewport();
    for (const [x, y] of [[400, 300], [200, 100], [600, 500]]) {
      await pointer(w, 'pointerdown', x, y); await pointer(w, 'pointerup', x, y);
    }
    expect(w.emitted('background-click')).toBeUndefined();
  });

  it('uses the current zoomed and panned image bounds', async () => {
    const w = viewport({ props: { src: '/test.jpg', imageWidth: 400, imageHeight: 400, modelValue: { zoom: 2, x: 0.5, y: 0.5 } } });
    await pointer(w, 'pointerdown', 20, 300); await pointer(w, 'pointerup', 20, 300);
    expect(w.emitted('background-click')).toBeUndefined();
  });

  it.each(['pointercancel', 'lostpointercapture'])('does not activate on %s', async type => {
    const w = viewport();
    await pointer(w, 'pointerdown', 20, 300); await pointer(w, type, 20, 300);
    await pointer(w, 'pointerup', 20, 300);
    expect(w.emitted('background-click')).toBeUndefined();
  });

  it('does not close after dragging out and back, or releasing outside the viewport', async () => {
    const w = viewport();
    await pointer(w, 'pointerdown', 20, 300); await pointer(w, 'pointermove', 70, 300);
    await pointer(w, 'pointermove', 20, 300); await pointer(w, 'pointerup', 20, 300);
    await pointer(w, 'pointerdown', 1, 300); await pointer(w, 'pointerup', -1, 300);
    expect(w.emitted('background-click')).toBeUndefined();
  });

  it('ignores secondary buttons and slotted controls', async () => {
    const w = viewport({ slots: { default: '<button>Action</button>' } });
    await pointer(w, 'pointerdown', 20, 300, { button: 2 }); await pointer(w, 'pointerup', 20, 300, { button: 2 });
    await pointer(w.get('button'), 'pointerdown', 20, 300);
    await pointer(w.get('button'), 'pointerup', 20, 300);
    expect(w.emitted('background-click')).toBeUndefined();
  });
});
