// 拆分前这条断言查的是 VideoListPage 渲染出的文案（VideoListPage.test.js
// 「抽取后接管主列表」）。状态条抽成独立组件后 DOM 断言搬到这里。
import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import RandomPickBanner from './RandomPickBanner.vue';

const mountBanner = (randomPick, extra = {}) =>
  mount(RandomPickBanner, { props: { randomPick, randomPickSize: 10, videoCount: 2, ...extra } });

describe('随机批次状态条', () => {
  it('未进入随机批次时整条不渲染', () => {
    const wrapper = mountBanner({ active: false, ids: [], reason: '', loading: false });
    expect(wrapper.find('[data-test="random-pick-banner"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('报出批量大小、抽取理由与当前条数', () => {
    const wrapper = mountBanner({ active: true, ids: [11, 12], reason: '在当前筛选范围内仅选择未看视频', loading: false });
    expect(wrapper.find('[data-test="random-pick-banner"]').exists()).toBe(true);
    expect(wrapper.text()).toContain('随机 10 部');
    expect(wrapper.text()).toContain('在当前筛选范围内仅选择未看视频');
    expect(wrapper.text()).toContain('当前 2 条');
    wrapper.unmount();
  });

  it('「换一批」与「退出随机」交回片库页；抽取途中两个按钮都禁用', async () => {
    const wrapper = mountBanner({ active: true, ids: [11], reason: '', loading: false });
    const buttons = wrapper.findAll('.random-pick-banner__actions button');
    await buttons[0].trigger('click');
    await buttons[1].trigger('click');
    expect(wrapper.emitted('reshuffle')).toHaveLength(1);
    expect(wrapper.emitted('exit')).toHaveLength(1);

    await wrapper.setProps({ randomPick: { active: true, ids: [11], reason: '', loading: true } });
    const busy = wrapper.findAll('.random-pick-banner__actions button');
    expect(busy[0].attributes('disabled')).toBeDefined();
    expect(busy[1].attributes('disabled')).toBeDefined();
    expect(busy[0].text()).toBe('抽取中...');
    wrapper.unmount();
  });

  it('PLAY-08 随机播放结果条：报出片名与理由，窗口内提示「换一个」不计入', async () => {
    const wrapper = mountBanner(
      { active: false, ids: [], reason: '', loading: false },
      { randomPlay: { active: true, videoName: '黑客帝国', reason: '在当前筛选范围内仅选择未看视频', rerollable: true, loading: false } }
    );
    expect(wrapper.find('[data-test="random-pick-banner"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="random-play-banner"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="random-play-name"]').text()).toBe('「黑客帝国」');
    expect(wrapper.text()).toContain('在当前筛选范围内仅选择未看视频');
    expect(wrapper.find('[data-test="random-play-hint"]').text()).toBe('30 秒内「换一个」不计入这次播放');

    await wrapper.find('[data-test="random-play-reroll"]').trigger('click');
    await wrapper.find('[data-test="random-play-dismiss"]').trigger('click');
    expect(wrapper.emitted('reroll')).toHaveLength(1);
    expect(wrapper.emitted('dismiss-play')).toHaveLength(1);
    wrapper.unmount();
  });

  it('PLAY-08 过了 30 秒改说已计入，「换一个」是重新随机；换一个进行中按钮禁用', async () => {
    const wrapper = mountBanner(
      { active: false, ids: [], reason: '', loading: false },
      { randomPlay: { active: true, videoName: 'a.mp4', reason: '', rerollable: false, loading: false } }
    );
    expect(wrapper.find('[data-test="random-play-hint"]').text()).toBe('这次随机已计入播放记录，「换一个」会重新随机一部');

    await wrapper.setProps({ randomPlay: { active: true, videoName: 'a.mp4', reason: '', rerollable: true, loading: true } });
    expect(wrapper.find('[data-test="random-play-reroll"]').attributes('disabled')).toBeDefined();
    expect(wrapper.find('[data-test="random-play-reroll"]').text()).toBe('换一个中...');
    wrapper.unmount();
  });
});
