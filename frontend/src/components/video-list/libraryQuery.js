// Owns list rows, cursor, query counts and reload lifecycle. The page supplies filters,
// row projections and UI refs; no global state. Selection remains in librarySelection.
import { SearchLibraryVideoPage, CountLibraryVideos, SearchSemanticVideos, FindSimilarVideos } from '../../../wailsjs/go/main/App';
import { notifyError } from '../../utils/feedback.js';
export const IN_PLACE_REFRESH_PAGE_LIMIT = 100;
const EMPTY_PAGING_CURSOR = Object.freeze({
  cursorScore: 0,
  cursorSize: 0,
  cursorID: 0,
  libraryCursor: null
});
export const libraryQueryMixin = {
  data() {
    return {
      viewInitialized: false,
      viewRefreshPending: false,
      videos: [],
      cursorScore: 0,
      cursorSize: 0,
      cursorID: 0,
      libraryCursor: null,
      pageSize: 20,
      loading: false,
      hasMore: true,
      // 列表当前装的是哪一组查询条件（virtualListQueryKey）。重载时条件没变就原地刷新、停在原位，
      // 条件变了才清空回顶。
      listQueryKey: '',
      // 原地刷新期间列表照常显示，不出「加载中」；分页游标被刷新占用，触底加载要等它结束。
      refreshingInPlace: false,
      reloadRequested: false,
      reloadPromise: null,
      loadIdleResolvers: [],
      filteredCount: null,
      libraryTotalCount: null,
      countToken: 0,
    };
  },
  methods: {
    activateLibraryView() {
      if (!this.pageActive) return;
      if (!this.viewInitialized) {
        this.viewInitialized = true;
        this.viewRefreshPending = false;
        this.listQueryKey = this.virtualListQueryKey;
        this.loadVideos();
        this.refreshLibraryCounts();
        this.loadSemanticStatus();
        this.loadSavedLibraryViews();
        this.loadEnhancementCapability();
      } else if (this.viewRefreshPending) {
        this.viewRefreshPending = false;
        this.resetAndLoadVideos();
      }
    },
    // limit：一页取多少条，触底加载用 pageSize；「全选当前结果」补齐剩余结果时一次取得多一些（与原地刷新同一上限）。
    async loadVideos(limit = this.pageSize) {
      if (this.loading || this.refreshingInPlace || !this.hasMore) return;
      this.loading = true;
      try {
        const keyword = this.currentQueryKeyword();
        this.debugLog('loadVideos begin', {
          keyword,
          searchMode: this.searchMode,
          hasStructuredFilters: this.hasStructuredFilters(),
          cursorScore: this.cursorScore,
          cursorSize: this.cursorSize,
          cursorID: this.cursorID,
          pageSize: this.pageSize,
          existingVideos: this.videos.length
        });

        const page = await this.fetchNextVideoPage(limit, this.videos.length);
        const newVideos = page.videos;

        this.debugLog('loadVideos query resolved', {
          count: newVideos.length,
          sample: newVideos.slice(0, 3).map(video => ({ id: video.id, name: video.name, path: video.path })),
          mode: this.smartView || keyword || this.hasStructuredFilters() ? 'filtered' : 'paginated'
        });

        if (!page.hasMore) {
          this.hasMore = false;
        }
        if (newVideos.length > 0) {
          this.videos.push(...newVideos);
        }
        this.debugLog('loadVideos applied to state', {
          totalVideos: this.videos.length,
          hasMore: this.hasMore
        });
      } catch (err) {
        this.reportVideoLoadFailure(err);
      } finally {
        this.loading = false;
        const idleResolvers = this.loadIdleResolvers.splice(0);
        idleResolvers.forEach(resolve => resolve());
        this.debugLog('loadVideos finished', {
          totalVideos: this.videos.length,
          hasMore: this.hasMore,
          loading: this.loading
        });
      }
    },
    // 从当前分页游标往后取一页并推进游标；取回的行要不要进列表、hasMore 怎么改由调用方决定。
    // offset 只有语义检索用（按条数翻页），其余接口走键集游标。
    async fetchNextVideoPage(limit, offset) {
      const keyword = this.currentQueryKeyword();
      let videos = [];
      let semanticHasMore = null;

      if (this.searchMode === 'semantic') {
        if (!keyword && !this.semanticSimilarVideoID) {
          return { videos, hasMore: false };
        }
        this.semanticSearchError = '';
        const request = {
          filter: this.currentLibraryFilter(),
          offset,
          limit
        };
        const page = this.semanticSimilarVideoID
          ? await FindSimilarVideos({ ...request, video_id: this.semanticSimilarVideoID })
          : await SearchSemanticVideos({ ...request, query: keyword });
        this.semanticCoverage = page?.coverage || null;
        semanticHasMore = !!page?.has_more;
        videos = (page?.hits || []).map(hit => ({ ...hit.video, _semanticScore: hit.score }));
        this.libraryCursor = null;
        await this.fillAutomaticOverrideKinds(videos);
      } else {
        const request = { filter: this.libraryPageFilter(), limit };
        if (this.libraryCursor) request.cursor = this.libraryCursor;
        const page = await SearchLibraryVideoPage(request);
        videos = page?.videos || [];
        this.libraryCursor = page?.next_cursor || null;
        this.mergeAutomaticOverrideKinds(page?.automatic_override_kinds);
        this.mergeVersionGroups(page?.version_groups, videos);
      }

      videos = await this.attachSubtitleHits(videos, keyword);

      const hasMore = this.searchMode === 'semantic' ? semanticHasMore : !!this.libraryCursor;
      return { videos, hasMore };
    },
    reportVideoLoadFailure(err) {
      if (this.searchMode === 'semantic') this.semanticSearchError = String(err);
      this.debugLog('loadVideos failed', { err: String(err) }, true);
      console.error('加载视频失败:', err);
      notifyError('加载视频失败: ' + err);
    },
    pagingCursor() {
      return Object.fromEntries(Object.keys(EMPTY_PAGING_CURSOR).map(key => [key, this[key]]));
    },
    setPagingCursor(cursor) {
      for (const key of Object.keys(EMPTY_PAGING_CURSOR)) this[key] = cursor[key];
    },
    // 同一组条件下的重载（后台对账、扫描汇总、行内操作之后）：列表不清空、不出「加载中」，
    // 从头按当前已加载的条数重取一遍，取齐后整体替换，再把视口顶部那一行放回原位。
    // 取的途中失败或条件变了，就留着旧列表、放回原游标，触底加载照旧往后翻。
    async refreshVideosInPlace() {
      const depth = this.videos.length;
      const queryKey = this.virtualListQueryKey;
      const previousCursor = this.pagingCursor();
      const refreshed = [];
      let hasMore = true;
      this.refreshingInPlace = true;
      this.setPagingCursor(EMPTY_PAGING_CURSOR);
      try {
        while (hasMore && refreshed.length < depth) {
          const limit = Math.min(IN_PLACE_REFRESH_PAGE_LIMIT, depth - refreshed.length);
          const page = await this.fetchNextVideoPage(limit, refreshed.length);
          refreshed.push(...page.videos);
          hasMore = page.hasMore;
          if (page.videos.length === 0) break;
        }
      } catch (err) {
        this.setPagingCursor(previousCursor);
        this.reportVideoLoadFailure(err);
        return;
      } finally {
        this.refreshingInPlace = false;
      }
      if (this.virtualListQueryKey !== queryKey) {
        this.setPagingCursor(previousCursor);
        return;
      }
      const anchor = this.$refs.virtualList?.captureScrollAnchor?.();
      this.videos = refreshed;
      this.hasMore = hasMore;
      const keptIDs = new Set(refreshed.map(video => video.id));
      this.selectedVideoIds = this.selectedVideoIds.filter(id => keptIDs.has(id));
      this.debugLog('refreshVideosInPlace applied', { depth, totalVideos: refreshed.length, hasMore });
      await this.$nextTick();
      this.$refs.virtualList?.restoreScrollAnchor?.(anchor);
    },
    waitForLoadIdle() {
      if (!this.loading) return Promise.resolve();
      return new Promise(resolve => this.loadIdleResolvers.push(resolve));
    },
    async resetAndLoadVideos() {
      if (!this.pageActive) { this.viewRefreshPending = true; return; }
      this.reloadRequested = true;
      if (this.reloadPromise) {
        await this.reloadPromise;
        if (this.reloadRequested) return this.resetAndLoadVideos();
        return;
      }
      this.refreshLibraryCounts();
      const activeReload = (async () => {
        while (this.reloadRequested) {
          this.reloadRequested = false;
          await this.waitForLoadIdle();
          const queryKey = this.virtualListQueryKey;
          if (this.videos.length > 0 && queryKey === this.listQueryKey) {
            await this.refreshVideosInPlace();
            continue;
          }
          this.listQueryKey = queryKey;
          this.videos = [];
          this.selectedVideoIds = [];
          this.setPagingCursor(EMPTY_PAGING_CURSOR);
          this.automaticOverrideKinds = {};
          if (this.searchMode !== 'semantic') {
            this.semanticCoverage = null;
            this.semanticSearchError = '';
          }
          this.hasMore = true;
          await this.loadVideos();
        }
      })();
      this.reloadPromise = activeReload;
      await activeReload;
      if (this.reloadPromise === activeReload) {
        this.reloadPromise = null;
      }
      if (this.reloadRequested) return this.resetAndLoadVideos();
    },
    // 片库总数只在为空时才重新取（refreshLibraryCounts）；扫描与重新检查之后置空让它重取。
    invalidateLibraryTotal() {
      this.libraryTotalCount = null;
    },
    // 结果条的命中数：筛选变化后请求一次，翻页不再重复请求。
    async refreshLibraryCounts() {
      if (!this.pageActive) { this.viewRefreshPending = true; return; }
      if (this.searchMode === 'semantic') {
        this.filteredCount = null;
        return;
      }
      const token = ++this.countToken;
      // 合并版本时两个数都按卡片计；口径（按卡片 / 按文件）变了，总数要重取。
      const collapse = this.versionCollapseActive();
      const totalStale = this.libraryTotalCount === null || this.libraryTotalCollapse !== collapse;
      try {
        const [filtered, total] = await Promise.all([
          CountLibraryVideos(this.libraryPageFilter()),
          totalStale ? CountLibraryVideos(this.libraryTotalFilter()) : Promise.resolve(this.libraryTotalCount)
        ]);
        if (token !== this.countToken) return;
        this.filteredCount = filtered;
        this.libraryTotalCount = total;
        this.libraryTotalCollapse = collapse;
      } catch (err) {
        if (token !== this.countToken) return;
        this.filteredCount = null;
        this.debugLog('refreshLibraryCounts failed', { err: String(err) }, true);
      }
    },
    async reloadCurrentView() {
      return this.resetAndLoadVideos();
    },
  }
};
