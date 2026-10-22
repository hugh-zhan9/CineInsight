<template>
  <div class="recommendation-feedback">
    <template v-if="video">
      <button type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="apply('work_video', video.id)">7 天内不推荐</button>
      <label v-if="video.tags?.length">减少推荐标签 <select v-model.number="tag" class="select-input"><option :value="0">请选择标签</option><option v-for="item in video.tags" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
      <button v-if="tag" type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="apply('tag', tag)">减少推荐</button>
    </template>
    <button type="button" class="btn btn--ghost btn--sm" @click="manage">管理推荐反馈</button>
    <div v-if="open">
      <p>作品暂避 7 天；标签减少推荐长期有效。以下反馈会影响随机、今晚看什么和首页选片。</p>
      <p v-if="!items.length">没有推荐反馈。</p>
      <ul><li v-for="item in items" :key="item.id">{{ item.label }} · {{ item.effect === 'snooze' ? '7 天内不推荐' : '减少推荐' }}{{ item.active ? '' : '（已过期或目标失效）' }} <span v-if="item.expires_at">截至 {{ new Date(item.expires_at).toLocaleString() }}</span> <button v-if="item.effect === 'snooze'" type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="renew(item)">重新暂避 7 天</button> <button type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="revoke(item)">撤销</button></li></ul>
      <button type="button" class="btn btn--ghost" @click="open = false">收起</button>
    </div>
    <p v-if="error" role="alert">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
  </div>
</template>
<script>
import { SetRecommendationFeedback, ListRecommendationFeedback, RevokeRecommendationFeedback } from '../../wailsjs/go/main/App';
export default {
  name: 'RecommendationFeedback',
  props: { expanded: Boolean, video: { type: Object, default: null } },
  emits: ['changed'],
  data: () => ({ tag: 0, busy: false, open: false, items: [], error: '', notice: '' }),
  watch: { expanded(value) { if (value) this.manage(); } },
  methods: {
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
