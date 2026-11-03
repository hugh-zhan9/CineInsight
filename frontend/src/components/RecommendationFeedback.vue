<template>
  <div class="recommendation-feedback">
    <template v-if="video">
      <button type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="apply('work_video', video.id)">7 天内不推荐</button>
      <label v-if="video.tags?.length">标签偏好 <select v-model.number="tag" class="select-input" :disabled="busy"><option :value="0">请选择标签</option><option v-for="item in video.tags" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
      <template v-if="tag">
        <span>{{ tagLoading ? '正在读取偏好…' : `当前：${effectLabel(tagPreference?.effect)}` }}</span>
        <button type="button" class="btn btn--ghost btn--sm" :disabled="busy || tagLoading || !tagReady" @click="prefer('boost')">增加推荐</button>
        <button type="button" class="btn btn--ghost btn--sm" :disabled="busy || tagLoading || !tagReady" @click="prefer('downweight')">减少推荐</button>
        <button v-if="tagPreference" type="button" class="btn btn--ghost btn--sm" :disabled="busy || tagLoading || !tagReady" @click="prefer('neutral')">恢复中立</button>
      </template>
    </template>
    <button type="button" class="btn btn--ghost btn--sm" @click="manage">管理推荐反馈</button>
    <div v-if="open">
      <p>作品暂避 7 天；标签偏好长期有效，可恢复中立。同一作品命中增加与减少推荐时，减少推荐优先；多个标签不重复叠加。以下反馈只影响随机、今晚看什么和首页选片。</p>
      <p v-if="!items.length">没有推荐反馈。</p>
      <ul><li v-for="item in items" :key="item.id">{{ item.label }} · {{ effectLabel(item.effect) }}{{ item.active ? '' : '（已过期或目标失效）' }} <span v-if="item.expires_at">截至 {{ new Date(item.expires_at).toLocaleString() }}</span> <button v-if="item.effect === 'snooze'" type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="renew(item)">重新暂避 7 天</button> <button type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="revoke(item)">撤销</button></li></ul>
      <button type="button" class="btn btn--ghost" @click="open = false">收起</button>
    </div>
    <p v-if="error" role="alert">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
  </div>
</template>
<script>
import { SetRecommendationFeedback, SetRecommendationTagPreference, ListRecommendationFeedback, RevokeRecommendationFeedback } from '../../wailsjs/go/main/App';
export default {
  name: 'RecommendationFeedback',
  props: { expanded: Boolean, video: { type: Object, default: null } },
  emits: ['changed'],
  data: () => ({ tag: 0, tagPreference: null, tagLoading: false, tagReady: false, tagRequest: 0, busy: false, open: false, items: [], error: '', notice: '' }),
  watch: { expanded(value) { if (value) this.manage(); }, tag() { this.loadTagPreference(); }, 'video.id'() { ++this.tagRequest; this.tag = 0; this.tagPreference = null; this.tagReady = false; } },
  beforeUnmount() { ++this.tagRequest; },
  methods: {
    effectLabel(effect) { return { snooze: '7 天内不推荐', boost: '增加推荐', downweight: '减少推荐' }[effect] || '中立'; },
    async loadTagPreference() {
      const request = ++this.tagRequest, tag = this.tag; this.tagPreference = null; this.tagReady = false;
      if (!tag) { this.tagLoading = false; return; }
      this.tagLoading = true;
      try {
        const items = await ListRecommendationFeedback();
        if (request !== this.tagRequest) return;
        this.tagPreference = items.find(item => item.target_kind === 'tag' && Number(item.target_id) === tag) || null; this.tagReady = true;
      } catch { if (request === this.tagRequest) this.error = '读取标签偏好失败，请重新选择标签'; }
      finally { if (request === this.tagRequest) this.tagLoading = false; }
    },
    async prefer(effect) {
      if (!this.tagReady || this.busy || this.tagLoading) return;
      const tag = this.tag, old = this.tagPreference; this.busy = true; this.error = ''; this.notice = '';
      try {
        const result = await SetRecommendationTagPreference({ tag_id: tag, effect, id: old?.id || 0, expected_revision: old?.revision || 0 });
        if (result.status !== 'ok') throw new Error(result.error?.message || '推荐偏好未保存');
        if (this.tag === tag) { this.notice = `此标签已设为${this.effectLabel(effect)}`; await this.loadTagPreference(); }
        this.$emit('changed'); if (this.open) await this.manage();
      } catch (error) { this.error = String(error); if (this.tag === tag) await this.loadTagPreference(); }
      finally { this.busy = false; }
    },
    async renew(item) {
      this.busy = true;
      this.error = '';
      try {
        await SetRecommendationFeedback({ target_kind: item.target_kind, target_id: item.target_id, id: item.id, expected_revision: item.revision });
        await this.manage();
        this.$emit('changed');
      } catch (error) {
        this.error = String(error);
      } finally {
        this.busy = false;
      }
    },
    async apply(kind, id) {
      this.busy = true;
      this.error = '';
      this.notice = '';
      try {
        await SetRecommendationFeedback({ target_kind: kind, target_id: id, id: 0, expected_revision: 0 });
        this.notice = kind === 'tag' ? '已减少此标签的推荐，可在管理推荐反馈中撤销。' : '这部作品将在 7 天内不参与推荐。';
        this.$emit('changed');
        if (this.open) await this.manage();
      } catch (error) {
        this.error = String(error);
        this.open = true;
        try { this.items = await ListRecommendationFeedback(); } catch { /* Preserve the original action error. */ }
      } finally {
        this.busy = false;
      }
    },
    async manage() {
      this.open = true;
      this.error = '';
      try {
        this.items = await ListRecommendationFeedback();
      } catch (error) {
        this.error = String(error);
      }
    },
    async revoke(item) {
      this.busy = true;
      this.error = '';
      try {
        await RevokeRecommendationFeedback(item.id, item.revision);
        await this.manage();
        this.$emit('changed');
      } catch (error) {
        this.error = String(error);
      } finally {
        this.busy = false;
      }
    }
  }
};
</script>
