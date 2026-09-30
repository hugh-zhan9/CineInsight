<template>
  <div class="subtitle-workbench-overlay" @click.self="requestClose">
    <section class="subtitle-workbench" role="dialog" aria-modal="true" aria-label="字幕编辑工作台">
      <header class="subtitle-workbench__header">
        <div class="subtitle-workbench__title">
          <p class="subtitle-workbench__eyebrow">字幕编辑工作台</p>
          <h2 :title="video.name">{{ video.name }}</h2>
        </div>
        <div class="subtitle-workbench__header-actions">
          <span :class="['subtitle-workbench__dirty', { 'subtitle-workbench__dirty--active': isDirty }]">
            {{ isDirty ? '有未保存修改' : '已保存' }}
          </span>
          <button type="button" class="btn-secondary" :disabled="loading" @click="reloadDocument">重新加载</button>
          <button data-test="close-workbench" type="button" class="btn-secondary" @click="requestClose">关闭</button>
        </div>
      </header>

      <div v-if="loading" class="subtitle-workbench__state">正在读取外置 SRT...</div>
      <!-- 还没有字幕（D-PC15、MEDIA-06）：不是死胡同，可以新建空白字幕，保存时才创建文件。 -->
      <div v-else-if="loadErrorCode === 'subtitle_missing'" class="subtitle-workbench__state" data-test="workbench-missing">
        <p>{{ loadError }}</p>
        <p class="subtitle-workbench__state-hint">可以新建一份空白字幕手动编写，保存时才会创建同名 .srt 文件。</p>
        <button type="button" class="btn-primary" :disabled="creatingBlank" data-test="workbench-create-blank" @click="createBlankDocument">
          {{ creatingBlank ? '正在新建…' : '新建空白字幕' }}
        </button>
      </div>
      <!-- 非 UTF-8（D-PC14、MEDIA-02）：不直接编辑，先一键转换（会先备份）；编码有歧义时按预览选。 -->
      <div v-else-if="loadErrorCode === 'subtitle_encoding_not_utf8'" class="subtitle-workbench__state" data-test="workbench-encoding">
        <p>{{ loadError }}</p>
        <div v-if="encoding.candidates.length > 1" class="subtitle-workbench__encoding-options">
          <label v-for="candidate in encoding.candidates" :key="candidate.encoding" class="subtitle-workbench__encoding-option">
            <input v-model="encoding.selected" type="radio" :value="candidate.encoding" :data-test="`workbench-encoding-${candidate.encoding}`" />
            <span>
              <strong>{{ encodingLabel(candidate.encoding) }}</strong>
              <em>{{ candidate.preview }}</em>
            </span>
          </label>
        </div>
        <p v-if="encoding.error" class="subtitle-workbench__state--error" role="alert">{{ encoding.error }}</p>
        <button
          type="button"
          class="btn-primary"
          :disabled="encoding.converting || encoding.unknown"
          data-test="workbench-convert-utf8"
          @click="convertToUTF8"
        >{{ encoding.converting ? '正在转换…' : '转换为 UTF-8（会先备份）' }}</button>
      </div>
      <div v-else-if="loadError" class="subtitle-workbench__state subtitle-workbench__state--error" role="alert" data-test="workbench-load-error">
        <p>{{ loadError }}</p>
        <button v-if="loadErrorCode !== 'subtitle_not_sidecar_srt'" type="button" class="btn-primary" @click="loadWorkbench">重试</button>
      </div>

      <template v-else>
        <div class="subtitle-workbench__layout">
          <aside class="subtitle-workbench__preview">
            <div v-if="previewLoading" class="subtitle-workbench__state">正在准备视频预览...</div>
            <template v-else-if="previewSession?.mode === 'inline' && previewSession.inline_source">
              <video
                ref="videoElement"
                class="subtitle-workbench__video"
                controls
                playsinline
                preload="metadata"
                :muted="true"
                @loadedmetadata="configureVideo"
                @timeupdate="handleTimeUpdate"
              >
                <source :src="previewSession.inline_source.locator_value" :type="previewSession.inline_source.mime" />
              </video>
            </template>
            <div v-else class="subtitle-workbench__preview-fallback">
              <p>{{ previewSession?.reason_message || previewError || '当前视频无法内嵌预览。' }}</p>
              <button type="button" class="btn-secondary" @click="previewExternally">用系统播放器预览</button>
            </div>
            <div class="subtitle-workbench__playback-status">
              <span>播放位置 {{ formatTimestamp(currentTimeMs) }}</span>
              <label><input v-model="followPlayback" type="checkbox" /> 跟随播放</label>
            </div>

            <section class="subtitle-workbench__tool-section">
              <h3>时间与结构</h3>
              <div class="subtitle-workbench__button-grid">
                <button type="button" class="btn-secondary" @click="insertEntry">插入</button>
                <button type="button" class="btn-secondary" :disabled="selectedIDs.length === 0" @click="deleteSelected">删除</button>
                <button type="button" class="btn-secondary" :disabled="selectedIDs.length !== 1" @click="splitSelected">拆分</button>
                <button type="button" class="btn-secondary" :disabled="!canMergeSelection" @click="mergeSelected">合并</button>
              </div>
              <div class="subtitle-workbench__offset-row">
                <input v-model.number="offsetMs" type="number" step="100" aria-label="偏移毫秒" />
                <span>毫秒</span>
              </div>
              <div class="subtitle-workbench__button-grid">
                <button type="button" class="btn-secondary" :disabled="!validOffset" @click="applyOffset(false)">全局偏移</button>
                <button type="button" class="btn-secondary" :disabled="!validOffset || selectedIDs.length === 0" @click="applyOffset(true)">选区偏移</button>
              </div>
            </section>

            <section class="subtitle-workbench__tool-section">
              <h3>查找替换</h3>
              <input ref="findInput" v-model="findText" type="search" placeholder="查找文本" />
              <input v-model="replaceText" type="text" placeholder="替换为" />
              <label><input v-model="replaceSelectionOnly" type="checkbox" /> 只处理选区</label>
              <button type="button" class="btn-secondary" :disabled="replaceMatchCount === 0" @click="replaceMatches">
                替换 {{ replaceMatchCount }} 处
              </button>
            </section>

            <section class="subtitle-workbench__tool-section">
              <h3>选区重新翻译</h3>
              <div class="subtitle-workbench__language-row">
                <select v-model="sourceLang" aria-label="源语言">
                  <option value="">自动识别</option>
                  <option v-for="language in languageOptions" :key="`source-${language.value}`" :value="language.value">{{ language.label }}</option>
                </select>
                <span>→</span>
                <select v-model="targetLang" aria-label="目标语言">
                  <option v-for="language in languageOptions" :key="`target-${language.value}`" :value="language.value">{{ language.label }}</option>
                </select>
              </div>
              <!-- 两种重译范围（D-PC16、MEDIA-07）：选区多数是双语两行时默认只替换译文行，不破坏原文。 -->
              <div class="subtitle-workbench__mode-row" role="radiogroup" aria-label="重译范围">
                <label><input v-model="retranslateModeModel" type="radio" value="translation_line" data-test="retranslate-mode-translation_line" /> 只替换译文行</label>
                <label><input v-model="retranslateModeModel" type="radio" value="whole_entry" data-test="retranslate-mode-whole_entry" /> 整条替换</label>
              </div>
              <button type="button" class="btn-secondary" :disabled="translating || selectedIDs.length === 0" @click="retranslateSelection">
                {{ translating ? '翻译中...' : `翻译选中 ${selectedIDs.length} 条` }}
              </button>
            </section>

            <!-- 历史版本（D-PC13、MEDIA-05）：每次覆盖前自动备份，保留最近 5 份。 -->
            <section class="subtitle-workbench__tool-section" data-test="workbench-backups">
              <h3>历史版本</h3>
              <p v-if="!backups.length" class="subtitle-workbench__hint">还没有备份。生成、翻译或保存覆盖字幕时，会自动备份最近 5 份。</p>
              <template v-else>
                <select v-model="selectedBackupID" aria-label="选择要恢复的版本" data-test="workbench-backup-select">
                  <option v-for="backup in backups" :key="backup.id" :value="backup.id">{{ backupLabel(backup) }}</option>
                </select>
                <button type="button" class="btn-secondary" :disabled="restoringBackup || !selectedBackupID" data-test="workbench-restore-backup" @click="restoreSelectedBackup">
                  {{ restoringBackup ? '正在恢复…' : '恢复所选版本' }}
                </button>
              </template>
            </section>
          </aside>

          <main class="subtitle-workbench__editor">
            <div class="subtitle-workbench__editor-toolbar">
              <div class="subtitle-workbench__history-actions">
                <button data-test="undo" type="button" class="btn-secondary" :disabled="history.length === 0" @click="undo">撤销</button>
                <button data-test="redo" type="button" class="btn-secondary" :disabled="future.length === 0" @click="redo">重做</button>
                <button type="button" class="btn-secondary" @click="toggleAllSelection">{{ allSelected ? '取消全选' : '全选' }}</button>
              </div>
              <!-- 问题导航与一键修复（D-PC15、MEDIA-06）：零时长、重叠在加载时是「待修问题」，不是打不开。 -->
              <div v-if="issueEntryIndexes.length" class="subtitle-workbench__issue-actions" data-test="workbench-issues">
                <span class="subtitle-workbench__issue-count">{{ issueEntryIndexes.length }} 条有问题</span>
                <button type="button" class="btn-secondary" data-test="workbench-next-issue" @click="goToNextIssue">下一个问题</button>
                <button type="button" class="btn-secondary" :disabled="timingIssueCount === 0" data-test="workbench-fix-timing" @click="fixTimingIssues">一键修复时间</button>
              </div>
              <span>共 {{ entries.length }} 条 · 已选 {{ selectedIDs.length }} 条</span>
            </div>

            <div
              ref="entryScroller"
              class="subtitle-workbench__entries"
              @scroll="handleEntryScroll"
            >
              <div :style="{ height: `${topSpacerHeight}px` }"></div>
              <article
                v-for="item in visibleEntries"
                :key="item.entry.client_id"
                data-test="subtitle-entry"
                :class="['subtitle-workbench__entry', {
                  'subtitle-workbench__entry--active': item.index === activeEntryIndex,
                  'subtitle-workbench__entry--invalid': issueIDs.has(item.entry.client_id)
                }]"
                @dblclick="seekToEntry(item.entry)"
              >
                <div class="subtitle-workbench__entry-heading">
                  <label>
                    <input
                      type="checkbox"
                      :checked="selectedIDs.includes(item.entry.client_id)"
                      @change="toggleEntrySelection(item.entry.client_id, $event.target.checked)"
                    />
                    #{{ item.index + 1 }}
                  </label>
                  <button type="button" class="subtitle-workbench__seek" @click="seekToEntry(item.entry)">定位</button>
                </div>
                <div class="subtitle-workbench__timing">
                  <label>
                    开始
                    <input
                      type="text"
                      :value="formatTimestampInput(item.entry.start_time_ms)"
                      @change="updateEntryTime(item.index, 'start_time_ms', $event.target.value)"
                    />
                  </label>
                  <span>→</span>
                  <label>
                    结束
                    <input
                      type="text"
                      :value="formatTimestampInput(item.entry.end_time_ms)"
                      @change="updateEntryTime(item.index, 'end_time_ms', $event.target.value)"
                    />
                  </label>
                </div>
                <textarea
                  :data-test="`entry-text-${item.entry.client_id}`"
                  :value="item.entry.text"
                  rows="3"
                  @input="updateEntryText(item.index, $event.target.value)"
                ></textarea>
                <ul v-if="issuesByID[item.entry.client_id]?.length" class="subtitle-workbench__entry-errors">
                  <li v-for="issue in issuesByID[item.entry.client_id]" :key="issue.code">{{ issue.message }}</li>
                </ul>
              </article>
              <div :style="{ height: `${bottomSpacerHeight}px` }"></div>
            </div>
          </main>
        </div>

        <footer class="subtitle-workbench__footer">
          <div class="subtitle-workbench__messages">
            <p v-if="operationError" class="subtitle-workbench__message subtitle-workbench__message--error" role="alert">{{ operationError }}</p>
            <p v-else-if="operationMessage" class="subtitle-workbench__message" role="status">{{ operationMessage }}</p>
            <p v-else-if="entries.length === 0" class="subtitle-workbench__message" data-test="workbench-empty-hint">
              字幕还没有内容，点「插入」添加第一条。
            </p>
            <p v-else-if="validationIssues.length" class="subtitle-workbench__message subtitle-workbench__message--error">
              当前有 {{ validationIssues.length }} 个校验问题，修正后才能保存；可用「下一个问题」逐条查看。
            </p>
            <p v-else class="subtitle-workbench__message">序号会在保存时自动重排。快捷键：⌘/Ctrl+S 保存，⌘/Ctrl+Z 撤销。</p>
          </div>
          <button
            data-test="save-subtitle"
            type="button"
            class="btn-primary"
            :disabled="saving || !isDirty || validationIssues.length > 0"
            @click="saveDocument"
          >
            {{ saving ? '保存中...' : '保存字幕' }}
          </button>
        </footer>
      </template>
    </section>
  </div>
</template>

<script>
import {
  ConvertSubtitleToUTF8,
  CreateBlankSubtitleDocument,
  GetPreviewSession,
  GetSubtitleEditDocument,
  ListSubtitleBackups,
  PreviewExternally,
  RestoreSubtitleBackup,
  RetranslateSubtitleEntries,
  SaveSubtitleEditDocument
} from '../../wailsjs/go/main/App';
import { confirmAction } from '../utils/feedback.js';
import { formatBytes } from '../utils/mediaDetails.js';
import {
  defaultRetranslateMode, fixSubtitleTimingIssues, formatLocalTime, subtitleEncodingLabel, subtitleEncodingPrompt,
  subtitleErrorText, subtitleExceptionText
} from '../utils/subtitleTools.js';

// 加载失败里需要单独给出口的错误码（D-PC14、D-PC15、D-PC17）。
const DOCUMENT_ERROR_CODES = ['subtitle_missing', 'subtitle_not_sidecar_srt', 'subtitle_encoding_not_utf8'];
// 一键修复只管时间问题；负时间、空文本等要人来改。
const TIMING_ISSUE_CODES = ['invalid_time_range', 'overlap'];

function emptyEncoding() {
  return { detected: '', candidates: [], selected: '', converting: false, error: '', unknown: false };
}

const ENTRY_ROW_HEIGHT = 210;
const HISTORY_LIMIT = 100;

function cloneEntries(entries) {
  return (entries || []).map(entry => ({ ...entry }));
}

function entriesSignature(entries) {
  return JSON.stringify((entries || []).map(entry => [entry.client_id, entry.start_time_ms, entry.end_time_ms, entry.text]));
}

function newClientID(counter) {
  return `cue-local-${Date.now()}-${counter}`;
}

export default {
  name: 'SubtitleWorkbench',
  props: {
    video: { type: Object, required: true }
  },
  emits: ['close', 'saved'],
  data() {
    return {
      loading: true,
      loadError: '',
      loadErrorCode: '',
      creatingBlank: false,
      encoding: emptyEncoding(),
      backups: [],
      selectedBackupID: '',
      restoringBackup: false,
      // 用户手动选的重译范围；空表示按选区自动判断（D-PC16）。选区一变就回到自动。
      retranslateModeOverride: '',
      documentFingerprint: null,
      entries: [],
      baselineSignature: '',
      history: [],
      future: [],
      selectedIDs: [],
      localIDCounter: 0,
      currentTimeMs: 0,
      followPlayback: true,
      lastFollowedIndex: -1,
      previewLoading: true,
      previewSession: null,
      previewError: '',
      scrollTop: 0,
      viewportHeight: 800,
      offsetMs: 0,
      findText: '',
      replaceText: '',
      replaceSelectionOnly: false,
      sourceLang: '',
      targetLang: 'zh',
      translating: false,
      saving: false,
      operationError: '',
      operationMessage: '',
      languageOptions: [
        { value: 'zh', label: '中文' },
        { value: 'en', label: '英语' },
        { value: 'ja', label: '日语' },
        { value: 'ko', label: '韩语' },
        { value: 'fr', label: '法语' },
        { value: 'de', label: '德语' },
        { value: 'es', label: '西班牙语' }
      ]
    };
  },
  computed: {
    isDirty() {
      return entriesSignature(this.entries) !== this.baselineSignature;
    },
    activeEntryIndex() {
      const time = this.currentTimeMs;
      return this.entries.findIndex(entry => time >= Number(entry.start_time_ms) && time < Number(entry.end_time_ms));
    },
    visibleRange() {
      const overscan = 4;
      const start = Math.max(0, Math.floor(this.scrollTop / ENTRY_ROW_HEIGHT) - overscan);
      const count = Math.ceil(this.viewportHeight / ENTRY_ROW_HEIGHT) + overscan * 2;
      return { start, end: Math.min(this.entries.length, start + count) };
    },
    visibleEntries() {
      const result = [];
      for (let index = this.visibleRange.start; index < this.visibleRange.end; index += 1) {
        result.push({ index, entry: this.entries[index] });
      }
      return result;
    },
    topSpacerHeight() {
      return this.visibleRange.start * ENTRY_ROW_HEIGHT;
    },
    bottomSpacerHeight() {
      return Math.max(0, (this.entries.length - this.visibleRange.end) * ENTRY_ROW_HEIGHT);
    },
    selectionIndexes() {
      const selected = new Set(this.selectedIDs);
      return this.entries.map((entry, index) => selected.has(entry.client_id) ? index : -1).filter(index => index >= 0);
    },
    canMergeSelection() {
      if (this.selectionIndexes.length < 2) return false;
      return this.selectionIndexes.every((index, position) => position === 0 || index === this.selectionIndexes[position - 1] + 1);
    },
    allSelected() {
      return this.entries.length > 0 && this.selectedIDs.length === this.entries.length;
    },
    validOffset() {
      return Number.isFinite(Number(this.offsetMs)) && Number(this.offsetMs) !== 0;
    },
    replaceMatchCount() {
      if (!this.findText) return 0;
      const selected = new Set(this.selectedIDs);
      return this.entries.reduce((count, entry) => {
        if (this.replaceSelectionOnly && !selected.has(entry.client_id)) return count;
        return count + entry.text.split(this.findText).length - 1;
      }, 0);
    },
    validationIssues() {
      const issues = [];
      const seen = new Set();
      this.entries.forEach((entry, index) => {
        const id = String(entry.client_id || '').trim();
        if (!id) issues.push({ client_id: id, code: 'missing_client_id', message: '条目标识为空' });
        else if (seen.has(id)) issues.push({ client_id: id, code: 'duplicate_client_id', message: '条目标识重复' });
        else seen.add(id);
        const start = Number(entry.start_time_ms);
        const end = Number(entry.end_time_ms);
        if (!Number.isFinite(start) || !Number.isFinite(end) || start < 0 || end < 0) {
          issues.push({ client_id: id, code: 'negative_time', message: '时间必须是非负毫秒数' });
        } else if (end <= start) {
          issues.push({ client_id: id, code: 'invalid_time_range', message: '结束时间必须晚于开始时间' });
        }
        if (index > 0 && start < Number(this.entries[index - 1].end_time_ms)) {
          issues.push({ client_id: id, code: 'overlap', message: '与上一条字幕重叠' });
        }
        if (!String(entry.text || '').trim()) issues.push({ client_id: id, code: 'empty_text', message: '字幕文本不能为空' });
        if (String(entry.text || '').split(/\r?\n/).some(line => !line.trim())) {
          issues.push({ client_id: id, code: 'invalid_text', message: '字幕文本不能包含空白分隔行' });
        }
      });
      if (this.entries.length === 0) issues.push({ client_id: '', code: 'empty_document', message: '字幕至少需要一条内容' });
      return issues;
    },
    issuesByID() {
      return this.validationIssues.reduce((groups, issue) => {
        if (!issue.client_id) return groups;
        if (!groups[issue.client_id]) groups[issue.client_id] = [];
        groups[issue.client_id].push(issue);
        return groups;
      }, {});
    },
    issueIDs() {
      return new Set(this.validationIssues.map(issue => issue.client_id).filter(Boolean));
    },
    issueEntryIndexes() {
      const ids = this.issueIDs;
      return this.entries.map((entry, index) => (ids.has(entry.client_id) ? index : -1)).filter(index => index >= 0);
    },
    timingIssueCount() {
      return this.validationIssues.filter(issue => TIMING_ISSUE_CODES.includes(issue.code)).length;
    },
    defaultRetranslateMode() {
      const selected = new Set(this.selectedIDs);
      return defaultRetranslateMode(this.entries.filter(entry => selected.has(entry.client_id)));
    },
    retranslateModeModel: {
      get() {
        return this.retranslateModeOverride || this.defaultRetranslateMode;
      },
      set(mode) {
        this.retranslateModeOverride = mode;
      }
    }
  },
  watch: {
    activeEntryIndex(next) {
      if (!this.followPlayback || next < 0 || next === this.lastFollowedIndex) return;
      this.lastFollowedIndex = next;
      this.scrollEntryIntoView(next);
    },
    selectedIDs() {
      this.retranslateModeOverride = '';
    }
  },
  mounted() {
    window.addEventListener('beforeunload', this.handleBeforeUnload);
    window.addEventListener('keydown', this.handleShortcut);
    this.loadWorkbench();
  },
  beforeUnmount() {
    window.removeEventListener('beforeunload', this.handleBeforeUnload);
    window.removeEventListener('keydown', this.handleShortcut);
  },
  methods: {
    async loadWorkbench() {
      this.loading = true;
      this.loadError = '';
      this.loadErrorCode = '';
      this.encoding = emptyEncoding();
      this.operationError = '';
      this.operationMessage = '';
      this.previewLoading = true;
      const documentPromise = GetSubtitleEditDocument(this.video.id);
      const previewPromise = GetPreviewSession(this.video.id);
      try {
        const document = await documentPromise;
        if (document?.error_code) {
          this.applyDocumentError(document);
        } else {
          this.applyDocument(document);
          const issueCount = Array.isArray(document?.issues) ? document.issues.length : 0;
          if (issueCount > 0) {
            this.operationMessage = `打开时发现 ${issueCount} 处时间问题（零时长或与上一条重叠），可用「一键修复时间」处理，修好后才能保存。`;
          }
        }
      } catch (error) {
        this.loadError = `无法打开字幕工作台：${subtitleExceptionText(error)}`;
      } finally {
        this.loading = false;
      }
      this.loadBackups();
      try {
        this.previewSession = await previewPromise;
      } catch (error) {
        this.previewError = subtitleExceptionText(error);
      } finally {
        this.previewLoading = false;
      }
      this.$nextTick(this.measureViewport);
    },
    applyDocument(document) {
      this.documentFingerprint = { ...(document?.fingerprint || {}) };
      this.entries = cloneEntries(document?.entries);
      this.baselineSignature = entriesSignature(this.entries);
      this.history = [];
      this.future = [];
      this.selectedIDs = [];
    },
    applyDocumentError(document) {
      const code = DOCUMENT_ERROR_CODES.includes(document.error_code) ? document.error_code : '';
      this.loadErrorCode = code || document.error_code;
      if (code === 'subtitle_encoding_not_utf8') {
        const detected = String(document.detected_encoding || '').toLowerCase();
        const candidates = Array.isArray(document.candidates) ? document.candidates.filter(item => item?.encoding) : [];
        this.encoding = {
          ...emptyEncoding(),
          detected,
          candidates,
          selected: detected || candidates[0]?.encoding || '',
          unknown: !detected || detected === 'unknown'
        };
        this.loadError = candidates.length > 1
          ? `${subtitleErrorText('subtitle_encoding_ambiguous', '')}转换时会先备份原文件。`
          : subtitleEncodingPrompt(detected);
        return;
      }
      this.loadError = subtitleErrorText(document.error_code, document.message);
    },
    encodingLabel(encoding) {
      return subtitleEncodingLabel(encoding);
    },
    async createBlankDocument() {
      if (this.creatingBlank) return;
      this.creatingBlank = true;
      try {
        const document = await CreateBlankSubtitleDocument(this.video.id);
        this.applyDocument(document);
        this.loadError = '';
        this.loadErrorCode = '';
        this.operationMessage = '已新建空白字幕，点「插入」添加第一条；保存时才会创建同名 .srt 文件。';
        this.$nextTick(this.measureViewport);
      } catch (error) {
        this.loadError = `新建空白字幕失败：${subtitleExceptionText(error)}`;
        this.loadErrorCode = '';
      } finally {
        this.creatingBlank = false;
      }
    },
    async convertToUTF8() {
      if (this.encoding.converting || this.encoding.unknown) return;
      const chosen = this.encoding.candidates.length > 1 ? this.encoding.selected : this.encoding.detected;
      this.encoding = { ...this.encoding, converting: true, error: '' };
      try {
        const result = await ConvertSubtitleToUTF8(this.video.id, chosen || '');
        if (result?.error_code === 'subtitle_encoding_ambiguous') {
          this.applyDocumentError({ ...result, error_code: 'subtitle_encoding_not_utf8' });
          return;
        }
        if (result?.error_code) {
          this.encoding = { ...this.encoding, converting: false, error: subtitleErrorText(result.error_code, result.message) };
          return;
        }
        await this.loadWorkbench();
        const warnings = Array.isArray(result?.warnings) && result.warnings.length ? ` ${result.warnings.join('；')}` : '';
        if (!this.loadError) this.operationMessage = `已转换为 UTF-8（原文件已备份，可在「历史版本」恢复）。${warnings}`;
      } catch (error) {
        this.encoding = { ...this.encoding, converting: false, error: `转换失败：${subtitleExceptionText(error)}` };
      }
    },
    async loadBackups() {
      try {
        const backups = await ListSubtitleBackups(this.video.id);
        this.backups = Array.isArray(backups) ? backups : [];
      } catch (_error) {
        this.backups = [];
      }
      if (!this.backups.some(backup => backup.id === this.selectedBackupID)) {
        this.selectedBackupID = this.backups[0]?.id || '';
      }
    },
    backupLabel(backup) {
      return `${formatLocalTime(backup.created_at)} · ${formatBytes(backup.size)}`;
    },
    async restoreSelectedBackup() {
      const backup = this.backups.find(item => item.id === this.selectedBackupID);
      if (!backup || this.restoringBackup) return;
      const lead = this.isDirty ? '当前还有未保存的修改，恢复后会丢弃。' : '';
      const confirmed = await confirmAction({
        title: '恢复字幕版本',
        message: `${lead}把字幕恢复到 ${formatLocalTime(backup.created_at)} 的版本？当前的字幕文件会先备份，之后仍可再恢复回来。`,
        confirmText: '恢复',
        danger: this.isDirty
      });
      if (!confirmed) return;
      this.restoringBackup = true;
      try {
        const result = await RestoreSubtitleBackup(this.video.id, backup.id);
        await this.loadWorkbench();
        const warnings = Array.isArray(result?.warnings) && result.warnings.length ? ` ${result.warnings.join('；')}` : '';
        if (!this.loadError) this.operationMessage = `已恢复到 ${formatLocalTime(backup.created_at)} 的版本（恢复前的字幕已备份）。${warnings}`;
        this.$emit('saved', { video_id: this.video.id, status: 'restored' });
      } catch (error) {
        this.operationError = `恢复失败：${subtitleExceptionText(error)}`;
      } finally {
        this.restoringBackup = false;
      }
    },
    focusEntry(index) {
      const entry = this.entries[index];
      if (!entry) return;
      this.selectedIDs = [entry.client_id];
      this.$nextTick(() => this.scrollEntryIntoView(index));
    },
    goToNextIssue() {
      const indexes = this.issueEntryIndexes;
      if (!indexes.length) return;
      const current = this.selectionIndexes.length ? this.selectionIndexes[this.selectionIndexes.length - 1] : -1;
      const next = indexes.find(index => index > current);
      this.focusEntry(next === undefined ? indexes[0] : next);
    },
    fixTimingIssues() {
      const { entries, fixed } = fixSubtitleTimingIssues(this.entries);
      if (!fixed) {
        this.operationMessage = '没有能自动修复的时间问题，请逐条调整。';
        return;
      }
      this.mutate(() => { this.entries = entries; });
      this.operationMessage = `已修复 ${fixed} 处时间问题，保存前仍可撤销。`;
    },
    async reloadDocument() {
      if (this.isDirty && !await confirmAction({ title: '重新加载字幕', message: '重新加载会丢弃当前未保存修改，确定继续？', confirmText: '丢弃并重载', danger: true })) return;
      await this.loadWorkbench();
    },
    async requestClose() {
      if (this.isDirty && !await confirmAction({ title: '关闭字幕工作台', message: '字幕还有未保存修改，确定关闭并丢弃吗？', confirmText: '丢弃并关闭', danger: true })) return;
      this.$emit('close');
    },
    handleBeforeUnload(event) {
      if (!this.isDirty) return;
      event.preventDefault();
      event.returnValue = '';
    },
    handleShortcut(event) {
      const modifier = event.metaKey || event.ctrlKey;
      if (!modifier) return;
      if (event.key.toLowerCase() === 's') {
        event.preventDefault();
        this.saveDocument();
      } else if (event.key.toLowerCase() === 'z' && event.shiftKey) {
        event.preventDefault();
        this.redo();
      } else if (event.key.toLowerCase() === 'z') {
        event.preventDefault();
        this.undo();
      } else if (event.key.toLowerCase() === 'f') {
        event.preventDefault();
        this.$refs.findInput?.focus();
      }
    },
    configureVideo() {
      const video = this.$refs.videoElement;
      if (!video) return;
      video.defaultMuted = true;
      video.muted = true;
    },
    handleTimeUpdate(event) {
      this.currentTimeMs = Math.max(0, Math.round(Number(event.target.currentTime || 0) * 1000));
    },
    seekToEntry(entry) {
      this.selectedIDs = [entry.client_id];
      this.currentTimeMs = Number(entry.start_time_ms) || 0;
      const video = this.$refs.videoElement;
      if (video) video.currentTime = this.currentTimeMs / 1000;
    },
    async previewExternally() {
      try {
        await PreviewExternally(this.video.id);
      } catch (error) {
        this.operationError = `无法打开系统播放器：${subtitleExceptionText(error)}`;
      }
    },
    measureViewport() {
      this.viewportHeight = this.$refs.entryScroller?.clientHeight || 800;
    },
    handleEntryScroll(event) {
      this.scrollTop = event.target.scrollTop;
      this.viewportHeight = event.target.clientHeight || this.viewportHeight;
    },
    scrollEntryIntoView(index) {
      const scroller = this.$refs.entryScroller;
      if (!scroller) return;
      const top = index * ENTRY_ROW_HEIGHT;
      const bottom = top + ENTRY_ROW_HEIGHT;
      if (top < scroller.scrollTop) scroller.scrollTop = top;
      else if (bottom > scroller.scrollTop + scroller.clientHeight) scroller.scrollTop = Math.max(0, bottom - scroller.clientHeight);
      this.scrollTop = scroller.scrollTop;
    },
    mutate(mutator) {
      const before = cloneEntries(this.entries);
      mutator();
      if (entriesSignature(before) === entriesSignature(this.entries)) return;
      this.history.push(before);
      if (this.history.length > HISTORY_LIMIT) this.history.shift();
      this.future = [];
      this.operationError = '';
      this.operationMessage = '';
    },
    undo() {
      if (this.history.length === 0) return;
      this.future.push(cloneEntries(this.entries));
      this.entries = this.history.pop();
      this.selectedIDs = this.selectedIDs.filter(id => this.entries.some(entry => entry.client_id === id));
    },
    redo() {
      if (this.future.length === 0) return;
      this.history.push(cloneEntries(this.entries));
      this.entries = this.future.pop();
      this.selectedIDs = this.selectedIDs.filter(id => this.entries.some(entry => entry.client_id === id));
    },
    updateEntryText(index, value) {
      this.mutate(() => { this.entries[index].text = value; });
    },
    updateEntryTime(index, field, value) {
      const milliseconds = this.parseTimestampInput(value);
      this.mutate(() => { this.entries[index][field] = milliseconds; });
    },
    toggleEntrySelection(clientID, checked) {
      if (checked && !this.selectedIDs.includes(clientID)) this.selectedIDs = [...this.selectedIDs, clientID];
      else if (!checked) this.selectedIDs = this.selectedIDs.filter(id => id !== clientID);
    },
    toggleAllSelection() {
      this.selectedIDs = this.allSelected ? [] : this.entries.map(entry => entry.client_id);
    },
    insertEntry() {
      const index = this.selectionIndexes.length ? this.selectionIndexes[this.selectionIndexes.length - 1] + 1 : this.entries.length;
      this.mutate(() => {
        const previous = this.entries[index - 1];
        const next = this.entries[index];
        const start = previous ? Number(previous.end_time_ms) : 0;
        let end = next ? Number(next.start_time_ms) : start + 2000;
        if (end <= start) {
          end = start + 1000;
          for (let position = index; position < this.entries.length; position += 1) {
            this.entries[position].start_time_ms += 1000;
            this.entries[position].end_time_ms += 1000;
          }
        }
        const entry = { client_id: newClientID(++this.localIDCounter), start_time_ms: start, end_time_ms: end, text: '新字幕' };
        this.entries.splice(index, 0, entry);
        this.selectedIDs = [entry.client_id];
      });
      this.$nextTick(() => this.scrollEntryIntoView(index));
    },
    deleteSelected() {
      const selected = new Set(this.selectedIDs);
      this.mutate(() => { this.entries = this.entries.filter(entry => !selected.has(entry.client_id)); });
      this.selectedIDs = [];
    },
    splitSelected() {
      if (this.selectionIndexes.length !== 1) return;
      const index = this.selectionIndexes[0];
      const selected = this.entries[index];
      if (Number(selected.end_time_ms) - Number(selected.start_time_ms) < 2) {
        this.operationError = '当前字幕时长太短，无法拆分。';
        return;
      }
      const parts = this.splitText(selected.text);
      if (!parts[0].trim() || !parts[1].trim()) {
        this.operationError = '当前字幕文本太短，无法拆分为两条非空字幕。';
        return;
      }
      this.mutate(() => {
        const entry = this.entries[index];
        const originalEnd = Number(entry.end_time_ms);
        const midpoint = Math.floor((Number(entry.start_time_ms) + originalEnd) / 2);
        const [firstText, secondText] = parts;
        entry.end_time_ms = midpoint;
        entry.text = firstText;
        const second = { ...entry, client_id: newClientID(++this.localIDCounter), start_time_ms: midpoint, end_time_ms: originalEnd, text: secondText };
        this.entries.splice(index + 1, 0, second);
        this.selectedIDs = [entry.client_id, second.client_id];
      });
    },
    splitText(text) {
      const lines = String(text || '').split('\n');
      if (lines.length > 1) {
        const midpoint = Math.ceil(lines.length / 2);
        return [lines.slice(0, midpoint).join('\n'), lines.slice(midpoint).join('\n')];
      }
      const value = lines[0] || '';
      let midpoint = Math.floor(value.length / 2);
      const nextSpace = value.indexOf(' ', midpoint);
      if (nextSpace > 0) midpoint = nextSpace;
      return [value.slice(0, midpoint).trim(), value.slice(midpoint).trim()];
    },
    mergeSelected() {
      if (!this.canMergeSelection) return;
      const indexes = this.selectionIndexes;
      this.mutate(() => {
        const first = this.entries[indexes[0]];
        const last = this.entries[indexes[indexes.length - 1]];
        first.end_time_ms = last.end_time_ms;
        first.text = indexes.map(index => this.entries[index].text).join('\n');
        this.entries.splice(indexes[0] + 1, indexes.length - 1);
        this.selectedIDs = [first.client_id];
      });
    },
    applyOffset(selectionOnly) {
      if (!this.validOffset) return;
      const offset = Number(this.offsetMs);
      const selected = new Set(this.selectedIDs);
      this.mutate(() => {
        this.entries.forEach(entry => {
          if (selectionOnly && !selected.has(entry.client_id)) return;
          entry.start_time_ms += offset;
          entry.end_time_ms += offset;
        });
      });
    },
    replaceMatches() {
      if (!this.findText || this.replaceMatchCount === 0) return;
      const selected = new Set(this.selectedIDs);
      this.mutate(() => {
        this.entries.forEach(entry => {
          if (this.replaceSelectionOnly && !selected.has(entry.client_id)) return;
          entry.text = entry.text.split(this.findText).join(this.replaceText);
        });
      });
    },
    async retranslateSelection() {
      if (this.translating || this.selectedIDs.length === 0) return;
      const selected = new Set(this.selectedIDs);
      const sourceEntries = this.entries.filter(entry => selected.has(entry.client_id));
      const mode = this.retranslateModeModel;
      this.translating = true;
      this.operationError = '';
      try {
        const result = await RetranslateSubtitleEntries({
          video_id: this.video.id,
          source_lang: this.sourceLang,
          target_lang: this.targetLang,
          mode,
          entries: sourceEntries.map(entry => ({ client_id: entry.client_id, text: entry.text }))
        });
        const translated = Array.isArray(result?.entries) ? result.entries : [];
        const exact = translated.length === sourceEntries.length && translated.every((entry, index) => entry.client_id === sourceEntries[index].client_id);
        if (!exact) throw new Error('翻译结果与选区不一致，未应用任何修改');
        const byID = new Map(translated.map(entry => [entry.client_id, entry.text]));
        this.mutate(() => {
          this.entries.forEach(entry => {
            if (byID.has(entry.client_id)) entry.text = byID.get(entry.client_id);
          });
        });
        const scope = mode === 'translation_line' ? '（只替换了译文行）' : '';
        const warnings = Array.isArray(result?.warnings) && result.warnings.length ? ` ${result.warnings.join('；')}` : '';
        this.operationMessage = `已翻译 ${translated.length} 条${scope}，保存前仍可撤销。${warnings}`;
      } catch (error) {
        this.operationError = `重新翻译失败：${subtitleExceptionText(error)}`;
      } finally {
        this.translating = false;
      }
    },
    async saveDocument() {
      if (this.saving || !this.isDirty || this.validationIssues.length > 0) return;
      this.saving = true;
      this.operationError = '';
      this.operationMessage = '';
      try {
        const result = await SaveSubtitleEditDocument({
          video_id: this.video.id,
          fingerprint: this.documentFingerprint,
          entries: cloneEntries(this.entries)
        });
        if (result?.status !== 'saved' && result?.status !== 'saved_index_pending') {
          const issueMessage = Array.isArray(result?.issues) && result.issues.length ? result.issues[0].message : '';
          // 校验被拒时直接跳到第一个问题（D-PC15）。
          const firstIndex = this.entries.findIndex(entry => entry.client_id === result?.first_issue_client_id);
          if (firstIndex >= 0) this.focusEntry(firstIndex);
          else if (Number(result?.first_issue_entry_index) > 0) this.focusEntry(Number(result.first_issue_entry_index) - 1);
          const position = Number(result?.first_issue_entry_index) > 0 ? `第 ${result.first_issue_entry_index} 条：` : '';
          this.operationError = issueMessage
            ? `${position}${issueMessage}`
            : subtitleErrorText(result?.error_code, result?.message || '字幕保存被拒绝');
          return;
        }
        if (result.fingerprint) this.documentFingerprint = { ...result.fingerprint };
        this.baselineSignature = entriesSignature(this.entries);
        this.history = [];
        this.future = [];
        const backup = result.backup_id ? '原字幕已备份，可在「历史版本」恢复。' : '';
        this.operationMessage = result.status === 'saved_index_pending'
          ? `${result.message || '字幕已保存，索引将在稍后重建。'}${backup}`
          : `字幕已保存。${backup}`;
        this.loadBackups();
        this.$emit('saved', { video_id: this.video.id, status: result.status });
      } catch (error) {
        this.operationError = `保存字幕失败：${subtitleExceptionText(error)}`;
      } finally {
        this.saving = false;
      }
    },
    formatTimestamp(milliseconds) {
      const total = Math.max(0, Math.floor(Number(milliseconds) || 0));
      const hours = Math.floor(total / 3600000);
      const minutes = Math.floor((total % 3600000) / 60000);
      const seconds = Math.floor((total % 60000) / 1000);
      return [hours, minutes, seconds].map(value => String(value).padStart(2, '0')).join(':');
    },
    formatTimestampInput(milliseconds) {
      const total = Math.max(0, Math.floor(Number(milliseconds) || 0));
      const hours = Math.floor(total / 3600000);
      const minutes = Math.floor((total % 3600000) / 60000);
      const seconds = Math.floor((total % 60000) / 1000);
      const millis = total % 1000;
      return `${String(hours).padStart(2, '0')}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}.${String(millis).padStart(3, '0')}`;
    },
    parseTimestampInput(value) {
      const match = String(value || '').trim().match(/^(\d+):([0-5]\d):([0-5]\d)[.,](\d{3})$/);
      if (!match) return Number.NaN;
      return Number(match[1]) * 3600000 + Number(match[2]) * 60000 + Number(match[3]) * 1000 + Number(match[4]);
    }
  }
};
</script>

<style scoped>
.subtitle-workbench-overlay {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: grid;
  place-items: center;
  padding: 16px;
  background: var(--overlay-strong);
}

.subtitle-workbench {
  width: min(1500px, 100%);
  height: min(960px, calc(100vh - 32px));
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid var(--border-color);
  border-radius: 18px;
  background: var(--panel-bg);
  box-shadow: var(--shadow-modal);
}

.subtitle-workbench__title { min-width: 0; }
.subtitle-workbench__title h2 { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.subtitle-workbench__header-actions { flex: none; }

.subtitle-workbench__header,
.subtitle-workbench__footer,
.subtitle-workbench__editor-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.subtitle-workbench__header {
  padding: 14px 18px;
  border-bottom: 1px solid var(--border-color);
}

.subtitle-workbench__header h2,
.subtitle-workbench__tool-section h3 {
  margin: 0;
}

.subtitle-workbench__eyebrow {
  margin: 0 0 3px;
  color: var(--text-muted);
  font-size: 12px;
}

.subtitle-workbench__header-actions,
.subtitle-workbench__history-actions,
.subtitle-workbench__button-grid,
.subtitle-workbench__language-row,
.subtitle-workbench__offset-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.subtitle-workbench__dirty {
  color: var(--text-muted);
  font-size: 13px;
}

.subtitle-workbench__dirty--active {
  color: var(--warning-color);
  font-weight: 650;
}

.subtitle-workbench__layout {
  flex: 1 1 auto;
  min-height: 0;
  display: grid;
  grid-template-columns: minmax(300px, 390px) minmax(0, 1fr);
}

.subtitle-workbench__preview {
  min-height: 0;
  overflow-y: auto;
  padding: 14px;
  border-right: 1px solid var(--border-color);
  background: var(--control-bg);
}

.subtitle-workbench__video {
  width: 100%;
  max-height: 260px;
  border-radius: 10px;
  background: #000;
}

.subtitle-workbench__preview-fallback,
.subtitle-workbench__state {
  padding: 24px;
  color: var(--text-secondary);
  text-align: center;
}

.subtitle-workbench__state--error,
.subtitle-workbench__message--error,
.subtitle-workbench__entry-errors {
  color: var(--danger-color);
}

.subtitle-workbench__state-hint,
.subtitle-workbench__hint {
  margin: 0;
  color: var(--text-muted);
  font-size: 12px;
}

.subtitle-workbench__encoding-options {
  display: grid;
  gap: 8px;
  max-width: 520px;
  margin: 12px auto;
  text-align: left;
}

.subtitle-workbench__encoding-option {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  cursor: pointer;
}

.subtitle-workbench__encoding-option span {
  display: grid;
  gap: 2px;
}

.subtitle-workbench__encoding-option em {
  color: var(--text-muted);
  font-size: 12px;
  font-style: normal;
  white-space: pre-line;
}

.subtitle-workbench__mode-row {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  color: var(--text-secondary);
  font-size: 12px;
}

.subtitle-workbench__issue-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.subtitle-workbench__issue-count {
  color: var(--danger-color);
  font-weight: 600;
}

.subtitle-workbench__playback-status {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  margin: 8px 0 12px;
  color: var(--text-secondary);
  font-size: 12px;
}

.subtitle-workbench__tool-section {
  display: grid;
  gap: 8px;
  margin-top: 12px;
  padding: 12px;
  border: 1px solid var(--border-color);
  border-radius: 12px;
  background: var(--panel-bg);
}

.subtitle-workbench__tool-section h3 {
  font-size: 14px;
}

.subtitle-workbench__tool-section input[type='text'],
.subtitle-workbench__tool-section input[type='search'],
.subtitle-workbench__tool-section input[type='number'],
.subtitle-workbench__tool-section select {
  width: 100%;
  min-width: 0;
  box-sizing: border-box;
  border: 1px solid var(--border-color);
  border-radius: 8px;
  padding: 7px 8px;
  background: var(--control-bg);
  color: var(--text-primary);
}

.subtitle-workbench__button-grid > * {
  flex: 1;
}

.subtitle-workbench__editor {
  min-width: 0;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.subtitle-workbench__editor-toolbar {
  padding: 10px 14px;
  border-bottom: 1px solid var(--border-color);
  color: var(--text-secondary);
  font-size: 13px;
}

.subtitle-workbench__entries {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: 0 14px;
}

.subtitle-workbench__entry {
  box-sizing: border-box;
  height: 198px;
  margin: 6px 0;
  padding: 10px 12px;
  overflow: hidden;
  border: 1px solid var(--border-color);
  border-radius: 12px;
  background: var(--control-bg);
}

.subtitle-workbench__entry--active {
  border-color: var(--accent-color);
  box-shadow: inset 3px 0 0 var(--accent-color);
}

.subtitle-workbench__entry--invalid {
  border-color: var(--danger-color);
}

.subtitle-workbench__entry-heading,
.subtitle-workbench__timing {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.subtitle-workbench__entry-heading {
  margin-bottom: 7px;
  font-size: 13px;
}

.subtitle-workbench__seek {
  border: 0;
  background: transparent;
  color: var(--accent-color);
  cursor: pointer;
}

.subtitle-workbench__timing label {
  flex: 1;
  color: var(--text-muted);
  font-size: 11px;
}

.subtitle-workbench__timing input,
.subtitle-workbench__entry textarea {
  width: 100%;
  box-sizing: border-box;
  border: 1px solid var(--border-color);
  border-radius: 7px;
  padding: 6px 7px;
  background: var(--panel-bg);
  color: var(--text-primary);
}

.subtitle-workbench__entry textarea {
  height: 70px;
  margin-top: 8px;
  resize: none;
  line-height: 1.45;
}

/* 条目行高固定（虚拟滚动），问题多于两条时在行内滚动而不是被裁掉。 */
.subtitle-workbench__entry-errors {
  margin: 5px 0 0;
  padding-left: 18px;
  max-height: 36px;
  overflow-y: auto;
  font-size: 11px;
}

.subtitle-workbench__footer {
  padding: 11px 18px;
  border-top: 1px solid var(--border-color);
}

.subtitle-workbench__messages {
  min-width: 0;
}

.subtitle-workbench__message {
  margin: 0;
  color: var(--text-secondary);
  font-size: 13px;
  overflow-wrap: anywhere;
}

@media (max-width: 900px) {
  .subtitle-workbench-overlay {
    padding: 0;
  }

  .subtitle-workbench {
    width: 100%;
    height: 100vh;
    border-radius: 0;
  }

  .subtitle-workbench__layout {
    grid-template-columns: 1fr;
    grid-template-rows: minmax(240px, 42vh) minmax(0, 1fr);
  }

  .subtitle-workbench__preview {
    border-right: 0;
    border-bottom: 1px solid var(--border-color);
  }

  .subtitle-workbench__header,
  .subtitle-workbench__footer {
    align-items: flex-start;
    flex-wrap: wrap;
  }
}
</style>
