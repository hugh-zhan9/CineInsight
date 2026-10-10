<template>
  <section class="edit-section" data-test="edit-results">
    <h3>导出结果</h3>
    <ol class="edit-list">
      <li v-for="item in items" :key="item.id" class="edit-list__item" :data-test="`edit-result-${item.id}`">
        <div class="edit-list__row">
          <span class="edit-list__index">{{ item.seq }}</span>
          <span class="edit-list__name" :title="item.output_name">{{ item.output_name || `第 ${item.seq} 项` }}</span>
          <span class="edit-badge">{{ statusText(item) }}</span>
          <span v-if="item.status === 'running'" class="edit-progress"><i :style="{ width: `${percent(item)}%` }"></i></span>
          <span v-if="item.status === 'running'" class="help-text">{{ phaseText(item) }} {{ percent(item) }}%</span>
        </div>
        <p v-if="failureText(item)" class="edit-error" :data-test="`edit-result-error-${item.id}`">{{ failureText(item) }}</p>
        <p v-if="item.status === 'completed' && item.error_message" class="help-text">提示：{{ item.error_message }}</p>
        <p
          v-if="item.status === 'completed' && item.output_video_id && outputState[item.output_video_id] === 'missing'"
          class="help-text"
          :data-test="`edit-result-missing-${item.id}`"
        >成品已不在片库</p>
        <div v-if="item.status === 'completed' && item.output_video_id && outputState[item.output_video_id] === 'present'" class="edit-inline">
          <button type="button" class="btn-secondary btn-compact" :data-test="`edit-result-preview-${item.id}`" @click="$emit('open-video', item.output_video_id)">预览成品</button>
          <label class="edit-check">
            <input type="checkbox" :checked="Boolean(verified[item.id])" :data-test="`edit-result-confirm-${item.id}`" @change="setVerified(item.id, $event.target.checked)" />
            确认成品无误
          </label>
          <button
            v-if="verified[item.id]"
            type="button"
            class="btn-danger btn-compact"
            :data-test="`edit-result-cleanup-${item.id}`"
            @click="$emit('cleanup-sources', [...(item.source_video_ids || [])])"
          >清理原片…</button>
          <button
            v-if="canLinkVersion"
            type="button"
            class="btn-secondary btn-compact"
            :disabled="linking"
            :data-test="`edit-result-version-${item.id}`"
            @click="linkVersion(item)"
          >与原片建立版本组</button>
        </div>
      </li>
    </ol>
  </section>
</template>

<script>
import { AddVersionMembers, CreateVersionGroup, GetVersionGroup, GetVideosByIDs } from '../../../wailsjs/go/main/App';
import { confirmAction, notify, notifyError, notifySuccess } from '../../utils/feedback.js';
import { parseVersionMemberConflict, versionGroupErrorText } from '../../utils/versionGroups.js';
import { EDIT_ITEM_ERROR_LABELS, EDIT_PHASE_LABELS, editStatusLabel } from '../../utils/videoEdit.js';

// 导出结果（Q4、TC-23）：成品「预览成品」交给宿主打开详情抽屉；勾选「确认成品无误」之后才出现
// 「清理原片…」，它只把来源 ID 交给宿主走片库既有的删除确认与废纸篓流程——这里从不直接删除。
// 去片头与高清替换的成品可以与原片建立版本组（先确认；原片已在某个组里时提议把成品加进去）。
// 成品可能已被删除或进了回收站：先用 GetVideosByIDs 确认它还在片库，才给预览、确认与清理入口，
// 否则只提示「成品已不在片库」——不能让用户在没有成品的情况下去清理原片。
export default {
  name: 'EditResults',
  props: {
    project: { type: Object, required: true }
  },
  emits: ['open-video', 'cleanup-sources'],
  data() {
    // outputState：成品视频 ID → 'present' | 'missing'；还没查到的不给入口。
    return { verified: {}, linking: false, outputState: {}, outputToken: 0 };
  },
  computed: {
    items() {
      return this.project?.items || [];
    },
    outputIDs() {
      return this.items.filter(item => item.status === 'completed' && item.output_video_id).map(item => Number(item.output_video_id));
    },
    outputKey() {
      return this.outputIDs.join(',');
    },
    canLinkVersion() {
      return this.project?.kind === 'trim_intro' || this.project?.kind === 'hd_replace';
    }
  },
  watch: {
    outputKey: { immediate: true, handler() { this.loadOutputs(); } }
  },
  methods: {
    async loadOutputs() {
      const ids = this.outputIDs;
      const token = ++this.outputToken;
      if (!ids.length) return;
      try {
        const videos = await GetVideosByIDs(ids);
        if (token !== this.outputToken) return;
        const present = new Set((videos || []).map(video => Number(video.id)));
        const next = { ...this.outputState };
        for (const id of ids) next[id] = present.has(id) ? 'present' : 'missing';
        this.outputState = next;
      } catch {
        // 查不到就保持未知：不给清理入口，也不误报「已不在片库」。
      }
    },
    statusText(item) {
      return editStatusLabel(item.status);
    },
    phaseText(item) {
      return EDIT_PHASE_LABELS[item.phase] || '';
    },
    percent(item) {
      return Math.max(0, Math.min(100, Math.round(Number(item.progress || 0) * 100)));
    },
    failureText(item) {
      if (!['failed', 'cancelled', 'interrupted'].includes(item.status)) return '';
      const label = EDIT_ITEM_ERROR_LABELS[item.error_code] || editStatusLabel(item.status);
      return item.error_message ? `${label}：${item.error_message}` : label;
    },
    setVerified(itemID, checked) {
      this.verified = { ...this.verified, [itemID]: checked };
    },
    // 版本组里的「原片」：去片头取该项的来源，高清替换取长版（主时间线）。
    originVideoID(item) {
      if (this.project.kind === 'hd_replace') return Number(this.project.recipe?.hd_replace?.long_video_id || 0);
      return Number(item.source_video_ids?.[0] || 0);
    },
    async linkVersion(item) {
      const ids = [this.originVideoID(item), Number(item.output_video_id)];
      if (!ids[0] || !ids[1] || this.linking) return;
      const ok = await confirmAction({
        title: '与原片建立版本组',
        message: `把成品「${item.output_name || '成品'}」与原片合并为一个版本组？之后可在片库「管理版本组…」里设标签或移出。`,
        confirmText: '建立版本组'
      });
      if (!ok) return;
      this.linking = true;
      try {
        await CreateVersionGroup(ids, '');
        notifySuccess('已与原片建立版本组');
      } catch (err) {
        const conflict = parseVersionMemberConflict(err);
        if (conflict && conflict.groupIDs.length === 1) await this.offerJoin(ids, conflict);
        else if (conflict) notifyError('原片与成品分属不同的版本组，无法合并；请在片库「管理版本组…」里调整。');
        else notifyError(`建立版本组失败：${versionGroupErrorText(err)}`);
      } finally {
        this.linking = false;
      }
    },
    async offerJoin(ids, conflict) {
      const rest = ids.filter(id => !conflict.videoIDs.includes(id));
      if (!rest.length) {
        notify('原片与成品已在同一个版本组里');
        return;
      }
      const ok = await confirmAction({
        title: '加入已有版本组',
        message: '其中一个视频已在一个版本组中。把另一个加入该组？',
        confirmText: '加入该组'
      });
      if (!ok) return;
      try {
        const group = await GetVersionGroup(conflict.groupIDs[0]);
        await AddVersionMembers(group.group_id, group.revision, rest);
        notifySuccess('已加入原有的版本组');
      } catch (err) {
        notifyError(`加入版本组失败：${versionGroupErrorText(err)}`);
      }
    }
  }
};
</script>
