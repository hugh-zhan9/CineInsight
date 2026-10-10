<template>
  <BaseModal class="version-panel" data-test="version-panel" @close="onEscape">
    <div class="version-panel__header">
      <h3>版本组</h3>
      <div class="merge-type-switch" role="group" aria-label="版本组分区">
        <button type="button" :class="{ active: tab === 'suggestions' }" data-test="version-panel-tab-suggestions" @click="tab = 'suggestions'">建议（{{ suggestions.length }}）</button>
        <button type="button" :class="{ active: tab === 'groups' }" data-test="version-panel-tab-groups" @click="tab = 'groups'">已有的组</button>
      </div>
      <div class="version-panel__spacer"></div>
      <button type="button" class="version-panel__close" aria-label="关闭" @click="$emit('close')">✕</button>
    </div>
    <p v-if="error" class="version-panel__error" role="alert" data-test="version-panel-error">{{ error }}</p>

    <div v-if="tab === 'suggestions'" class="version-panel__body">
      <p class="help-text">建议来自清理中心已检测到的同源配对：时长相近（较短的不少于较长的 90%），截取片段不在此列。只有你点「建立版本组」才会建组。</p>
      <div v-if="loadingSuggestions" class="version-panel__placeholder">正在读取建议……</div>
      <div v-else-if="!suggestions.length" class="version-panel__placeholder" data-test="version-panel-no-suggestions">暂无建议。</div>
      <ul v-else class="version-panel__list">
        <li v-for="suggestion in suggestions" :key="suggestion.video_ids.join('-')" class="version-panel__item" data-test="version-panel-suggestion">
          <div class="version-panel__item-main">
            <div v-for="(member, index) in suggestion.members" :key="member.video_id" class="version-panel__member">
              <span v-if="index === 0" class="version-panel__primary">主版本</span>
              <span class="version-panel__member-title" :title="member.name">{{ memberTitle(member) }}</span>
              <span class="help-text">{{ memberFacts(member) }}</span>
            </div>
          </div>
          <div class="version-panel__actions">
            <button type="button" class="btn-primary btn-compact" :disabled="busy" data-test="version-panel-accept" @click="accept(suggestion)">建立版本组</button>
            <button type="button" class="btn-secondary btn-compact" :disabled="busy" data-test="version-panel-dismiss" @click="dismiss(suggestion)">忽略</button>
          </div>
        </li>
      </ul>
    </div>

    <div v-else class="version-panel__body">
      <div v-if="loadingGroups" class="version-panel__placeholder">正在读取版本组……</div>
      <div v-else-if="!groups.length" class="version-panel__placeholder" data-test="version-panel-no-groups">还没有版本组。在片库里多选视频后点「合并为版本组」，或接受上面的建议。</div>
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
            <button type="button" class="btn-secondary btn-compact" :data-test="`version-panel-manage-${group.group_id}`" @click="managedGroupID = group.group_id">管理</button>
          </div>
        </li>
      </ul>
      <button v-if="nextCursorID" type="button" class="btn-secondary" :disabled="loadingGroups" data-test="version-panel-more" @click="loadGroups(true)">加载更多</button>
    </div>

    <VersionGroupDialog
      v-if="managedGroupID"
      :group-id="managedGroupID"
      @close="closeManaged"
      @changed="$emit('changed')"
    />
  </BaseModal>
</template>

<script>
import { CreateVersionGroup, DismissVersionGroupSuggestion, ListVersionGroupSuggestions, ListVersionGroups } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import VersionGroupDialog from './VersionGroupDialog.vue';
import { notify, notifySuccess } from '../utils/feedback.js';
import { versionGroupErrorText, versionGroupTitle, versionMemberFacts, versionMemberTitle } from '../utils/versionGroups.js';

// 管理菜单「版本组」面板（D-MW-VERSIONS）：建议（建立 / 忽略）与已有组（逐个打开管理弹窗）。
// 建议从不自动建组；任何写操作成功后 emit changed，片库页原地刷新。
export default {
  name: 'VersionGroupPanel',
  components: { BaseModal, VersionGroupDialog },
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
.version-panel__header { display: flex; align-items: center; gap: 12px; }
.version-panel__header h3 { margin: 0; }
.version-panel__spacer { flex: 1; }
.version-panel__close { border: none; background: none; color: var(--text-muted); cursor: pointer; font-size: 14px; }
.version-panel__body { margin-top: 12px; max-height: 60vh; overflow-y: auto; }
.version-panel__placeholder { padding: 16px 0; color: var(--text-muted); }
.version-panel__list { list-style: none; margin: 8px 0; padding: 0; }
.version-panel__item { display: flex; align-items: center; gap: 12px; padding: 10px 0; border-top: 1px solid var(--border-color); flex-wrap: wrap; }
.version-panel__item-main { flex: 1; min-width: 220px; }
.version-panel__member { display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; font-size: 12px; }
.version-panel__member-title, .version-panel__group-title { font-weight: 600; }
.version-panel__primary { padding: 0 6px; border-radius: 4px; background: var(--accent-soft); color: var(--accent-text); font-size: 11px; }
.version-panel__actions { display: flex; gap: 6px; }
.version-panel__error { margin: 8px 0; color: var(--danger-text); font-size: 12px; }
</style>
