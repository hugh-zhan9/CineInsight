import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'CancelSubtitleEnginePreparation', 'GetSubtitleEngineStatuses', 'PrepareSubtitleEngine',
  'PrepareAppleSubtitleLanguage'
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

const apple = (extra = {}) => ({
  engine: 'apple', display_name: 'Apple 系统', supported: true, available: false, needs_prepare: true,
  prepare_hint: '需要先准备 Apple 系统 运行时', reason_message: '', ...extra
});

beforeEach(() => {
  vi.resetAllMocks();
  for (const key of Object.keys(handlers)) delete handlers[key];
  window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
  api.GetSubtitleEngineStatuses.mockResolvedValue([
    apple()
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
    expect(wrapper.get('[data-test="subtitle-engine-apple"]').text()).toContain('需要先准备 Apple 系统 运行时');
    expect(wrapper.find('[data-test="subtitle-engine-prepare-apple"]').exists()).toBe(true);
    expect(wrapper.find('input[type="password"]').exists()).toBe(false);
    expect(wrapper.findAll('.subtitle-engine-row')).toHaveLength(1);
  });

  it('macOS 15 语音识别权限被拒时显示去系统设置的说明，系统翻译语言仍可准备', async () => {
    const guidance = 'apple_speech_permission_denied: 请在「系统设置 › 隐私与安全性 › 语音识别」中允许析微影策后重试';
    api.GetSubtitleEngineStatuses.mockResolvedValue([apple({
      supported: false, needs_prepare: false, reason_message: guidance,
      translation_languages: [{ code: 'en', name: '英语', installed: false }, { code: 'zh', name: '中文', installed: false }]
    })]);
    api.PrepareAppleSubtitleLanguage.mockResolvedValue(undefined);
    const wrapper = await mountSection();
    expect(wrapper.get('[data-test="subtitle-engine-apple"]').text()).toContain('「系统设置 › 隐私与安全性 › 语音识别」中允许析微影策');
    expect(wrapper.get('[data-test="subtitle-engine-apple"]').text()).not.toContain('不支持');
    expect(wrapper.findAll('[data-test="apple-translation-source"] option').map(option => option.attributes('value'))).toEqual(['en', 'zh']);
    await wrapper.get('[data-test="apple-translation-prepare"]').trigger('click');
    await flushPromises();
    expect(api.PrepareAppleSubtitleLanguage).toHaveBeenCalledWith('en', 'zh', true);
  });

  it('D-004 说明 macOS 15 起可用，以及 macOS 15 上的较早识别引擎与听写语言下载', async () => {
    const wrapper = await mountSection();
    const text = wrapper.text();
    expect(text).toContain('需要 macOS 15 或更新版本');
    expect(text).toContain('「系统设置 › 键盘 › 听写」');
    expect(text).not.toContain('macOS 26');
  });

  it('MEDIA-13 准备过程显示中文进度，可以取消；取消不当失败', async () => {
    const wrapper = await mountSection();
    const pending = deferred();
    api.PrepareSubtitleEngine.mockReturnValue(pending.promise);
    await wrapper.get('[data-test="subtitle-engine-prepare-apple"]').trigger('click');
    await flushPromises();
    expect(api.PrepareSubtitleEngine).toHaveBeenCalledWith('apple');

    handlers['subtitle-progress']({ action: 'prepare', engine: 'apple', phase: 'downloading-model', percent: 40, message: 'Downloading 40%' });
    await flushPromises();
    const progress = wrapper.get('[data-test="subtitle-engine-progress"]').text();
    expect(progress).toContain('正在下载模型（40%）');
    expect(progress).not.toContain('Downloading');

    api.CancelSubtitleEnginePreparation.mockResolvedValue(undefined);
    await wrapper.get('[data-test="subtitle-engine-cancel"]').trigger('click');
    expect(api.CancelSubtitleEnginePreparation).toHaveBeenCalledTimes(1);
    handlers['subtitle-progress']({ action: 'prepare', engine: 'apple', phase: 'cancelled', percent: 0, message: '已取消准备' });
    pending.reject('已取消字幕引擎准备');
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-engine-progress"]').exists()).toBe(false);
    const message = wrapper.get('[data-test="subtitle-engine-message"]');
    expect(message.text()).toContain('已取消准备');
    expect(message.classes()).not.toContain('settings-error');
  });

  it('MEDIA-13 别处（生成字幕弹窗）发起的准备在这里也看得到并能取消；完成后刷新状态', async () => {
    const wrapper = await mountSection();
    handlers['subtitle-progress']({ action: 'prepare', engine: 'apple', phase: 'preparing-runtime', percent: 10, message: '正在准备 Apple 系统 运行时…' });
    await flushPromises();
    expect(wrapper.get('[data-test="subtitle-engine-progress"]').text()).toContain('正在准备运行时');
    expect(wrapper.get('[data-test="subtitle-engine-prepare-apple"]').attributes('disabled')).toBeDefined();
    // 生成字幕的进度事件不算准备。
    handlers['subtitle-progress']({ action: 'generate', phase: 'transcribing', percent: 50 });
    await flushPromises();
    expect(wrapper.get('[data-test="subtitle-engine-progress"]').text()).toContain('正在准备运行时');

    api.GetSubtitleEngineStatuses.mockResolvedValue([apple({ available: true, needs_prepare: false })]);
    handlers['subtitle-prepare-complete']({ engine: 'apple' });
    await flushPromises();
    expect(wrapper.find('[data-test="subtitle-engine-progress"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="subtitle-engine-apple"]').text()).toContain('已就绪');
  });

  it('MEDIA-13 准备失败给出原因，不静默', async () => {
    const wrapper = await mountSection();
    api.PrepareSubtitleEngine.mockRejectedValue('pip 安装失败');
    await wrapper.get('[data-test="subtitle-engine-prepare-apple"]').trigger('click');
    await flushPromises();
    const message = wrapper.get('[data-test="subtitle-engine-message"]');
    expect(message.text()).toContain('准备字幕引擎失败：pip 安装失败');
    expect(message.classes()).toContain('settings-error');
  });
});

describe('苹果系统语言准备', () => {
  it('通过系统接口准备翻译语言，不保存API配置', async () => {
    api.GetSubtitleEngineStatuses.mockResolvedValue([apple({ available: true, needs_prepare: false, translation_languages: [{ code: 'en', name: '英语' }, { code: 'zh', name: '中文' }] })]);
    api.PrepareAppleSubtitleLanguage.mockResolvedValue(undefined);
    const wrapper = await mountSection();
    await wrapper.get('[data-test="apple-translation-prepare"]').trigger('click');
    await flushPromises();
    expect(api.PrepareAppleSubtitleLanguage).toHaveBeenCalledWith('en', 'zh', true);
    expect(wrapper.text()).toContain('系统语言资源已准备好');
    expect(wrapper.find('input[type="password"]').exists()).toBe(false);
  });
});
