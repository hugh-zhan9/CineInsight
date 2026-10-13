<template>
  <DialogShell title="版本组" size="lg" class="version-panel" data-test="version-panel" @close="onEscape">
    <!-- 两个分区（D-UD-06 AppTabs），每个页签带 version-panel-tab-* 钩子；建议的条数写在徽标里。 -->
    <AppTabs v-model="tab" class="version-panel__tabs" :tabs="panelTabs" aria-label="版本组分区" />
    <p v-if="error" class="version-panel__error" role="alert" data-test="version-panel-error">{{ error }}</p>

    <div v-if="tab === 'suggestions'" class="version-panel__body">
      <p class="help-text">建议来自清理中心已检测到的同源配对：时长相近（较短的不少于较长的 90%），截取片段不在此列。只有你点「建立版本组」才会建组。</p>
      <SkeletonBlock v-if="loadingSuggestions" class="version-panel__placeholder" :lines="3" />
      <EmptyState v-else-if="!suggestions.length" title="暂无建议" message="清理中心检测到时长相近的同源配对后，会在这里给出建议。" data-test="version-panel-no-suggestions" />
      <ul v-else class="version-panel__list">
        <li v-for="suggestion in suggestions" :key="suggestion.video_ids.join('-')" class="version-panel__item" data-test="version-panel-suggestion">
          <div class="version-panel__item-main">
            <div v-for="(member, index) in suggestion.members" :key="member.video_id" class="version-panel__member">
              <AppBadge v-if="index === 0" tone="accent">主版本</AppBadge>
              <span class="version-panel__member-title" :title="member.name">{{ memberTitle(member) }}</span>
              <span class="help-text">{{ memberFacts(member) }}</span>
            </div>
          </div>
          <div class="version-panel__actions">
            <button type="button" class="btn btn--secondary btn--sm" :disabled="busy" data-test="version-panel-dismiss" @click="dismiss(suggestion)">忽略</button>
            <button type="button" class="btn btn--primary btn--sm" :disabled="busy" data-test="version-panel-accept" @click="accept(suggestion)">建立版本组</button>
          </div>
        </li>
      </ul>
    </div>

    <div v-else class="version-panel__body">
      <SkeletonBlock v-if="loadingGroups" class="version-panel__placeholder" :lines="3" />
      <EmptyState v-else-if="!groups.length" title="还没有版本组" message="在片库里多选视频后点「合并为版本组」，或接受建议。" data-test="version-panel-no-groups" />
      <ul v-else class="version-panel__list">
        <li v-for="group in groups" :key="group.group_id" class="version-panel__item" :data-test="`version-panel-group-${group.group_id}`">
          <div class="version-panel__item-main">
            <div class="version-panel__group-title">{{ groupTitle(group) }}</div>
            <div class="help-text">
              {{ group.member_count }} 个版本<span v-if="group.deleted_member_count"> · {{ group.deleted_member_count }} 个在回收站</span>
              · {{ group.members.map(member => member.label || memberTitle(member)).join(' / ') }}
            </div>
          </div>
          <div class="version-panel__actions">
            <button type="button" class="btn btn--secondary btn--sm" :data-test="`version-panel-manage-${group.group_id}`" @click="managedGroupID = group.group_id">管理</button>
          </div>
        </li>
      </ul>
      <button v-if="nextCursorID" type="button" class="btn btn--secondary btn--sm" :disabled="loadingGroups" data-test="version-panel-more" @click="loadGroups(true)">加载更多</button>
    </div>

    <VersionGroupDialog
      v-if="managedGroupID"
      :group-id="managedGroupID"
      @close="closeManaged"
      @changed="$emit('changed')"
    />
  </DialogShell>
</template>

<script>
import { CreateVersionGroup, DismissVersionGroupSuggestion, ListVersionGroupSuggestions, ListVersionGroups } from '../../wailsjs/go/main/App';
import AppBadge from './ui/AppBadge.vue';
import AppTabs from './ui/AppTabs.vue';
import DialogShell from './ui/DialogShell.vue';
import EmptyState from './ui/EmptyState.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import VersionGroupDialog from './VersionGroupDialog.vue';
import { notify, notifySuccess } from '../utils/feedback.js';
import { versionGroupErrorText, versionGroupTitle, versionMemberFacts, versionMemberTitle } from '../utils/versionGroups.js';

// 管理菜单「版本组」面板（D-MW-VERSIONS）：建议（建立 / 忽略）与已有组（逐个打开管理弹窗）。
// 建议从不自动建组；任何写操作成功后 emit changed，片库页原地刷新。
export default {
  name: 'VersionGroupPanel',
  components: { AppBadge, AppTabs, DialogShell, EmptyState, SkeletonBlock, VersionGroupDialog },
  emits: ['close', 'changed'],
  data() {
    return {
      tab: 'suggestions',
      suggestions: [],
      groups: [],
      nextCursorID: 0,
      loadingSuggestions: false,
      loadingGroups: false,
      busy: false,
      error: '',
      managedGroupID: 0
    };
  },
  computed: {
    panelTabs() {
      return [
        { key: 'suggestions', label: '建议', count: String(this.suggestions.length), dataTest: 'version-panel-tab-suggestions' },
        { key: 'groups', label: '已有的组', dataTest: 'version-panel-tab-groups' }
      ];
    }
  },
  mounted() {
    this.loadSuggestions();
    this.loadGroups(false);
  },
  methods: {
    memberTitle(member) {
      return versionMemberTitle(member);
    },
    memberFacts(member) {
      return versionMemberFacts(member);
    },
    groupTitle(group) {
      return versionGroupTitle(group);
    },
    // 嵌套的管理弹窗开着时，Esc 只关它，不连面板一起关。
    onEscape() {
      if (this.managedGroupID) {
        this.managedGroupID = 0;
        return;
      }
      this.$emit('close');
    },
    closeManaged() {
      this.managedGroupID = 0;
      this.loadGroups(false);
      this.loadSuggestions();
    },
    async loadSuggestions() {
      this.loadingSuggestions = true;
      try {
        this.suggestions = (await ListVersionGroupSuggestions(50)) || [];
      } catch (err) {
        this.error = versionGroupErrorText(err);
      } finally {
        this.loadingSuggestions = false;
      }
    },
    async loadGroups(more) {
      this.loadingGroups = true;
      try {
        const page = await ListVersionGroups(more ? this.nextCursorID : 0, 50);
        const items = page?.items || [];
        this.groups = more ? [...this.groups, ...items] : items;
        this.nextCursorID = page?.has_more ? Number(page.next_cursor_id || 0) : 0;
      } catch (err) {
        this.error = versionGroupErrorText(err);
      } finally {
        this.loadingGroups = false;
      }
    },
    async accept(suggestion) {
      this.busy = true;
      this.error = '';
      try {
        const group = await CreateVersionGroup([...suggestion.video_ids], '');
        notifySuccess(`已建立版本组（${group?.member_count || suggestion.video_ids.length} 个版本）`);
        this.$emit('changed');
      } catch (err) {
        this.error = versionGroupErrorText(err);
      } finally {
        this.busy = false;
        this.loadSuggestions();
        this.loadGroups(false);
      }
    },
    async dismiss(suggestion) {
      this.busy = true;
      this.error = '';
      try {
        await DismissVersionGroupSuggestion([...suggestion.video_ids]);
        this.suggestions = this.suggestions.filter(item => item !== suggestion);
        notify('已忽略这条建议');
      } catch (err) {
        this.error = versionGroupErrorText(err);
      } finally {
        this.busy = false;
      }
    }
  }
};
</script>

<style scoped>
.version-panel__body { margin-top: var(--space-3); max-height: 60vh; overflow-y: auto; }
.version-panel__placeholder { padding: var(--space-4) 0; }
.version-panel__list { list-style: none; margin: var(--space-2) 0; padding: 0; }
.version-panel__item { display: flex; align-items: center; gap: var(--space-3); padding: 10px 0; border-top: 1px solid var(--hairline-soft); flex-wrap: wrap; }
.version-panel__item-main { flex: 1; min-width: 220px; }
.version-panel__member { display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; font-size: var(--fs-caption); }
.version-panel__member-title, .version-panel__group-title { font-weight: var(--fw-semibold); }
.version-panel__actions { display: flex; gap: 6px; }
.version-panel__error { margin: var(--space-2) 0; color: var(--danger-text); font-size: var(--fs-caption); }
</style>
