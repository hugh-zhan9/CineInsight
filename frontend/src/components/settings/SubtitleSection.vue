<template>
  <!-- 字幕设置 -->
  <div :id="`settings-subtitle-translate`" class="settings-section">
    <h3>字幕翻译</h3>
    <p class="help-text">使用苹果系统翻译，无需 API Key。缺少语言资源时，由 macOS 提示下载；已安装资源由系统共享。</p>
    <div class="setting-item">
      <label>准备翻译语言</label>
      <select v-model="prepareSource" class="select-input" data-test="apple-translation-source"><option v-for="language in translationLanguages" :key="language.code" :value="language.code">{{ language.name }}</option></select>
      <span> → </span>
      <select v-model="prepareTarget" class="select-input" data-test="apple-translation-target"><option v-for="language in translationLanguages" :key="language.code" :value="language.code">{{ language.name }}</option></select>
      <button type="button" class="btn-secondary btn-compact" data-test="apple-translation-prepare" :disabled="preparing || !prepareSource || !prepareTarget || prepareSource === prepareTarget" @click="prepareLanguage(true)">准备系统翻译语言</button>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input type="checkbox" v-model="form.bilingual_enabled" />
        <span class="slider"></span>
        <span>生成字幕后自动翻译为双语</span>
      </label>
      <p class="help-text">只影响新生成的字幕。已有字幕随时可以在片库的行菜单里选「翻译字幕」。</p>
    </div>
    <div v-if="form.bilingual_enabled" class="setting-item">
      <label>自动双语的目标语言</label>
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

  </div>

  <div :id="`settings-subtitle-quality`" class="settings-section">
    <h3>字幕识别质量</h3>
    <!-- 识别引擎的状态与准备入口（D-PC22 / MEDIA-13）：状态后端缓存 60 秒，准备前后失效；
         准备可取消，取消不是失败。 -->
    <div class="setting-item subtitle-engines" data-test="subtitle-engine-status">
      <label>识别引擎</label>
      <p v-if="engineError" class="help-text settings-error" data-test="subtitle-engine-error">{{ engineError }}</p>
      <p v-else-if="!engineStatuses" class="help-text">正在读取引擎状态…</p>
      <div v-else class="subtitle-engine-list">
        <div
          v-for="engine in engineStatuses"
          :key="engine.engine"
          class="subtitle-engine-row"
          :data-test="`subtitle-engine-${engine.engine}`"
        >
          <div class="subtitle-engine-main">
            <strong>{{ engine.display_name || engine.engine }}</strong>
            <span data-test="subtitle-engine-state">{{ engineStateText(engine) }}</span>
          </div>
          <button
            v-if="engine.supported && engine.needs_prepare"
            type="button"
            class="btn-secondary btn-compact"
            :disabled="preparing"
            :data-test="`subtitle-engine-prepare-${engine.engine}`"
            @click="prepareEngine(engine)"
          >准备引擎</button>
        </div>
      </div>
      <div v-if="preparing" class="subtitle-engine-progress" data-test="subtitle-engine-progress" role="status">
        <span>{{ prepareProgressText }}</span>
        <button type="button" class="btn-secondary btn-compact" data-test="subtitle-engine-cancel" @click="cancelPrepare">取消准备</button>
      </div>
      <p v-if="prepareMessage" class="help-text" :class="{ 'settings-error': prepareMessageIsError }" data-test="subtitle-engine-message">{{ prepareMessage }}</p>
      <button type="button" class="btn-secondary btn-compact" data-test="subtitle-engine-refresh" :disabled="preparing" @click="loadEngineStatuses">刷新状态</button>
    </div>
    <div class="setting-item">
      <label>系统识别语言</label>
      <select v-model="speechLanguage" class="select-input" data-test="apple-speech-language"><option v-for="language in speechLanguages" :key="language.code" :value="language.code">{{ language.name }}{{ language.installed ? '（已准备）' : '' }}</option></select>
      <button type="button" class="btn-secondary btn-compact" data-test="apple-speech-prepare" :disabled="preparing || !speechLanguage" @click="prepareLanguage(false)">准备系统识别语言</button>
      <p class="help-text">需要 macOS 15 或更新版本。macOS 15 使用较早的系统识别引擎，识别质量较低，识别语言需先在「系统设置 › 键盘 › 听写」中下载。自动检测只覆盖已安装的系统识别语种，结果可手动修改。首次准备可能需要下载苹果语言资源。</p>
    </div>
  </div>
</template>

<script>
import { CancelSubtitleEnginePreparation, GetSubtitleEngineStatuses, PrepareSubtitleEngine, PrepareAppleSubtitleLanguage } from '../../../wailsjs/go/main/App';

// 准备进度的阶段说法（subtitle-progress 事件 action=prepare 的 phase）。后端个别阶段的
// message 仍是英文（模型下载），界面统一按阶段说中文。
const subtitlePrepareError = err => `准备系统语言失败：${String(err?.message || err || "未知错误")}`;
const PREPARE_PHASE_LABELS = {
  checking: '正在检查运行环境',
  'preparing-runtime': '正在准备运行时',
  'downloading-model': '正在下载模型',
  cancelled: '已取消准备'
};

// 取消返回的是这句（services.ErrSubtitleEnginePreparationCancelled）：它不是失败。
const PREPARE_CANCELLED_TEXT = '已取消字幕引擎准备';

// 双语偏好沿用设置保存；系统语言资源准备独立执行。
export default {
  name: 'SubtitleSection',
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return {
      engineStatuses: null,
      speechLanguage: 'zh_CN',
      prepareSource: 'en',
      prepareTarget: 'zh',
      engineError: '',
      preparing: false,
      prepareProgress: null,
      prepareMessage: '',
      prepareMessageIsError: false,
      eventOffs: []
    };
  },
  computed: {
    speechLanguages() { return this.engineStatuses?.[0]?.languages || []; },
    translationLanguages() { return this.engineStatuses?.[0]?.translation_languages || []; },
    prepareProgressText() {
      const progress = this.prepareProgress;
      if (!progress) return '正在准备字幕引擎…';
      const phase = PREPARE_PHASE_LABELS[progress.phase] || '正在准备字幕引擎';
      const percent = Number(progress.percent);
      return Number.isFinite(percent) && percent > 0 ? `${phase}（${Math.min(100, Math.round(percent))}%）` : `${phase}…`;
    }
  },
  mounted() {
    this.loadEngineStatuses();
    if (window.runtime?.EventsOn) {
      const progressOff = window.runtime.EventsOn('subtitle-progress', progress => this.applyPrepareProgress(progress));
      const completeOff = window.runtime.EventsOn('subtitle-prepare-complete', () => this.handlePrepareComplete());
      this.eventOffs = [progressOff, completeOff].filter(off => typeof off === 'function');
    }
  },
  beforeUnmount() {
    for (const off of this.eventOffs) off();
    this.eventOffs = [];
  },
  methods: {
    async prepareLanguage(translation) {
      if (this.preparing) return;
      this.preparing = true; this.prepareMessage = ''; this.prepareMessageIsError = false;
      try {
        await PrepareAppleSubtitleLanguage(translation ? this.prepareSource : this.speechLanguage, translation ? this.prepareTarget : '', translation);
        this.prepareMessage = '系统语言资源已准备好。';
      } catch (err) {
        this.prepareMessage = subtitlePrepareError(err); this.prepareMessageIsError = true;
      } finally { this.preparing = false; await this.loadEngineStatuses(); }
    },
    async loadEngineStatuses() {
      try {
        this.engineStatuses = (await GetSubtitleEngineStatuses()) || [];
        this.engineError = '';
      } catch (err) {
        this.engineStatuses = null;
        this.engineError = `读取字幕引擎状态失败：${err}`;
      }
    },
    engineStateText(engine) {
      if (!engine.supported) return engine.reason_message || '这台电脑上不支持';
      if (engine.available && !engine.needs_prepare) return '已就绪';
      if (engine.needs_prepare) return engine.prepare_hint || engine.reason_message || '需要先准备才能使用';
      return engine.reason_message || '暂不可用';
    },
    // 准备可能在别处发起（生成字幕弹窗），这里只要看到进度事件就显示进度与取消入口。
    applyPrepareProgress(progress) {
      if (!progress || progress.action !== 'prepare') return;
      if (progress.phase === 'cancelled') {
        this.preparing = false;
        this.prepareProgress = null;
        this.prepareMessage = '已取消准备，可以随时重新开始。';
        this.prepareMessageIsError = false;
        return;
      }
      this.preparing = true;
      this.prepareProgress = progress;
    },
    handlePrepareComplete() {
      this.preparing = false;
      this.prepareProgress = null;
      this.loadEngineStatuses();
    },
    async prepareEngine(engine) {
      if (this.preparing) return;
      this.preparing = true;
      this.prepareProgress = null;
      this.prepareMessage = '';
      this.prepareMessageIsError = false;
      try {
        await PrepareSubtitleEngine(engine.engine);
        this.prepareMessage = `${engine.display_name || engine.engine} 已准备好。`;
      } catch (err) {
        const text = String(err?.message || err || '');
        if (text.includes(PREPARE_CANCELLED_TEXT)) {
          this.prepareMessage = '已取消准备，可以随时重新开始。';
        } else {
          this.prepareMessage = `准备字幕引擎失败：${text}`;
          this.prepareMessageIsError = true;
        }
      } finally {
        this.preparing = false;
        this.prepareProgress = null;
        await this.loadEngineStatuses();
      }
    },
    async cancelPrepare() {
      try {
        await CancelSubtitleEnginePreparation();
      } catch (err) {
        this.prepareMessage = `取消准备失败：${err}`;
        this.prepareMessageIsError = true;
      }
    }
  }
};
</script>

<style scoped>
.subtitle-engine-list { display: grid; gap: 8px; margin-bottom: 10px; }
.subtitle-engine-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
}
.subtitle-engine-main { display: grid; gap: 2px; min-width: 0; }
.subtitle-engine-main span { color: var(--text-secondary); font-size: 12px; overflow-wrap: anywhere; }
.subtitle-engine-progress { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; color: var(--text-secondary); font-size: 12.5px; }
</style>
