export function confidenceMeta(confidence) {
  const value = String(confidence || '').toLowerCase();
  if (value === 'high') {
    return {
      label: '高置信',
      className: 'ai-confidence--high',
      rank: 0,
    };
  }
  if (value === 'medium') {
    return {
      label: '中置信',
      className: 'ai-confidence--medium',
      rank: 1,
    };
  }
  return {
    label: '未知',
    className: 'ai-confidence--unknown',
    rank: 2,
  };
}

export function groupCandidatesByVideo(candidates) {
  const groupsByKey = new Map();
  for (const candidate of Array.isArray(candidates) ? candidates : []) {
    const video = candidate?.video || {};
    const key = String(candidate?.video_id || video.id || 'unknown');
    if (!groupsByKey.has(key)) {
      groupsByKey.set(key, {
        videoId: candidate?.video_id || video.id || 0,
        videoName: video.name || `视频 #${candidate?.video_id || ''}`,
        videoPath: video.path || '',
        video,
        videoTags: Array.isArray(video.tags) ? video.tags : [],
        videoDeleted: Boolean(candidate?.video_deleted),
        candidates: [],
      });
    }
    const group = groupsByKey.get(key);
    group.videoDeleted = group.videoDeleted || Boolean(candidate?.video_deleted);
    group.candidates.push(candidate);
  }
  return Array.from(groupsByKey.values()).map(group => ({
    ...group,
    candidates: group.candidates.slice().sort((a, b) => confidenceMeta(a.confidence).rank - confidenceMeta(b.confidence).rank),
  }));
}

export function filterCandidatesForReview(candidates, searchTerm) {
  const keyword = String(searchTerm || '').trim().toLowerCase();
  const list = Array.isArray(candidates) ? candidates : [];
  if (!keyword) return list;
  return list.filter(candidate => {
    const video = candidate?.video || {};
    return [
      video.name,
      video.path,
      ...(Array.isArray(video.tags) ? video.tags.map(tag => tag?.name) : []),
      candidate?.suggested_name,
      candidate?.matched_tag?.name,
      candidate?.reasoning,
    ].some(value => String(value || '').toLowerCase().includes(keyword));
  });
}

export function removeCandidateById(candidates, candidateId) {
  const id = Number(candidateId);
  return (Array.isArray(candidates) ? candidates : []).filter(candidate => Number(candidate.id) !== id);
}

export function createRejectVideoConfirm(group) {
  if (!group?.videoId) return null;
  const candidates = Array.isArray(group.candidates) ? group.candidates : [];
  if (candidates.length === 0) return null;
  return {
    show: true,
    videoId: group.videoId,
    videoName: group.videoName || `视频 #${group.videoId}`,
    count: candidates.length,
    candidateIds: candidates.map(candidate => Number(candidate.id)),
  };
}

// 后端判"同名"用的是 normalized_name 这一列的相等比较（见 ApproveCandidate /
// ApproveImageAITagCandidate 的 SQL），所以这里也只看它：退回 suggested_name
// 会造出一条数据库没有的规则，两边对同一批候选的判断就会分叉。
function candidateTagKey(candidate) {
  return String(candidate?.normalized_name ?? '');
}

// 翻页追加：后端按 id DESC 发页，游标是上一页最后一条的 id。重复只可能来自
// 两次请求之间的并发写入，按 id 去重——同一条候选出现两次会让 Vue 的 :key 也撞上。
export function appendCandidates(existing, incoming) {
  const merged = Array.isArray(existing) ? [...existing] : [];
  const seen = new Set(merged.map(candidate => Number(candidate?.id)));
  for (const candidate of Array.isArray(incoming) ? incoming : []) {
    const id = Number(candidate?.id);
    if (seen.has(id)) continue;
    seen.add(id);
    merged.push(candidate);
  }
  return merged;
}

// 整媒体移除：拒绝某个视频/图片的全部待审候选，或重新分析把它们置 superseded 之后用。
export function removeCandidatesByMedia(candidates, mediaField, mediaId) {
  const id = Number(mediaId);
  return (Array.isArray(candidates) ? candidates : []).filter(candidate => Number(candidate?.[mediaField]) !== id);
}

// 接受一条候选后后端还会动别的行：同媒体同名的其他待审候选置 superseded；
// 该媒体已有手工标签时（返回 status=superseded）整媒体的待审候选一并作废。
// 前端按同一条规则局部移除，而不是整表重拉——分页之后重拉会把已加载的页全丢掉。
export function removeCandidatesAfterApproval(candidates, candidate, approvedItem, mediaField) {
  const list = Array.isArray(candidates) ? candidates : [];
  const mediaId = Number(candidate?.[mediaField]);
  if (String(approvedItem?.status || '') === 'superseded') {
    return removeCandidatesByMedia(list, mediaField, mediaId);
  }
  const approvedKey = candidateTagKey(candidate);
  return list.filter(item => {
    if (Number(item?.id) === Number(candidate?.id)) return false;
    if (Number(item?.[mediaField]) !== mediaId) return true;
    return candidateTagKey(item) !== approvedKey;
  });
}
