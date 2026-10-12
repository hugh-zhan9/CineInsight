import { describe, expect, it } from 'vitest';
import {
  actionSummary, conditionSummary, conditionTypesFor, fieldRow, formToInput, parseRuleError, ruleSummary, ruleToForm
} from './automationRules.js';

const MB = 1024 * 1024;

describe('本地规则 · 前端纯函数', () => {
  it('条件类型按媒体类型过滤（时长/高度/编码只给视频，地理位置只给图片）', () => {
    const video = conditionTypesFor('video').map(item => item.type);
    const image = conditionTypesFor('image').map(item => item.type);
    expect(video).toContain('duration_between');
    expect(video).not.toContain('has_gps');
    expect(image).toContain('has_gps');
    expect(image).not.toContain('height_between');
    expect(image).not.toContain('video_codec_in');
  });

  it('条件与动作的摘要按界面单位显示', () => {
    expect(conditionSummary({ type: 'under_directory', path: '/Volumes/A/下载' })).toBe('在「下载」目录下');
    expect(conditionSummary({ type: 'size_between', min: 500 * MB })).toBe('文件大小 ≥ 500 MB');
    expect(conditionSummary({ type: 'duration_between', min: 600, max: 5400 })).toBe('时长 10–90 分钟');
    expect(conditionSummary({ type: 'height_between', max: 720 })).toBe('画面高度 ≤ 720 像素');
    expect(conditionSummary({ type: 'has_gps', value: false })).toBe('没有地理位置');
    expect(conditionSummary({ type: 'extension_in', exts: ['mkv', 'mp4'] })).toBe('扩展名为 mkv、mp4');
    expect(actionSummary({ type: 'add_tag', tag_id: 1, target_name: '待整理' })).toBe('加标签「待整理」');
    expect(actionSummary({ type: 'add_to_collection', collection_id: 2, target_name: '' })).toBe('加入作品集「（已删除）」');
    expect(ruleSummary({ conditions: [{ type: 'name_contains', text: 'x' }, { type: 'has_gps', value: true }], actions: [] }).conditions)
      .toBe('文件名包含「x」，且有地理位置');
  });

  it('表单与入参互转：MB/分钟换算回字节/秒，空的一端不传，列表按逗号拆分', () => {
    const rule = {
      id: 7, name: '大片', media_kind: 'video', enabled: false,
      conditions: [
        { type: 'size_between', min: 1500 * MB },
        { type: 'duration_between', max: 7200 },
        { type: 'video_codec_in', codecs: ['hevc', 'av1'] }
      ],
      actions: [{ type: 'add_to_collection', collection_id: 3, target_name: '合集' }]
    };
    const form = ruleToForm(rule);
    expect(form.conditions[0].min).toBe('1500');
    expect(form.conditions[1].max).toBe('120');
    form.conditions[2].list = 'hevc，av1 vp9';
    expect(formToInput(form)).toEqual({
      id: 7, name: '大片', media_kind: 'video', enabled: false,
      conditions: [
        { type: 'size_between', min: 1500 * MB },
        { type: 'duration_between', max: 7200 },
        { type: 'video_codec_in', codecs: ['hevc', 'av1', 'vp9'] }
      ],
      actions: [{ type: 'add_to_collection', collection_id: 3 }]
    });
    const fresh = ruleToForm(null);
    expect(fresh).toMatchObject({ id: 0, media_kind: 'video', enabled: true });
    expect(fresh.conditions).toHaveLength(1);
    expect(fresh.actions).toHaveLength(1);
  });

  it('解析后端错误：invalid_rule 带字段，另有 rule_not_found 与 automation_run_busy', () => {
    expect(parseRuleError('invalid_rule: conditions[1].pattern: 正则表达式写法有误')).toEqual({
      code: 'invalid_rule', field: 'conditions[1].pattern', message: '正则表达式写法有误'
    });
    expect(parseRuleError(new Error('rule_not_found')).code).toBe('rule_not_found');
    expect(parseRuleError('automation_run_busy').message).toBe('已有一次规则运行在进行中');
    expect(fieldRow('actions[2].tag_id')).toEqual({ list: 'actions', index: 2 });
    expect(fieldRow('name')).toBeNull();
  });
});
