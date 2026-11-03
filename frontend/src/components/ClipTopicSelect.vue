<template>
  <div class="clip-topic-select">
    <label>专题<select class="select-input" :value="modelValue?.topic.id || 0" :disabled="disabled || loading" aria-label="选择专题" @change="select(Number($event.target.value))"><option :value="0">请选择专题</option><option v-for="item in items" :key="item.topic.id" :value="item.topic.id">{{ item.topic.name }} · {{ item.member_count }} 项 · #{{ item.topic.id }}</option></select></label>
    <p v-if="error" role="alert">{{ error }}</p><p v-if="!loading && !items.length && !error" class="help-text">还没有专题，可在「观看笔记 → 片段专题」新建。</p>
    <button v-if="cursor" type="button" class="btn btn--secondary btn--sm" :disabled="disabled || loading" @click="load(false)">加载更多专题</button>
    <button type="button" class="btn btn--ghost btn--sm" :disabled="disabled || loading" @click="load(true)">刷新专题</button>
  </div>
</template>
<script>
import { clipTopicAPI, clipError } from '../utils/clipTopics.js';
export default {
  name: 'ClipTopicSelect', props: { modelValue: { type: Object, default: null }, disabled: Boolean, api: { type: Object, default: () => clipTopicAPI } }, emits: ['update:modelValue'],
  data: () => ({ items: [], cursor: 0, loading: false, error: '', generation: 0 }), mounted() { this.load(true); }, beforeUnmount() { this.generation++; },
  methods: {
    select(id) { this.$emit('update:modelValue', this.items.find(item => item.topic.id === id) || null); },
    async load(reset) {
      const ticket = ++this.generation; this.loading = true; this.error = '';
      if (reset) { this.items = []; this.cursor = 0; this.$emit('update:modelValue', null); }
      try { const page = await this.api.list({ cursor_id: this.cursor, limit: 50 }); if (ticket !== this.generation) return; this.items = [...this.items, ...page.items]; this.cursor = page.next_cursor; }
      catch (error) { if (ticket === this.generation) this.error = clipError(error); }
      finally { if (ticket === this.generation) this.loading = false; }
    }
  }
};
</script>
<style scoped>
.clip-topic-select label { display: grid; gap: var(--space-2); }
</style>
