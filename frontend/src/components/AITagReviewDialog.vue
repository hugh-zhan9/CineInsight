<template>
  <div v-if="visible" class="modal-overlay">
    <div class="modal ai-tag-review-modal">
      <div class="ai-tag-review-header">
        <div>
          <h3>AI 标签审阅</h3>
          <p class="help-text">待审 {{ candidates.length }} 条，高置信和中置信需人工确认后才会写入正式标签。</p>
          <p v-if="summary && !summary.config_available" class="ai-tag-warning">AI 配置不可用，后台分析已暂停。</p>
        </div>
        <button type="button" class="btn-secondary" @click="$emit('close')">关闭</button>
      </div>

      <div class="ai-tag-review-actions">
        <button type="button" class="btn-secondary" @click="loadCandidates" :disabled="loading">刷新</button>
      </div>

      <div v-if="loading" class="ai-tag-review-empty">加载中...</div>
      <div v-else-if="error" class="ai-tag-review-error">{{ error }}</div>
      <div v-else-if="groups.length === 0" class="ai-tag-review-empty">暂无待审 AI 标签</div>
      <div v-else class="ai-tag-review-list">
        <section v-for="group in groups" :key="group.videoId" class="ai-video-group">
          <div class="ai-video-title">
            <span>{{ group.videoName }}</span>
            <button type="button" class="btn-action" @click="retryVideo(group.videoId)" :disabled="processingIds.includes(group.videoId)">重新分析</button>
          </div>
          <div v-if="group.videoPath" class="ai-video-path">{{ group.videoPath }}</div>

          <div
            v-for="candidate in group.candidates"
            :key="candidate.id"
            class="ai-candidate-row"
          >
            <div class="ai-candidate-main">
              <span
                class="ai-confidence"
                :class="confidenceMeta(candidate.confidence).className"
                :data-confidence="candidate.confidence"
              >
                {{ confidenceMeta(candidate.confidence).label }}
              </span>
              <span class="ai-candidate-name">{{ candidate.suggested_name }}</span>
              <span v-if="candidate.matched_tag" class="ai-match-note">匹配已有：{{ candidate.matched_tag.name }}</span>
              <span v-else class="ai-match-note">新标签候选</span>
              <p v-if="candidate.reasoning" class="ai-candidate-reason">{{ candidate.reasoning }}</p>
            </div>
            <div class="ai-candidate-actions">
              <button type="button" class="btn-primary" @click="approve(candidate)" :disabled="processingIds.includes(candidate.id)">批准</button>
              <button type="button" class="btn-secondary" @click="reject(candidate)" :disabled="processingIds.includes(candidate.id)">拒绝</button>
            </div>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>

<script>
import { ApproveAITagCandidate, GetAITaggingStatusSummary, ListAITagCandidates, RejectAITagCandidate, RetryAITagging } from '../../wailsjs/go/main/App';
import { confidenceMeta, groupCandidatesByVideo, removeCandidateById } from '../utils/aiTagReview.js';

export default {
  name: 'AITagReviewDialog',
  props: {
    visible: { type: Boolean, default: false },
  },
  emits: ['close', 'changed'],
  data() {
    return {
      candidates: [],
      summary: null,
      loading: false,
      error: '',
      processingIds: [],
    };
  },
  computed: {
    groups() {
      return groupCandidatesByVideo(this.candidates);
    },
  },
  watch: {
    visible(value) {
      if (value) {
        this.loadCandidates();
      }
    },
  },
  methods: {
    confidenceMeta,
    async loadCandidates() {
      this.loading = true;
      this.error = '';
      try {
        const [summary, candidates] = await Promise.all([
          GetAITaggingStatusSummary(),
          ListAITagCandidates(0, '', 'pending'),
        ]);
        this.summary = summary;
        this.candidates = Array.isArray(candidates) ? candidates : [];
      } catch (err) {
        this.error = '加载 AI 标签候选失败: ' + err;
      } finally {
        this.loading = false;
      }
    },
    async approve(candidate) {
      await this.withProcessing(candidate.id, async () => {
        const item = await ApproveAITagCandidate(candidate.id);
        this.candidates = removeCandidateById(this.candidates, candidate.id);
        if (item?.status === 'superseded') {
          await this.loadCandidates();
        }
        this.$emit('changed');
      });
    },
    async reject(candidate) {
      await this.withProcessing(candidate.id, async () => {
        await RejectAITagCandidate(candidate.id);
        this.candidates = removeCandidateById(this.candidates, candidate.id);
      });
    },
    async retryVideo(videoId) {
      await this.withProcessing(videoId, async () => {
        await RetryAITagging(videoId);
        await this.loadCandidates();
      });
    },
    async withProcessing(id, action) {
      if (this.processingIds.includes(id)) return;
      this.processingIds = [...this.processingIds, id];
      this.error = '';
      try {
        await action();
      } catch (err) {
        this.error = String(err);
      } finally {
        this.processingIds = this.processingIds.filter(item => item !== id);
      }
    },
  },
};
</script>

<style scoped>
.ai-tag-review-modal {
  width: min(760px, calc(100vw - 40px));
  max-width: 760px;
  max-height: min(720px, calc(100vh - 48px));
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.ai-tag-review-header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: flex-start;
  border-bottom: 1px solid var(--border-color);
  padding-bottom: 14px;
}

.ai-tag-review-actions {
  display: flex;
  justify-content: flex-end;
  padding: 12px 0;
}

.ai-tag-review-list {
  overflow-y: auto;
  padding-right: 4px;
}

.ai-video-group {
  border-top: 1px solid var(--border-color);
  padding: 14px 0;
}

.ai-video-title {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  font-weight: 700;
  color: var(--text-primary);
}

.ai-video-path {
  margin-top: 4px;
  color: var(--text-muted);
  font-size: 12px;
  overflow-wrap: anywhere;
}

.ai-candidate-row {
  display: flex;
  justify-content: space-between;
  gap: 14px;
  padding: 12px 0;
  border-top: 1px solid rgba(148, 163, 184, 0.22);
}

.ai-candidate-main {
  min-width: 0;
}

.ai-confidence {
  display: inline-flex;
  align-items: center;
  height: 24px;
  padding: 0 8px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 700;
  margin-right: 8px;
}

.ai-confidence--high {
  color: #065f46;
  background: rgba(16, 185, 129, 0.16);
  border: 1px solid rgba(16, 185, 129, 0.35);
}

.ai-confidence--medium {
  color: #92400e;
  background: rgba(245, 158, 11, 0.16);
  border: 1px solid rgba(245, 158, 11, 0.35);
}

.ai-confidence--unknown {
  color: var(--text-secondary);
  background: rgba(148, 163, 184, 0.16);
  border: 1px solid rgba(148, 163, 184, 0.35);
}

.ai-candidate-name {
  font-weight: 700;
  color: var(--text-primary);
}

.ai-match-note {
  margin-left: 8px;
  color: var(--text-muted);
  font-size: 12px;
}

.ai-candidate-reason {
  margin: 8px 0 0;
  color: var(--text-secondary);
  font-size: 13px;
  line-height: 1.45;
}

.ai-candidate-actions {
  display: flex;
  gap: 8px;
  flex: 0 0 auto;
  align-items: flex-start;
}

.ai-tag-warning,
.ai-tag-review-error {
  color: var(--danger-color);
}

.ai-tag-review-empty {
  padding: 32px 0;
  text-align: center;
  color: var(--text-muted);
}
</style>
