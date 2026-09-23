import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
const settingsSource = readFileSync(new URL('../src/components/SettingsPage.vue', import.meta.url), 'utf8');
const tagManagerSource = readFileSync(new URL('../src/components/TagManagerDialog.vue', import.meta.url), 'utf8');
// P-002 把设置分区拆成了 components/settings/** 的子组件，分区内的断言跟着搬。
const aiTagSource = readFileSync(new URL('../src/components/settings/AITagSection.vue', import.meta.url), 'utf8');
assert.doesNotMatch(settingsSource, /AITagLibrarySection/, '设置页不再编辑 AI 标签库');
assert.doesNotMatch(tagManagerSource, /AITagLibrarySection|GetAITagLibrary|SaveAITagLibrary|ClearAITagLibrary|is_system/, '标签管理使用统一逐项接口');
assert.match(settingsSource, /class="settings-save-status"/);
assert.match(settingsSource, /设置保存成功，已触发 AI 自动打标/);
assert.match(aiTagSource, /ai_tagging_max_extra_frames/);
assert.match(aiTagSource, /原始音频不会发送/);
assert.match(aiTagSource, /临时字幕只在内存中使用/);
assert.doesNotMatch(settingsSource, /alert\('设置保存/);

console.log('ai-tag-library tests passed');
