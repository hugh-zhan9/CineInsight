import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  GetSemanticIndexStatus: vi.fn(),
  StartSemanticIndex: vi.fn(),
  CancelSemanticIndex: vi.fn(),
  TestAITaggingConnection: vi.fn(),
}));
vi.mock('../../../wailsjs/go/main/App', () => api);
vi.mock('../PhotoAITaskPanel.vue', () => ({ default: { template: '<div />' } }));

import AITagSection from './AITagSection.vue';

// 打标失败率高时分不清是接口不通还是抽帧出错：设置页给一个用表单当前值发请求的测试按钮。
describe('AITagSection connection test', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    delete window.runtime;
    api.GetSemanticIndexStatus.mockResolvedValue({ available: true });
  });

  it('sends the form values and shows the backend verdict', async () => {
    api.TestAITaggingConnection.mockResolvedValue({ ok: true, message: '连接正常，模型已响应（120 ms）', reply: 'OK' });
    const form = { ai_tagging_base_url: 'http://127.0.0.1:1234/v1', ai_tagging_api_key: 'k', ai_tagging_model: 'vision-x' };
    const wrapper = mount(AITagSection, { props: { form } });
    await flushPromises();

    await wrapper.get('[data-test="ai-connection-test"]').trigger('click');
    await flushPromises();

    expect(api.TestAITaggingConnection).toHaveBeenCalledWith({ base_url: 'http://127.0.0.1:1234/v1', api_key: 'k', model: 'vision-x' });
    const result = wrapper.get('[data-test="ai-connection-result"]');
    expect(result.text()).toContain('连接正常');
    expect(result.text()).toContain('回复「OK」');
    expect(result.classes()).toContain('ai-connection-test__result--ok');
  });

  it('renders a failed probe in the error style, including a thrown binding error', async () => {
    api.TestAITaggingConnection.mockResolvedValueOnce({ ok: false, message: 'API Key 无效或没有权限（HTTP 401）' });
    const wrapper = mount(AITagSection, { props: { form: { ai_tagging_base_url: 'http://x', ai_tagging_model: 'm' } } });
    await flushPromises();
    await wrapper.get('[data-test="ai-connection-test"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="ai-connection-result"]').classes()).toContain('ai-connection-test__result--error');
    expect(wrapper.text()).toContain('HTTP 401');

    api.TestAITaggingConnection.mockRejectedValueOnce(new Error('bridge down'));
    await wrapper.get('[data-test="ai-connection-test"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="ai-connection-result"]').text()).toContain('测试失败');
  });
});
