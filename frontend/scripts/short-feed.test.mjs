import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createSwipeTracker, keyboardDirection, wheelDirection } from '../src/short-feed/gesture.js';
import { unsupportedStatusText } from '../src/short-feed/videoState.js';

const source = readFileSync(new URL('../src/short-feed/ShortFeedApp.vue', import.meta.url), 'utf8');
const css = readFileSync(new URL('../src/short-feed/short-feed.css', import.meta.url), 'utf8');

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
assert.equal(unsupportedStatusText({ id: 3, media_url: '/short-media/3' }), '');

assert.doesNotMatch(source, />\s*(Fav|Mute|Sound|Like|Save|Del)\s*</, 'short-feed action buttons should not render text labels');
assert.match(source, /class="action-icon action-icon--heart"/, 'like should use a dedicated icon span');
assert.match(source, /class="action-icon action-icon--bookmark"/, 'favorite should use a dedicated icon span');
assert.match(source, /class="action-icon action-icon--trash"/, 'delete should use a dedicated icon span');
assert.match(css, /\.feed-video\s*{[^}]*object-fit:\s*cover;/s, 'short-feed video should fill the viewport like a vertical feed');
assert.match(css, /\.progress-dock\s*{[^}]*height:\s*3px;/s, 'short-feed progress should be a minimal bottom bar');
assert.doesNotMatch(css, /\.progress-time/, 'short-feed should not keep the old time panel visible');

console.log('short-feed tests passed');
