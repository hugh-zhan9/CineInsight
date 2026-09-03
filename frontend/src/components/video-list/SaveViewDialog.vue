<template>
  <BaseModal v-if="saveViewDialog.show" class="download-modal">
      <h3>保存当前片库视图</h3>
      <input
        ref="saveViewNameInput"
        v-model="saveViewDialog.name"
        type="text"
        maxlength="80"
        class="search-input rename-input"
        placeholder="输入视图名称"
        @keyup.enter="saveCurrentView"
      />
      <p v-if="saveViewDialog.error" class="cleanup-error">{{ saveViewDialog.error }}</p>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" @click="saveViewDialog.show = false">取消</button>
        <button type="button" class="btn-primary" :disabled="saveViewDialog.saving" @click="saveCurrentView">
          {{ saveViewDialog.saving ? '保存中...' : '保存' }}
        </button>
      </div>
  </BaseModal>
</template>

<script>
import { SaveLibraryView } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';

// 「保存当前片库视图」弹窗。筛选条件与保存后的列表刷新都还在片库页，
// 这里通过两个函数 prop 拿到它们，保存流程本身的先后顺序不变。
export default {
  name: 'SaveViewDialog',
  components: { BaseModal },
  props: {
    currentLibraryFilter: { type: Function, required: true },
    afterSaved: { type: Function, required: true }
  },
  data() {
    return {
      saveViewDialog: { show: false, name: '', saving: false, error: '' }
    };
  },
  methods: {
    open() {
      return this.openSaveViewDialog();
    },
    openSaveViewDialog() {
      this.saveViewDialog = { show: true, name: '', saving: false, error: '' };
      this.$nextTick(() => this.$refs.saveViewNameInput?.focus());
    },
    async saveCurrentView() {
      const name = this.saveViewDialog.name.trim();
      if (!name || this.saveViewDialog.saving) return;
      this.saveViewDialog.saving = true;
      this.saveViewDialog.error = '';
      try {
        const saved = await SaveLibraryView({ name, ...this.currentLibraryFilter() });
        await this.afterSaved(saved);
        this.saveViewDialog.show = false;
      } catch (err) {
        this.saveViewDialog.error = String(err);
      } finally {
        this.saveViewDialog.saving = false;
      }
    },
  }
};
</script>

<style scoped>
:deep(.download-modal) {
  width: 400px;
  text-align: center;
  padding: 30px;
}
.rename-input {
  margin: 15px 0;
}
.cleanup-error,
.cleanup-intro,
.cleanup-loading,
.cleanup-empty {
  color: var(--review-text-muted);
  font-size: 13px;
}
</style>
