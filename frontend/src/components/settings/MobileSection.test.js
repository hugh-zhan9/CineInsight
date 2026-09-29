import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'ClearShortFeedPIN', 'GetShortFeedAccessStatus', 'GetShortFeedQRCode', 'GetShortFeedServerStatus',
  'SetShortFeedEnabled', 'SetShortFeedPIN', 'UnlockShortFeedLogin'
].map(name => [name, vi.fn()])));
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
vi.mock('../../utils/feedback.js', () => feedback);

import MobileSection, { SHORT_FEED_PIN_BANNER_KEY, shortFeedPINLengthProblem, shortFeedPINProblem } from './MobileSection.vue';

const QR = 'data:image/png;base64,AAAA';
const wrappers = [];
let storage = {};

beforeEach(() => {
  vi.resetAllMocks();
  storage = {};
  vi.stubGlobal('localStorage', {
    getItem: key => (key in storage ? storage[key] : null),
    setItem: (key, value) => { storage[key] = String(value); },
    removeItem: key => { delete storage[key]; }
  });
  api.GetShortFeedServerStatus.mockResolvedValue({ running: true, url: 'http://127.0.0.1:18088/short/', lan_urls: ['http://192.168.1.8:18088/short/'] });
  api.GetShortFeedAccessStatus.mockResolvedValue({ enabled: true, pin_set: false, listening: true, url: 'http://192.168.1.8:18088/short/', login_locked: false });
  api.GetShortFeedQRCode.mockResolvedValue(QR);
  feedback.confirmAction.mockResolvedValue(true);
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  vi.unstubAllGlobals();
});

async function mountSection() {
  const wrapper = mount(MobileSection, { props: { form: { short_feed_max_duration_minutes: 5 } } });
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}
const find = (wrapper, name) => wrapper.get(`[data-test="short-feed-${name}"]`);
const exists = (wrapper, name) => wrapper.find(`[data-test="short-feed-${name}"]`).exists();

describe('手机端访问开关（D-PC45）', () => {
  it('PLAY-01 开关即时启停服务，不走通用保存；启动失败以重读到的状态为准并报错', async () => {
    const wrapper = await mountSection();
    const toggle = () => find(wrapper, 'enabled-toggle');
    expect(toggle().element.checked).toBe(true);

    api.SetShortFeedEnabled.mockResolvedValue(undefined);
    api.GetShortFeedAccessStatus.mockResolvedValue({ enabled: false, pin_set: false, listening: false, login_locked: false });
    api.GetShortFeedServerStatus.mockResolvedValue({ running: false });
    await toggle().setValue(false);
    await flushPromises();
    expect(api.SetShortFeedEnabled).toHaveBeenCalledWith(false);
    expect(toggle().element.checked).toBe(false);
    expect(exists(wrapper, 'qr')).toBe(false);

    api.SetShortFeedEnabled.mockRejectedValue('手机端访问启动失败：端口被占用');
    await toggle().setValue(true);
    await flushPromises();
    expect(find(wrapper, 'toggle-error').text()).toContain('端口被占用');
    expect(toggle().element.checked).toBe(false);
  });

  it('PLAY-01 开着但没设 PIN 时提示建议设置；「不再提示」记在本机', async () => {
    const wrapper = await mountSection();
    expect(find(wrapper, 'pin-banner').text()).toContain('建议设置 PIN');
    await find(wrapper, 'pin-banner-dismiss').trigger('click');
    expect(exists(wrapper, 'pin-banner')).toBe(false);
    expect(storage[SHORT_FEED_PIN_BANNER_KEY]).toBe('1');

    const again = await mountSection();
    expect(exists(again, 'pin-banner')).toBe(false);
  });

  it('PLAY-01 已设 PIN 或服务关着时不显示提示条', async () => {
    api.GetShortFeedAccessStatus.mockResolvedValue({ enabled: true, pin_set: true, listening: true, login_locked: false });
    expect(exists(await mountSection(), 'pin-banner')).toBe(false);
    api.GetShortFeedAccessStatus.mockResolvedValue({ enabled: false, pin_set: false, listening: false, login_locked: false });
    expect(exists(await mountSection(), 'pin-banner')).toBe(false);
  });
});

describe('访问 PIN 与登录锁定（D-PC45）', () => {
  it('PLAY-01 PIN 至少 6 个字符，前端先挡，不发请求', async () => {
    expect(shortFeedPINProblem('12345')).toContain('至少 6');
    expect(shortFeedPINProblem('123456')).toBe('');
    expect(shortFeedPINProblem('x'.repeat(33))).toContain('最多 32');
    expect(shortFeedPINProblem('abc\u0001def')).toContain('控制字符');

    const wrapper = await mountSection();
    await find(wrapper, 'pin-input').setValue('1234');
    await find(wrapper, 'pin-save').trigger('click');
    await flushPromises();
    expect(api.SetShortFeedPIN).not.toHaveBeenCalled();
    expect(find(wrapper, 'pin-message').text()).toContain('至少 6');
  });

  it('PLAY-01 粘贴 40 个字符时提示过长而不是静默截断，保存被拦下', async () => {
    const wrapper = await mountSection();
    const input = find(wrapper, 'pin-input');
    expect(input.attributes('maxlength')).toBeUndefined();
    const pasted = 'p'.repeat(40);
    await input.setValue(pasted);
    // 输入框里是完整的 40 个字符，没有被截成 32 个。
    expect(input.element.value).toBe(pasted);
    expect(find(wrapper, 'pin-length-hint').text()).toContain('最多 32 个字符，当前 40 个');

    await find(wrapper, 'pin-save').trigger('click');
    await flushPromises();
    expect(api.SetShortFeedPIN).not.toHaveBeenCalled();
    expect(find(wrapper, 'pin-message').text()).toContain('最多 32 个字符');
    // 同一句不在两处重复显示。
    expect(exists(wrapper, 'pin-length-hint')).toBe(false);

    // 删回合法长度，提示随之消失。
    await input.setValue('p'.repeat(32));
    expect(exists(wrapper, 'pin-length-hint')).toBe(false);
  });

  it('PLAY-01 字符数没超但超过后端 72 字节上限时同样提示过长', async () => {
    expect(shortFeedPINLengthProblem('汉'.repeat(24))).toBe('');
    expect(shortFeedPINProblem('汉'.repeat(24))).toBe('');
    expect(shortFeedPINLengthProblem('汉'.repeat(25))).toContain('最多 72 字节');
    expect(shortFeedPINProblem('汉'.repeat(25))).toContain('当前 75 字节');
    expect(shortFeedPINProblem('abc\u0085def')).toContain('控制字符');

    const wrapper = await mountSection();
    await find(wrapper, 'pin-input').setValue('汉'.repeat(25));
    expect(find(wrapper, 'pin-length-hint').text()).toContain('72 字节');
    await find(wrapper, 'pin-save').trigger('click');
    await flushPromises();
    expect(api.SetShortFeedPIN).not.toHaveBeenCalled();
  });

  it('PLAY-01 设置 PIN 后清空输入并刷新状态；清除 PIN 先确认', async () => {
    const wrapper = await mountSection();
    api.SetShortFeedPIN.mockResolvedValue(undefined);
    api.GetShortFeedAccessStatus.mockResolvedValue({ enabled: true, pin_set: true, listening: true, login_locked: false });
    await find(wrapper, 'pin-input').setValue('246810');
    await find(wrapper, 'pin-save').trigger('click');
    await flushPromises();
    expect(api.SetShortFeedPIN).toHaveBeenCalledWith('246810');
    expect(find(wrapper, 'pin-input').element.value).toBe('');
    expect(find(wrapper, 'pin-state').text()).toContain('已设置 PIN');
    expect(find(wrapper, 'pin-save').text()).toBe('修改 PIN');

    feedback.confirmAction.mockResolvedValueOnce(false);
    await find(wrapper, 'pin-clear').trigger('click');
    await flushPromises();
    expect(api.ClearShortFeedPIN).not.toHaveBeenCalled();

    api.ClearShortFeedPIN.mockResolvedValue(undefined);
    await find(wrapper, 'pin-clear').trigger('click');
    await flushPromises();
    expect(api.ClearShortFeedPIN).toHaveBeenCalledTimes(1);
  });

  it('PLAY-01 输错太多被锁定时显示解除时间，桌面上可以「解除锁定」', async () => {
    api.GetShortFeedAccessStatus.mockResolvedValue({
      enabled: true, pin_set: true, listening: true, login_locked: true, locked_until: '2026-09-30T08:00:00Z'
    });
    const wrapper = await mountSection();
    expect(find(wrapper, 'login-locked').text()).toContain('自动解除');

    api.UnlockShortFeedLogin.mockResolvedValue(undefined);
    api.GetShortFeedAccessStatus.mockResolvedValue({ enabled: true, pin_set: true, listening: true, login_locked: false });
    await find(wrapper, 'unlock').trigger('click');
    await flushPromises();
    expect(api.UnlockShortFeedLogin).toHaveBeenCalledTimes(1);
    expect(exists(wrapper, 'login-locked')).toBe(false);
    expect(find(wrapper, 'pin-message').text()).toContain('已解除锁定');
  });

  it('PLAY-01 文案不再说「不启用登录或 PIN」，也不再提点赞维护自动标签', async () => {
    const wrapper = await mountSection();
    expect(wrapper.text()).not.toContain('不启用登录或 PIN');
    expect(wrapper.text()).not.toContain('短视频喜欢');
  });
});

describe('手机访问地址的二维码（D-PC47）', () => {
  it('PLAY-14 运行中显示二维码；平台不支持（空串）时不显示', async () => {
    const wrapper = await mountSection();
    expect(find(wrapper, 'qr').find('img').attributes('src')).toBe(QR);
    expect(wrapper.text()).not.toContain('127.0.0.1');

    api.GetShortFeedQRCode.mockResolvedValue('');
    const none = await mountSection();
    expect(none.find('[data-test="short-feed-qr"]').exists()).toBe(false);
    expect(none.get('[data-test="short-feed-phone-url"]').text()).toBe('http://192.168.1.8:18088/short/');
  });
});
