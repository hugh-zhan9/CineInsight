import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createSwipeTracker, keyboardDirection, wheelDirection } from '../src/short-feed/gesture.js';
import { unsupportedStatusText } from '../src/short-feed/videoState.js';

// 组件已拆分，源码断言跟着搬到各自的文件，而不是放宽或删掉。
const source = readFileSync(new URL('../src/short-feed/ShortFeedApp.vue', import.meta.url), 'utf8');
const stage = readFileSync(new URL('../src/short-feed/components/FeedStage.vue', import.meta.url), 'utf8');
const rail = readFileSync(new URL('../src/short-feed/components/FeedActionRail.vue', import.meta.url), 'utf8');
const topBar = readFileSync(new URL('../src/short-feed/components/FeedTopBar.vue', import.meta.url), 'utf8');
const meta = readFileSync(new URL('../src/short-feed/components/FeedMeta.vue', import.meta.url), 'utf8');
const wakeLockSource = readFileSync(new URL('../src/short-feed/useWakeLock.js', import.meta.url), 'utf8');
const css = readFileSync(new URL('../src/short-feed/short-feed.css', import.meta.url), 'utf8');
const chrome = [source, stage, rail, topBar].join('\n');

function touchEvent(startX, startY, endX = startX, endY = startY) {
  return {
    touches: [{ clientX: startX, clientY: startY }],
    changedTouches: [{ clientX: endX, clientY: endY }]
  };
}

{
  const tracker = createSwipeTracker(50);
  tracker.start(touchEvent(100, 300));
  assert.equal(tracker.end(touchEvent(100, 300, 96, 190)), 1);
}

{
  const tracker = createSwipeTracker(50);
  tracker.start(touchEvent(100, 180));
  assert.equal(tracker.end(touchEvent(100, 180, 110, 260)), -1);
}

{
  const tracker = createSwipeTracker(50);
  tracker.start(touchEvent(100, 180));
  assert.equal(tracker.end(touchEvent(100, 180, 190, 120)), 0);
}

{
  const state = { lastWheelAt: 0 };
  assert.equal(wheelDirection(80, 1000, state), 1);
  assert.equal(wheelDirection(80, 1100, state), 0);
  assert.equal(wheelDirection(-80, 1500, state), -1);
}

assert.equal(keyboardDirection('ArrowDown'), 1);
assert.equal(keyboardDirection('PageDown'), 1);
assert.equal(keyboardDirection(' '), 1);
assert.equal(keyboardDirection('ArrowUp'), -1);
assert.equal(keyboardDirection('PageUp'), -1);
assert.equal(keyboardDirection('Enter'), 0);

assert.equal(
  unsupportedStatusText({ id: 1, media_url: '', reason_message: '当前文件格式不适合浏览器内播放。' }),
  '当前文件格式不适合浏览器内播放。'
);
assert.equal(unsupportedStatusText({ id: 2, media_url: '' }), '当前视频暂不支持浏览器播放');
assert.equal(unsupportedStatusText({ id: 4, media_kind: 'image', media_url: '' }), '当前图片暂不支持浏览器显示');
assert.equal(unsupportedStatusText({ id: 3, media_url: '/short-media/video/3' }), '');

assert.doesNotMatch(chrome, />\s*(Fav|Mute|Sound|Like|Save|Del)\s*</, 'short-feed action buttons should not render text labels');
assert.match(stage, /v-if="!item \|\| !item\.media_url"\s+class="feed-empty"/, 'empty layer should only render when the current media is unavailable');
assert.match(source, /handleStageTap/, 'short-feed should use explicit tap detection for reliable double-tap playback');
assert.match(wakeLockSource, /navigator\?\.wakeLock\?\.request/, 'short-feed should keep the screen awake while video is playing');
assert.match(source, /document\.addEventListener\('visibilitychange', this\.handleVisibilityChange\)/, 'wake lock should be restored after returning to the page');
assert.match(rail, /class="action-icon"[^>]*viewBox="0 0 24 24"/, 'action buttons should use stable svg icons');

// 图片分支：适屏而非裁切、双击缩放、不显示进度条、不自动翻页，并展示 AI 描述。
assert.match(stage, /media_kind === 'image'/, 'stage should branch on media kind');
assert.match(css, /\.feed-photo\s*{[^}]*object-fit:\s*contain;/s, 'photos should fit the screen instead of being cropped');
assert.match(source, /v-if="!isImageItem && currentVideo && currentVideo\.media_url"/, 'progress dock should be hidden for photos');
assert.match(source, /this\.photoZoomed = !this\.photoZoomed/, 'double tap should toggle photo zoom instead of playback');
assert.match(source, /schedulePhotoControlsHide/, 'photos need their own controls-hide schedule because they have no playing state');
assert.match(meta, /item\.description/, 'photo captions should surface the generated AI description');
assert.doesNotMatch(source, /cyclePlaybackRate/, 'dead playback-rate cycling should be gone');
assert.doesNotMatch(css, /--heart::|--bookmark::|--trash::|--sound::|--muted::|--stack::/, 'short-feed should not draw action icons with fragile CSS pseudo-elements');
assert.match(css, /\.feed-video\s*{[^}]*object-fit:\s*cover;/s, 'short-feed video should fill the viewport like a vertical feed');
assert.match(css, /\.progress-dock\s*{[^}]*height:\s*3px;/s, 'short-feed progress should be a minimal bottom bar');
assert.doesNotMatch(css, /\.progress-time/, 'short-feed should not keep the old time panel visible');

console.log('short-feed tests passed');
