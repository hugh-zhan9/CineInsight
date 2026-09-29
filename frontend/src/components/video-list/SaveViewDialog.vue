<template>
  <BaseModal v-if="saveViewDialog.show" class="download-modal">
      <h3>{{ dialogTitle }}</h3>
      <!-- 三种用法（D-PC35、LIB-15）：另存为新视图、用当前条件覆盖已有视图、只改名字。 -->
      <div v-if="savedViews.length > 0" class="merge-type-switch save-view-modes" role="group" aria-label="保存方式">
        <button type="button" :class="{ active: saveViewDialog.mode === 'create' }" data-test="save-view-mode-create" @click="switchMode('create')">另存为新视图</button>
        <button type="button" :class="{ active: saveViewDialog.mode === 'update' }" data-test="save-view-mode-update" @click="switchMode('update')">用当前条件更新</button>
        <button type="button" :class="{ active: saveViewDialog.mode === 'rename' }" data-test="save-view-mode-rename" @click="switchMode('rename')">重命名</button>
      </div>
      <select
        v-if="saveViewDialog.mode !== 'create'"
        v-model.number="saveViewDialog.viewID"
        class="select-input save-view-target"
        aria-label="要修改的视图"
        data-test="save-view-target"
        @change="syncTargetName"
      >
        <option v-for="view in savedViews" :key="view.id" :value="view.id">{{ view.name }}</option>
      </select>
      <input
        ref="saveViewNameInput"
        v-model="saveViewDialog.name"
        type="text"
        maxlength="80"
        class="search-input rename-input"
        placeholder="输入视图名称"
        data-test="save-view-name"
        @keyup.enter="saveCurrentView"
      />
      <p v-if="modeHint" class="save-view-hint">{{ modeHint }}</p>
      <p v-if="saveViewDialog.error" class="cleanup-error" role="alert">{{ saveViewDialog.error }}</p>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" @click="saveViewDialog.show = false">取消</button>
        <button type="button" class="btn-primary" :disabled="saveViewDialog.saving" data-test="save-view-submit" @click="saveCurrentView">
          {{ saveViewDialog.saving ? '保存中...' : submitLabel }}
        </button>
      </div>
  </BaseModal>
</template>

<script>
import { SaveLibraryView, UpdateSavedLibraryView } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';

// 保存视图里的 tag_ids_json / person_ids_json 是 JSON 数组；读不出来按空处理，不让一条坏记录拖垮菜单。
export function parseSavedViewIDs(json) {
  try {
    const parsed = JSON.parse(json || '[]');
    return Array.isArray(parsed) ? [...new Set(parsed.map(Number).filter(id => Number.isInteger(id) && id > 0))] : [];
  } catch (_err) {
    return [];
  }
}

// 只改名时原样保留视图里存的条件（包括已被删除的标签与人物 ID：设计上删除标签不改视图，D-PC35）。
export function savedViewFilter(view) {
  return {
    search_mode: view.search_mode || 'file',
    keyword: view.keyword || '',
    path_prefix: '',
    smart_view: view.smart_view || '',
    tag_ids: parseSavedViewIDs(view.tag_ids_json),
    person_ids: parseSavedViewIDs(view.person_ids_json),
    stale_reason: '',
    min_size: Number(view.min_size || 0),
    max_size: Number(view.max_size || 0),
    min_height: Number(view.min_height || 0),
    max_height: Number(view.max_height || 0),
    min_rating: view.min_rating ?? null,
    max_rating: view.max_rating ?? null,
    sort_mode: view.sort_mode || 'balanced'
  };
}

const SAVE_VIEW_ERROR_TEXT = {
  saved_view_name_taken: '已经有同名的视图了，换一个名字吧。'
};

function saveViewErrorText(err) {
  const raw = String(err?.message || err || '');
  const code = Object.keys(SAVE_VIEW_ERROR_TEXT).find(item => raw.includes(item));
  return code ? SAVE_VIEW_ERROR_TEXT[code] : raw;
}

// 「保存当前片库视图」弹窗。筛选条件与保存后的列表刷新都还在片库页，
// 这里通过两个函数 prop 拿到它们，保存流程本身的先后顺序不变。
export default {
  name: 'SaveViewDialog',
  components: { BaseModal },
  props: {
    currentLibraryFilter: { type: Function, required: true },
    afterSaved: { type: Function, required: true },
    savedViews: { type: Array, default: () => [] },
    // 最近一次应用的视图：更新与改名默认就是它。
    activeViewId: { type: Number, default: 0 }
  },
  data() {
    return {
      saveViewDialog: { show: false, mode: 'create', viewID: 0, name: '', saving: false, error: '' }
    };
  },
  computed: {
    dialogTitle() {
      if (this.saveViewDialog.mode === 'update') return '用当前条件更新视图';
      if (this.saveViewDialog.mode === 'rename') return '重命名视图';
      return '保存当前片库视图';
    },
    submitLabel() {
      if (this.saveViewDialog.mode === 'update') return '更新';
      if (this.saveViewDialog.mode === 'rename') return '重命名';
      return '保存';
    },
    modeHint() {
      if (this.saveViewDialog.mode === 'update') return '所选视图的条件会被替换成现在列表上的条件，名字可以顺便改。';
      if (this.saveViewDialog.mode === 'rename') return '只改名字，视图里的条件保持不变。';
      return '';
    },
    targetView() {
      return this.savedViews.find(view => Number(view.id) === Number(this.saveViewDialog.viewID)) || null;
    }
  },
  methods: {
    open(mode = 'create') {
      return this.openSaveViewDialog(mode);
    },
    openSaveViewDialog(mode = 'create') {
      const normalized = this.savedViews.length > 0 && ['update', 'rename'].includes(mode) ? mode : 'create';
      this.saveViewDialog = { show: true, mode: 'create', viewID: 0, name: '', saving: false, error: '' };
      this.switchMode(normalized);
      this.$nextTick(() => this.$refs.saveViewNameInput?.focus());
    },
    switchMode(mode) {
      this.saveViewDialog.mode = mode;
      this.saveViewDialog.error = '';
      if (mode === 'create') {
        this.saveViewDialog.viewID = 0;
        this.saveViewDialog.name = '';
        return;
      }
      const preferred = this.savedViews.find(view => Number(view.id) === Number(this.activeViewId)) || this.savedViews[0];
      this.saveViewDialog.viewID = Number(preferred?.id || 0);
      this.syncTargetName();
    },
    syncTargetName() {
      this.saveViewDialog.name = this.targetView?.name || '';
    },
    async saveCurrentView() {
      const name = this.saveViewDialog.name.trim();
      if (!name || this.saveViewDialog.saving) return;
      const mode = this.saveViewDialog.mode;
      const target = this.targetView;
      if (mode !== 'create' && !target) return;
      this.saveViewDialog.saving = true;
      this.saveViewDialog.error = '';
      try {
        let saved;
        if (mode === 'update') {
          saved = await UpdateSavedLibraryView(target.id, name, this.currentLibraryFilter());
        } else if (mode === 'rename') {
          saved = await UpdateSavedLibraryView(target.id, name, savedViewFilter(target));
        } else {
          saved = await SaveLibraryView({ name, ...this.currentLibraryFilter() });
        }
        await this.afterSaved(saved);
        this.saveViewDialog.show = false;
      } catch (err) {
        this.saveViewDialog.error = saveViewErrorText(err);
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
.save-view-modes {
  margin-top: 12px;
  grid-template-columns: repeat(3, 1fr);
}
.save-view-target {
  width: 100%;
  margin-top: 12px;
}
.save-view-hint {
  margin: 0 0 8px;
  color: var(--text-muted);
  font-size: 12px;
}
.cleanup-error,
.cleanup-intro,
.cleanup-loading,
.cleanup-empty {
  color: var(--review-text-muted);
  font-size: 13px;
}
</style>
