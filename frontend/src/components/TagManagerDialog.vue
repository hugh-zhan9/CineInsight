<template>
  <BaseModal v-if="visible" close-on-overlay stop-modal-clicks style="max-width: 900px; max-height: 90vh; overflow-y: auto;" @close="handleClose">
    <TagPersonConversionPanel v-if="conversionTagID" :key="conversionTagID" :tag-id="conversionTagID" @back="handleClose" @busy="conversionBusy = $event" @converted="handlePersonConverted" />
    <template v-else>
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
		<select v-model="newTag.namespace" class="select-input tag-category-create" aria-label="新标签的主题分类"><option value="">未分类</option><option v-for="category in assignableCategories" :key="category" :value="category">{{ category }}</option></select>
        <p v-if="tagCreateError" class="help-text" style="color: var(--danger-color);">{{ tagCreateError }}</p>
      </div>

      <div class="divider"></div>

      <div class="setting-item">
        <label>合并同义标签</label>
        <p class="help-text">先选要保留的标签，再勾选同义标签。视频和图片关联会转移到保留标签，来源标签随后删除。</p>
        <div class="merge-target-row">
          <span>保留目标</span>
          <select v-model.number="mergeTargetId" class="select-input merge-target-select">
            <option :value="0">选择要保留的标签</option>
            <option v-for="tag in mergeTargetOptions" :key="`target-${tag.id}`" :value="tag.id">{{ tag.name }}</option>
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
            </label>
            <div v-if="filteredMergeSourceTags.length === 0" class="merge-source-empty">没有符合筛选条件的来源标签</div>
          </div>
          <div class="merge-source-tools">
            <button type="button" class="btn-secondary btn-compact" :disabled="filteredMergeSourceTags.length === 0" @click="selectAllVisibleMergeSources">全选筛选结果</button>
            <button type="button" class="btn-secondary btn-compact" :disabled="mergeSourceIds.length === 0" @click="clearMergeSources">清空已选</button>
          </div>
        </div>
        <p v-else class="merge-source-empty merge-source-empty--target">选择要保留的标签后即可多选来源标签。</p>
        <div class="merge-actions">
          <span v-if="mergeError" class="help-text merge-error">{{ mergeError }}</span>
          <button class="btn-primary" :disabled="mergeLoading || !canMerge" @click="handleMergeTags">
            {{ mergeLoading ? '合并中...' : '合并标签' }}
          </button>
        </div>
      </div>

      <div class="divider"></div>

      <!-- 标签列表 -->
      <div class="tag-list-heading">
        <label>全部标签</label>
        <span v-if="tagKeyword" class="tag-list-count">显示 {{ filteredTags.length }} / {{ localTags.length }}</span>
        <span v-else class="tag-list-count">共 {{ localTags.length }} 个</span>
      </div>
      <input
        v-model.trim="tagKeyword"
        type="search"
        class="text-input tag-filter-input"
        placeholder="搜索标签名称..."
        aria-label="搜索标签"
      />
	  <p class="help-text tag-category-help">所有非自动标签都可供 AI 打标选择。分类可在下方统一管理。</p>
      <div class="tag-list-container" style="max-height: 260px; overflow-y: auto; padding-right: 4px;">
        <div v-for="tag in filteredTags" :key="tag.id" class="tag-edit-row">
          <input v-model="tag.color" type="color" class="color-picker" :disabled="Boolean(tag.automatic_kind)" style="width: 28px; height: 28px; border: none; padding: 0; background: none; cursor: pointer; border-radius: 4px;" />
          <input v-model="tag.name" type="text" class="text-input" :disabled="Boolean(tag.automatic_kind)" style="height: 32px; font-size: 13px;" />
		  <select v-model="tag.namespace" class="select-input tag-category-edit" :disabled="Boolean(tag.automatic_kind)" :aria-label="`${tag.name}的主题分类`"><option value="">未分类</option><option v-if="tag.namespace && !assignableCategories.includes(tag.namespace)" :value="tag.namespace">{{ tag.namespace }}</option><option v-for="category in assignableCategories" :key="category" :value="category">{{ category }}</option></select>
          <div style="display: flex; gap: 6px;">
            <span v-if="tag.automatic_kind" class="system-tag-note">自动标签</span>
            <template v-else>
              <span v-if="isDirtyTag(tag)" class="tag-dirty-note" :data-test="`tag-dirty-${tag.id}`">未保存</span>
              <button @click="saveTag(tag)" class="btn-secondary">保存</button>
              <button v-if="canConvertToPerson(tag)" type="button" class="btn-secondary" @click="openPersonConversion(tag)">转为人物</button>
              <button @click.stop="$emit('request-delete-tag', tag)" class="btn-secondary" style="color: var(--danger-color); border-color: var(--danger-color);">删除</button>
            </template>
          </div>
        </div>
        <div v-if="localTags.length === 0" class="help-text" style="text-align: center; padding: 20px;">暂无标签</div>
        <div v-else-if="filteredTags.length === 0" class="help-text" style="text-align: center; padding: 20px;">没有匹配“{{ tagKeyword }}”的标签</div>
      </div>

      <div class="divider"></div>
      <div class="tag-manager-panel">
        <h3>分类管理</h3>
        <p class="help-text">标签按主题分类。新建分类时选择至少一个标签；删除分类只清除分类归属，标签仍保留。</p>
        <div class="category-create"><input v-model.trim="newCategoryName" class="text-input" placeholder="新分类名称" aria-label="新分类名称" /><button type="button" class="btn-primary" :disabled="categorySaving" @click="handleCreateCategory">新建分类</button></div>
        <div class="category-tag-picker" aria-label="选择加入新分类的标签"><label v-for="tag in categoryEligibleTags" :key="tag.id"><input v-model="newCategoryTagIDs" type="checkbox" :value="Number(tag.id)" />{{ tag.name }}</label><p v-if="!categoryEligibleTags.length" class="help-text">请先创建一个标签。</p></div>
        <p v-if="categoryError" role="alert" class="merge-error">{{ categoryError }}</p>
        <div v-for="category in categorySuggestions" :key="category" class="category-row"><span>{{ category }} · {{ categoryCount(category) }} 个标签</span><template v-if="category === '自动'"><span class="help-text">系统分类，不可改名或删除</span></template><template v-else><input v-model.trim="categoryDrafts[category]" class="text-input" :aria-label="`重命名分类 ${category}`" /><button type="button" class="btn-secondary" :disabled="categorySaving" @click="handleRenameCategory(category)">改名</button><button type="button" class="btn-secondary category-delete" :disabled="categorySaving" @click="handleDeleteCategory(category)">删除</button></template></div>
        <p v-if="!categorySuggestions.length" class="help-text">暂无分类。</p>
      </div>

      <div class="divider"></div>
      <!-- D-PC34：最近的标签转人物记录，可撤销（META-02）。撤销恢复原标签与打标关系，
           移除这次新增的人物关系；本次新建且已无其他关系的人物一并删除。 -->
      <div class="tag-manager-panel tag-conversion-history" data-test="tag-conversion-history">
        <h3>最近转换</h3>
        <p v-if="conversionsError" class="merge-error" role="alert">{{ conversionsError }}</p>
        <p v-if="conversionsLoading && !conversions.length" class="help-text">正在读取转换记录…</p>
        <p v-else-if="!conversions.length && !conversionsError" class="help-text">还没有把标签转为人物的记录。</p>
        <div v-for="record in conversions" :key="record.id" class="conversion-row" :data-test="`tag-conversion-${record.id}`">
          <span class="conversion-row__main">
            <strong>「{{ record.tag_name }}」→ 人物「{{ record.person_name || `人物 #${record.person_id}` }}」</strong>
            <small>{{ record.video_count }} 部视频、{{ record.image_count }} 张图片 · {{ formatConversionTime(record.created_at) }}<template v-if="record.person_created"> · 转换时新建了人物</template></small>
          </span>
          <span v-if="record.state === 'undone'" class="system-tag-note">已撤销</span>
          <button
            v-else-if="record.undoable"
            type="button"
            class="btn-secondary btn-compact"
            :disabled="undoingConversionID !== 0"
            :data-test="`tag-conversion-undo-${record.id}`"
            @click="undoConversion(record)"
          >{{ undoingConversionID === record.id ? '撤销中…' : '撤销' }}</button>
          <span v-else class="system-tag-note" title="原标签已被清理，无法恢复">不可撤销</span>
        </div>
      </div>

      <div class="modal-actions">
        <button @click="handleClose" class="btn-secondary">完成</button>
      </div>
    </template>
  </BaseModal>
</template>

<script>
import { CreateTagWithCategory, MergeTags, UpdateTagWithCategory, TriggerAITagging, CreateTagCategory, RenameTagCategory, DeleteTagCategory, GetTagUsageCounts, ListTagPersonConversions, UndoTagPersonConversion } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import TagPersonConversionPanel from './TagPersonConversionPanel.vue';
import { confirmAction, notify, notifyError } from '../utils/feedback.js';

// 撤销标签转人物的错误码（D-PC34）。
const UNDO_ERROR_TEXT = {
  conversion_not_applied: '这次转换已经撤销过了，记录已刷新。',
  conversion_not_undoable: '原标签已被清理，无法撤销这次转换。',
  tag_name_taken: '已经有同名的标签，无法恢复原标签。请先改名或合并那个标签，再撤销这次转换。'
};
const RECENT_CONVERSION_LIMIT = 10;

// 合并确认框里的影响范围（META-14）：来源标签上的视频与图片（回收站里的另列）。
function usageSummary(counts, ids) {
  const total = { videos: 0, images: 0, trashedVideos: 0, trashedImages: 0 };
  for (const id of ids) {
    const usage = counts?.[id] || counts?.[String(id)] || {};
    total.videos += Number(usage.videos || 0);
    total.images += Number(usage.images || 0);
    total.trashedVideos += Number(usage.trashed_videos || 0);
    total.trashedImages += Number(usage.trashed_images || 0);
  }
  return total;
}

export default {
  name: 'TagManagerDialog',
  components: { BaseModal, TagPersonConversionPanel },
  props: {
    visible: { type: Boolean, default: false },
    tags: { type: Array, default: () => [] }
  },
  // conversion-undone（P-035 新增）：撤销标签转人物成功，载荷为后端 TagPersonConversionUndoResult。
  emits: ['close', 'tags-changed', 'request-delete-tag', 'person-converted', 'conversion-undone'],
  data() {
    return {
      conversionTagID: 0,
      conversionBusy: false,
      newCategoryName: '',
      newCategoryTagIDs: [],
      categoryDrafts: {},
      categorySaving: false,
      categoryError: '',
      newTag: { name: '', namespace: '' },
      createTagLoading: false,
      tagCreateError: '',
      localTags: [],
      categoryTags: [],
      tagKeyword: '',
      // 按 id 快照标签名，避免行内改名时该行立刻从筛选结果消失（改完保存后才随 props 刷新）。
      tagFilterNames: {},
      mergeTargetId: 0,
      mergeSourceIds: [],
      mergeKeyword: '',
      mergeLoading: false,
      mergeError: '',
      conversions: [],
      conversionsLoading: false,
      conversionsError: '',
      undoingConversionID: 0
    };
  },
  computed: {
	categorySuggestions() {
	  return [...new Set(this.categoryTags.map(tag => String(tag.namespace || '').trim()).filter(Boolean))]
		.sort((a, b) => a.localeCompare(b, 'zh-Hans-CN'));
	},
    assignableCategories() {
      return this.categorySuggestions.filter(name => name !== '自动');
    },
    categoryEligibleTags() {
      return this.categoryTags.filter(tag => !tag.automatic_kind);
    },
    filteredTags() {
      return this.localTags.filter(tag => this.matchesTagKeyword(tag));
    },
    mergeableTags() {
      return this.localTags.filter(tag => !tag.automatic_kind);
    },
    mergeTargetOptions() {
      return this.mergeableTags;
    },
    mergeSourceTags() {
      const target = this.mergeableTags.find(tag => Number(tag.id) === Number(this.mergeTargetId));
      if (!target) return [];
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
    },
    // D-PC37：改了但还没点「保存」的行（名称、颜色、分类任一与已保存值不同）。
    dirtyTagIDs() {
      return this.localTags.filter(tag => this.isDirtyTag(tag)).map(tag => Number(tag.id));
    }
  },
  watch: {
    mergeTargetId() {
      const allowed = new Set(this.mergeSourceTags.map(tag => Number(tag.id)));
      this.mergeSourceIds = this.mergeSourceIds.filter(id => allowed.has(Number(id)));
    },
    tags: {
      handler(val) {
        this.localTags = val.map(t => ({ ...t }));
        this.categoryTags = val.map(t => ({ ...t }));
        this.categoryDrafts = Object.fromEntries([...new Set(val.map(t => String(t.namespace || '').trim()).filter(Boolean))].map(name => [name, name]));
        this.tagFilterNames = val.reduce((acc, t) => {
          acc[t.id] = String(t.name || '');
          return acc;
        }, {});
      },
      immediate: true,
      deep: true
    },
    visible(val) {
      if (val) {
        this.conversionTagID = 0;
        this.conversionBusy = false;
        this.tagCreateError = '';
        this.mergeError = '';
        this.mergeTargetId = 0;
        this.mergeSourceIds = [];
        this.mergeKeyword = '';
        this.tagKeyword = '';
        this.newCategoryName = '';
        this.newCategoryTagIDs = [];
        this.categoryError = '';
        this.localTags = this.tags.map(t => ({ ...t }));
        this.categoryTags = this.tags.map(t => ({ ...t }));
        this.loadConversions();
      }
    }
  },
  mounted() {
    if (this.visible) this.loadConversions();
  },
  methods: {
    async handleClose() {
      if (this.conversionBusy) return;
      if (this.conversionTagID) { this.conversionTagID = 0; return; }
      const dirty = this.dirtyTagIDs.length;
      if (dirty && !await confirmAction({ title: '放弃未保存的修改', message: `放弃 ${dirty} 项未保存修改？这些标签会保持上次保存时的名称、颜色和分类。`, confirmText: '放弃修改', danger: true })) return;
      this.$emit('close');
    },
    isDirtyTag(tag) {
      if (!tag || tag.automatic_kind) return false;
      const saved = this.tags.find(item => Number(item.id) === Number(tag.id));
      if (!saved) return false;
      return String(saved.name || '') !== String(tag.name || '')
        || String(saved.color || '') !== String(tag.color || '')
        || String(saved.namespace || '') !== String(tag.namespace || '');
    },
    async loadConversions() {
      const token = Symbol('tag-conversions');
      this._conversionsToken = token;
      this.conversionsLoading = true;
      this.conversionsError = '';
      try {
        const records = await ListTagPersonConversions(RECENT_CONVERSION_LIMIT);
        if (this._conversionsToken !== token) return;
        this.conversions = Array.isArray(records) ? records : [];
      } catch (err) {
        if (this._conversionsToken === token) this.conversionsError = '读取转换记录失败：' + err;
      } finally {
        if (this._conversionsToken === token) this.conversionsLoading = false;
      }
    },
    formatConversionTime(value) {
      const date = value ? new Date(value) : null;
      return date && Number.isFinite(date.getTime()) ? date.toLocaleString() : '时间未知';
    },
    async undoConversion(record) {
      if (!record?.undoable || this.undoingConversionID) return;
      const personPart = record.person_created ? '；这次新建的人物如果已经没有其他关系，也会一并删除' : '';
      const confirmed = await confirmAction({
        title: '撤销标签转人物',
        message: `撤销后恢复标签「${record.tag_name}」及其 ${record.video_count} 部视频、${record.image_count} 张图片的打标关系，并移除这次新增的人物关系${personPart}。`,
        confirmText: '撤销转换',
        danger: true
      });
      if (!confirmed) return;
      this.undoingConversionID = Number(record.id);
      this.conversionsError = '';
      try {
        const result = await UndoTagPersonConversion(Number(record.id));
        this.conversions = this.conversions.map(item => (Number(item.id) === Number(record.id) ? { ...item, state: 'undone', undoable: false } : item));
        const deletedPart = result?.person_deleted ? '，这次新建的人物已删除' : '';
        notify(`已撤销：恢复标签「${result?.tag?.name || record.tag_name}」，${Number(result?.video_count || 0)} 部视频、${Number(result?.image_count || 0)} 张图片${deletedPart}。`);
        this.$emit('tags-changed');
        this.$emit('conversion-undone', result);
      } catch (err) {
        const raw = String(err?.message || err || '');
        const code = Object.keys(UNDO_ERROR_TEXT).find(item => raw.includes(item));
        // 记录已经变了（别处撤销过、原标签被清理）就先重读，再把原因写上——重读会清空旧提示。
        if (code === 'conversion_not_applied' || code === 'conversion_not_undoable') await this.loadConversions();
        this.conversionsError = code ? UNDO_ERROR_TEXT[code] : `撤销失败：${raw}`;
      } finally {
        this.undoingConversionID = 0;
      }
    },
    canConvertToPerson(tag) {
      return !tag.automatic_kind && String(tag.namespace || '').trim() === '人物';
    },
    openPersonConversion(tag) {
      if (!this.canConvertToPerson(tag)) return;
      const saved = this.tags.find(item => Number(item.id) === Number(tag.id));
      if (!saved || saved.name !== tag.name || saved.namespace !== tag.namespace || saved.color !== tag.color) {
        notifyError('请先保存该标签的修改，再转为人物。');
        return;
      }
      this.conversionTagID = Number(tag.id);
    },
    handlePersonConverted(result) {
      this.conversionTagID = 0;
      this.conversionBusy = false;
      const keep = tag => Number(tag.id) !== Number(result.tag_id);
      this.localTags = this.localTags.filter(keep);
      this.categoryTags = this.categoryTags.filter(keep);
      this.mergeSourceIds = this.mergeSourceIds.filter(id => Number(id) !== Number(result.tag_id));
      this.newCategoryTagIDs = this.newCategoryTagIDs.filter(id => Number(id) !== Number(result.tag_id));
      if (Number(this.mergeTargetId) === Number(result.tag_id)) this.mergeTargetId = 0;
      notify(`已转为人物「${result.person.display_name}」，关联 ${result.video_count} 部视频、${result.image_count} 张图片，原标签已删除。可在「最近转换」里撤销。`);
      this.$emit('tags-changed');
      this.$emit('person-converted', result);
      this.loadConversions();
    },
    categoryCount(name) {
      return this.categoryTags.filter(tag => String(tag.namespace || '').trim() === name).length;
    },
    async refreshAfterChange() {
      this.$emit('tags-changed');
      try { await TriggerAITagging(); }
      catch (err) { notifyError('标签已保存，但唤醒 AI 打标失败: ' + err); }
    },
    async handleCreateCategory() {
      if (this.categorySaving) return;
      const name = this.newCategoryName.trim();
      if (!name || !this.newCategoryTagIDs.length) { this.categoryError = '输入分类名称并选择至少一个标签。'; return; }
      if (name === '自动') { this.categoryError = '“自动”是系统分类，不能手动创建。'; return; }
      this.categorySaving = true;
      this.categoryError = '';
      try {
        await CreateTagCategory(name, this.newCategoryTagIDs.map(Number));
        for (const list of [this.localTags, this.categoryTags]) list.forEach(tag => { if (this.newCategoryTagIDs.includes(Number(tag.id))) tag.namespace = name; });
        this.categoryDrafts[name] = name;
        this.newCategoryName = '';
        this.newCategoryTagIDs = [];
        await this.refreshAfterChange();
      } catch (err) { this.categoryError = '新建分类失败: ' + err; }
      finally { this.categorySaving = false; }
    },
    async handleRenameCategory(oldName) {
      if (this.categorySaving) return;
      const name = String(this.categoryDrafts[oldName] || '').trim();
      if (!name || name === oldName) { this.categoryError = '请输入不同的分类名称。'; return; }
      if (name === '自动' || oldName === '自动') { this.categoryError = '“自动”是系统分类，不能手动改名。'; return; }
      this.categorySaving = true;
      this.categoryError = '';
      try {
        await RenameTagCategory(oldName, name);
        for (const list of [this.localTags, this.categoryTags]) list.forEach(tag => { if (!tag.automatic_kind && tag.namespace === oldName) tag.namespace = name; });
        delete this.categoryDrafts[oldName];
        this.categoryDrafts[name] = name;
        await this.refreshAfterChange();
      } catch (err) { this.categoryError = '分类改名失败: ' + err; }
      finally { this.categorySaving = false; }
    },
    async handleDeleteCategory(name) {
      if (this.categorySaving) return;
      if (name === '自动') { this.categoryError = '“自动”是系统分类，不能手动删除。'; return; }
      if (!await confirmAction({ title: '删除分类', message: `删除「${name}」后，其中的标签都会归入「未分类」，标签本身保留。`, confirmText: '删除分类', danger: true })) return;
      this.categorySaving = true;
      this.categoryError = '';
      try {
        await DeleteTagCategory(name);
        for (const list of [this.localTags, this.categoryTags]) list.forEach(tag => { if (!tag.automatic_kind && tag.namespace === name) tag.namespace = ''; });
        delete this.categoryDrafts[name];
        await this.refreshAfterChange();
      } catch (err) { this.categoryError = '删除分类失败: ' + err; }
      finally { this.categorySaving = false; }
    },
    matchesTagKeyword(tag) {
      const keyword = this.tagKeyword.trim().toLocaleLowerCase();
      if (!keyword) return true;
      // 用快照名匹配：行内编辑时该行不会因为改名而中途消失。
      const snapshot = this.tagFilterNames[tag?.id];
      const name = snapshot === undefined ? String(tag?.name || '') : snapshot;
      return name.toLocaleLowerCase().includes(keyword);
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
        await CreateTagWithCategory(name, '', this.newTag.namespace || '');
        this.newTag.name = '';
		this.newTag.namespace = '';
        await this.refreshAfterChange();
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
        notify('标签名称不能为空');
        return;
      }
      try {
        await UpdateTagWithCategory(tag.id, name, tag.color, tag.namespace || '');
        await this.refreshAfterChange();
      } catch (err) {
        notifyError('更新失败: ' + err);
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
      if (!target) return;
      // META-14：确认框里说清受影响的视频与图片数；统计失败只在文案里说明，不挡合并本身。
      let impact = '';
      try {
        const usage = usageSummary(await GetTagUsageCounts(sourceIds), sourceIds);
        const trashed = usage.trashedVideos || usage.trashedImages ? `（回收站中另有 ${usage.trashedVideos} 部视频、${usage.trashedImages} 张图片）` : '';
        impact = `${usage.videos} 部视频、${usage.images} 张图片${trashed}上的这些标签会改为「${target.name}」。`;
      } catch (err) {
        impact = `（受影响的媒体数统计失败：${err}）`;
      }
      if (!await confirmAction({ title: '合并标签', message: `确定将 ${sourceNames} 合并到「${target.name}」吗？${impact}源标签会被删除，此操作不能自动撤销。`, confirmText: '合并', danger: true })) return;
      this.mergeLoading = true;
      this.mergeError = '';
      try {
        await MergeTags(sourceIds, Number(this.mergeTargetId));
        this.mergeSourceIds = [];
        this.mergeTargetId = 0;
        this.mergeKeyword = '';
        await this.refreshAfterChange();
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
.tag-manager-panel { min-height: 250px; }
.category-create { display: flex; gap: 8px; margin-top: 14px; }
.category-create input { flex: 1; min-width: 0; }
.category-tag-picker { display: flex; flex-wrap: wrap; gap: 8px; max-height: 160px; overflow-y: auto; margin: 10px 0 18px; }
.category-tag-picker label { display: inline-flex; align-items: center; gap: 5px; padding: 6px 9px; border: 1px solid var(--border-color); border-radius: 7px; }
.category-tag-picker small { color: var(--text-secondary); }
.category-row { display: grid; grid-template-columns: minmax(110px, 1fr) minmax(120px, 1fr) auto auto; align-items: center; gap: 8px; padding: 10px 0; border-top: 1px solid var(--border-color); }
.category-delete { color: var(--danger-color); border-color: var(--danger-color); }
.tag-edit-row { display: flex; align-items: center; gap: 8px; padding: 10px 0; border-bottom: 1px solid var(--border-color); }
.tag-edit-row > input[type="text"] { min-width: 0; flex: 1; }
.tag-category-create { width: 100%; margin-top: 8px; }
.tag-category-help { margin: 0 0 6px; }
.tag-edit-row > .tag-category-edit { flex: 0 0 130px; min-width: 0; }
.tag-list-container::-webkit-scrollbar { width: 4px; }
.merge-filter-input { width: calc(100% - 16px); margin: 8px 8px 0; }
.tag-list-heading { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; }
.tag-list-count { color: var(--text-secondary); font-size: 12px; }
.tag-filter-input { width: 100%; margin: 8px 0; }
.merge-target-row { display: grid; grid-template-columns: 72px minmax(0, 1fr); align-items: center; gap: 10px; margin-top: 8px; color: var(--text-secondary); font-size: 12px; }
.merge-target-select { width: 100%; }
.merge-source-picker { margin-top: 10px; overflow: hidden; border: 1px solid var(--border-color); border-radius: 10px; background: var(--control-bg); }
.merge-source-heading { display: flex; justify-content: space-between; gap: 8px; padding: 9px 10px; border-bottom: 1px solid var(--border-color); color: var(--text-secondary); font-size: 12px; }
.merge-selected-tags { display: flex; flex-wrap: wrap; gap: 6px; padding: 8px 10px 0; }
.merge-selected-tags button { height: 25px; padding: 0 8px; border: 1px solid var(--accent-border); border-radius: 999px; background: var(--accent-soft); color: var(--accent-color); cursor: pointer; font-size: 11px; }
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
.tag-dirty-note { align-self: center; color: var(--warning-text); font-size: 12px; white-space: nowrap; }
.tag-conversion-history { min-height: 0; }
.conversion-row { display: flex; align-items: center; gap: 10px; padding: 10px 0; border-top: 1px solid var(--border-color); }
.conversion-row__main { display: grid; flex: 1; gap: 3px; min-width: 0; }
.conversion-row__main strong { overflow-wrap: anywhere; font-size: 13px; }
.conversion-row__main small { color: var(--text-secondary); font-size: 12px; }
</style>
