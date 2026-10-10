import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { reactive } from 'vue';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import SceneSection from './SceneSection.vue';
import { feedbackState, resetFeedback, resolveConfirm } from '../../utils/feedback.js';

const wrappers = [];
function mountSection(form, extra = {}) {
  const wrapper = mount(SceneSection, { props: { form, ...extra } });
  wrappers.push(wrapper);
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  window.runtime = { EventsOn: () => () => {} };
  api.GetSceneRuntimeStatus.mockResolvedValue({ state: 'missing_model', reason: '场景检索模型尚未下载', model_size: '182 MB', runtime_dir: '/data/scene-runtime' });
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
});

describe('设置页「场景检索」分区（D-MW-SCENES）', () => {
  it('切到外部描述先显示披露；取消则保持本地，确认后才写入表单', async () => {
    const form = reactive({ scene_visual_provider: 'local', scene_visual_interval_seconds: 5, scene_model_mirror_url: '' });
    const wrapper = mountSection(form);
    await flushPromises();
    const external = wrapper.get('[data-test="scene-provider-external"]');

    await external.setValue(true);
    await flushPromises();
    expect(feedbackState.confirm?.title).toBe('启用外部描述');
    expect(feedbackState.confirm?.message).toContain('采样帧缩略图');
    expect(form.scene_visual_provider).toBe('local');
    resolveConfirm(false);
    await flushPromises();
    expect(form.scene_visual_provider).toBe('local');
    expect(external.element.checked).toBe(false);
    expect(wrapper.get('[data-test="scene-provider-local"]').element.checked).toBe(true);
    expect(wrapper.find('[data-test="scene-external-disclosure"]').exists()).toBe(false);

    await external.setValue(true);
    await flushPromises();
    resolveConfirm(true);
    await flushPromises();
    expect(form.scene_visual_provider).toBe('external');
    expect(wrapper.get('[data-test="scene-external-disclosure"]').text()).toContain('AI 标签');

    await wrapper.get('[data-test="scene-provider-local"]').setValue(true);
    await flushPromises();
    expect(feedbackState.confirm).toBeNull();
    expect(form.scene_visual_provider).toBe('local');
  });

  it('显示运行时状态与联网披露；准备前按已保存的镜像主机确认保存', async () => {
    const form = reactive({ scene_visual_provider: 'local', scene_visual_interval_seconds: 5, scene_model_mirror_url: 'https://hf-mirror.com' });
    const ensureSaved = vi.fn(() => Promise.resolve(true));
    api.PrepareSceneRuntime.mockResolvedValue({ state: 'missing_model', preparing: true, message: '正在下载', total_bytes: 200, downloaded_bytes: 50 });
    const wrapper = mountSection(form, { ensureSaved });
    await flushPromises();
    expect(wrapper.get('[data-test="scene-privacy-note"]').text()).toContain('PyPI');
    expect(wrapper.get('[data-test="scene-runtime-reason"]').text()).toContain('尚未下载');
    await wrapper.get('[data-test="scene-prepare-runtime"]').trigger('click');
    await flushPromises();
    expect(ensureSaved).toHaveBeenCalledWith(['scene_model_mirror_url'], '准备模型');
    expect(api.PrepareSceneRuntime).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="scene-runtime-progress"]').text()).toContain('25%');
    await wrapper.get('[data-test="scene-cancel-prepare"]').trigger('click');
    await flushPromises();
    expect(api.CancelSceneRuntimePrepare).toHaveBeenCalledTimes(1);
  });

  it('镜像与间隔直接绑定到表单（随设置页保存）', async () => {
    const form = reactive({ scene_visual_provider: 'local', scene_visual_interval_seconds: 5, scene_model_mirror_url: '' });
    const wrapper = mountSection(form);
    await flushPromises();
    await wrapper.get('[data-test="scene-mirror-url"]').setValue('https://hf-mirror.com');
    await wrapper.get('[data-test="scene-interval"]').setValue('10');
    expect(form.scene_model_mirror_url).toBe('https://hf-mirror.com');
    expect(form.scene_visual_interval_seconds).toBe(10);
  });
});
