import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import AutomationRulesPanel, { AUTOMATION_RULES_RUN_EVENT } from './AutomationRulesPanel.vue';
import { feedbackState, resetFeedback, resolveConfirm } from '../../utils/feedback.js';

const rule = (id, extra = {}) => ({
  id, name: `规则 ${id}`, media_kind: 'video', enabled: true, position: id, disabled_reason: '', last_run_at: null,
  conditions: [{ type: 'under_directory', path: '/Volumes/A/下载' }],
  actions: [{ type: 'add_tag', tag_id: 1, target_name: '待整理' }],
  ...extra
});

const IDLE = { running: false, completed: false, cancelled: false, rule_ids: [], total: 0, processed: 0, matched: 0, added: 0, error: '' };
let handlers;

async function mountPanel() {
  const wrapper = mount(AutomationRulesPanel, { attachTo: document.body });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  resetFeedback();
  document.body.innerHTML = '';
  handlers = {};
  window.runtime = { EventsOn: (name, handler) => { handlers[name] = handler; return () => {}; } };
  api.ListAutomationRules.mockResolvedValue([
    rule(1),
    rule(2, { media_kind: 'image', enabled: false, disabled_reason: 'target_deleted', actions: [] })
  ]);
  api.GetAutomationRuleRunStatus.mockResolvedValue(IDLE);
  api.GetAllTags.mockResolvedValue([{ id: 1, name: '待整理' }]);
  api.ListCollections.mockResolvedValue([{ collection: { id: 9, name: '合集' } }]);
});

afterEach(() => {
  delete window.runtime;
});

describe('本地规则面板', () => {
  it('列出规则：启用开关、媒体类型、条件与动作摘要、停用原因', async () => {
    const wrapper = await mountPanel();
    const rows = wrapper.findAll('[data-test="rules-row"]');
    expect(rows).toHaveLength(2);
    expect(rows[0].get('[data-test="rules-toggle"]').element.checked).toBe(true);
    expect(rows[0].get('[data-test="rules-kind"]').text()).toBe('视频');
    expect(rows[0].get('[data-test="rules-conditions"]').text()).toBe('当在「下载」目录下');
    expect(rows[0].get('[data-test="rules-actions"]').text()).toBe('就加标签「待整理」');
    expect(rows[1].get('[data-test="rules-kind"]').text()).toBe('图片');
    expect(rows[1].get('[data-test="rules-disabled-reason"]').text()).toContain('已删除');
    expect(rows[1].get('[data-test="rules-preview"]').element.disabled).toBe(true);
    expect(rows[0].get('[data-test="rules-up"]').element.disabled).toBe(true);
    wrapper.unmount();
  });

  it('启停、下移、删除（确认后）都调用对应接口', async () => {
    api.SetAutomationRuleEnabled.mockResolvedValue(rule(1, { enabled: false }));
    const wrapper = await mountPanel();
    await wrapper.findAll('[data-test="rules-toggle"]')[0].setValue(false);
    await flushPromises();
    expect(api.SetAutomationRuleEnabled).toHaveBeenCalledWith(1, false);
    expect(wrapper.findAll('[data-test="rules-toggle"]')[0].element.checked).toBe(false);

    await wrapper.findAll('[data-test="rules-down"]')[0].trigger('click');
    await flushPromises();
    expect(api.ReorderAutomationRules).toHaveBeenCalledWith([2, 1]);

    await wrapper.findAll('[data-test="rules-delete"]')[0].trigger('click');
    await flushPromises();
    expect(feedbackState.confirm.message).toContain('都会保留');
    resolveConfirm(true);
    await flushPromises();
    expect(api.DeleteAutomationRule).toHaveBeenCalledWith(1);
    wrapper.unmount();
  });

  it('启用失败（例如动作已被删光）：显示原因，开关按重取的列表复原', async () => {
    api.SetAutomationRuleEnabled.mockRejectedValue('invalid_rule: actions: 至少需要一个动作');
    const wrapper = await mountPanel();
    const toggle = wrapper.findAll('[data-test="rules-toggle"]')[1];
    await toggle.setValue(true);
    await flushPromises();
    expect(api.SetAutomationRuleEnabled).toHaveBeenCalledWith(2, true);
    expect(wrapper.get('[data-test="rules-error"]').text()).toBe('至少需要一个动作');
    expect(wrapper.findAll('[data-test="rules-toggle"]')[1].element.checked).toBe(false);
    wrapper.unmount();
  });

  it('新建：保存失败时错误交给编辑框按字段显示，成功后关闭并刷新', async () => {
    api.SaveAutomationRule
      .mockRejectedValueOnce('invalid_rule: actions[0].tag_id: 请选择标签')
      .mockResolvedValueOnce(rule(3));
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="rules-new"]').trigger('click');
    await flushPromises();
    expect(api.ListCollections).toHaveBeenCalledWith('', '', 0, 200);
    await wrapper.get('[data-test="rule-name"]').setValue('新规则');
    await wrapper.get('[data-test="rule-save"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="rule-action-error"]').text()).toBe('请选择标签');
    await wrapper.get('[data-test="rule-action-target"]').setValue('1');
    await wrapper.get('[data-test="rule-save"]').trigger('click');
    await flushPromises();
    expect(api.SaveAutomationRule).toHaveBeenLastCalledWith(expect.objectContaining({ name: '新规则', actions: [{ type: 'add_tag', tag_id: 1 }] }));
    expect(wrapper.find('[data-test="rule-editor"]').exists()).toBe(false);
    expect(api.ListAutomationRules).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it('预览显示条数与示例，点视频示例打开详情，「执行」只跑这一条', async () => {
    api.PreviewAutomationRule.mockResolvedValue({
      rule_id: 1, count: 51,
      samples: [{ media_kind: 'video', id: 42, name: 'a.mkv', directory: '/Volumes/A/下载' }]
    });
    api.RunAutomationRules.mockResolvedValue({ ...IDLE, running: true, rule_ids: [1] });
    const wrapper = await mountPanel();
    await wrapper.findAll('[data-test="rules-preview"]')[0].trigger('click');
    await flushPromises();
    expect(api.PreviewAutomationRule).toHaveBeenCalledWith(1);
    expect(wrapper.get('[data-test="rules-preview-count"]').text()).toContain('51');
    await wrapper.get('[data-test="rules-preview-sample"] button').trigger('click');
    expect(wrapper.emitted('open-video-id')[0]).toEqual([42]);
    await wrapper.get('[data-test="rules-preview-run"]').trigger('click');
    await flushPromises();
    expect(api.RunAutomationRules).toHaveBeenCalledWith([1]);
    expect(wrapper.get('[data-test="rules-run-status"]').text()).toContain('正在准备');
    wrapper.unmount();
  });

  it('对现有内容运行：只带启用且有动作的规则；跟进度事件，可取消；结束后刷新并通知片库', async () => {
    api.RunAutomationRules.mockResolvedValue({ ...IDLE, running: true, rule_ids: [1] });
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="rules-run-all"]').trigger('click');
    await flushPromises();
    expect(api.RunAutomationRules).toHaveBeenCalledWith([1]);
    handlers[AUTOMATION_RULES_RUN_EVENT]({ ...IDLE, running: true, total: 600, processed: 500, added: 12 });
    await flushPromises();
    expect(wrapper.get('[data-test="rules-run-status"]').text()).toContain('500/600');
    expect(wrapper.get('[data-test="rules-run-all"]').element.disabled).toBe(true);
    await wrapper.get('[data-test="rules-run-cancel"]').trigger('click');
    expect(api.CancelAutomationRuleRun).toHaveBeenCalled();
    handlers[AUTOMATION_RULES_RUN_EVENT]({ ...IDLE, completed: true, cancelled: true, total: 600, processed: 500, added: 12 });
    await flushPromises();
    expect(wrapper.get('[data-test="rules-run-status"]').text()).toContain('已取消');
    expect(wrapper.emitted('videos-changed')).toHaveLength(1);
    expect(api.ListAutomationRules).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it('已有运行时报忙；没有规则时给出说明', async () => {
    api.RunAutomationRules.mockRejectedValue('automation_run_busy');
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="rules-run-all"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="rules-error"]').text()).toBe('已有一次规则运行在进行中');
    wrapper.unmount();
    api.ListAutomationRules.mockResolvedValue([]);
    const empty = await mountPanel();
    expect(empty.get('[data-test="rules-empty"]').text()).toContain('新建规则');
    expect(empty.get('[data-test="rules-run-all"]').element.disabled).toBe(true);
    empty.unmount();
  });
});
