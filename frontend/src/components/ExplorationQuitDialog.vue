<template>
  <Teleport v-if="request" to="body">
    <DialogShell title="正在保存浏览位置" size="sm" :busy="saving && !error" data-test="exploration-quit" @close="cancel">
      <p v-if="!error" role="status">正在保存这一墙的展示记录和缩放位置…</p>
      <p v-else role="alert">{{ error }} 尚未确认的浏览记录可能丢失。</p>
      <template v-if="error" #footer>
        <button type="button" class="btn btn--secondary" @click="cancel">继续运行</button>
        <button type="button" class="btn btn--secondary" @click="retry">重试保存</button>
        <button type="button" class="btn btn--danger" @click="discard">仍然退出</button>
      </template>
    </DialogShell>
  </Teleport>
</template>

<script>
import { FinishMediaExplorationClose } from '../../wailsjs/go/main/App';
import DialogShell from './ui/DialogShell.vue';
export default {
  name: 'ExplorationQuitDialog', components: { DialogShell },
  props: { flush: { type: Function, required: true }, resume: { type: Function, required: true } },
  data: () => ({ request: '', saving: false, error: '', generation: 0 }),
  mounted() {
    this._offs = [];
    for (const [name, handler] of [
      ['exploration-close-flush', payload => this.save(payload?.request_id)],
      ['exploration-close-timeout', payload => { if (payload?.request_id === this.request) { this.generation++; this.saving = false; this.error = '保存等待已超时。'; } }]
    ]) { const off = window.runtime?.EventsOn?.(name, handler); if (typeof off === 'function') this._offs.push(off); }
  },
  beforeUnmount() { this.generation++; for (const off of this._offs || []) off(); },
  methods: {
    async save(id) {
      if (!id || (id === this.request && this.saving)) return;
      const ticket = ++this.generation; this.request = id; this.saving = true; this.error = '';
      try {
        await this.flush();
        if (ticket !== this.generation) return;
        await FinishMediaExplorationClose(id, 'saved');
        if (ticket === this.generation) this.request = '';
      } catch (error) { if (ticket === this.generation) this.error = error.message || String(error); }
      finally { if (ticket === this.generation) this.saving = false; }
    },
    async finish(action) {
      const id = this.request; this.generation++; this.saving = false;
      try { await FinishMediaExplorationClose(id, action); if (this.request === id) this.request = ''; if (action === 'cancel') this.resume(); }
      catch (error) { this.error = error.message || String(error); }
    },
    cancel() { if (!this.saving) return this.finish('cancel'); },
    retry() { return this.finish('retry'); },
    discard() { return this.finish('discard'); }
  }
};
</script>
