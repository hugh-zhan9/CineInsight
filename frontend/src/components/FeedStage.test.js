import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import FeedStage from '../short-feed/components/FeedStage.vue';

// 横屏素材不能按竖屏 feed 那样 cover 裁切；方向先按后端宽高预判，元数据到了以实际尺寸为准。
describe('短视频舞台横竖屏适配', () => {
  const video = extra => ({ media_kind: 'video', id: 1, name: 'clip', media_url: '/short-media/video/1', ...extra });

  function setIntrinsic(el, width, height) {
    Object.defineProperty(el, 'videoWidth', { value: width, configurable: true });
    Object.defineProperty(el, 'videoHeight', { value: height, configurable: true });
  }

  it('后端给出横屏宽高时一开始就按横屏完整显示', () => {
    const wrapper = mount(FeedStage, { props: { item: video({ width: 1920, height: 1080 }) } });
    expect(wrapper.find('video').classes()).toContain('feed-video--landscape');
  });

  it('竖屏视频保持 feed 的铺满裁切', () => {
    const wrapper = mount(FeedStage, { props: { item: video({ width: 1080, height: 1920 }) } });
    expect(wrapper.find('video').classes()).not.toContain('feed-video--landscape');
  });

  it('没有宽高时以解码后的实际尺寸为准，换条目后重置', async () => {
    const wrapper = mount(FeedStage, { props: { item: video({ width: 0, height: 0 }) } });
    const player = wrapper.find('video');
    expect(player.classes()).not.toContain('feed-video--landscape');
    setIntrinsic(player.element, 1280, 720);
    await player.trigger('loadedmetadata');
    expect(player.classes()).toContain('feed-video--landscape');
    expect(wrapper.emitted('media-loaded')).toHaveLength(1);
    // 同一条目被后端回传覆盖（评分、标签）不重置结论。
    await wrapper.setProps({ item: video({ width: 0, height: 0, liked: true }) });
    expect(wrapper.find('video').classes()).toContain('feed-video--landscape');
    // 换到另一条竖屏视频，回到按记录预判。
    await wrapper.setProps({ item: video({ id: 2, width: 1080, height: 1920 }) });
    expect(wrapper.find('video').classes()).not.toContain('feed-video--landscape');
  });

  it('实际尺寸与记录相反时以实际为准（旋转元数据的手机视频）', async () => {
    const wrapper = mount(FeedStage, { props: { item: video({ width: 1920, height: 1080 }) } });
    const player = wrapper.find('video');
    setIntrinsic(player.element, 1080, 1920);
    await player.trigger('loadedmetadata');
    expect(player.classes()).not.toContain('feed-video--landscape');
  });
});
