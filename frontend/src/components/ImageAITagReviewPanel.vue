<template>
  <BaseModal v-if="visible" class="image-ai-tag-review" data-test="image-ai-tag-review" @close="$emit('close')">
    <header class="image-ai-tag-review__header">
      <div>
        <h3>AI 标签审阅</h3>
        <p class="image-ai-tag-review__subtitle">
          AI 只会从标签库里选标签，接受后才会真正挂到图片上。
        </p>
      </div>
      <button type="button" class="btn-secondary btn-compact" data-test="image-ai-tag-review-close" @click="$emit('close')">关闭</button>
    </header>

    <div class="image-ai-tag-review__toolbar">
      <div class="image-ai-tag-review__filters" role="group" aria-label="置信度筛选">
        <button
          v-for="option in confidenceOptions"
          :key="option.value"
          type="button"
          :class="['btn-secondary', 'btn-compact', { active: confidence === option.value }]"
          :data-test="`image-ai-tag-confidence-${option.value || 'all'}`"
          @click="setConfidence(option.value)"
        >{{ option.label }}</button>
      </div>
      <span class="image-ai-tag-review__count" data-test="image-ai-tag-review-count">
        待审 {{ candidates.length }} 条 · 涉及 {{ groups.length }} 张图片
      </span>
    </div>

    <p v-if="error" class="image-ai-tag-review__error" role="alert" data-test="image-ai-tag-review-error">{{ error }}</p>
    <p v-if="notice" class="image-ai-tag-review__notice" role="status" data-test="image-ai-tag-review-notice">{{ notice }}</p>
    <p v-if="loading" class="image-ai-tag-review__status" role="status">正在加载候选…</p>
    <p
      v-else-if="!candidates.length"
      class="image-ai-tag-review__status"
      role="status"
      data-test="image-ai-tag-review-empty"
    >没有待审候选。AI 打标跑完后，候选会出现在这里。</p>

    <section
      v-for="group in groups"
      :key="group.imageID"
      class="image-ai-tag-review__group glass-surface"
      data-test="image-ai-tag-group"
    >
      <div class="image-ai-tag-review__group-head">
        <strong :title="group.name">{{ group.name }}</strong>
        <span v-if="group.deleted" class="image-ai-tag-review__deleted" data-test="image-ai-tag-deleted">图片已删除</span>
        <button
          type="button"
          class="btn-secondary btn-compact"
          :disabled="busy"
          :data-test="`image-ai-tag-reject-all-${group.imageID}`"
          @click="rejectAll(group)"
        >全部拒绝</button>
      </div>
      <ul class="image-ai-tag-review__list">
        <li v-for="candidate in group.items" :key="candidate.id" class="image-ai-tag-review__item">
          <span class="tag-chip" :style="{ '--tag-color': candidate.matched_tag?.color }">{{ candidate.suggested_name }}</span>
          <span :class="['image-ai-tag-review__confidence', `is-${candidate.confidence}`]">{{ confidenceLabel(candidate.confidence) }}</span>
          <span class="image-ai-tag-review__reason" :title="candidate.reasoning">{{ candidate.reasoning }}</span>
          <span class="image-ai-tag-review__actions">
            <button
              type="button"
              class="btn-primary btn-compact"
              :disabled="busy || group.deleted"
              :data-test="`image-ai-tag-approve-${candidate.id}`"
              @click="approve(candidate)"
            >接受</button>
            <button
              type="button"
              class="btn-secondary btn-compact"
              :disabled="busy"
              :data-test="`image-ai-tag-reject-${candidate.id}`"
              @click="reject(candidate)"
            >拒绝</button>
          </span>
        </li>
      </ul>
    </section>
  </BaseModal>
</template>

<script>
import {
  ApproveImageAITagCandidate, ListImageAITagCandidates,
  RejectImageAITagCandidate, RejectImageAITagCandidatesByImage
} from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';

export default {
  name: 'ImageAITagReviewPanel',
  components: { BaseModal },
  props: {
    visible: { type: Boolean, default: false }
  },
  emits: ['close', 'changed'],
  data() {
    return {
      candidates: [],
      confidence: '',
      loading: false,
      busy: false,
      error: '',
      // notice 与 error 分开：接受候选被整体作废是"说明为什么没挂上标签"，不是调用失败，
      // 而且 load() 会清空 error，混用会让这条说明在紧随其后的刷新里被冲掉。
      notice: '',
      confidenceOptions: [
        { value: '', label: '全部' },
        { value: 'high', label: '高' },
        { value: 'medium', label: '中' }
      ]
    };
  },
  computed: {
    // 按图片分组：审阅时用户是在看"这张图该打哪些标签"，逐条平铺会让同一张图的候选散开。
    groups() {
      const byImage = new Map();
      for (const candidate of this.candidates) {
        const imageID = candidate.image_id;
        if (!byImage.has(imageID)) {
          byImage.set(imageID, {
            imageID,
            name: candidate.image?.name || `图片 #${imageID}`,
            deleted: Boolean(candidate.image_deleted),
            items: []
          });
        }
        byImage.get(imageID).items.push(candidate);
      }
      return Array.from(byImage.values());
    }
  },
  watch: {
    visible(value) {
      if (value) this.load();
    }
  },
  mounted() {
    if (this.visible) this.load();
  },
  methods: {
    confidenceLabel(value) {
      return { high: '高', medium: '中', low: '低' }[value] || value;
    },
    setConfidence(value) {
      if (this.confidence === value) return;
      this.confidence = value;
      this.load();
    },
    async load() {
      this.loading = true;
      this.error = '';
      try {
        this.candidates = (await ListImageAITagCandidates(0, this.confidence, '')) || [];
      } catch (err) {
        this.error = `加载候选失败: ${err}`;
        this.candidates = [];
      } finally {
        this.loading = false;
      }
    },
    async approve(candidate) {
      if (this.busy) return;
      this.busy = true;
      this.error = '';
      this.notice = '';
      try {
        const item = await ApproveImageAITagCandidate(candidate.id);
        // 该图已有手工标签时后端不写入标签，而是把待审候选整体作废。
        // 这不是错误，但用户点的是"接受"，得让他知道为什么标签没出现。
        if (item && item.status === 'superseded') {
          this.notice = '这张图片已经有你手工打的标签，AI 候选已整体作废，没有写入标签。';
        }
        this.$emit('changed');
        await this.load();
      } catch (err) {
        this.error = `接受候选失败: ${err}`;
      } finally {
        this.busy = false;
      }
    },
    async reject(candidate) {
      if (this.busy) return;
      this.busy = true;
      this.error = '';
      this.notice = '';
      try {
        await RejectImageAITagCandidate(candidate.id);
        await this.load();
      } catch (err) {
        this.error = `拒绝候选失败: ${err}`;
      } finally {
        this.busy = false;
      }
    },
    async rejectAll(group) {
      if (this.busy) return;
      this.busy = true;
      this.error = '';
      this.notice = '';
      try {
        await RejectImageAITagCandidatesByImage(group.imageID);
        await this.load();
      } catch (err) {
        this.error = `批量拒绝失败: ${err}`;
      } finally {
        this.busy = false;
      }
    }
  }
};
</script>

<style scoped>
.image-ai-tag-review {
  width: min(920px, 92vw);
  max-height: 82vh;
  overflow-y: auto;
}

.image-ai-tag-review__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}

.image-ai-tag-review__header h3 {
  margin: 0 0 4px;
}

.image-ai-tag-review__subtitle {
  margin: 0;
  font-size: 12px;
  color: var(--text-secondary);
}

.image-ai-tag-review__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.image-ai-tag-review__filters {
  display: flex;
  gap: 6px;
}

.image-ai-tag-review__count {
  font-size: 12px;
  color: var(--text-secondary);
}

.image-ai-tag-review__error {
  margin: 0 0 12px;
  color: var(--danger-color, #d9534f);
  font-size: 13px;
}

.image-ai-tag-review__notice {
  margin: 0 0 12px;
  color: var(--text-secondary);
  font-size: 13px;
}

.image-ai-tag-review__status {
  margin: 24px 0;
  text-align: center;
  color: var(--text-secondary);
  font-size: 13px;
}

.image-ai-tag-review__group {
  padding: 12px;
  border-radius: 10px;
  margin-bottom: 12px;
}

.image-ai-tag-review__group-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
}

.image-ai-tag-review__group-head strong {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.image-ai-tag-review__deleted {
  font-size: 12px;
  color: var(--danger-color, #d9534f);
}

.image-ai-tag-review__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.image-ai-tag-review__item {
  display: flex;
  align-items: center;
  gap: 10px;
}

.image-ai-tag-review__confidence {
  font-size: 12px;
  color: var(--text-secondary);
}

.image-ai-tag-review__confidence.is-high {
  color: var(--success-color, #2f9e44);
}

.image-ai-tag-review__reason {
  flex: 1;
  font-size: 12px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.image-ai-tag-review__actions {
  display: flex;
  gap: 6px;
}
</style>
