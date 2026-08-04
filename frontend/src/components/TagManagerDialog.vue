<template>
  <BaseModal v-if="visible" close-on-overlay stop-modal-clicks style="max-width: 520px; max-height: 90vh; overflow-y: auto;" @close="$emit('close')">
      <h2>标签管理</h2>
      
      <!-- 创建新标签 -->
      <div class="setting-item">
        <label>新建标签</label>
        <div style="display: flex; gap: 8px; margin-top: 8px;">
          <input 
            v-model="newTag.name" 
            type="text" 
            placeholder="输入标签名称..." 
            class="text-input" 
            style="flex: 1;"
            @keyup.enter="handleCreateTag" 
          />
          <button @click="handleCreateTag" class="btn-primary" :disabled="createTagLoading">添加</button>
        </div>
        <p v-if="tagCreateError" class="help-text" style="color: var(--danger-color);">{{ tagCreateError }}</p>
      </div>

      <div class="divider"></div>

      <div class="setting-item">
        <label>合并同义标签</label>
        <p class="help-text">先选要保留的目标，再勾选一个或多个普通或 AI 来源标签。视频关联会转移到目标标签，来源随后删除；合并结果保留目标标签的类型。</p>
        <div class="merge-type-row">
          <span>目标类型</span>
          <div class="merge-type-switch" role="group" aria-label="选择目标标签类型">
            <button type="button" :class="{ active: mergeType === 'normal' }" :aria-pressed="mergeType === 'normal'" @click="mergeType = 'normal'">普通标签</button>
            <button type="button" :class="{ active: mergeType === 'ai' }" :aria-pressed="mergeType === 'ai'" @click="mergeType = 'ai'">AI 标签</button>
          </div>
        </div>
        <div class="merge-target-row">
          <span>保留目标</span>
          <select v-model.number="mergeTargetId" class="select-input merge-target-select">
            <option :value="0">选择要保留的标签</option>
            <option v-for="tag in mergeTargetOptions" :key="`target-${tag.id}`" :value="tag.id">{{ tag.name }} · {{ tag.is_system ? 'AI' : '普通' }}</option>
          </select>
        </div>
        <div v-if="mergeTargetId" class="merge-source-picker">
          <div class="merge-source-heading">
            <span>选择来源（可多选）</span>
            <span>已选 {{ mergeSourceIds.length }} 个</span>
          </div>
          <input
            v-model.trim="mergeKeyword"
            type="search"
            class="text-input merge-filter-input"
            placeholder="筛选来源标签名称..."
            aria-label="筛选待合并标签"
          />
          <div v-if="selectedMergeSourceTags.length" class="merge-selected-tags" aria-label="已选择的来源标签">
            <button v-for="tag in selectedMergeSourceTags" :key="`selected-${tag.id}`" type="button" @click="toggleMergeSource(tag.id, false)">
              {{ tag.name }} <span>×</span>
            </button>
          </div>
          <div class="merge-source-list">
            <label v-for="tag in filteredMergeSourceTags" :key="`source-${tag.id}`" class="merge-source-option">
              <input
                type="checkbox"
                :checked="mergeSourceIds.includes(Number(tag.id))"
                @change="toggleMergeSource(tag.id, $event.target.checked)"
              />
              <span class="merge-source-color" :style="{ backgroundColor: tag.color || 'var(--accent-color)' }"></span>
              <span class="merge-source-name">{{ tag.name }}</span>
              <small>{{ tag.is_system ? 'AI 标签' : '普通标签' }}</small>
            </label>
            <div v-if="filteredMergeSourceTags.length === 0" class="merge-source-empty">没有符合筛选条件的来源标签</div>
          </div>
          <div class="merge-source-tools">
            <button type="button" class="btn-secondary btn-compact" :disabled="filteredMergeSourceTags.length === 0" @click="selectAllVisibleMergeSources">全选筛选结果</button>
            <button type="button" class="btn-secondary btn-compact" :disabled="mergeSourceIds.length === 0" @click="clearMergeSources">清空已选</button>
          </div>
        </div>
        <p v-else class="merge-source-empty merge-source-empty--target">选择目标标签后即可多选普通或 AI 来源标签。</p>
        <div class="merge-actions">
          <span v-if="mergeError" class="help-text merge-error">{{ mergeError }}</span>
          <button class="btn-primary" :disabled="mergeLoading || !canMerge" @click="handleMergeTags">
            {{ mergeLoading ? '合并中...' : '合并标签' }}
          </button>
        </div>
      </div>

      <div class="divider"></div>

      <!-- 标签列表 -->
      <div class="tag-list-container" style="max-height: 260px; overflow-y: auto; padding-right: 4px;">
        <div v-for="tag in localTags" :key="tag.id" class="tag-edit-row">
          <input v-model="tag.color" type="color" class="color-picker" :disabled="tag.is_system || tag.automatic_kind" style="width: 28px; height: 28px; border: none; padding: 0; background: none; cursor: pointer; border-radius: 4px;" />
          <input v-model="tag.name" type="text" class="text-input" :disabled="tag.is_system || tag.automatic_kind" style="height: 32px; font-size: 13px;" />
          <div style="display: flex; gap: 6px;">
            <span v-if="tag.is_system" class="system-tag-note">AI 标签</span>
            <span v-else-if="tag.automatic_kind" class="system-tag-note">自动标签</span>
            <template v-else>
              <button @click="saveTag(tag)" class="btn-secondary">保存</button>
              <button @click.stop="$emit('request-delete-tag', tag)" class="btn-secondary" style="color: var(--danger-color); border-color: var(--danger-color);">删除</button>
            </template>
          </div>
        </div>
        <div v-if="localTags.length === 0" class="help-text" style="text-align: center; padding: 20px;">暂无标签</div>
      </div>

      <div class="modal-actions">
        <button @click="$emit('close')" class="btn-secondary">完成</button>
      </div>
  </BaseModal>
</template>

<script>
import { CreateTag, MergeTags, UpdateTag } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';

export default {
  name: 'TagManagerDialog',
  components: { BaseModal },
  props: {
    visible: { type: Boolean, default: false },
    tags: { type: Array, default: () => [] }
  },
  emits: ['close', 'tags-changed', 'request-delete-tag'],
  data() {
    return {
      newTag: { name: '' },
      createTagLoading: false,
      tagCreateError: '',
      localTags: [],
      mergeTargetId: 0,
      mergeSourceIds: [],
      mergeType: 'normal',
      mergeKeyword: '',
      mergeLoading: false,
      mergeError: ''
    };
  },
  computed: {
    mergeableTags() {
      return this.localTags.filter(tag => !tag.automatic_kind);
    },
    mergeTargetOptions() {
      return this.mergeableTags.filter(tag => this.matchesMergeType(tag));
    },
    mergeSourceTags() {
      const target = this.mergeableTags.find(tag => Number(tag.id) === Number(this.mergeTargetId));
      if (!target || !this.matchesMergeType(target)) return [];
      return this.mergeableTags.filter(tag => Number(tag.id) !== Number(target.id));
    },
    filteredMergeSourceTags() {
      return this.mergeSourceTags.filter(tag => this.matchesMergeKeyword(tag));
    },
    selectedMergeSourceTags() {
      const selected = new Set(this.mergeSourceIds.map(Number));
      return this.mergeSourceTags.filter(tag => selected.has(Number(tag.id)));
    },
    canMerge() {
      return this.mergeTargetId > 0 && this.mergeSourceIds.length > 0;
    }
  },
  watch: {
    mergeType() {
      this.mergeTargetId = 0;
      this.mergeSourceIds = [];
      this.mergeKeyword = '';
      this.mergeError = '';
    },
    mergeTargetId() {
      const allowed = new Set(this.mergeSourceTags.map(tag => Number(tag.id)));
      this.mergeSourceIds = this.mergeSourceIds.filter(id => allowed.has(Number(id)));
    },
    tags: {
      handler(val) {
        this.localTags = val.map(t => ({ ...t }));
      },
      immediate: true,
      deep: true
    },
    visible(val) {
      if (val) {
        this.tagCreateError = '';
        this.mergeError = '';
        this.mergeType = 'normal';
        this.mergeTargetId = 0;
        this.mergeSourceIds = [];
        this.mergeKeyword = '';
        this.localTags = this.tags.map(t => ({ ...t }));
      }
    }
  },
  methods: {
    matchesMergeType(tag) {
      return this.mergeType === 'ai' ? Boolean(tag?.is_system) : !Boolean(tag?.is_system);
    },
    matchesMergeKeyword(tag) {
      const keyword = this.mergeKeyword.trim().toLocaleLowerCase();
      return !keyword || String(tag?.name || '').toLocaleLowerCase().includes(keyword);
    },
    toggleMergeSource(tagID, selected) {
      const id = Number(tagID);
      if (!this.mergeSourceTags.some(tag => Number(tag.id) === id)) return;
      this.mergeSourceIds = selected
        ? [...new Set([...this.mergeSourceIds.map(Number), id])]
        : this.mergeSourceIds.filter(item => Number(item) !== id);
    },
    selectAllVisibleMergeSources() {
      this.mergeSourceIds = [...new Set([
        ...this.mergeSourceIds.map(Number),
        ...this.filteredMergeSourceTags.map(tag => Number(tag.id))
      ])];
    },
    clearMergeSources() {
      this.mergeSourceIds = [];
    },
    isDuplicateError(err) {
      const raw = err && (err.message || err.error || err.toString ? err.toString() : err);
      const msg = String(raw || '').toLowerCase();
      return msg.includes('tag_exists') || msg.includes('unique') || msg.includes('duplicate') || msg.includes('constraint');
    },
    async handleCreateTag() {
      if (this.createTagLoading) return;
      const name = this.newTag.name.trim();
      if (!name) return;
      this.tagCreateError = '';
      if (this.localTags.some(t => String(t.name).toLowerCase() === name.toLowerCase())) {
        this.tagCreateError = '标签已存在';
        return;
      }
      this.createTagLoading = true;

      try {
        await CreateTag(name, '');
        this.newTag.name = '';
        this.$emit('tags-changed');
      } catch (err) {
        if (this.isDuplicateError(err)) {
          this.tagCreateError = '标签已存在';
          return;
        }
        this.tagCreateError = '创建失败';
      } finally {
        this.createTagLoading = false;
      }
    },
    async saveTag(tag) {
      const name = (tag.name || '').trim();
      if (!name) {
        alert('标签名称不能为空');
        return;
      }
      try {
        await UpdateTag(tag.id, name, tag.color);
        this.$emit('tags-changed');
      } catch (err) {
        alert('更新失败: ' + err);
      }
    },
    async handleMergeTags() {
      if (this.mergeLoading || !this.canMerge) return;
      const sourceIds = this.mergeSourceIds
        .map(Number)
        .filter(id => id > 0 && id !== Number(this.mergeTargetId));
      const target = this.mergeableTags.find(tag => Number(tag.id) === Number(this.mergeTargetId));
      const sourceNames = this.mergeableTags
        .filter(tag => sourceIds.includes(Number(tag.id)))
        .map(tag => `「${tag.name}」`)
        .join('、');
      if (!target || !window.confirm(`确定将 ${sourceNames} 合并到「${target.name}」吗？源标签会被删除，此操作不能自动撤销。`)) return;
      this.mergeLoading = true;
      this.mergeError = '';
      try {
        await MergeTags(sourceIds, Number(this.mergeTargetId));
        this.mergeSourceIds = [];
        this.mergeTargetId = 0;
        this.mergeKeyword = '';
        this.$emit('tags-changed');
      } catch (err) {
        this.mergeError = '合并失败: ' + String(err);
      } finally {
        this.mergeLoading = false;
      }
    }
  }
};
</script>

<style scoped>
.tag-edit-row { display: flex; align-items: center; gap: 10px; padding: 10px 0; border-bottom: 1px solid var(--border-color); }
.tag-list-container::-webkit-scrollbar { width: 4px; }
.merge-type-row { display: grid; grid-template-columns: 72px minmax(0, 1fr); align-items: center; gap: 10px; margin-top: 10px; color: var(--text-secondary); font-size: 12px; }
.merge-type-switch { display: grid; grid-template-columns: 1fr 1fr; padding: 3px; border: 1px solid var(--border-color); border-radius: 9px; background: var(--control-bg); }
.merge-type-switch button { min-height: 30px; border: 0; border-radius: 6px; background: transparent; color: var(--text-secondary); cursor: pointer; font-size: 12px; }
.merge-type-switch button.active { background: var(--accent-soft); color: var(--accent-color); font-weight: 600; }
.merge-filter-input { width: calc(100% - 16px); margin: 8px 8px 0; }
.merge-target-row { display: grid; grid-template-columns: 72px minmax(0, 1fr); align-items: center; gap: 10px; margin-top: 8px; color: var(--text-secondary); font-size: 12px; }
.merge-target-select { width: 100%; }
.merge-source-picker { margin-top: 10px; overflow: hidden; border: 1px solid var(--border-color); border-radius: 10px; background: var(--control-bg); }
.merge-source-heading { display: flex; justify-content: space-between; gap: 8px; padding: 9px 10px; border-bottom: 1px solid var(--border-color); color: var(--text-secondary); font-size: 12px; }
.merge-selected-tags { display: flex; flex-wrap: wrap; gap: 6px; padding: 8px 10px 0; }
.merge-selected-tags button { height: 25px; padding: 0 8px; border: 1px solid rgba(15, 143, 130, .3); border-radius: 999px; background: var(--accent-soft); color: var(--accent-color); cursor: pointer; font-size: 11px; }
.merge-selected-tags button span { margin-left: 3px; }
.merge-source-list { max-height: 168px; overflow-y: auto; padding: 7px; }
.merge-source-option { display: grid; grid-template-columns: 18px 10px minmax(0, 1fr) auto; align-items: center; gap: 8px; min-height: 34px; padding: 4px 7px; border-radius: 7px; cursor: pointer; }
.merge-source-option:hover { background: var(--control-hover-bg); }
.merge-source-option input { width: 15px; height: 15px; accent-color: var(--accent-color); }
.merge-source-color { width: 9px; height: 9px; border-radius: 50%; }
.merge-source-name { overflow: hidden; color: var(--text-primary); text-overflow: ellipsis; white-space: nowrap; }
.merge-source-option small { color: var(--text-muted); font-size: 11px; }
.merge-source-empty { padding: 18px 10px; color: var(--text-muted); text-align: center; font-size: 12px; }
.merge-source-empty--target { margin-top: 8px; padding: 12px; border: 1px dashed var(--border-color); border-radius: 9px; }
.merge-source-tools { display: flex; justify-content: flex-end; gap: 7px; padding: 8px; border-top: 1px solid var(--border-color); }
.merge-actions { display: flex; justify-content: space-between; align-items: center; gap: 8px; margin-top: 8px; }
.merge-error { color: var(--danger-color); }
.tag-list-container::-webkit-scrollbar-thumb { background: var(--border-color); border-radius: 4px; }
.color-picker::-webkit-color-swatch-wrapper { padding: 0; }
.color-picker::-webkit-color-swatch { border: 1px solid var(--border-color); border-radius: 4px; }
.system-tag-note { align-self: center; color: var(--text-secondary); font-size: 12px; white-space: nowrap; }
</style>
