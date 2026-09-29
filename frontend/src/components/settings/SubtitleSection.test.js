import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'CancelSubtitleEnginePreparation', 'GetSubtitleEngineStatuses', 'PrepareSubtitleEngine',
  'ListGlossaryEntries', 'UpsertGlossaryEntry', 'DeleteGlossaryEntry'
].map(name => [name, vi.fn()])));
vi.mock('../../../wailsjs/go/main/App', () => api);

import SubtitleSection from './SubtitleSection.vue';

const handlers = {};
const wrappers = [];
const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
};

const whisperx = (extra = {}) => ({
  engine: 'whisperx', display_name: 'WhisperX', supported: true, available: false, needs_prepare: true,
  prepare_hint: '需要先准备 WhisperX 运行时', reason_message: '', ...extra
});

beforeEach(() => {
  vi.resetAllMocks();
  for (const key of Object.keys(handlers)) delete handlers[key];
  window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
  api.ListGlossaryEntries.mockResolvedValue([]);
  api.GetSubtitleEngineStatuses.mockResolvedValue([
    whisperx(),
    { engine: 'qwen', display_name: 'Qwen ASR', supported: false, available: false, needs_prepare: false, reason_message: '仅支持 Apple Silicon' }
  ]);
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  delete window.runtime;
});

async function mountSection() {
  const wrapper = mount(SubtitleSection, { props: { form: { subtitle_translation_provider: 'deepl' } } });
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}

describe('字幕引擎状态与准备（D-PC22）', () => {
  it('MEDIA-13 设置页列出引擎状态，需要准备的给「准备引擎」，不支持的说明原因', async () => {
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="subtitle-engine-whisperx"]').text()).toContain('需要先准备 WhisperX 运行时');
    expect(wrapper.find('[data-test="subtitle-engine-prepare-whisperx"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="subtitle-engine-qwen"]').text()).toContain('仅支持 Apple Silicon');
    expect(wrapper.find('[data-test="subtitle-engine-prepare-qwen"]').exists()).toBe(false);
  });

  it('MEDIA-13 准备过程显示中文进度，可以取消；取消不当失败', async () => {
    const wrapper = await mountSection();
    const pending = deferred();
    api.PrepareSubtitleEngine.mockReturnValue(pending.promise);
    await wrapper.get('[data-test="subtitle-engine-prepare-whisperx"]').trigger('click');
    await flushPromises();
    expect(api.PrepareSubtitleEngine).toHaveBeenCalledWith('whisperx');

    handlers['subtitle-progress']({ action: 'prepare', engine: 'whisperx', phase: 'downloading-model', percent: 40, message: 'Downloading 40%' });
    await flushPromises();
    const progress = wrapper.get('[data-test="subtitle-engine-progress"]').text();
    expect(progress).toContain('正在下载模型（40%）');
    expect(progress).not.toContain('Downloading');

    api.CancelSubtitleEnginePreparation.mockResolvedValue(undefined);
    await wrapper.get('[data-test="subtitle-engine-cancel"]').trigger('click');
    expect(api.CancelSubtitleEnginePreparation).toHaveBeenCalledTimes(1);
    handlers['subtitle-progress']({ action: 'prepare', engine: 'whisperx', phase: 'cancelled', percent: 0, message: '已取消准备' });
    pending.reject('已取消字幕引擎准备');
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-engine-progress"]').exists()).toBe(false);
    const message = wrapper.get('[data-test="subtitle-engine-message"]');
    expect(message.text()).toContain('已取消准备');
    expect(message.classes()).not.toContain('settings-error');
  });

  it('MEDIA-13 别处（生成字幕弹窗）发起的准备在这里也看得到并能取消；完成后刷新状态', async () => {
    const wrapper = await mountSection();
    handlers['subtitle-progress']({ action: 'prepare', engine: 'whisperx', phase: 'preparing-runtime', percent: 10, message: '正在准备 WhisperX 运行时…' });
    await flushPromises();
    expect(wrapper.get('[data-test="subtitle-engine-progress"]').text()).toContain('正在准备运行时');
    expect(wrapper.get('[data-test="subtitle-engine-prepare-whisperx"]').attributes('disabled')).toBeDefined();
    // 生成字幕的进度事件不算准备。
    handlers['subtitle-progress']({ action: 'generate', phase: 'transcribing', percent: 50 });
    await flushPromises();
    expect(wrapper.get('[data-test="subtitle-engine-progress"]').text()).toContain('正在准备运行时');

    api.GetSubtitleEngineStatuses.mockResolvedValue([whisperx({ available: true, needs_prepare: false })]);
    handlers['subtitle-prepare-complete']({ engine: 'whisperx' });
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-engine-progress"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="subtitle-engine-whisperx"]').text()).toContain('已就绪');
  });

  it('MEDIA-13 准备失败给出原因，不静默', async () => {
    const wrapper = await mountSection();
    api.PrepareSubtitleEngine.mockRejectedValue('pip 安装失败');
    await wrapper.get('[data-test="subtitle-engine-prepare-whisperx"]').trigger('click');
    await flushPromises();
    const message = wrapper.get('[data-test="subtitle-engine-message"]');
    expect(message.text()).toContain('准备字幕引擎失败：pip 安装失败');
    expect(message.classes()).toContain('settings-error');
  });
});
