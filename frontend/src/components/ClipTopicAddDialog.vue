<template>
  <Teleport to="body"><DialogShell title="加入片段专题" :busy="busy" @close="$emit('close')">
    <p>将 {{ bookmarkIds.length }} 条书签加入专题；已有成员不会重复或改变位置。</p>
    <ClipTopicSelect v-model="selected" :api="api" :disabled="busy" />
    <p v-if="error" role="alert">{{ error }}</p>
    <template #footer><button type="button" class="btn btn--primary" :disabled="busy || !selected || !bookmarkIds.length || bookmarkIds.length > 200" data-test="clip-topic-add-confirm" @click="save">加入专题</button></template>
  </DialogShell></Teleport>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import ClipTopicSelect from './ClipTopicSelect.vue';
import { clipTopicAPI, clipError } from '../utils/clipTopics.js';
export default {
  name: 'ClipTopicAddDialog', components: { DialogShell, ClipTopicSelect }, props: { bookmarkIds: { type: Array, required: true }, api: { type: Object, default: () => clipTopicAPI } }, emits: ['close', 'saved'],
  data: () => ({ selected: null, busy: false, error: '', alive: true }), beforeUnmount() { this.alive = false; },
  methods: { async save() {
    if (this.busy || !this.selected || !this.bookmarkIds.length || this.bookmarkIds.length > 200) return;
    this.busy = true; this.error = '';
    try { const result = await this.api.edit({ topic_id: this.selected.topic.id, expected_revision: this.selected.topic.revision, action: 'add', bookmark_ids: [...this.bookmarkIds] }); if (this.alive) this.$emit('saved', result); }
    catch (error) { if (this.alive) this.error = clipError(error); }
    finally { if (this.alive) this.busy = false; }
  } }
};
</script>
