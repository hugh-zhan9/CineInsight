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
        <span>默认将原始文件移入回收站</span>
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
		<button data-test="shortcut-help-button" type="button" class="btn-secondary" @click="showShortcutHelp = true">查看快捷键</button>
		<p class="help-text">用于列表和详情抽屉中的连续审阅。</p>
	  </div>
  </div>

	<BaseModal v-if="showShortcutHelp" stop-modal-clicks @close="showShortcutHelp = false">
	  <h2>键盘快捷键</h2>
	  <dl class="shortcut-help-list">
		<dt>J / ↓</dt><dd>选择下一个视频</dd>
		<dt>K / ↑</dt><dd>选择上一个视频</dd>
		<dt>空格</dt><dd>打开或关闭详情预览</dd>
		<dt>F</dt><dd>切换收藏</dd>
		<dt>W</dt><dd>切换已看</dd>
		<dt>T</dt><dd>打开添加标签</dd>
		<dt>回车</dt><dd>播放视频</dd>
	  </dl>
	  <p class="help-text">输入框、下拉框和弹窗处于焦点时不会触发快捷键。</p>
	  <div class="modal-actions"><button type="button" class="btn-primary" @click="showShortcutHelp = false">知道了</button></div>
	</BaseModal>
</template>

<script>
import BaseModal from '../ui/BaseModal.vue';

// 基本设置分区。表单对象由 SettingsPage 持有并传下来，这里只绑定字段；
// 「查看快捷键」弹窗只有这一个入口，跟着一起放在这里。
export default {
  name: 'BasicSection',
  components: { BaseModal },
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return { showShortcutHelp: false };
  }
};
</script>

<style scoped>
.shortcut-help-list {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 10px 18px;
  margin: 18px 0;
}

.shortcut-help-list dt {
  color: var(--text-primary);
  font-weight: 700;
}

.shortcut-help-list dd {
  margin: 0;
  color: var(--text-secondary);
}

</style>
