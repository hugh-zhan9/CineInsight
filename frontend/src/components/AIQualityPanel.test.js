import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetAIQualityReport: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import AIQualityPanel from './AIQualityPanel.vue';

const emptyReport = () => ({
  tag_summary: { decided: 0, approved: 0, rejected: 0, approval_rate: null, rejection_rate: null },
  tag_groups: [],
  same_source_summary: { decided: 0, approved: 0, rejected: 0, approval_rate: null, rejection_rate: null },
  same_source_groups: [],
  run_summary: { total: 0, completed: 0, skipped: 0, failed: 0, processing: 0, failure_rate: null },
  run_groups: [],
});

beforeEach(() => {
  vi.clearAllMocks();
});

describe('AIQualityPanel', () => {
  it('shows loading and empty states without invoking any AI action', async () => {
    let resolveReport;
    api.GetAIQualityReport.mockReturnValue(new Promise(resolve => { resolveReport = resolve; }));
    const wrapper = mount(AIQualityPanel);
	await wrapper.vm.$nextTick();
    expect(wrapper.text()).toContain('正在汇总本地质量记录');

    resolveReport(emptyReport());
    await flushPromises();
    expect(wrapper.find('[data-test="quality-empty"]').exists()).toBe(true);
    expect(api.GetAIQualityReport).toHaveBeenCalledTimes(1);
    expect(Object.keys(api)).toEqual(['GetAIQualityReport']);
  });

  it('applies local report filters and renders metrics', async () => {
    api.GetAIQualityReport.mockResolvedValue({
      ...emptyReport(),
      tag_summary: { decided: 2, approved: 1, rejected: 1, approval_rate: 0.5, rejection_rate: 0.5 },
      tag_groups: [{ tag_id: 7, tag_name: '动作', confidence: 'high', model_identifier: 'model-a', prompt_schema_version: 'v2', decided: 2, approved: 1, rejected: 1, approval_rate: 0.5, rejection_rate: 0.5 }],
      run_summary: { total: 2, completed: 1, skipped: 0, failed: 1, processing: 0, failure_rate: 0.5, duration_p50_ms: 1200, duration_p95_ms: 1800, average_requests: 2, average_tool_calls: 1 },
      run_groups: [],
    });
    const wrapper = mount(AIQualityPanel, { props: { tags: [{ id: 7, name: '动作' }] } });
    await flushPromises();
    expect(wrapper.text()).toContain('50.0%');
    expect(wrapper.text()).toContain('动作');

    await wrapper.find('[data-test="quality-window"]').setValue('7d');
    await wrapper.find('[data-test="quality-tag"]').setValue('7');
    await wrapper.find('[data-test="quality-confidence"]').setValue('high');
    await wrapper.find('[data-test="quality-model"]').setValue('model-a');
    await wrapper.find('[data-test="quality-apply"]').trigger('click');
    await flushPromises();

    expect(api.GetAIQualityReport).toHaveBeenLastCalledWith(expect.objectContaining({
      window: '7d', tag_id: 7, confidence: 'high', model_identifier: 'model-a',
    }));
  });

  it('shows a recoverable error state', async () => {
    api.GetAIQualityReport.mockRejectedValueOnce(new Error('database unavailable')).mockResolvedValueOnce(emptyReport());
    const wrapper = mount(AIQualityPanel);
    await flushPromises();
    expect(wrapper.text()).toContain('加载 AI 质量报告失败');
    await wrapper.find('.ai-quality-error button').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="quality-empty"]').exists()).toBe(true);
  });
});
