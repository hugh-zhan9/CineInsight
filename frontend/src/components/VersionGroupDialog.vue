<template>
  <BaseModal class="version-dialog" data-test="version-dialog" @close="$emit('close')">
    <div class="version-dialog__header">
      <h3>{{ group ? '管理版本组' : '加入版本组' }}</h3>
      <button type="button" class="version-dialog__close" aria-label="关闭" @click="$emit('close')">✕</button>
    </div>
    <p v-if="error" class="version-dialog__error" role="alert" data-test="version-dialog-error">{{ error }}</p>
    <div v-if="loading" class="version-dialog__placeholder">正在读取……</div>

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
              <span v-if="index === 0" class="version-dialog__primary">主版本</span>
              <span :title="member.name">{{ memberTitle(member) }}</span>
            </div>
            <div class="help-text">{{ memberFacts(member) }}</div>
          </div>
          <input v-model="draftLabels[member.video_id]" type="text" maxlength="40" class="text-input version-dialog__label" placeholder="标签，如 原版 / 高清版" :data-test="`version-dialog-label-${member.video_id}`" />
          <div class="version-dialog__member-actions">
            <button type="button" class="row-btn" :disabled="busy || index === 0" aria-label="上移" :data-test="`version-dialog-up-${member.video_id}`" @click="move(index, -1)">↑</button>
            <button type="button" class="row-btn" :disabled="busy || index === group.members.length - 1" aria-label="下移" @click="move(index, 1)">↓</button>
            <button type="button" class="row-btn" :disabled="busy" :data-test="`version-dialog-remove-${member.video_id}`" @click="remove(member)">移出</button>
          </div>
        </li>
      </ol>
      <div class="modal-actions">
        <button type="button" class="btn-danger" :disabled="busy" data-test="version-dialog-dissolve" @click="dissolve">解散版本组</button>
        <div class="version-dialog__spacer"></div>
        <button type="button" class="btn-secondary" @click="$emit('close')">关闭</button>
        <button type="button" class="btn-primary" :disabled="busy || !dirty" data-test="version-dialog-save" @click="save">保存标题与标签</button>
      </div>
    </template>

    <!-- 视频还不在组里：可加入已有的组；新建要在片库多选后用批量栏「合并为版本组」。 -->
    <template v-else>
      <p class="help-text">「{{ videoTitle || `视频 #${videoId}` }}」还不在任何版本组里。可以把它加入下面已有的组；要新建，请在片库里多选 2 个以上视频后点「合并为版本组」。</p>
      <div v-if="!candidates.length" class="version-dialog__placeholder" data-test="version-dialog-empty">还没有版本组。</div>
      <ul v-else class="version-dialog__groups">
        <li v-for="candidate in candidates" :key="candidate.group_id" class="version-dialog__group">
          <span class="version-dialog__group-title">{{ groupTitle(candidate) }}</span>
          <span class="help-text">{{ candidate.member_count }} 个版本</span>
          <button type="button" class="btn-secondary btn-compact" :disabled="busy" :data-test="`version-dialog-join-${candidate.group_id}`" @click="join(candidate)">加入</button>
        </li>
      </ul>
      <div class="modal-actions">
        <button v-if="nextCursorID" type="button" class="btn-secondary" :disabled="busy" @click="loadCandidates(true)">加载更多</button>
        <div class="version-dialog__spacer"></div>
        <button type="button" class="btn-secondary" @click="$emit('close')">关闭</button>
      </div>
    </template>
  </BaseModal>
</template>

<script>
import { AddVersionMembers, DissolveVersionGroup, GetVersionGroup, ListVersionGroups, RemoveVersionMember, ReorderVersionMembers, UpdateVersionGroup } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import { confirmAction, notify, notifySuccess } from '../utils/feedback.js';
import { isVersionGroupConflict, isVersionGroupNotFound, moveVersionMember, parseVersionMemberConflict, versionGroupErrorText, versionGroupTitle, versionMemberFacts, versionMemberTitle } from '../utils/versionGroups.js';

// 版本组管理弹窗（D-MW-VERSIONS）：groupId>0 管理该组；否则为 videoId 选择一个已有组加入。
// 每次写操作都带当前 revision；冲突时重读并提示，不覆盖别处的修改。成功后 emit changed，
// 片库页据此原地刷新。
export default {
  name: 'VersionGroupDialog',
  components: { BaseModal },
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
.version-dialog__header { display: flex; align-items: center; gap: 8px; }
.version-dialog__header h3 { margin: 0; flex: 1; }
.version-dialog__close { border: none; background: none; color: var(--text-muted); cursor: pointer; font-size: 14px; }
.version-dialog__placeholder { padding: 16px 0; color: var(--text-muted); }
.version-dialog__field { display: flex; flex-direction: column; gap: 4px; margin: 12px 0; font-size: 12px; }
.version-dialog__members, .version-dialog__groups { list-style: none; margin: 8px 0; padding: 0; max-height: 50vh; overflow-y: auto; }
.version-dialog__member, .version-dialog__group { display: flex; align-items: center; gap: 8px; padding: 8px 0; border-top: 1px solid var(--border-color); flex-wrap: wrap; }
.version-dialog__member-info { flex: 1; min-width: 180px; }
.version-dialog__member-name { display: flex; gap: 6px; align-items: center; font-weight: 600; overflow: hidden; }
.version-dialog__primary { padding: 0 6px; border-radius: 4px; background: var(--accent-soft); color: var(--accent-text); font-size: 11px; white-space: nowrap; }
.version-dialog__label { width: 160px; }
.version-dialog__member-actions { display: flex; gap: 4px; }
.version-dialog__group-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.version-dialog__spacer { flex: 1; }
.version-dialog__error { margin: 8px 0; color: var(--danger-text); font-size: 12px; }
</style>
