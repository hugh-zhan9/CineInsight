import test from 'node:test';
import assert from 'node:assert/strict';

import { pickMediaTitle, stripSiteSuffix, looksLikeUrlTitle } from '../../src/common/title.js';

test('标题其实是个地址时判为不可用', () => {
  assert.equal(looksLikeUrlTitle('https://a.example.com/v/1.html'), true);
  assert.equal(looksLikeUrlTitle('npt.example.xyz/video/1423974486.html'), true);
  assert.equal(looksLikeUrlTitle('example.com'), true);
  assert.equal(looksLikeUrlTitle(''), true);
  assert.equal(looksLikeUrlTitle('   '), true);
  // 正常标题不该被误判
  assert.equal(looksLikeUrlTitle('第一集 深海'), false);
  assert.equal(looksLikeUrlTitle('纪录片 S01E02'), false);
});

test('知道站点名时精确剥掉，首尾都剥', () => {
  assert.equal(stripSiteSuffix('某某纪录片 第一集 | 某站', '某站'), '某某纪录片 第一集');
  assert.equal(stripSiteSuffix('某站 - 某某纪录片 第一集', '某站'), '某某纪录片 第一集');
  assert.equal(stripSiteSuffix('某某纪录片_某站', '某站'), '某某纪录片');
});

test('不知道站点名时按"末段够短"剥，且只剥一次', () => {
  assert.equal(stripSiteSuffix('深海纪录片 第一集 | 影视站'), '深海纪录片 第一集');
  // 只剥一次：不能把「剧名 - S01 - E02」剥成「剧名」
  assert.equal(stripSiteSuffix('远山的回响 - 第一季 - 第二集'), '远山的回响 - 第一季');
});

test('末段比正文还长时不剥——那多半是标题本身', () => {
  const title = '短名 | 这是一段明显更长的正文内容不该被当成站点名剥掉';
  assert.equal(stripSiteSuffix(title), title);
});

test('去掉【站点】这类前缀', () => {
  assert.equal(stripSiteSuffix('【某影视】远山的回响 第一集'), '远山的回响 第一集');
});

test('优先用页面自己声明的标题，其次 h1，最后才是 document.title', () => {
  assert.equal(pickMediaTitle({
    ogTitle: '远山的回响 第一集',
    heading: '播放页',
    documentTitle: '远山的回响 第一集 | 某影视站'
  }), '远山的回响 第一集');

  assert.equal(pickMediaTitle({
    heading: '远山的回响 第一集',
    documentTitle: '远山的回响 第一集 | 某影视站'
  }), '远山的回响 第一集');

  assert.equal(pickMediaTitle({
    documentTitle: '远山的回响 第一集 | 某影视站',
    siteName: '某影视站'
  }), '远山的回响 第一集');
});

test('候选里混着地址时跳过它，而不是拿来当文件名', () => {
  assert.equal(pickMediaTitle({
    ogTitle: 'https://cdn.example.com/v/1.m3u8',
    documentTitle: '远山的回响 第一集 | 某影视站'
  }), '远山的回响 第一集');
});

test('全都不可用时返回空串，由调用方决定退到什么', () => {
  assert.equal(pickMediaTitle({ documentTitle: 'npt.example.xyz/video/1423974486.html' }), '');
  assert.equal(pickMediaTitle({}), '');
});
