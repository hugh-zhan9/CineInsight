// 拆分前这几条断言查的是 VideoListPage 渲染出的 DOM（VideoListPage.test.js 的
// 「语义检索降级」一节）。提示条抽成独立组件后，DOM 断言搬到这里，
// 片库页那侧只保留「状态确实传下来了」。
import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import SemanticNoticeBar from './SemanticNoticeBar.vue';

const mountBar = (props = {}) => mount(SemanticNoticeBar, { props });

describe('语义检索提示条', () => {
  it('能力不可用时原因常驻可见，不用等到搜索之后', () => {
    const wrapper = mountBar({
      semanticAvailable: false,
      semanticUnavailableNotice: '语义搜索不可用：数据库缺少 pgvector 扩展'
    });
    const bar = wrapper.find('[data-test="semantic-unavailable"]');
    expect(bar.exists()).toBe(true);
    expect(bar.text()).toContain('pgvector');
    wrapper.unmount();
  });

  it('能力可用时不出提示条；不在语义模式时整条都不渲染', () => {
    const wrapper = mountBar({ semanticAvailable: true, searchMode: 'file' });
    expect(wrapper.find('[data-test="semantic-unavailable"]').exists()).toBe(false);
    expect(wrapper.find('.scan-sync-status').exists()).toBe(false);
    wrapper.unmount();
  });

  it('语义模式下按覆盖率 / 失败 / 未搜索三种情况给不同文案', () => {
    const coverage = mountBar({ searchMode: 'semantic', semanticCoverage: { indexed: 3, total: 10 } });
    expect(coverage.text()).toContain('语义索引覆盖 3/10');
    coverage.unmount();

    const empty = mountBar({ searchMode: 'semantic' });
    expect(empty.text()).toContain('用自然语言描述想找的内容');
    empty.unmount();

    const failed = mountBar({ searchMode: 'semantic', semanticSearchError: 'pgvector missing' });
    expect(failed.text()).toContain('语义搜索失败');
    expect(failed.attributes('role')).toBe('alert');
    failed.unmount();
  });

  it('把后端原始错误翻成用户看得懂的话', () => {
    const cases = [
      ['semantic_index_rebuild_required', '需要重建'],
      ['尚未建立语义索引', '索引补全'],
      ['pgvector', 'pgvector 扩展']
    ];
    for (const [raw, expected] of cases) {
      const wrapper = mountBar({ searchMode: 'semantic', semanticSearchError: raw });
      expect(wrapper.vm.semanticSearchErrorText).toContain(expected);
      wrapper.unmount();
    }
    // 认不出来的原样透出，不吞掉。
    const other = mountBar({ searchMode: 'semantic', semanticSearchError: '连接超时' });
    expect(other.vm.semanticSearchErrorText).toBe('连接超时');
    other.unmount();
  });
});
