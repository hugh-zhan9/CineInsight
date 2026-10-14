import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createSwipeTracker, keyboardDirection, wheelDirection } from '../src/short-feed/gesture.js';
import { isScopeExhaustedHint, unsupportedStatusText } from '../src/short-feed/videoState.js';
import { formatClock, formatCount, formatRating, kindLine, metaLine, starFill } from '../src/short-feed/format.js';

// 组件已拆分，源码断言跟着搬到各自的文件，而不是放宽或删掉。
// 2026-10-14 手机网页重做（D-MW-01..07）：全屏、已看、删除、评分进了「更多」面板（MoreSheet），标签页换成 FeedTabs，
// 收藏页换成缩略图墙。只有设计明确改掉的行为对应的断言改写（各处注明 D-MW 编号），其余原样搬家。
const shortFeedFile = path => readFileSync(new URL(`../src/short-feed/${path}`, import.meta.url), 'utf8');
const source = shortFeedFile('ShortFeedApp.vue');
const stage = shortFeedFile('components/FeedStage.vue');
const rail = shortFeedFile('components/FeedActionRail.vue');
const topBar = shortFeedFile('components/FeedTopBar.vue');
const tabs = shortFeedFile('components/FeedTabs.vue');
const meta = shortFeedFile('components/FeedMeta.vue');
const more = shortFeedFile('components/MoreSheet.vue');
const ratingPicker = shortFeedFile('components/RatingPicker.vue');
const progress = shortFeedFile('components/FeedProgress.vue');
const wakeLockSource = shortFeedFile('useWakeLock.js');
const css = shortFeedFile('short-feed.css');
const chrome = [source, stage, rail, topBar, tabs, more].join('\n');

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
// 设计稿「推荐 · 视频」：时间只在控件出来时写在进度线右上角，收起时只剩那条线。
assert.match(progress, /<span v-if="visible" class="progress-dock__time"/, 'the elapsed / total label only shows with the chrome');

// ---- 2026-09-01 手机端原型重构 ----
const sheet = readFileSync(new URL('../src/short-feed/components/FeedSheet.vue', import.meta.url), 'utf8');
const api = readFileSync(new URL('../src/short-feed/api.js', import.meta.url), 'utf8');

// 右侧只留三个（D-MW-02，改写原「右侧六个动作」）：收藏 · 点赞 · 更多。用户裁决保留点赞，标签偏好学习链路因此不断；
// 评分、标签、已看、全屏、移到废纸篓都在「更多」里。
assert.deepEqual([...rail.matchAll(/data-test="(short-feed-[\w-]+)"/g)].map(match => match[1]), ['short-feed-favorite', 'short-feed-like', 'short-feed-more'], 'the rail keeps exactly favorite, like and more');
assert.match(rail, /item\?\.favorited \? '已收藏' : '收藏'/, 'favorite shows its state in the label');
assert.match(rail, /item\?\.liked \? '已点赞' : '点赞'/, 'like shows its state in the label');
assert.match(rail, /:aria-pressed="item\?\.favorited/, 'favorite is a toggle button');
assert.match(rail, /:aria-pressed="item\?\.liked/, 'like is a toggle button');
assert.match(rail, /rail-action__label">更多</, 'the third action opens 更多');
for (const label of ['个人评分', '标签', '已看过', '移到废纸篓', '取消']) {
  assert.match(more, new RegExp(`>\\s*${label}\\s*<`), `更多 should offer ${label}`);
}
assert.match(more, /v-if="item\.media_kind !== 'image'"[\s\S]{0,160}role="switch"/, 'watched must not appear for photos, which have no watched state');
assert.ok(more.indexOf('short-feed-delete') > more.indexOf('short-feed-fullscreen'), 'move to trash is the last action, after fullscreen');

// 四个底部面板（「更多」、评分、标签、范围）与提示。
assert.match(source, /sheet === 'more'/, 'more sheet should exist');
assert.match(source, /sheet === 'rating'/, 'rating sheet should exist');
assert.match(source, /sheet === 'tags'/, 'tag sheet should exist');
assert.match(source, /sheet === 'scope'/, 'playback scope sheet should exist');
assert.match(source, /class="feed-toast"/, 'a toast should confirm the delete');
// D-MW-03（改写原「删除提示要给撤销」）：不弹确认、不给撤销，提示说去电脑端回收站找回。
assert.doesNotMatch(source, /undoDelete|pendingUndo|restoreItem|DeleteDialog/, 'mobile delete has no undo and no confirm dialog');
assert.match(source, /已移到废纸篓`, \{ detail: '要找回，在电脑端「回收站」里恢复'/, 'the delete notice says where to restore');
assert.doesNotMatch(source.slice(source.indexOf('class="feed-toast"'), source.indexOf('</Teleport>')), /<button/, 'the toast has no button');
assert.match(more, /:disabled="deleting"/, 'move to trash is disabled while a delete is in flight');
// D-MW-04（改写原「21 个数字格子」）：10 颗星点第 n 颗记 n 分，滑杆 0–10 步长 0.5，拖动只改显示、松手写入。
assert.match(ratingPicker, /v-for="star in 10"[\s\S]*@click="\$emit\('commit', star\)"/, 'tapping star n writes n');
assert.match(ratingPicker, /type="range"\s+min="0"\s+max="10"\s+step="0\.5"/, 'the slider covers 0–10 in half steps');
assert.match(ratingPicker, /@input="\$emit\('preview'[\s\S]*@change="\$emit\('commit'/, 'dragging only previews; releasing commits');
assert.match(source, /@click="applyRating\(null\)"/, 'clearing the rating writes null');

// 浏览历史：往回划走历史而不是重新抽签。
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
// 预取不能与正在播的视频抢带宽：预取的 <video> 先只拿元数据，当前条 canplaythrough 后才整只下载。
assert.match(stage, /:preload="prefetchPreload"/, 'prefetched video must not hardcode preload=auto');
assert.doesNotMatch(stage, /class="preload-video"[^>]*\spreload="auto"/s, 'prefetched video must not download in full up front');
assert.match(stage, /@canplaythrough="\$emit\('buffered'\)"/, 'stage must report when the current video has buffered enough');
assert.match(source, /onVideoBuffered\(\) \{\s*this\.prefetchPreload = 'auto';/s, 'host upgrades prefetch to auto only after buffering');
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
// 标签搜索实时查服务端：关键词随每次输入发出，过期响应按序号丢弃；自动标签只读展示。
assert.match(api, /q=\$\{encodeURIComponent\(trimmed\)\}/, 'api client should send the tag search keyword');
assert.match(source, /tagKeyword\(value\)\s*{[\s\S]*?loadFeedTags\(value\)/, 'typing in the tag search must query the server');
assert.match(source, /seq !== this\.tagQuerySeq/, 'stale tag query responses must be discarded');
assert.match(source, /:disabled="tag\.automatic"/, 'automatic tags are shown read-only');
assert.match(source, /tall @close="sheet = null"/, 'tag sheet should use the tall variant');
assert.match(sheet, /data-test="sheet-more"/, 'sheets must hint when more content is below the fold');
for (const fn of ['getScopes', 'getFeedTags', 'createFeedTag', 'setRating', 'setWatched', 'setItemTag', 'thumbnailURL']) {
  assert.match(api, new RegExp(`export function ${fn}\\b`), `api client should expose ${fn}`);
}
// D-MW-03：页面不再调用 restore，客户端里的 restoreItem 一并删除（后端接口不动）。
assert.doesNotMatch(api, /restoreItem|\/restore/, 'the mobile page no longer calls restore');
assert.match(api, /if \(scope && scope !== 'all'\) params\.set\('scope', scope\)/, 'next-item requests should carry the scope');

// ---- 2026-09-09 横屏素材 / 全屏 / 主屏幕启动 ----
// 横屏素材完整显示：竖屏 feed 的 cover 裁切只会剩中间三分之一；方向以解码后的实际尺寸为准（含旋转元数据）。
assert.match(stage, /'feed-video--landscape': landscape/, 'landscape videos need their own class');
assert.match(stage, /el\.videoWidth > el\.videoHeight/, 'orientation should come from the decoded frame size, which honours rotation metadata');
assert.match(css, /\.feed-video--landscape\s*{[^}]*object-fit:\s*contain;/s, 'landscape videos must be letterboxed, not cropped');
assert.match(css, /@media \(orientation: landscape\) and \(pointer: coarse\)\s*{\s*\.feed-video\s*{[^}]*object-fit:\s*contain;/s, 'a rotated phone must not crop portrait videos either, while desktop keeps its rule');
assert.match(stage, /webkitendfullscreen/, 'iPhone native fullscreen must report its exit so the toggle state follows');
// 全屏：有元素全屏就整页进全屏（面板与滑动都留在里面），iPhone 只能走系统播放器全屏。
// D-MW-01：入口从顶栏移进「更多」。
assert.match(more, /toggle-fullscreen/, '更多 should offer a fullscreen toggle');
assert.match(more, /v-if="fullscreenAvailable"/, 'the toggle only shows where some fullscreen path exists');
assert.match(source, /documentElement;[\s\S]{0,80}requestFullscreen/, 'fullscreen should target the whole page, not just the video');
assert.match(source, /webkitEnterFullscreen/, 'iPhone Safari falls back to the native video fullscreen');
assert.match(source, /addEventListener\('fullscreenchange', this\.syncFullscreen\)/, 'fullscreen state must follow the browser, not the button');
assert.match(source, /removeEventListener\('fullscreenchange', this\.syncFullscreen\)/, 'fullscreen listener must be released on unmount');
const shortHTML = readFileSync(new URL('../short.html', import.meta.url), 'utf8');
assert.match(shortHTML, /apple-mobile-web-app-capable/, 'home-screen launch on iPhone needs the standalone meta tag');
assert.match(shortHTML, /viewport-fit=cover/, 'standalone mode must keep drawing under the notch');

// ---- 2026-09-30 产品完善度批次：PIN 页、有效观看阈值、失败提示（D-PC43 / D-PC45 / D-PC46）----
{
  const { feedErrorText, pinInputProblem, unplayableHintText, DAILY_LOCKED_TEXT, PIN_MIN_LENGTH } = await import('../src/short-feed/errors.js');
  const { FeedRequestError } = await import('../src/short-feed/api.js');
  const { viewThreshold } = await import('../src/utils/viewThreshold.js');
  const pinGate = readFileSync(new URL('../src/short-feed/components/PinGate.vue', import.meta.url), 'utf8');
  const favoritesView = readFileSync(new URL('../src/short-feed/components/FavoritesView.vue', import.meta.url), 'utf8');

  // PLAY-01 PIN 页：6 位下限在本地先挡，不白白消耗失败次数；输入框不设 maxlength（超长会被静默截断）。
  assert.equal(PIN_MIN_LENGTH, 6, 'PLAY-01 PIN minimum is 6, matching SetShortFeedPIN');
  assert.equal(pinInputProblem(''), '请输入 PIN');
  assert.equal(pinInputProblem('12345'), 'PIN 至少 6 位', 'PLAY-01 five characters are rejected locally');
  assert.equal(pinInputProblem('123456'), '', 'PLAY-01 six characters are accepted');
  assert.equal(pinInputProblem('密码密码密码'), '', 'PLAY-01 length counts characters, not bytes');
  assert.doesNotMatch(pinGate, /\smaxlength=/i, 'PLAY-01 the PIN input must not silently truncate');
  // 设计 §6「密码框（显示 / 隐藏）」：默认遮住，点眼睛才显示（改写原 type="password" 字面断言）。
  assert.match(pinGate, /:type="pinVisible \? 'text' : 'password'"/, 'PLAY-01 the PIN input is masked unless revealed');
  assert.match(pinGate, /pinVisible: false/, 'PLAY-01 the PIN starts masked');
  assert.match(source, /<PinGate v-if="authRequired"/, 'PLAY-01 a 401 pin_required switches the page to the PIN gate');
  assert.match(api, /status === 401 && code === 'pin_required'/, 'PLAY-01 every request routes pin_required to the PIN gate');
  assert.match(api, /'\/short-api\/auth', \{ pin \}/, 'PLAY-01 the gate posts the PIN to /short-api/auth');
  assert.match(source, /\.pin-gate/, 'PLAY-01 swipe handling must leave the PIN page alone');

  // PLAY-01 锁定：短锁给秒数；每日上限（daily_locked）提示去电脑上解除或 24 小时后再试。
  const locked = (payload) => new FeedRequestError('x', { status: 429, code: 'pin_locked', payload });
  assert.equal(feedErrorText(locked({ code: 'pin_locked', daily_locked: true, retry_after: 86000 })), '请在电脑上解除锁定，或 24 小时后再试');
  assert.equal(DAILY_LOCKED_TEXT, '请在电脑上解除锁定，或 24 小时后再试');
  assert.equal(feedErrorText(locked({ code: 'pin_locked', retry_after: 42 })), '尝试次数过多，请 42 秒后再试');
  assert.equal(feedErrorText(locked({ code: 'pin_locked' })), '尝试次数过多，请稍后再试');
  assert.equal(feedErrorText(new FeedRequestError('PIN 不正确', { status: 401, code: 'pin_invalid' })), 'PIN 不正确');

  // PLAY-13 删除的两种 409 与网络异常。
  assert.equal(feedErrorText(new FeedRequestError('m', { status: 409, code: 'volume_offline' })), '文件所在磁盘当前未连接，未做任何改动');
  assert.equal(feedErrorText(new FeedRequestError('m', { status: 409, code: 'permission_denied' })), '没有权限把文件移到废纸篓，请在桌面端处理');
  assert.equal(feedErrorText(new FeedRequestError('m', { status: 409, code: 'trash_unsupported' })), '该磁盘不支持废纸篓，请在桌面端处理');
  assert.equal(feedErrorText(new FeedRequestError('网络异常', { network: true })), '网络异常');
  assert.equal(feedErrorText(new FeedRequestError('没有可播放的内容', { status: 404, code: 'no_eligible_videos' })), '没有可播放的内容', 'unknown codes keep the server message');
  assert.equal(feedErrorText(null), '');

  // PLAY-13 不可播放提示：0 条不显示。D-MW-07：文案改成设计稿的白话，面板里一段，跟着当前范围。
  assert.equal(unplayableHintText(3), '另有 3 个视频手机放不了（格式不支持）。在电脑端片库里为它们生成兼容副本后就会出现在这里。');
  assert.equal(unplayableHintText(0), '');
  assert.equal(unplayableHintText(undefined), '');
  assert.match(source, /unplayableHintText\(this\.currentScopeEntry\?\.unplayable_count\)/, 'PLAY-13 the scope sheet shows the unplayable count of the chosen scope');

  // PLAY-13 网络错误保留历史：取下一条失败不再清空 items，给「重试」。
  const loadBody = source.match(/async loadNextItem\(\) \{([\s\S]*?)\n    \},/)?.[1] || '';
  assert.ok(loadBody.includes('catch (err)'), 'loadNextItem should be found');
  assert.doesNotMatch(loadBody, /this\.items = \[\]/, 'PLAY-13 a failed load must not clear the browsing history');
  assert.match(source, /retryLoad\(\) \{\s*return this\.loadNextItem\(\);/, 'PLAY-13 retry always fetches a new item, even after swiping back');
  assert.match(source, /data-test="short-feed-retry"/, 'PLAY-13 a failed load offers a retry');
  assert.match(favoritesView, /short-feed-favorites-error/, 'PLAY-13 favorites show an error state instead of looking empty');

  // PLAY-07 手机端有效观看：playing 那一刻不再上报，累计播放越过 viewThreshold 才报（后端来源仍是 mobile_feed）。
  assert.doesNotMatch(source, /@playing="onVideoPlaying"/, 'PLAY-07 playing alone must not record a view');
  assert.match(source, /seconds >= viewThreshold\(duration\)/, 'PLAY-07 the mobile view is recorded once the shared threshold is crossed');
  assert.equal(viewThreshold(30), 15, 'PLAY-07 mobile uses the same threshold as the desktop and Go');
}

// ---- 2026-10-14 手机网页重做（D-MW-01..07）----
{
  const { emptyScopeText } = await import('../src/short-feed/errors.js');
  const favoritesView = shortFeedFile('components/FavoritesView.vue');
  const sheet = shortFeedFile('components/FeedSheet.vue');

  // D-MW-01：顶栏只有「推荐 · 收藏」、范围与静音；标题、计数、位置圆点、收藏夹与全屏按钮都去掉。
  assert.doesNotMatch(topBar.replace(/<!--[\s\S]*?-->/g, ''), /短视频|counter|open-favorites|fullscreen/, 'the top bar has no title, counter, favorites or fullscreen button');
  assert.match(topBar, /<FeedTabs v-else current="feed"/, 'the feed top bar shows the 推荐 · 收藏 tabs');
  assert.match(tabs, /:aria-current="tab\.key === current \? 'page' : undefined"/, 'the current tab is marked with aria-current');
  assert.match(tabs, /label: '推荐'[\s\S]*label: '收藏'/, 'tabs read 推荐 · 收藏');
  assert.match(favoritesView, /<FeedTabs current="favorites"/, 'the favorites page reuses the same tabs');
  assert.doesNotMatch(source, /FeedDots|counterText/, 'the position dots and counter are gone');
  assert.match(source, /short: '视频'[\s\S]*short: '图片'/, 'the scope button appends · 视频 / · 图片 when the kind is narrowed');

  // D-MW-02：左下信息行「类型 · 时长 · 评分 X · 已看」，没有的项不写；「更多」副标题写类型与规格。
  assert.equal(metaLine({ media_kind: 'video', duration: 161, personal_rating: 8.5, watched: true }), '视频 · 02:41 · 评分 8.5 · 已看');
  assert.equal(metaLine({ media_kind: 'video', duration: 48, personal_rating: null, watched: false }), '视频 · 00:48');
  assert.equal(metaLine({ media_kind: 'image', width: 4032, height: 3024, personal_rating: 9, watched: true }), '图片 · 4032 × 3024 · 评分 9.0');
  assert.equal(metaLine({ media_kind: 'image' }), '图片');
  assert.equal(kindLine({ media_kind: 'video', duration: 0 }), '视频');
  assert.equal(metaLine(null), '');
  assert.equal(formatClock(161.9), '02:41');
  assert.equal(formatClock(NaN), '00:00');
  assert.equal(formatRating(null), '');
  assert.equal(formatRating(7), '7.0');
  assert.match(meta, /＋ 标签/, 'the tag row ends with ＋ 标签');

  // D-MW-04：第 n 颗星满、半、空的口径与电脑端 10 星半分一致。
  assert.deepEqual([1, 8, 9, 10].map(star => starFill(8.5, star)), ['full', 'full', 'half', 'empty']);
  assert.equal(starFill(null, 1), 'empty');
  assert.equal(starFill(0, 1), 'empty');
  assert.equal(starFill(0.5, 1), 'half');

  // D-MW-05：三列缩略图墙，懒加载 /short-thumb/，视频角标时长。
  assert.match(api, /`\/short-thumb\/\$\{item\.media_kind\}\/\$\{item\.id\}`/, 'thumbnails come from /short-thumb/{kind}/{id}');
  assert.match(favoritesView, /:src="thumbnailURL\(item\)"[\s\S]{0,40}alt=""[\s\S]{0,20}loading="lazy"/, 'wall thumbnails load lazily');
  assert.match(css, /\.favorites-grid\s*{[^}]*grid-template-columns:\s*repeat\(3, minmax\(0, 1fr\)\)/s, 'the wall has three columns');
  assert.match(css, /\.favorite-tile\s*{[^}]*aspect-ratio:\s*3 \/ 4/s, 'wall tiles are 3:4');

  // D-MW-06：只用 tokens；面板与提示的层级走 --z-*，全局提示压在面板之上。
  assert.match(css, /\.sheet-layer\s*{[^}]*z-index:\s*var\(--z-modal\)/s, 'sheets sit on the modal layer');
  assert.match(css, /\.feed-toast\s*{[^}]*z-index:\s*var\(--z-toast\)/s, 'toasts sit above sheets');
  assert.match(source, /<Teleport to="body">\s*<div v-if="toast" class="feed-toast"/, 'the toast is teleported next to the sheets so it can sit above them');
  assert.match(shortHTML, /<body class="surface-cinema">/, 'the mobile page is always on the cinema surface');
  assert.doesNotMatch(sheet, /✕/, 'the sheet close uses the shared icon, not a glyph');

  // D-MW-07：空范围（404 no_eligible_videos，或服务端回一条放不了的视频当说明）给整屏空态与「换个范围」。
  assert.equal(isScopeExhaustedHint({ id: 1, media_url: '', reason_code: 'inline_not_supported' }), true);
  assert.equal(isScopeExhaustedHint({ id: 1, media_url: '/short-media/video/1' }), false);
  assert.equal(isScopeExhaustedHint({ id: 1, media_url: '', reason_code: 'other' }), false);
  assert.equal(isScopeExhaustedHint(null), false);
  assert.equal(emptyScopeText('还没打标签', 'video', 7), '「还没打标签」里的 7 个视频手机都放不了。在电脑端为它们生成兼容副本，或者换个范围。');
  assert.equal(emptyScopeText('只在收藏里随机', 'image', 0), '「只在收藏里随机」里现在没有图片，换个范围试试。');
  assert.equal(emptyScopeText('全部', 'all', undefined), '「全部」里现在没有视频或图片，换个范围试试。');
  assert.match(source, /err\?\.code === 'no_eligible_videos'/, 'a 404 no_eligible_videos on an empty history is an empty scope, not a failed load');
  assert.match(source, /title="这个范围里没有能播的"/, 'the empty scope says nothing here can play');
  assert.match(source, /data-test="short-feed-change-scope" @click="openScopeSheet">换个范围</, 'the empty scope offers 换个范围');
  assert.match(source, /title="刷什么"/, 'the scope sheet is titled 刷什么');
  assert.equal(formatCount(1284), '1,284');
  assert.equal(formatCount(undefined), '0');
}

console.log('short-feed tests passed');
