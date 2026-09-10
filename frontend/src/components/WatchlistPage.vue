<template>
  <section class="page-content watchlist-page">
    <header class="watchlist-heading">
      <h2>想看</h2>
      <p>记下想看但还没收进片库的影片，找到后手动移除。</p>
    </header>

    <form class="watchlist-form" data-test="watchlist-form" @submit.prevent="save">
      <label for="watchlist-title">{{ editID ? '修改片名' : '添加想看的影片' }}</label>
      <div class="watchlist-form-row">
        <input id="watchlist-title" ref="titleInput" v-model="title" class="text-input" type="text"
          placeholder="输入片名，回车添加" :disabled="busy" data-test="watchlist-title" />
        <button class="btn-primary" type="submit" :disabled="!canSave" data-test="watchlist-save">
          {{ busy ? '处理中…' : editID ? '保存修改' : '添加' }}
        </button>
        <button v-if="editID" class="btn-secondary" type="button" :disabled="busy"
          data-test="watchlist-cancel" @click="resetDraft">取消</button>
      </div>
    </form>

    <div class="watchlist-search-row">
      <input v-model="keyword" class="text-input" type="search" placeholder="搜索片名" aria-label="搜索想看片名"
        :disabled="busy" data-test="watchlist-search" @input="search" />
      <button class="btn-secondary" type="button" :disabled="loading || busy" @click="load()">刷新</button>
    </div>
    <p v-if="error" class="watchlist-error" role="alert" data-test="watchlist-error">{{ error }}</p>
    <p v-if="loading" class="watchlist-hint" role="status">正在读取…</p>
    <p v-else-if="loaded && !entries.length && !nextID && !error" class="watchlist-empty" data-test="watchlist-empty">
      {{ keyword.trim() ? '没有匹配的片名' : '还没有想看的影片，先记下一部吧。' }}
    </p>

    <ul class="watchlist-list">
      <li v-for="entry in entries" :key="entry.id" class="watchlist-entry" data-test="watchlist-entry">
        <div class="watchlist-entry-info">
          <strong>{{ entry.title }}</strong>
          <time :datetime="entry.created_at">{{ formatDate(entry.created_at) }} 添加</time>
        </div>
        <div class="watchlist-actions">
          <button class="btn-secondary btn-compact" type="button" :disabled="busy || loading"
            :data-test="`watchlist-edit-${entry.id}`" @click="startEdit(entry)">改名</button>
          <button class="btn-secondary btn-danger-outline btn-compact" type="button" :disabled="busy || loading"
            :data-test="`watchlist-remove-${entry.id}`" @click="remove(entry)">移除</button>
        </div>
      </li>
    </ul>
    <button v-if="nextID" class="btn-secondary watchlist-more" type="button" :disabled="loading || busy"
      data-test="watchlist-more" @click="load(true)">加载更多</button>
  </section>
</template>

<script>
import { CreateWatchlistEntry, DeleteWatchlistEntry, ListWatchlist, UpdateWatchlistEntry } from '../../wailsjs/go/main/App';
import { confirmAction, notifySuccess } from '../utils/feedback.js';

export default {
  name: 'WatchlistPage',
  data() {
    return { entries: [], nextID: 0, keyword: '', title: '', editID: 0, error: '', loading: false, loaded: false, busy: false, requestID: 0 };
  },
  computed: {
    canSave() {
      return Boolean(this.title.trim()) && !this.busy && !this.loading;
    }
  },
  mounted() { this.load(); },
  beforeUnmount() { this.requestID++; },
  methods: {
    async load(more = false) {
      if (more && (this.loading || !this.nextID)) return;
      const requestID = ++this.requestID;
      this.loading = true;
      this.error = '';
      try {
        const page = await ListWatchlist(this.keyword.trim(), more ? this.nextID : 0, 50);
        if (requestID !== this.requestID) return;
        this.entries = more ? [...this.entries, ...page.entries] : page.entries;
        this.nextID = page.next_id;
        this.loaded = true;
      } catch (err) {
        if (requestID === this.requestID) this.error = `读取想看片单失败：${err}`;
      } finally {
        if (requestID === this.requestID) this.loading = false;
      }
    },
    search() {
      this.entries = [];
      this.nextID = 0;
      this.loaded = false;
      this.load();
    },
    resetDraft() {
      this.title = '';
      this.editID = 0;
    },
    async startEdit(entry) {
      this.editID = entry.id;
      this.title = entry.title;
      await this.$nextTick();
      this.$refs.titleInput?.focus();
    },
    async save() {
      if (!this.canSave) return;
      const title = this.title.trim();
      if (Array.from(title).length > 200) {
        this.error = '片名不能超过 200 个字符';
        return;
      }
      this.busy = true;
      this.error = '';
      try {
        if (this.editID) {
          await UpdateWatchlistEntry(this.editID, title);
          notifySuccess('片名已修改');
        } else {
          await CreateWatchlistEntry(title);
          notifySuccess('已添加到想看');
          this.keyword = '';
        }
        this.resetDraft();
        await this.load();
      } catch (err) {
        this.error = String(err);
      } finally {
        this.busy = false;
      }
    },
    async remove(entry) {
      if (this.busy || this.loading) return;
      this.busy = true;
      try {
        if (!await confirmAction({ title: '移除想看记录', message: `从想看片单移除「${entry.title}」？`, confirmText: '移除', danger: true })) return;
        await DeleteWatchlistEntry(entry.id);
        this.entries = this.entries.filter(item => item.id !== entry.id);
        if (this.editID === entry.id) this.resetDraft();
        this.error = '';
        if (!this.entries.length && this.nextID) await this.load(true);
      } catch (err) {
        this.error = String(err);
      } finally {
        this.busy = false;
      }
    },
    formatDate(value) {
      return new Date(value).toLocaleDateString('zh-CN');
    }
  }
};
</script>

<style scoped>
.watchlist-page { max-width: 960px; margin: 0 auto; padding: 24px 28px 40px; }
.watchlist-heading h2 { margin: 0 0 6px; }
.watchlist-heading p, .watchlist-hint { color: var(--text-secondary); font-size: 13px; }
.watchlist-form { margin: 24px 0; padding: 18px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--surface-faint); }
.watchlist-form label { display: block; margin-bottom: 10px; font-weight: 600; font-size: 13px; }
.watchlist-form-row, .watchlist-search-row, .watchlist-actions { display: flex; align-items: center; gap: 10px; }
.watchlist-form-row .text-input, .watchlist-search-row .text-input { flex: 1; min-width: 0; }
.watchlist-form-row button, .watchlist-actions { flex-shrink: 0; }
.watchlist-search-row { margin-bottom: 16px; }
.watchlist-list { list-style: none; padding: 0; margin: 0; display: grid; gap: 10px; }
.watchlist-entry { display: flex; align-items: center; gap: 16px; padding: 14px 16px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--surface-faint); }
.watchlist-entry-info { flex: 1; min-width: 0; display: grid; gap: 6px; }
.watchlist-entry-info strong { overflow-wrap: anywhere; font-size: 15px; }
.watchlist-entry-info time { color: var(--text-secondary); font-size: 12px; }
.watchlist-empty { padding: 40px 16px; text-align: center; color: var(--text-secondary); }
.watchlist-error { color: var(--danger-color); overflow-wrap: anywhere; }
.watchlist-more { display: block; margin: 20px auto 0; }
</style>
