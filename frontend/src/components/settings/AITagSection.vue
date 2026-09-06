<template>
  <div :id="`settings-ai-tags`" class="settings-section">
    <h3>AI 标签</h3>
    <div class="setting-item">
      <label>接口地址</label>
      <input
        type="text"
        v-model.trim="form.ai_tagging_base_url"
        placeholder="https://api.openai.com/v1 或 http://127.0.0.1:1234/v1"
        class="text-input"
      />
    </div>
    <div class="setting-item">
      <label>API Key</label>
      <input
        type="password"
        v-model="form.ai_tagging_api_key"
        placeholder="本地 LM Studio 可留空；云端接口填写 API Key"
        class="text-input"
        autocomplete="off"
      />
    </div>
    <div class="setting-item">
      <label>模型</label>
      <input
        type="text"
        v-model.trim="form.ai_tagging_model"
        placeholder="支持图像理解的模型"
        class="text-input"
      />
    </div>
    <div class="setting-item ai-connection-test">
      <div class="ai-connection-test__row">
        <button type="button" class="btn-secondary" :disabled="connectionTesting" data-test="ai-connection-test" @click="testConnection">{{ connectionTesting ? '测试中…' : '测试连接' }}</button>
        <span
          v-if="connectionResult"
          :class="['ai-connection-test__result', connectionResult.ok ? 'ai-connection-test__result--ok' : 'ai-connection-test__result--error']"
          data-test="ai-connection-result"
          role="status"
        >{{ connectionResult.message }}<template v-if="connectionResult.ok && connectionResult.reply">，回复「{{ connectionResult.reply }}」</template></span>
      </div>
      <p class="help-text">用上面填写的地址、Key 与模型发一条极短的文本请求，只验证接口与模型可达（不验证图像输入）。打标失败率高时先据此排除接口问题，剩下的就是抽帧或扫描那一侧。留空字段按已保存或环境变量配置。</p>
    </div>
    <div class="setting-grid">
      <div class="setting-item">
        <label>单次请求图片上限</label>
        <input
          type="number"
          v-model.number="form.ai_tagging_images_per_request"
          min="1"
          max="100"
          step="1"
          class="number-input"
        />
		  <p class="help-text">视频按每分钟一帧、至少 10 帧抽取；超过此数量时拆成多个模型请求。</p>
      </div>
      <div class="setting-item">
        <label>字幕字符上限</label>
        <input
          type="number"
          v-model.number="form.ai_tagging_subtitle_char_limit"
          min="200"
          max="12000"
          step="100"
          class="number-input"
        />
      </div>
      <div class="setting-item">
        <label>后台批量数量</label>
        <input
          type="number"
          v-model.number="form.ai_tagging_startup_batch_size"
          min="1"
          max="100"
          step="1"
          class="number-input"
        />
      </div>
      <div class="setting-item">
        <label>Agent 额外补帧上限</label>
        <input
          type="number"
          v-model.number="form.ai_tagging_max_extra_frames"
          min="1"
          max="100"
          step="1"
          class="number-input"
        />
        <p class="help-text">模型决定每次补多少帧，服务端按此值限制单个视频的额外总量，默认 20。</p>
      </div>
    </div>
    <p class="help-text">AI Agent 可按证据决定补帧、临时生成字幕或查找同源视频。临时字幕只在内存中使用，不写入 SRT；只调用已经准备好的本地 WhisperX/Qwen，不会自动安装组件。</p>
    <p class="help-text">抽帧图片、临时字幕文本和同源候选帧可能发送到上方配置的外部 API；原始音频不会发送。保存后后台会自动使用新配置。</p>
    <div class="setting-item semantic-index-controls">
      <label>语义索引</label>
      <input type="text" v-model.trim="form.semantic_embedding_model" class="text-input" placeholder="例如 text-embedding-3-small；留空复用上方模型" />
      <p class="help-text">复用上方 OpenAI 兼容接口的 <code>/embeddings</code> 能力。索引仅在显式启动后构建；模型或向量维度变化时必须显式重建。</p>
      <div class="backup-status" :class="{ 'backup-status--error': semanticIndexStatus && !semanticIndexStatus.available }">
        <strong>{{ semanticIndexStatusText }}</strong>
        <span v-if="semanticIndexStatus?.model">模型 {{ semanticIndexStatus.model }}<template v-if="semanticIndexStatus.dimension"> · {{ semanticIndexStatus.dimension }} 维</template></span>
        <span v-if="semanticIndexStatus?.running || semanticIndexStatus?.completed">进度 {{ semanticIndexStatus.processed || 0 }}/{{ semanticIndexStatus.total || 0 }} · 成功 {{ semanticIndexStatus.succeeded || 0 }} · 跳过 {{ semanticIndexStatus.skipped || 0 }} · 失败 {{ semanticIndexStatus.failed || 0 }}</span>
        <span v-if="semanticIndexStatus?.unavailable">{{ semanticIndexStatus.unavailable }}</span>
      </div>
      <div class="backup-actions">
        <button type="button" class="btn-primary" :disabled="semanticIndexStatus?.running || !semanticIndexStatus?.available" @click="startSemanticIndex(false)">开始/继续构建</button>
        <button type="button" class="btn-secondary" :disabled="semanticIndexStatus?.running || !semanticIndexStatus?.available" @click="startSemanticIndex(true)">重建索引</button>
        <button v-if="semanticIndexStatus?.running" type="button" class="btn-secondary" @click="cancelSemanticIndex">取消</button>
      </div>
    </div>
    <PhotoAITaskPanel />
  </div>
</template>

<script>
import { GetSemanticIndexStatus, StartSemanticIndex, CancelSemanticIndex, TestAITaggingConnection } from '../../../wailsjs/go/main/App';
import PhotoAITaskPanel from '../PhotoAITaskPanel.vue';
import { confirmAction, notifyError } from '../../utils/feedback.js';

// AI 标签分区：接口配置、抽帧与字幕预算，以及视频语义索引的启动/取消。
// 图片侧的三个任务面板由 PhotoAITaskPanel 自己管。
export default {
  name: 'AITagSection',
  components: { PhotoAITaskPanel },
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return {
      semanticIndexStatus: null,
      semanticIndexStatusOff: null,
      connectionTesting: false,
      connectionResult: null
    };
  },
  mounted() {
    this.loadSemanticIndexStatus();
    if (window.runtime?.EventsOn) {
      const semanticOff = window.runtime.EventsOn('semantic-index-state', (status) => {
        this.semanticIndexStatus = { ...(this.semanticIndexStatus || {}), ...(status || {}) };
      });
      if (typeof semanticOff === 'function') this.semanticIndexStatusOff = semanticOff;
    }
  },
  beforeUnmount() {
    this.semanticIndexStatusOff?.();
  },
  computed: {
    semanticIndexStatusText() {
      const status = this.semanticIndexStatus;
      if (!status) return '正在读取语义索引状态...';
      if (!status.available) return '语义索引不可用';
      if (status.running) return '语义索引构建中';
      if (status.needs_rebuild) return '模型或维度已变化，需要重建';
      if (status.cancelled) return '语义索引构建已取消，可继续';
      if (status.completed) return '语义索引构建完成';
      return '语义索引可用，尚未构建';
    }
  },
  methods: {
    // 用表单当前值测，不要求先保存；返回值只回显地址与模型，不含 Key。
    async testConnection() {
      this.connectionTesting = true;
      this.connectionResult = null;
      try {
        this.connectionResult = await TestAITaggingConnection({
          base_url: this.form.ai_tagging_base_url || '',
          api_key: this.form.ai_tagging_api_key || '',
          model: this.form.ai_tagging_model || ''
        });
      } catch (err) {
        this.connectionResult = { ok: false, message: '测试失败：' + err };
      } finally {
        this.connectionTesting = false;
      }
    },
    async loadSemanticIndexStatus() {
      try {
        this.semanticIndexStatus = await GetSemanticIndexStatus();
      } catch (err) {
        this.semanticIndexStatus = { available: false, unavailable: String(err) };
      }
    },
    async startSemanticIndex(rebuild) {
      if (rebuild && !await confirmAction({ title: '重建语义索引', message: '重建会为当前模型重新请求全部视频的 embedding。继续吗？', confirmText: '重建' })) return;
      try {
        this.semanticIndexStatus = { ...(this.semanticIndexStatus || {}), ...(await StartSemanticIndex({ rebuild })) };
      } catch (err) {
        notifyError((rebuild ? '重建' : '启动') + '语义索引失败：' + err);
        await this.loadSemanticIndexStatus();
      }
    },
    async cancelSemanticIndex() {
      try {
        await CancelSemanticIndex();
        await this.loadSemanticIndexStatus();
      } catch (err) {
        notifyError('取消语义索引失败：' + err);
      }
    },
  }
};
</script>

<style scoped>
.ai-connection-test__row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
}

.ai-connection-test__result {
  min-width: 0;
  font-size: 13px;
  overflow-wrap: anywhere;
}

.ai-connection-test__result--ok { color: var(--success-color); }
.ai-connection-test__result--error { color: var(--danger-color); }
</style>
