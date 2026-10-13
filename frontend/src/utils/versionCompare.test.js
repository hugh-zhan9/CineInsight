// 版本对比播放器的纯规则与全局开关（2026-10-12）：纠偏阈值、偏移方向与取整、跟随位置夹取、
// 一侧出声、滑动分界与裁切、播放源与标注，以及 openVersionCompare / closeVersionCompare 的合同。
import { beforeEach, describe, expect, it } from 'vitest';
import {
  COMPARE_DRIFT_THRESHOLD, COMPARE_OFFSET_LIMIT, COMPARE_SYNC_INTERVAL_MS,
  applyCompareOffset, closeVersionCompare, compareClipPath, compareDriftSeek, compareFollowerTarget, compareMediaErrorText,
  compareMuteState, compareSideLabel, compareSliderPercent, compareSourceFromSession, formatCompareOffset, formatCompareTime,
  openVersionCompare, versionCompareState
} from './versionCompare.js';

describe('全局开关合同', () => {
  beforeEach(() => closeVersionCompare());

  it('openVersionCompare 记下左右 ID、打开并开启新会话；closeVersionCompare 关掉并清空', () => {
    const before = versionCompareState.session;
    expect(openVersionCompare(3, '7')).toBe(true);
    expect(versionCompareState).toMatchObject({ open: true, leftVideoID: 3, rightVideoID: 7, session: before + 1 });
    // 开着时换一对：新会话，宿主据此重挂载（偏移清零）。
    expect(openVersionCompare(7, 9)).toBe(true);
    expect(versionCompareState).toMatchObject({ open: true, leftVideoID: 7, rightVideoID: 9, session: before + 2 });
    closeVersionCompare();
    expect(versionCompareState).toMatchObject({ open: false, leftVideoID: 0, rightVideoID: 0 });
  });

  it('同一个视频、缺 ID 或非法 ID 不打开', () => {
    const session = versionCompareState.session;
    for (const [left, right] of [[3, 3], [0, 4], [4, 0], [-1, 2], [1.5, 2], ['x', 2], [null, undefined]]) {
      expect(openVersionCompare(left, right)).toBe(false);
    }
    expect(versionCompareState.open).toBe(false);
    expect(versionCompareState.session).toBe(session);
  });
});

describe('跟随位置与纠偏', () => {
  it('常量在约定范围内：每 250–500 ms 查一次，偏差阈值约 0.15 秒', () => {
    expect(COMPARE_SYNC_INTERVAL_MS).toBeGreaterThanOrEqual(250);
    expect(COMPARE_SYNC_INTERVAL_MS).toBeLessThanOrEqual(500);
    expect(COMPARE_DRIFT_THRESHOLD).toBe(0.15);
  });

  it('右侧位置 = 左侧 + 偏移；正偏移右侧更靠后，负偏移更靠前', () => {
    expect(compareFollowerTarget(10, 2.5, 100)).toEqual({ time: 12.5, inRange: true });
    expect(compareFollowerTarget(10, -2.5, 100)).toEqual({ time: 7.5, inRange: true });
    expect(compareFollowerTarget(10, 0, NaN)).toEqual({ time: 10, inRange: true });
  });

  it('超出右侧范围时夹到 0 或片尾，并标出不在范围内', () => {
    expect(compareFollowerTarget(1, -3, 100)).toEqual({ time: 0, inRange: false });
    expect(compareFollowerTarget(99, 3, 100)).toEqual({ time: 100, inRange: false });
    // 时长未知（元数据还没到）不设上限。
    expect(compareFollowerTarget(5000, 3, 0)).toEqual({ time: 5003, inRange: true });
  });

  it('偏差超过阈值才纠偏，返回右侧要拖到的位置', () => {
    expect(compareDriftSeek({ leaderTime: 10, followerTime: 10.1, offset: 0, followerDuration: 100 })).toBeNull();
    expect(compareDriftSeek({ leaderTime: 10, followerTime: 10.14, offset: 0, followerDuration: 100 })).toBeNull();
    expect(compareDriftSeek({ leaderTime: 10, followerTime: 10.16, offset: 0, followerDuration: 100 })).toBe(10);
    expect(compareDriftSeek({ leaderTime: 10, followerTime: 10.2, offset: 0, followerDuration: 100 })).toBe(10);
    expect(compareDriftSeek({ leaderTime: 10, followerTime: 9.8, offset: 0, followerDuration: 100 })).toBe(10);
    // 偏移计入目标：左 10 + 偏移 2 = 12，右侧在 10 就要拖到 12。
    expect(compareDriftSeek({ leaderTime: 10, followerTime: 10, offset: 2, followerDuration: 100 })).toBe(12);
    expect(compareDriftSeek({ leaderTime: 10, followerTime: 12.05, offset: 2, followerDuration: 100 })).toBeNull();
    // 右侧已停在片头等左侧追上：不再反复拖动。
    expect(compareDriftSeek({ leaderTime: 1, followerTime: 0, offset: -3, followerDuration: 100 })).toBeNull();
  });
});

describe('偏移调整', () => {
  it('按 0.1 秒取整、累加不漂移，正负对称，夹在上限内', () => {
    let offset = 0;
    offset = applyCompareOffset(offset, 0.1);
    offset = applyCompareOffset(offset, 0.1);
    offset = applyCompareOffset(offset, 0.1);
    expect(offset).toBe(0.3);
    expect(applyCompareOffset(0.3, -1)).toBe(-0.7);
    expect(applyCompareOffset(-0.1, 0.1)).toBe(0);
    expect(Object.is(applyCompareOffset(-0.1, 0.1), -0)).toBe(false);
    expect(applyCompareOffset(COMPARE_OFFSET_LIMIT, 1)).toBe(COMPARE_OFFSET_LIMIT);
    expect(applyCompareOffset(-COMPARE_OFFSET_LIMIT, -1)).toBe(-COMPARE_OFFSET_LIMIT);
  });

  it('偏移文字带正负号与一位小数', () => {
    expect(formatCompareOffset(0)).toBe('0.0 秒');
    expect(formatCompareOffset(1.5)).toBe('+1.5 秒');
    expect(formatCompareOffset(-0.3)).toBe('-0.3 秒');
  });
});

describe('声音、滑动分界与时间', () => {
  it('同一时刻只有一侧出声', () => {
    expect(compareMuteState('left')).toEqual({ left: false, right: true });
    expect(compareMuteState('right')).toEqual({ left: true, right: false });
  });

  it('分界线按指针位置算百分比并夹在 0–100；宽度为 0 时居中', () => {
    const rect = { left: 100, width: 400 };
    expect(compareSliderPercent(200, rect)).toBe(25);
    expect(compareSliderPercent(50, rect)).toBe(0);
    expect(compareSliderPercent(900, rect)).toBe(100);
    expect(compareSliderPercent(200, { left: 0, width: 0 })).toBe(50);
  });

  it('上层左侧画面只露出分界线左边', () => {
    expect(compareClipPath(30)).toBe('inset(0 70% 0 0)');
    expect(compareClipPath(0)).toBe('inset(0 100% 0 0)');
    expect(compareClipPath(100)).toBe('inset(0 0% 0 0)');
  });

  it('时间显示：0 与未知显示 00:00', () => {
    expect(formatCompareTime(0)).toBe('00:00');
    expect(formatCompareTime(NaN)).toBe('00:00');
    expect(formatCompareTime(65)).toBe('01:05');
    expect(formatCompareTime(3725)).toBe('01:02:05');
  });
});

describe('播放源与标注', () => {
  it('内嵌会话直接用预览地址，带代理标注', () => {
    const session = { mode: 'inline', inline_source: { locator_value: '/preview/media/3', mime: 'video/mp4' }, proxy: { strategy: 'remux', size: 9 } };
    expect(compareSourceFromSession(session)).toEqual({ src: '/preview/media/3', proxy: { strategy: 'remux', size: 9 }, error: '', manual: false });
  });

  it('不能内嵌时给出原因；格式不支持的提示去详情抽屉生成播放代理', () => {
    const external = compareSourceFromSession({ mode: 'external-preview', reason_code: 'inline_not_supported', reason_message: '当前文件格式不适合在应用内稳定预览。' });
    expect(external.src).toBe('');
    expect(external.error).toContain('当前文件格式不适合在应用内稳定预览。');
    expect(external.error).toContain('生成播放代理');
    // 2026-10-13：proxy-preparing 由对话框自动请求代理、另显示进度，这里不报错；proxy-manual 报原因并给手动按钮。
    expect(compareSourceFromSession({ mode: 'proxy-preparing', reason_message: '需要先生成播放代理。' })).toEqual({ src: '', proxy: null, error: '', manual: false });
    const manual = compareSourceFromSession({ mode: 'proxy-manual', reason_message: '文件较大（约 12.0 GB），生成播放代理会占用相同大小的空间' });
    expect(manual).toEqual({ src: '', proxy: null, error: '文件较大（约 12.0 GB），生成播放代理会占用相同大小的空间', manual: true });
    const missing = compareSourceFromSession({ mode: 'unsupported', reason_code: 'file_missing', reason_message: '源文件不存在，当前无法预览。' });
    expect(missing.error).toBe('源文件不存在，当前无法预览。');
    expect(compareSourceFromSession(null).error).toBe('当前视频暂不支持在应用内预览。');
  });

  it('内嵌报错：经代理时不再建议生成代理', () => {
    expect(compareMediaErrorText(null)).toContain('生成播放代理');
    expect(compareMediaErrorText({ strategy: 'remux' })).not.toContain('生成播放代理');
  });

  it('标注取标题、视频流宽×高、文件大小与编码；缺项省略', () => {
    const details = {
      effective_title: '电影 高清',
      video: { name: 'movie.mkv', size: 4 * 1024 * 1024 * 1024, width: 1, height: 1 },
      streams: [{ stream_type: 'audio', codec_name: 'aac' }, { stream_type: 'video', codec_name: 'hevc', width: 3840, height: 2160 }]
    };
    expect(compareSideLabel(details, { mode: 'inline' }, 5)).toEqual({ title: '电影 高清', facts: '3840×2160 · 4.0 GB · hevc', proxyNote: '' });
    // 没有流信息时退回视频行上的宽高；没有详情时用会话里的文件名，再不行用 ID。
    expect(compareSideLabel({ video: { name: 'a.mp4', width: 1280, height: 720, size: 0 } }, null, 5).facts).toBe('1280×720');
    expect(compareSideLabel(null, { display_name: 'b.mp4' }, 5).title).toBe('b.mp4');
    expect(compareSideLabel(null, null, 5)).toEqual({ title: '视频 #5', facts: '', proxyNote: '' });
  });

  it('经代理播放时注明；重编码的代理说明画面不代表原片', () => {
    expect(compareSideLabel(null, { proxy: { strategy: 'transcode' } }, 1).proxyNote).toContain('不代表原片画质');
    expect(compareSideLabel(null, { proxy: { strategy: 'remux' } }, 1).proxyNote).toBe('经播放代理播放（换容器（未重编码））');
  });
});
