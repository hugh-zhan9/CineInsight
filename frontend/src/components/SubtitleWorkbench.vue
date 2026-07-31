<template>
  <div class="subtitle-workbench-overlay" @click.self="requestClose">
    <section class="subtitle-workbench" role="dialog" aria-modal="true" aria-label="字幕编辑工作台">
      <header class="subtitle-workbench__header">
        <div>
          <p class="subtitle-workbench__eyebrow">字幕编辑工作台</p>
          <h2>{{ video.name }}</h2>
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
      <div v-else-if="loadError" class="subtitle-workbench__state subtitle-workbench__state--error" role="alert">
        <p>{{ loadError }}</p>
        <button type="button" class="btn-primary" @click="loadWorkbench">重试</button>
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
              <button type="button" class="btn-secondary" :disabled="translating || selectedIDs.length === 0" @click="retranslateSelection">
                {{ translating ? '翻译中...' : `翻译选中 ${selectedIDs.length} 条` }}
              </button>
            </section>
          </aside>

          <main class="subtitle-workbench__editor">
            <div class="subtitle-workbench__editor-toolbar">
              <div class="subtitle-workbench__history-actions">
                <button data-test="undo" type="button" class="btn-secondary" :disabled="history.length === 0" @click="undo">撤销</button>
                <button data-test="redo" type="button" class="btn-secondary" :disabled="future.length === 0" @click="redo">重做</button>
                <button type="button" class="btn-secondary" @click="toggleAllSelection">{{ allSelected ? '取消全选' : '全选' }}</button>
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
            <p v-else-if="validationIssues.length" class="subtitle-workbench__message subtitle-workbench__message--error">
              当前有 {{ validationIssues.length }} 个校验问题，修正后才能保存。
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
  GetPreviewSession,
  GetSubtitleEditDocument,
  PreviewExternally,
  RetranslateSubtitleEntries,
  SaveSubtitleEditDocument
} from '../../wailsjs/go/main/App';

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
    }
  },
  watch: {
    activeEntryIndex(next) {
      if (!this.followPlayback || next < 0 || next === this.lastFollowedIndex) return;
      this.lastFollowedIndex = next;
      this.scrollEntryIntoView(next);
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
      this.operationError = '';
      this.operationMessage = '';
      this.previewLoading = true;
      const documentPromise = GetSubtitleEditDocument(this.video.id);
      const previewPromise = GetPreviewSession(this.video.id);
      try {
        const document = await documentPromise;
        this.documentFingerprint = { ...document.fingerprint };
        this.entries = cloneEntries(document.entries);
        this.baselineSignature = entriesSignature(this.entries);
        this.history = [];
        this.future = [];
        this.selectedIDs = [];
      } catch (error) {
        this.loadError = `无法打开字幕工作台：${String(error)}`;
      } finally {
        this.loading = false;
      }
      try {
        this.previewSession = await previewPromise;
      } catch (error) {
        this.previewError = String(error);
      } finally {
        this.previewLoading = false;
      }
      this.$nextTick(this.measureViewport);
    },
    async reloadDocument() {
      if (this.isDirty && !window.confirm('重新加载会丢弃当前未保存修改，确定继续？')) return;
      await this.loadWorkbench();
    },
    requestClose() {
      if (this.isDirty && !window.confirm('字幕还有未保存修改，确定关闭并丢弃吗？')) return;
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
        this.operationError = `无法打开系统播放器：${String(error)}`;
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
      this.translating = true;
      this.operationError = '';
      try {
        const result = await RetranslateSubtitleEntries({
          video_id: this.video.id,
          source_lang: this.sourceLang,
          target_lang: this.targetLang,
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
        this.operationMessage = `已翻译 ${translated.length} 条，保存前仍可撤销。`;
      } catch (error) {
        this.operationError = `重新翻译失败：${String(error)}`;
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
          this.operationError = issueMessage || result?.message || '字幕保存被拒绝';
          return;
        }
        this.documentFingerprint = { ...result.fingerprint };
        this.baselineSignature = entriesSignature(this.entries);
        this.history = [];
        this.future = [];
        this.operationMessage = result.status === 'saved_index_pending' ? (result.message || '字幕已保存，索引将在稍后重建。') : '字幕已保存。';
        this.$emit('saved', { video_id: this.video.id, status: result.status });
      } catch (error) {
        this.operationError = `保存字幕失败：${String(error)}`;
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
  background: rgba(15, 23, 42, 0.72);
  backdrop-filter: blur(8px);
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
  box-shadow: 0 24px 80px rgba(15, 23, 42, 0.38);
}

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
  border-color: rgba(229, 72, 77, 0.55);
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

.subtitle-workbench__entry-errors {
  margin: 5px 0 0;
  padding-left: 18px;
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
