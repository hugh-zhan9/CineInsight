import { AddVersionMembers, CreateVersionGroup, GetVersionGroup, GetVideosByIDs, RemoveVersionMember, ReorderVersionMembers } from '../../../wailsjs/go/main/App';
import { confirmAction, notify, notifyError, notifySuccess } from '../../utils/feedback.js';
import {
  VERSION_GROUP_MAX_MEMBERS, isVersionGroupConflict, isVersionGroupNotFound, loadCollapseVersions, parseVersionMemberConflict,
  primaryFirstOrder, saveCollapseVersions, versionGroupErrorText, versionMemberTitle
} from '../../utils/versionGroups.js';

// 片库页的多版本聚合接线（D-MW-VERSIONS）。查询条件、列表与原地刷新仍归 VideoListPage；
// 这里只持有「合并版本」开关、本页代表 → 组汇总的映射、展开状态与两个弹窗的开关。
// 宿主需提供：currentLibraryFilter、isContinueWatchingKeyset、resetAndLoadVideos、playVideo、
// openPreview、selectedVideoIds、searchMode、smartView、sortMode。
export const versionGroupsMixin = {
  data() {
    return {
      collapseVersions: loadCollapseVersions(),
      // 视频 ID（字符串）→ VersionGroupSummary；只记本页代表，按页合并。
      versionGroups: {},
      expandedVersionGroupIDs: [],
      versionGroupBusy: false,
      versionGroupDialog: { show: false, groupId: 0, videoId: 0, videoTitle: '' },
      versionPanelOpen: false,
      // 片库总数是在哪种口径下取的（按卡片 / 按文件）；口径变了要重取。
      libraryTotalCollapse: null
    };
  },
  methods: {
    // 只有走 SearchLibraryVideoPage 的列表才聚合；最近播放、继续观看（均衡排序）、语义搜索与随机批次是文件级入口。
    versionCollapseActive() {
      if (!this.collapseVersions || this.searchMode === 'semantic' || this.randomPick?.active) return false;
      if (this.smartView === 'recently_played' && this.sortMode === 'balanced') return false;
      return !this.isContinueWatchingKeyset();
    },
    libraryPageFilter() {
      return { ...this.currentLibraryFilter(), collapse_versions: this.versionCollapseActive() };
    },
    libraryTotalFilter() {
      return { ...this.emptyLibraryFilter(), collapse_versions: this.versionCollapseActive() };
    },
    setCollapseVersions(value) {
      this.collapseVersions = !!value;
      saveCollapseVersions(this.collapseVersions);
      this.libraryTotalCount = null;
      this.resetAndLoadVideos();
    },
    // 按页合并：本页每个视频要么换成新汇总，要么去掉旧汇总（组解散、代表变化）。
    mergeVersionGroups(groups, videos) {
      const next = { ...this.versionGroups };
      for (const video of videos || []) {
        const key = String(video?.id);
        const summary = groups?.[key];
        // 整页数据比任何还在路上的单组重读都新：给新旧两个组记一次版本，让迟到的单组响应作废。
        this.bumpVersionGroupStamp(next[key]?.group_id);
        this.bumpVersionGroupStamp(summary?.group_id);
        if (summary && Number(summary.member_count) >= 2) next[key] = summary;
        else delete next[key];
      }
      this.versionGroups = next;
    },
    bumpVersionGroupStamp(groupID) {
      const id = Number(groupID || 0);
      if (!id) return 0;
      if (!this._versionGroupStamps) this._versionGroupStamps = new Map();
      const stamp = (this._versionGroupStamps.get(id) || 0) + 1;
      this._versionGroupStamps.set(id, stamp);
      return stamp;
    },
    // 片库里就地改了某个文件（已看、进度、评分、播放）后，若它属于本页展示的某个组，单独重读该组汇总。
    refreshVersionGroupForVideo(videoID) {
      const id = Number(videoID || 0);
      if (!id || !this.versionCollapseActive()) return Promise.resolve(false);
      const summary = Object.values(this.versionGroups)
        .find(item => (item?.members || []).some(member => Number(member.video_id) === id));
      return summary ? this.reloadVersionGroupSummary(Number(summary.group_id)) : Promise.resolve(false);
    },
    // 只采用这个组最新一次请求的结果；期间整页数据或更新的请求到了，这次响应作废。
    async reloadVersionGroupSummary(groupID) {
      const stamp = this.bumpVersionGroupStamp(groupID);
      let detail = null;
      try {
        detail = await GetVersionGroup(groupID);
      } catch (err) {
        if (!isVersionGroupNotFound(err)) return false;
      }
      if (this._versionGroupStamps.get(groupID) !== stamp) return false;
      const next = { ...this.versionGroups };
      for (const [key, summary] of Object.entries(next)) {
        if (Number(summary?.group_id) !== groupID) continue;
        const stillShown = detail && Number(detail.member_count) >= 2 &&
          (detail.members || []).some(member => String(member.video_id) === key);
        if (stillShown) next[key] = detail;
        else delete next[key];
      }
      this.versionGroups = next;
      return true;
    },
    versionGroupFor(video) {
      if (!this.versionCollapseActive()) return null;
      return this.versionGroups[String(video?.id)] || null;
    },
    isVersionGroupExpanded(video) {
      const summary = this.versionGroupFor(video);
      return !!summary && this.expandedVersionGroupIDs.includes(Number(summary.group_id));
    },
    toggleVersionGroup(video) {
      const summary = this.versionGroupFor(video);
      if (!summary) return;
      const id = Number(summary.group_id);
      this.expandedVersionGroupIDs = this.expandedVersionGroupIDs.includes(id)
        ? this.expandedVersionGroupIDs.filter(value => value !== id)
        : [...this.expandedVersionGroupIDs, id];
    },
    // 并入虚拟列表 itemVersion：组内容或展开状态变了，这一行要重新测量。
    versionVisualKey(video) {
      const summary = this.versionGroupFor(video);
      if (!summary) return null;
      return [summary.group_id, summary.revision, summary.member_count, summary.watched_count, summary.rated_count, this.isVersionGroupExpanded(video)];
    },
    versionExtraHeight(video) {
      if (!this.isVersionGroupExpanded(video)) return 0;
      return 48 + 56 * Number(this.versionGroupFor(video)?.member_count || 0);
    },
    // 组写操作成功后按现有原地刷新重取（同条件不回顶）。卡片数会变，片库总数（按卡片计）也要重取。
    afterVersionGroupMutation() {
      if (typeof this.invalidateLibraryTotal === 'function') this.invalidateLibraryTotal();
      return this.resetAndLoadVideos();
    },
    reportVersionGroupError(err) {
      if (isVersionGroupNotFound(err)) {
        notifyError('版本组已解散，列表已刷新');
        this.afterVersionGroupMutation();
        return;
      }
      if (isVersionGroupConflict(err)) {
        notifyError('版本组已被其他操作修改，列表已刷新，请再试一次');
        this.afterVersionGroupMutation();
        return;
      }
      notifyError(versionGroupErrorText(err));
    },
    async runVersionGroupWrite(operation) {
      if (this.versionGroupBusy) return false;
      this.versionGroupBusy = true;
      try {
        await operation();
        await this.afterVersionGroupMutation();
        return true;
      } catch (err) {
        this.reportVersionGroupError(err);
        return false;
      } finally {
        this.versionGroupBusy = false;
      }
    },
    // 播放所选具体文件：直接走既有 PlayVideo(id)，按该文件校验与计数。
    playVersionMember(videoID) {
      return this.playVideo(Number(videoID));
    },
    async previewVersionMember(video, videoID) {
      if (Number(videoID) === Number(video?.id)) return this.openPreview(video);
      try {
        const [member] = (await GetVideosByIDs([Number(videoID)])) || [];
        if (member) return this.openPreview(member);
        notifyError('这个版本已不可用，列表将刷新');
        this.afterVersionGroupMutation();
      } catch (err) {
        notifyError('打开预览失败: ' + err);
      }
      return undefined;
    },
    setPrimaryVersion(video, videoID) {
      const summary = this.versionGroupFor(video);
      if (!summary) return Promise.resolve(false);
      const order = primaryFirstOrder(summary.members, videoID);
      return this.runVersionGroupWrite(() => ReorderVersionMembers(summary.group_id, summary.revision, order));
    },
    async removeVersionFromGroup(video, videoID) {
      const summary = this.versionGroupFor(video);
      if (!summary) return false;
      const member = (summary.members || []).find(item => Number(item.video_id) === Number(videoID));
      const dissolves = Number(summary.member_count || 0) <= 2;
      const ok = await confirmAction({
        title: '移出版本组',
        message: `把「${versionMemberTitle(member || { video_id: videoID })}」移出版本组？只解除关系，不删除文件。` +
          (dissolves ? '这个组只剩 2 个版本，移出后版本组将解散，两个文件恢复为单独的卡片。' : ''),
        confirmText: '移出'
      });
      if (!ok) return false;
      return this.runVersionGroupWrite(async () => {
        const result = await RemoveVersionMember(summary.group_id, summary.revision, Number(videoID));
        notify(result ? '已移出版本组，文件未删除' : '已移出；组内不足两个版本，版本组已解散');
      });
    },
    openVersionGroupDialogFor(video) {
      const summary = this.versionGroupFor(video);
      this.versionGroupDialog = {
        show: true,
        groupId: Number(summary?.group_id || 0),
        videoId: Number(video?.id || 0),
        videoTitle: String(video?.display_title || video?.name || '')
      };
    },
    closeVersionGroupDialog() {
      this.versionGroupDialog = { show: false, groupId: 0, videoId: 0, videoTitle: '' };
    },
    // 批量栏「合并为版本组」：按选择顺序成为 1..N。选中项已有组时，仅当冲突项都属于同一组，
    // 才提供「加入该组」把其余视频追加进去；冲突跨多个组时只报错。
    async mergeSelectedAsVersionGroup() {
      const ids = [...new Set(this.selectedVideoIds.map(Number).filter(Boolean))];
      if (ids.length < 2 || ids.length > VERSION_GROUP_MAX_MEMBERS) {
        notifyError(`合并为版本组需要选择 2–${VERSION_GROUP_MAX_MEMBERS} 个视频`);
        return false;
      }
      if (this.versionGroupBusy) return false;
      this.versionGroupBusy = true;
      try {
        const group = await CreateVersionGroup(ids, '');
        notifySuccess(`已合并为版本组（${group?.member_count || ids.length} 个版本），可在行菜单「管理版本组…」里设标签`);
        this.selectedVideoIds = [];
        await this.afterVersionGroupMutation();
        return true;
      } catch (err) {
        const conflict = parseVersionMemberConflict(err);
        if (conflict && conflict.groupIDs.length === 1) return this.offerJoinVersionGroup(ids, conflict);
        if (conflict) {
          notifyError('选中的视频分属多个版本组，无法合并；请先在「管理版本组…」里调整');
          return false;
        }
        this.reportVersionGroupError(err);
        return false;
      } finally {
        this.versionGroupBusy = false;
      }
    },
    async offerJoinVersionGroup(ids, conflict) {
      const rest = ids.filter(id => !conflict.videoIDs.includes(id));
      if (rest.length === 0) {
        notify('选中的视频已在同一个版本组里');
        return false;
      }
      const ok = await confirmAction({
        title: '加入已有版本组',
        message: `选中的视频里有 ${conflict.videoIDs.length} 个已在一个版本组中。把其余 ${rest.length} 个加入该组？`,
        confirmText: '加入该组'
      });
      if (!ok) return false;
      try {
        const group = await GetVersionGroup(conflict.groupIDs[0]);
        await AddVersionMembers(group.group_id, group.revision, rest);
        notifySuccess(`已加入版本组（共 ${group.member_count + rest.length} 个版本）`);
        this.selectedVideoIds = [];
        await this.afterVersionGroupMutation();
        return true;
      } catch (err) {
        this.reportVersionGroupError(err);
        return false;
      }
    }
  }
};
