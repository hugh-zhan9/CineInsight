import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import AutomationRuleEditor from './AutomationRuleEditor.vue';

const TAGS = [
  { id: 1, name: '待整理', namespace: '' },
  { id: 2, name: '短视频', namespace: '自动', automatic_kind: 'short_video' },
  { id: 3, name: '某人', namespace: '人物' }
];
const COLLECTIONS = [{ id: 9, name: '合集' }];

function mountEditor(props = {}) {
  return mount(AutomationRuleEditor, { props: { tags: TAGS, collections: COLLECTIONS, ...props }, attachTo: document.body });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.innerHTML = '';
});

describe('规则编辑', () => {
  it('新建：选文件夹、选标签后保存出完整入参；自动标签不在可选项里', async () => {
    api.SelectDirectory.mockResolvedValue('/Volumes/A/下载');
    const wrapper = mountEditor();
    await wrapper.get('[data-test="rule-name"]').setValue('下载目录打待整理');
    await wrapper.get('[data-test="rule-condition-pick"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="rule-condition-path"]').element.value).toBe('/Volumes/A/下载');
    const options = wrapper.get('[data-test="rule-action-target"]').findAll('option').map(option => option.text());
    expect(options).toEqual(['选择标签', '待整理', '人物 / 某人']);
    await wrapper.get('[data-test="rule-action-target"]').setValue('1');
    await wrapper.get('[data-test="rule-save"]').trigger('click');
    expect(wrapper.emitted('save')[0][0]).toEqual({
      id: 0, name: '下载目录打待整理', media_kind: 'video', enabled: true,
      conditions: [{ type: 'under_directory', path: '/Volumes/A/下载' }],
      actions: [{ type: 'add_tag', tag_id: 1 }]
    });
    wrapper.unmount();
  });

  it('换成图片：去掉只适用于视频的条件与「加入作品集」，条件下拉只剩图片适用的类型', async () => {
    const wrapper = mountEditor({
      rule: null
    });
    await wrapper.get('[data-test="rule-condition-type"]').setValue('duration_between');
    await wrapper.get('[data-test="rule-action-type"]').setValue('add_to_collection');
    await wrapper.get('[data-test="rule-kind"]').setValue('image');
    const types = wrapper.get('[data-test="rule-condition-type"]').findAll('option').map(option => option.element.value);
    expect(types).toContain('has_gps');
    expect(types).not.toContain('duration_between');
    expect(wrapper.get('[data-test="rule-condition-type"]').element.value).toBe('under_directory');
    expect(wrapper.get('[data-test="rule-action-type"]').findAll('option').map(option => option.element.value)).toEqual(['add_tag']);
    wrapper.unmount();
  });

  it('后端校验错误按字段落到对应的行；编辑已有规则时不能换媒体类型', async () => {
    const rule = {
      id: 5, name: '正则', media_kind: 'video', enabled: true,
      conditions: [{ type: 'under_directory', path: '/a' }, { type: 'name_regex', pattern: '([' }],
      actions: [{ type: 'add_tag', tag_id: 1, target_name: '待整理' }]
    };
    const wrapper = mountEditor({ rule, error: { code: 'invalid_rule', field: 'conditions[1].pattern', message: '正则表达式写法有误' } });
    const rows = wrapper.findAll('[data-test="rule-condition"]');
    expect(rows[0].find('[data-test="rule-condition-error"]').exists()).toBe(false);
    expect(rows[1].get('[data-test="rule-condition-error"]').text()).toBe('正则表达式写法有误');
    expect(wrapper.find('[data-test="rule-error"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="rule-kind"]').element.disabled).toBe(true);
    await wrapper.setProps({ error: { code: '', field: '', message: '数据库忙' } });
    expect(wrapper.get('[data-test="rule-error"]').text()).toBe('数据库忙');
    wrapper.unmount();
  });

  it('区间条件按界面单位输入，留空的一端不传', async () => {
    const wrapper = mountEditor();
    await wrapper.get('[data-test="rule-condition-type"]').setValue('size_between');
    await wrapper.get('[data-test="rule-condition-min"]').setValue('500');
    await wrapper.get('[data-test="rule-save"]').trigger('click');
    expect(wrapper.emitted('save')[0][0].conditions).toEqual([{ type: 'size_between', min: 500 * 1024 * 1024 }]);
    wrapper.unmount();
  });
});
