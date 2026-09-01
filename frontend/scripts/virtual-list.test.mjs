import assert from 'node:assert/strict';
import {
  calculateAnchorScrollTop,
  calculateVirtualWindow,
  createHeightCacheKey,
  defaultRangeEngine,
  estimateVideoRowHeight,
  getWidthBucket,
  resolveScrollOwnerDescriptor
} from '../src/utils/virtualList.js';

function runTests() {
  assert.equal(createHeightCacheKey(12, 8, false), '12:8:0');
  assert.equal(createHeightCacheKey(12, 8, true), '12:8:1');
  assert.equal(typeof defaultRangeEngine.calculateVirtualWindow, 'function');
  assert.equal(typeof defaultRangeEngine.calculateAnchorScrollTop, 'function');
  assert.equal(typeof defaultRangeEngine.getWidthBucket, 'function');

  const noOwner = resolveScrollOwnerDescriptor({ closest: () => null }, null);
  assert.equal(noOwner.missing, true);
  assert.equal(noOwner.sameOwner, true);
  assert.equal(noOwner.nextOwner, null);

  assert.equal(getWidthBucket(0), 1);
  assert.equal(getWidthBucket(320), 4);

  // 2026-09-01 重构后行高固定：标签与字幕命中都压在同一行横向溢出隐藏，
  // 不再换行撑高，所以预估只随档位与窄变体变化。三档必须互不相同且递增，
  // 否则切档位后滚动条长度会算错。
  const simpleVideo = { id: 1, tags: [], is_stale: false };
  const subtitleVideo = { id: 2, tags: [{ id: 1 }], is_stale: true, _subtitleMatchText: 'match' };
  assert.equal(estimateVideoRowHeight(simpleVideo, 10, false), 94);
  assert.equal(estimateVideoRowHeight(subtitleVideo, 10, true), 94);
  assert.equal(estimateVideoRowHeight(simpleVideo, 10, false, 'comfortable'), 110);
  assert.equal(estimateVideoRowHeight(simpleVideo, 10, false, 'narrow'), 74);
  assert.equal(estimateVideoRowHeight(simpleVideo, 10, false, 'nope'), 94);

  const items = [
    { id: 1 },
    { id: 2 },
    { id: 3 },
    { id: 4 },
    { id: 5 }
  ];
  const heights = [100, 110, 120, 130, 140];
  const windowA = calculateVirtualWindow({
    items,
    scrollTop: 210,
    viewportHeight: 180,
    listTop: 0,
    overscan: 1,
    getItemHeight: (_, index) => heights[index]
  });
  assert.deepEqual(windowA, {
    startIndex: 1,
    endIndex: 5,
    topSpacer: 100,
    bottomSpacer: 0,
    totalHeight: 600
  });

  const windowB = calculateVirtualWindow({
    items,
    scrollTop: 260,
    viewportHeight: 100,
    listTop: 0,
    overscan: 0,
    getItemHeight: (_, index) => heights[index]
  });
  assert.equal(windowB.startIndex, 2);
  assert.equal(windowB.endIndex, 4);
  assert.equal(windowB.topSpacer, 210);
  assert.equal(windowB.totalHeight, 600);

  const anchorTop = calculateAnchorScrollTop({
    items,
    listTop: 40,
    anchorIndex: 2,
    anchorOffsetWithin: 15,
    getItemHeight: (_, index) => heights[index]
  });
  assert.equal(anchorTop, 265);
}

runTests();
console.log('virtual-list tests: ok');
