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

// ---- 2026-09-01 手机端原型重构 ----
const sheet = readFileSync(new URL('../src/short-feed/components/FeedSheet.vue', import.meta.url), 'utf8');
const api = readFileSync(new URL('../src/short-feed/api.js', import.meta.url), 'utf8');

// 右侧六个动作：收藏 · 点赞 · 评分 · 标签 · 已看 · 删除。用户裁决保留点赞，
// 标签偏好学习链路因此不断。
for (const label of ['收藏', '点赞', '评分', '已看', '删除']) {
  assert.match(rail, new RegExp(`rail-action__label">${label}<`), `action rail should keep the ${label} action`);
}
assert.match(rail, /item\?\.media_kind !== 'image'/, 'watched must not appear for photos, which have no watched state');

// 四个底部面板与撤销提示。
assert.match(source, /sheet === 'rating'/, 'rating sheet should exist');
assert.match(source, /sheet === 'tags'/, 'tag sheet should exist');
assert.match(source, /sheet === 'scope'/, 'playback scope sheet should exist');
assert.match(source, /class="feed-toast"/, 'a toast should confirm the delete');
assert.match(source, /@click="undoDelete"/, 'the delete toast must offer an undo');
assert.match(source, /ratingOptions\(\)\s*{[\s\S]*length: 21/, 'rating grid should cover 0–10 in half steps');

// 浏览历史：往回划走历史而不是重新抽签，圆点指示才有意义。
assert.match(source, /if \(direction < 0\)/, 'swiping back should walk history');
assert.match(source, /FEED_HISTORY_LIMIT/, 'history must be bounded');
assert.doesNotMatch(source, /applyVideo\(/, 'the single-item apply path is replaced by the history list');

// 写入结果由后端回整条 DTO，前端不猜。
assert.match(source, /replaceCurrent\(await setRating/, 'rating writes should adopt the returned item');
assert.match(source, /replaceCurrent\(await setWatched/, 'watched writes should adopt the returned item');
assert.match(source, /replaceCurrent\(await setItemTag/, 'tag writes should adopt the returned item');

// 换范围要重开时间线，否则历史会前后矛盾。
assert.match(source, /this\.items = \[\];[\s\S]{0,120}this\.recentKeys = \[\];/, 'switching scope should reset the timeline');

assert.match(sheet, /Escape/, 'bottom sheets should close on Escape');
// 资源类型筛选：全部 / 仅视频 / 仅图片，随下一条请求一起发给后端，换类型重开时间线。
assert.match(source, /short-feed-media-kind/, 'scope sheet should offer a media kind switch');
assert.match(source, /getNextItem\(this\.recentKeys\.slice\(-12\), this\.scope, this\.mediaKind\)/, 'the main next-item request should carry the media kind');
assert.match(source, /getNextItem\(excludeKeys, this\.scope, this\.mediaKind\)/, 'the prefetch request should carry the media kind');
assert.match(source, /getScopes\(this\.mediaKind\)/, 'scope counts should follow the media kind');
assert.match(source, /touchBeganZoomed/, 'a touch that began while zoomed must not be re-interpreted as a swipe or tap on touchend');
assert.match(api, /if \(media && media !== 'all'\) params\.set\('media', media\)/, 'api client should send the media filter');
// 面板要能在浏览器里关掉：显式关闭按钮 + 舞台不拦截面板层的触摸。
assert.match(sheet, /data-test="sheet-close"/, 'bottom sheets need an explicit close button');
assert.match(source, /\.sheet-layer'\)/, 'stage touch handling must leave the sheet layer alone');
// 图片放大后必须能回来，否则上下滑切换被原生平移卡死。
assert.match(stage, /photo-zoom-reset/, 'zoomed photo needs an explicit reset control');
assert.match(source, /event\.pointerType !== 'touch' \|\| this\.photoZoomed/, 'double tap must exit zoom on touch too');
// 搜不到的标签要能就地新建，并且每次打开面板都重拉列表（桌面端刚建的标签不该等刷新）。
assert.match(source, /short-feed-create-tag/, 'tag sheet should offer creating the searched tag');
assert.match(source, /createFeedTag\(name\)/, 'creating a tag should go through the api client');
assert.doesNotMatch(source, /if \(this\.feedTags\.length === 0\) await this\.loadFeedTags\(\)/, 'tag sheet must reload tags on every open');
for (const fn of ['getScopes', 'getFeedTags', 'createFeedTag', 'setRating', 'setWatched', 'setItemTag', 'restoreItem']) {
  assert.match(api, new RegExp(`export function ${fn}\\b`), `api client should expose ${fn}`);
}
assert.match(api, /if \(scope && scope !== 'all'\) params\.set\('scope', scope\)/, 'next-item requests should carry the scope');

console.log('short-feed tests passed');
