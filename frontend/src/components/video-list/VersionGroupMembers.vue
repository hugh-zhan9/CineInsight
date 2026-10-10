<template>
  <div class="version-members" data-test="version-members" @click.stop>
    <div class="version-members__head">
      <span class="version-members__title">{{ groupTitle }}</span>
      <span class="version-members__hint">各版本的已看、进度、评分独立保存</span>
      <button type="button" class="version-members__manage" data-test="version-members-manage" @click="$emit('manage')">管理版本组…</button>
    </div>
    <ol class="version-members__list">
      <li
        v-for="(member, index) in summary.members"
        :key="member.video_id"
        :class="['version-member', { 'version-member--current': Number(member.video_id) === Number(currentVideoId) }]"
        :data-test="`version-member-${member.video_id}`"
      >
        <div class="version-member__info">
          <div class="version-member__name">
            <span class="version-member__label">{{ member.label || `版本 ${index + 1}` }}</span>
            <span v-if="index === 0" class="version-member__tag">主版本</span>
            <span v-if="member.is_stale" class="version-member__tag version-member__tag--danger">路径失效</span>
            <span class="version-member__file" :title="member.name">{{ title(member) }}</span>
          </div>
          <div class="version-member__facts">{{ facts(member) }}</div>
        </div>
        <div class="version-member__actions">
          <button type="button" class="row-btn row-btn--primary" :disabled="busy" data-test="version-member-play" @click="$emit('play', member.video_id)">播放</button>
          <button type="button" class="row-btn" :disabled="busy" data-test="version-member-preview" @click="$emit('preview', member.video_id)">预览</button>
          <button v-if="index > 0" type="button" class="row-btn" :disabled="busy" data-test="version-member-primary" @click="$emit('set-primary', member.video_id)">设为主版本</button>
          <button type="button" class="row-btn" :disabled="busy" data-test="version-member-remove" @click="$emit('remove', member.video_id)">移出组</button>
        </div>
      </li>
    </ol>
  </div>
</template>

<script>
import { versionGroupTitle, versionMemberFacts, versionMemberTitle } from '../../utils/versionGroups.js';

// 卡片内展开的版本列表（D-MW-VERSIONS）。每个版本都按自己的视频 ID 播放与预览；
// 写操作只发事件，由片库页调用后端并按现有原地刷新重取。
export default {
  name: 'VersionGroupMembers',
  props: {
    summary: { type: Object, required: true },
    currentVideoId: { type: [Number, String], default: 0 },
    busy: { type: Boolean, default: false }
  },
  emits: ['play', 'preview', 'set-primary', 'remove', 'manage'],
  computed: {
    groupTitle() {
      return versionGroupTitle(this.summary);
    }
  },
  methods: {
    title(member) {
      return versionMemberTitle(member);
    },
    facts(member) {
      return versionMemberFacts(member);
    }
  }
};
</script>

<style scoped>
.version-members {
  margin: 4px 0 8px;
  padding: 8px 12px;
  border: 1px solid var(--border-color);
  border-radius: 8px;
  background: var(--panel-subtle-bg);
  font-size: 12px;
}
.version-members__head { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; flex-wrap: wrap; }
.version-members__title { font-weight: 650; }
.version-members__hint { color: var(--text-muted); flex: 1; min-width: 0; }
.version-members__list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.version-member { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 6px 0; border-top: 1px solid var(--border-color); }
.version-member:first-child { border-top: none; }
.version-member--current .version-member__label { color: var(--accent-text); }
.version-member__info { flex: 1; min-width: 0; }
.version-member__name { display: flex; align-items: center; gap: 6px; min-width: 0; }
.version-member__label { font-weight: 650; white-space: nowrap; }
.version-member__tag { padding: 0 6px; border-radius: 4px; background: var(--accent-soft); color: var(--accent-text); font-size: 11px; white-space: nowrap; }
.version-member__tag--danger { background: var(--danger-soft); color: var(--danger-text); }
.version-member__file { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); }
.version-member__facts { color: var(--text-muted); margin-top: 2px; }
.version-member__actions { display: flex; gap: 6px; flex-wrap: wrap; }
.version-members__manage { flex: none; border: 0; background: transparent; color: var(--accent-text); font-size: 12px; cursor: pointer; padding: 0; }
</style>
