import { mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import { describe, expect, it } from 'vitest';
import CropTimeline from './CropTimeline.vue';
const props = { videoId: 1, durationMs: 10000, atMs: 2500, editable: true, canStep: true };
async function pointer(node, type, data) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, ...data });
  Object.defineProperty(event, 'pointerId', { value: 1 });
  node.element.dispatchEvent(event); await nextTick();
}
describe('拖拽修剪时间条', () => {
  it('两端把手只在松开时保存区间，拖动时联动画面；取消不保存', async () => {
    const w = mount(CropTimeline, { props });
    const strip = w.get('[data-test="crop-time-strip"]');
    strip.element.getBoundingClientRect = () => ({ left: 0, width: 1000 });
    await pointer(w.get('[data-test="crop-time-start"]'), 'pointerdown', { clientX: 0, button: 0 });
    await pointer(strip, 'pointermove', { clientX: 200 });
    expect(w.emitted('range')).toBeUndefined();
    expect(w.emitted('seek').at(-1)).toEqual([2000]);
    await pointer(strip, 'pointerup', {});
    expect(w.emitted('range')).toHaveLength(1);
    expect(w.emitted('range')[0]).toEqual([{ start_ms: 2000, end_ms: 10000 }]);
    await w.setProps({ range: w.emitted('range')[0][0] });
    await pointer(w.get('[data-test="crop-time-end"]'), 'pointerdown', { clientX: 1000 });
    await pointer(strip, 'pointermove', { clientX: 700 });
    await pointer(strip, 'pointercancel', {});
    expect(w.emitted('range')).toHaveLength(1);
    expect(w.get('[data-test="crop-range-summary"]').text()).toContain('0:10.000');
    w.unmount();
  });
  it('独立游标只浏览，逐帧按钮独立发出请求；忙时禁用', async () => {
    const w = mount(CropTimeline, { props });
    await w.get('[data-test="crop-seek"]').setValue(5000);
    expect(w.emitted('seek').at(-1)).toEqual([5000]);
    expect(w.emitted('range')).toBeUndefined();
    await w.get('[data-test="crop-frame-next"]').trigger('click');
    expect(w.emitted('step-frame')[0]).toEqual([1]);
    await w.setProps({ busy: true });
    expect(w.get('[data-test="crop-frame-prev"]').element.disabled).toBe(true);
    w.unmount();
  });
  it('缩放以当前点为中心，平移不改变选区，支持精确时间/恢复整片', async () => {
    const w = mount(CropTimeline, { props: { ...props, atMs: 5000, range: { start_ms: 2000, end_ms: 8000 } } });
    await w.get('[data-test="crop-time-zoom"]').setValue(10);
    expect(w.get('[data-test="crop-seek"]').attributes('min')).toBe('4500');
    await w.get('[data-test="crop-time-pan"]').setValue(7000);
    expect(w.get('[data-test="crop-seek"]').attributes('min')).toBe('7000');
    expect(w.emitted('range')).toBeUndefined();
    const input = w.get('[data-test="crop-range-start"]');
    await input.setValue('0:02.123');
    expect(w.emitted('range').at(-1)).toEqual([{ start_ms: 2123, end_ms: 8000 }]);
    await input.setValue('0:09.000');
    expect(w.emitted('range')).toHaveLength(1);
    expect(w.text()).toContain('入点须早于出点');
    await w.get('[data-test="crop-range-reset"]').trigger('click');
    expect(w.emitted('range').at(-1)).toEqual([null]);
    w.unmount();
  });
  it('把手不能交叉或越界；只读仍可浏览但不可裁时间', async () => {
    const w = mount(CropTimeline, { props });
    const strip = w.get('[data-test="crop-time-strip"]');
    strip.element.getBoundingClientRect = () => ({ left: 0, width: 1000 });
    await pointer(w.get('[data-test="crop-time-start"]'), 'pointerdown', { clientX: 0 });
    await pointer(strip, 'pointermove', { clientX: 2000 });
    await pointer(strip, 'pointerup', {});
    expect(w.emitted('range')[0][0]).toEqual({ start_ms: 9999, end_ms: 10000 });
    await w.setProps({ editable: false });
    expect(w.get('[data-test="crop-time-end"]').element.disabled).toBe(true);
    expect(w.get('[data-test="crop-seek"]').element.disabled).toBe(false);
    w.unmount();
  });
});
