// 在线资料源分区（P-009）：三家凭证、资料源出网代理，以及每个源一个连接探测按钮。
//
// 这个文件同时守着一条**不在本组件里**的规矩：设置页的保存载荷是显式对象字面量，
// 五个新键必须在里面。漏掉的话，在别的分区改一项设置再保存，就会把配好的代理和
// 凭证全部清空——见文末那两条用例。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));

// 自动补桩：设置页挂载时十几个分区各自会调后端，这里一律给一个 resolve(null)，
// 用例只对自己关心的那几个再单独设返回值。
const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import OnlineSourceSection from './OnlineSourceSection.vue';
import SettingsPage from '../SettingsPage.vue';

const SOURCE_CREDENTIALS = {
  metadata_proxy_url: 'socks5://127.0.0.1:1080',
  tmdb_api_key: 'tmdb-key',
  bangumi_access_token: 'bangumi-token',
  fanza_api_id: 'fanza-id',
  fanza_affiliate_id: 'fanza-affiliate-990'
};

function mountSection(form = {}) {
  return mount(OnlineSourceSection, { props: { form: { ...SOURCE_CREDENTIALS, ...form } } });
}

async function clickTest(wrapper, source) {
  await wrapper.get(`[data-test="online-source-test-${source}"]`).trigger('click');
  await flushPromises();
}

const resultText = (wrapper, source) => wrapper.get(`[data-test="online-source-result-${source}"]`).text();

beforeEach(() => {
  vi.clearAllMocks();
});

describe('OnlineSourceSection', () => {
  it('四个源各有一个测试按钮，代理与三家凭证各有一个输入框', () => {
    const wrapper = mountSection();
    for (const source of ['tmdb', 'bangumi', 'fanza', 'javbus']) {
      expect(wrapper.find(`[data-test="online-source-test-${source}"]`).exists(), source).toBe(true);
    }
    for (const field of ['proxy-url', 'tmdb-api-key', 'bangumi-access-token', 'fanza-api-id', 'fanza-affiliate-id']) {
      expect(wrapper.find(`[data-test="online-source-${field}"]`).exists(), field).toBe(true);
    }
  });

  // 代理项的名字必须和「播放代理」分区区分开。两者同名会造成真实误解：那个说的是
  // 本地转码缓存，跟出网没有半点关系。
  it('代理项叫「资料源出网代理」，并点明与播放代理无关', () => {
    const text = mountSection().text();
    expect(text).toContain('资料源出网代理');
    expect(text).toContain('播放代理');
  });

  // 四种成因要产生四条不同且可读的结果，而不是一句"连接失败"。
  it('凭证缺失、凭证无效、代理不通、网络不可达各显示各自的说明', async () => {
    const cases = [
      { failure: 'credential_missing', message: '凭证缺失：还没填「TMDB API Key」' },
      { failure: 'credential_invalid', message: '凭证无效：源站拒绝了这套凭证（HTTP 401）' },
      { failure: 'proxy_unreachable', message: '代理不通：检查「资料源出网代理」的地址与端口' },
      { failure: 'network_unreachable', message: '网络不可达：15 秒内没能连上源站' }
    ];
    const seen = new Set();
    for (const testCase of cases) {
      api.TestWatchlistMetadataConnection.mockResolvedValue({
        ok: false, source: 'tmdb', failure: testCase.failure, proxy_url: '', message: testCase.message
      });
      const wrapper = mountSection();
      await clickTest(wrapper, 'tmdb');
      const rendered = resultText(wrapper, 'tmdb');
      expect(rendered, testCase.failure).toContain(testCase.message);
      expect(seen.has(rendered), `${testCase.failure} 与前一种成因显示了同一句话`).toBe(false);
      seen.add(rendered);
    }
  });

  // 同一句"网络不可达"，直连和走代理的下一步完全不同，结果必须说清这次走的哪条路。
  it('结果注明这次是直连还是经代理', async () => {
    api.TestWatchlistMetadataConnection.mockResolvedValue({ ok: true, source: 'tmdb', proxy_url: '', message: '连接正常' });
    let wrapper = mountSection();
    await clickTest(wrapper, 'tmdb');
    expect(resultText(wrapper, 'tmdb')).toContain('直连');

    api.TestWatchlistMetadataConnection.mockResolvedValue({
      ok: true, source: 'tmdb', proxy_url: 'socks5://127.0.0.1:1080', message: '连接正常'
    });
    wrapper = mountSection();
    await clickTest(wrapper, 'tmdb');
    expect(resultText(wrapper, 'tmdb')).toContain('经代理 socks5://127.0.0.1:1080');
  });

  // 探测用表单当前值发起，不必先保存——照 AI 标签分区「测试连接」的既有做法。
  it('用表单当前值探测，且不触发保存', async () => {
    const wrapper = mountSection();
    await wrapper.get('[data-test="online-source-proxy-url"]').setValue('http://127.0.0.1:7890');
    await wrapper.get('[data-test="online-source-tmdb-api-key"]').setValue('刚改的-key');
    await clickTest(wrapper, 'tmdb');

    expect(api.TestWatchlistMetadataConnection).toHaveBeenCalledWith({
      source: 'tmdb',
      proxy_url: 'http://127.0.0.1:7890',
      tmdb_api_key: '刚改的-key'
    });
    expect(api.UpdateSettings).not.toHaveBeenCalled();
  });

  // 只带被测源用得到的凭证：探测 Bangumi 没有理由把 TMDB 的 Key 一起递过去。
  it('每个源只递自己的凭证', async () => {
    const wrapper = mountSection();

    await clickTest(wrapper, 'bangumi');
    expect(api.TestWatchlistMetadataConnection).toHaveBeenLastCalledWith({
      source: 'bangumi', proxy_url: SOURCE_CREDENTIALS.metadata_proxy_url, bangumi_access_token: 'bangumi-token'
    });

    await clickTest(wrapper, 'fanza');
    expect(api.TestWatchlistMetadataConnection).toHaveBeenLastCalledWith({
      source: 'fanza', proxy_url: SOURCE_CREDENTIALS.metadata_proxy_url,
      fanza_api_id: 'fanza-id', fanza_affiliate_id: 'fanza-affiliate-990'
    });

    // JavBus 不要凭证，一个都不该带。
    await clickTest(wrapper, 'javbus');
    expect(api.TestWatchlistMetadataConnection).toHaveBeenLastCalledWith({
      source: 'javbus', proxy_url: SOURCE_CREDENTIALS.metadata_proxy_url
    });
  });

  // 后端压根没答话时不能说"直连"——我们并不知道这次走的哪条路。
  it('探测失败时把错误显示出来，不编造走的哪条路，也不让按钮卡在测试中', async () => {
    api.TestWatchlistMetadataConnection.mockRejectedValue(new Error('boom'));
    const wrapper = mountSection();
    await clickTest(wrapper, 'tmdb');

    const rendered = resultText(wrapper, 'tmdb');
    expect(rendered).toContain('boom');
    expect(rendered).not.toContain('直连');
    expect(rendered).not.toContain('经代理');
    expect(wrapper.get('[data-test="online-source-test-tmdb"]').attributes('disabled')).toBeUndefined();
  });
});

// 设置页的保存载荷是显式对象字面量，不是展开。五个新键漏掉任何一个，后端都会照
// 白名单把它赋成空串——用户在别的分区改一项设置再保存，配好的代理和凭证就没了。
// 这与该文件里 BrowserBridgeToken 那条事故记录是同一类。
describe('设置页保存载荷带上在线资料源五项', () => {
  const baseSettings = () => ({
    video_extensions: '.mp4',
    play_weight: 2,
    random_half_life_days: 90,
    theme: 'system',
    subtitle_translation_provider: 'deepl',
    subtitle_whisperx_model: 'medium',
    subtitle_whisperx_batch_size: 8,
    backup_directory: '',
    backup_retention_count: 7,
    backup_interval_hours: 24,
    ...SOURCE_CREDENTIALS
  });

  async function mountSettingsPage(settings = baseSettings()) {
    api.GetAITagLibrary.mockResolvedValue([]);
    api.SaveAITagLibrary.mockResolvedValue([]);
    api.ClearAITagLibrary.mockResolvedValue([]);
    api.UpdateSettings.mockResolvedValue();
    api.TriggerAITagging.mockResolvedValue(false);
    api.GetLibraryWatcherStatus.mockResolvedValue({ running: false, roots: [] });
    const wrapper = mount(SettingsPage, { props: { settings, directories: [] } });
    await flushPromises();
    return wrapper;
  }

  it('分区挂进了设置页，锚点导航里也有它', async () => {
    const wrapper = await mountSettingsPage();
    expect(wrapper.findComponent({ name: 'OnlineSourceSection' }).exists()).toBe(true);
    expect(wrapper.find('#settings-online-sources').exists()).toBe(true);
    expect(wrapper.text()).toContain('在线资料源');
  });

  it('保存时回传代理与三家凭证的当前值', async () => {
    const wrapper = await mountSettingsPage();
    await wrapper.get('.settings-save-button').trigger('click');
    await flushPromises();

    expect(api.UpdateSettings).toHaveBeenCalledTimes(1);
    expect(api.UpdateSettings.mock.calls[0][0]).toMatchObject(SOURCE_CREDENTIALS);
  });

  // 判据里点名的那一条：在别的分区改一项设置并保存，代理与凭证不丢。
  it('在别的分区改一项设置并保存，代理与凭证不被清空', async () => {
    const wrapper = await mountSettingsPage();
    wrapper.vm.settingsForm.play_weight = 5;
    await wrapper.get('.settings-save-button').trigger('click');
    await flushPromises();

    const payload = api.UpdateSettings.mock.calls[0][0];
    expect(payload.play_weight).toBe(5);
    expect(payload).toMatchObject(SOURCE_CREDENTIALS);
  });

  // 后端读到 undefined 与读到空串是两回事：Go 侧 models.Settings 的字段是字符串，
  // 漏传等于传空串。这里钉住"键一定在"，而不只是"值对得上"。
  it('五个键一个都不能少，未配置时发空串而不是 undefined', async () => {
    const wrapper = await mountSettingsPage({ ...baseSettings(), ...Object.fromEntries(Object.keys(SOURCE_CREDENTIALS).map(key => [key, undefined])) });
    await wrapper.get('.settings-save-button').trigger('click');
    await flushPromises();

    const payload = api.UpdateSettings.mock.calls[0][0];
    for (const key of Object.keys(SOURCE_CREDENTIALS)) {
      expect(Object.hasOwn(payload, key), key).toBe(true);
      expect(payload[key], key).toBe('');
    }
  });
});
