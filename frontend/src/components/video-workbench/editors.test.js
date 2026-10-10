import { mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it } from 'vitest';

import EditExportPanel from './EditExportPanel.vue';
import HDReplaceEditor from './HDReplaceEditor.vue';
import TrimIntroEditor from './TrimIntroEditor.vue';
import { feedbackState, resetFeedback } from '../../utils/feedback.js';

const tracks = () => ({ audio: [], subtitle: [] });
const videos = { 1: { id: 1, name: 'long.mkv', duration: 60 }, 2: { id: 2, name: 'hd.mkv', duration: 50 },
  21: { id: 21, name: 'e01.mkv', duration: 1500 }, 22: { id: 22, name: 'e02.mkv', duration: 1500 } };

function trimRecipe() {
  return { v: 1, trim_intro: { items: [
    { video_id: 21, remove_start_ms: 0, remove_end_ms: 0, origin: 'manual', detect_status: 'undetected', confidence: 0, confirmed: false },
    { video_id: 22, remove_start_ms: 0, remove_end_ms: 90_000, origin: 'detected', detect_status: 'detected', confidence: 0.92, confirmed: false }
  ], analysis_window_ms: 600_000 }, tracks: tracks() };
}

function hdRecipe() {
  return { v: 1, hd_replace: { long_video_id: 1, hd_video_id: 2, segments: [
    { long_start_ms: 10_000, long_end_ms: 20_000, hd_start_ms: 4_000, hd_end_ms: 14_000, audio_source: 'long', origin: 'detected', match_rate: 0.97, status: 'matched', confirmed: false }
  ] }, tracks: tracks() };
}

const fps25 = { video: { frame_rate: '25/1' } };

beforeEach(() => resetFeedback());

describe('批量去片头编辑器', () => {
  it('统一区间应用到全部；未识别项明确标出且未填区间前不能确认', async () => {
    const wrapper = mount(TrimIntroEditor, { props: { recipe: trimRecipe(), editable: true, videos } });
    expect(wrapper.get('[data-test="trim-flag-21"]').text()).toContain('未识别');
    expect(wrapper.get('[data-test="trim-confirm-21"]').attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="trim-uniform-end"]').setValue('1:30');
    await wrapper.get('[data-test="trim-uniform-end"]').trigger('change');
    await wrapper.get('[data-test="trim-apply-uniform"]').trigger('click');
    const next = wrapper.emitted('change')[0][0];
    expect(next.trim_intro.items.map(item => [item.remove_start_ms, item.remove_end_ms, item.origin, item.confirmed]))
      .toEqual([[0, 90_000, 'uniform', false], [0, 90_000, 'uniform', false]]);
  });

  it('全部确认跳过区间无效的项并提示；切点前后帧取自单帧路由', async () => {
    const wrapper = mount(TrimIntroEditor, { props: { recipe: trimRecipe(), editable: true, videos, preflight: { sources: [{ video_id: 22, ...fps25 }] } } });
    await wrapper.get('[data-test="trim-confirm-all"]').trigger('click');
    expect(wrapper.emitted('change')[0][0].trim_intro.items.map(item => item.confirmed)).toEqual([false, true]);
    expect(feedbackState.toasts[0].message).toContain('1 项');
    const frames = wrapper.get('[data-test="trim-item-22"]').findAll('[data-test="edit-frame"] img').map(img => img.attributes('src'));
    expect(frames).toEqual(['/preview/frame/22?ms=89960&w=240', '/preview/frame/22?ms=90000&w=240']);
  });

  it('快速模式显示实际关键帧切点', () => {
    const fast = { fast: { available: true, cut_points: [{ seq: 2, segment: 0, video_id: 22, requested_ms: 90_000, actual_ms: 88_960 }] } };
    const wrapper = mount(TrimIntroEditor, { props: { recipe: trimRecipe(), editable: true, videos, fastPreflight: fast } });
    expect(wrapper.get('[data-test="trim-item-22"] [data-test="edit-fast-actual"]').text()).toContain('1:28.960');
    expect(wrapper.get('[data-test="trim-item-22"]').findAll('img')[1].attributes('src')).toBe('/preview/frame/22?ms=88960&w=240');
  });

  it('不足两项时不能自动识别；只读时不能编辑', () => {
    const single = trimRecipe();
    single.trim_intro.items.pop();
    const wrapper = mount(TrimIntroEditor, { props: { recipe: single, editable: true, videos } });
    expect(wrapper.get('[data-test="trim-analyze"]').attributes('disabled')).toBeDefined();
    const readonly = mount(TrimIntroEditor, { props: { recipe: trimRecipe(), editable: false, videos } });
    expect(readonly.get('[data-test="trim-apply-uniform"]').attributes('disabled')).toBeDefined();
  });
});

describe('高清替换编辑器', () => {
  const preflight = { sources: [{ video_id: 1, duration_ms: 60_000, ...fps25 }, { video_id: 2, duration_ms: 50_000, ...fps25 }] };

  it('±1帧 / ±1秒 / 毫秒输入改长版端点时高清同端平移，两侧时长相等', async () => {
    const wrapper = mount(HDReplaceEditor, { props: { recipe: hdRecipe(), editable: true, videos, preflight } });
    await wrapper.get('[data-test="hd-start-plus-frame-0"]').trigger('click');
    await wrapper.get('[data-test="hd-end-minus-second-0"]').trigger('click');
    await wrapper.get('[data-test="hd-start-ms-0"]').setValue('12000');
    await wrapper.get('[data-test="hd-hdstart-plus-second-0"]').trigger('click');
    const [plusFrame, minusSecond, typed, hdShift] = wrapper.emitted('change').map(([recipe]) => recipe.hd_replace.segments[0]);
    expect(plusFrame).toMatchObject({ long_start_ms: 10_040, hd_start_ms: 4_040, confirmed: false, origin: 'manual' });
    expect(minusSecond).toMatchObject({ long_end_ms: 19_000, hd_end_ms: 13_000 });
    expect(typed).toMatchObject({ long_start_ms: 12_000, hd_start_ms: 6_000 });
    expect(hdShift).toMatchObject({ long_start_ms: 10_000, long_end_ms: 20_000, hd_start_ms: 5_000, hd_end_ms: 15_000 });
  });

  it('帧率未知时 ±1帧 不可用（不编默认帧率）', () => {
    const wrapper = mount(HDReplaceEditor, { props: { recipe: hdRecipe(), editable: true, videos } });
    expect(wrapper.get('[data-test="hd-start-plus-frame-0"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="hd-start-plus-second-0"]').attributes('disabled')).toBeUndefined();
  });

  it('首尾帧并排显示长版与高清；未覆盖的长版区间显示为保留长版', () => {
    const wrapper = mount(HDReplaceEditor, { props: { recipe: hdRecipe(), editable: true, videos, preflight } });
    const srcs = wrapper.get('[data-test="hd-segment-0"]').findAll('img').map(img => img.attributes('src'));
    expect(srcs).toEqual([
      '/preview/frame/1?ms=9960&w=240', '/preview/frame/1?ms=10000&w=240', '/preview/frame/2?ms=4000&w=240',
      '/preview/frame/1?ms=19960&w=240', '/preview/frame/2?ms=13960&w=240', '/preview/frame/1?ms=20000&w=240'
    ]);
    const unmatched = wrapper.get('[data-test="hd-unmatched"]').text();
    expect(unmatched).toContain('保留长版：0:00.000 – 0:10.000');
    expect(unmatched).toContain('保留长版：0:20.000 – 1:00.000');
  });

  it('音频来源可逐段切到高清；确认写回配方', async () => {
    const wrapper = mount(HDReplaceEditor, { props: { recipe: hdRecipe(), editable: true, videos, preflight } });
    await wrapper.get('[data-test="hd-audio-0"]').setValue('hd');
    await wrapper.get('[data-test="hd-confirm-0"]').setValue(true);
    const [audio, confirmed] = wrapper.emitted('change').map(([recipe]) => recipe.hd_replace.segments[0]);
    expect(audio.audio_source).toBe('hd');
    expect(confirmed.confirmed).toBe(true);
  });
});

describe('导出面板的轨道冲突', () => {
  it('冲突项可选来源流、填充或整轨不导出，写进配方 tracks', async () => {
    const project = { id: 1, kind: 'merge', status: 'draft', revision: 2, mode: 'precise', acknowledged_warnings: [] };
    const recipe = { v: 1, merge: { sources: [{ video_id: 1 }, { video_id: 2 }], spec_source_video_id: 0 }, tracks: tracks() };
    const conflict = { seq: 1, kind: 'audio', output_index: 1, video_id: 2, reason: '无法按语言与序号对应',
      candidates: [{ index: 3, language: 'jpn', codec: 'aac', channels: 2 }], allowed_fills: ['stream', 'drop', 'silence'] };
    const preflight = { project_id: 1, revision: 2, mode: 'precise', sources: [], outputs: [], conflicts: [conflict, { ...conflict }],
      fast: { available: true, reasons: [], cut_points: [] }, warnings: [], errors: [{ key: 'track_conflict:1:audio:1:2', message: '音轨第 2 条在视频 2 上无法按语言与序号对应' }], ready: false };
    const wrapper = mount(EditExportPanel, { props: { project, recipe, preflight, editable: true, videos } });
    const select = wrapper.get('[data-test="edit-conflict-audio-1-2"] select');
    expect(wrapper.findAll('[data-test^="edit-conflict-audio"]')).toHaveLength(1);
    expect(select.findAll('option').map(option => option.text())).toEqual(['请选择', '流 #3 · jpn · aac · 2 声道', '整条轨不导出', '该段填静音']);
    await select.setValue('stream:3');
    await select.setValue('silence');
    const [stream, silence] = wrapper.emitted('change').map(([next]) => next.tracks.audio);
    expect(stream).toEqual([{ output: 1, video_id: 2, choice: 'stream', stream_index: 3 }]);
    expect(silence).toEqual([{ output: 1, video_id: 2, choice: 'silence', stream_index: 0 }]);
  });
});
