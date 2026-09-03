import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { flattenAITagGroups, groupAITagsByNamespace, validateAITagGroups } from '../src/utils/aiTagLibrary.js';

const groups = groupAITagsByNamespace([
  { id: 1, namespace: '内容', name: '访谈', color: '#111111', is_active: true },
  { id: 2, namespace: '内容', name: '剧情', color: '#222222', is_active: false },
  { id: 3, namespace: '画面', name: '夜景', color: '#333333', is_active: true }
]);

assert.equal(groups.length, 2);
assert.equal(groups[0].namespace, '内容');
assert.deepEqual(groups[0].tags.map(tag => tag.name), ['访谈', '剧情']);
assert.equal(groups[1].namespace, '画面');

assert.deepEqual(flattenAITagGroups(groups), [
  { id: 1, namespace: '内容', name: '访谈', color: '#111111', review_required: false, is_active: true },
  { id: 2, namespace: '内容', name: '剧情', color: '#222222', review_required: false, is_active: false },
  { id: 3, namespace: '画面', name: '夜景', color: '#333333', review_required: false, is_active: true }
]);

assert.deepEqual(flattenAITagGroups([{ namespace: '空分类', tags: [] }]), []);
assert.deepEqual(groupAITagsByNamespace(null), []);
assert.equal(validateAITagGroups(groups), '');
assert.equal(validateAITagGroups([{ namespace: '', tags: [{ name: '标签' }] }]), '标签分类名称不能为空');
assert.equal(validateAITagGroups([{ namespace: '内容', tags: [] }]), '分类“内容”至少需要一个标签');
assert.equal(validateAITagGroups([
  { namespace: '内容', tags: [{ name: '访谈' }] },
  { namespace: '画面', tags: [{ name: '访谈' }] }
]), '标签名称重复：访谈');

const settingsSource = readFileSync(new URL('../src/components/SettingsPage.vue', import.meta.url), 'utf8');
// P-002 把设置分区拆成了 components/settings/** 的子组件，分区内的断言跟着搬。
const aiTagLibrarySource = readFileSync(new URL('../src/components/settings/AITagLibrarySection.vue', import.meta.url), 'utf8');
const aiTagSource = readFileSync(new URL('../src/components/settings/AITagSection.vue', import.meta.url), 'utf8');
assert.match(aiTagLibrarySource, /v-for="\(group, groupIndex\) in groups"/);
assert.match(settingsSource, /:groups="localAITagGroups"/, '标签组数组仍由设置页持有并传给编辑器');
assert.match(aiTagLibrarySource, /class="ai-tag-library-group"/);
assert.match(settingsSource, /class="settings-save-status"/);
assert.match(settingsSource, /设置保存成功，已触发 AI 自动打标/);
assert.match(aiTagSource, /ai_tagging_max_extra_frames/);
assert.match(aiTagSource, /原始音频不会发送/);
assert.match(aiTagSource, /临时字幕只在内存中使用/);
assert.doesNotMatch(settingsSource, /alert\('设置保存/);

console.log('ai-tag-library tests passed');
