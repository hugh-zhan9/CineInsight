<template>
  <!-- 字幕设置 -->
  <div :id="`settings-subtitle-translate`" class="settings-section">
    <h3>字幕翻译</h3>
    <div class="setting-item">
      <label class="switch">
        <input type="checkbox" v-model="form.bilingual_enabled" />
        <span class="slider"></span>
        <span>启用双语字幕翻译</span>
      </label>
    </div>
    <template v-if="form.bilingual_enabled">
      <div class="setting-item">
        <label>目标翻译语言</label>
        <select v-model="form.bilingual_lang" class="select-input">
          <option value="zh">中文</option>
          <option value="en">英语</option>
          <option value="ja">日语</option>
          <option value="ko">韩语</option>
          <option value="fr">法语</option>
          <option value="de">德语</option>
          <option value="es">西班牙语</option>
          <option value="pt">葡萄牙语</option>
          <option value="ru">俄语</option>
          <option value="it">意大利语</option>
        </select>
      </div>
      <div class="setting-item">
        <label>翻译服务</label>
        <select v-model="form.subtitle_translation_provider" class="select-input">
          <option value="deepl">DeepL</option>
          <option value="llm">OpenAI 兼容接口（本地 / 远程）</option>
        </select>
      </div>
      <div v-if="form.subtitle_translation_provider !== 'llm'" class="setting-item">
        <label>DeepL API Key</label>
        <input 
          type="password" 
          v-model="form.deepl_api_key" 
          placeholder="填入 DeepL API Key" 
          class="text-input"
          autocomplete="off"
        />
        <p class="help-text">免费版 Key 通常以 :fx 结尾。额度 50 万字符/月。</p>
      </div>
      <template v-else>
        <div class="setting-item">
          <label>字幕翻译接口地址</label>
          <input type="url" v-model.trim="form.subtitle_translation_base_url" class="text-input" placeholder="https://api.example.com/v1 或 http://127.0.0.1:1234/v1" />
        </div>
        <div class="setting-item">
          <label>字幕翻译 API Key</label>
          <input type="password" v-model="form.subtitle_translation_api_key" class="text-input" autocomplete="off" />
          <p class="help-text">本地服务可留空；远程服务按提供商要求填写。此配置不会复用 AI 标签接口。</p>
        </div>
        <div class="setting-item">
          <label>字幕翻译模型</label>
          <input type="text" v-model.trim="form.subtitle_translation_model" class="text-input" placeholder="gpt-4o-mini 或本地兼容模型" />
        </div>
      </template>
    </template>
    <div class="setting-item">
      <label>术语表（全局）</label>
      <p v-if="!isLLMProvider" class="help-text" data-test="glossary-deepl-notice">术语表不适用于 DeepL：DeepL 走它自己的翻译接口，术语只注入 OpenAI 兼容接口的提示词。</p>
      <GlossaryEditor :collection-id="0" hint="全局术语对所有视频生效；作品集详情里维护的术语会覆盖同名的全局条目。" />
    </div>
  </div>

  <div :id="`settings-subtitle-quality`" class="settings-section">
    <h3>字幕识别质量</h3>
    <div class="setting-item">
      <label>WhisperX 模型</label>
      <select v-model="form.subtitle_whisperx_model" class="select-input">
        <option value="tiny">tiny（最快）</option>
        <option value="base">base</option>
        <option value="small">small</option>
        <option value="medium">medium（推荐）</option>
        <option value="large-v2">large-v2</option>
        <option value="large-v3">large-v3（最准确）</option>
      </select>
    </div>
    <div class="setting-item">
      <label>WhisperX 批量大小</label>
      <input type="number" min="1" max="16" v-model.number="form.subtitle_whisperx_batch_size" class="number-input" />
      <p class="help-text">CPU 运行时建议使用 4-8；数值越大占用内存越多。</p>
    </div>
  </div>
</template>

<script>
import GlossaryEditor from '../GlossaryEditor.vue';

// 字幕翻译与字幕识别质量两个分区：除术语表外都是纯表单字段。
// 术语表自己读写库（D-033），不进 form，因此与设置页的保存流程无关。
export default {
  name: 'SubtitleSection',
  components: { GlossaryEditor },
  props: {
    form: { type: Object, required: true }
  },
  computed: {
    isLLMProvider() {
      return this.form.subtitle_translation_provider === 'llm';
    }
  }
};
</script>
