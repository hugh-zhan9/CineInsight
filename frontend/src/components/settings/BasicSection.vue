<template>
  <div :id="`settings-basic`" class="settings-section">
    <h3>基本设置</h3>
    <div class="setting-item">
      <label class="switch">
        <input type="checkbox" v-model="form.confirm_before_delete" />
        <span class="slider"></span>
        <span>删除前确认</span>
      </label>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input type="checkbox" v-model="form.delete_original_file" />
        <span class="slider"></span>
        <span>默认将原始文件移到废纸篓</span>
      </label>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input type="checkbox" v-model="form.log_enabled" />
        <span class="slider"></span>
        <span>启用日志记录</span>
      </label>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input data-test="desktop-notifications-toggle" type="checkbox" v-model="form.desktop_notifications_enabled" />
        <span class="slider"></span>
        <span>桌面通知</span>
      </label>
      <p class="help-text">
        长任务跑完或失败时发一条系统通知（字幕、超分、语义索引、备份失败）。
        应用在前台时不发，界面里已经有提示。仅 macOS 生效。
      </p>
    </div>
    <div class="setting-item">
      <label>主题模式</label>
      <select v-model="form.theme" class="select-input">
        <option value="light">浅色模式 (Light)</option>
        <option value="dark">深色模式 (Dark)</option>
        <option value="system">跟随系统 (System)</option>
      </select>
    </div>
	  <div class="setting-item">
		<label>键盘快捷键</label>
		<button data-test="shortcut-help-button" type="button" class="btn btn--secondary" @click="showShortcutHelp = true">查看快捷键</button>
		<p class="help-text">命令面板、清理中心、片库与列表审阅、字幕编辑、图片查看器的快捷键。</p>
	  </div>
  </div>

	<DialogShell v-if="showShortcutHelp" title="键盘快捷键" size="md" stop-modal-clicks data-test="shortcut-help-dialog" @close="showShortcutHelp = false">
	  <h3 class="shortcut-help-group">任何页面</h3>
	  <dl class="shortcut-help-list" data-test="shortcut-help-global">
		<dt>⌘K / ⌘⇧P</dt><dd>打开命令面板（页面、命令与视频搜索）</dd>
		<dt>⌘⇧K</dt><dd>打开清理中心</dd>
		<dt>Esc</dt><dd>关闭弹窗；列表里有选中时取消选择</dd>
	  </dl>
	  <h3 class="shortcut-help-group">片库</h3>
	  <dl class="shortcut-help-list">
		<dt>⌘F</dt><dd>搜索片库</dd>
		<dt>⌘R</dt><dd>增量扫描</dd>
		<dt>⌘T</dt><dd>打开 AI 标签审阅</dd>
		<dt>⌘⇧N</dt><dd>扫描新目录</dd>
	  </dl>
	  <h3 class="shortcut-help-group">列表与详情审阅</h3>
	  <dl class="shortcut-help-list">
		<dt>J / ↓</dt><dd>选择下一个视频</dd>
		<dt>K / ↑</dt><dd>选择上一个视频</dd>
		<dt>空格</dt><dd>打开或关闭详情预览</dd>
		<dt>F</dt><dd>切换收藏</dd>
		<dt>W</dt><dd>切换已看</dd>
		<dt>T</dt><dd>打开添加标签</dd>
		<dt>回车</dt><dd>播放视频</dd>
	  </dl>
	  <h3 class="shortcut-help-group">字幕编辑</h3>
	  <dl class="shortcut-help-list" data-test="shortcut-help-subtitle">
		<dt>⌘S</dt><dd>保存</dd>
		<dt>⌘Z</dt><dd>撤销</dd>
		<dt>⌘⇧Z</dt><dd>重做</dd>
		<dt>⌘F</dt><dd>查找（光标移到「查找替换」的查找框）</dd>
	  </dl>
	  <h3 class="shortcut-help-group">图片查看器</h3>
	  <dl class="shortcut-help-list" data-test="shortcut-help-photo-viewer">
		<dt>← / →</dt><dd>上一张 / 下一张</dd>
		<dt>F</dt><dd>切换收藏</dd>
		<dt>Esc</dt><dd>关闭查看器</dd>
	  </dl>
	  <p class="help-text shortcut-help-note">单键（J、K、空格、F、W、T、回车）在输入框、下拉框和弹窗处于焦点时不会触发快捷键；带 ⌘ 的组合键在输入框里也能用。Windows 与 Linux 上 ⌘ 换成 Ctrl。</p>
	  <template #footer><button type="button" class="btn btn--primary" @click="showShortcutHelp = false">知道了</button></template>
	</DialogShell>
</template>

<script>
import DialogShell from '../ui/DialogShell.vue';

// 基本设置分区。表单对象由 SettingsPage 持有并传下来，这里只绑定字段；
// 「查看快捷键」弹窗只有这一个入口，跟着一起放在这里。
export default {
  name: 'BasicSection',
  components: { DialogShell },
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return { showShortcutHelp: false };
  }
};
</script>

<style scoped>
/* 快捷键帮助（D-UD-14 列全，内容由 A 组维护）：分组小标题 + 两列「按键 / 作用」，按键用等宽字排成键帽样式。 */
.shortcut-help-group {
  margin: var(--space-4) 0 var(--space-2);
  color: var(--text-secondary);
  font-size: var(--fs-caption);
  font-weight: var(--fw-semibold);
}

.shortcut-help-group:first-child { margin-top: 0; }

.shortcut-help-list {
  display: grid;
  /* 按键列给一个最小宽度，各组的「作用」列就能对齐。 */
  grid-template-columns: minmax(96px, max-content) 1fr;
  align-items: baseline;
  gap: var(--space-2) var(--space-4);
  margin: 0 0 var(--space-1);
  font-size: var(--fs-secondary);
}

.shortcut-help-list dt {
  justify-self: start;
  padding: 1px var(--space-2);
  border: 1px solid var(--hairline);
  border-radius: var(--radius-sm);
  background: var(--panel-subtle-bg);
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: var(--fs-caption);
  font-weight: var(--fw-semibold);
  white-space: nowrap;
}

.shortcut-help-list dd {
  margin: 0;
  color: var(--text-secondary);
}

.shortcut-help-note { margin-top: var(--space-4); }
</style>
