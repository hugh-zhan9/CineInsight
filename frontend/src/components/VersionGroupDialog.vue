<template>
  <DialogShell :title="group ? '管理版本组' : '加入版本组'" size="md" class="version-dialog" data-test="version-dialog" @close="$emit('close')">
    <p v-if="error" class="version-dialog__error" role="alert" data-test="version-dialog-error">{{ error }}</p>
    <SkeletonBlock v-if="loading" class="version-dialog__placeholder" :lines="3" />

    <!-- 管理一个组：标题、标签、顺序（第 1 位是主版本）、移出与解散。 -->
    <template v-else-if="group">
      <label class="version-dialog__field">
        <span>组标题（留空显示主版本标题）</span>
        <input v-model="draftTitle" type="text" maxlength="200" class="text-input" :placeholder="fallbackTitle" data-test="version-dialog-title" />
      </label>
      <p v-if="group.deleted_member_count" class="help-text" data-test="version-dialog-deleted">
        另有 {{ group.deleted_member_count }} 个版本在回收站里，恢复后自动回到组内。
      </p>
      <ol class="version-dialog__members">
        <li v-for="(member, index) in group.members" :key="member.video_id" class="version-dialog__member" :data-test="`version-dialog-member-${member.video_id}`">
          <div class="version-dialog__member-info">
            <div class="version-dialog__member-name">
              <AppBadge v-if="index === 0" tone="accent">主版本</AppBadge>
              <span :title="member.name">{{ memberTitle(member) }}</span>
            </div>
            <div class="help-text">{{ memberFacts(member) }}</div>
          </div>
          <input v-model="draftLabels[member.video_id]" type="text" maxlength="40" class="text-input version-dialog__label" placeholder="标签，如 原版 / 高清版" :data-test="`version-dialog-label-${member.video_id}`" />
          <div class="version-dialog__member-actions">
            <button type="button" class="btn-icon btn--sm" :disabled="busy || index === 0" aria-label="上移" :data-test="`version-dialog-up-${member.video_id}`" @click="move(index, -1)"><AppIcon name="chevron-up" :size="16" /></button>
            <button type="button" class="btn-icon btn--sm" :disabled="busy || index === group.members.length - 1" aria-label="下移" @click="move(index, 1)"><AppIcon name="chevron-down" :size="16" /></button>
            <button type="button" class="btn btn--secondary btn--sm" :disabled="busy" :data-test="`version-dialog-remove-${member.video_id}`" @click="remove(member)">移出</button>
          </div>
        </li>
      </ol>
    </template>

    <!-- 视频还不在组里：可加入已有的组；新建要在片库多选后用批量栏「合并为版本组」。 -->
    <template v-else>
      <p class="help-text">「{{ videoTitle || `视频 #${videoId}` }}」还不在任何版本组里。可以把它加入下面已有的组；要新建，请在片库里多选 2 个以上视频后点「合并为版本组」。</p>
      <EmptyState v-if="!candidates.length" title="还没有版本组" message="在片库里多选 2 个以上视频后点「合并为版本组」即可新建。" data-test="version-dialog-empty" />
      <ul v-else class="version-dialog__groups">
        <li v-for="candidate in candidates" :key="candidate.group_id" class="version-dialog__group">
          <span class="version-dialog__group-title">{{ groupTitle(candidate) }}</span>
          <span class="help-text">{{ candidate.member_count }} 个版本</span>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="busy" :data-test="`version-dialog-join-${candidate.group_id}`" @click="join(candidate)">加入</button>
        </li>
      </ul>
      <button v-if="nextCursorID" type="button" class="btn btn--secondary btn--sm version-dialog__more" :disabled="busy" @click="loadCandidates(true)">加载更多</button>
    </template>

    <template v-if="!loading && group" #footer>
      <button type="button" class="btn btn--danger" :disabled="busy" data-test="version-dialog-dissolve" @click="dissolve">解散版本组</button>
      <button type="button" class="btn btn--secondary" @click="$emit('close')">关闭</button>
      <button type="button" class="btn btn--primary" :disabled="busy || !dirty" data-test="version-dialog-save" @click="save">保存标题与标签</button>
    </template>
    <template v-else-if="!loading" #footer>
      <button type="button" class="btn btn--secondary" @click="$emit('close')">关闭</button>
    </template>
  </DialogShell>
</template>

<script>
import { AddVersionMembers, DissolveVersionGroup, GetVersionGroup, ListVersionGroups, RemoveVersionMember, ReorderVersionMembers, UpdateVersionGroup } from '../../wailsjs/go/main/App';
import AppBadge from './ui/AppBadge.vue';
import AppIcon from './ui/AppIcon.vue';
import DialogShell from './ui/DialogShell.vue';
import EmptyState from './ui/EmptyState.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import { confirmAction, notify, notifySuccess } from '../utils/feedback.js';
import { isVersionGroupConflict, isVersionGroupNotFound, moveVersionMember, parseVersionMemberConflict, versionGroupErrorText, versionGroupTitle, versionMemberFacts, versionMemberTitle } from '../utils/versionGroups.js';

// 版本组管理弹窗（D-MW-VERSIONS）：groupId>0 管理该组；否则为 videoId 选择一个已有组加入。
// 每次写操作都带当前 revision；冲突时重读并提示，不覆盖别处的修改。成功后 emit changed，
// 片库页据此原地刷新。
export default {
  name: 'VersionGroupDialog',
  components: { AppBadge, AppIcon, DialogShell, EmptyState, SkeletonBlock },
  props: {
    groupId: { type: Number, default: 0 },
    videoId: { type: Number, default: 0 },
    videoTitle: { type: String, default: '' }
  },
  emits: ['close', 'changed'],
  data() {
    return {
      activeGroupID: this.groupId,
      group: null,
      loading: false,
      busy: false,
      error: '',
      draftTitle: '',
      draftLabels: {},
      candidates: [],
      nextCursorID: 0
    };
  },
  computed: {
    fallbackTitle() {
      return versionGroupTitle({ members: this.group?.members || [] });
    },
    dirty() {
      if (!this.group) return false;
      if (this.draftTitle.trim() !== String(this.group.title || '')) return true;
      return this.group.members.some(member => String(this.draftLabels[member.video_id] || '').trim() !== String(member.label || ''));
    }
  },
  mounted() {
    this.reload();
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
    async reload() {
      if (this.activeGroupID) return this.loadGroup(this.activeGroupID);
      this.group = null;
      return this.loadCandidates(false);
    },
    async loadGroup(groupID) {
      this.loading = true;
      try {
        this.applyGroup(await GetVersionGroup(groupID));
      } catch (err) {
        this.group = null;
        this.error = versionGroupErrorText(err);
      } finally {
        this.loading = false;
      }
    },
    // keepDrafts：重排或移出后保留尚未保存的标题与标签输入。
    applyGroup(group, keepDrafts = false) {
      const previous = this.draftLabels;
      this.group = group;
      if (!keepDrafts) this.draftTitle = group?.title || '';
      const labels = {};
      for (const member of group?.members || []) {
        labels[member.video_id] = keepDrafts && previous[member.video_id] !== undefined ? previous[member.video_id] : (member.label || '');
      }
      this.draftLabels = labels;
    },
    async loadCandidates(more) {
      this.loading = !more;
      try {
        const page = await ListVersionGroups(more ? this.nextCursorID : 0, 50);
        const items = page?.items || [];
        this.candidates = more ? [...this.candidates, ...items] : items;
        this.nextCursorID = page?.has_more ? Number(page.next_cursor_id || 0) : 0;
      } catch (err) {
        this.error = versionGroupErrorText(err);
      } finally {
        this.loading = false;
      }
    },
    // 统一执行一次写操作：成功 emit changed 并返回 { ok, result }；revision 冲突时重读该组。
    async run(operation) {
      this.busy = true;
      this.error = '';
      try {
        const result = await operation();
        this.$emit('changed');
        return { ok: true, result };
      } catch (err) {
        // 组已不存在（被解散，或只剩一个版本而被清理）：重读也没用，按已解散关闭并让片库刷新。
        if (isVersionGroupNotFound(err) && this.group) {
          notify('版本组已解散');
          this.$emit('changed');
          this.$emit('close');
          return { ok: false, error: err };
        }
        if (isVersionGroupConflict(err)) {
          await this.reload();
          this.error = '版本组已被其他操作修改，已重新读取，请再试一次。';
        } else {
          this.error = versionGroupErrorText(err);
        }
        return { ok: false, error: err };
      } finally {
        this.busy = false;
      }
    },
    async move(index, delta) {
      const group = this.group;
      const order = moveVersionMember(group.members.map(member => member.video_id), index, delta);
      const outcome = await this.run(() => ReorderVersionMembers(group.group_id, group.revision, order));
      if (outcome.ok && outcome.result) this.applyGroup(outcome.result, true);
    },
    async remove(member) {
      const group = this.group;
      const total = group.members.length + Number(group.deleted_member_count || 0);
      const ok = await confirmAction({
        title: '移出版本组',
        message: `把「${versionMemberTitle(member)}」移出版本组？只解除关系，不删除文件。${total <= 2 ? '移出后组内不足两个版本，版本组会解散。' : ''}`,
        confirmText: '移出'
      });
      if (!ok) return;
      const outcome = await this.run(() => RemoveVersionMember(group.group_id, group.revision, member.video_id));
      if (!outcome.ok) return;
      if (outcome.result) {
        this.applyGroup(outcome.result, true);
        return;
      }
      notify('版本组已解散');
      this.$emit('close');
    },
    async dissolve() {
      const group = this.group;
      const ok = await confirmAction({ title: '解散版本组', message: '解散后各版本恢复为独立的卡片，不删除任何文件。', confirmText: '解散', danger: true });
      if (!ok) return;
      const outcome = await this.run(() => DissolveVersionGroup(group.group_id, group.revision));
      if (!outcome.ok) return;
      notify('版本组已解散');
      this.$emit('close');
    },
    async save() {
      const group = this.group;
      const labels = {};
      for (const member of group.members) labels[member.video_id] = String(this.draftLabels[member.video_id] || '').trim();
      const outcome = await this.run(() => UpdateVersionGroup(group.group_id, group.revision, this.draftTitle.trim(), labels));
      if (outcome.ok && outcome.result) {
        this.applyGroup(outcome.result);
        notifySuccess('已保存');
      }
    },
    async join(candidate) {
      const outcome = await this.run(() => AddVersionMembers(candidate.group_id, candidate.revision, [this.videoId]));
      if (outcome.ok) {
        this.activeGroupID = Number(candidate.group_id);
        this.applyGroup(outcome.result);
        notifySuccess('已加入版本组');
        return;
      }
      // 视频其实已在某个组里（例如关闭合并时打开的菜单）：直接打开那个组。
      const conflict = parseVersionMemberConflict(outcome.error);
      if (conflict && conflict.groupIDs.length === 1) {
        this.activeGroupID = conflict.groupIDs[0];
        this.error = '';
        await this.reload();
        notify('这个视频已在一个版本组里，已打开该组');
      }
    }
  }
};
</script>

<style scoped>
.version-dialog__placeholder { padding: var(--space-4) 0; }
.version-dialog__field { display: flex; flex-direction: column; gap: var(--space-1); margin: 0 0 var(--space-3); color: var(--text-secondary); font-size: var(--fs-caption); }
.version-dialog__members, .version-dialog__groups { list-style: none; margin: var(--space-2) 0; padding: 0; max-height: 50vh; overflow-y: auto; }
.version-dialog__member, .version-dialog__group { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2) 0; border-top: 1px solid var(--hairline-soft); flex-wrap: wrap; }
.version-dialog__member-info { flex: 1; min-width: 180px; }
.version-dialog__member-name { display: flex; gap: 6px; align-items: center; font-weight: var(--fw-semibold); overflow: hidden; }
.version-dialog__label { width: 160px; }
.version-dialog__member-actions { display: flex; align-items: center; gap: var(--space-1); }
.version-dialog__group-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.version-dialog__more { margin-top: var(--space-2); }
.version-dialog__error { margin: 0 0 var(--space-3); color: var(--danger-text); font-size: var(--fs-caption); }
</style>
