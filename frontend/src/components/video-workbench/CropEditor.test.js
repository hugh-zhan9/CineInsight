import { nextTick } from 'vue';
import { mount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';
import CropEditor from './CropEditor.vue';
import { dragCrop, fitCropRatio, validCrop } from '../../utils/videoCrop.js';

const recipe = () => ({ v: 1, crop: { items: [{ video_id: 1, rect: { x: 112, y: 10, width: 96, height: 160 }, source_version: '12:34', confirmed: true }] }, tracks: { audio: [], subtitle: [] } });
const preflight = { sources: [{ video_id: 1, duration_ms: 2000, display_width: 320, display_height: 180, source_version: '12:34' }] };
async function pointer(node, type, init = {}) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, ...init });
  Object.defineProperty(event, 'pointerId', { value: init.pointerId });
  node.element.dispatchEvent(event);
  await nextTick();
}
const props = () => ({ recipe: recipe(), editable: true, preflight, videos: { 1: { id: 1, name: 'portrait.mp4' } } });

describe('固定黑边裁剪', () => {
  it('放大不改配方，边距微调和时间区间修改取消确认', async () => {
    const w = mount(CropEditor, { props: props() });
    await w.get('[data-test="crop-zoom"]').setValue(4);
    expect(w.emitted('change')).toBeUndefined();
    expect(w.get('[data-test="crop-stage"]').attributes('style')).toContain('max-width: none');
    await w.get('[data-test="crop-nudge-left-more"]').trigger('click');
    expect(w.emitted('change')[0][0].crop.items[0]).toMatchObject({ rect: { x: 114, width: 94 }, confirmed: false });
    await w.get('[data-test="crop-mark-in"]').trigger('click');
    expect(w.emitted('change').at(-1)[0].crop.items[0]).toMatchObject({ range: { start_ms: 250, end_ms: 2000 }, confirmed: false });
    w.unmount();
  });
  it('真实逐帧返回更新预览；手动定位后丢弃旧响应', async () => {
    let resolve;
    const navigateFrame = vi.fn(() => new Promise(done => { resolve = done; }));
    const w = mount(CropEditor, { props: { ...props(), navigateFrame } });
    await w.get('[data-test="crop-frame-next"]').trigger('click');
    expect(navigateFrame).toHaveBeenCalledWith({ video_id: 1, source_version: '12:34', at_ms: 250, direction: 1 });
    await w.get('[data-test="crop-seek"]').setValue(900);
    resolve({ at_ms: 300 }); await nextTick(); await nextTick();
    expect(w.get('[data-test="crop-stage"] img').attributes('src')).toContain('ms=900');
    navigateFrame.mockResolvedValueOnce({ at_ms: 1000 });
    await w.get('[data-test="crop-frame-next"]').trigger('click'); await nextTick();
    expect(w.get('[data-test="crop-stage"] img').attributes('src')).toContain('ms=1000');
    w.unmount();
  });
  it.each(['detected', 'no_borders', 'undetected'])('预检超时而检测完成（%s）时仍能手动设置并确认', async status => {
    const wrapper = mount(CropEditor, { props: {
      ...props(),
      recipe: { v: 1, crop: { items: [{ video_id: 1, rect: null, source_version: '', confirmed: false }] } },
      preflight: { sources: [{ video_id: 1, error: 'context deadline exceeded' }] },
      analysis: { kind: 'crop', status: 'completed', crops: [{ video_id: 1, status, display_width: 320, display_height: 180, source_version: '12:34', duration_ms: 2000 }] }
    } });
    expect(wrapper.find('[data-test="crop-margin-left"]').exists()).toBe(true);
    await wrapper.get('[data-test="crop-margin-left"]').setValue(100);
    const next = wrapper.emitted('change')[0][0];
    expect(next.crop.items[0]).toMatchObject({ rect: { x: 100, y: 0, width: 220, height: 180 }, source_version: '12:34', confirmed: false });
    await wrapper.setProps({ recipe: next });
    expect(wrapper.get('[data-test="crop-stage"] img').attributes('src')).toContain('v=12%3A34');
    expect(wrapper.get('[data-test="crop-seek"]').attributes('max')).toBe('1999');
    await wrapper.get('[data-test="crop-confirm"]').setValue(true);
    expect(wrapper.emitted('change').at(-1)[0].crop.items[0].confirmed).toBe(true);
    wrapper.unmount();
  });
  it('像素调整使确认失效，输出采用裁剪尺寸；时间选择更新两个预览', async () => {
    const wrapper = mount(CropEditor, { props: props() });
    expect(wrapper.get('[data-test="crop-output-size"]').text()).toContain('96×160');
    await wrapper.get('[data-test="crop-margin-left"]').setValue(110);
    const next = wrapper.emitted('change')[0][0];
    expect(next.crop.items[0]).toMatchObject({ rect: { x: 110, width: 98 }, source_version: '12:34', confirmed: false });
    expect(recipe().crop.items[0].rect.width).toBe(96);
    await wrapper.get('[data-test="crop-seek"]').setValue(1000);
    expect(wrapper.get('[data-test="crop-stage"] img').attributes('src')).toContain('ms=1000');
    expect(wrapper.get('[data-test="crop-result"] img').attributes('src')).toContain('ms=1000');
    wrapper.unmount();
  });
  it('固定比例保留中心，重置和逐项确认可用；过期来源和预览失败不能确认', async () => {
    const wrapper = mount(CropEditor, { props: props() });
    await wrapper.get('[data-test="crop-ratio"]').setValue('1:1');
    expect(wrapper.emitted('change')[0][0].crop.items[0]).toMatchObject({ rect: { x: 112, y: 42, width: 96, height: 96 }, confirmed: false });
    await wrapper.get('[data-test="crop-reset"]').trigger('click');
    expect(wrapper.emitted('change')[1][0].crop.items[0].rect).toEqual({ x: 0, y: 0, width: 320, height: 180 });
    await wrapper.get('[data-test="crop-confirm"]').setValue(false);
    expect(wrapper.emitted('change')[2][0].crop.items[0].confirmed).toBe(false);
    await wrapper.setProps({ recipe: wrapper.emitted('change')[2][0] });
    await wrapper.get('[data-test="crop-stage"] img').trigger('error');
    expect(wrapper.get('[data-test="crop-confirm"]').element.disabled).toBe(true);
    await wrapper.get('[data-test="crop-stage"] img').trigger('load');
    await wrapper.setProps({ preflight: { sources: [{ ...preflight.sources[0], source_version: 'changed' }] } });
    expect(wrapper.get('[data-test="crop-confirm"]').element.disabled).toBe(true);
    await wrapper.setProps({ recipe: recipe() });
    expect(wrapper.get('[data-test="crop-confirm"]').element.disabled).toBe(false);
    await wrapper.get('[data-test="crop-confirm"]').setValue(false);
    const cleared = wrapper.emitted('change').at(-1)[0];
    expect(cleared.crop.items[0].confirmed).toBe(false);
    await wrapper.setProps({ recipe: cleared });
    expect(wrapper.get('[data-test="crop-confirm"]').element.disabled).toBe(true);
    wrapper.unmount();
  });
  it('拖动只在松开时提交，取消不保存；只读不可拖动', async () => {
    const wrapper = mount(CropEditor, { props: props() });
    const stage = wrapper.get('[data-test="crop-stage"]');
    stage.element.getBoundingClientRect = () => ({ width: 320, height: 180 });
    await pointer(wrapper.get('[data-test="crop-box"]'), 'pointerdown', { pointerId: 1, clientX: 120, clientY: 20, button: 0 });
    await pointer(stage, 'pointermove', { pointerId: 1, clientX: 130, clientY: 22 });
    expect(wrapper.emitted('change')).toBeUndefined();
    await pointer(stage, 'pointerup', { pointerId: 1 });
    expect(wrapper.emitted('change')[0][0].crop.items[0]).toMatchObject({ rect: { x: 122, y: 12 }, confirmed: false });
    await pointer(wrapper.get('[data-test="crop-box"]'), 'pointerdown', { pointerId: 2, clientX: 120, clientY: 20 });
    await pointer(stage, 'pointercancel', { pointerId: 2 });
    expect(wrapper.emitted('change')).toHaveLength(1);
    await wrapper.setProps({ editable: false });
    expect(wrapper.get('[data-test="crop-analyze"]').element.disabled).toBe(true);
    expect(wrapper.get('[data-test="crop-margin-left"]').element.disabled).toBe(true);
    wrapper.unmount();
  });
  it('移动/缩放不会越界，极限仍保留至少2像素；9:16近似比例在2像素内', () => {
    const r = { x: 112, y: 10, width: 96, height: 160 };
    expect(dragCrop(r, 'n', 100, 20, 320, 180)).toEqual({ x:112, y:30, width:96, height:140 });
    expect(dragCrop(r, 'e', 20, 100, 320, 180)).toEqual({ x:112, y:10, width:116, height:160 });
    for (const h of ['move', 'nw', 'ne', 'sw', 'se']) {
      for (const dx of [-1000, 0, 1000]) for (const dy of [-1000, 0, 1000]) {
        const result = dragCrop(r, h, dx, dy, 320, 180);
        expect(validCrop(result, 320, 180)).toBe(true);
      }
    }
    const resized = dragCrop({ x: 0, y: 0, width: 100, height: 100 }, 'se', 0, 20, 320, 180, '1:1');
    expect(resized).toEqual({ x: 0, y: 0, width: 120, height: 120 });
    const vertical = fitCropRatio({ x: 0, y: 0, width: 320, height: 180 }, '9:16');
    expect(Math.abs(vertical.width - vertical.height * 9 / 16)).toBeLessThan(2);
    expect(validCrop(vertical, 320, 180)).toBe(true);
  });
  it('自动检测返回不同的框后，固定比例选项同步为自由比例', async () => {
    const wrapper = mount(CropEditor, { props: props() });
    await wrapper.get('[data-test="crop-ratio"]').setValue('1:1');
    await wrapper.setProps({ recipe: wrapper.emitted('change')[0][0] });
    expect(wrapper.get('[data-test="crop-ratio"]').element.value).toBe('1:1');
    await wrapper.setProps({ recipe: recipe() });
    expect(wrapper.get('[data-test="crop-ratio"]').element.value).toBe('free');
    wrapper.unmount();
  });
  it('无可靠检测结果明确提示并可手动编辑；确认项在批次选择中可见', () => {
    const wrapper = mount(CropEditor, { props: { ...props(), analysis: { kind: 'crop', status: 'completed', crops: [{ video_id: 1, status: 'undetected' }] } } });
    expect(wrapper.text()).toContain('未能可靠识别');
    expect(wrapper.get('[data-test="crop-select"]').text()).toContain('已确认');
    wrapper.unmount();
  });
});
