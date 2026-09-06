<template>
  <div class="glossary-editor">
    <p v-if="hint" class="help-text" data-test="glossary-hint">{{ hint }}</p>
    <div class="glossary-list">
      <div v-for="entry in entries" :key="entry.id" class="glossary-row" data-test="glossary-entry">
        <div class="glossary-terms">
          <strong>{{ entry.source_term }}</strong>
          <span class="glossary-arrow">→</span>
          <strong>{{ entry.target_term }}</strong>
        </div>
        <span v-if="entry.note" class="glossary-note">{{ entry.note }}</span>
        <div class="glossary-actions">
          <button type="button" class="btn-secondary btn-compact" :data-test="`glossary-edit-${entry.id}`" @click="startEdit(entry)">编辑</button>
          <button type="button" class="btn-secondary btn-danger-outline btn-compact" :data-test="`glossary-delete-${entry.id}`" @click="remove(entry)">删除</button>
        </div>
      </div>
      <div v-if="!entries.length" class="glossary-empty" data-test="glossary-empty">还没有术语条目。</div>
    </div>

    <div class="glossary-form">
      <input
        type="text"
        class="text-input"
        maxlength="200"
        placeholder="源词"
        data-test="glossary-source-input"
        v-model.trim="draft.source_term"
      />
      <input
        type="text"
        class="text-input"
        maxlength="200"
        placeholder="目标词"
        data-test="glossary-target-input"
        v-model.trim="draft.target_term"
      />
      <input
        type="text"
        class="text-input"
        maxlength="500"
        placeholder="备注（可选）"
        data-test="glossary-note-input"
        v-model.trim="draft.note"
      />
      <div class="glossary-form-actions">
        <button type="button" class="btn-primary btn-compact" :disabled="!canSave" data-test="glossary-save" @click="save">
          {{ draft.id ? '保存修改' : '添加术语' }}
        </button>
        <button v-if="draft.id" type="button" class="btn-secondary btn-compact" data-test="glossary-cancel" @click="resetDraft">取消</button>
      </div>
    </div>
    <p v-if="error" class="glossary-error" data-test="glossary-error">{{ error }}</p>
  </div>
</template>

<script>
import { DeleteGlossaryEntry, ListGlossaryEntries, UpsertGlossaryEntry } from '../../wailsjs/go/main/App';
import { confirmAction } from '../utils/feedback.js';

const emptyDraft = () => ({ id: 0, source_term: '', target_term: '', note: '' });

// 字幕翻译术语表编辑器（D-033）。同一个组件服务两个作用域：collectionId 为 0 时维护
// 全局表，非 0 时维护该作品集的表；作用域的覆盖规则在服务端解析，这里不参与。
export default {
  name: 'GlossaryEditor',
  props: {
    collectionId: { type: Number, default: 0 },
    hint: { type: String, default: '' }
  },
  data() {
    return { entries: [], draft: emptyDraft(), error: '' };
  },
  computed: {
    canSave() {
      return Boolean(this.draft.source_term && this.draft.target_term);
    }
  },
  watch: {
    collectionId() {
      this.resetDraft();
      this.load();
    }
  },
  mounted() {
    this.load();
  },
  methods: {
    async load() {
      try {
        this.entries = await ListGlossaryEntries(Number(this.collectionId) || 0) || [];
        this.error = '';
      } catch (err) {
        this.entries = [];
        this.error = String(err);
      }
    },
    resetDraft() {
      this.draft = emptyDraft();
    },
    startEdit(entry) {
      this.draft = {
        id: Number(entry.id),
        source_term: entry.source_term || '',
        target_term: entry.target_term || '',
        note: entry.note || ''
      };
    },
    async save() {
      if (!this.canSave) return;
      const collectionID = Number(this.collectionId) || 0;
      try {
        await UpsertGlossaryEntry({
          id: this.draft.id || 0,
          collection_id: collectionID || null,
          source_term: this.draft.source_term,
          target_term: this.draft.target_term,
          note: this.draft.note
        });
        this.resetDraft();
        await this.load();
      } catch (err) {
        this.error = String(err);
      }
    },
    async remove(entry) {
      if (!await confirmAction({
        title: '删除术语',
        message: `确定要删除术语「${entry.source_term}」吗？`,
        confirmText: '删除',
        danger: true
      })) return;
      try {
        await DeleteGlossaryEntry(Number(entry.id));
        if (this.draft.id === Number(entry.id)) this.resetDraft();
        await this.load();
      } catch (err) {
        this.error = String(err);
      }
    }
  }
};
</script>

<style scoped>
.glossary-editor {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.glossary-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.glossary-empty {
  color: var(--text-muted);
  font-size: 12px;
}

.glossary-error {
  color: var(--danger-color);
  font-size: 12px;
}

.glossary-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 11px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  background: var(--surface-faint);
}

.glossary-terms {
  display: flex;
  flex: 1 1 auto;
  align-items: center;
  gap: 8px;
  min-width: 0;
  overflow: hidden;
}

.glossary-terms strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
}

.glossary-arrow {
  color: var(--text-secondary);
}

.glossary-note {
  flex: 0 1 auto;
  max-width: 40%;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-secondary);
  font-size: 12px;
}

.glossary-actions {
  display: flex;
  gap: 8px;
  margin-left: auto;
}

.glossary-form {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.glossary-form .text-input {
  flex: 1 1 140px;
  min-width: 0;
}

.glossary-form-actions {
  display: flex;
  gap: 8px;
}

@media (max-width: 980px) {
  .glossary-row {
    align-items: stretch;
    flex-direction: column;
  }

  .glossary-actions {
    justify-content: flex-end;
    margin-left: 0;
  }
}
</style>
