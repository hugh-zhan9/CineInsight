<template>
  <!-- 后台继续翻译（D-PC22、MEDIA-12）：弹窗收起后在片库页留一条进度，随时可以回看或取消。 -->
  <div v-if="background && video" class="subtitle-translate-bar glass-surface" role="status" data-test="subtitle-translate-bar">
    <span class="subtitle-translate-bar__text">正在后台翻译「{{ videoLabel }}」的字幕 · {{ dialog.percent }}%</span>
    <button type="button" class="btn-secondary btn-compact" data-test="subtitle-translate-show" @click="showProgress">查看进度</button>
    <button type="button" class="btn-danger btn-compact" :disabled="cancelling" data-test="subtitle-translate-bar-cancel" @click="cancelTranslate">
      {{ cancelling ? '正在取消…' : '取消翻译' }}
    </button>
  </div>

  <BaseModal v-if="dialog.show" class="subtitle-translate-modal" data-test="subtitle-translate-dialog" @close="handleClose">
    <h3>{{ dialog.title }}</h3>
    <p class="modal-lead" data-test="subtitle-translate-msg">{{ dialog.msg }}</p>

    <div v-if="dialog.mode === 'confirm'" class="translate-form">
      <label class="dialog-field-label">翻译为</label>
      <select v-model="targetLang" class="search-input dialog-select">
        <option v-for="option in targetLangOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
      </select>

      <label class="dialog-field-label dialog-field-label--spaced">字幕当前语言</label>
      <select v-model="sourceLang" class="search-input dialog-select">
        <option v-for="option in sourceLangOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
      </select>
      <p class="dialog-field-hint">保持「自动识别」即可；指定原语言可以让翻译更稳。</p>

      <label class="dialog-field-label dialog-field-label--spaced">输出形态</label>
      <label v-for="option in modeOptions" :key="option.value" class="mode-option">
        <input type="radio" :value="option.value" v-model="mode" />
        <span class="mode-option-text">
          <strong>{{ option.label }}</strong>
          <em>{{ option.hint }}</em>
        </span>
      </label>

      <p v-if="sameLanguageSelected" class="dialog-warning">原语言和目标语言相同，不需要翻译。</p>
      <p v-if="existingTranslationNotice" class="dialog-warning" data-test="subtitle-translate-existing-notice">{{ existingTranslationNotice }}</p>
      <p class="dialog-note">翻译结果会覆盖这个视频当前的 .srt 文件（会先备份，可在行菜单「恢复上一版字幕」找回），请确认它现在还是原文字幕。</p>
    </div>

    <!-- 非 UTF-8 字幕（D-PC14、MEDIA-02）：先转换为 UTF-8（会先备份），再翻译；编码有歧义时按预览选。 -->
    <div v-if="dialog.mode === 'encoding'" class="translate-form" data-test="subtitle-translate-encoding">
      <template v-if="encoding.candidates.length > 1">
        <p class="dialog-field-hint">请对照预览选择显示正常的一项：</p>
        <label v-for="candidate in encoding.candidates" :key="candidate.encoding" class="mode-option">
          <input type="radio" :value="candidate.encoding" v-model="encoding.selected" :data-test="`subtitle-translate-encoding-${candidate.encoding}`" />
          <span class="mode-option-text">
            <strong>{{ encodingLabel(candidate.encoding) }}</strong>
            <em class="encoding-preview">{{ candidate.preview }}</em>
          </span>
        </label>
      </template>
      <p v-if="encoding.error" class="dialog-warning" data-test="subtitle-translate-encoding-error">{{ encoding.error }}</p>
    </div>

    <template v-if="dialog.mode === 'progress'">
      <div class="progress-bar-container">
        <div class="progress-bar" :style="{ width: dialog.percent + '%' }"></div>
      </div>
      <p class="progress-text">{{ dialog.percent }}%</p>
      <p class="progress-hint">字幕按批送去翻译，条数多时需要几分钟。可以「后台继续」去做别的事，完成后会提示。</p>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" data-test="subtitle-translate-background" @click="runInBackground">后台继续</button>
        <button type="button" class="btn-danger" data-test="subtitle-translate-cancel" :disabled="cancelling" @click="cancelTranslate">
          {{ cancelling ? '正在取消…' : '取消翻译' }}
        </button>
      </div>
    </template>

    <div v-if="dialog.mode === 'confirm'" class="modal-actions">
      <button type="button" class="btn-secondary" @click="close">取消</button>
      <button type="button" class="btn-primary" data-test="subtitle-translate-start" :disabled="sameLanguageSelected" @click="startTranslate">开始翻译</button>
    </div>
    <div v-if="dialog.mode === 'encoding'" class="modal-actions">
      <button type="button" class="btn-secondary" @click="close">取消</button>
      <button
        type="button"
        class="btn-primary"
        data-test="subtitle-translate-convert"
        :disabled="encoding.converting || encoding.unknown"
        @click="convertEncoding"
      >{{ encoding.converting ? '正在转换…' : '转换为 UTF-8' }}</button>
    </div>
    <div v-if="dialog.mode === 'result'" class="modal-actions">
      <button type="button" class="btn-primary" @click="close">确定</button>
    </div>
  </BaseModal>
</template>

<script>
import { TranslateSubtitle, CancelSubtitleTranslation, ConvertSubtitleToUTF8, GetSubtitleSegments } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import { notify, notifyError } from '../../utils/feedback.js';
import { SUBTITLE_BACKUP_NOTE, subtitleEncodingLabel, subtitleEncodingPrompt, subtitleErrorText, subtitleExceptionText } from '../../utils/subtitleTools.js';
import { runtimeEventsMixin } from './runtimeEvents.js';

const ENCODING_NOT_UTF8 = 'subtitle_encoding_not_utf8';
const ENCODING_AMBIGUOUS = 'subtitle_encoding_ambiguous';

function emptyEncoding() {
  return { detected: '', candidates: [], selected: '', converting: false, error: '', unknown: false };
}

// 对已有外挂字幕单独发起翻译。生成流程里的自动双语走设置页那套全局配置，
// 这里的目标语言与输出形态按次指定，产物同样覆盖同名 .srt（写入前先备份，D-PC13）。
export default {
  name: 'SubtitleTranslateDialog',
  components: { BaseModal },
  mixins: [runtimeEventsMixin],
  emits: ['translated', 'translating-change', 'translate-progress'],
  data() {
    return {
      dialog: { show: false, mode: 'confirm', title: '', msg: '', percent: 0 },
      video: null,
      // 翻译仍在跑、但弹窗已收起（「后台继续」）。
      background: false,
      cancelling: false,
      existingTranslationNotice: '',
      encoding: emptyEncoding(),
      targetLang: 'zh',
      sourceLang: 'auto',
      mode: 'bilingual',
      targetLangOptions: [
        { label: '中文', value: 'zh' },
        { label: '英语', value: 'en' },
        { label: '日语', value: 'ja' },
        { label: '韩语', value: 'ko' },
        { label: '法语', value: 'fr' },
        { label: '德语', value: 'de' },
        { label: '西班牙语', value: 'es' },
        { label: '葡萄牙语', value: 'pt' },
        { label: '俄语', value: 'ru' },
        { label: '意大利语', value: 'it' }
      ],
      modeOptions: [
        { label: '双语对照', value: 'bilingual', hint: '上行原文，下行译文' },
        { label: '仅译文', value: 'translated_only', hint: '原文会被译文替换掉' }
      ]
    };
  },
  computed: {
    sourceLangOptions() {
      return [{ label: '自动识别', value: 'auto' }, ...this.targetLangOptions];
    },
    sameLanguageSelected() {
      return this.sourceLang !== 'auto' && this.sourceLang === this.targetLang;
    },
    translating() {
      return this.dialog.mode === 'progress' && !!this.video;
    },
    videoLabel() {
      return this.video?.display_title || this.video?.name || `视频 #${this.video?.id}`;
    }
  },
  mounted() {
    this.registerRuntimeEvent('subtitle-translate-progress', (data) => {
      if (!this.video || Number(data?.videoID || 0) !== this.video.id) return;
      if (this.dialog.mode !== 'progress') return;
      this.dialog.percent = Number(data.percent || 0);
      this.dialog.msg = data.message || this.dialog.msg;
      this.$emit('translate-progress', { videoId: this.video.id, percent: this.dialog.percent });
    });
  },
  methods: {
    encodingLabel(encoding) {
      return subtitleEncodingLabel(encoding);
    },
    async translate(video) {
      if (!video) return;
      // 一次只跑一个：翻译进行中再从别的行点进来，会把正在跑的那次结果张冠李戴。
      if (this.translating) {
        if (this.background) this.showProgress();
        return;
      }
      this.video = video;
      this.background = false;
      this.cancelling = false;
      this.existingTranslationNotice = '';
      this.encoding = emptyEncoding();
      this.dialog = {
        show: true,
        mode: 'confirm',
        title: '翻译字幕',
        msg: `为《${video.name || `视频 #${video.id}`}》已有的字幕生成译文。`,
        percent: 0
      };
      await this.detectExistingTranslation(video);
    },
    async detectExistingTranslation(video) {
      // 译文覆盖原文件，对已经翻译过的字幕再翻一次就是在译文上叠加。
      // 多行条目占多数是「已经是双语」最可靠的现成信号，读不到就不提示。
      try {
        const segments = await GetSubtitleSegments(video.id);
        if (!Array.isArray(segments) || segments.length === 0) return;
        if (this.video?.id !== video.id) return;
        const multiline = segments.filter(segment => (segment?.lines?.length || 0) > 1).length;
        if (multiline * 2 >= segments.length) {
          this.existingTranslationNotice = '当前字幕多数条目已经是多行，看起来已经包含译文；再翻译一次会在现有内容上继续叠加。';
        }
      } catch (err) {
        // 读不到字幕不在这里下判断，让后端在翻译时给出准确原因。
      }
    },
    // 弹窗的关闭（Esc）：翻译进行中等同「后台继续」，不取消也不丢结果。
    handleClose() {
      if (this.dialog.mode === 'progress') {
        this.runInBackground();
        return;
      }
      this.close();
    },
    close() {
      this.dialog.show = false;
      this.video = null;
      this.background = false;
      this.cancelling = false;
      this.existingTranslationNotice = '';
      this.encoding = emptyEncoding();
    },
    runInBackground() {
      if (this.dialog.mode !== 'progress') return;
      this.background = true;
      this.dialog.show = false;
    },
    showProgress() {
      if (!this.translating) return;
      this.background = false;
      this.dialog.show = true;
    },
    async cancelTranslate() {
      if (!this.video || this.cancelling) return;
      this.cancelling = true;
      try {
        await CancelSubtitleTranslation(this.video.id);
      } catch (err) {
        this.cancelling = false;
      }
    },
    showEncodingPrompt(result) {
      const detected = String(result?.detected_encoding || '').toLowerCase();
      const candidates = Array.isArray(result?.candidates) ? result.candidates.filter(item => item?.encoding) : [];
      this.encoding = {
        detected,
        candidates,
        selected: detected || candidates[0]?.encoding || '',
        converting: false,
        error: '',
        unknown: !detected || detected === 'unknown'
      };
      this.dialog.mode = 'encoding';
      this.dialog.title = '字幕不是 UTF-8 编码';
      this.dialog.msg = candidates.length > 1
        ? `${subtitleErrorText(ENCODING_AMBIGUOUS, '')}转换为 UTF-8 时会先备份原文件。`
        : subtitleEncodingPrompt(detected);
    },
    async convertEncoding() {
      const video = this.video;
      if (!video || this.encoding.converting || this.encoding.unknown) return;
      this.encoding = { ...this.encoding, converting: true, error: '' };
      try {
        const chosen = this.encoding.candidates.length > 1 ? this.encoding.selected : this.encoding.detected;
        const result = await ConvertSubtitleToUTF8(video.id, chosen || '');
        if (this.video?.id !== video.id) return;
        if (result?.error_code === ENCODING_AMBIGUOUS) {
          this.showEncodingPrompt(result);
          return;
        }
        if (result?.error_code) {
          this.encoding = { ...this.encoding, converting: false, error: subtitleErrorText(result.error_code, result.message) };
          return;
        }
        this.encoding = emptyEncoding();
        this.dialog.mode = 'confirm';
        this.dialog.title = '翻译字幕';
        this.dialog.msg = '已转换为 UTF-8（原文件已备份），现在可以开始翻译。';
      } catch (err) {
        this.encoding = { ...this.encoding, converting: false, error: '转换失败：' + subtitleExceptionText(err) };
      }
    },
    // 收尾：弹窗开着就显示结果；已「后台继续」就改用提示条告知（MEDIA-12）。
    finish({ icon = '', title, msg, error = false }) {
      if (this.background) {
        const text = `${title}：「${this.videoLabel}」。${msg}`;
        if (error) notifyError(text);
        else notify(text);
        this.close();
        return;
      }
      this.dialog.mode = 'result';
      this.dialog.title = icon ? `${icon} ${title}` : title;
      this.dialog.msg = msg;
    },
    async startTranslate() {
      const video = this.video;
      if (!video || this.sameLanguageSelected) return;
      this.cancelling = false;
      this.$emit('translating-change', video.id);
      this.dialog.mode = 'progress';
      this.dialog.title = '正在翻译字幕';
      this.dialog.msg = '准备翻译...';
      this.dialog.percent = 0;
      try {
        const result = await TranslateSubtitle({
          video_id: video.id,
          source_lang: this.sourceLang,
          target_lang: this.targetLang,
          mode: this.mode
        });
        this.cancelling = false;
        this.$emit('translating-change', null);
        // 带错误码的失败（D-PC14、D-PC17）：非 UTF-8 给转换出口，其余（缺字幕、只有内嵌字幕）说清原因。
        if (result?.error_code === ENCODING_NOT_UTF8 || result?.error_code === ENCODING_AMBIGUOUS) {
          if (this.background) {
            this.finish({ title: '字幕翻译没有开始', msg: subtitleEncodingPrompt(result.detected_encoding), error: true });
            return;
          }
          this.showEncodingPrompt(result);
          return;
        }
        if (result?.error_code) {
          this.finish({ icon: '❌', title: '无法翻译这个字幕', msg: subtitleErrorText(result.error_code, result.message), error: true });
          return;
        }
        const warnings = Array.isArray(result?.warnings) && result.warnings.length > 0
          ? `\n\n注意：\n${result.warnings.join('\n')}`
          : '';
        const backup = result?.backup_id ? `\n${SUBTITLE_BACKUP_NOTE}` : '';
        this.finish({ icon: '✅', title: '字幕翻译完成', msg: `已翻译 ${result?.entries || 0} 条字幕，写回同名字幕文件。${backup}${warnings}` });
        this.$emit('translated', { video, result });
      } catch (err) {
        const cancelled = this.cancelling || String(err).includes('已取消');
        this.cancelling = false;
        this.$emit('translating-change', null);
        if (cancelled) this.finish({ icon: '⏹️', title: '已取消翻译', msg: '原字幕没有改动。' });
        else this.finish({ icon: '❌', title: '字幕翻译失败', msg: subtitleExceptionText(err), error: true });
      }
    }
  }
};
</script>

<style scoped>
:deep(.subtitle-translate-modal) {
  width: 400px;
  padding: 30px;
  text-align: center;
}
.subtitle-translate-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin: 12px 0;
  padding: 10px 16px;
  font-size: 13px;
}
.subtitle-translate-bar__text {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.modal-lead {
  margin: 0;
  color: var(--text-secondary);
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-line;
}
.translate-form {
  margin-top: 16px;
}
.dialog-field-label {
  display: block;
  margin-bottom: 8px;
  text-align: left;
  color: var(--text-secondary);
  font-size: 13px;
}
.dialog-field-label--spaced {
  margin-top: 12px;
}
.dialog-select {
  height: var(--h-unit);
  padding: 0 10px;
  text-align: left;
}
.dialog-field-hint {
  margin-top: 5px;
  text-align: left;
  color: var(--text-muted);
  font-size: 11px;
}
.mode-option {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-bottom: 6px;
  text-align: left;
  cursor: pointer;
}
.mode-option-text {
  display: flex;
  flex-direction: column;
  font-size: 13px;
}
.mode-option-text em {
  color: var(--text-muted);
  font-size: 11px;
  font-style: normal;
}
.mode-option-text .encoding-preview {
  white-space: pre-line;
}
.dialog-warning {
  margin-top: 12px;
  text-align: left;
  color: var(--danger, #d9534f);
  font-size: 12px;
}
.dialog-note {
  margin-top: 12px;
  text-align: left;
  color: var(--text-muted);
  font-size: 11px;
  line-height: 1.6;
}
.progress-bar-container {
  width: 100%;
  height: 10px;
  margin: 20px 0;
  border-radius: 5px;
  background-color: var(--review-progress-track-bg);
  overflow: hidden;
}
.progress-bar {
  height: 100%;
  background-color: var(--success-bright);
  transition: width 0.3s ease;
}
.progress-text {
  margin: 0;
  color: var(--review-text-muted);
  font-size: 0.9em;
}
.progress-hint {
  margin: 8px 0 0;
  color: var(--review-text-muted);
  font-size: 12px;
  line-height: 1.6;
}
.btn-compact {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
}
</style>
