<template>
  <div v-if="items.length" class="task-failure-list" :data-test="dataTest">
    <ul>
      <li
        v-for="(failure, index) in visibleFailures"
        :key="`${keyPrefix}${failure.video_id ?? failure.media_id ?? failure.image_id ?? ''}:${failure.name || ''}:${index}`"
        :title="describe(failure)"
      >{{ describe(failure) }}</li>
    </ul>
    <button
      v-if="items.length > limit"
      type="button"
      class="btn-link task-failure-list__toggle"
      data-test="task-failure-toggle"
      @click="expanded = !expanded"
    >{{ toggleText }}</button>
    <p v-if="truncated" class="task-failure-list__capped" data-test="task-failure-capped">共 {{ total }} 条失败，只保留了前 {{ items.length }} 条明细。</p>
  </div>
</template>

<script>
// 后台任务的失败明细：后端每轮最多带 50 条、每条最长 500 字符的错误文本。
// 直接平铺会把整条状态条撑成一屏，所以默认只露前几条、每条一行截断（悬停看全文），
// 其余折进"展开"，展开后也只在固定高度内滚动。total 是这一轮的失败总数：比带来的明细多时（后端截在 50 条）
// 照实说只保留了前几条，展开按钮也不说「全部」。
export default {
  name: 'TaskFailureList',
  props: {
    failures: { type: Array, default: () => [] },
    limit: { type: Number, default: 3 },
    keyPrefix: { type: String, default: '' },
    dataTest: { type: String, default: undefined },
    fallbackLabel: { type: String, default: '视频' },
    total: { type: Number, default: 0 },
  },
  data() {
    return { expanded: false };
  },
  computed: {
    // Go 的 nil 切片会序列化成 null，别让它把整条状态条炸掉。
    items() {
      return Array.isArray(this.failures) ? this.failures : [];
    },
    truncated() {
      return Number(this.total) > this.items.length;
    },
    toggleText() {
      if (this.expanded) return '收起';
      return this.truncated ? `展开已保留的 ${this.items.length} 条` : `展开全部 ${this.items.length} 条`;
    },
    visibleFailures() {
      return this.expanded ? this.items : this.items.slice(0, this.limit);
    },
  },
  watch: {
    // 任务跑着的时候每个进度事件都会带一份新的数组（内容只增不减），这时不能收起，
    // 否则用户刚展开就被下一次事件折回去。只有列表变短——新一轮任务清空重来——才收起。
    items(next, prev) {
      if (next.length < (prev?.length || 0)) this.expanded = false;
    },
  },
  methods: {
    describe(failure) {
      const id = failure?.video_id ?? failure?.media_id ?? failure?.image_id;
      const name = failure?.name || (id ? `${this.fallbackLabel} #${id}` : this.fallbackLabel);
      return `${name}：${failure?.error || ''}`;
    },
  },
};
</script>

<style scoped>
.task-failure-list {
  flex: 1 1 100%;
  min-width: 0;
  display: grid;
  gap: 6px;
  margin-top: 6px;
  font-size: var(--fs-caption);
  color: var(--text-secondary);
}

.task-failure-list ul {
  margin: 0;
  padding-left: 18px;
  display: grid;
  gap: 3px;
  grid-auto-rows: max-content;
  max-height: 168px;
  overflow-y: auto;
}

.task-failure-list li {
  line-height: 1.5;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-failure-list__capped {
  margin: 0;
}

.task-failure-list__toggle {
  justify-self: start;
  font-size: var(--fs-caption);
}
</style>
