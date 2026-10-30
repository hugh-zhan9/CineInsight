// Owns selection and all-results acquisition. Uses the query owner's pagination
// and query identity; edits/partial failures must never become a partial "select all".
import { selectedSizeText } from './BatchActionBar.vue';
import { IN_PLACE_REFRESH_PAGE_LIMIT } from './libraryQuery.js';
export const librarySelectionMixin = {
  data() {
    return {
      selectedVideoIds: [],
      // 「全选当前结果」正在补取剩余的页（D-UD-10）。
      selectingAllMatching: false,
      // 用户自己改选择（勾选/取消、清空、键盘移动）时加一：补取途中改过，「全选当前结果」作废，不覆盖用户的改动。
      selectionEditSeq: 0,
    };
  },
  computed: {
    // 「全选当前结果（N）」的范围（D-UD-10）：工具栏按钮与底部选择操作条共用这一份，N 与点了之后选中的条数一致。
    //   筛选结果：整个筛选（N 是命中数，没加载的页先取回来，见 selectAllMatching）；
    //   语义检索、相似视频与随机批次：只选已经加载、列出来的行（N 是已加载条数），不往后取——
    //   后端没有距离阈值，往后取就是把整个已索引的片库都选上。
    selectAllScope() {
      const loadedOnly = !this.listIsFilterResult;
      return { loadedOnly, total: loadedOnly ? this.videos.length : this.matchingTotal };
    },
    // 「全选当前结果」要选的每一条都已选中：工具栏的按钮据此换成「取消选择」。
    selectAllScopeSelected() {
      if (!this.selectAllScope.loadedOnly) return this.allMatchingSelected;
      if (this.videos.length === 0) return false;
      const selected = new Set(this.selectedVideoIds);
      return this.videos.every(video => selected.has(video.id));
    },
    // 当前结果已全部选中：结果加载完了，且每一条都在选中里。
    allMatchingSelected() {
      if (this.hasMore || this.videos.length === 0) return false;
      const selected = new Set(this.selectedVideoIds);
      return this.videos.every(video => selected.has(video.id));
    },
    selectedTotalSizeText() {
      return selectedSizeText(this.videos, this.selectedVideoIds);
    },
  },
  methods: {
    isVideoSelected(videoID) {
      return this.selectedVideoIds.includes(videoID);
    },
    toggleVideoSelection(video, selected) {
      if (!video) return;
      this.selectionEditSeq += 1;
      if (selected) {
        if (!this.selectedVideoIds.includes(video.id)) {
          this.selectedVideoIds = [...this.selectedVideoIds, video.id];
        }
      } else {
        this.selectedVideoIds = this.selectedVideoIds.filter(id => id !== video.id);
      }
    },
    // 工具栏的「全选当前结果 / 取消选择」：已全选时清空，否则选中整个当前结果（与选择操作条同一个范围）。
    toggleSelectAllMatching() {
      if (this.selectAllScopeSelected) {
        this.clearSelection();
        return undefined;
      }
      return this.selectAllMatching();
    },
    // 「全选当前结果（N）」（D-UD-10），范围见 selectAllScope：
    //   语义检索、相似视频与随机批次只选已加载的行，不往后取；
    //   筛选结果作用于整个筛选，先把还没加载的页按当前条件取回来再全部选中（期间按钮写「正在选择…」）。
    //   取的途中条件变了、某一页取失败（已提示）、或用户自己改了选择，就不选——不把一部分说成「全部」，
    //   也不覆盖用户刚做的勾选。
    async selectAllMatching() {
      if (this.selectingAllMatching) return;
      if (this.selectAllScope.loadedOnly) {
        this.selectedVideoIds = this.videos.map(video => video.id);
        return;
      }
      this.selectingAllMatching = true;
      const queryKey = this.virtualListQueryKey;
      const editSeq = this.selectionEditSeq;
      // 勾选、取消选择与键盘移动会推 selectionEditSeq；批量动作做完直接清空选择的路径（导入本地资料、移动、
      // 版本组、剪辑工程）不推，所以再比一次内容：选择变了就作废这次全选。
      const startSelection = [...this.selectedVideoIds].sort((a, b) => a - b).join(',');
      const sameSelection = () => [...this.selectedVideoIds].sort((a, b) => a - b).join(',') === startSelection;
      const unchanged = () => this.virtualListQueryKey === queryKey && this.selectionEditSeq === editSeq && sameSelection();
      try {
        if (this.reloadPromise) await this.reloadPromise;
        await this.waitForLoadIdle();
        while (this.hasMore && unchanged()) {
          const before = this.videos.length;
          await this.loadVideos(IN_PLACE_REFRESH_PAGE_LIMIT);
          if (this.videos.length === before) break;
        }
        if (this.hasMore || !unchanged()) return;
        this.selectedVideoIds = this.videos.map(video => video.id);
      } finally {
        this.selectingAllMatching = false;
      }
    },
    clearSelection() {
      this.selectedVideoIds = [];
      this.selectionEditSeq += 1;
    },
  }
};
